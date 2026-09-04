// INDX index records: the generic tagged-index format that carries a
// MOBI file's structured indexes — the NCX table of contents, the KF8
// guide, and (in the KF8 stage) skeletons and fragments.
//
// Ported with attribution from KindleUnpack's MobiIndex
// (lib/mobi_index.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// getIndexData / INDX_HEADER / TAGX_HEADER (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js) as the structural
// cross-check. The two port sources agree on every field this file
// reads; where they differ (KindleUnpack's ORDT remapping for a handful
// of hacked ESP sample files, foliate's 4-byte cap on forward varlen
// reads) the differences are noted inline.
//
// An index is a run of records: a first record holding the shared TAGX
// tag table, then one record per batch of entries, then the CNCX
// phrase-pool records. Entries carry a name plus per-tag value lists,
// decoded from control bytes per the tag table; string values live in
// the CNCX pool and are referenced by byte offset.

package mobi

import (
	"fmt"
	"math/bits"

	"github.com/madeofpendletonwool/mobi-go/internal/varlen"
)

// INDX record header layout (all big-endian; offsets absolute within
// the record). The first record of an index uses length as the TAGX
// offset and count as the number of entry records that follow; entry
// records use idxt as their offset table's position and count as the
// entries they hold. Both port sources read the same struct for both
// roles.
const (
	indxHdrLen      = 4  // magic "INDX"
	indxMinRecord   = 56 // magic + the 13 header words
	indxWordLength  = 4  // length @4
	indxWordIDXT    = 20 // idxt offset @20
	indxWordCount   = 24 // entry/record count @24
	indxWordCodepg  = 28 // encoding @28
	indxWordLang    = 32 // language @32
	indxWordTotal   = 36 // total entries @36 (advisory)
	indxWordNumCncx = 52 // CNCX record count @52

	// TAGX section layout, at the first record's length offset.
	tagxHeaderLen = 12 // magic, section length, control byte count
	tagxWordLen   = 4
	tagxWordCount = 8

	// cncxStride is the address window each CNCX record owns: a phrase
	// at in-record offset o in CNCX record r is addressed as
	// r*cncxStride + o.
	cncxStride = 0x10000
)

// indxHeader is the parsed header common to every INDX record.
type indxHeader struct {
	Length   uint32 // first record: TAGX offset; entry records: header length
	IDXT     uint32 // entry records: offset of the IDXT offset table
	Count    uint32 // first record: entry-record count; entry record: entry count
	Encoding uint32
	Language uint32
	Total    uint32
	NumCNCX  uint32
}

// tagxEntry is one row of the TAGX tag table: (tag, valuesPerEntry,
// mask, endFlags). endFlags&1 marks a header-only row: it consumes a
// control byte and carries no value — the conventional (0,0,0,1) row
// is a pure control-byte boundary.
type tagxEntry struct {
	Tag            int
	ValuesPerEntry int
	Mask           byte
	EndFlags       byte
}

// indexEntry is one parsed index entry: its raw name bytes and its
// per-tag value lists in the order the tag table defines. Names are
// kept undecoded — they are ASCII type tokens or numbers in every
// index this library reads; CNCX phrases carry the display strings.
type indexEntry struct {
	Name   string
	Values map[int][]int
}

// cncxPool is the index's phrase pool: every CNCX record's strings,
// keyed by absolute byte offset (record index * 0x10000 + in-record
// offset), decoded with the index's encoding.
type cncxPool struct {
	phrases map[int]string
}

// lookup returns the phrase at absolute offset off.
func (c *cncxPool) lookup(off int) (string, bool) {
	if c == nil {
		return "", false
	}
	s, ok := c.phrases[off]
	return s, ok
}

