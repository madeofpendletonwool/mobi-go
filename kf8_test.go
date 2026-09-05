// KF8 reassembly tests: assembly round-trips over authored sections
// (cuts inside tags included), combo files, flows and kindle: URIs,
// RESC page spreads, TOC section resolution, corruption shapes, and
// the assembly fuzz target.

package mobi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// tinyGIF is a 1x1 GIF image record.
var tinyGIF = []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00,")

// tinyPNG is a PNG-header-only image record.
var tinyPNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}

// kf8Fixture authors a three-section book exercising the assembly
// corners: section 0 carries three fragments (one cut inside a tag,
// the attribute value of <h1 class="title">), section 1 has zero
// fragments (linear no), and section 2 carries one whole-body
// fragment. Two extra flows ride after the text flow.
func kf8Fixture() (testutil.KF8Layout, []testutil.BuiltKF8Section, []string) {
	var cutsA [][2]int
	docA := `<html><head><title>A</title></head><body>`
	docA += `<h1 class=`
	start := len(docA)
	docA += `"title"`
	cutsA = append(cutsA, [2]int{start, len(docA)}) // inside the tag
	docA += `>Chapter One</h1>`
	start = len(docA)
	docA += `<p>alpha</p>`
	cutsA = append(cutsA, [2]int{start, len(docA)})
	start = len(docA)
	docA += `<p>beta</p>`
	cutsA = append(cutsA, [2]int{start, len(docA)})
	docA += `</body></html>`

	docB := `<html><head><title>B</title></head><body></body></html>`
	docC := `<html><head><title>C</title></head><body><p>gamma</p></body></html>`
	cutC := [2]int{strings.Index(docC, `<p>gamma`), strings.Index(docC, `</body>`)}

	sections := []testutil.KF8Author{
		{XHTML: docA, Cuts: cutsA},
		{XHTML: docB},
		{XHTML: docC, Cuts: [][2]int{cutC}},
	}
	flows := []string{"body { margin: 0 }", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`}
	layout, built := testutil.AuthorKF8(sections, flows)
	return layout, built, []string{docA, docB, docC}
}

func openKF8(t *testing.T, spec testutil.KF8BookSpec) *Book {
	t.Helper()
	data := testutil.BuildKF8(spec).Data
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

func TestKF8Assembly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		compression uint16
		recordSize  uint16
	}{
		{name: "uncompressed-single-record"},
		{name: "palmdoc-multi-record", compression: 2, recordSize: 48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, facts, docs := kf8Fixture()
			b := openKF8(t, testutil.KF8BookSpec{
				Layout:      layout,
				Compression: tc.compression,
				RecordSize:  tc.recordSize,
			})
			sections := b.KF8Sections()
			if len(sections) != len(docs) {
				t.Fatalf("assembled %d sections, want %d", len(sections), len(docs))
			}
			for i, doc := range docs {
				if got := sections[i].XHTML(); got != doc {
					t.Errorf("section %d XHTML = %q, want %q", i, got, doc)
				}
				if sections[i].SizeBytes != len(doc) {
					t.Errorf("section %d SizeBytes = %d, want %d", i, sections[i].SizeBytes, len(doc))
				}
				wantLinear := facts[i].NumFrag > 0
				if sections[i].Linear != wantLinear {
					t.Errorf("section %d Linear = %v, want %v", i, sections[i].Linear, wantLinear)
				}
			}
		})
	}
}

func TestKF8Flow(t *testing.T) {
	layout, _, _ := kf8Fixture()
	b := openKF8(t, testutil.KF8BookSpec{Layout: layout})

	if got := mustFlow(t, b, 0); got != layout.Flow0 {
		t.Errorf("flow 0 mismatch: %d bytes, want the %d-byte text flow", len(got), len(layout.Flow0))
	}
	for i, want := range layout.ExtraFlows {
		if got := mustFlow(t, b, i+1); got != want {
			t.Errorf("flow %d = %q, want %q", i+1, got, want)
		}
	}
	if _, err := b.Flow(len(layout.ExtraFlows) + 1); !errors.Is(err, ErrRecordRange) {
		t.Errorf("Flow past the end = %v, want ErrRecordRange", err)
	}
	if _, err := b.Flow(-1); !errors.Is(err, ErrRecordRange) {
		t.Errorf("Flow(-1) = %v, want ErrRecordRange", err)
	}

	// A KF8 book with no FDST record degenerates to one flow covering
	// the whole raw flow — the text flow and the extra flows alike.
	b = openKF8(t, testutil.KF8BookSpec{Layout: layout, SkipFDST: true})
	whole := layout.Flow0
	for _, extra := range layout.ExtraFlows {
		whole += extra
	}
	if got := mustFlow(t, b, 0); got != whole {
		t.Errorf("FDST-less flow 0 = %d bytes, want the %d-byte whole raw flow", len(got), len(whole))
	}
	if _, err := b.Flow(1); !errors.Is(err, ErrRecordRange) {
		t.Errorf("Flow(1) without FDST = %v, want ErrRecordRange", err)
	}
}

func mustFlow(t *testing.T, b *Book, i int) string {
	t.Helper()
	data, err := b.Flow(i)
	if err != nil {
		t.Fatalf("Flow(%d): %v", i, err)
	}
	return string(data)
}

func TestKindleURI(t *testing.T) {
	cases := []struct {
		uri   string
		want  KindleURI
		fails bool
	}{
		{uri: "kindle:flow:0010?mime=text/css", want: KindleURI{Kind: KindleURIFlow, Flow: 32, MIME: "text/css"}},
		{uri: "kindle:flow:0000", want: KindleURI{Kind: KindleURIFlow, Flow: 0}},
		{uri: "kindle:embed:000B?mime=image/gif", want: KindleURI{Kind: KindleURIEmbed, Embed: 11, MIME: "image/gif"}},
		{uri: "kindle:embed:0001", want: KindleURI{Kind: KindleURIEmbed, Embed: 1}},
		{uri: "kindle:pos:fid:000A:off:0000000010", want: KindleURI{Kind: KindleURIPos, Fid: 10, Off: 32}},
		{uri: "kindle:pos:fid:0001:off:0000000000", want: KindleURI{Kind: KindleURIPos, Fid: 1, Off: 0}},
		{uri: "kindle:flow:", fails: true},
		{uri: "kindle:embed:00W1", fails: true}, // W is outside base-32
		{uri: "kindle:pos:fid:1", fails: true},  // missing off
		{uri: "kindle:pos:fid:1:off:", fails: true},
		{uri: "filepos:123", fails: true},
		{uri: "", fails: true},
	}
	for _, tc := range cases {
		got, err := ParseKindleURI(tc.uri)
		if tc.fails {
			if err == nil {
				t.Errorf("ParseKindleURI(%q) = %+v, want an error", tc.uri, got)
			} else if !errors.Is(err, ErrNotKindleURI) {
				t.Errorf("ParseKindleURI(%q) = %v, want ErrNotKindleURI", tc.uri, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseKindleURI(%q): %v", tc.uri, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseKindleURI(%q) = %+v, want %+v", tc.uri, got, tc.want)
		}
	}
}

func TestResolveKindleURI(t *testing.T) {
	layout, facts, _ := kf8Fixture()
	b := openKF8(t, testutil.KF8BookSpec{
		Layout:    layout,
		Resources: [][]byte{tinyGIF, tinyPNG},
	})

	// kindle:pos round-trips on targets inside fragments of sections
	// 0 and 2: the resolved section and offset are the insert position
	// (section-relative) plus the offset into the fragment payload.
	sec0Frag1 := facts[0].Inserts[1] - facts[0].SkelOffset
	uri := "kindle:pos:fid:" + pad32(facts[0].FragSeqs[1]) + ":off:" + pad32Wide(3)
	got, err := b.ResolveKindleURI(uri)
	if err != nil {
		t.Fatalf("ResolveKindleURI(%q): %v", uri, err)
	}
	if got.Section != 0 || got.Offset != sec0Frag1+3 {
		t.Errorf("pos uri resolved to (section %d, offset %d), want (0, %d)", got.Section, got.Offset, sec0Frag1+3)
	}
	sec2Frag0 := facts[2].Inserts[0] - facts[2].SkelOffset
	uri = "kindle:pos:fid:" + pad32(facts[2].FragSeqs[0]) + ":off:" + pad32Wide(5)
	got, err = b.ResolveKindleURI(uri)
	if err != nil {
		t.Fatalf("ResolveKindleURI(%q): %v", uri, err)
	}
	if got.Section != 2 || got.Offset != sec2Frag0+5 {
		t.Errorf("pos uri resolved to (section %d, offset %d), want (2, %d)", got.Section, got.Offset, sec2Frag0+5)
	}

	// Embeds resolve against the resource range, 1-based.
	got, err = b.ResolveKindleURI("kindle:embed:0002?mime=image/png")
	if err != nil {
		t.Fatalf("ResolveKindleURI embed: %v", err)
	}
	if got.Embed != 2 {
		t.Errorf("embed uri = %+v, want Embed 2", got)
	}
	data, mime, err := b.Resource(got.Embed - 1)
	if err != nil || !bytes.Equal(data, tinyPNG) || mime != "image/png" {
		t.Errorf("embed 2 resource = %d bytes (%s, %v), want the tiny PNG", len(data), mime, err)
	}

	// Failure shapes.
	for _, tc := range []struct {
		uri  string
		want error
	}{
		{"kindle:flow:0009", ErrRecordRange},
		{"kindle:embed:0999", ErrRecordRange},
		{"kindle:pos:fid:0999:off:0000000000", ErrCorrupt},
		{"kindle:pos:fid:" + pad32(facts[0].FragSeqs[1]) + ":off:" + pad32Wide(100000), ErrCorrupt},
		{"kindle:nope:0001", ErrNotKindleURI},
	} {
		if _, err := b.ResolveKindleURI(tc.uri); !errors.Is(err, tc.want) {
			t.Errorf("ResolveKindleURI(%q) = %v, want %v", tc.uri, err, tc.want)
		}
	}
}

// pad32 renders v as the 4-digit base-32 form kindle:pos URIs carry.
func pad32(v int) string { return toBase32(v, 4) }

// pad32Wide is pad32 with the 10-digit off padding.
func pad32Wide(v int) string { return toBase32(v, 10) }

func TestKF8RESC(t *testing.T) {
	layout, _, _ := kf8Fixture()
	spine := `<?xml version="1.0"?><package version="2.0"><spine>` +
		`<itemref skelid="0" properties="page-spread-left"/>` +
		`<itemref skelid="1" properties="rendition:page-spread-center"/>` +
		`<itemref skelid="2"/></spine></package>`
	b := openKF8(t, testutil.KF8BookSpec{Layout: layout, Resources: [][]byte{tinyGIF}, RESCSpine: spine})
	want := []string{"left", "center", ""}
	for i, w := range want {
		if got := b.KF8Sections()[i].PageSpread; got != w {
			t.Errorf("section %d PageSpread = %q, want %q", i, got, w)
		}
	}

	// A RESC fragment without the <package> root still parses (the
	// parser wraps it, as foliate-js does).
	fragment := `<spine><itemref skelid="2" properties="page-spread-right"/></spine>`
	b = openKF8(t, testutil.KF8BookSpec{Layout: layout, Resources: [][]byte{tinyGIF}, RESCSpine: fragment})
	if got := b.KF8Sections()[2].PageSpread; got != "right" {
		t.Errorf("fragment RESC section 2 PageSpread = %q, want right", got)
	}

	// No RESC record: no spreads anywhere.
	b = openKF8(t, testutil.KF8BookSpec{Layout: layout})
	for i := range b.KF8Sections() {
		if got := b.KF8Sections()[i].PageSpread; got != "" {
			t.Errorf("section %d PageSpread = %q, want empty", i, got)
		}
	}
}

func TestKF8TOC(t *testing.T) {
	layout, facts, _ := kf8Fixture()
	toc0 := facts[0].Inserts[1] - facts[0].SkelOffset + 1 // inside fragment 1 of section 0
	toc2 := facts[2].Inserts[0] - facts[2].SkelOffset + 6 // inside fragment 0 of section 2
	b := openKF8(t, testutil.KF8BookSpec{
		Layout: layout,
		TOC: []testutil.KF8TOCEntry{
			{Label: "Chapter One", Section: 0, Offset: toc0},
			{Label: "Gamma", Section: 2, Offset: toc2},
		},
	})
	items, err := b.TOC()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("NCX returned %d items, want 2", len(items))
	}
	if items[0].Label != "Chapter One" || items[0].Section != 0 || items[0].SectionOffset != toc0 {
		t.Errorf("item 0 = %+v, want section 0 offset %d label Chapter One", items[0], toc0)
	}
	if items[1].Label != "Gamma" || items[1].Section != 2 || items[1].SectionOffset != toc2 {
		t.Errorf("item 1 = %+v, want section 2 offset %d label Gamma", items[1], toc2)
	}
	for _, item := range items {
		if item.Fid < 0 || item.Off < 0 {
			t.Errorf("item %q carries no pos pair: %+v", item.Label, item)
		}
	}
}

func TestKF8Combo(t *testing.T) {
	layout, _, docs := kf8Fixture()
	m6Text := `<html><body><p>old mobi</p><mbp:pagebreak/><p>still mobi</p></body></html>`
	m6 := testutil.BookConfig{
		Text:      m6Text,
		Title:     "Combo",
		Resources: [][]byte{tinyGIF, tinyPNG},
		EXTH:      []testutil.EXTHRecord{testutil.EXTHUint(201, 0)},
	}
	k8 := testutil.KF8BookSpec{
		Layout: layout,
		EXTH:   []testutil.EXTHRecord{testutil.EXTHUint(201, 0)},
	}
	data, boundary := testutil.BuildComboFile(m6, k8)
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open combo: %v", err)
	}
	if boundary < 1 || data == nil {
		t.Fatal("combo fixture failed to build")
	}
	if !b.IsKF8() || !b.HasMOBI6Half() {
		t.Fatalf("combo opened as KF8=%v with MOBI6 half=%v, want true/true", b.IsKF8(), b.HasMOBI6Half())
	}
	sections := b.KF8Sections()
	if len(sections) != len(docs) {
		t.Fatalf("combo KF8 half assembled %d sections, want %d", len(sections), len(docs))
	}
	for i, doc := range docs {
		if sections[i].XHTML() != doc {
			t.Errorf("combo section %d XHTML mismatch", i)
		}
	}

	// Shared images: the cover resolves through the MOBI6 half's
	// absolute firstImageIndex, which the KF8 header's own field does
	// not name.
	cover, mime, err := b.Cover()
	if err != nil || !bytes.Equal(cover, tinyGIF) || mime != "image/gif" {
		t.Errorf("combo cover = %d bytes (%s, %v), want the shared tiny GIF", len(cover), mime, err)
	}

	// The MOBI6 half reads as a plain MOBI6 book.
	half, err := b.MOBI6Half()
	if err != nil {
		t.Fatalf("MOBI6Half: %v", err)
	}
	if half.IsKF8() || half.HasMOBI6Half() {
		t.Errorf("MOBI6 half is KF8=%v combo=%v, want false/false", half.IsKF8(), half.HasMOBI6Half())
	}
	if got := half.Text(); got != m6Text {
		t.Errorf("MOBI6 half text = %q, want the authored text", got)
	}
	if len(half.Sections()) != 2 {
		t.Errorf("MOBI6 half has %d pagebreak sections, want 2", len(half.Sections()))
	}
	halfCover, halfMime, err := half.Cover()
	if err != nil || !bytes.Equal(halfCover, tinyGIF) || halfMime != "image/gif" {
		t.Errorf("MOBI6 half cover = %d bytes (%s, %v), want the shared tiny GIF", len(halfCover), halfMime, err)
	}
	if _, err := b.MOBI6Half(); err != nil {
		t.Fatalf("second MOBI6Half call: %v", err)
	}

	// The plain MOBI6 half of the world: a non-combo book has no
	// MOBI6 half to read.
	plain := testutil.BuildBook(testutil.BookConfig{Text: m6Text})
	pb, err := Open(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatalf("Open plain: %v", err)
	}
	if pb.HasMOBI6Half() {
		t.Error("plain MOBI6 book claims a MOBI6 half")
	}
	if _, err := pb.MOBI6Half(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("MOBI6Half on a plain book = %v, want ErrCorrupt", err)
	}
}

func TestKF8Corruption(t *testing.T) {
	layout, _, _ := kf8Fixture()
	valid := testutil.KF8BookSpec{
		Layout:    layout,
		Resources: [][]byte{tinyGIF},
	}

	// withFDST re-renders the book around a replaced FDST record.
	withFDST := func(t *testing.T, fdst []byte) []byte {
		t.Helper()
		rec0, records, built := testutil.BuildKF8Parts(valid)
		if built.FDSTIndex < 0 {
			t.Fatal("fixture has no FDST record to replace")
		}
		records[built.FDSTIndex-1] = fdst
		return testutil.Build(append([][]byte{rec0}, records...)...)
	}
	// withLayout re-renders the book around a mutated layout.
	withLayout := func(t *testing.T, mutate func(l testutil.KF8Layout) testutil.KF8Layout) []byte {
		t.Helper()
		spec := valid
		spec.Layout = mutate(spec.Layout)
		return testutil.BuildKF8(spec).Data
	}

	badMagic := append([]byte("XXXX"), make([]byte, 20)...)
	overflowRanges := append([][2]int{}, validRanges(valid)...)
	overflowRanges[len(overflowRanges)-1][1] += 1 << 20
	fdstOverflow := testutil.FDSTRecord(overflowRanges)
	beyondRaw := make([]byte, 12)
	binary.BigEndian.PutUint32(beyondRaw[8:], 0x7FFF) // 32747 entries in a 12-byte record

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "fdst bad magic", data: withFDST(t, badMagic)},
		{name: "fdst end past raw flow", data: withFDST(t, fdstOverflow)},
		{name: "fdst count overflows record", data: withFDST(t, beyondRaw)},
		{name: "skel index missing", data: testutil.BuildKF8(testutil.KF8BookSpec{Layout: layout, SkelMissing: true}).Data},
		{name: "frag index missing", data: testutil.BuildKF8(testutil.KF8BookSpec{Layout: layout, FragMissing: true}).Data},
		{name: "frag name not a number", data: withLayout(t, func(l testutil.KF8Layout) testutil.KF8Layout {
			l.Frag[0].Name = "not-a-number"
			return l
		})},
		{name: "skel offset past raw flow", data: withLayout(t, func(l testutil.KF8Layout) testutil.KF8Layout {
			l.Skel[0].Offset += len(l.Flow0) + 1000
			return l
		})},
		{name: "numFrag past fragment table", data: withLayout(t, func(l testutil.KF8Layout) testutil.KF8Layout {
			l.Skel[0].NumFrag += 100
			return l
		})},
		{name: "insert position past section", data: withLayout(t, func(l testutil.KF8Layout) testutil.KF8Layout {
			l.Frag[0].InsertOffset += 1 << 20
			return l
		})},
		{name: "fragment payload past raw flow", data: withLayout(t, func(l testutil.KF8Layout) testutil.KF8Layout {
			l.Frag[len(l.Frag)-1].Length += 1 << 20
			return l
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Open(bytes.NewReader(tc.data), int64(len(tc.data)))
			if err == nil {
				t.Fatalf("Open succeeded, want ErrCorrupt")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("Open = %v, want ErrCorrupt", err)
			}
			if b != nil {
				t.Error("Open returned a book alongside an error")
			}
		})
	}
}

// validRanges derives a spec's FDST ranges from its layout.
func validRanges(spec testutil.KF8BookSpec) [][2]int {
	ranges := [][2]int{{0, len(spec.Layout.Flow0)}}
	end := len(spec.Layout.Flow0)
	for _, f := range spec.Layout.ExtraFlows {
		ranges = append(ranges, [2]int{end, end + len(f)})
		end += len(f)
	}
	return ranges
}

func FuzzKF8Assembly(f *testing.F) {
	layout, _, _ := kf8Fixture()

	// Seed with the valid fixture's index records and raw flow, a
	// dangling-insert variant, and truncations.
	skel := testutil.BuildIndex(testutil.IndexConfig{
		TagTable: testutil.SkelTagTable,
		Entries:  skelEntries(layout),
	})
	frag := testutil.BuildIndex(testutil.IndexConfig{
		TagTable: testutil.FragTagTable,
		Entries:  fragEntries(layout),
	})
	fdst := testutil.FDSTRecord([][2]int{{0, len(layout.Flow0)}})
	f.Add(skel.Records[0], skel.Records[len(skel.Records)-1], frag.Records[0], frag.Records[len(frag.Records)-1], fdst, []byte(layout.Flow0))

	mutated := layout
	mutated.Frag[0].InsertOffset += 1 << 20
	fragBad := testutil.BuildIndex(testutil.IndexConfig{
		TagTable: testutil.FragTagTable,
		Entries:  fragEntries(mutated),
	})
	f.Add(skel.Records[0], skel.Records[len(skel.Records)-1], fragBad.Records[0], fragBad.Records[len(fragBad.Records)-1], fdst, []byte(mutated.Flow0))
	f.Add([]byte("INDX"), []byte("INDX"), []byte("INDX"), []byte("INDX"), fdst, []byte(layout.Flow0))
	f.Add([]byte(nil), []byte(nil), []byte(nil), []byte(nil), []byte(nil), []byte(nil))

	f.Fuzz(func(t *testing.T, skelFirst, skelEntry, fragFirst, fragEntry, fdst, raw []byte) {
		idx := func(v uint32) *uint32 { return &v }
		rec0 := testutil.BuildRecord0(testutil.Record0Config{
			Compression:    1,
			TextLength:     uint32(len(raw)),
			NumTextRecords: 1,
			Version:        8,
			FDST:           idx(2),
			Skel:           idx(3),
			Frag:           idx(5),
		})
		data := testutil.Build(rec0, raw, fdst, skelFirst, skelEntry, fragFirst, fragEntry)
		b, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
				t.Errorf("Open = %v, want a typed ErrCorrupt/ErrRecordRange", err)
			}
			return
		}
		for i := range b.KF8Sections() {
			_ = b.KF8Sections()[i].XHTML()
		}
		_, _ = b.Flow(0)
		if _, err := b.ResolveKindleURI("kindle:pos:fid:0001:off:0000000000"); err != nil &&
			!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
			t.Errorf("ResolveKindleURI = %v, want typed", err)
		}
		if _, err := b.TOC(); err != nil && !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
			t.Errorf("NCX = %v, want typed", err)
		}
	})
}

// skelEntries renders the layout's skeleton rows as index entries.
func skelEntries(layout testutil.KF8Layout) []testutil.IndexEntry {
	entries := make([]testutil.IndexEntry, len(layout.Skel))
	for i, s := range layout.Skel {
		entries[i] = testutil.IndexEntry{
			Name:   string(rune('0' + i)),
			Values: map[int][]int{1: {s.NumFrag}, 6: {s.Offset, s.Length}},
		}
	}
	return entries
}

// fragEntries renders the layout's fragment rows as index entries;
// selectors are omitted (their CNCX pool is optional).
func fragEntries(layout testutil.KF8Layout) []testutil.IndexEntry {
	entries := make([]testutil.IndexEntry, len(layout.Frag))
	for i, fr := range layout.Frag {
		entries[i] = testutil.IndexEntry{
			Name:   fr.Name,
			Values: map[int][]int{3: {fr.FileNum}, 4: {fr.Seq}, 6: {fr.Offset, fr.Length}},
		}
	}
	return entries
}
