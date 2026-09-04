// HUFF/CDIC text-record decompression: the compression behind most
// Amazon-produced MOBI6 files. A HUFF record holds two code tables; the
// CDIC records after it hold the phrase dictionary those codes index
// into. Decompression reads Huffman codes MSB-first, maps each to a
// phrase index, and emits the phrase; phrases stored without the
// already-decompressed flag are themselves compressed data, expanded
// recursively and memoized back into the dictionary.
//
// Ported with attribution from KindleUnpack's HuffcdicReader
// (lib/mobi_uncompress.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), cross-checked
// branch for branch against foliate-js's huffcdic / read32Bits
// (mobi.js, MIT, https://github.com/johnfactotum/foliate-js). The two
// agree on every decode semantic; where both would crash or recurse
// without bound on malformed input (dictionary overruns, zero-length
// codes, code-length walks past 32, self-referential or absurdly deep
// phrases), this port returns ErrCorrupt instead.

package mobi

import "fmt"

// HUFF/CDIC record layout constants. All integers are big-endian.
const (
	// HUFF record: magic, then the byte offsets (uint32 @8 and @12) of
	// the two code tables within the record.
	huffMinLen = 16
	// table1 holds one uint32 per possible next-eight-input-bits value.
	huffTable1Entries = 256
	// table2 holds 32 (min, max) code pairs, one per code length
	// 1..32.
	huffTable2Entries = 32

	// CDIC record: magic, entry-area offset @4, global phrase total
	// @8, index-bit count @12.
	cdicMinLen = 16
)

// huffMaxPhraseDepth caps recursive phrase expansion: a phrase whose
// expansion references another not-yet-decompressed phrase, which
// references another, and so on. Real dictionaries nest a few levels
// deep at most, and KindleUnpack itself dies at Python's ~1000-frame
// recursion limit, so 4096 accepts every file the port sources read
// while keeping stack use bounded.
const huffMaxPhraseDepth = 4096

// huffMaxOutput caps the bytes one record may decompress to. Text
// records decompress to the 4 KiB record size in every real file; the
// cap exists to turn hostile dictionaries (phrases whose expansions
// reference each other in exponentially growing patterns) into ErrCorrupt
// instead of unbounded memory use.
const huffMaxOutput = 32 << 20

// huffTable1Entry is one slot of the HUFF record's first table,
// indexed by the next eight input bits. Terminal entries resolve the
// phrase index directly; nonterminal entries start a walk up the code
// lengths in table2.
type huffTable1Entry struct {
	codelen  uint32 // low 5 bits of the packed entry; 0 is invalid
	terminal bool   // bit 0x80
	value    uint32 // entry >>> 8
}

// huffTable2Entry is one (min, max) code pair from the HUFF record's
// second table, giving the smallest and largest code of a given
// length. The parsed table is indexed by code length: slot L (1..32)
// comes from pair L-1 at offset2 + (L-1)*8; slot 0 is synthetic and
// unused.
type huffTable2Entry struct {
	min, max uint32
}

// huffPhrase is one dictionary entry. Phrases stored without the
// already-decompressed flag are themselves HUFF/CDIC-compressed data:
// they are expanded on first use and the result is memoized back into
// the dictionary (which is what the flag exists to track).
type huffPhrase struct {
	data         []byte
	decompressed bool
	expanding    bool // cycle guard; not an on-disk field
}

// huffCDIC is a parsed HUFF/CDIC dictionary: the code tables plus the
// phrases the codes index into. The dictionary mutates during
// decompression as nested phrases are expanded and cached, so a
// huffCDIC belongs to one Book and one goroutine.
type huffCDIC struct {
	table1     [huffTable1Entries]huffTable1Entry
	table2     [huffTable2Entries + 1]huffTable2Entry
	dictionary []huffPhrase
}

// newHuffCDIC parses the HUFF record and the CDIC records that follow
// it and returns the decompressor. cdics may be empty, in which case
// the dictionary starts empty and any coded input reports ErrCorrupt.
func newHuffCDIC(huff []byte, cdics [][]byte) (func([]byte) ([]byte, error), error) {
	d := &huffCDIC{}
	if err := d.loadHuff(huff); err != nil {
		return nil, err
	}
	for _, cdic := range cdics {
		if err := d.loadCdic(cdic); err != nil {
			return nil, err
		}
	}
	return d.decompress, nil
}