// readIndex parses the index rooted at record firstIdx and returns its
// entries in file order alongside the CNCX pool. The record layout
// follows both port sources: firstIdx is the header record; entry
// records run firstIdx+1 .. firstIdx+header.Count; CNCX records follow
// immediately.
func (b *Book) readIndex(firstIdx int) ([]indexEntry, *cncxPool, error) {
	if firstIdx < 0 {
		return nil, nil, fmt.Errorf("%w: index record %d", ErrRecordRange, firstIdx)
	}
	first, err := b.pdb.Record(firstIdx)
	if err != nil {
		return nil, nil, err
	}
	hdr, err := parseINDXHeader(first)
	if err != nil {
		return nil, nil, fmt.Errorf("index record %d: %w", firstIdx, err)
	}
	if hdr.Encoding != codepageUTF8 && hdr.Encoding != codepageCP1252 {
		return nil, nil, fmt.Errorf("%w: index encoding %d, want 1252 or 65001",
			ErrCorrupt, hdr.Encoding)
	}
	ctrlCount, table, err := parseTAGX(first, int64(hdr.Length))
	if err != nil {
		return nil, nil, fmt.Errorf("index record %d: %w", firstIdx, err)
	}

	// CNCX records sit right after the last entry record.
	var cncxRecords [][]byte
	for i := range uint32(hdr.NumCNCX) {
		rec, err := b.pdb.Record(firstIdx + 1 + int(hdr.Count) + int(i))
		if err != nil {
			return nil, nil, fmt.Errorf("index record %d: cncx record %d: %w", firstIdx, i, err)
		}
		cncxRecords = append(cncxRecords, rec)
	}
	pool, err := parseCNCX(cncxRecords, hdr.Encoding)
	if err != nil {
		return nil, nil, fmt.Errorf("index record %d: %w", firstIdx, err)
	}

	var entries []indexEntry
	for i := range uint32(hdr.Count) {
		rec, err := b.pdb.Record(firstIdx + 1 + int(i))
		if err != nil {
			return nil, nil, fmt.Errorf("index record %d: entry record %d: %w", firstIdx, i, err)
		}
		recEntries, err := parseEntryRecord(rec, ctrlCount, table)
		if err != nil {
			return nil, nil, fmt.Errorf("index record %d, entry record %d: %w", firstIdx, i, err)
		}
		entries = append(entries, recEntries...)
	}
	return entries, pool, nil
}

// parseINDXHeader reads the fixed INDX header fields from a record.
func parseINDXHeader(rec []byte) (indxHeader, error) {
	if len(rec) < indxMinRecord {
		return indxHeader{}, fmt.Errorf("%w: INDX record is %d bytes, shorter than its %d-byte header",
			ErrCorrupt, len(rec), indxMinRecord)
	}
	if string(rec[:4]) != "INDX" {
		return indxHeader{}, fmt.Errorf("%w: record magic is %q, want INDX",
			ErrCorrupt, rec[:4])
	}
	return indxHeader{
		Length:   be32(rec, indxWordLength),
		IDXT:     be32(rec, indxWordIDXT),
		Count:    be32(rec, indxWordCount),
		Encoding: be32(rec, indxWordCodepg),
		Language: be32(rec, indxWordLang),
		Total:    be32(rec, indxWordTotal),
		NumCNCX:  be32(rec, indxWordNumCncx),
	}, nil
}

// parseTAGX reads the TAGX section at absolute offset off in the index's
// first record: magic, section length, the control-byte count every
// entry carries, and the 4-byte tag rows.
func parseTAGX(rec []byte, off int64) (controlByteCount int, table []tagxEntry, err error) {
	if off < 0 || off+tagxHeaderLen > int64(len(rec)) {
		return 0, nil, fmt.Errorf("%w: TAGX section at %d overflows the %d-byte record",
			ErrCorrupt, off, len(rec))
	}
	if string(rec[off:off+4]) != "TAGX" {
		return 0, nil, fmt.Errorf("%w: TAGX magic is %q, want TAGX",
			ErrCorrupt, rec[off:off+4])
	}
	length := be32(rec, int(off)+tagxWordLen)
	ctrl := be32(rec, int(off)+tagxWordCount)
	secLen := int64(length)
	if secLen < tagxHeaderLen || off+secLen > int64(len(rec)) {
		return 0, nil, fmt.Errorf("%w: TAGX length %d runs past the %d-byte record",
			ErrCorrupt, secLen, len(rec))
	}
	rows := int((secLen - tagxHeaderLen) / 4)
	table = make([]tagxEntry, 0, rows)
	for i := range rows {
		pos := int(off) + tagxHeaderLen + 4*i
		table = append(table, tagxEntry{
			Tag:            int(rec[pos]),
			ValuesPerEntry: int(rec[pos+1]),
			Mask:           rec[pos+2],
			EndFlags:       rec[pos+3],
		})
	}
	return int(ctrl), table, nil
}

