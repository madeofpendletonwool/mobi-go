package mobi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// openIndexBook builds a book around cfg plus an INDX index and returns
// the opened Book and the index's first-record position.
func openIndexBook(t *testing.T, book testutil.BookConfig, idx testutil.IndexConfig) (*Book, int) {
	t.Helper()
	indx := uint32(1 + testutil.NumTextRecords(book) + len(book.Resources) + len(book.TrailingRecords))
	built := testutil.BuildIndex(idx)
	book.TrailingRecords = append(book.TrailingRecords, built.Records...)
	book.Indx = &indx
	data := testutil.BuildBook(book)
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b, int(indx)
}

// openIndexBookBytes is openIndexBook over pre-built index records, for
// corruption cases that mutate the bytes directly.
func openIndexBookBytes(t *testing.T, records [][]byte) (*Book, int) {
	t.Helper()
	book := testutil.BookConfig{Text: "hello"}
	indx := uint32(1 + testutil.NumTextRecords(book) + len(records))
	book.TrailingRecords = records
	book.Indx = &indx
	data := testutil.BuildBook(book)
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b, int(indx)
}

// ncxTagTable mirrors the tag layout a real NCX index carries: tags
// 1/2/3/4/5/6/21/22 fill control byte 0, a header-only marker ends it,
// and tag 23 lives in byte 1.
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

// multiBitTagTable packs multi-bit masks into control byte 0 so the
// round trip exercises inline counts and byte-count value encodings,
// plus a byte-1 tag after a header-only marker.
var multiBitTagTable = []testutil.TagDesc{
	{Tag: 1, ValuesPerEntry: 1, Mask: 0x60},
	{Tag: 2, ValuesPerEntry: 2, Mask: 0x18},
	{Tag: 3, ValuesPerEntry: 1, Mask: 0x06},
	{Tag: 4, ValuesPerEntry: 1, Mask: 0x01},
	{Tag: 0, ValuesPerEntry: 0, Mask: 0x00, EndFlags: 1},
	{Tag: 5, ValuesPerEntry: 1, Mask: 0x80},
}

// withEntries re-renders an index with entries that can reference the
// CNCX offsets of a first (entry-less) pass — the two-pass pattern
// label-carrying fixtures need.
func withEntries(base testutil.IndexConfig, entries []testutil.IndexEntry, perRecord int) testutil.IndexConfig {
	base.Entries = entries
	base.EntriesPerRecord = perRecord
	return base
}

func TestINDXRoundTripAllEncodings(t *testing.T) {
	entries := []testutil.IndexEntry{
		{Name: "inline-count-1", Values: map[int][]int{1: {10}, 4: {7}}},
		{Name: "inline-count-2", Values: map[int][]int{1: {10, 20}}},
		{Name: "byte-count-3", Values: map[int][]int{1: {1, 2, 3}}},
		{Name: "byte-count-overflow", Values: map[int][]int{1: {5, 6, 7, 8, 9}}},
		{Name: "vpe-2", Values: map[int][]int{2: {1, 2, 3, 4}}},
		{Name: "byte-count-tz1", Values: map[int][]int{3: {1, 2, 3}}},
		{Name: "byte-1-tag", Values: map[int][]int{5: {99}}},
		{Name: "all-unset", Values: map[int][]int{}},
	}
	b, indx := openIndexBook(t, testutil.BookConfig{Text: "hello"}, testutil.IndexConfig{
		TagTable: multiBitTagTable,
		Entries:  entries,
		CNCX:     [][]string{{"phrase"}},
	})
	got, _, err := b.readIndex(indx)
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("parsed %d entries, want %d", len(got), len(entries))
	}
	for i, want := range entries {
		if got[i].Name != want.Name {
			t.Errorf("entry %d name = %q, want %q", i, got[i].Name, want.Name)
		}
		for tag, wantVals := range want.Values {
			if len(wantVals) == 0 {
				continue
			}
			gotVals, ok := got[i].Values[tag]
			if !ok {
				t.Errorf("entry %d (%s): tag %d missing", i, want.Name, tag)
				continue
			}
			if !reflect.DeepEqual(gotVals, wantVals) {
				t.Errorf("entry %d (%s): tag %d = %v, want %v", i, want.Name, tag, gotVals, wantVals)
			}
		}
		for tag := range got[i].Values {
			if _, authored := want.Values[tag]; !authored {
				t.Errorf("entry %d (%s): unexpected tag %d = %v", i, want.Name, tag, got[i].Values[tag])
			}
		}
	}
}

