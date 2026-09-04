package varlen

import (
	"bytes"
	"testing"
)

func TestRead(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		value int
		size  int
		ok    bool
	}{
		{"zero", []byte{0x80}, 0, 1, true},
		{"one", []byte{0x81}, 1, 1, true},
		{"127", []byte{0xFF}, 127, 1, true},
		{"128", []byte{0x01, 0x80}, 128, 2, true},
		{"16383", []byte{0x7F, 0xFF}, 16383, 2, true},
		{"16384", []byte{0x01, 0x00, 0x80}, 16384, 3, true},
		{"65536", []byte{0x04, 0x00, 0x80}, 65536, 3, true},
		{"large", []byte{0x7F, 0x7F, 0x7F, 0x7F, 0x7F, 0x7F, 0x7F, 0x7F, 0xFF}, 1<<63 - 1, 9, true},
		{"multi-digit", []byte{0x00, 0x00, 0x00, 0x11, 0x22, 0x33, 0x80}, 0x11<<21 | 0x22<<14 | 0x33<<7, 7, true},
		{"unterminated", []byte{0x01, 0x02}, 1<<7 | 2, 2, false},
		{"empty", nil, 0, 0, false},
		{"overlong", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x80}, 73196826878133257, 9, false},
	}
	for _, tt := range tests {
		v, n, ok := Read(tt.bytes, 0)
		if v != tt.value || n != tt.size || ok != tt.ok {
			t.Errorf("%s: Read = (%d, %d, %v), want (%d, %d, %v)",
				tt.name, v, n, ok, tt.value, tt.size, tt.ok)
		}
	}
}

func TestReadOffset(t *testing.T) {
	b := []byte{0xAA, 0x81, 0xFF}
	v, n, ok := Read(b, 1)
	if v != 1 || n != 1 || !ok {
		t.Fatalf("Read at offset = (%d, %d, %v), want (1, 1, true)", v, n, ok)
	}
	// Starting at the last byte: value only, terminator reached.
	v, n, ok = Read(b, 2)
	if v != 127 || n != 1 || !ok {
		t.Fatalf("Read at 2 = (%d, %d, %v), want (127, 1, true)", v, n, ok)
	}
	// Starting past a terminator reads the remainder as a fresh value.
	if _, _, ok := Read(b, 3); ok {
		t.Fatal("Read past the end = ok, want false")
	}
}

func TestFromEnd(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		want  int
	}{
		{"single", []byte{0x00, 0x00, 0x00, 0x85}, 5},
		{"two bytes", []byte{0x00, 0x00, 0x81, 0x00}, 128},
		{"reset midstream", []byte{0x00, 0x83, 0x01, 0x00}, 3<<14 | 1<<7},
		{"four bytes", []byte{0x81, 0x00, 0x00, 0x00}, 1 << 21},
		{"empty", nil, 0},
		{"short", []byte{0x82}, 2},
	}
	for _, tt := range tests {
		if got := FromEnd(tt.bytes); got != tt.want {
			t.Errorf("%s: FromEnd = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestAppendReadRoundTrip(t *testing.T) {
	values := []int{0, 1, 127, 128, 16383, 16384, 65535, 65536, 1 << 20, 0x11223344, 1<<63 - 1}
	for _, v := range values {
		b := Append(nil, v)
		if got, n, ok := Read(b, 0); !ok || got != v || n != len(b) {
			t.Errorf("Append(%d) → Read = (%d, %d, %v), want (%d, %d, true)", v, got, n, ok, v, len(b))
		}
	}
}

func TestAppendForm(t *testing.T) {
	// 65536 = groups (4, 0, 0) → 0x04 0x00 0x80.
	if b := Append(nil, 65536); !bytes.Equal(b, []byte{0x04, 0x00, 0x80}) {
		t.Fatalf("Append(65536) = % X, want 04 00 80", b)
	}
	if b := Append(nil, 0); !bytes.Equal(b, []byte{0x80}) {
		t.Fatalf("Append(0) = % X, want 80", b)
	}
	// Fixture sanity: u16-sized quantities round-trip.
	for v := 0; v < 0x10000; v += 257 {
		b := Append(nil, v)
		if got, _, ok := Read(b, 0); !ok || got != v {
			t.Fatalf("round trip %d failed", v)
		}
	}
}
