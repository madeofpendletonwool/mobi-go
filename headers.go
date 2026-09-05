// Record-0 header chain: the 16-byte PalmDOC header, the MOBI header,
// and the DRM refusal gate.
//
// Ported with attribution from KindleUnpack's MobiHeader
// (lib/mobi_header.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// #getHeaders and header tables (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js) as the structural
// cross-check.

package mobi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Errors reported while parsing the record-0 header chain. Every parse
// failure wraps one of these sentinels (or the container sentinels in
// pdb.go); use errors.Is, never string matching.
var (
	// ErrDRM reports a file protected by DRM. DRM circumvention is
	// permanently out of scope: such files are refused whole, never
	// partially parsed. Backhog maps this error to media_skipped.
	ErrDRM = errors.New("mobi: DRM-protected file")
	// ErrUnsupportedCompression reports a PalmDOC compression type
	// other than none (1), PalmDOC (2), or HUFF/CDIC (17480).
	ErrUnsupportedCompression = errors.New("mobi: unsupported compression")
)

// Record-0 layout constants. All integers are big-endian; offsets are
// absolute within record 0. The MOBI header starts at offset 16 and
// its length counts from the "MOBI" magic.
const (
	palmdocHeaderLen = 16

	// PalmDOC header fields.
	palmdocCompression    = 0
	palmdocTextLength     = 4
	palmdocNumTextRecords = 8
	palmdocRecordSize     = 10
	palmdocEncryption     = 12
	palmdocUnknown        = 14

	// MOBI header fields.
	mobiMagicOffset      = 16
	mobiLengthOffset     = 20
	mobiTypeOffset       = 24
	mobiEncodingOffset   = 28
	mobiUIDOffset        = 32
	mobiVersionOffset    = 36
	mobiTitleOffsetField = 84
	mobiTitleLengthField = 88
	mobiLocaleRegion     = 94 // uint8
	mobiLocaleLanguage   = 95 // uint8
	mobiFirstImageIndex  = 108
	mobiHuffcdic         = 112
	mobiNumHuffcdic      = 116
	mobiEXTHFlag         = 128
	mobiDRMOffsetField   = 168
	mobiDRMCountField    = 172
	mobiFDST             = 192
	mobiNumFDST          = 196
	mobiTrailingFlags    = 240
	mobiIndx             = 244
	mobiFrag             = 248
	mobiSkel             = 252
	mobiGuide            = 260

	// mobiMinLength is the shortest MOBI header that still carries a
	// version field; anything shorter cannot be processed.
	mobiMinLength = 24

	// PalmDOC compression types.
	compressionNone     = 1
	compressionPalmDOC  = 2
	compressionHuffCDIC = 17480

	// exthFlagPresent is the exthFlag bit marking an EXTH block.
	exthFlagPresent = 0x40

	// indexAbsent is the sentinel stored in index fields to mean
	// "absent"; it parses as -1.
	indexAbsent = 0xFFFFFFFF
)

// palmDocHeader is the 16-byte PalmDOC header at the start of record 0.
type palmDocHeader struct {
	Compression    uint16 // 1 none, 2 PalmDOC, 17480 HUFF/CDIC
	TextLength     uint32 // uncompressed text size in bytes
	NumTextRecords uint16
	RecordSize     uint16 // text record size, 4096 in practice
	Encryption     uint16 // nonzero: the file carries DRM
	Unknown        uint16 // unused @14
}

// mobiHeader is the MOBI header starting at record-0 offset 16. Fields
// beyond the header's declared length stay at their zero value (index
// fields at their -1 sentinel); every read is guarded by that length.
type mobiHeader struct {
	Length          uint32 // bytes, counting from the "MOBI" magic
	Type            uint32
	Encoding        uint32 // 1252 (windows-1252) or 65001 (UTF-8)
	UID             uint32
	Version         uint32 // 6/7 MOBI6, 8 KF8
	TitleOffset     uint32 // absolute offset in record 0
	TitleLength     uint32
	LocaleRegion    uint8 // region list index is this byte >> 2
	LocaleLanguage  uint8 // key into the mobiLanguages table
	FirstImageIndex int32 // first resource record, -1 absent
	Huffcdic        int32 // first HUFF/CDIC record, -1 absent
	NumHuffcdic     uint32
	EXTHFlag        uint32
	DRMOffset       int32 // -1 absent
	DRMCount        uint32
	TrailingFlags   uint32
	Indx            int32 // NCX index, -1 absent
}