func TestINDXMultiRecordAndCNCXStride(t *testing.T) {
	// Two CNCX groups: region 0 holds "Alpha" at the record start and
	// "Gamma" behind a large filler phrase (a deep region-0 offset),
	// and region 1's "Delta" is addressed from the 0x10000 stride base.
	filler := strings.Repeat("f", 0xFF00)
	base := testutil.IndexConfig{
		TagTable: multiBitTagTable,
		CNCX:     [][]string{{"Alpha", filler, "Gamma"}, {"Delta"}},
	}
	built := testutil.BuildIndex(base)
	if len(built.CNCX) != 4 {
		t.Fatalf("fixture built %d cncx offsets, want 4", len(built.CNCX))
	}
	entries := []testutil.IndexEntry{
		{Name: "region0-start", Values: map[int][]int{3: {built.CNCX[0]}}},
		{Name: "region0-deep", Values: map[int][]int{3: {built.CNCX[2]}}},
		{Name: "region1", Values: map[int][]int{3: {built.CNCX[3]}}},
	}
	b, indx := openIndexBook(t, testutil.BookConfig{Text: "hello"}, withEntries(base, entries, 1))
	got, pool, err := b.readIndex(indx)
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}
	// EntriesPerRecord 1 → three entry records; all entries present in
	// order with resolvable labels.
	wantLabels := []string{"Alpha", "Gamma", "Delta"}
	if len(got) != 3 {
		t.Fatalf("parsed %d entries, want 3", len(got))
	}
	for i, want := range wantLabels {
		off := got[i].Values[3][0]
		label, ok := pool.lookup(off)
		if !ok || label != want {
			t.Errorf("entry %d label(%d) = (%q, %v), want %q", i, off, label, ok, want)
		}
	}
}

func TestINDXProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x5eed, 0x1d3b))
	for iter := range 150 {
		table := randomTagTable(rng)
		groups := 1 + rng.IntN(3)
		var phrases [][]string
		for range groups {
			var group []string
			for range rng.IntN(5) {
				group = append(group, fmt.Sprintf("phrase %d/%d", iter, rng.IntN(1000)))
			}
			phrases = append(phrases, group)
		}
		base := testutil.IndexConfig{TagTable: table, CNCX: phrases}

		var entries []testutil.IndexEntry
		for range rng.IntN(7) {
			values := map[int][]int{}
			for _, row := range table {
				if row.EndFlags&1 != 0 || rng.IntN(2) == 0 {
					continue
				}
				// A mask of p bits expresses at most 2^p - 1 inline
				// groups (single-bit masks just one); anything up to
				// that bound keeps every encoding form reachable.
				capacity := 1<<bits.OnesCount8(row.Mask) - 1
				for range 1 + rng.IntN(capacity) {
					for range row.ValuesPerEntry {
						values[row.Tag] = append(values[row.Tag], rng.IntN(0xFFFF))
					}
				}
			}
			entries = append(entries, testutil.IndexEntry{
				Name:   fmt.Sprintf("n%d", rng.IntN(100)),
				Values: values,
			})
		}
		cfg := withEntries(base, entries, 1+rng.IntN(3))
		b, indx := openIndexBook(t, testutil.BookConfig{Text: "hello"}, cfg)
		got, _, err := b.readIndex(indx)
		if err != nil {
			t.Fatalf("iter %d: readIndex: %v", iter, err)
		}
		if len(got) != len(entries) {
			t.Fatalf("iter %d: parsed %d entries, want %d", iter, len(got), len(entries))
		}
		for i, want := range entries {
			if got[i].Name != want.Name {
				t.Fatalf("iter %d: entry %d name = %q, want %q", iter, i, got[i].Name, want.Name)
			}
			if !reflect.DeepEqual(got[i].Values, nonEmpty(want.Values)) {
				t.Fatalf("iter %d: entry %d values = %v, want %v", iter, i, got[i].Values, nonEmpty(want.Values))
			}
		}
	}
}

