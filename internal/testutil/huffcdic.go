// HUFF/CDIC fixture builder: a test-side encoder whose code layout
// exercises both lookup paths of the decoder — terminal table1 entries
// and the table2 code-length walk.
//
// Coding scheme (a valid instance of the on-disk format, chosen to be
// hand-verifiable):
//
//   - Phrases 0..A-1 ("short") use 8-bit terminal codes 0..A-1. Byte b
//     decodes via table1[b] = terminal, codelen 8, value 2b, so the
//     phrase index is value - code = b.
//   - Phrases A..D-1 ("long", D the dictionary size) use 10-bit codes
//     1023-i for global index i, reached through the table2 walk: every
//     table1 entry at or above A is nonterminal with codelen 8, and
//     table2's minimum codes force the walk 8 -> 9 -> 10, where the
//     maximum code 1023 maps index i = 1023 - code.
//
// Long codes occupy the top of the 10-bit space, so their 8-bit
// prefixes never collide with the short codes' bytes and the scheme is
// prefix-free. Every code is at least 8 bits, so zero padding after
// the last code always overruns and padded streams decode exactly.
//
// Constraints (panics from NewHuffCDIC mean a test bug, not an input
// class): 1 <= D <= 1024, 0 <= A <= D, and when long codes exist their
// lowest 8-bit prefix must clear A: A <= (1024-D)/4.

package testutil

import (
	"encoding/binary"
	"fmt"
)

// HuffCDIC is a fixed HUFF/CDIC codec fixture: a dictionary, the code
// assignment above, and (optionally) nested phrases whose bytes are
// themselves encoded index streams.
type HuffCDIC struct {
	phrases    [][]byte // dictionary order; nested slots hold their encoded streams
	nested     map[int][]int
	shortCount int
}

// NewHuffCDIC builds a fixture over phrases. shortCount phrases get
// 8-bit terminal codes, the rest 10-bit walk codes. nested maps a
// phrase index to the index stream its compressed bytes encode; that
// stream may only reference phrases with strictly greater indices, so
// expansions stay acyclic. The phrases[i] entry is ignored (replaced
// by the encoded stream) for every i in nested.
func NewHuffCDIC(phrases [][]byte, shortCount int, nested map[int][]int) *HuffCDIC {
	d := len(phrases)
	if d == 0 || d > 1024 {
		panic(fmt.Sprintf("testutil: dictionary of %d phrases, want 1..1024", d))
	}
	if shortCount < 0 || shortCount > d {
		panic(fmt.Sprintf("testutil: shortCount %d outside dictionary of %d", shortCount, d))
	}
	if d > shortCount && (1024-d)>>2 < shortCount {
		panic(fmt.Sprintf("testutil: shortCount %d collides with long-code prefixes at D=%d", shortCount, d))
	}
	for i, sub := range nested {
		if i < 0 || i >= d {
			panic(fmt.Sprintf("testutil: nested phrase %d outside dictionary of %d", i, d))
		}
		for _, j := range sub {
			if j <= i {
				panic(fmt.Sprintf("testutil: nested phrase %d references earlier phrase %d", i, j))
			}
		}
	}
	h := &HuffCDIC{phrases: append([][]byte(nil), phrases...), nested: nested, shortCount: shortCount}
	for i, sub := range nested {
		h.phrases[i] = h.Encode(sub)
	}
	return h
}

// HUFFRecord renders the HUFF record: the 8-byte header (magic plus
// the 0x18 table1 offset KindleUnpack's loader requires), table1 at
// 24, table2 at 24+1024. Nonterminal table1 entries carry code length
// 9 — real files never store nonterminal entries at or below 8 bits
// (KindleUnpack's dict1_unpack asserts it), and the walk from 9 to 10
// is exactly what table2's minimum codes encode.
func (h *HuffCDIC) HUFFRecord() []byte {
	const off1 = 24
	const off2 = off1 + 4*256
	rec := make([]byte, off2+8*32)
	copy(rec, "HUFF")
	put32(rec, 4, 24)
	put32(rec, 8, off1)
	put32(rec, 12, off2)
	for b := 0; b < 256; b++ {
		x := uint32(9) // nonterminal, min code length 9: start of the walk
		if b < h.shortCount {
			x = 0x80 | 8 | uint32(2*b)<<8
		}
		put32(rec, off1+4*b, x)
	}
	// Walk bounds: the long codes' 9-bit prefixes all sit below 512,
	// so the walk advances 9 -> 10, where the minimum code 1024-D
	// lands it on the code's own length; the maximum 10-bit code maps
	// index 0 of the long block... more precisely code 1023-i maps i.
	put32(rec, off2+7*8, 256)
	put32(rec, off2+8*8, 512)
	put32(rec, off2+9*8, uint32(1024-len(h.phrases)))
	put32(rec, off2+9*8+4, 1023)
	return rec
}

// CDICRecords splits the dictionary across CDIC records of up to
// maxEntries phrases each (maxEntries <= 0 means one record). The
// per-record count follows the format's capacity rule — n = min(1 <<
// codeLength, total - already loaded) — so maxEntries is rounded up to
// a power of two and the last record carries the remainder. Every
// record carries the global phrase total in numEntries.
func (h *HuffCDIC) CDICRecords(maxEntries int) [][]byte {
	d := len(h.phrases)
	if maxEntries <= 0 || maxEntries > d {
		maxEntries = d
	}
	codeLength := uint32(0)
	for 1<<codeLength < maxEntries {
		codeLength++
	}
	capEntries := 1 << codeLength
	var recs [][]byte
	for loaded := 0; loaded < d; loaded += capEntries {
		n := min(capEntries, d-loaded)
		area := 2 * n
		for j := range n {
			area += 2 + len(h.phrases[loaded+j])
		}
		rec := make([]byte, 16+area)
		copy(rec, "CDIC")
		put32(rec, 4, 16)
		put32(rec, 8, uint32(d))
		put32(rec, 12, codeLength)
		off := 2 * n
		for j := range n {
			p := loaded + j
			binary.BigEndian.PutUint16(rec[16+2*j:], uint16(off))
			x := uint16(len(h.phrases[p]))
			if _, isNested := h.nested[p]; !isNested {
				x |= 0x8000
			}
			binary.BigEndian.PutUint16(rec[16+off:], x)
			copy(rec[16+off+2:], h.phrases[p])
			off += 2 + len(h.phrases[p])
		}
		recs = append(recs, rec)
	}
	return recs
}

// Encode renders the index stream as compressed bytes, MSB-first,
// zero-padded to a byte boundary.
func (h *HuffCDIC) Encode(indices []int) []byte {
	var out []byte
	var acc uint32
	var nbits uint
	for _, i := range indices {
		code, length := uint32(i), uint(8)
		if i >= h.shortCount {
			code, length = 1023-uint32(i), 10
		}
		acc = acc<<length | code
		nbits += length
		for nbits >= 8 {
			nbits -= 8
			out = append(out, byte(acc>>nbits))
		}
	}
	if nbits > 0 {
		out = append(out, byte(acc<<(8-nbits)))
	}
	return out
}

// Expand renders the expected decompression of the index stream,
// applying the same nested-phrase expansion the decoder must perform.
func (h *HuffCDIC) Expand(indices []int) []byte {
	var out []byte
	for _, i := range indices {
		if sub, ok := h.nested[i]; ok {
			out = append(out, h.Expand(sub)...)
		} else {
			out = append(out, h.phrases[i]...)
		}
	}
	return out
}
