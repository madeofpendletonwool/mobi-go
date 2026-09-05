package mobi

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// bookText is a multi-section MOBI6 page with pagebreaks and filepos
// links; the byte offsets of the pagebreaks and targets are asserted
// below, so keep the string byte-stable when editing.
const bookText = `<html><head><guide><reference type="toc" title="Contents" filepos="7"/></head>` +
	`<body><h1>One</h1><a filepos='48'>Chapter Two</a><mbp:pagebreak/>` +
	`<h1>Two</h1><p>xx</p><mbp:pagebreak>` +
	`<h1>Three</h1><a filepos="0007">Back to top</a><PAGEBREAK foo=1>` +
	`<h1>Four</h1></body></html>`

func openBookOK(t *testing.T, data []byte) *Book {
	t.Helper()
	b, err := openBook(t, data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

func TestTextEndToEnd(t *testing.T) {
	tests := []struct {
		name          string
		cfg           testutil.BookConfig
		wantDecodedAs string // "" means identical to bookText
	}{
		{"uncompressed", testutil.BookConfig{Text: bookText, RecordSize: 48}, ""},
		{"palmdoc compressed", testutil.BookConfig{Text: bookText, RecordSize: 48, Compression: 2}, ""},
		{
			"cp1252 encoded",
			testutil.BookConfig{Text: "<html>\x93Quoth\x94</html>", RecordSize: 8, Compression: 2, Encoding: 1252},
			"<html>\u201CQuoth\u201D</html>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := openBookOK(t, testutil.BuildBook(tt.cfg))
			want := tt.cfg.Text
			if tt.wantDecodedAs != "" {
				want = tt.wantDecodedAs
				if string(b.RawText()) != tt.cfg.Text {
					t.Errorf("RawText = %q, want the raw %q bytes", b.RawText(), tt.cfg.Text)
				}
			} else if string(b.RawText()) != want {
				t.Errorf("RawText = %q, want %q", b.RawText(), want)
			}
			if b.Text() != want {
				t.Errorf("Text() = %q, want %q", b.Text(), want)
			}
		})
	}
}

func TestTextEndToEndTrailingEntries(t *testing.T) {
	// Every combination of the two trailing-entry kinds, over both
	// compression types: the round trip proves the bookkeeping is
	// stripped from the compressed record before decompression (the
	// ordering both port sources share; see stripTrailingEntries).
	for _, flags := range []uint32{1, 2, 6, 0x0E, 0b1011, 0xFFFF} {
		for _, compression := range []uint16{1, 2} {
			cfg := testutil.BookConfig{
				Text:          bookText,
				RecordSize:    40,
				Compression:   compression,
				TrailingFlags: flags,
			}
			t.Run("trailing-flags", func(t *testing.T) {
				b := openBookOK(t, testutil.BuildBook(cfg))
				if string(b.RawText()) != bookText {
					t.Errorf("flags %#x compression %d: RawText = %q, want the original text", flags, compression, b.RawText())
				}
			})
		}
	}
}

func TestSections(t *testing.T) {
	b := openBookOK(t, testutil.BuildBook(testutil.BookConfig{
		Text:        bookText,
		RecordSize:  64,
		Compression: 2,
	}))
	raw := b.RawText()
	sections := b.Sections()
	if len(sections) != 4 {
		t.Fatalf("Sections = %d, want 4 (three pagebreaks)", len(sections))
	}

	// Monotonic, contiguous, covering.
	prevEnd := 0
	for i, sec := range sections {
		if sec.Start != prevEnd {
			t.Errorf("section %d starts at %d, want %d (previous end)", i, sec.Start, prevEnd)
		}
		if sec.End <= sec.Start {
			t.Errorf("section %d is empty or reversed: [%d, %d)", i, sec.Start, sec.End)
		}
		prevEnd = sec.End
	}
	if prevEnd != len(raw) {
		t.Errorf("sections cover %d bytes, text is %d", prevEnd, len(raw))
	}

	// Section byte ranges concatenate to the whole text.
	var joined []byte
	for _, sec := range sections {
		joined = append(joined, raw[sec.Start:sec.End]...)
	}
	if !bytes.Equal(joined, raw) {
		t.Error("concatenated sections do not reproduce the text")
	}

	// Each section after the first starts on its pagebreak tag.
	for _, sec := range sections[1:] {
		if !bytes.HasPrefix(raw[sec.Start:], []byte("<")) {
			t.Errorf("section at %d does not start on a tag: %q", sec.Start, raw[sec.Start:sec.Start+16])
		}
	}

	// Section 0 carries the guide, section 1 the second chapter, etc.
	if !bytes.Contains(raw[sections[0].Start:sections[0].End], []byte("guide")) {
		t.Error("section 0 does not contain the guide block")
	}
	if !bytes.Contains(raw[sections[2].Start:sections[2].End], []byte("<h1>Three</h1>")) {
		t.Error("section 2 does not contain chapter three")
	}
}

func TestSectionsNoPagebreaks(t *testing.T) {
	text := "<html><body>one section only</body></html>"
	b := openBookOK(t, testutil.BuildBook(testutil.BookConfig{Text: text, Compression: 2}))
	sections := b.Sections()
	if len(sections) != 1 || sections[0].Start != 0 || sections[0].End != len(text) {
		t.Fatalf("Sections = %+v, want one covering section", sections)
	}
}

func TestFileposTargets(t *testing.T) {
	b := openBookOK(t, testutil.BuildBook(testutil.BookConfig{
		Text:        bookText,
		RecordSize:  64,
		Compression: 2,
	}))
	targets := b.FileposTargets()
	want := []int{7, 48}
	if len(targets) != len(want) {
		t.Fatalf("FileposTargets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Errorf("FileposTargets[%d] = %d, want %d", i, targets[i], want[i])
		}
	}

	// Unrepresentable values are skipped, not fatal.
	big := `<a filepos="99999999999999999999999">x</a><a filepos="5">y</a>`
	b2 := openBookOK(t, testutil.BuildBook(testutil.BookConfig{Text: big, Compression: 2}))
	if got := b2.FileposTargets(); len(got) != 1 || got[0] != 5 {
		t.Errorf("FileposTargets = %v, want [5]", got)
	}
}

func TestTextLengthMismatchTolerated(t *testing.T) {
	// The declared textLength is advisory (record-boundary slack); a
	// wrong value must not fail the open. BuildBookParts is used so the
	// header can lie while the records stay real.
	rec0, records := testutil.BuildBookParts(testutil.BookConfig{
		Text:        bookText,
		RecordSize:  64,
		Compression: 2,
	})
	rec0[4], rec0[5], rec0[6], rec0[7] = 0xFF, 0xFF, 0xFF, 0xFF
	b := openBookOK(t, testutil.Build(append([][]byte{rec0}, records...)...))
	if string(b.RawText()) != bookText {
		t.Error("RawText changed under a lying textLength")
	}
}

func TestOpenTextRecordFailures(t *testing.T) {
	// A text record that will not decompress fails the whole open.
	rec0, records := testutil.BuildBookParts(testutil.BookConfig{
		Text:        bookText,
		RecordSize:  64,
		Compression: 2,
	})
	records[1] = []byte{0x05, 'a'} // truncated literal run
	_, err := openBook(t, testutil.Build(append([][]byte{rec0}, records...)...))
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt text record: error = %v, want ErrCorrupt", err)
	}

	// Missing physical records for the declared count.
	rec0b, recordsb := testutil.BuildBookParts(testutil.BookConfig{
		Text:        bookText,
		RecordSize:  64,
		Compression: 2,
	})
	rec0b[8], rec0b[9] = byte(len(recordsb)+5), 0 // NumTextRecords += 5
	_, err = openBook(t, testutil.Build(append([][]byte{rec0b}, recordsb...)...))
	if !errors.Is(err, ErrRecordRange) {
		t.Errorf("missing records: error = %v, want ErrRecordRange", err)
	}

	// HUFF/CDIC compression with no HUFF/CDIC dictionary records is
	// corrupt (the decompressor itself is huffcdic.go).
	_, err = openBook(t, testutil.BuildBook(testutil.BookConfig{
		Text:        bookText,
		Compression: 17480,
	}))
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("HUFF/CDIC text without dictionary records: error = %v, want ErrCorrupt", err)
	}
}

