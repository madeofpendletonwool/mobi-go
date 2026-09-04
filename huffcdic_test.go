package mobi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// put32be and put16be write big-endian integers at off, for hand-built
// records.
func put32be(b []byte, off int, v uint32) {
	binary.BigEndian.PutUint32(b[off:], v)
}

func put16be(b []byte, off int, v uint16) {
	binary.BigEndian.PutUint16(b[off:], v)
}

// handHUFF assembles a HUFF record from ready-made tables: table1 is
// 1024 bytes of packed entries, table2 one (min, max) pair per code
// length 1..32. Mirrors the layout real files use (tables at offsets
// 24 and 1048).
func handHUFF(table1 []byte, table2 [][2]uint32) []byte {
	rec := make([]byte, 24+1024+8*len(table2))
	copy(rec, "HUFF")
	put32be(rec, 8, 24)
	put32be(rec, 12, 24+1024)
	copy(rec[24:], table1)
	for length, pair := range table2 { // length 0 is slot 1
		put32be(rec, 1048+8*length, pair[0])
		put32be(rec, 1048+8*length+4, pair[1])
	}
	return rec
}

// handCDIC assembles a CDIC record from phrases; indices in raw are
// stored without the already-decompressed flag (their bytes are
// themselves compressed data).
func handCDIC(phrases [][]byte, raw map[int]bool) []byte {
	n := len(phrases)
	area := 2 * n
	for _, p := range phrases {
		area += 2 + len(p)
	}
	codeLength := uint32(0)
	for 1<<codeLength < n {
		codeLength++
	}
	rec := make([]byte, 16+area)
	copy(rec, "CDIC")
	put32be(rec, 4, 16)
	put32be(rec, 8, uint32(n))
	put32be(rec, 12, codeLength)
	off := 2 * n
	for i, p := range phrases {
		put16be(rec, 16+2*i, uint16(off))
		x := uint16(len(p))
		if !raw[i] {
			x |= 0x8000
		}
		put16be(rec, 16+off, x)
		copy(rec[16+off+2:], p)
		off += 2 + len(p)
	}
	return rec
}

// bitStream is a tiny MSB-first bit writer for hand-computed streams.
type bitStream struct {
	out  []byte
	acc  uint32
	nbit uint
}

func (s *bitStream) emit(code uint32, length uint) {
	s.acc = s.acc<<length | code
	s.nbit += length
	for s.nbit >= 8 {
		s.nbit -= 8
		s.out = append(s.out, byte(s.acc>>s.nbit))
	}
}

// TestHuffCDICUniformShortCodes decodes against a fully hand-computed
// table: eight phrases under uniform 4-bit terminal codes, where nibble
// n is phrase n via table1[b] = terminal, codelen 4, value 2*(b>>4) —
// the phrase index is value - code = b>>4. table2 is never consulted.
func TestHuffCDICUniformShortCodes(t *testing.T) {
	phrases := [][]byte{
		[]byte("a"), []byte("b"), []byte("c"), []byte("d"),
		[]byte("e"), []byte("f"), []byte("g"), []byte("h"),
	}
	table1 := make([]byte, 1024)
	for b := range 256 {
		put32be(table1, 4*b, 0x80|4|uint32(2*(b>>4))<<8)
	}
	decomp, err := newHuffCDIC(handHUFF(table1, make([][2]uint32, 32)), [][]byte{handCDIC(phrases, nil)})
	if err != nil {
		t.Fatalf("newHuffCDIC: %v", err)
	}

	got, err := decomp([]byte{0x31, 0x41, 0x52, 0x65}) // nibbles 3,1,4,1,5,2,6,5
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != "dbebfcgf" {
		t.Errorf("decompress = %q, want %q", got, "dbebfcgf")
	}

	// An odd nibble count leaves a trailing zero nibble that is itself
	// a code and decodes as phrase 0 — the decode-the-padding behavior
	// both port sources share (real files' codes are long enough that
	// padding never decodes).
	got, err = decomp([]byte{0x72, 0x60}) // nibbles 7,2,6,pad
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != "hcga" {
		t.Errorf("decompress with pad nibble = %q, want %q", got, "hcga")
	}
}

