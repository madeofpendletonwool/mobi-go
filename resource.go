// Resource record extraction: raster images (including the cover),
// obfuscated/compressed FONT records, and VIDE/AUDI payloads.
//
// Ported with attribution from KindleUnpack's processFONT and
// CoverProcessor (lib/kindleunpack.py, lib/mobi_cover.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// loadResource / getFont / FONT_HEADER / MOBI6.loadRecindex / getCover
// (mobi.js, MIT, https://github.com/johnfactotum/foliate-js) as the
// structural cross-check.

package mobi

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// ErrNoCover reports a book with no cover image: neither EXTH 201
// (coverOffset) nor EXTH 202 (thumbnailOffset) is present and valid.
// Callers treat it as non-fatal — plenty of books simply have no cover.
var ErrNoCover = errors.New("mobi: no cover image")

// Resource record magics, probed with RecordMagic (stage 2). The INDX
// magic also marks the trailing index records the TOC stage consumes;
// RESC marks the KF8 spine-properties record.
var (
	identFONT = [4]byte{'F', 'O', 'N', 'T'}
	identVIDE = [4]byte{'V', 'I', 'D', 'E'}
	identAUDI = [4]byte{'A', 'U', 'D', 'I'}
	identINDX = [4]byte{'I', 'N', 'D', 'X'}
	identRESC = [4]byte{'R', 'E', 'S', 'C'}
)

// FONT record layout, beyond the shared 4-byte magic. Header fields
// per KindleUnpack's processFONT: flags @8, payload start @12, key
// length @16, key start @20. Flag bits: 0x1 zlib-compressed payload,
// 0x2 XOR-obfuscated payload.
const (
	fontHeaderLen = 24 // magic + five uint32s
	fontFlags     = 8
	fontDataStart = 12
	fontKeyLength = 16
	fontKeyStart  = 20

	fontFlagCompressed uint32 = 0x1
	fontFlagObfuscated uint32 = 0x2
)

// Resource returns the book's resource record i, counted 0-based from
// the MOBI header's firstImageIndex — the same arithmetic foliate-js's
// loadResource uses (record firstImageIndex + i). Records are decoded
// per their magic: FONT records are deobfuscated and inflated, VIDE and
// AUDI records lose their 12-byte header, and everything else is
// returned raw. MIME types are sniffed from the decoded bytes (image
// magic only); non-image payloads report application/octet-stream.
//
// The returned data may alias the book's internal buffer; callers must
// not modify it.
func (b *Book) Resource(i int) ([]byte, string, error) {
	start := int(b.mobi.FirstImageIndex)
	if start < 0 {
		return nil, "", fmt.Errorf("%w: file declares no resource records",
			ErrRecordRange)
	}
	count := b.resourceEnd() - start
	if i < 0 || count <= 0 || i >= count {
		return nil, "", fmt.Errorf("%w: resource %d, file has %d resources",
			ErrRecordRange, i, max(count, 0))
	}
	rec, err := b.pdb.Record(start + i)
	if err != nil {
		return nil, "", err
	}
	data, err := decodeResourceRecord(rec)
	if err != nil {
		return nil, "", fmt.Errorf("resource %d: %w", i, err)
	}
	return data, sniffImageMIME(data), nil
}

// ResolveRecindex resolves the 1-based recindex attribute value carried
// by MOBI6 HTML — <img recindex="00042"> — to its resource. The
// mediarecindex variant on audio/video elements uses the same
// arithmetic (foliate-js routes both through loadRecindex). recindex 1
// is the first resource record; values below 1 or past the resource
// range fail with ErrRecordRange.
func (b *Book) ResolveRecindex(n int) ([]byte, string, error) {
	return b.Resource(n - 1)
}

// Cover returns the book's cover image. The EXTH coverOffset record
// (type 201) names the resource index when it holds any value other
// than the 0xFFFFFFFF sentinel; otherwise thumbnailOffset (type 202)
// is tried the same way, matching both port sources. A book with
// neither returns ErrNoCover, which callers should treat as non-fatal.
func (b *Book) Cover() ([]byte, string, error) {
	offset := -1
	if b.exth != nil {
		if v, ok := b.exth.uint(exthCoverOffset); ok && v != indexAbsent {
			offset = int(v)
		} else if v, ok := b.exth.uint(exthThumbnailOffset); ok && v != indexAbsent {
			offset = int(v)
		}
	}
	if offset < 0 {
		return nil, "", ErrNoCover
	}
	data, mime, err := b.Resource(offset)
	if err != nil {
		return nil, "", fmt.Errorf("cover resource %d: %w", offset, err)
	}
	return data, mime, nil
}

