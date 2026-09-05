// Command fixgen authors the synthetic fixture corpus under
// testdata/books. Every book is generated deterministically from this
// source — the corpus is authored for this repo (see
// tools/fixtures/README.md), committed, and verified against the
// KindleUnpack oracle through testdata/golden (make regen-golden).
//
// The corpus covers the stage-9 matrix: MOBI6 uncompressed / PalmDOC /
// HUFF-CDIC, with and without EXTH, with images + cover, TOC via INDX,
// pure KF8, combo files, a DRM refusal, and corrupt container/header/
// FDST shapes.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// Tiny deterministic raster images (the same blobs resource_test.go
// sniffs), embedded so the generator has no inputs beyond its source.
var (
	gifCover = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff" +
		"\x21\xf9\x04\x01\x00\x00\x00\x00\x2c\x00\x00\x00\x00\x01\x00\x01\x00" +
		"\x00\x02\x02\x44\x01\x00\x3b")
	pngImage = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00" +
		"\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc" +
		"\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	jpegImage = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00" +
		"\x01\x00\x00\xff\xd9")
	bmpImage = []byte("BM\x3a\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00" +
		"\x00\x00\x01\x00\x00\x00\x01\x00\x00\x00\x01\x00\x18\x00\x00\x00\x00" +
		"\x00\x04\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00" +
		"\x00\x00\x00\x00\x00\x00\x00\x00\x00\xff\x00\x00\x00")
)

// ncxTagTable mirrors the tag layout a real MOBI6 NCX index carries
// (same layout the index tests use): tags 1/2/3/4/5/6/21/22 fill
// control byte 0, a header-only marker ends it, tag 23 lives in byte 1.
var ncxTagTable = []testutil.TagDesc{
	{Tag: 1, ValuesPerEntry: 1, Mask: 0x01},  // filepos
	{Tag: 2, ValuesPerEntry: 1, Mask: 0x02},  // length
	{Tag: 3, ValuesPerEntry: 1, Mask: 0x04},  // label (CNCX)
	{Tag: 4, ValuesPerEntry: 1, Mask: 0x08},  // heading level
	{Tag: 5, ValuesPerEntry: 1, Mask: 0x10},  // kind (CNCX)
	{Tag: 6, ValuesPerEntry: 2, Mask: 0x20},  // pos pair (fid, off)
	{Tag: 21, ValuesPerEntry: 1, Mask: 0x40}, // parent
	{Tag: 22, ValuesPerEntry: 1, Mask: 0x80}, // first child
	{Tag: 0, ValuesPerEntry: 0, Mask: 0x00, EndFlags: 1},
	{Tag: 23, ValuesPerEntry: 1, Mask: 0x01}, // last child, byte 1
}

// guideTagTable is the KF8 guide index's tag layout: tag 1 the label
// (CNCX), tag 3 the file number, tag 6 the pos pair.
var guideTagTable = []testutil.TagDesc{
	{Tag: 1, ValuesPerEntry: 1, Mask: 0x01},
	{Tag: 3, ValuesPerEntry: 1, Mask: 0x02},
	{Tag: 6, ValuesPerEntry: 2, Mask: 0x04},
}

func main() {
	dir := "testdata/books"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("creating %s: %v", dir, err)
	}

	write := func(name string, data []byte) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			fail("writing %s: %v", p, err)
		}
		fmt.Printf("%s (%d bytes)\n", p, len(data))
	}

	write("mobi6-none.mobi", buildMobi6None())
	write("mobi6-palmdoc.mobi", buildMobi6Palmdoc())
	write("mobi6-huffcdic.mobi", buildMobi6HuffCDIC())
	kf8 := buildKF8()
	write("azw3-kf8.azw3", kf8)
	write("azw3-combo.azw3", buildCombo())
	write("mobi6-drm.mobi", buildDRM())
	write("corrupt-not-palm-db.bin", buildNotPalmDB())
	write("corrupt-truncated.mobi", buildTruncated())
	write("corrupt-fdst.azw3", buildCorruptFDST())
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fixgen: "+format+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------- MOBI6

