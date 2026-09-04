// INDX fixture encoder: the inverse of the parser in indx.go. It
// renders the generic tagged-index format (first INDX record with a
// TAGX tag table, entry records with IDXT offset tables, CNCX phrase
// records) so the index and TOC tests — and the KF8 stage after them —
// can round-trip authored structures instead of carrying binary blobs.
//
// The control-byte writer produces all three value encodings the format
// allows (inline value, inline count, byte-count), chosen per tag from
// the value count and the tag's mask, exactly mirroring the parser's
// dispatch.

package testutil

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/madeofpendletonwool/mobi-go/internal/varlen"
)

// INDX layout constants, mirrored from the parser for fixture writing.
const (
	indxHeaderLen  = 192     // first record's TAGX section starts here
	indxIDXTLen    = 4       // 'IDXT' magic before the u16 offset table
	cncxRecordSize = 0x10000 // CNCX stride: one record's address window

	indxCodepageCP1252 = 1252
	indxCodepageUTF8   = 65001
)

// TagDesc is one TAGX tag-table row: (tag, valuesPerEntry, mask,
// endFlags). endFlags&1 marks a header-only row that advances the
// control-byte index and carries no value; a row of (0, 0, 0, 1) is the
// conventional pure boundary marker.
type TagDesc struct {
	Tag            int
	ValuesPerEntry int
	Mask           byte
	EndFlags       byte
}

// IndexEntry is one index entry: its name bytes and its per-tag value
// lists. Values[tag] must hold a multiple of the tag's ValuesPerEntry;
// an absent or empty list encodes "tag not set".
type IndexEntry struct {
	Name   string
	Values map[int][]int
}

// IndexConfig tweaks the index BuildIndex renders. The zero value
// builds a valid empty UTF-8 index with no CNCX records.
type IndexConfig struct {
	// Encoding is the INDX records' encoding field: 65001 (default)
	// or 1252.
	Encoding uint32

	// TagTable is the TAGX section's tag rows, in order.
	TagTable []TagDesc

	// Entries are the index entries, in order. EntriesPerRecord (when
	// > 0) splits them across that many entry records per record;
	// 0 puts them all in one entry record.
	Entries          []IndexEntry
	EntriesPerRecord int

	// CNCX lists the phrase-pool records: each inner slice becomes one
	// CNCX record, addressed at its own 0x10000-stride base. Phrases in
	// the second and later groups force offsets in the upper stride
	// regions.
	CNCX [][]string
}

// BuiltIndex is a rendered index: Records holds the first INDX record,
// the entry records, and the CNCX records, ready to drop into a
// container in that order; CNCX holds the absolute lookup offset of
// each phrase, flattened across the CNCX groups in order.
type BuiltIndex struct {
	Records [][]byte
	CNCX    []int
}

// BuildIndex renders cfg. It panics on configurations the format cannot
// express (misaligned value arities, non-contiguous masks, names over
// 255 bytes): a broken fixture is a test bug and should fail loudly.
func BuildIndex(cfg IndexConfig) BuiltIndex {
	encoding := cfg.Encoding
	if encoding == 0 {
		encoding = indxCodepageUTF8
	}

	perRecord := cfg.EntriesPerRecord
	if perRecord <= 0 {
		perRecord = len(cfg.Entries)
	}
	var entryRecords [][]byte
	total := 0
	for start := 0; start < len(cfg.Entries); start += perRecord {
		end := min(start+perRecord, len(cfg.Entries))
		entryRecords = append(entryRecords, buildEntryRecord(cfg, encoding, cfg.Entries[start:end]))
		total += end - start
	}

	var cncxRecords [][]byte
	var cncxOffsets []int
	for _, phrases := range cfg.CNCX {
		rec, offsets := buildCNCXRecord(phrases, encoding, len(cncxRecords))
		cncxRecords = append(cncxRecords, rec)
		cncxOffsets = append(cncxOffsets, offsets...)
	}

	first := buildFirstRecord(cfg, encoding, len(entryRecords), len(cncxRecords))

	records := make([][]byte, 0, 1+len(entryRecords)+len(cncxRecords))
	records = append(records, first)
	records = append(records, entryRecords...)
	records = append(records, cncxRecords...)
	return BuiltIndex{Records: records, CNCX: cncxOffsets}
}

// buildFirstRecord renders the index's header record: the INDX header
// followed by the TAGX section at offset 192.
func buildFirstRecord(cfg IndexConfig, encoding uint32, numEntryRecords, numCNCX int) []byte {
	tagx := buildTAGX(cfg.TagTable)
	rec := make([]byte, indxHeaderLen+len(tagx))
	putINDXHeader(rec, indxHeaderLen, indxHeaderLen, numEntryRecords, encoding, 0)
	binary.BigEndian.PutUint32(rec[52:56], uint32(numCNCX))
	copy(rec[indxHeaderLen:], tagx)
	return rec
}