// NumResources returns the number of resource records: everything from
// firstImageIndex to the end of the resource run. Zero when the book
// declares no resources.
func (b *Book) NumResources() int {
	if b.mobi.FirstImageIndex < 0 {
		return 0
	}
	return max(b.resourceEnd()-int(b.mobi.FirstImageIndex), 0)
}

// resourceEnd returns the exclusive record index one past the last
// resource record. Resources nominally run from firstImageIndex to the
// end of the active half, but trailing index records belong to later
// stages: when the MOBI header's indx field names their position it
// bounds the run, and otherwise records are probed by magic so a
// trailing INDX run is not reported as resources. Combo views cap
// before the BOUNDARY record — the KF8 half's shared images live in
// the MOBI6 half (the header's 0x0800 "shared resources" flag), the
// MOBI6 half's records stop before the BOUNDARY record itself, and
// KindleUnpack's resource loops end at the boundary record exclusive
// (it is a placeholder slot, never a resource).
func (b *Book) resourceEnd() int {
	end := b.pdb.NumRecords()
	if b.boundary >= 0 {
		end = min(end, b.boundary-1)
	}
	if b.m6End > 0 {
		end = min(end, b.m6End)
	}
	start := int(b.mobi.FirstImageIndex)
	if b.mobi.Indx >= 0 {
		if indx := b.start + int(b.mobi.Indx); indx > start && indx < end {
			return indx
		}
	}
	for p := start; p < end; p++ {
		if magic, err := b.pdb.RecordMagic(p); err == nil && magic == identINDX {
			return p
		}
	}
	return end
}

// decodeResourceRecord turns one raw resource record into its payload,
// dispatching on the record's magic. Slices alias rec except where a
// transform requires a copy (fonts); callers must not modify the
// result.
func decodeResourceRecord(rec []byte) ([]byte, error) {
	var magic [4]byte
	copy(magic[:], rec)
	switch magic {
	case identFONT:
		return decodeFontRecord(rec)
	case identVIDE, identAUDI:
		// Media payloads follow a 12-byte sub-header (magic plus
		// two reserved words); the bytes themselves are raw.
		if len(rec) < 12 {
			return nil, fmt.Errorf("%w: %q record is %d bytes, shorter than its 12-byte header",
				ErrCorrupt, string(magic[:]), len(rec))
		}
		return rec[12:], nil
	default:
		return rec, nil
	}
}

// decodeFontRecord recovers a FONT record's payload: a copy of the
// bytes from the header's data start, XOR-deobfuscated when flag 0x2
// is set (the first 1024 bytes for 16-byte keys, 1040 otherwise —
// foliate-js and KindleUnpack agree), then zlib-inflated when flag 0x1
// is set. The copy is forced: deobfuscation writes into the payload,
// and record slices alias the book's buffered file.
func decodeFontRecord(rec []byte) ([]byte, error) {
	if len(rec) < fontHeaderLen {
		return nil, fmt.Errorf("%w: FONT record is %d bytes, shorter than its %d-byte header",
			ErrCorrupt, len(rec), fontHeaderLen)
	}
	flags := be32(rec, fontFlags)
	dataStart := be32(rec, fontDataStart)
	keyLength := be32(rec, fontKeyLength)
	keyStart := be32(rec, fontKeyStart)
	if int64(dataStart) > int64(len(rec)) {
		return nil, fmt.Errorf("%w: font data starts at %d, past the %d-byte record",
			ErrCorrupt, dataStart, len(rec))
	}
	data := append([]byte(nil), rec[dataStart:]...)

	if flags&fontFlagObfuscated != 0 {
		if keyLength == 0 {
			return nil, fmt.Errorf("%w: obfuscated font with a zero-length key", ErrCorrupt)
		}
		if int64(keyStart)+int64(keyLength) > int64(len(rec)) {
			return nil, fmt.Errorf("%w: font key spans [%d, %d), past the %d-byte record",
				ErrCorrupt, keyStart, keyStart+keyLength, len(rec))
		}
		key := rec[keyStart:][:keyLength]
		n := 1040
		if keyLength == 16 {
			n = 1024
		}
		n = min(n, len(data))
		for i := range n {
			data[i] ^= key[i%int(keyLength)]
		}
	}

	if flags&fontFlagCompressed != 0 {
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: font zlib stream: %v", ErrCorrupt, err)
		}
		out, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: font zlib stream: %v", ErrCorrupt, err)
		}
		data = out
	}
	return data, nil
}

// sniffImageMIME reports the MIME type of data from its leading magic
// bytes alone: the four raster formats MOBI6 books carry. Everything
// else — fonts, media payloads, unknown records — is
// application/octet-stream.
func sniffImageMIME(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case bytes.HasPrefix(data, []byte("BM")):
		return "image/bmp"
	}
	return "application/octet-stream"
}