func TestTrailingEntryCorruption(t *testing.T) {
	// A trailing-entry size larger than its record is corrupt.
	text := strings.Repeat("chapter text ", 6)
	rec0, records := testutil.BuildBookParts(testutil.BookConfig{
		Text:          text,
		RecordSize:    48,
		Compression:   2,
		TrailingFlags: 2, // one trailing entry
	})
	// Replace the entry's varlen size byte with an absurd value: the
	// last 4 bytes decode to 0x7F7F = 32639.
	records[0] = append(records[0][:len(records[0])-1], 0x7F, 0x7F)
	_, err := openBook(t, testutil.Build(append([][]byte{rec0}, records...)...))
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("oversized trailing entry: error = %v, want ErrCorrupt", err)
	}
}

func TestKF8TextZeroValues(t *testing.T) {
	// A real KF8 book (raw flow, skeleton, fragments) still reports
	// zero through the MOBI6 accessors: its text lives in
	// KF8Sections and Flow.
	layout, _ := testutil.AuthorKF8([]testutil.KF8Author{{XHTML: bookText}}, nil)
	data := testutil.BuildKF8(testutil.KF8BookSpec{Layout: layout}).Data
	b := openBookOK(t, data)
	if b.RawText() != nil || b.Text() != "" || b.Sections() != nil || b.FileposTargets() != nil {
		t.Errorf("KF8 text accessors are non-zero: %+v %q %v %v",
			b.RawText(), b.Text(), b.Sections(), b.FileposTargets())
	}
}