// buildTAGX renders the TAGX section: magic, section length, control
// byte count (one per header-only row plus one), then the 4-byte tag
// rows.
func buildTAGX(table []TagDesc) []byte {
	markers := 0
	for _, td := range table {
		if td.EndFlags&1 != 0 {
			markers++
		}
	}
	n := 12 + 4*len(table)
	out := make([]byte, n)
	copy(out, "TAGX")
	binary.BigEndian.PutUint32(out[4:8], uint32(n))
	binary.BigEndian.PutUint32(out[8:12], uint32(markers+1))
	for i, td := range table {
		pos := 12 + 4*i
		out[pos] = byte(td.Tag)
		out[pos+1] = byte(td.ValuesPerEntry)
		out[pos+2] = td.Mask
		out[pos+3] = td.EndFlags
	}
	return out
}

// buildEntryRecord renders one entry record: INDX header, the encoded
// entries, zero padding to a 4-byte boundary, then the IDXT section
// (magic plus a u16 offset per entry, absolute within the record).
func buildEntryRecord(cfg IndexConfig, encoding uint32, entries []IndexEntry) []byte {
	enc := func(e IndexEntry) []byte {
		ctrl, data := encodeTagValues(cfg.TagTable, e.Values)
		name := []byte(e.Name)
		if len(name) > 255 {
			panic(fmt.Sprintf("testutil: index entry name %q is %d bytes, over the u8 limit", e.Name, len(name)))
		}
		out := make([]byte, 0, 1+len(name)+len(ctrl)+len(data))
		out = append(out, byte(len(name)))
		out = append(out, name...)
		out = append(out, ctrl...)
		out = append(out, data...)
		return out
	}

	body := indxHeaderLen
	offsets := make([]int, len(entries))
	var entriesBuf []byte
	for i, e := range entries {
		offsets[i] = body + len(entriesBuf)
		entriesBuf = append(entriesBuf, enc(e)...)
	}
	idxt := body + len(entriesBuf)
	if pad := (4 - idxt%4) % 4; pad != 0 {
		entriesBuf = append(entriesBuf, make([]byte, pad)...)
		idxt += pad
	}

	rec := make([]byte, idxt+indxIDXTLen+2*len(entries))
	putINDXHeader(rec, indxHeaderLen, idxt, len(entries), encoding, 0)
	copy(rec[body:], entriesBuf)
	copy(rec[idxt:], "IDXT")
	for i, off := range offsets {
		binary.BigEndian.PutUint16(rec[idxt+indxIDXTLen+2*i:], uint16(off))
	}
	return rec
}

// buildCNCXRecord renders one CNCX record: a stream of
// varlen-length-prefixed phrases, zero-padded to a 4-byte boundary.
// Offsets are reported relative to the record start; the caller adds
// the record's stride base. Phrases encode per the index encoding:
// UTF-8 verbatim, cp1252 through CP1252's inverse map.
func buildCNCXRecord(phrases []string, encoding uint32, recordIndex int) ([]byte, []int) {
	var buf []byte
	offsets := make([]int, len(phrases))
	for i, p := range phrases {
		var b []byte
		if encoding == indxCodepageCP1252 {
			b = CP1252(p)
		} else {
			b = []byte(p)
		}
		offsets[i] = len(buf)
		buf = varlen.Append(buf, len(b))
		buf = append(buf, b...)
	}
	if pad := (4 - len(buf)%4) % 4; pad != 0 {
		buf = append(buf, make([]byte, pad)...)
	}
	base := recordIndex * cncxRecordSize
	abs := make([]int, len(offsets))
	for i, off := range offsets {
		abs[i] = base + off
	}
	return buf, abs
}

// cp1252Inverse maps the Unicode code points of windows-1252's 0x80–0x9F
// range back to their byte values — the reverse of the parser's table
// in encoding.go (same public-domain consortium data).
var cp1252Inverse = map[rune]byte{
	0x20AC: 0x80, 0x0081: 0x81, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84,
	0x2026: 0x85, 0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88, 0x2030: 0x89,
	0x0160: 0x8A, 0x2039: 0x8B, 0x0152: 0x8C, 0x008D: 0x8D, 0x017D: 0x8E,
	0x008F: 0x8F, 0x0090: 0x90, 0x2018: 0x91, 0x2019: 0x92, 0x201C: 0x93,
	0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97, 0x02DC: 0x98,
	0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B, 0x0153: 0x9C, 0x009D: 0x9D,
	0x017E: 0x9E, 0x0178: 0x9F,
}

// CP1252 encodes s as windows-1252 bytes: ASCII and the Latin-1 range
// pass through, the twenty-seven mapped code points convert through the
// inverse table, and anything else panics (fixtures must round-trip
// the decoder). Author phrases and titles as ordinary Go strings.
func CP1252(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			b, ok := cp1252Inverse[r]
			if !ok {
				panic(fmt.Sprintf("testutil: rune %U is not representable in windows-1252", r))
			}
			out = append(out, b)
		}
	}
	return out
}

