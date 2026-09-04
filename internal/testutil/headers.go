// Record-0 fixture builder: the PalmDOC header, the MOBI header, an
// optional EXTH block, and the trailing title, laid out exactly as
// the parser expects. Compose with Build/BuildWith for whole files.

package testutil

import "encoding/binary"

// Record-0 layout constants, mirrored from the parser for fixture
// writing.
const (
	r0MOBIMagic        = 16
	r0MOBILength       = 20
	r0MOBIType         = 24
	r0MOBIEncoding     = 28
	r0MOBIUID          = 32
	r0MOBIVersion      = 36
	r0TitleOffsetField = 84
	r0TitleLengthField = 88
	r0LocaleRegion     = 94
	r0LocaleLanguage   = 95
	r0FirstImageIndex  = 108
	r0Huffcdic         = 112
	r0NumHuffcdic      = 116
	r0EXTHFlag         = 128
	r0DRMOffset        = 168
	r0DRMCount         = 172
	r0FDST             = 192
	r0NumFDST          = 196
	r0TrailingFlags    = 240
	r0Indx             = 244
	r0Frag             = 248
	r0Skel             = 252
	r0Guide            = 260

	r0Absent      = 0xFFFFFFFF
	r0EXTHPresent = 0x40
)

// EXTHRecord is one EXTH record for Record0Config.EXTH.
type EXTHRecord struct {
	Type uint32
	Data []byte
}

// EXTHString builds a string-valued EXTH record.
func EXTHString(typ uint32, s string) EXTHRecord {
	return EXTHRecord{Type: typ, Data: []byte(s)}
}

// EXTHUint builds a 4-byte big-endian uint-valued EXTH record.
func EXTHUint(typ uint32, v uint32) EXTHRecord {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return EXTHRecord{Type: typ, Data: b}
}

// U32 returns a pointer to v, for Record0Config's optional index
// fields; a nil field writes the 0xFFFFFFFF "absent" sentinel.
func U32(v uint32) *uint32 { return &v }

// BuildEXTH renders records as a standalone EXTH block: magic,
// 4-byte-aligned length, count, then the records.
func BuildEXTH(records ...EXTHRecord) []byte {
	n := exthHeaderLen
	for _, r := range records {
		n += 8 + len(r.Data)
	}
	n = (n + 3) &^ 3
	out := make([]byte, n)
	copy(out, "EXTH")
	binary.BigEndian.PutUint32(out[4:8], uint32(n))
	binary.BigEndian.PutUint32(out[8:12], uint32(len(records)))
	pos := exthHeaderLen
	for _, r := range records {
		binary.BigEndian.PutUint32(out[pos:pos+4], r.Type)
		binary.BigEndian.PutUint32(out[pos+4:pos+8], uint32(8+len(r.Data)))
		copy(out[pos+8:], r.Data)
		pos += 8 + len(r.Data)
	}
	return out
}

const exthHeaderLen = 12

// Record0Config tweaks the record-0 bytes BuildRecord0 produces. The
// zero value builds a valid version-6 MOBI record 0 with no EXTH and
// no title; only the fields a test cares about need setting.
type Record0Config struct {
	// PalmDOC header. Compression defaults to 1 (none) and RecordSize
	// to 4096; Encryption is the DRM fixture knob.
	Compression    uint16
	TextLength     uint32
	NumTextRecords uint16
	RecordSize     uint16
	Encryption     uint16

	// MOBI header. MOBILength 0 picks the default for the version
	// (232 for v6/v7, 264 for v8). Encoding defaults to 65001,
	// Version to 6, MOBIType to 2, locale to en (9) / en-US (region
	// byte 4).
	MOBIType       uint32
	Encoding       uint32
	UID            uint32
	Version        uint32
	MOBILength     uint32
	LocaleLanguage uint8
	LocaleRegion   uint8

	// Optional index fields; nil writes the 0xFFFFFFFF sentinel.
	FirstImageIndex *uint32
	Huffcdic        *uint32
	Indx            *uint32
	DRMOffset       *uint32
	FDST            *uint32
	Frag            *uint32
	Skel            *uint32
	Guide           *uint32

	// Counts and flags, written as given (zero default).
	NumHuffcdic   uint32
	ExtraEXTHFlag uint32 // OR'd into the EXTH flag; bit 0x40 is automatic
	TrailingFlags uint32
	DRMCount      uint32
	NumFDST       uint32

	// Content after the headers: the EXTH block (when non-empty) then
	// the title. TitleOffset/TitleLength are computed and written.
	Title string
	EXTH  []EXTHRecord
}