func TestEmptyText(t *testing.T) {
	// Zero text records: open fine, one empty section.
	rec0 := testutil.BuildRecord0(testutil.Record0Config{Compression: 2})
	b := openBookOK(t, testutil.Build(rec0))
	if len(b.RawText()) != 0 || b.Text() != "" {
		t.Errorf("empty book text = %q", b.Text())
	}
	if sections := b.Sections(); len(sections) != 1 || sections[0] != (Section{0, 0}) {
		t.Errorf("Sections = %+v, want one empty section", sections)
	}
	if got := b.FileposTargets(); len(got) != 0 {
		t.Errorf("FileposTargets = %v, want none", got)
	}
}

// The variable-length quantity decoder this file used to test lives in
// internal/varlen now (varlen_test.go carries the table); the strip
// path itself is covered by the trailing-entry tests above.

func FuzzTextAssembly(f *testing.F) {
	// Fuzz the whole text path over record bytes: Open (and where it
	// succeeds, the accessors) must never panic, whatever the payload.
	f.Add([]byte(bookText), uint32(0), uint16(0))
	f.Add([]byte(bookText), uint32(0b11), uint16(2))
	f.Add([]byte(strings.Repeat("run text ", 300)), uint32(0x0E), uint16(2))
	f.Add([]byte{0x00, 0x01, 0x80, 0xFF}, uint32(0xFFFF), uint16(2))
	f.Add([]byte("<a filepos='999'>"), uint32(0), uint16(1))
	f.Fuzz(func(t *testing.T, text []byte, flags uint32, compression uint16) {
		cfg := testutil.BookConfig{
			Text:          string(text),
			RecordSize:    32,
			TrailingFlags: flags,
			Compression:   compression,
			Version:       6,
		}
		switch compression {
		case 0, 1:
			cfg.Compression = 1
		case 2:
			cfg.Compression = 2
		default:
			cfg.Compression = 2
		}
		if flags&0x80000000 != 0 { // absurd popcounts stress the strip loop
			cfg.TrailingFlags = flags
		}
		b, err := openBook(t, testutil.BuildBook(cfg))
		if err != nil {
			return
		}
		_ = b.Text()
		for _, sec := range b.Sections() {
			if sec.Start < 0 || sec.End > len(b.RawText()) || sec.Start > sec.End {
				t.Fatalf("section %+v outside %d-byte text", sec, len(b.RawText()))
			}
		}
		_ = b.FileposTargets()
	})
}