// parseEntryRecord reads one entry record: its INDX header, then the
// IDXT offset table, then each entry — name, control bytes, and the
// tag values the control bytes direct. Entry j spans from its IDXT
// offset to the next entry's (the last runs to the IDXT table, zero
// padding tolerated, exactly as KindleUnpack's idxPositions loop).
func parseEntryRecord(rec []byte, ctrlCount int, table []tagxEntry) ([]indexEntry, error) {
	hdr, err := parseINDXHeader(rec)
	if err != nil {
		return nil, err
	}
	idxt := int64(hdr.IDXT)
	n := int64(hdr.Count)
	if idxt < 0 || idxt+4+2*n > int64(len(rec)) {
		return nil, fmt.Errorf("%w: IDXT at %d with %d entries overflows the %d-byte record",
			ErrCorrupt, idxt, n, len(rec))
	}
	entries := make([]indexEntry, 0, n)
	for j := range n {
		start := int64(be16(rec, int(idxt)+4+2*int(j)))
		if start < 0 || start >= int64(len(rec)) {
			return nil, fmt.Errorf("%w: entry %d offset %d is outside the %d-byte record",
				ErrCorrupt, j, start, len(rec))
		}
		nameLen := int64(rec[start])
		if start+1+nameLen > int64(len(rec)) {
			return nil, fmt.Errorf("%w: entry %d name spans [%d, %d), past the %d-byte record",
				ErrCorrupt, j, start+1, start+1+nameLen, len(rec))
		}
		name := rec[start+1 : start+1+nameLen]
		values, err := parseTagMap(rec, start+1+nameLen, ctrlCount, table)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", j, err)
		}
		entries = append(entries, indexEntry{Name: string(name), Values: values})
	}
	return entries, nil
}

