// Whole-book fixture builder: splits a text into records, compresses
// and decorates them per the config, and wraps record 0 around them.
// Every text-stage test composes from here.

package testutil

import "math/bits"

// BookConfig tweaks the MOBI6 files BuildBook produces. The zero value
// builds a valid uncompressed version-6 book around the given text.
type BookConfig struct {
	// Compression: 1 none (default), 2 PalmDOC.
	Compression uint16

	// Encoding: 65001 default, or 1252.
	Encoding uint32

	// Version defaults to 6.
	Version uint32

	// RecordSize caps each text record's uncompressed size (default
	// 4096); small values force multi-record books.
	RecordSize uint16

	// TrailingFlags: bit 0 adds multibyte overlap bytes, each set bit
	// above that adds one varlen-sized trailing data entry, laid out
	// so the stripper removes exactly what was added.
	TrailingFlags uint32

	// Text is the full uncompressed book text (HTML), byte-exact as
	// RawText must return it.
	Text string

	// Resources are appended after the text records as one contiguous
	// run; FirstImageIndex is computed and written to point at the
	// first one. Absent when empty.
	Resources [][]byte

	// TrailingRecords are appended after the resources without being
	// counted as resources — INDX fixtures for boundary tests.
	TrailingRecords [][]byte

	// Indx, when non-nil, is written into the MOBI header's indx field
	// (the absolute record index of the first INDX record).
	Indx *uint32

	// Guide, when non-nil, is written into the KF8-only guide field
	// (the absolute record index of the guide INDX). Only written for
	// version >= 8 books.
	Guide *uint32

	// Record-0 extras, passed through to BuildRecord0.
	Title string
	EXTH  []EXTHRecord
}

// BuildBook renders cfg as complete file bytes, ready for Open.
func BuildBook(cfg BookConfig) []byte {
	rec0, records := BuildBookParts(cfg)
	return Build(append([][]byte{rec0}, records...)...)
}

// BuildBookParts renders cfg into record 0 and the text records, for
// tests that need to mutate the pieces before wrapping the container
// (record-count lies, corrupt records, wrong textLength).
func BuildBookParts(cfg BookConfig) ([]byte, [][]byte) {
	compression := cfg.Compression
	if compression == 0 {
		compression = 1
	}
	recordSize := int(cfg.RecordSize)
	if recordSize == 0 {
		recordSize = 4096
	}
	encoding := cfg.Encoding
	if encoding == 0 {
		encoding = 65001
	}
	version := cfg.Version
	if version == 0 {
		version = 6
	}

	text := []byte(cfg.Text)
	var records [][]byte
	for start := 0; ; start += recordSize {
		end := min(start+recordSize, len(text))
		rec := append([]byte(nil), text[start:end]...)
		if compression == 2 {
			rec = CompressPalmDOC(rec)
		}
		// Trailing bookkeeping rides after the compressed payload: the
		// stripper removes it from the compressed record before
		// decompression (the ordering both port sources share).
		rec = AppendTrailingEntries(rec, cfg.TrailingFlags)
		records = append(records, rec)
		if end >= len(text) {
			break
		}
	}

	numText := len(records)
	var firstImage *uint32
	if len(cfg.Resources) > 0 {
		firstImage = U32(uint32(1 + numText))
	}
	records = append(records, cfg.Resources...)
	records = append(records, cfg.TrailingRecords...)

	rec0 := BuildRecord0(Record0Config{
		Compression:     compression,
		TextLength:      uint32(len(text)),
		NumTextRecords:  uint16(numText),
		RecordSize:      cfg.RecordSize,
		Encoding:        encoding,
		Version:         version,
		TrailingFlags:   cfg.TrailingFlags,
		FirstImageIndex: firstImage,
		Indx:            cfg.Indx,
		Guide:           cfg.Guide,
		Title:           cfg.Title,
		EXTH:            cfg.EXTH,
	})
	return rec0, records
}

// AppendTrailingEntries lays out the per-record trailing bookkeeping
// the stripper removes: multibyte overlap bytes first, then one
// varlen-sized trailing data entry per flag bit, entries outermost —
// the exact reverse of the strip order (entries from the end first,
// then the multibyte bytes). The multibyte pair is two bytes whose
// final byte's low bits encode the count 2; each entry is four marker
// bytes plus the one-byte varlen size 5 (which counts itself).
func AppendTrailingEntries(rec []byte, flags uint32) []byte {
	if flags&1 != 0 {
		rec = append(rec, 0xEE, 0x01)
	}
	for range bits.OnesCount32(flags >> 1) {
		rec = append(rec, 0xDE, 0xAD, 0xBE, 0xEF, 0x85)
	}
	return rec
}
