// EXTH metadata block parsing and book metadata resolution.
//
// Ported with attribution from foliate-js's getEXTH and EXTH record
// type table (mobi.js, MIT, https://github.com/johnfactotum/foliate-js),
// with KindleUnpack's parseMetaData (lib/mobi_header.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack) as the
// field-by-field reference.

package mobi

import (
	"encoding/binary"
	"fmt"
	"html"
)

// EXTH record types carried in the metadata block. Types marked
// "multi" may legally appear more than once; values decode as strings
// in the book's codepage unless noted. Raw numeric records are stored
// big-endian with 1, 2, or 4 bytes of payload.
const (
	exthAuthor             uint32 = 100 // multi
	exthPublisher          uint32 = 101
	exthDescription        uint32 = 103
	exthISBN               uint32 = 104
	exthSubject            uint32 = 105 // multi
	exthDate               uint32 = 106
	exthContributor        uint32 = 108 // multi
	exthRights             uint32 = 109
	exthASIN               uint32 = 113
	exthBoundary           uint32 = 121 // uint: combo-file KF8 offset
	exthNumResources       uint32 = 125 // uint
	exthOriginalResolution uint32 = 126
	exthCoverURI           uint32 = 129
	exthCoverOffset        uint32 = 201 // uint
	exthThumbnailOffset    uint32 = 202 // uint
	exthTitle              uint32 = 503
	exthLanguage           uint32 = 524 // multi
)

// exthHeaderLen is the EXTH block's fixed prefix: magic, length,
// count.
const exthHeaderLen = 12

// exthBlock is a parsed EXTH metadata block from record 0. Entries are
// kept in file order with their raw bytes — unknown record types are
// preserved, never dropped. All data slices alias record 0.
type exthBlock struct {
	offset  int // absolute offset of the block within record 0
	length  uint32
	entries []exthEntry
}

// exthEntry is one raw EXTH record.
type exthEntry struct {
	typ  uint32
	data []byte
}

// All returns the raw data of every record of the given type, in file
// order.
func (e *exthBlock) All(typ uint32) [][]byte {
	var out [][]byte
	for _, rec := range e.entries {
		if rec.typ == typ {
			out = append(out, rec.data)
		}
	}
	return out
}

// last returns the raw data of the last record of the given type —
// the value single-valued types resolve to (both port sources
// overwrite on repeats).
func (e *exthBlock) last(typ uint32) ([]byte, bool) {
	var found []byte
	ok := false
	for _, rec := range e.entries {
		if rec.typ == typ {
			found, ok = rec.data, true
		}
	}
	return found, ok
}

// uint returns the unsigned integer value of the last record of typ,
// reading 4, 2, or 1 bytes of payload as stored.
func (e *exthBlock) uint(typ uint32) (uint32, bool) {
	data, ok := e.last(typ)
	if !ok {
		return 0, false
	}
	switch len(data) {
	case 4:
		return binary.BigEndian.Uint32(data), true
	case 2:
		return uint32(binary.BigEndian.Uint16(data)), true
	case 1:
		return uint32(data[0]), true
	}
	return 0, false
}

// parseEXTH parses the EXTH block at absolute offset off in record 0.
// The block sits at 16 + MOBI header length whenever the exthFlag
// 0x40 bit is set.
func parseEXTH(rec []byte, off int) (*exthBlock, error) {
	if off+exthHeaderLen > len(rec) {
		return nil, fmt.Errorf("%w: EXTH header at %d overflows the %d-byte record 0",
			ErrCorrupt, off, len(rec))
	}
	if string(rec[off:off+4]) != "EXTH" {
		return nil, fmt.Errorf("%w: EXTH magic is %q",
			ErrCorrupt, rec[off:off+4])
	}
	length := be32(rec, off+4)
	count := be32(rec, off+8)
	if length < exthHeaderLen {
		return nil, fmt.Errorf("%w: EXTH length %d is smaller than its %d-byte header",
			ErrCorrupt, length, exthHeaderLen)
	}
	if int64(off)+int64(length) > int64(len(rec)) {
		return nil, fmt.Errorf("%w: EXTH block spans [%d, %d), past the %d-byte record 0",
			ErrCorrupt, off, int64(off)+int64(length), len(rec))
	}
	// KindleUnpack rounds the declared length up to a 4-byte boundary
	// before slicing; tolerate the padding bytes being absent.
	end := int64(off) + (int64(length)+3)&^3
	if end > int64(len(rec)) {
		end = int64(len(rec))
	}

	block := &exthBlock{offset: off, length: length}
	pos := int64(off) + exthHeaderLen
	for i := uint32(0); i < count; i++ {
		if pos+8 > end {
			return nil, fmt.Errorf("%w: EXTH record %d header overflows the block", ErrCorrupt, i)
		}
		typ := be32(rec, int(pos))
		size := be32(rec, int(pos+4))
		if size < 8 || pos+int64(size) > end {
			return nil, fmt.Errorf("%w: EXTH record %d (type %d) spans %d bytes, overflowing the block",
				ErrCorrupt, i, typ, size)
		}
		block.entries = append(block.entries, exthEntry{
			typ:  typ,
			data: rec[pos+8 : pos+int64(size)],
		})
		pos += int64(size)
	}
	return block, nil
}

// Metadata is the resolved book metadata. Fields without a source in
// the file come back zero.
type Metadata struct {
	Title        string
	Authors      []string
	Language     string
	Published    string // EXTH 106, the publication date string
	Publisher    string
	ISBN         string
	Description  string
	Rights       string
	ASIN         string
	Subjects     []string
	Contributors []string
}

// Metadata resolves the book's metadata. EXTH values take precedence
// over the MOBI header (title, language); every string is decoded in
// the book's codepage and HTML-unescaped.
func (b *Book) Metadata() Metadata {
	var md Metadata
	if b.exth != nil {
		decode := func(data []byte) string {
			return html.UnescapeString(decodeString(b.mobi.Encoding, data))
		}
		str := func(typ uint32) string {
			if data, ok := b.exth.last(typ); ok {
				return decode(data)
			}
			return ""
		}
		all := func(typ uint32) []string {
			var out []string
			for _, data := range b.exth.All(typ) {
				out = append(out, decode(data))
			}
			return out
		}
		md.Title = str(exthTitle)
		md.Authors = all(exthAuthor)
		md.Language = str(exthLanguage)
		md.Published = str(exthDate)
		md.Publisher = str(exthPublisher)
		md.ISBN = str(exthISBN)
		md.Description = str(exthDescription)
		md.Rights = str(exthRights)
		md.ASIN = str(exthASIN)
		md.Subjects = all(exthSubject)
		md.Contributors = all(exthContributor)
	}
	if md.Title == "" && len(b.title) > 0 {
		md.Title = html.UnescapeString(decodeString(b.mobi.Encoding, b.title))
	}
	if md.Language == "" {
		md.Language = mobiLocale(b.mobi.LocaleLanguage, b.mobi.LocaleRegion)
	}
	return md
}
