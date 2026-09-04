package mobi

import (
	"bytes"
	"errors"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

func openBook(t *testing.T, data []byte) (*Book, error) {
	t.Helper()
	return Open(bytes.NewReader(data), int64(len(data)))
}

// parseBook builds a one-record PDB around cfg's record 0 and parses
// just the header chain, returning the Book and any error.
func parseBook(t *testing.T, cfg testutil.Record0Config) (*Book, error) {
	t.Helper()
	rec0 := testutil.BuildRecord0(cfg)
	b := &Book{}
	err := b.parseRecord0(rec0)
	return b, err
}

func TestParseRecord0MOBI6(t *testing.T) {
	b, err := parseBook(t, testutil.Record0Config{
		Compression:     2,
		TextLength:      123456,
		NumTextRecords:  31,
		MOBIType:        3,
		UID:             42,
		FirstImageIndex: testutil.U32(5),
		Huffcdic:        testutil.U32(20),
		NumHuffcdic:     2,
		TrailingFlags:   0x0E,
		Indx:            testutil.U32(7),
		Title:           "The MOBI6 Book",
	})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}

	pd := b.palmdoc
	if pd.Compression != 2 || pd.TextLength != 123456 || pd.NumTextRecords != 31 ||
		pd.RecordSize != 4096 || pd.Encryption != 0 || pd.Unknown != 0 {
		t.Errorf("palmDocHeader = %+v", pd)
	}

	m := b.mobi
	if m.Length != 232 {
		t.Errorf("Length = %d, want 232", m.Length)
	}
	if m.Type != 3 {
		t.Errorf("Type = %d, want 3", m.Type)
	}
	if m.Encoding != 65001 {
		t.Errorf("Encoding = %d, want 65001", m.Encoding)
	}
	if m.UID != 42 {
		t.Errorf("UID = %d, want 42", m.UID)
	}
	if m.Version != 6 {
		t.Errorf("Version = %d, want 6", m.Version)
	}
	if m.TitleOffset != 248 { // 16 + 232, no EXTH
		t.Errorf("TitleOffset = %d, want 248", m.TitleOffset)
	}
	if m.TitleLength != 14 {
		t.Errorf("TitleLength = %d, want 14", m.TitleLength)
	}
	if m.LocaleRegion != 4 || m.LocaleLanguage != 9 {
		t.Errorf("locale = (%d, %d), want (4, 9)", m.LocaleRegion, m.LocaleLanguage)
	}
	if m.FirstImageIndex != 5 {
		t.Errorf("FirstImageIndex = %d, want 5", m.FirstImageIndex)
	}
	if m.Huffcdic != 20 {
		t.Errorf("Huffcdic = %d, want 20", m.Huffcdic)
	}
	if m.NumHuffcdic != 2 {
		t.Errorf("NumHuffcdic = %d, want 2", m.NumHuffcdic)
	}
	if m.EXTHFlag != 0 {
		t.Errorf("EXTHFlag = %#x, want 0", m.EXTHFlag)
	}
	if m.DRMOffset != -1 || m.DRMCount != 0 {
		t.Errorf("DRM = (%d, %d), want (-1, 0)", m.DRMOffset, m.DRMCount)
	}
	if m.TrailingFlags != 0x0E {
		t.Errorf("TrailingFlags = %#x, want 0x0E", m.TrailingFlags)
	}
	if m.Indx != 7 {
		t.Errorf("Indx = %d, want 7", m.Indx)
	}

	if b.exth != nil {
		t.Errorf("exth = %v, want nil", b.exth)
	}
	if b.kf8 != nil {
		t.Errorf("kf8 = %v, want nil", b.kf8)
	}
	if b.IsKF8() {
		t.Errorf("IsKF8() = true, want false")
	}

	md := b.Metadata()
	if md.Title != "The MOBI6 Book" {
		t.Errorf("Metadata.Title = %q, want %q", md.Title, "The MOBI6 Book")
	}
	if md.Language != "en-US" {
		t.Errorf("Metadata.Language = %q, want en-US", md.Language)
	}
	if md.Authors != nil {
		t.Errorf("Metadata.Authors = %v, want nil", md.Authors)
	}
}