// buildMobi6None: uncompressed v6, no EXTH (title and language come
// from the MOBI header, windows-1252 encoded), no images, no INDX —
// the chapter list is the legacy <toc> block and the guide is the
// <guide> block, both parsed from the raw text.
func buildMobi6None() []byte {
	chapters := []string{
		"<h1>Alpha</h1><p>First things first.</p>",
		"<h1>Beta</h1><p>Second chapter, with a cross reference <a filepos=\"{LINK2}\">to chapter two</a>.</p>",
		"<h1>Gamma</h1><p>Third chapter wraps up.</p>",
	}

	// Two-pass assembly with fixed-width (8-digit) filepos values so
	// the second pass cannot shift any offset.
	build := func(fill func(n int) string) (string, []int) {
		var b strings.Builder
		starts := make([]int, len(chapters))
		// Placeholder offsets keep the width stable across passes.
		offs := make([]int, len(chapters))
		for i := range offs {
			offs[i] = i // digits rewritten below at fixed width
		}
		writeToc := func() {
			b.WriteString("<toc>")
			for i, ch := range chapters {
				fmt.Fprintf(&b, `<tocpoint filepos="%08d" tocdepth="1">%s</tocpoint>`, offs[i], ch[strings.Index(ch, "<h1>")+4:strings.Index(ch, "</h1>")])
			}
			b.WriteString("</toc>")
		}
		b.WriteString("<html><head><guide><reference type=\"toc\" title=\"Contents\" filepos=\"")
		b.WriteString(fill(0))
		b.WriteString("\"/><reference type=\"text\" title=\"Begin Reading\" filepos=\"")
		b.WriteString(fill(1))
		b.WriteString("\"/></guide></head><body>")
		writeToc()
		for i, ch := range chapters {
			b.WriteString("<mbp:pagebreak/>")
			starts[i] = b.Len()
			b.WriteString(ch)
		}
		b.WriteString("</body></html>")
		return b.String(), starts
	}

	_, starts := build(func(n int) string { return "00000000" })
	// Second pass with the real offsets, including the in-text link.
	real := func(n int) string { return fmt.Sprintf("%08d", starts[n]) }
	text2, starts2 := build(real)
	if starts2[len(starts2)-1] > 99999999 {
		fail("filepos values exceed the fixed 8-digit width")
	}
	// Patch the in-chapter cross link (its placeholder widened nothing:
	// same 8-digit width).
	text2 = strings.ReplaceAll(text2, "{LINK2}", real(2))
	if _, s2 := build(real); s2[len(s2)-1] != starts2[len(starts2)-1] {
		fail("two-pass assembly is not stable")
	}

	// windows-1252 title bytes: BuildRecord0 copies the string verbatim.
	title := string(testutil.CP1252("Café Synthétique"))
	return testutil.BuildBook(testutil.BookConfig{
		Text:     text2,
		Encoding: 1252,
		Title:    title,
	})
}