// randomTagTable builds a valid tag table the way real ones look:
// unique tags, contiguous masks packed head-first into each control
// byte, header-only markers between bytes.
func randomTagTable(rng *rand.Rand) []testutil.TagDesc {
	var table []testutil.TagDesc
	tag := 1
	budget := 8
	for len(table) < 2 || rng.IntN(2) == 0 {
		pop := 1 + rng.IntN(3)
		if pop > budget {
			table = append(table, testutil.TagDesc{Tag: 0, EndFlags: 1})
			budget = 8
			continue
		}
		mask := byte(((1 << pop) - 1) << (budget - pop))
		budget -= pop
		table = append(table, testutil.TagDesc{
			Tag:            tag,
			ValuesPerEntry: 1 + rng.IntN(2),
			Mask:           mask,
		})
		tag++
	}
	return table
}

// nonEmpty drops empty value lists — the parser omits tags with no
// values the way the encoder does.
func nonEmpty(m map[int][]int) map[int][]int {
	out := make(map[int][]int, len(m))
	for k, v := range m {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

// tocEqual compares the fields the NCX tree carries.
func tocEqual(got, want TOCItem) bool {
	if got.Label != want.Label || got.StartByte != want.StartByte ||
		got.Length != want.Length || got.Fid != want.Fid || got.Off != want.Off {
		return false
	}
	if len(got.Children) != len(want.Children) {
		return false
	}
	for i := range got.Children {
		if !tocEqual(got.Children[i], want.Children[i]) {
			return false
		}
	}
	return true
}

func TestNCXSyntheticBook(t *testing.T) {
	// Chapters are joined by pagebreaks with no preamble, and a
	// section boundary sits at the pagebreak tag's start, so each
	// chapter's filepos is captured before its pagebreak is written.
	chapters := []string{"Chapter One", "Chapter Two", "Chapter Three"}
	var text strings.Builder
	starts := make([]int, len(chapters))
	for i, ch := range chapters {
		if i > 0 {
			starts[i] = text.Len()
			text.WriteString("<mbp:pagebreak/>")
		}
		fmt.Fprintf(&text, "<h1>%s</h1><p>body %d</p>", ch, i)
	}

	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX: [][]string{
			{"Front Matter", "Chapter One", "Chapter Two", "Chapter Three"},
			{"Part A", "Part B"}, // region 1: stride addressing
		},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		{Values: map[int][]int{1: {0}, 3: {built.CNCX[0]}, 4: {0}}},
		{Values: map[int][]int{1: {starts[0]}, 3: {built.CNCX[1]}, 4: {0}}},
		// Sub-entries point inside chapter one (level 1, parent 1).
		{Values: map[int][]int{1: {starts[0] + 20}, 3: {built.CNCX[4]}, 4: {1}, 21: {1}}},
		{Values: map[int][]int{1: {starts[0] + 30}, 3: {built.CNCX[5]}, 4: {1}, 21: {1}}},
		{Values: map[int][]int{1: {starts[1]}, 3: {built.CNCX[2]}, 4: {0}}},
		{Values: map[int][]int{1: {starts[2]}, 3: {built.CNCX[3]}, 4: {0}}},
	}
	b, _ := openIndexBook(t, testutil.BookConfig{Text: text.String()}, withEntries(base, entries, 0))

	sections := b.Sections()
	boundaries := map[int]bool{}
	for _, s := range sections {
		boundaries[s.Start] = true
	}

	want := []TOCItem{
		{Label: "Front Matter", StartByte: 0, Length: -1, Fid: -1, Off: -1},
		{Label: "Chapter One", StartByte: starts[0], Length: -1, Fid: -1, Off: -1, Children: []TOCItem{
			{Label: "Part A", StartByte: 20, Length: -1, Fid: -1, Off: -1},
			{Label: "Part B", StartByte: 30, Length: -1, Fid: -1, Off: -1},
		}},
		{Label: "Chapter Two", StartByte: starts[1], Length: -1, Fid: -1, Off: -1},
		{Label: "Chapter Three", StartByte: starts[2], Length: -1, Fid: -1, Off: -1},
	}
	got, err := b.NCX()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("NCX returned %d roots, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !tocEqual(got[i], want[i]) {
			t.Errorf("root %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Every root's filepos lands on a real stage-4 section boundary.
	for _, it := range got {
		if !boundaries[it.StartByte] {
			t.Errorf("root %q filepos %d is not a section boundary (sections start at %v)",
				it.Label, it.StartByte, boundaries)
		}
	}

	// The cache returns the same tree without reparsing.
	again, err := b.NCX()
	if err != nil || len(again) != len(got) {
		t.Fatalf("second NCX() = (%d items, %v), want (%d, nil)", len(again), err, len(got))
	}
}

func TestNCXCP1252Labels(t *testing.T) {
	base := testutil.IndexConfig{
		Encoding: 1252,
		TagTable: ncxTagTable,
		CNCX:     [][]string{{"Vorwort", "Kapitel Übung"}},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		{Values: map[int][]int{1: {0}, 3: {built.CNCX[0]}, 4: {0}}},
		{Values: map[int][]int{1: {5}, 3: {built.CNCX[1]}, 4: {0}}},
	}
	b, _ := openIndexBook(t, testutil.BookConfig{Text: "hello"}, withEntries(base, entries, 0))
	got, err := b.NCX()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("NCX returned %d roots, want 2", len(got))
	}
	if got[0].Label != "Vorwort" || got[1].Label != "Kapitel Übung" {
		t.Fatalf("labels = %q, %q; want Vorwort, Kapitel Übung", got[0].Label, got[1].Label)
	}
}

func TestNCXChildRangeFallback(t *testing.T) {
	// No parent tags: entry 1's [first, last] child range (2..3)
	// supplies the hierarchy, KindleUnpack's recursINDX form.
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX:     [][]string{{"Root", "Parent", "Kid A", "Kid B"}},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		{Values: map[int][]int{3: {built.CNCX[0]}, 4: {0}}},
		{Values: map[int][]int{3: {built.CNCX[1]}, 4: {0}, 22: {2}, 23: {3}}},
		{Values: map[int][]int{3: {built.CNCX[2]}, 4: {1}}},
		{Values: map[int][]int{3: {built.CNCX[3]}, 4: {1}}},
	}
	b, _ := openIndexBook(t, testutil.BookConfig{Text: "hello"}, withEntries(base, entries, 0))
	got, err := b.NCX()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	want := []TOCItem{
		{Label: "Root", StartByte: -1, Length: -1, Fid: -1, Off: -1},
		{Label: "Parent", StartByte: -1, Length: -1, Fid: -1, Off: -1, Children: []TOCItem{
			{Label: "Kid A", StartByte: -1, Length: -1, Fid: -1, Off: -1},
			{Label: "Kid B", StartByte: -1, Length: -1, Fid: -1, Off: -1},
		}},
	}
	if len(got) != len(want) {
		t.Fatalf("NCX returned %d roots, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !tocEqual(got[i], want[i]) {
			t.Errorf("root %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestNCXFlatAndKF8Pos(t *testing.T) {
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX:     [][]string{{"One", "Two"}},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		{Values: map[int][]int{3: {built.CNCX[0]}, 4: {0}}},
		{Values: map[int][]int{3: {built.CNCX[1]}, 4: {0}, 6: {0x0A, 0x100}}},
	}
	b, _ := openIndexBook(t, testutil.BookConfig{Text: "hello"}, withEntries(base, entries, 0))
	got, err := b.NCX()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	if len(got) != 2 || got[0].Label != "One" || got[1].Label != "Two" {
		t.Fatalf("flat NCX = %+v", got)
	}
	if len(got[0].Children) != 0 || len(got[1].Children) != 0 {
		t.Fatalf("flat NCX has children: %+v", got)
	}
	// No hierarchy tags on a KF8-shaped index still surfaces the pos
	// pair for the reassembly stage to resolve.
	if got[1].Fid != 0x0A || got[1].Off != 0x100 || got[1].StartByte != -1 {
		t.Fatalf("pos pair = (%d, %d), StartByte = %d; want (10, 256), -1",
			got[1].Fid, got[1].Off, got[1].StartByte)
	}
}

func TestNCXTreeProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 0xc0ffee))
	type node struct {
		label string
		kids  []node
	}
	var grow func(depth int) []node
	grow = func(depth int) []node {
		var out []node
		for range rng.IntN(4) {
			n := node{label: fmt.Sprintf("node %d/%d", depth, rng.IntN(100))}
			if depth < 3 {
				n.kids = grow(depth + 1)
			}
			out = append(out, n)
		}
		return out
	}

	for iter := range 50 {
		tree := grow(0)

		// Flatten in document order, tracking each entry's level and
		// parent index.
		var labels []string
		type spec struct{ level, parent, pos int }
		var specs []spec
		var walk func(kids []node, level, parent int)
		walk = func(kids []node, level, parent int) {
			for _, k := range kids {
				myPos := len(specs)
				labels = append(labels, k.label)
				specs = append(specs, spec{level: level, parent: parent, pos: 1000 + myPos})
				walk(k.kids, level+1, myPos)
			}
		}
		walk(tree, 0, -1)

		base := testutil.IndexConfig{TagTable: ncxTagTable, CNCX: [][]string{labels}}
		built := testutil.BuildIndex(base)
		entries := make([]testutil.IndexEntry, len(specs))
		for i, s := range specs {
			values := map[int][]int{
				1: {s.pos},
				3: {built.CNCX[i]},
				4: {s.level},
			}
			if s.parent >= 0 {
				values[21] = []int{s.parent}
			}
			entries[i] = testutil.IndexEntry{Values: values}
		}
		cfg := withEntries(base, entries, 1+rng.IntN(3))
		b, _ := openIndexBook(t, testutil.BookConfig{Text: "hello"}, cfg)
		got, err := b.NCX()
		if err != nil {
			t.Fatalf("iter %d: NCX: %v", iter, err)
		}

		pos := 0
		var toItems func(kids []node) []TOCItem
		toItems = func(kids []node) []TOCItem {
			var out []TOCItem
			for _, k := range kids {
				item := TOCItem{Label: k.label, StartByte: 1000 + pos, Length: -1, Fid: -1, Off: -1}
				pos++
				item.Children = toItems(k.kids)
				out = append(out, item)
			}
			return out
		}
		want := toItems(tree)
		if len(got) != len(want) {
			t.Fatalf("iter %d: NCX returned %d roots, want %d: %+v", iter, len(got), len(want), got)
		}
		for i := range want {
			if !tocEqual(got[i], want[i]) {
				t.Errorf("iter %d: root %d = %+v, want %+v", iter, i, got[i], want[i])
			}
		}
	}
}

