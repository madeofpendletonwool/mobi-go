package mobi

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

func TestDecompressPalmDOCLiterals(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", nil, nil},
		{"zero literal", []byte{0x00}, []byte{0x00}},
		{"self literal 0x09", []byte{0x09}, []byte{0x09}},
		{"self literal A", []byte{'A'}, []byte{'A'}},
		{"self literal 0x7F", []byte{0x7F}, []byte{0x7F}},
		{"run of 1", []byte{0x01, 0x80}, []byte{0x80}},
		{"run of 3", []byte{0x03, 'a', 'b', 'c'}, []byte("abc")},
		{"run of 8", []byte{0x08, 1, 2, 3, 4, 5, 6, 7, 8}, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{"space plus char 0xC1", []byte{0xC1}, []byte{0x20, 0x41}},
		{"space plus char 0xC0", []byte{0xC0}, []byte{0x20, 0x40}},
		{"space plus char 0xFF", []byte{0xFF}, []byte{0x20, 0x7F}},
		{"mixed stream", []byte{0x02, 0x80, 0x81, 'x', 0xE9}, []byte{0x80, 0x81, 'x', 0x20, 0x69}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decompressPalmDOC(tt.in)
			if err != nil {
				t.Fatalf("decompressPalmDOC: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("decompressPalmDOC = %x, want %x", got, tt.want)
			}
		})
	}
}

// pair builds the 2-byte encoding of a back-reference with the given
// distance and length (3–10).
func pair(dist, length int) []byte {
	p := uint16(dist<<3 | (length - 3))
	return []byte{byte(0x80 | p>>8), byte(p)}
}

func TestDecompressPalmDOCBackReferences(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{
			"plain copy dist 4 len 3",
			append([]byte("ABCD"), pair(4, 3)...),
			"ABCDABC",
		},
		{
			"copy dist 1 len 3",
			append([]byte("A"), pair(1, 3)...),
			"AAAA",
		},
		{
			"overlap dist 1 len 10",
			append([]byte("A"), pair(1, 10)...),
			"A" + "AAAAAAAAAA",
		},
		{
			"overlap dist 2 len 7",
			append([]byte("AB"), pair(2, 7)...),
			"ABABABABA",
		},
		{
			"overlap dist 3 len 10",
			append([]byte("ABC"), pair(3, 10)...),
			"ABCABCABCABCA",
		},
		{
			"max distance 2047",
			append(bytes.Repeat([]byte{'z'}, 2047), pair(2047, 3)...),
			string(bytes.Repeat([]byte{'z'}, 2050)),
		},
		{
			"chained pairs",
			append(append([]byte("ab"), pair(2, 4)...), pair(2, 4)...),
			"ababababab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decompressPalmDOC(tt.in)
			if err != nil {
				t.Fatalf("decompressPalmDOC: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("decompressPalmDOC = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecompressPalmDOCCorrupt(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"truncated run", []byte{0x05, 'a', 'b'}},
		{"truncated run of 1", []byte{0x01}},
		{"pair missing second byte", []byte{'A', 0x80}},
		{"pair missing second byte at end", []byte{0xBF}},
		{"distance zero", []byte{'A', 0x80, 0x00}},
		{"distance beyond output", []byte{'A', 0x80, 0xF8}},
		{"distance beyond empty output", pair(1, 3)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := decompressPalmDOC(tt.in)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("decompressPalmDOC(%x) = (%x, %v), want ErrCorrupt", tt.in, out, err)
			}
		})
	}
}

func TestCompressPalmDOCRoundTrip(t *testing.T) {
	var allBytes []byte
	for i := range 256 {
		allBytes = append(allBytes, byte(i))
	}
	tests := []struct {
		name string
		src  []byte
		// pairs: the compressed stream must contain at least one
		// back-reference opcode byte.
		pairs bool
	}{
		{"empty", nil, false},
		{"plain ascii", []byte("Hello, Mobipocket!"), false},
		{"with 0x00 and control bytes", []byte("a\x00b\x01c\x08d"), false},
		{"high bytes", []byte("\x80\x81\xFE\xFF and text"), false},
		{"all 256 byte values", allBytes, false},
		{"short run", []byte("abbb"), false},
		{"run of 4", []byte("abbbb"), true},
		{"run of 10", []byte("a" + "bbbbbbbbbb" + "c"), true},
		{"run of 25", []byte(strings.Repeat("a", 25)), true},
		{"run of 37", []byte(strings.Repeat("z", 37)), true},
		{"repeated block", []byte(strings.Repeat("abcabcabc", 40)), false},
		{"long mixed", []byte(strings.Repeat("The quick \xB0rown fox\x00 ", 90)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed := testutil.CompressPalmDOC(tt.src)
			if tt.pairs && !bytes.Contains(compressed, []byte{0x80}) {
				t.Errorf("compressor emitted no back-reference for %q: %x", tt.src, compressed)
			}
			got, err := decompressPalmDOC(compressed)
			if err != nil {
				t.Fatalf("decompressPalmDOC(compressed): %v", err)
			}
			if !bytes.Equal(got, tt.src) {
				t.Errorf("round trip = %x, want %x", got, tt.src)
			}
		})
	}
}

func FuzzPalmDOC(f *testing.F) {
	f.Add([]byte{0x03, 'a', 'b', 'c'})
	f.Add([]byte{0x08, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{'A', 0x80, 0x0F})
	f.Add(append([]byte("AB"), pair(2, 7)...))
	f.Add([]byte{0xC1, 0x00, 0x7F})
	f.Add([]byte{0x05, 'a'})
	f.Add([]byte{'A', 0x80, 0xF8})
	f.Add(testutil.CompressPalmDOC([]byte("Hello, Mobipocket!")))
	f.Add(testutil.CompressPalmDOC([]byte(strings.Repeat("aaaa", 20))))
	f.Add(testutil.CompressPalmDOC([]byte{0x00, 0x01, 0x08, 0x7F, 0x80, 0xFF}))
	f.Fuzz(func(t *testing.T, src []byte) {
		out, err := decompressPalmDOC(src)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("error %v does not wrap ErrCorrupt", err)
			}
			return
		}
		// Every opcode expands its input by at most 5x (a 2-byte pair
		// emits up to 10 bytes), so no valid stream can beat that.
		if len(out) > 5*len(src) {
			t.Errorf("%d-byte input expanded to %d bytes", len(src), len(out))
		}
	})
}