func TestParseRecord0V7WithEXTH(t *testing.T) {
	cfg := testutil.Record0Config{
		Version: 7,
		UID:     7,
		Title:   "Different MOBI Title",
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "Jane Austen"),
			testutil.EXTHString(101, "Penguin"),
			testutil.EXTHString(103, "A &amp; Tale"),
			testutil.EXTHString(104, "978-3-16-148410-0"),
			testutil.EXTHString(105, "Fiction"),
			testutil.EXTHString(105, "Classic"),
			testutil.EXTHString(106, "1813-01-28"),
			testutil.EXTHString(108, "The Illustrator"),
			testutil.EXTHString(109, "Public Domain"),
			testutil.EXTHString(113, "B000EXAMPLE"),
			{Type: 4242, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
			{Type: 777, Data: []byte{1, 2, 3}}, // 3-byte payload: not a uint
			testutil.EXTHUint(121, 4242),
			testutil.EXTHUint(125, 12),
			testutil.EXTHString(126, "1200x1600"),
			testutil.EXTHString(129, "kindle:embed:0001"),
			testutil.EXTHUint(201, 11),
			testutil.EXTHUint(202, 12),
			testutil.EXTHString(503, "Pride &amp; Prejudice"),
			testutil.EXTHString(524, "en-GB"),
			{Type: 65001, Data: []byte("zz")},
		},
	}
	b, err := parseBook(t, cfg)
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	if b.mobi.Version != 7 || b.mobi.Length != 232 {
		t.Errorf("version/length = %d/%d, want 7/232", b.mobi.Version, b.mobi.Length)
	}
	if b.exth == nil {
		t.Fatalf("exth = nil, want parsed block")
	}

	// Entries keep file order, unknown types included.
	wantOrder := []uint32{100, 101, 103, 104, 105, 105, 106, 108, 109, 113,
		4242, 777, 121, 125, 126, 129, 201, 202, 503, 524, 65001}
	if len(b.exth.entries) != len(wantOrder) {
		t.Fatalf("entry count = %d, want %d", len(b.exth.entries), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got := b.exth.entries[i].typ; got != want {
			t.Errorf("entry %d type = %d, want %d", i, got, want)
		}
	}

	// Unknown types round-trip raw, never dropped.
	unknown := b.exth.All(4242)
	if len(unknown) != 1 || !bytes.Equal(unknown[0], []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("All(4242) = %x, want deadbeef", unknown)
	}
	if got := b.exth.All(65001); len(got) != 1 || string(got[0]) != "zz" {
		t.Errorf("All(65001) = %q, want zz", got)
	}

	// Numeric reads.
	for _, tc := range []struct {
		typ  uint32
		want uint32
	}{
		{121, 4242}, {125, 12}, {201, 11}, {202, 12},
	} {
		got, ok := b.exth.uint(tc.typ)
		if !ok || got != tc.want {
			t.Errorf("uint(%d) = %d, %v; want %d, true", tc.typ, got, ok, tc.want)
		}
	}
	if _, ok := b.exth.uint(777); ok {
		t.Errorf("uint(777) reported ok for 3-byte payload")
	}

	// String-valued non-metadata records stay raw but reachable.
	for _, tc := range []struct {
		typ  uint32
		want string
	}{
		{exthOriginalResolution, "1200x1600"},
		{exthCoverURI, "kindle:embed:0001"},
	} {
		got, ok := b.exth.last(tc.typ)
		if !ok || string(got) != tc.want {
			t.Errorf("last(%d) = %q, %v; want %q, true", tc.typ, got, ok, tc.want)
		}
	}

	// Multi-valued lookups keep file order.
	if authors := b.exth.All(100); len(authors) != 1 || string(authors[0]) != "Jane Austen" {
		t.Errorf("All(100) = %q, want [Jane Austen]", authors)
	}

	// Block bookkeeping fields.
	exthLen := uint32(len(testutil.BuildEXTH(cfg.EXTH...)))
	if b.exth.offset != 16+232 {
		t.Errorf("exth.offset = %d, want 248", b.exth.offset)
	}
	if b.exth.length != exthLen {
		t.Errorf("exth.length = %d, want %d", b.exth.length, exthLen)
	}

	// Metadata resolution with EXTH precedence and HTML unescaping.
	md := b.Metadata()
	if md.Title != "Pride & Prejudice" {
		t.Errorf("Title = %q, want EXTH 503 with entities unescaped", md.Title)
	}
	if md.Publisher != "Penguin" {
		t.Errorf("Publisher = %q", md.Publisher)
	}
	if md.Description != "A & Tale" {
		t.Errorf("Description = %q", md.Description)
	}
	if md.ISBN != "978-3-16-148410-0" {
		t.Errorf("ISBN = %q", md.ISBN)
	}
	if md.Published != "1813-01-28" {
		t.Errorf("Published = %q", md.Published)
	}
	if md.Rights != "Public Domain" {
		t.Errorf("Rights = %q", md.Rights)
	}
	if md.ASIN != "B000EXAMPLE" {
		t.Errorf("ASIN = %q", md.ASIN)
	}
	if md.Language != "en-GB" {
		t.Errorf("Language = %q, want EXTH 524 over locale", md.Language)
	}
	wantSubjects := []string{"Fiction", "Classic"}
	if len(md.Subjects) != 2 || md.Subjects[0] != wantSubjects[0] || md.Subjects[1] != wantSubjects[1] {
		t.Errorf("Subjects = %v, want %v", md.Subjects, wantSubjects)
	}
}