func TestNCXLegacyTOCFallback(t *testing.T) {
	text := `<toc>` +
		`<tocpoint filepos="0000000000" tocdepth="0">Beginning</tocpoint>` +
		`<tocpoint filepos="0000000042" tocdepth="1">Sub Topic</tocpoint>` +
		`<tocpoint filepos="0000000100" tocdepth="0">Later</tocpoint>` +
		`</toc><mbp:pagebreak/>body`
	data := testutil.BuildBook(testutil.BookConfig{Text: text}) // no Indx field
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := b.NCX()
	if err != nil {
		t.Fatalf("NCX: %v", err)
	}
	want := []TOCItem{
		{Label: "Beginning", StartByte: 0, Length: -1, Fid: -1, Off: -1, Children: []TOCItem{
			{Label: "Sub Topic", StartByte: 42, Length: -1, Fid: -1, Off: -1},
		}},
		{Label: "Later", StartByte: 100, Length: -1, Fid: -1, Off: -1},
	}
	if len(got) != len(want) {
		t.Fatalf("legacy TOC = %+v, want %d roots", got, len(want))
	}
	for i := range want {
		if !tocEqual(got[i], want[i]) {
			t.Errorf("root %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestNCXNone(t *testing.T) {
	data := testutil.BuildBook(testutil.BookConfig{Text: "no toc here"})
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := b.NCX()
	if err != nil || got != nil {
		t.Fatalf("NCX = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestGuideKF8(t *testing.T) {
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		CNCX:     [][]string{{"Table of Contents", "Start"}},
	}
	built := testutil.BuildIndex(base)
	entries := []testutil.IndexEntry{
		// Tag 6 pair: full kindle:pos URI.
		{Name: "toc", Values: map[int][]int{1: {built.CNCX[0]}, 6: {3, 256}}},
		// Tag 3 only: fid with off 0.
		{Name: "text", Values: map[int][]int{1: {built.CNCX[1]}, 3: {7}}},
	}
	guide := withEntries(base, entries, 0)
	guideBuilt := testutil.BuildIndex(guide)
	layout, _ := testutil.AuthorKF8([]testutil.KF8Author{{XHTML: "<html><body><p>x</p></body></html>"}}, nil)
	data := testutil.BuildKF8(testutil.KF8BookSpec{
		Layout:       layout,
		ExtraRecords: guideBuilt.Records,
	}).Data
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := b.Guide()
	if err != nil {
		t.Fatalf("Guide: %v", err)
	}
	want := []GuideEntry{
		{Label: "Table of Contents", Type: "toc", Href: "kindle:pos:fid:0003:off:0000000080", Filepos: -1},
		{Label: "Start", Type: "text", Href: "kindle:pos:fid:0007:off:0000000000", Filepos: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Guide = %+v, want %+v", got, want)
	}
}

func TestGuideMOBI6Fallback(t *testing.T) {
	text := `<html><guide><reference type="toc" title="Table of Contents" filepos="0000000123"/>` +
		`<reference type='text' title='Start' filepos='5'/></guide></html>`
	data := testutil.BuildBook(testutil.BookConfig{Text: text})
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := b.Guide()
	if err != nil {
		t.Fatalf("Guide: %v", err)
	}
	want := []GuideEntry{
		{Label: "Table of Contents", Type: "toc", Filepos: 123},
		{Label: "Start", Type: "text", Filepos: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Guide = %+v, want %+v", got, want)
	}
}

func TestGuideNone(t *testing.T) {
	data := testutil.BuildBook(testutil.BookConfig{Text: "no guide"})
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := b.Guide()
	if err != nil || got != nil {
		t.Fatalf("Guide = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestINDXCorrupt(t *testing.T) {
	base := testutil.IndexConfig{
		TagTable: ncxTagTable,
		Entries: []testutil.IndexEntry{
			{Values: map[int][]int{1: {1}, 4: {0}}},
			{Values: map[int][]int{1: {2}, 4: {0}}},
		},
		CNCX: [][]string{{"Label"}},
	}
	built := testutil.BuildIndex(base)
	entry := append([]byte(nil), built.Records[1]...)
	idxt := int(binary.BigEndian.Uint32(entry[20:24]))

	set32 := func(b []byte, off int, v uint32) { binary.BigEndian.PutUint32(b[off:], v) }
	set16 := func(b []byte, off int, v uint16) { binary.BigEndian.PutUint16(b[off:], v) }

	tests := []struct {
		name    string
		records func() [][]byte
	}{
		{"first record magic", func() [][]byte {
			r := cloneAll(built.Records)
			r[0][3] = 'Y'
			return r
		}},
		{"tagx magic", func() [][]byte {
			r := cloneAll(built.Records)
			r[0][195] = 'Y'
			return r
		}},
		{"tagx offset past record", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 4, uint32(len(r[0])+1))
			return r
		}},
		{"tagx length past record", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 196, uint32(len(r[0])))
			return r
		}},
		{"huge control byte count", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 192+8, 0xFFFF)
			return r
		}},
		{"unknown encoding", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 28, 65002)
			return r
		}},
		{"missing entry record", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 24, 99)
			return r
		}},
		{"entry record magic", func() [][]byte {
			r := cloneAll(built.Records)
			r[1][0] = 'X'
			return r
		}},
		{"entry idxt past record", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[1], 20, uint32(len(r[1])+1))
			return r
		}},
		{"entry offset outside record", func() [][]byte {
			r := cloneAll(built.Records)
			set16(r[1], idxt+4, uint16(len(r[1])+5))
			return r
		}},
		{"entry name past record", func() [][]byte {
			r := cloneAll(built.Records)
			set16(r[1], idxt+4, uint16(len(r[1])-1))
			return r
		}},
		{"missing cncx record", func() [][]byte {
			r := cloneAll(built.Records)
			set32(r[0], 52, 9)
			return r
		}},
		{"cncx phrase past record", func() [][]byte {
			r := cloneAll(built.Records)
			// A length varlen promising 10000 bytes in a tiny record.
			r[len(r)-1] = append([]byte{0xC4, 0x90}, 'x')
			return r
		}},
		{"cncx unterminated varlen", func() [][]byte {
			r := cloneAll(built.Records)
			r[len(r)-1] = []byte{0x01}
			return r
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := openIndexBookBytes(t, tt.records())
			_, err := b.NCX()
			if err == nil {
				t.Fatal("NCX succeeded on a corrupt index, want a typed error")
			}
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
				t.Fatalf("NCX error = %v, want ErrCorrupt or ErrRecordRange", err)
			}
		})
	}
}