// kf8Header carries the KF8-only fields of the MOBI header, parsed
// when version >= 8 and reused by the KF8 reassembly stage.
type kf8Header struct {
	FDST    int32 // FDST record index, -1 absent
	NumFDST uint32
	Frag    int32 // fragment index, -1 absent
	Skel    int32 // skeleton index, -1 absent
	Guide   int32 // guide index, -1 absent
}

// Book is an opened MOBI or AZW3 (KF8) file.
//
// A Book is stateful and not safe for concurrent use.
type Book struct {
	pdb        *pdbFile
	palmdoc    palmDocHeader
	mobi       mobiHeader
	kf8        *kf8Header
	exth       *exthBlock
	title      []byte // raw MOBI full name, from titleOffset/titleLength
	rawText    []byte // assembled MOBI6 text / KF8 raw flow, byte-exact
	textLoaded bool

	// start is the record offset of the active half: 0 for plain
	// files, the combo boundary for the KF8 half of a combo file
	// (every index the headers name — text records, HUFF/CDIC,
	// FDST, INDX — counts from it). boundary is the combo boundary
	// record index, -1 when the file is not a combo. m6 retains the
	// MOBI6 half's original header parse for MOBI6Half. m6End caps
	// record scans on a MOBI6-half view (its records stop before
	// the combo's BOUNDARY record), -1 otherwise.
	start    int
	boundary int
	m6       *mobiHalf
	m6End    int

	// KF8 reassembly state, loaded eagerly at Open for version >= 8
	// (kf8.go).
	fdst          []fdstRange
	skels         []kf8Skel
	frags         []kf8Frag
	kf8Sections   []KF8Section
	sectionOfFrag []int // fragment row -> section index
	fragBySeq     map[int]int
	fragBase      int // flow-0 start: skeleton/fragment offsets count from it
	kf8Loaded     bool
	pageSpreads   map[int]string

	// huffcdic is the HUFF/CDIC decompressor, built lazily from the
	// records the MOBI header points at and cached for the book's
	// life. It memoizes expanded phrases into its dictionary as it
	// decodes.
	huffcdic func([]byte) ([]byte, error)

	// Lazily parsed TOC and guide, cached after their first call.
	toc              []TOCItem
	guide            []GuideEntry
	tocErr, guideErr error
	tocLoaded        bool
	guideLoaded      bool
}

// mobiHalf retains one half's header parse in a combo file.
type mobiHalf struct {
	palmdoc palmDocHeader
	mobi    mobiHeader
	kf8     *kf8Header
	exth    *exthBlock
	title   []byte
}

// Open parses the PalmDB container and the record-0 header chain of a
// MOBI or AZW3 file. DRM-protected files are refused with ErrDRM
// before any content is parsed.
//
// Both halves load eagerly, following the MOBI6 open in foliate-js: a
// book whose text records will not decompress is refused whole rather
// than half-opened. MOBI6 files assemble their full text; KF8 files
// decompress the raw flow and reassemble every section.
//
// Combo files (MOBI6 plus KF8, named by EXTH 121) open as their KF8
// half; HasMOBI6Half reports them and MOBI6Half returns the other
// view.
func Open(r io.ReaderAt, size int64) (*Book, error) {
	pdb, err := openPDB(r, size)
	if err != nil {
		return nil, err
	}
	rec0, err := pdb.Record(0)
	if err != nil {
		return nil, err
	}
	b := &Book{pdb: pdb, boundary: -1}
	if err := b.parseRecord0(rec0); err != nil {
		return nil, err
	}
	if b.mobi.Version < 8 {
		if boundary, ok := b.kf8Boundary(); ok {
			if err := b.openKF8Half(boundary); err != nil {
				return nil, err
			}
		}
	}
	if b.mobi.Version >= 8 {
		if err := b.loadKF8(); err != nil {
			return nil, err
		}
	} else if err := b.loadAllText(); err != nil {
		return nil, err
	}
	return b, nil
}