// BuildRecord0 renders record 0 per cfg.
func BuildRecord0(cfg Record0Config) []byte {
	compression := cfg.Compression
	if compression == 0 {
		compression = 1
	}
	recordSize := cfg.RecordSize
	if recordSize == 0 {
		recordSize = 4096
	}
	mobiType := cfg.MOBIType
	if mobiType == 0 {
		mobiType = 2
	}
	encoding := cfg.Encoding
	if encoding == 0 {
		encoding = 65001
	}
	version := cfg.Version
	if version == 0 {
		version = 6
	}
	mlen := cfg.MOBILength
	if mlen == 0 {
		mlen = 232
		if version >= 8 {
			mlen = 264
		}
	}
	// The locale zero values are en / region byte 4 (en-US).
	localeLanguage, localeRegion := cfg.LocaleLanguage, cfg.LocaleRegion
	if localeLanguage == 0 {
		localeLanguage = 9
	}
	if localeRegion == 0 {
		localeRegion = 4
	}

	var exth []byte
	if len(cfg.EXTH) > 0 {
		exth = BuildEXTH(cfg.EXTH...)
	}
	exthOff := r0MOBIMagic + int(mlen)
	titleOff := exthOff + len(exth)
	title := []byte(cfg.Title)

	rec := make([]byte, titleOff+len(title))
	// covered reports whether the header length covers the field at
	// absolute offset off of size bytes; fields past a short header
	// stay zero (the record itself only grows to 16+mlen+content).
	covered := func(off, size int) bool {
		return 16+int(mlen) >= off+size
	}
	binary.BigEndian.PutUint16(rec[0:], compression)
	binary.BigEndian.PutUint32(rec[4:], cfg.TextLength)
	binary.BigEndian.PutUint16(rec[8:], cfg.NumTextRecords)
	binary.BigEndian.PutUint16(rec[10:], recordSize)
	binary.BigEndian.PutUint16(rec[12:], cfg.Encryption)
	// @14 unknown stays zero.

	copy(rec[r0MOBIMagic:], "MOBI")
	put32(rec, r0MOBILength, mlen)
	if covered(r0MOBIType, 4) {
		put32(rec, r0MOBIType, mobiType)
	}
	if covered(r0MOBIEncoding, 4) {
		put32(rec, r0MOBIEncoding, encoding)
	}
	if covered(r0MOBIUID, 4) {
		put32(rec, r0MOBIUID, cfg.UID)
	}
	if covered(r0MOBIVersion, 4) {
		put32(rec, r0MOBIVersion, version)
	}
	if covered(r0TitleOffsetField, 4) {
		put32(rec, r0TitleOffsetField, uint32(titleOff))
	}
	if covered(r0TitleLengthField, 4) {
		put32(rec, r0TitleLengthField, uint32(len(title)))
	}
	if covered(r0LocaleRegion, 1) {
		rec[r0LocaleRegion] = localeRegion
	}
	if covered(r0LocaleLanguage, 1) {
		rec[r0LocaleLanguage] = localeLanguage
	}
	if covered(r0FirstImageIndex, 4) {
		putOpt(rec, r0FirstImageIndex, cfg.FirstImageIndex)
	}
	if covered(r0Huffcdic, 4) {
		putOpt(rec, r0Huffcdic, cfg.Huffcdic)
	}
	if covered(r0NumHuffcdic, 4) {
		put32(rec, r0NumHuffcdic, cfg.NumHuffcdic)
	}
	flag := cfg.ExtraEXTHFlag
	if len(exth) > 0 {
		flag |= r0EXTHPresent
	}
	if covered(r0EXTHFlag, 4) {
		put32(rec, r0EXTHFlag, flag)
	}
	if covered(r0DRMOffset, 4) {
		putOpt(rec, r0DRMOffset, cfg.DRMOffset)
	}
	if covered(r0DRMCount, 4) {
		put32(rec, r0DRMCount, cfg.DRMCount)
	}
	if covered(r0TrailingFlags, 4) {
		put32(rec, r0TrailingFlags, cfg.TrailingFlags)
	}
	if covered(r0Indx, 4) {
		putOpt(rec, r0Indx, cfg.Indx)
	}
	if version >= 8 {
		if covered(r0FDST, 4) {
			putOpt(rec, r0FDST, cfg.FDST)
		}
		if covered(r0NumFDST, 4) {
			put32(rec, r0NumFDST, cfg.NumFDST)
		}
		if covered(r0Frag, 4) {
			putOpt(rec, r0Frag, cfg.Frag)
		}
		if covered(r0Skel, 4) {
			putOpt(rec, r0Skel, cfg.Skel)
		}
		if covered(r0Guide, 4) {
			putOpt(rec, r0Guide, cfg.Guide)
		}
	}

	copy(rec[exthOff:], exth)
	copy(rec[titleOff:], title)
	return rec
}

func put32(b []byte, off int, v uint32) { binary.BigEndian.PutUint32(b[off:], v) }

func putOpt(b []byte, off int, v *uint32) {
	if v == nil {
		put32(b, off, r0Absent)
		return
	}
	put32(b, off, *v)
}