func cloneAll(records [][]byte) [][]byte {
	out := make([][]byte, len(records))
	for i, r := range records {
		out[i] = append([]byte(nil), r...)
	}
	return out
}

func FuzzINDX(f *testing.F) {
	for _, cfg := range []testutil.IndexConfig{
		{
			TagTable: ncxTagTable,
			Entries:  []testutil.IndexEntry{{Values: map[int][]int{1: {1}, 4: {0}}}},
			CNCX:     [][]string{{"Label"}},
		},
		{
			TagTable: multiBitTagTable,
			Entries: []testutil.IndexEntry{
				{Name: "a", Values: map[int][]int{1: {1, 2, 3, 4, 5}}},
				{Name: "b", Values: map[int][]int{2: {1, 2, 3, 4}, 5: {9}}},
			},
			CNCX: [][]string{{"x"}, {"y"}},
		},
	} {
		built := testutil.BuildIndex(cfg)
		first, entry, cncx := built.Records[0], built.Records[1], built.Records[len(built.Records)-1]
		f.Add(first, entry, cncx)
	}
	f.Fuzz(func(t *testing.T, first, entry, cncx []byte) {
		data := testutil.BuildBook(testutil.BookConfig{
			Text:            "x",
			TrailingRecords: [][]byte{first, entry, cncx},
			Indx:            testutil.U32(2),
		})
		b, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		if _, err := b.NCX(); err != nil &&
			!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
			t.Fatalf("NCX error = %v, want a typed sentinel", err)
		}
		if _, err := b.Guide(); err != nil &&
			!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Guide error = %v, want a typed sentinel", err)
		}
	})
}