// putINDXHeader writes the fixed INDX header fields the parser reads:
// magic, length, idxt, count, encoding, and language; numCncx (@52) is
// written separately by the first record. Unused words stay zero.
func putINDXHeader(rec []byte, length, idxt, count int, encoding uint32, language uint32) {
	copy(rec[0:4], "INDX")
	binary.BigEndian.PutUint32(rec[4:8], uint32(length))
	binary.BigEndian.PutUint32(rec[20:24], uint32(idxt))
	binary.BigEndian.PutUint32(rec[24:28], uint32(count))
	binary.BigEndian.PutUint32(rec[28:32], encoding)
	binary.BigEndian.PutUint32(rec[32:36], language)
}

// encodeTagValues renders one entry's control bytes and value bytes
// from the tag table and the entry's values, the exact inverse of the
// parser's getTagMap: header-only rows consume a control byte; a set
// single-bit mask means one value group follows; a partial multi-bit
// mask holds an inline group count; a full multi-bit mask switches to
// a leading varlen byte count for the value bytes that follow.
//
// The value area has two phases, matching how both port sources read
// it: every byte-count varlen comes first (tag-table order), then all
// value varlens (tag order, byte-count tags' values at their table
// position). KindleUnpack's getTagMap and foliate-js's tag loop both
// consume the byte counts during their control-byte walk and the
// values in a second pass, so that — not a per-tag interleave — is
// the wire layout.
func encodeTagValues(table []TagDesc, values map[int][]int) (ctrl, data []byte) {
	ctrl = make([]byte, 0, 1)
	var countPrefixes, valueBytes []byte
	for _, td := range table {
		if td.EndFlags&1 != 0 {
			ctrl = append(ctrl, 0)
			continue
		}
		if len(ctrl) == 0 {
			ctrl = append(ctrl, 0)
		}
		vals := values[td.Tag]
		if len(vals) == 0 {
			continue
		}
		if td.ValuesPerEntry <= 0 {
			panic(fmt.Sprintf("testutil: tag %d carries %d values but declares ValuesPerEntry %d", td.Tag, len(vals), td.ValuesPerEntry))
		}
		if len(vals)%td.ValuesPerEntry != 0 {
			panic(fmt.Sprintf("testutil: tag %d has %d values, not a multiple of %d", td.Tag, len(vals), td.ValuesPerEntry))
		}
		count := len(vals) / td.ValuesPerEntry

		pop := bits.OnesCount8(td.Mask)
		tz := bits.TrailingZeros8(td.Mask)
		if uint32(td.Mask) != ((1<<pop)-1)<<tz {
			panic(fmt.Sprintf("testutil: tag %d mask %#02x is not contiguous", td.Tag, td.Mask))
		}
		last := len(ctrl) - 1
		switch {
		case pop == 1:
			if count != 1 {
				panic(fmt.Sprintf("testutil: tag %d mask %#02x is single-bit and cannot express %d value groups", td.Tag, td.Mask, count))
			}
			ctrl[last] |= td.Mask
			valueBytes = appendVarlens(valueBytes, vals)
		case count < 1<<pop-1:
			ctrl[last] |= byte(count) << tz
			valueBytes = appendVarlens(valueBytes, vals)
		default:
			// The all-ones (or overflow) count: set the whole mask;
			// the byte-count prefix joins the other prefixes and the
			// values join the value phase at this tag's position.
			ctrl[last] |= td.Mask
			payload := appendVarlens(nil, vals)
			countPrefixes = varlen.Append(countPrefixes, len(payload))
			valueBytes = append(valueBytes, payload...)
		}
	}
	data = append(countPrefixes, valueBytes...)
	return ctrl, data
}

func appendVarlens(dst []byte, vals []int) []byte {
	for _, v := range vals {
		dst = varlen.Append(dst, v)
	}
	return dst
}

// NumTextRecords reports how many text records BuildBook renders for
// cfg, so tests can compute absolute record indices for the index and
// resource fields they wire in.
func NumTextRecords(cfg BookConfig) int {
	recordSize := int(cfg.RecordSize)
	if recordSize == 0 {
		recordSize = 4096
	}
	n := (len(cfg.Text) + recordSize - 1) / recordSize
	return max(n, 1)
}

// BuildBookWithIndex renders a book whose trailing records carry an
// INDX index, with the MOBI header's indx field pointing at the index's
// first record. Extra trailing records (a KF8 guide index, say) can be
// appended before the index via book.TrailingRecords and their absolute
// indices computed with NumTextRecords.
func BuildBookWithIndex(book BookConfig, idx IndexConfig) []byte {
	built := BuildIndex(idx)
	base := 1 + NumTextRecords(book) + len(book.Resources)
	indx := uint32(base + len(book.TrailingRecords))
	book.TrailingRecords = append(book.TrailingRecords, built.Records...)
	book.Indx = &indx
	return BuildBook(book)
}