// loadHuff parses the HUFF record's two code tables. The tables are
// validated only far enough that every later read is in bounds;
// semantic damage (zero code lengths, empty code ranges) surfaces as
// ErrCorrupt at decode time, so unused-but-garbage table slots do not
// reject a file the port sources would read.
func (d *huffCDIC) loadHuff(huff []byte) error {
	if len(huff) < huffMinLen {
		return fmt.Errorf("%w: HUFF record is %d bytes, shorter than the %d-byte header",
			ErrCorrupt, len(huff), huffMinLen)
	}
	if string(huff[:4]) != "HUFF" {
		return fmt.Errorf("%w: HUFF record magic is %q, want HUFF", ErrCorrupt, huff[:4])
	}
	off1 := int64(be32(huff, 8))
	off2 := int64(be32(huff, 12))
	if off1+huffTable1Entries*4 > int64(len(huff)) {
		return fmt.Errorf("%w: HUFF table1 offset %d runs past the %d-byte record",
			ErrCorrupt, off1, len(huff))
	}
	if off2+huffTable2Entries*8 > int64(len(huff)) {
		return fmt.Errorf("%w: HUFF table2 offset %d runs past the %d-byte record",
			ErrCorrupt, off2, len(huff))
	}
	for i := range d.table1 {
		v := be32(huff, int(off1)+i*4)
		d.table1[i] = huffTable1Entry{
			codelen:  v & 0x1F,
			terminal: v&0x80 != 0,
			value:    v >> 8,
		}
	}
	for length := 1; length <= huffTable2Entries; length++ {
		pair := int(off2) + (length-1)*8
		d.table2[length] = huffTable2Entry{min: be32(huff, pair), max: be32(huff, pair+4)}
	}
	return nil
}

// loadCdic parses one CDIC record and appends its phrases to the
// dictionary. The numEntries field carries the global phrase total
// across every CDIC record, so this record contributes the entries
// beyond those already loaded, capped by its index-bit count.
func (d *huffCDIC) loadCdic(cdic []byte) error {
	if len(cdic) < cdicMinLen {
		return fmt.Errorf("%w: CDIC record is %d bytes, shorter than the %d-byte header",
			ErrCorrupt, len(cdic), cdicMinLen)
	}
	if string(cdic[:4]) != "CDIC" {
		return fmt.Errorf("%w: CDIC record magic is %q, want CDIC", ErrCorrupt, cdic[:4])
	}
	area := int64(be32(cdic, 4))
	numEntries := int64(be32(cdic, 8))
	codeLength := be32(cdic, 12)
	if area > int64(len(cdic)) {
		return fmt.Errorf("%w: CDIC entry area starts at %d, past the %d-byte record",
			ErrCorrupt, area, len(cdic))
	}
	remaining := numEntries - int64(len(d.dictionary))
	if remaining < 0 {
		return fmt.Errorf("%w: CDIC declares %d total phrases but %d are already loaded",
			ErrCorrupt, numEntries, len(d.dictionary))
	}
	n := remaining
	if codeLength < 32 { // past 32 bits the cap exceeds any uint32 total
		if indexed := int64(1) << codeLength; indexed < n {
			n = indexed
		}
	}
	if area+2*n > int64(len(cdic)) {
		return fmt.Errorf("%w: CDIC offset table for %d phrases runs past the %d-byte record",
			ErrCorrupt, n, len(cdic))
	}
	for i := int64(0); i < n; i++ {
		off := int64(be16(cdic, int(area+2*i)))
		if area+off+2 > int64(len(cdic)) {
			return fmt.Errorf("%w: CDIC phrase header at %d runs past the %d-byte record",
				ErrCorrupt, area+off, len(cdic))
		}
		x := be16(cdic, int(area+off))
		length := int64(x & 0x7FFF)
		if area+off+2+length > int64(len(cdic)) {
			return fmt.Errorf("%w: CDIC phrase at %d spans %d bytes, past the %d-byte record",
				ErrCorrupt, area+off, length, len(cdic))
		}
		d.dictionary = append(d.dictionary, huffPhrase{
			data:         cdic[int(area+off+2):int(area+off+2+length)],
			decompressed: x&0x8000 != 0,
		})
	}
	return nil
}

// decompress decodes one HUFF/CDIC-compressed record.
//
// Codes are read MSB-first through a zero-padded 32-bit window (the
// read32Bits port): a table1 lookup by the next eight bits resolves
// terminal codes directly; nonterminal entries walk the code lengths
// upward until the window's top bits reach that length's minimum code,
// then take the length's maximum code as the base. The phrase index is
// value minus the code, so higher codes of a length mean lower
// dictionary indices. When a code's bits run past the end of the input
// the loop stops without emitting — both port sources behave the same
// way, so trailing pad bits in the final byte decode nothing in files
// whose shortest code outruns the padding (all real files).
func (d *huffCDIC) decompress(src []byte) ([]byte, error) {
	return d.decode(src, palmdocTextRecordSize, 0)
}