// mixedHandFixture builds the hand-computed mixed-length codec: four
// 8-bit terminal phrases and four 10-bit walk phrases (code 1023-i for
// global index i), one of which is nested — phrase 0's bytes encode the
// index stream [1, 2] (both short codes, bytes 0x01 0x02). table2 walks
// 8 -> 9 -> 10 via min codes 256, 512, and 1024-8, with max 1023 at
// length 10.
func mixedHandFixture() (huff, cdic, stream []byte, want string) {
	phrases := [][]byte{
		[]byte{0x01, 0x02}, // phrase 0: compressed form of indices [1, 2]
		[]byte("quick "), []byte("brown "), []byte("fox "),
		[]byte("jumps "), []byte("over "), []byte("lazy "), []byte("The "),
	}
	table1 := make([]byte, 1024)
	for b := range 256 {
		x := uint32(8) // nonterminal, codelen 8: walk from here
		if b < 4 {
			x = 0x80 | 8 | uint32(2*b)<<8 // terminal: index = 2b - b
		}
		put32be(table1, 4*b, x)
	}
	table2 := make([][2]uint32, 32)
	table2[7] = [2]uint32{256, 0}         // length 8: every prefix walks
	table2[8] = [2]uint32{512, 0}         // length 9: ditto
	table2[9] = [2]uint32{1024 - 8, 1023} // length 10: the long codes
	huff = handHUFF(table1, table2)
	cdic = handCDIC(phrases, map[int]bool{0: true})

	var s bitStream
	for _, i := range []int{7, 4, 0, 3, 6, 2, 1} {
		if i < 4 {
			s.emit(uint32(i), 8)
		} else {
			s.emit(1023-uint32(i), 10)
		}
	}
	if s.nbit > 0 {
		s.out = append(s.out, byte(s.acc<<(8-s.nbit)))
	}
	stream = s.out
	want = "The jumps quick brown fox lazy brown quick "
	return
}

// TestHuffCDICHandCodedMixed asserts phrase-for-phrase decoding on the
// hand-computed mixed-length fixture, covering the table1 terminal
// path, the table2 walk, the reversed code-to-index mapping, and nested
// phrase expansion with memoization. It also cross-validates the
// testutil fixture builder against the hand-built records.
func TestHuffCDICHandCodedMixed(t *testing.T) {
	huff, cdic, stream, want := mixedHandFixture()

	// Spot-check the hand-computed CDIC layout: header fields, first
	// offset, the nested phrase's flag-free header (0x0002), and the
	// decompressed flags on the phrases after it.
	if string(cdic[:4]) != "CDIC" || be32(cdic, 4) != 16 || be32(cdic, 8) != 8 || be32(cdic, 12) != 3 {
		t.Fatalf("CDIC header = %x", cdic[:16])
	}
	if be16(cdic, 16) != 16 || be16(cdic, 32) != 0x0002 || be16(cdic, 36) != 0x8006 || be16(cdic, 80) != 0x8004 {
		t.Fatalf("CDIC layout wrong: first offset %d, phrase 0 x %#04x, phrase 1 x %#04x, phrase 7 x %#04x",
			be16(cdic, 16), be16(cdic, 32), be16(cdic, 36), be16(cdic, 80))
	}

	decomp, err := newHuffCDIC(huff, [][]byte{cdic})
	if err != nil {
		t.Fatalf("newHuffCDIC: %v", err)
	}
	got, err := decomp(stream)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != want {
		t.Errorf("decompress = %q, want %q", got, want)
	}

	// The dictionary memoizes phrase 0's expansion; decoding again
	// (and decoding a stream that repeats it) must agree.
	again, err := decomp(stream)
	if err != nil || !bytes.Equal(again, got) {
		t.Errorf("second decode = (%q, %v), want the same bytes", again, err)
	}

	// The testutil builder must produce byte-identical records from
	// the same specification — the property tests lean on it.
	fix := testutil.NewHuffCDIC([][]byte{
		[]byte("placeholder"), []byte("quick "), []byte("brown "), []byte("fox "),
		[]byte("jumps "), []byte("over "), []byte("lazy "), []byte("The "),
	}, 4, map[int][]int{0: {1, 2}})
	if !bytes.Equal(fix.HUFFRecord(), huff) {
		t.Error("testutil HUFF record differs from the hand-computed table")
	}
	if fixtureCDICs := fix.CDICRecords(0); !bytes.Equal(fixtureCDICs[0], cdic) {
		t.Error("testutil CDIC record differs from the hand-computed record")
	}
	if !bytes.Equal(fix.Encode([]int{7, 4, 0, 3, 6, 2, 1}), stream) {
		t.Error("testutil Encode differs from the hand-computed stream")
	}
	if string(fix.Expand([]int{7, 4, 0, 3, 6, 2, 1})) != want {
		t.Error("testutil Expand differs from the expected text")
	}
}