// buildMobi6Palmdoc: PalmDOC-compressed multi-record book with
// trailing bookkeeping, full EXTH (two authors, cover offsets), four
// image resources, and a hierarchical INDX NCX.
func buildMobi6Palmdoc() []byte {
	chapters := []string{
		`<html><head><guide><reference type="text" title="Begin" filepos="{F1}"/></guide></head><body>`,
		`<h1>Synthetic PalmDOC</h1><p>This book exists to be parsed.</p><img recindex="00002" alt="figure">`,
		`<h1>Second Chapter</h1><p>PalmDOC back-references compress repeated repeated repeated text.</p>`,
		`<h1>Third Chapter</h1><p>See <a filepos="{F1}">the beginning</a>.</p></body></html>`,
	}

	// Fixed-width placeholders again: chapter starts and the guide's
	// filepos rewrite at the same width.
	build := func(offs []string) (string, []int) {
		var b strings.Builder
		starts := make([]int, len(chapters))
		for i, ch := range chapters {
			if i > 0 {
				b.WriteString("<mbp:pagebreak/>")
			}
			starts[i] = b.Len()
			b.WriteString(ch)
		}
		out := b.String()
		for i, off := range offs {
			out = strings.Replace(out, fmt.Sprintf("{F%d}", i+1), off, 1)
		}
		return out, starts
	}
	_, starts := build([]string{"00000000", "00000000", "00000000", "00000000"})
	offs := make([]string, len(starts))
	for i, s := range starts {
		offs[i] = fmt.Sprintf("%08d", s)
	}
	text, _ := build(offs)

	labels := []string{"Begin Reading", "Synthetic PalmDOC", "Second Chapter", "Third Chapter"}
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX:     [][]string{labels},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		// Root: begin-reading marker at offset 0.
		{Values: map[int][]int{1: {0}, 3: {built.CNCX[0]}, 4: {0}}},
		// Chapter roots with one nested child under chapter two.
		{Values: map[int][]int{1: {starts[1]}, 3: {built.CNCX[1]}, 4: {0}}},
		{Values: map[int][]int{1: {starts[2]}, 3: {built.CNCX[2]}, 4: {0}, 22: {3}, 23: {3}}},
		{Values: map[int][]int{1: {starts[2] + 10}, 3: {built.CNCX[3]}, 4: {1}, 21: {2}}},
	}
	base.Entries = entries

	book := testutil.BookConfig{
		Text:          text,
		Compression:   2,
		RecordSize:    512,
		TrailingFlags: 0b11, // multibyte overlap + one trailing entry
		Title:         "Synthetic PalmDOC Book",
		Resources:     [][]byte{gifCover, pngImage, jpegImage, bmpImage},
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "Jane Doe"),
			testutil.EXTHString(100, "John Roe"),
			testutil.EXTHString(101, "Fixture Press"),
			testutil.EXTHString(104, "978-0000000000"),
			testutil.EXTHString(106, "2026-01-02"),
			testutil.EXTHString(103, "A synthetic PalmDOC book authored for the mobi-go corpus."),
			testutil.EXTHUint(201, 0), // coverOffset: the GIF
			testutil.EXTHUint(202, 1), // thumbnailOffset: the PNG
		},
	}
	return testutil.BuildBookWithIndex(book, base)
}