// decode is decompress with an explicit output capacity and nesting
// depth, so recursive phrase expansion shares one code path.
func (d *huffCDIC) decode(src []byte, capacity, depth int) ([]byte, error) {
	if depth > huffMaxPhraseDepth {
		return nil, fmt.Errorf("%w: phrase nesting deeper than %d levels",
			ErrCorrupt, huffMaxPhraseDepth)
	}
	if capacity < len(src) {
		capacity = len(src)
	}
	dst := make([]byte, 0, capacity)
	bitLen := len(src) * 8
	for pos := 0; pos < bitLen; {
		bits := peek32Bits(src, pos)
		entry := d.table1[bits>>24]
		if entry.codelen == 0 {
			return nil, fmt.Errorf("%w: HUFF table1 entry for byte %d has code length 0",
				ErrCorrupt, bits>>24)
		}
		codelen := entry.codelen
		value := entry.value
		if !entry.terminal {
			for codelen <= huffTable2Entries && bits>>(32-codelen) < d.table2[codelen].min {
				codelen++
			}
			if codelen > huffTable2Entries {
				return nil, fmt.Errorf("%w: code length walk ran past %d bits",
					ErrCorrupt, huffTable2Entries)
			}
			value = d.table2[codelen].max
		}
		pos += int(codelen)
		if pos > bitLen {
			break
		}
		code := value - (bits >> (32 - codelen))
		if uint64(code) >= uint64(len(d.dictionary)) {
			return nil, fmt.Errorf("%w: phrase index %d outside the %d-entry dictionary",
				ErrCorrupt, code, len(d.dictionary))
		}
		phrase := &d.dictionary[code]
		data := phrase.data
		if !phrase.decompressed {
			if phrase.expanding {
				return nil, fmt.Errorf("%w: phrase %d expands into itself", ErrCorrupt, code)
			}
			phrase.expanding = true
			expanded, err := d.decode(data, 2*len(data), depth+1)
			phrase.expanding = false
			if err != nil {
				return nil, err
			}
			phrase.data, phrase.decompressed = expanded, true
			data = expanded
		}
		dst = append(dst, data...)
		if len(dst) > huffMaxOutput {
			return nil, fmt.Errorf("%w: record expands past %d bytes", ErrCorrupt, huffMaxOutput)
		}
	}
	return dst, nil
}

// peek32Bits returns the 32 bits starting at bit offset pos (MSB
// first), zero-padded past the end of src. Ported from foliate-js's
// read32Bits; bit offsets never reach len(src)*8, so five source bytes
// always cover the window.
func peek32Bits(src []byte, pos int) uint32 {
	i := pos >> 3
	var x uint64
	for k := range 5 {
		var b byte
		if i+k < len(src) {
			b = src[i+k]
		}
		x = x<<8 | uint64(b)
	}
	return uint32(x >> uint(8-(pos&7)))
}

// huffCDICDecompressor lazily builds and caches the book's HUFF/CDIC
// decompressor from the records the MOBI header points at (HUFF at
// huffcdic, then numHuffcdic-1 CDIC records after it).
func (b *Book) huffCDICDecompressor() (func([]byte) ([]byte, error), error) {
	if b.huffcdic != nil {
		return b.huffcdic, nil
	}
	first := int64(b.mobi.Huffcdic)
	count := int64(b.mobi.NumHuffcdic)
	if first < 0 || count < 1 {
		return nil, fmt.Errorf("%w: HUFF/CDIC compression with dictionary index %d and count %d",
			ErrCorrupt, b.mobi.Huffcdic, b.mobi.NumHuffcdic)
	}
	if first+count > int64(b.pdb.NumRecords()) {
		return nil, fmt.Errorf("%w: HUFF/CDIC records %d..%d run past the %d-record file",
			ErrRecordRange, first, first+count-1, b.pdb.NumRecords())
	}
	huff, err := b.pdb.Record(int(first))
	if err != nil {
		return nil, err
	}
	cdics := make([][]byte, 0, count-1)
	for i := int64(1); i < count; i++ {
		cdic, err := b.pdb.Record(int(first + i))
		if err != nil {
			return nil, err
		}
		cdics = append(cdics, cdic)
	}
	decompress, err := newHuffCDIC(huff, cdics)
	if err != nil {
		return nil, err
	}
	b.huffcdic = decompress
	return decompress, nil
}