// TestHuffCDICProperty round-trips random dictionaries and index
// streams through the decoder: the fixture encodes, the decoder must
// reproduce the fixture's expansion exactly. Randomized CDIC chunking
// exercises the global-numEntries arithmetic; randomized nesting builds
// acyclic phrase DAGs; two sequential streams pin the memoization
// behavior across records.
func TestHuffCDICProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := range 60 {
		d := 2 + rng.Intn(299)
		short := rng.Intn(min(d, 180) + 1)
		phrases := make([][]byte, d)
		for i := range phrases {
			p := make([]byte, rng.Intn(49))
			for k := range p {
				p[k] = byte(rng.Intn(256))
			}
			phrases[i] = p
		}
		nested := map[int][]int{}
		for i := 0; i+1 < d; i++ {
			if rng.Intn(6) == 0 {
				sub := make([]int, 1+rng.Intn(6))
				for j := range sub {
					sub[j] = i + 1 + rng.Intn(d-1-i)
				}
				nested[i] = sub
			}
		}
		fix := testutil.NewHuffCDIC(phrases, short, nested)

		chunk := []int{1, 2, 7, 64, 0}[rng.Intn(5)]
		decomp, err := newHuffCDIC(fix.HUFFRecord(), fix.CDICRecords(chunk))
		if err != nil {
			t.Fatalf("iter %d: newHuffCDIC: %v", iter, err)
		}

		stream := make([]int, rng.Intn(4001))
		for j := range stream {
			stream[j] = rng.Intn(d)
		}
		want := fix.Expand(stream)
		got, err := decomp(fix.Encode(stream))
		if err != nil {
			t.Fatalf("iter %d: decompress: %v", iter, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("iter %d: decompress = %d bytes, want %d (chunk %d, %d phrases, %d short)",
				iter, len(got), len(want), chunk, d, short)
		}

		// A second stream through the same (now partially memoized)
		// decoder, then the first stream again: both must be exact.
		stream2 := make([]int, rng.Intn(2001))
		for j := range stream2 {
			stream2[j] = rng.Intn(d)
		}
		got2, err := decomp(fix.Encode(stream2))
		if err != nil || !bytes.Equal(got2, fix.Expand(stream2)) {
			t.Fatalf("iter %d: second stream = (%d bytes, %v)", iter, len(got2), err)
		}
		got3, err := decomp(fix.Encode(stream))
		if err != nil || !bytes.Equal(got3, want) {
			t.Fatalf("iter %d: repeat of the first stream = (%d bytes, %v)", iter, len(got3), err)
		}
	}
}