// buildMobi6HuffCDIC: HUFF/CDIC-compressed book. The whole text is
// authored as a phrase dictionary (pieces[3] == pieces[5] so phrase 3
// can nest through phrase 5, exercising recursive expansion and
// memoization), split into ~4KB text records of phrase-index streams.
func buildMobi6HuffCDIC() []byte {
	const (
		chunkTarget = 4096 // uncompressed bytes per text record
		cdicEntries = 64   // phrases per CDIC record
	)

	pieces := []string{
		"<html><head><title>HUFF",
		" CDIC Synthetic</title></hea",
		"der></head><body>",
		"<p>filler paragraph aaaaaa", // piece 3
		"bbbb</p>",
		"<p>filler paragraph aaaaaa", // piece 5 == piece 3
		"bbbb</p>",
	}
	chapters := []string{
		"<h1>Compressed</h1><p>This text arrives through the HUFF/CDIC phrase dictionary.</p>",
		"<h1>Dictionary</h1><p>Phrases expand recursively; one phrase nests through another.</p>",
		"<h1>Records</h1><p>Streams split across records with trailing bookkeeping bytes.</p>",
	}
	// Chapters begin on phrase boundaries so their filepos values are
	// simply cumulative text lengths.
	var starts []int
	pos := 0
	for _, ch := range chapters {
		pieces = append(pieces, "<mbp:pagebreak/>")
		pos += len("<mbp:pagebreak/>")
		starts = append(starts, pos)
		for len(ch) > 32 {
			pieces = append(pieces, ch[:32])
			pos += 32
			ch = ch[32:]
		}
		pieces = append(pieces, ch)
		pos += len(ch)
	}
	text := strings.Join(pieces, "")
	if text == "" {
		fail("empty huffcdic text")
	}

	phrases := make([][]byte, len(pieces))
	for i, p := range pieces {
		phrases[i] = []byte(p)
	}
	if string(phrases[3]) != string(phrases[5]) {
		fail("phrase 3 and 5 must be identical for the nested expansion")
	}
	nested := map[int][]int{3: {5}}

	d := len(phrases)
	short := d / 2
	if (1024-d)/4 < short {
		fail("dictionary of %d phrases cannot use shortCount %d", d, short)
	}
	hc := testutil.NewHuffCDIC(phrases, short, nested)

	// Split the index stream into records of ~chunkTarget output bytes.
	var textRecs [][]byte
	var cur []int
	size := 0
	for i, p := range pieces {
		if size >= chunkTarget {
			textRecs = append(textRecs, testutil.AppendTrailingEntries(hc.Encode(cur), 0b11))
			cur, size = nil, 0
		}
		cur = append(cur, i)
		size += len(p)
	}
	if len(cur) > 0 {
		textRecs = append(textRecs, testutil.AppendTrailingEntries(hc.Encode(cur), 0b11))
	}
	numText := len(textRecs)

	// Record layout: rec0, text records, HUFF, CDICs, images, NCX.
	cdicRecs := hc.CDICRecords(cdicEntries)
	huffIdx := 1 + numText
	imgIdx := huffIdx + 1 + len(cdicRecs)
	images := [][]byte{gifCover, pngImage}

	labels := []string{"Compressed", "Dictionary", "Records"}
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX:     [][]string{labels},
	}
	built := testutil.BuildIndex(base)
	base.Entries = []testutil.IndexEntry{
		{Values: map[int][]int{1: {starts[0]}, 3: {built.CNCX[0]}, 4: {0}}},
		{Values: map[int][]int{1: {starts[1]}, 3: {built.CNCX[1]}, 4: {0}}},
		{Values: map[int][]int{1: {starts[2]}, 3: {built.CNCX[2]}, 4: {0}}},
	}
	ncx := testutil.BuildIndex(base)
	ncxIdx := imgIdx + len(images)

	records := append([][]byte{}, textRecs...)
	records = append(records, hc.HUFFRecord())
	records = append(records, cdicRecs...)
	records = append(records, images...)
	records = append(records, ncx.Records...)

	rec0 := testutil.BuildRecord0(testutil.Record0Config{
		Compression:     17480,
		TextLength:      uint32(len(text)),
		NumTextRecords:  uint16(numText),
		RecordSize:      chunkTarget,
		Version:         6,
		TrailingFlags:   0b11,
		Huffcdic:        testutil.U32(uint32(huffIdx)),
		NumHuffcdic:     uint32(1 + len(cdicRecs)),
		FirstImageIndex: testutil.U32(uint32(imgIdx)),
		Indx:            testutil.U32(uint32(ncxIdx)),
		Title:           "HUFF CDIC Synthetic",
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "Huff Author"),
			testutil.EXTHUint(201, 0),
		},
	})
	return testutil.Build(append([][]byte{rec0}, records...)...)
}

