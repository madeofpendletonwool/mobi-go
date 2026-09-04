// PalmDB (PDB) container parsing.
//
// Ported with attribution from KindleUnpack's Sectionizer
// (lib/mobi_sectioner.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's PDB
// class and isMOBI (mobi.js, MIT, https://github.com/johnfactotum/foliate-js)
// as the structural cross-check.

package mobi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Errors returned when opening or reading a PalmDB container. Every parse
// failure wraps one of these sentinels; use errors.Is, never string
// matching.
var (
	// ErrNotPalmDB reports a file that is not a PalmDB ebook container:
	// too short for the 78-byte header, or a type/creator pair other
	// than BOOK/MOBI or TEXt/REAd.
	ErrNotPalmDB = errors.New("mobi: not a PalmDB file")
	// ErrCorrupt reports a structurally invalid container: a zero or
	// absurd record count, a record table that does not fit in the
	// file, non-monotonic or out-of-range record offsets, or data that
	// ends before the claimed size.
	ErrCorrupt = errors.New("mobi: corrupt PalmDB container")
	// ErrRecordRange reports a record index outside [0, NumRecords).
	ErrRecordRange = errors.New("mobi: record index out of range")
)

// PalmDB layout constants (all integers big-endian).
const (
	pdbHeaderLen     = 78 // fixed header before the record info list
	pdbRecordInfoLen = 8  // one entry in the record info list
	pdbNameLen       = 32 // NUL-padded database name at offset 0
	pdbTypeOffset    = 60 // 4-byte type
	pdbCreatorOffset = 64 // 4-byte creator
	pdbCountOffset   = 76 // uint16 record count
	pdbTableOffset   = 78 // start of the record info list
)

// Accepted type+creator pairs, concatenated as they appear at offset 60.
var (
	identBOOKMOBI = [8]byte{'B', 'O', 'O', 'K', 'M', 'O', 'B', 'I'}
	identTEXtREAd = [8]byte{'T', 'E', 'X', 't', 'R', 'E', 'A', 'd'}
)

// openPDB reads the whole file into memory and validates the container.
// The eager copy is deliberate: ebooks are small (a big one is a few MB)
// and record access then costs nothing, which every later parsing stage
// relies on. Record slices returned by Record alias that copy; callers
// must not modify them.
func openPDB(r io.ReaderAt, size int64) (*pdbFile, error) {
	if size < pdbHeaderLen {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the %d-byte header",
			ErrNotPalmDB, size, pdbHeaderLen)
	}
	data := make([]byte, size)
	if n, err := r.ReadAt(data, 0); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: data ends after %d of %d bytes",
				ErrCorrupt, n, size)
		}
		return nil, fmt.Errorf("mobi: reading PalmDB data: %w", err)
	}

	var ident [8]byte
	copy(ident[:], data[pdbTypeOffset:pdbCreatorOffset+4])
	if ident != identBOOKMOBI && ident != identTEXtREAd {
		return nil, fmt.Errorf("%w: type/creator is %q, want BOOK/MOBI or TEXt/REAd",
			ErrNotPalmDB, string(ident[:]))
	}

	numRecords := int(binary.BigEndian.Uint16(data[pdbCountOffset:pdbTableOffset]))
	if numRecords == 0 {
		return nil, fmt.Errorf("%w: record count is zero", ErrCorrupt)
	}
	tableEnd := pdbTableOffset + numRecords*pdbRecordInfoLen
	if int64(tableEnd) > size {
		return nil, fmt.Errorf("%w: record table (%d entries) ends at %d, past file size %d",
			ErrCorrupt, numRecords, tableEnd, size)
	}

	// Record i spans [offsets[i], offsets[i+1]); the sentinel end offset
	// is the file size, so the last record runs to EOF (KindleUnpack
	// appends filelength the same way).
	offsets := make([]int64, numRecords+1)
	for i := range numRecords {
		offsets[i] = int64(binary.BigEndian.Uint32(data[pdbTableOffset+i*pdbRecordInfoLen:][:4]))
	}
	offsets[numRecords] = size

	if offsets[0] < int64(tableEnd) {
		return nil, fmt.Errorf("%w: first record offset %d is inside the record table (ends at %d)",
			ErrCorrupt, offsets[0], tableEnd)
	}
	for i := 1; i < numRecords; i++ {
		if offsets[i] < offsets[i-1] {
			return nil, fmt.Errorf("%w: record %d offset %d precedes record %d offset %d",
				ErrCorrupt, i, offsets[i], i-1, offsets[i-1])
		}
	}
	for i, off := range offsets[:numRecords] {
		if off > size {
			return nil, fmt.Errorf("%w: record %d offset %d is past file size %d",
				ErrCorrupt, i, off, size)
		}
	}

	return &pdbFile{
		name:    palmName(data[:pdbNameLen]),
		ident:   ident,
		data:    data,
		offsets: offsets,
	}, nil
}

// pdbFile is an open PalmDB container. The 8-byte ident is the type and
// creator fields concatenated (BOOKMOBI or TEXtREAd).
type pdbFile struct {
	name    string
	ident   [8]byte
	data    []byte
	offsets []int64
}

// Name returns the database name from the header, NUL padding stripped.
// It is mostly redundant with the MOBI title but kept for fidelity.
func (p *pdbFile) Name() string { return p.name }

// NumRecords returns the number of records in the container.
func (p *pdbFile) NumRecords() int { return len(p.offsets) - 1 }

// Record returns the bytes of record i. The slice aliases the internal
// buffered copy of the file; callers must not modify it.
func (p *pdbFile) Record(i int) ([]byte, error) {
	if i < 0 || i >= p.NumRecords() {
		return nil, fmt.Errorf("%w: %d, file has %d records",
			ErrRecordRange, i, p.NumRecords())
	}
	return p.data[p.offsets[i]:p.offsets[i+1]], nil
}

// RecordMagic returns the first four bytes of record i, for probing
// record types (MOBI, EXTH, FDST, RESC, INDX, ...). A record shorter
// than four bytes is zero-padded.
func (p *pdbFile) RecordMagic(i int) ([4]byte, error) {
	rec, err := p.Record(i)
	if err != nil {
		return [4]byte{}, err
	}
	var magic [4]byte
	copy(magic[:], rec)
	return magic, nil
}

// palmName decodes the 32-byte NUL-padded header name field.
func palmName(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(string(b), " ")
}
