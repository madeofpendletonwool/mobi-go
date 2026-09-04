// Package testutil builds synthetic MOBI container fixtures. Every test
// in the repo composes from here; no real-book bytes are ever committed.
package testutil

import "encoding/binary"

// PDBConfig tweaks the containers produced by BuildWith. The zero value
// builds a valid BOOK/MOBI database.
type PDBConfig struct {
	// Name is the database name written into the 32-byte header field.
	// Default "Test Book". Longer names are truncated to 32 bytes.
	Name string

	// Type and Creator are the 4-byte header fields at offsets 60 and
	// 64. Defaults "BOOK" and "MOBI". Pass other values (e.g. "TEXt" /
	// "REAd", or junk to test rejection) as needed.
	Type    string
	Creator string

	// NumRecords, when non-nil, overrides the record count written at
	// offset 76 instead of the true count — for zero-count and
	// absurd-count corruption cases. The record info list and data are
	// still laid out for the real records.
	NumRecords *uint16

	// RecordOffsets overrides the offset written for a given record's
	// entry in the record info list, while the record data stays where
	// BuildWith put it — for non-monotonic and out-of-range offsets.
	RecordOffsets map[int]uint32

	// Truncate, when > 0, cuts the produced file to that many bytes
	// (past the end is not extended). Values <= 0 leave the file intact.
	Truncate int
}

// Build writes a valid PalmDB container around records.
func Build(records ...[]byte) []byte {
	return BuildWith(PDBConfig{}, records...)
}

// BuildWith writes a PalmDB container around records using the knobs in
// cfg. With the zero config the result round-trips through the parser.
func BuildWith(cfg PDBConfig, records ...[]byte) []byte {
	name := cfg.Name
	if name == "" {
		name = "Test Book"
	}
	typ := cfg.Type
	if typ == "" {
		typ = "BOOK"
	}
	creator := cfg.Creator
	if creator == "" {
		creator = "MOBI"
	}

	n := len(records)
	count := uint16(n)
	if cfg.NumRecords != nil {
		count = *cfg.NumRecords
	}

	out := make([]byte, 0, pdbHeaderLen+pdbRecordInfoLen*n+totalLen(records))
	out = out[:pdbHeaderLen]
	copy(out, name)
	copy(out[pdbTypeOffset:pdbTypeOffset+4], typ)
	copy(out[pdbCreatorOffset:pdbCreatorOffset+4], creator)
	binary.BigEndian.PutUint16(out[pdbCountOffset:pdbCountOffset+2], count)

	tableEnd := pdbHeaderLen + pdbRecordInfoLen*n
	offset := tableEnd
	for i, rec := range records {
		entry := [8]byte{}
		off := uint32(offset)
		if over, ok := cfg.RecordOffsets[i]; ok {
			off = over
		}
		binary.BigEndian.PutUint32(entry[0:4], off)
		entry[5] = byte(i >> 16) // uniqueID, 3 bytes big-endian
		entry[6] = byte(i >> 8)
		entry[7] = byte(i)
		out = append(out, entry[:]...)
		offset += len(rec)
	}
	for _, rec := range records {
		out = append(out, rec...)
	}

	if cfg.Truncate > 0 && cfg.Truncate < len(out) {
		out = out[:cfg.Truncate]
	}
	return out
}

func totalLen(records [][]byte) int {
	var n int
	for _, r := range records {
		n += len(r)
	}
	return n
}

// PalmDB layout constants, mirrored from the parser for fixture writing.
const (
	pdbHeaderLen     = 78
	pdbRecordInfoLen = 8
	pdbTypeOffset    = 60
	pdbCreatorOffset = 64
	pdbCountOffset   = 76
)