func TestEXTHDuplicateSingleValue(t *testing.T) {
	// Repeated single-valued records resolve to the last one, matching
	// both port sources' overwrite semantics.
	b, err := parseBook(t, testutil.Record0Config{
		Version: 6,
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(503, "First Title"),
			testutil.EXTHString(503, "Last Title"),
		},
	})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	if got, want := b.Metadata().Title, "Last Title"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if got := len(b.exth.All(503)); got != 2 {
		t.Errorf("All(503) count = %d, want 2 (raw entries never collapse)", got)
	}
}

func TestParseRecord0KF8(t *testing.T) {
	b, err := parseBook(t, testutil.Record0Config{
		Version:         8,
		Encoding:        1252,
		UID:             7,
		FirstImageIndex: testutil.U32(9),
		FDST:            testutil.U32(3),
		NumFDST:         4,
		Frag:            testutil.U32(6),
		Skel:            testutil.U32(7),
		Title:           "KF8 Book",
		EXTH:            []testutil.EXTHRecord{testutil.EXTHUint(121, 25)},
	})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	if !b.IsKF8() {
		t.Errorf("IsKF8() = false, want true")
	}
	if b.mobi.Length != 264 {
		t.Errorf("Length = %d, want 264", b.mobi.Length)
	}
	k := b.kf8
	if k == nil {
		t.Fatalf("kf8 = nil, want parsed header")
	}
	if k.FDST != 3 || k.NumFDST != 4 || k.Frag != 6 || k.Skel != 7 || k.Guide != -1 {
		t.Errorf("kf8Header = %+v, want {3 4 6 7 -1}", *k)
	}
	if got, ok := b.exth.uint(121); !ok || got != 25 {
		t.Errorf("boundary = %d, %v; want 25, true", got, ok)
	}
	if b.mobi.Encoding != 1252 {
		t.Errorf("Encoding = %d, want 1252", b.mobi.Encoding)
	}
}

func TestParseRecord0Sentinels(t *testing.T) {
	// Zero config: every optional index field is the 0xFFFFFFFF
	// sentinel and parses as -1, never as a giant number.
	b, err := parseBook(t, testutil.Record0Config{})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	m := b.mobi
	if m.FirstImageIndex != -1 || m.Huffcdic != -1 || m.Indx != -1 || m.DRMOffset != -1 {
		t.Errorf("sentinels = image %d, huff %d, indx %d, drm %d; want all -1",
			m.FirstImageIndex, m.Huffcdic, m.Indx, m.DRMOffset)
	}
	if m.NumHuffcdic != 0 {
		t.Errorf("NumHuffcdic = %d, want 0", m.NumHuffcdic)
	}

	// Index value 0 is a real zero, not the sentinel.
	b8, err := parseBook(t, testutil.Record0Config{
		Version: 8,
		FDST:    testutil.U32(0),
		Frag:    testutil.U32(0),
	})
	if err != nil {
		t.Fatalf("parseRecord0 (v8 zeros): %v", err)
	}
	if b8.kf8.FDST != 0 || b8.kf8.Frag != 0 {
		t.Errorf("v8 zero indexes = fdst %d, frag %d; want 0, 0", b8.kf8.FDST, b8.kf8.Frag)
	}
	if b8.kf8.Skel != -1 || b8.kf8.Guide != -1 {
		t.Errorf("v8 nil indexes = skel %d, guide %d; want -1, -1", b8.kf8.Skel, b8.kf8.Guide)
	}
}