// parseTagMap decodes one entry's control bytes and value bytes into
// its tag-value lists, the port of KindleUnpack's getTagMap and
// foliate-js's tag loop, which agree byte for byte:
//
//   - A header-only row (endFlags&1) consumes a control byte and
//     contributes no value; value-bearing rows between two boundaries
//     share the current control byte, each reading it under its own
//     mask.
//   - A zero masked value means the tag is unset.
//   - A partially-set mask holds an inline value-group count:
//     groups = value >> trailing zeros of the mask.
//   - A fully-set single-bit mask means exactly one group follows in
//     the value bytes.
//   - A fully-set multi-bit mask means a varlen byte count follows,
//     then that many bytes of varlen values.
func parseTagMap(rec []byte, tagStart int64, ctrlCount int, table []tagxEntry) (map[int][]int, error) {
	if tagStart < 0 || tagStart+int64(ctrlCount) > int64(len(rec)) {
		return nil, fmt.Errorf("%w: %d control bytes at %d overflows the %d-byte record",
			ErrCorrupt, ctrlCount, tagStart, len(rec))
	}
	dataStart := tagStart + int64(ctrlCount)

	// pending tags, gathered while walking the control bytes.
	type pending struct {
		tag       int
		groups    int // > 0: that many value groups
		byteCount int // groups == 0: read until this many bytes consumed
		perEntry  int
	}
	var pendingTags []pending
	ctrlIdx := 0
	for _, te := range table {
		if te.EndFlags&1 != 0 {
			ctrlIdx++
			continue
		}
		pos := tagStart + int64(ctrlIdx)
		if pos >= int64(len(rec)) {
			return nil, fmt.Errorf("%w: control byte %d at %d is outside the %d-byte record",
				ErrCorrupt, ctrlIdx, pos, len(rec))
		}
		value := rec[pos] & te.Mask
		if value == 0 {
			continue
		}
		if value == te.Mask {
			if bits.OnesCount8(te.Mask) > 1 {
				// All mask bits set with a multi-bit mask: a varlen
				// byte count precedes the value bytes.
				n, size, ok := varlen.Read(rec, int(dataStart))
				if !ok {
					return nil, fmt.Errorf("%w: tag %d byte count varlen at %d is truncated",
						ErrCorrupt, te.Tag, dataStart)
				}
				dataStart += int64(size)
				pendingTags = append(pendingTags, pending{tag: te.Tag, byteCount: n, perEntry: te.ValuesPerEntry})
			} else {
				pendingTags = append(pendingTags, pending{tag: te.Tag, groups: 1, perEntry: te.ValuesPerEntry})
			}
		} else {
			groups := int(value) >> bits.TrailingZeros8(te.Mask)
			pendingTags = append(pendingTags, pending{tag: te.Tag, groups: groups, perEntry: te.ValuesPerEntry})
		}
	}

	values := make(map[int][]int, len(pendingTags))
	readVarlen := func() (v, size int, err error) {
		v, size, ok := varlen.Read(rec, int(dataStart))
		if !ok {
			return 0, 0, fmt.Errorf("%w: value varlen at %d is truncated", ErrCorrupt, dataStart)
		}
		dataStart += int64(size)
		return v, size, nil
	}
	for _, p := range pendingTags {
		var out []int
		switch {
		case p.groups > 0:
			for range p.groups * p.perEntry {
				v, _, err := readVarlen()
				if err != nil {
					return nil, fmt.Errorf("tag %d: %w", p.tag, err)
				}
				out = append(out, v)
			}
		default:
			// Byte-count form: varlen values until exactly byteCount
			// bytes of encodings are consumed; overshoot is corrupt.
			consumed := 0
			for consumed < p.byteCount {
				v, size, err := readVarlen()
				if err != nil {
					return nil, fmt.Errorf("tag %d: %w", p.tag, err)
				}
				out = append(out, v)
				consumed += size
			}
			if consumed != p.byteCount {
				return nil, fmt.Errorf("%w: tag %d values span %d bytes, want %d",
					ErrCorrupt, p.tag, consumed, p.byteCount)
			}
		}
		values[p.tag] = out
	}
	return values, nil
}

// parseCNCX reads the phrase-pool records: each is a stream of
// varlen-length-prefixed strings whose absolute offsets (record index
// * 0x10000 + in-record offset) are the values entries reference.
// Phrases are decoded with the index encoding. A zero byte ends a
// record's pool — KindleUnpack's terminator for the zero padding every
// real record carries.
func parseCNCX(records [][]byte, encoding uint32) (*cncxPool, error) {
	pool := &cncxPool{phrases: make(map[int]string)}
	for r, rec := range records {
		base := r * cncxStride
		pos := 0
		for pos < len(rec) {
			if rec[pos] == 0 {
				break
			}
			entryOff := base + pos
			size, sizeBytes, ok := varlen.Read(rec, pos)
			if !ok {
				return nil, fmt.Errorf("%w: cncx record %d: length varlen at %d is truncated",
					ErrCorrupt, r, pos)
			}
			pos += sizeBytes
			if size < 0 || pos+size > len(rec) {
				return nil, fmt.Errorf("%w: cncx record %d: phrase at %d spans %d bytes, past the %d-byte record",
					ErrCorrupt, r, pos, size, len(rec))
			}
			pool.phrases[entryOff] = decodeString(encoding, rec[pos:pos+size])
			pos += size
		}
	}
	return pool, nil
}