// ---------------------------------------------------------------- KF8// buildKF8: pure AZW3 — three authored sections (one cut inside a
// tag, one multi-fragment, one fragmentless), a CSS extra flow, cover
// + image resources, RESC page spreads, an NCX, and a guide index.
func buildKF8() []byte {
	coverDoc := `<html><head><title>Cover</title></head><body><div id="cover"><img kindle:embed="00001" alt="cover"/></div></body></html>`
	ch1 := `<?xml version="1.0" encoding="utf-8"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter One</title><link kindle:flowstyle="0001" type="text/css"/></head><body><h1 id="ch1">Chapter One</h1><p>The first section reassembles from a skeleton and its fragments.</p><p>Fragments splice at insert offsets, even mid-tag.</p></body></html>`
	ch2 := `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter Two</title></head><body><h1 id="ch2">Chapter Two</h1><p>Second section, also fragmented.</p></body></html>`
	tail := `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Colophon</title></head><body><p>Non-linear tail with no fragments.</p></body></html>`

	// One cut covers the opening <h1 ...> tag whole (its end lands
	// mid-attribute when a longer tag is cut), the other the heading
	// text.
	h1Open := strings.Index(ch1, "<h1")
	h1TagEnd := h1Open + strings.Index(ch1[h1Open:], ">") + 1
	h1Text := strings.Index(ch1, "Chapter One</h1>")
	authors := []testutil.KF8Author{
		{XHTML: coverDoc, Cuts: [][2]int{{strings.Index(coverDoc, "<img"), strings.Index(coverDoc, "/>") + 2}}},
		{XHTML: ch1, Cuts: [][2]int{
			{h1Open, h1TagEnd},    // the opening tag, whole
			{h1Text, h1Text + 25}, // the heading text
		}},
		{XHTML: ch2, Cuts: [][2]int{
			{strings.Index(ch2, "<h1"), strings.Index(ch2, "</h1>")},
		}},
		{XHTML: tail},
	}
	layout, built := testutil.AuthorKF8(authors, []string{"body { margin: 0; }\np { text-indent: 1em; }"})

	// TOC targets: the heading text inside each chapter's heading cut.
	tocOf := func(sec, cut int) testutil.KF8TOCEntry {
		doc := authors[sec].XHTML
		c := authors[sec].Cuts[cut]
		h := strings.Index(doc[c[0]:c[1]], "Chapter")
		return testutil.KF8TOCEntry{Label: "Chapter " + string(rune('0'+sec)), Section: sec, Offset: c[0] + h}
	}
	toc := []testutil.KF8TOCEntry{
		{Label: "Cover", Section: 0, Offset: authors[0].Cuts[0][0] + 1},
		tocOf(1, 1),
		tocOf(2, 0),
	}

	// Guide index: a "toc" reference into the cover, a "text"
	// reference into chapter one's heading fragment.
	guideBase := testutil.IndexConfig{
		TagTable: guideTagTable,
		CNCX:     [][]string{{"Table of Contents", "Begin Reading"}},
	}
	guideBuilt := testutil.BuildIndex(guideBase)
	fid, off := posOf(layout, built, 1, authors[1].Cuts[1][0]+strings.Index(authors[1].XHTML[authors[1].Cuts[1][0]:], "Chapter"))
	guideBase.Entries = []testutil.IndexEntry{
		{Name: "toc", Values: map[int][]int{1: {guideBuilt.CNCX[0]}, 3: {0}, 6: {fid, off}}},
		{Name: "text", Values: map[int][]int{1: {guideBuilt.CNCX[1]}, 3: {1}, 6: {fid, off}}},
	}
	guide := testutil.BuildIndex(guideBase)

	spec := testutil.KF8BookSpec{
		Layout:       layout,
		Resources:    [][]byte{gifCover, pngImage},
		RESCSpine:    `<spine><itemref skelid="0000" properties="rendition:page-spread-center"/><itemref skelid="0001" properties="rendition:page-spread-left"/></spine>`,
		TOC:          toc,
		Title:        "Synthetic AZW3",
		ExtraRecords: guide.Records,
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "KF8 Author"),
			testutil.EXTHString(101, "Fixture Press"),
			testutil.EXTHUint(201, 0),
		},
	}
	return testutil.BuildKF8(spec).Data
}

// posOf resolves a (section, docOffset) target to its fragment's
// (fid, off) pair using the layout the authoring pass produced.
func posOf(layout testutil.KF8Layout, built []testutil.BuiltKF8Section, section, offset int) (int, int) {
	row := 0
	for i := range section {
		row += built[i].NumFrag
	}
	skel := layout.Skel[section]
	for k := range built[section].NumFrag {
		f := layout.Frag[row+k]
		start := f.InsertOffset - skel.Offset
		if offset >= start && offset < start+f.Length {
			return f.Seq, offset - start
		}
	}
	fail("guide target (section %d, offset %d) not inside a fragment", section, offset)
	return 0, 0
}