// IsKF8 reports whether the file is an AZW3/KF8 (MOBI version >= 8).
func (b *Book) IsKF8() bool { return b.mobi.Version >= 8 }

// parseRecord0 parses the record-0 header chain into b. Field order
// matters: the DRM gate runs before anything that could yield a
// partial book.
func (b *Book) parseRecord0(rec []byte) error {
	if len(rec) < palmdocHeaderLen {
		return fmt.Errorf("%w: record 0 is %d bytes, shorter than the %d-byte PalmDOC header",
			ErrCorrupt, len(rec), palmdocHeaderLen)
	}
	b.palmdoc = palmDocHeader{
		Compression:    be16(rec, palmdocCompression),
		TextLength:     be32(rec, palmdocTextLength),
		NumTextRecords: be16(rec, palmdocNumTextRecords),
		RecordSize:     be16(rec, palmdocRecordSize),
		Encryption:     be16(rec, palmdocEncryption),
		Unknown:        be16(rec, palmdocUnknown),
	}

	if len(rec) < mobiLengthOffset+4 {
		return fmt.Errorf("%w: record 0 is %d bytes, too short for the MOBI header",
			ErrCorrupt, len(rec))
	}
	if string(rec[mobiMagicOffset:mobiMagicOffset+4]) != "MOBI" {
		return fmt.Errorf("%w: record 0 magic is %q, want MOBI",
			ErrCorrupt, rec[mobiMagicOffset:mobiMagicOffset+4])
	}

	// DRM gate, part one: once the file is established as a MOBI book,
	// a nonzero PalmDOC encryption type means the whole file is
	// protected. Refuse before parsing anything else — this error must
	// never depend on, or be masked by, later damage.
	if b.palmdoc.Encryption != 0 {
		return fmt.Errorf("%w: PalmDOC encryption type %d",
			ErrDRM, b.palmdoc.Encryption)
	}

	m := mobiHeader{
		Length:          be32(rec, mobiLengthOffset),
		FirstImageIndex: -1,
		Huffcdic:        -1,
		DRMOffset:       -1,
		Indx:            -1,
	}
	if m.Length < mobiMinLength {
		return fmt.Errorf("%w: MOBI header length %d is shorter than the minimum %d",
			ErrCorrupt, m.Length, mobiMinLength)
	}
	if mobiMagicOffset+int64(m.Length) > int64(len(rec)) {
		return fmt.Errorf("%w: MOBI header ends at %d, past the %d-byte record 0",
			ErrCorrupt, mobiMagicOffset+int64(m.Length), len(rec))
	}
	headerEnd := mobiMagicOffset + int64(m.Length)

	// has reports whether the declared header length covers the field
	// at absolute record-0 offset off of size bytes.
	has := func(off, size int) bool {
		return headerEnd >= int64(off+size)
	}

	// These four sit within the minimum header length; read directly.
	m.Type = be32(rec, mobiTypeOffset)
	m.Encoding = be32(rec, mobiEncodingOffset)
	m.UID = be32(rec, mobiUIDOffset)
	m.Version = be32(rec, mobiVersionOffset)

	if has(mobiTitleOffsetField, 4) {
		m.TitleOffset = be32(rec, mobiTitleOffsetField)
	}
	if has(mobiTitleLengthField, 4) {
		m.TitleLength = be32(rec, mobiTitleLengthField)
	}
	if has(mobiLocaleRegion, 1) {
		m.LocaleRegion = rec[mobiLocaleRegion]
	}
	if has(mobiLocaleLanguage, 1) {
		m.LocaleLanguage = rec[mobiLocaleLanguage]
	}
	if has(mobiFirstImageIndex, 4) {
		m.FirstImageIndex = idxField(be32(rec, mobiFirstImageIndex))
	}
	if has(mobiHuffcdic, 4) {
		m.Huffcdic = idxField(be32(rec, mobiHuffcdic))
	}
	if has(mobiNumHuffcdic, 4) {
		m.NumHuffcdic = be32(rec, mobiNumHuffcdic)
	}
	if has(mobiEXTHFlag, 4) {
		m.EXTHFlag = be32(rec, mobiEXTHFlag)
	}
	if has(mobiDRMOffsetField, 4) {
		m.DRMOffset = idxField(be32(rec, mobiDRMOffsetField))
	}
	if has(mobiDRMCountField, 4) {
		m.DRMCount = be32(rec, mobiDRMCountField)
	}
	if has(mobiTrailingFlags, 4) {
		m.TrailingFlags = be32(rec, mobiTrailingFlags)
	}
	if has(mobiIndx, 4) {
		m.Indx = idxField(be32(rec, mobiIndx))
	}
	b.mobi = m

	// DRM gate, part two: DRM records present with a nonzero count.
	// Checked before encoding and compression so DRM always outranks
	// other damage in DRM-protected files.
	if m.DRMOffset != -1 && m.DRMCount != 0 {
		return fmt.Errorf("%w: %d DRM records at offset %d",
			ErrDRM, m.DRMCount, m.DRMOffset)
	}

	if m.Encoding != codepageCP1252 && m.Encoding != codepageUTF8 {
		return fmt.Errorf("%w: codepage %d, want 1252 or 65001",
			ErrCorrupt, m.Encoding)
	}
	switch b.palmdoc.Compression {
	case compressionNone, compressionPalmDOC, compressionHuffCDIC:
	default:
		return fmt.Errorf("%w: compression %d, want 1, 2, or 17480",
			ErrUnsupportedCompression, b.palmdoc.Compression)
	}

	if m.TitleLength > 0 {
		if int64(m.TitleOffset)+int64(m.TitleLength) > int64(len(rec)) {
			return fmt.Errorf("%w: title spans [%d, %d), past the %d-byte record 0",
				ErrCorrupt, m.TitleOffset, m.TitleOffset+m.TitleLength, len(rec))
		}
		b.title = rec[m.TitleOffset:][:m.TitleLength]
	}

	if m.EXTHFlag&exthFlagPresent != 0 {
		exth, err := parseEXTH(rec, mobiMagicOffset+int(m.Length))
		if err != nil {
			return err
		}
		b.exth = exth
	}

	if m.Version >= 8 {
		k := &kf8Header{FDST: -1, Frag: -1, Skel: -1, Guide: -1}
		if has(mobiFDST, 4) {
			k.FDST = idxField(be32(rec, mobiFDST))
		}
		if has(mobiNumFDST, 4) {
			k.NumFDST = be32(rec, mobiNumFDST)
		}
		if has(mobiFrag, 4) {
			k.Frag = idxField(be32(rec, mobiFrag))
		}
		if has(mobiSkel, 4) {
			k.Skel = idxField(be32(rec, mobiSkel))
		}
		if has(mobiGuide, 4) {
			k.Guide = idxField(be32(rec, mobiGuide))
		}
		b.kf8 = k
	}
	return nil
}