// TestHuffCDICCorrupt feeds damaged HUFF/CDIC records and streams to
// the decoder; every case must wrap ErrCorrupt, never panic.
func TestHuffCDICCorrupt(t *testing.T) {
	huff, cdic, stream, _ := mixedHandFixture()
	clone := func(b []byte) []byte { return append([]byte(nil), b...) }
	one := func(c []byte) [][]byte { return [][]byte{c} }

	tests := []struct {
		name   string
		mutate func(h, c, s []byte) ([]byte, [][]byte, []byte)
	}{
		{"bad HUFF magic", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			h[0] = 'X'
			return h, one(c), s
		}},
		{"HUFF record truncated", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			return h[:15], one(c), s
		}},
		{"table1 offset past record", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			put32be(h, 8, uint32(len(h)))
			return h, one(c), s
		}},
		{"table2 offset past record", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			put32be(h, 12, uint32(len(h)))
			return h, one(c), s
		}},
		{"bad CDIC magic", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			c[0] = 'X'
			return h, one(c), s
		}},
		{"CDIC record truncated", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			return h, one(c[:15]), s
		}},
		{"CDIC entry area past record", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			put32be(c, 4, uint32(len(c)))
			return h, one(c), s
		}},
		{"CDIC phrase runs past record", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			return h, one(c[:len(c)-3]), s
		}},
		{"CDIC count regression", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			put32be(c, 8, 4) // total 4 after 8 already loaded
			return h, [][]byte{clone(cdic), c}, s
		}},
		{"no CDIC records at all", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			return h, nil, s
		}},
		{"zero code length", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			put32be(h, 24, 0) // table1 slot for byte 0x00, which the stream starts with
			return h, one(c), s
		}},
		{"code length walk past 32", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			for length := 1; length <= 32; length++ {
				put32be(h, 1048+(length-1)*8, 0xFFFFFFFF)
			}
			return h, one(c), []byte{0x04, 0x00, 0x00, 0x00}
		}},
		{"dictionary overrun", func(h, c, s []byte) ([]byte, [][]byte, []byte) {
			// 0xFB00... walks to a length whose codes lie below the
			// dictionary's range: the phrase index underflows.
			return h, one(c), []byte{0xFB, 0x00}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cdics, s := tt.mutate(clone(huff), clone(cdic), clone(stream))
			decomp, err := newHuffCDIC(h, cdics)
			if err == nil {
				_, err = decomp(s)
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// TestHuffCDICCyclicPhrase builds a one-phrase dictionary whose only
// phrase is its own code: expansion must re-enter the phrase and
// report it as corrupt rather than recurse forever.
func TestHuffCDICCyclicPhrase(t *testing.T) {
	table1 := make([]byte, 1024)
	for b := range 256 {
		put32be(table1, 4*b, 0x80|8) // terminal, codelen 8, value 0: byte b decodes phrase b-0... index 0 for byte 0
	}
	huff := handHUFF(table1, make([][2]uint32, 32))
	cdic := handCDIC([][]byte{{0x00}}, map[int]bool{0: true}) // phrase 0 = byte 0 = its own code
	decomp, err := newHuffCDIC(huff, [][]byte{cdic})
	if err != nil {
		t.Fatalf("newHuffCDIC: %v", err)
	}
	if _, err := decomp([]byte{0x00}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("cyclic phrase: error = %v, want ErrCorrupt", err)
	}
}

// TestHuffCDICTruncatedStreams truncates a valid stream at every byte:
// the decoder must never panic, must report only ErrCorrupt, and —
// because the fixture's code is prefix-free and every code outruns the
// zero padding — a successful decode is always a prefix of the full
// expansion. That is the silent-stop truncation semantics both port
// sources share.
func TestHuffCDICTruncatedStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	phrases := make([][]byte, 20)
	for i := range phrases {
		phrases[i] = []byte(fmt.Sprintf("phrase-%d ", i))
	}
	fix := testutil.NewHuffCDIC(phrases, 8, nil)
	decomp, err := newHuffCDIC(fix.HUFFRecord(), fix.CDICRecords(9))
	if err != nil {
		t.Fatalf("newHuffCDIC: %v", err)
	}
	stream := make([]int, 500)
	for i := range stream {
		stream[i] = rng.Intn(len(phrases))
	}
	full := fix.Encode(stream)
	want := fix.Expand(stream)
	for cut := 0; cut <= len(full); cut++ {
		got, err := decomp(full[:cut])
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("cut %d: error = %v, want ErrCorrupt", cut, err)
			}
			continue
		}
		if !bytes.HasPrefix(want, got) {
			t.Fatalf("cut %d: decode = %q, not a prefix of the full expansion", cut, got)
		}
	}
}

// TestHuffCDICThroughBook drives the full Open path: a version-6 book
// with compression 17480, HUFF/CDIC dictionary records after the text
// records, and — in one variant — trailing-entry bookkeeping on every
// text record, proving the strip runs before HUFF/CDIC decompression.
func TestHuffCDICThroughBook(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	phrases := make([][]byte, 24)
	for i := range phrases {
		phrases[i] = []byte(fmt.Sprintf(`<p class="c%d">HUFF/CDIC phrase %d of the synthetic text.</p>`, i%5, i))
	}
	fix := testutil.NewHuffCDIC(phrases, 8, nil)

	var streams [][]int
	var want []byte
	for len(want) < 12000 {
		var rec []int
		size := 0
		for size < 4096 {
			i := rng.Intn(len(phrases))
			rec = append(rec, i)
			size += len(phrases[i])
		}
		streams = append(streams, rec)
		want = append(want, fix.Expand(rec)...)
	}
	huffRec := fix.HUFFRecord()
	cdicRecs := fix.CDICRecords(10)

	for _, flags := range []uint32{0, 3} {
		t.Run(fmt.Sprintf("trailing-flags-%d", flags), func(t *testing.T) {
			rec0 := testutil.BuildRecord0(testutil.Record0Config{
				Compression:    17480,
				TextLength:     uint32(len(want)),
				NumTextRecords: uint16(len(streams)),
				TrailingFlags:  flags,
				Huffcdic:       testutil.U32(uint32(1 + len(streams))),
				NumHuffcdic:    uint32(1 + len(cdicRecs)),
				Title:          "Huffed",
			})
			records := [][]byte{rec0}
			for _, stream := range streams {
				records = append(records, testutil.AppendTrailingEntries(fix.Encode(stream), flags))
			}
			records = append(records, huffRec)
			records = append(records, cdicRecs...)
			b := openBookOK(t, testutil.Build(records...))
			if !bytes.Equal(b.RawText(), want) {
				t.Errorf("RawText = %d bytes, want %d", len(b.RawText()), len(want))
			}
			if b.Text() != string(want) {
				t.Errorf("Text differs from the expected expansion")
			}
		})
	}
}