// buildCombo: MOBI6 half (PalmDOC, shared images, EXTH 121 written by
// the combo builder) + KF8 half in one file.
func buildCombo() []byte {
	chapters := []string{
		`<html><head><guide><reference type="text" title="Begin" filepos="{F1}"/></guide></head><body>`,
		`<h1>Combo Half</h1><p>The MOBI6 half of a combination file.</p><img recindex="00001">`,
		`<h1>Second</h1><p>Still MOBI6.</p></body></html>`,
	}
	build := func(offs []string) (string, []int) {
		var b strings.Builder
		starts := make([]int, len(chapters))
		for i, ch := range chapters {
			if i > 0 {
				b.WriteString("<mbp:pagebreak/>")
			}
			starts[i] = b.Len()
			b.WriteString(ch)
		}
		out := b.String()
		for i, off := range offs {
			out = strings.Replace(out, fmt.Sprintf("{F%d}", i+1), off, 1)
		}
		return out, starts
	}
	_, starts := build([]string{"00000000", "00000000", "00000000"})
	offs := make([]string, len(starts))
	for i, s := range starts {
		offs[i] = fmt.Sprintf("%08d", s)
	}
	text, _ := build(offs)

	m6 := testutil.BookConfig{
		Text:        text,
		Compression: 2,
		RecordSize:  512,
		Title:       "Synthetic Combo",
		Resources:   [][]byte{gifCover, pngImage},
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(100, "Combo Author"),
			testutil.EXTHUint(201, 0),
		},
	}

	kf8Doc := `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>KF8 Half</title></head><body><h1 id="k">KF8 Half</h1><p>The KF8 half of the same combination file.</p></body></html>`
	cutStart := strings.Index(kf8Doc, "<h1")
	heading := strings.Index(kf8Doc[cutStart:], "KF8 Half") + cutStart
	authors := []testutil.KF8Author{{
		XHTML: kf8Doc,
		Cuts:  [][2]int{{cutStart, strings.Index(kf8Doc, "</h1>")}},
	}}
	layout, _ := testutil.AuthorKF8(authors, nil)
	k8 := testutil.KF8BookSpec{
		Layout:    layout,
		Resources: nil, // shared images live in the MOBI6 half
		TOC:       []testutil.KF8TOCEntry{{Label: "KF8 Half", Section: 0, Offset: heading}},
		Title:     "Synthetic Combo KF8",
		EXTH: []testutil.EXTHRecord{
			testutil.EXTHString(503, "Synthetic Combo"),
			testutil.EXTHString(100, "Combo Author"),
			testutil.EXTHUint(201, 0),
		},
	}
	data, _ := testutil.BuildComboFile(m6, k8)
	return data
}

// ---------------------------------------------------------------- refusals

// buildDRM: a valid MOBI6 file whose PalmDOC encryption type is 1 —
// refused whole with ErrDRM.
func buildDRM() []byte {
	text := "<html><body><p>Encrypted fixture; never parsed.</p></body></html>"
	rec0 := testutil.BuildRecord0(testutil.Record0Config{
		Compression:    2,
		TextLength:     uint32(len(text)),
		NumTextRecords: 1,
		Encryption:     1,
		Version:        6,
		Title:          "Locked Fixture",
	})
	rec := testutil.CompressPalmDOC([]byte(text))
	return testutil.Build(rec0, rec)
}

// buildNotPalmDB: a container with a type/creator nobody accepts.
func buildNotPalmDB() []byte {
	out := make([]byte, 256)
	copy(out, "Definitely not a database")
	copy(out[60:], "JUNKJUNK")
	return out
}

// buildTruncated: the PalmDOC fixture cut mid-file, so record offsets
// point past EOF.
func buildTruncated() []byte {
	full := buildMobi6Palmdoc()
	cut := len(full) * 2 / 5
	if cut < 512 {
		fail("truncation cut (%d) leaves too little header", cut)
	}
	return full[:cut]
}

// buildCorruptFDST: a valid KF8 book whose FDST first range ends far
// past the raw flow.
func buildCorruptFDST() []byte {
	rec0, records, _ := testutil.BuildKF8Parts(testutil.KF8BookSpec{
		Layout: func() testutil.KF8Layout {
			doc := `<html><body><p>fdst corruption target</p></body></html>`
			l, _ := testutil.AuthorKF8([]testutil.KF8Author{{XHTML: doc}}, nil)
			return l
		}(),
	})
	for i, rec := range records {
		if len(rec) >= 12 && string(rec[:4]) == "FDST" {
			patched := append([]byte(nil), rec...)
			// First entry's end at an absurd offset.
			patched[16] = 0x7F
			patched[17] = 0xFF
			patched[18] = 0xFF
			patched[19] = 0xFF
			records[i] = patched
			break
		}
	}
	return testutil.Build(append([][]byte{rec0}, records...)...)
}