// idxField converts a raw index field to its parsed form: the
// 0xFFFFFFFF sentinel means absent and parses as -1.
func idxField(v uint32) int32 {
	if v == indexAbsent {
		return -1
	}
	return int32(v)
}

func be16(b []byte, off int) uint16 { return binary.BigEndian.Uint16(b[off:]) }
func be32(b []byte, off int) uint32 { return binary.BigEndian.Uint32(b[off:]) }

// mobiLanguages maps MOBI locale language codes to their region
// variants, indexed by the header's region byte shifted right two
// bits. Ported from foliate-js's MOBI_LANG table (mobi.js, MIT;
// originally the Palm OS locale table). Empty entries are unassigned.
var mobiLanguages = map[uint8][]string{
	1:  {"ar", "ar-SA", "ar-IQ", "ar-EG", "ar-LY", "ar-DZ", "ar-MA", "ar-TN", "ar-OM", "ar-YE", "ar-SY", "ar-JO", "ar-LB", "ar-KW", "ar-AE", "ar-BH", "ar-QA"},
	2:  {"bg"},
	3:  {"ca"},
	4:  {"zh", "zh-TW", "zh-CN", "zh-HK", "zh-SG"},
	5:  {"cs"},
	6:  {"da"},
	7:  {"de", "de-DE", "de-CH", "de-AT", "de-LU", "de-LI"},
	8:  {"el"},
	9:  {"en", "en-US", "en-GB", "en-AU", "en-CA", "en-NZ", "en-IE", "en-ZA", "en-JM", "", "en-BZ", "en-TT", "en-ZW", "en-PH"},
	10: {"es", "es-ES", "es-MX", "", "es-GT", "es-CR", "es-PA", "es-DO", "es-VE", "es-CO", "es-PE", "es-AR", "es-EC", "es-CL", "es-UY", "es-PY", "es-BO", "es-SV", "es-HN", "es-NI", "es-PR"},
	11: {"fi"},
	12: {"fr", "fr-FR", "fr-BE", "fr-CA", "fr-CH", "fr-LU", "fr-MC"},
	13: {"he"},
	14: {"hu"},
	15: {"is"},
	16: {"it", "it-IT", "it-CH"},
	17: {"ja"},
	18: {"ko"},
	19: {"nl", "nl-NL", "nl-BE"},
	20: {"no", "nb", "nn"},
	21: {"pl"},
	22: {"pt", "pt-BR", "pt-PT"},
	23: {"rm"},
	24: {"ro"},
	25: {"ru"},
	26: {"hr", "", "sr"},
	27: {"sk"},
	28: {"sq"},
	29: {"sv", "sv-SE", "sv-FI"},
	30: {"th"},
	31: {"tr"},
	32: {"ur"},
	33: {"id"},
	34: {"uk"},
	35: {"be"},
	36: {"sl"},
	37: {"et"},
	38: {"lv"},
	39: {"lt"},
	41: {"fa"},
	42: {"vi"},
	43: {"hy"},
	44: {"az"},
	45: {"eu"},
	46: {"hsb"},
	47: {"mk"},
	48: {"st"},
	49: {"ts"},
	50: {"tn"},
	52: {"xh"},
	53: {"zu"},
	54: {"af"},
	55: {"ka"},
	56: {"fo"},
	57: {"hi"},
	58: {"mt"},
	59: {"se"},
	62: {"ms"},
	63: {"kk"},
	65: {"sw"},
	67: {"uz", "", "uz-UZ"},
	68: {"tt"},
	69: {"bn"},
	70: {"pa"},
	71: {"gu"},
	72: {"or"},
	73: {"ta"},
	74: {"te"},
	75: {"kn"},
	76: {"ml"},
	77: {"as"},
	78: {"mr"},
	79: {"sa"},
	82: {"cy", "cy-GB"},
	83: {"gl", "gl-ES"},
	87: {"kok"},
	97: {"ne"},
	98: {"fy"},
}

// mobiLocale resolves the language tag encoded in the MOBI header's
// locale bytes, e.g. "en-US". The region byte at offset 94 indexes
// (>> 2) the language's variant list; unassigned slots and unknown
// languages fall back to the primary tag (or "" when nothing is
// known). Ported from foliate-js's #getHeaders language resolution.
func mobiLocale(language, region uint8) string {
	variants, ok := mobiLanguages[language]
	if !ok || len(variants) == 0 {
		return ""
	}
	if i := int(region) >> 2; i < len(variants) && variants[i] != "" {
		return variants[i]
	}
	return variants[0]
}