// TestHuffCDICDictionaryRangeErrors covers the Book-level wiring
// failures: dictionary records past the end of the file, and a huffcdic
// index that points at a non-HUFF record.
func TestHuffCDICDictionaryRangeErrors(t *testing.T) {
	fix := testutil.NewHuffCDIC([][]byte{[]byte("alpha "), []byte("beta ")}, 1, nil)
	stream := fix.Encode([]int{0, 1, 1, 0})
	build := func(huffcdic uint32, num uint32, insertRecords [][]byte) []byte {
		rec0 := testutil.BuildRecord0(testutil.Record0Config{
			Compression:    17480,
			TextLength:     uint32(len("alpha beta beta alpha ")),
			NumTextRecords: 1,
			Huffcdic:       testutil.U32(huffcdic),
			NumHuffcdic:    num,
		})
		records := [][]byte{rec0, stream}
		records = append(records, insertRecords...)
		return testutil.Build(records...)
	}

	// numHuffcdic runs past the last record.
	_, err := openBook(t, build(2, 0xFFFF, [][]byte{fix.HUFFRecord(), fix.CDICRecords(0)[0]}))
	if !errors.Is(err, ErrRecordRange) {
		t.Errorf("dictionary past EOF: error = %v, want ErrRecordRange", err)
	}

	// The huffcdic index points at the text record, not a HUFF record.
	_, err = openBook(t, build(1, 2, [][]byte{fix.HUFFRecord(), fix.CDICRecords(0)[0]}))
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("huffcdic on a text record: error = %v, want ErrCorrupt", err)
	}
}

func BenchmarkHuffCDIC(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	phrases := make([][]byte, 256)
	for i := range phrases {
		phrases[i] = []byte(fmt.Sprintf(`<p class="c%d">sentence %d of the synthetic benchmark text.</p>`, i%9, i))
	}
	fix := testutil.NewHuffCDIC(phrases, 64, nil)
	decomp, err := newHuffCDIC(fix.HUFFRecord(), fix.CDICRecords(256))
	if err != nil {
		b.Fatalf("newHuffCDIC: %v", err)
	}
	var streams [][]byte
	var total int64
	for total < 500_000 {
		var rec []int
		size := 0
		for size < 4096 {
			i := rng.Intn(256)
			rec = append(rec, i)
			size += len(phrases[i])
		}
		streams = append(streams, fix.Encode(rec))
		total += int64(size)
	}
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, s := range streams {
			if _, err := decomp(s); err != nil {
				b.Fatalf("decompress: %v", err)
			}
		}
	}
}

func FuzzHuffCDIC(f *testing.F) {
	huff, cdic, stream, _ := mixedHandFixture()
	f.Add(huff, cdic, stream)
	f.Add(huff[:24], cdic, []byte{})
	f.Add(huff, cdic, []byte{0x00})
	f.Add(huff, cdic, []byte{0xFB, 0x00})
	f.Fuzz(func(t *testing.T, huff, cdic, data []byte) {
		decomp, err := newHuffCDIC(huff, [][]byte{cdic})
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("parse error %v does not wrap ErrCorrupt", err)
			}
			return
		}
		out1, err1 := decomp(data)
		if err1 != nil {
			if !errors.Is(err1, ErrCorrupt) {
				t.Errorf("decode error %v does not wrap ErrCorrupt", err1)
			}
			return
		}
		// Decoding is deterministic even with the memoizing
		// dictionary mutating underneath.
		out2, err2 := decomp(data)
		if err2 != nil || !bytes.Equal(out1, out2) {
			t.Errorf("second decode = (%d bytes, %v), want (%d bytes, nil)", len(out2), err2, len(out1))
		}
	})
}