func TestMetadataTitleFallbackCP1252(t *testing.T) {
	// No EXTH: the title comes from the MOBI full-name field, decoded
	// as windows-1252 and HTML-unescaped.
	title := append([]byte("Cheap \x93quotes\x94 &amp; dash \x97"), 0xE9) // é as cp1252
	b, err := parseBook(t, testutil.Record0Config{
		Encoding: 1252,
		Title:    string(title),
	})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	want := "Cheap \u201Cquotes\u201D & dash \u2014\u00E9"
	if got := b.Metadata().Title; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

func TestMobiLocale(t *testing.T) {
	tests := []struct {
		lang   uint8
		region uint8
		want   string
	}{
		{9, 4, "en-US"}, // region index 1
		{7, 8, "de-CH"}, // region index 2
		{9, 0, "en"},    // region index 0: primary tag
		{9, 36, "en"},   // region index 9 unassigned: primary fallback
		{9, 252, "en"},  // region index 63 out of range: primary fallback
		{7, 0, "de"},
		{16, 8, "it-CH"},
		{200, 0, ""}, // unknown language
		{0, 0, ""},   // unset locale
	}
	for _, tt := range tests {
		if got := mobiLocale(tt.lang, tt.region); got != tt.want {
			t.Errorf("mobiLocale(%d, %d) = %q, want %q", tt.lang, tt.region, got, tt.want)
		}
	}
}

func TestMetadataLanguageFallback(t *testing.T) {
	b, err := parseBook(t, testutil.Record0Config{
		LocaleLanguage: 7,
		LocaleRegion:   8, // de, region index 2 → de-CH
	})
	if err != nil {
		t.Fatalf("parseRecord0: %v", err)
	}
	if got := b.Metadata().Language; got != "de-CH" {
		t.Errorf("Language = %q, want de-CH (locale fallback)", got)
	}
}

func TestDecodeCP1252(t *testing.T) {
	// The full 0x80–0x9F range against an independently transcribed
	// table (Unicode consortium cp1252 mapping).
	wantRunes := []rune{
		0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
		0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
		0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
		0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
	}
	high := make([]byte, 32)
	for i := range high {
		high[i] = byte(0x80 + i)
	}
	if got := decodeString(1252, high); got != string(wantRunes) {
		t.Errorf("decodeString(1252, 0x80..0x9F) = %q, want %q", got, string(wantRunes))
	}

	// 0xA0–0xFF are their own code points (Latin-1 identity).
	var lat []rune
	for r := rune(0xA0); r <= 0xFF; r++ {
		lat = append(lat, r)
	}
	latin := make([]byte, 0x60)
	for i := range latin {
		latin[i] = byte(0xA0 + i)
	}
	if got := decodeString(1252, latin); got != string(lat) {
		t.Errorf("decodeString(1252, 0xA0..0xFF) = %q, want %q", got, string(lat))
	}

	// ASCII fast path and a mixed sample.
	if got := decodeString(1252, []byte("plain ascii")); got != "plain ascii" {
		t.Errorf("ascii decode = %q", got)
	}
	mixed := append([]byte("caf"), 0xE9, 0x99) // é, ™
	if got, want := decodeString(1252, mixed), "caf\u00E9\u2122"; got != want {
		t.Errorf("mixed decode = %q, want %q", got, want)
	}

	// UTF-8 passthrough.
	utf := []byte("h\u00E9llo \u20AC")
	if got := decodeString(65001, utf); got != string(utf) {
		t.Errorf("utf-8 decode = %q", got)
	}
}

func TestOpenDRM(t *testing.T) {
	tests := []struct {
		name string
		cfg  testutil.Record0Config
	}{
		{"encryption type 1", testutil.Record0Config{Encryption: 1}},
		{"encryption type 2", testutil.Record0Config{Encryption: 2}},
		{"DRM records", testutil.Record0Config{
			DRMOffset: testutil.U32(10),
			DRMCount:  2,
		}},
		{"DRM outranks compression", testutil.Record0Config{
			Encryption:  1,
			Compression: 3,
		}},
		{"DRM outranks codepage", testutil.Record0Config{
			Encoding:  9999,
			DRMOffset: testutil.U32(10),
			DRMCount:  1,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := testutil.Build(testutil.BuildRecord0(tt.cfg), []byte("text record"))
			b, err := openBook(t, data)
			if !errors.Is(err, ErrDRM) {
				t.Fatalf("Open error = %v, want ErrDRM", err)
			}
			if b != nil {
				t.Errorf("Open returned a partial book alongside ErrDRM")
			}
		})
	}

	// DRM offset without records is not DRM.
	b, err := parseBook(t, testutil.Record0Config{DRMOffset: testutil.U32(10)})
	if err != nil || b == nil {
		t.Errorf("DRMOffset with zero count: (%v, %v), want clean open", b, err)
	}
}

func TestOpenUnsupportedCompression(t *testing.T) {
	// Compression 0 cannot go through the builder (0 selects the
	// default), so patch the bytes directly.
	zero := testutil.BuildRecord0(testutil.Record0Config{})
	zero[0], zero[1] = 0, 0
	for _, rec0 := range [][]byte{
		zero,
		testutil.BuildRecord0(testutil.Record0Config{Compression: 3}),
		testutil.BuildRecord0(testutil.Record0Config{Compression: 17481}),
	} {
		b := &Book{}
		if err := b.parseRecord0(rec0); !errors.Is(err, ErrUnsupportedCompression) {
			t.Errorf("parseRecord0: error = %v, want ErrUnsupportedCompression", err)
		}
	}
	// HUFF/CDIC parses now; decompression arrives in a later stage.
	if _, err := parseBook(t, testutil.Record0Config{Compression: 17480}); err != nil {
		t.Errorf("compression 17480: %v, want accepted at header-parse time", err)
	}
}

func TestParseRecord0Malformed(t *testing.T) {
	valid := testutil.BuildRecord0(testutil.Record0Config{
		Version: 7,
		Title:   "T",
		EXTH:    []testutil.EXTHRecord{testutil.EXTHString(100, "A")},
	})
	exthOff := bytes.Index(valid, []byte("EXTH"))
	if exthOff < 0 {
		t.Fatalf("fixture has no EXTH block")
	}

	badMagic := append([]byte(nil), valid...)
	copy(badMagic[16:20], "PALM")

	shortHeader := testutil.BuildRecord0(testutil.Record0Config{MOBILength: 20})

	hugeLength := testutil.BuildRecord0(testutil.Record0Config{})
	putU32(hugeLength, 20, 0xFFFFFFF0)

	badCodepage := testutil.BuildRecord0(testutil.Record0Config{Encoding: 9999})

	exthBadMagic := append([]byte(nil), valid...)
	copy(exthBadMagic[exthOff:exthOff+4], "XXXX")

	exthShortLength := append([]byte(nil), valid...)
	putU32(exthShortLength, exthOff+4, 4)

	exthHugeLength := append([]byte(nil), valid...)
	putU32(exthHugeLength, exthOff+4, 0xFFFFFF)

	exthShortRecord := append([]byte(nil), valid...)
	putU32(exthShortRecord, exthOff+16, 3) // first record's size

	exthOverCount := append([]byte(nil), valid...)
	putU32(exthOverCount, exthOff+8, 1000)

	titlePastEnd := testutil.BuildRecord0(testutil.Record0Config{Title: "Some Title"})
	putU32(titlePastEnd, 88, 4096) // titleLength way past the record

	tests := []struct {
		name string
		rec0 []byte
	}{
		{"record 0 too short for PalmDOC", valid[:15]},
		{"record 0 too short for MOBI", valid[:20]},
		{"bad MOBI magic", badMagic},
		{"MOBI length below minimum", shortHeader},
		{"MOBI length past record", hugeLength},
		{"unknown codepage", badCodepage},
		{"EXTH magic wrong", exthBadMagic},
		{"EXTH length below header", exthShortLength},
		{"EXTH length past record", exthHugeLength},
		{"EXTH record size below header", exthShortRecord},
		{"EXTH count overflows block", exthOverCount},
		{"EXTH header truncated", valid[:exthOff+11]},
		{"title past record end", titlePastEnd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Book{}
			err := b.parseRecord0(tt.rec0)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("parseRecord0 error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestParseRecord0Truncations(t *testing.T) {
	// Cut the record at every byte position; every truncation must
	// produce a typed error, never a panic or a partial parse. The
	// fixture carries EXTH and a title so all boundaries are live.
	rec0 := testutil.BuildRecord0(testutil.Record0Config{
		Version: 7,
		Title:   "Truncation Fixture",
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "Author Name"),
			testutil.EXTHString(503, "Truncated"),
		},
	})
	full := &Book{}
	if err := full.parseRecord0(rec0); err != nil {
		t.Fatalf("full-length fixture does not parse: %v", err)
	}
	for cut := 0; cut < len(rec0); cut++ {
		b := &Book{}
		err := b.parseRecord0(rec0[:cut])
		if err == nil {
			t.Fatalf("truncation at %d of %d parsed without error", cut, len(rec0))
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("truncation at %d: error = %v, want ErrCorrupt", cut, err)
		}
	}
}

func TestOpenEndToEnd(t *testing.T) {
	rec0 := testutil.BuildRecord0(testutil.Record0Config{
		Compression:    2,
		TextLength:     100,
		NumTextRecords: 1,
		Title:          "Whole File",
		EXTH:           []testutil.EXTHRecord{testutil.EXTHString(100, "Whole Author")},
	})
	data := testutil.Build(rec0, []byte("first text record"), []byte("second text record"))

	b, err := openBook(t, data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := b.pdb.NumRecords(); got != 3 {
		t.Errorf("NumRecords = %d, want 3", got)
	}
	md := b.Metadata()
	if md.Title != "Whole File" || len(md.Authors) != 1 || md.Authors[0] != "Whole Author" {
		t.Errorf("Metadata = %+v", md)
	}

	// A container whose record 0 lacks the MOBI header is corrupt.
	legacy := testutil.BuildWith(testutil.PDBConfig{
		Name:    "Legacy",
		Type:    "TEXt",
		Creator: "REAd",
	}, []byte("just palm doc text"))
	if _, err := openBook(t, legacy); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Open on TEXt/REAd without MOBI header: error = %v, want ErrCorrupt", err)
	}
}

func FuzzHeaders(f *testing.F) {
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Title: "A",
		Compression: 2, NumTextRecords: 1, TextLength: 4}))
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Version: 7, Title: "B",
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "x"), testutil.EXTHUint(201, 1),
			{Type: 4242, Data: []byte{0xDE, 0xAD}},
		}}))
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Version: 8, Title: "C",
		FDST: testutil.U32(3), NumFDST: 4, Frag: testutil.U32(5), Skel: testutil.U32(6)}))
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Encryption: 1}))
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Encryption: 2,
		Compression: 17480, Encoding: 1252}))
	f.Add(testutil.BuildRecord0(testutil.Record0Config{Encoding: 1252, Title: "\x93cp1252\x94"}))
	f.Add([]byte("MOBI"))
	f.Add([]byte(nil))
	f.Fuzz(func(t *testing.T, rec []byte) {
		b := &Book{}
		if err := b.parseRecord0(rec); err != nil {
			return
		}
		// Whatever the parser accepted must satisfy its invariants.
		if (b.mobi.Version >= 8) != (b.kf8 != nil) {
			t.Errorf("kf8 header presence = %v at version %d", b.kf8 != nil, b.mobi.Version)
		}
		if b.mobi.TitleLength > 0 &&
			int64(b.mobi.TitleOffset)+int64(b.mobi.TitleLength) > int64(len(rec)) {
			t.Errorf("title [%d, %d) past %d-byte record",
				b.mobi.TitleOffset, b.mobi.TitleOffset+b.mobi.TitleLength, len(rec))
		}
		if b.exth != nil {
			if b.exth.offset+int(b.exth.length) > len(rec) {
				t.Errorf("EXTH [%d, %d) past %d-byte record",
					b.exth.offset, b.exth.offset+int(b.exth.length), len(rec))
			}
		}
		_ = b.Metadata()
	})
}

// putU32 patches a big-endian uint32 into b at off.
func putU32(b []byte, off int, v uint32) {
	b[off], b[off+1], b[off+2], b[off+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
