// KF8/AZW3 fixture builder: authors XHTML sections, cuts them into
// skeletons and fragments, and composes the FDST, skeleton, fragment,
// NCX, and RESC records around the pieces — the byte-level inverse of
// the reassembly in kf8.go.
//
// The authoring model mirrors the format: a section's skeleton bytes
// land first in the raw flow with the section's fragment payloads
// immediately after them, each fragment's insert position is its
// content's offset in the final document (flow-0 relative), and its
// tag-6 payload offset counts from the end of the skeleton.

package testutil

import (
	"fmt"
	"strconv"
)

// SkelTagTable is the skeleton index's tag layout: tag 1 carries the
// fragment count and tag 6 the (offset, length) pair of the skeleton
// bytes in flow 0.
var SkelTagTable = []TagDesc{
	{Tag: 1, ValuesPerEntry: 1, Mask: 0x01},
	{Tag: 6, ValuesPerEntry: 2, Mask: 0x02},
}

// FragTagTable is the fragment index's tag layout: tag 2 the selector
// phrase (CNCX offset), tag 3 the file (section) number, tag 4 the
// sequence number, and tag 6 the (offset, length) pair of the fragment
// payload, counted from the end of its skeleton.
var FragTagTable = []TagDesc{
	{Tag: 2, ValuesPerEntry: 1, Mask: 0x01},
	{Tag: 3, ValuesPerEntry: 1, Mask: 0x02},
	{Tag: 4, ValuesPerEntry: 1, Mask: 0x04},
	{Tag: 6, ValuesPerEntry: 2, Mask: 0x08},
}

// NCXTagTable is the KF8 NCX index's tag layout: tag 3 the label (CNCX
// offset), tag 4 the heading level, and tag 6 the (fid, off) pos pair.
var NCXTagTable = []TagDesc{
	{Tag: 3, ValuesPerEntry: 1, Mask: 0x01},
	{Tag: 4, ValuesPerEntry: 1, Mask: 0x02},
	{Tag: 6, ValuesPerEntry: 2, Mask: 0x04},
}

// FDSTRecord renders ranges as an FDST record: magic, the entry count
// @8, then (start, end) pairs @12+8i.
func FDSTRecord(ranges [][2]int) []byte {
	out := make([]byte, 12+8*len(ranges))
	copy(out, "FDST")
	put32(out, 8, uint32(len(ranges)))
	for i, r := range ranges {
		put32(out, 12+8*i, uint32(r[0]))
		put32(out, 12+8*i+4, uint32(r[1]))
	}
	return out
}

// RESCRecord wraps an OPF spine fragment in the RESC record envelope:
// the magic, a sixteen-byte header naming the payload size (base-32
// digits, unread by the parser), then the XML bytes and a NUL.
func RESCRecord(spine string) []byte {
	payload := []byte(spine)
	out := make([]byte, 0, 16+len(payload)+1)
	out = append(out, 'R', 'E', 'S', 'C', 0, 0, 0, 0)
	out = append(out, "size="...)
	out = append(out, strconv.Itoa(len(payload)%32)...)
	out = append(out, '&')
	out = append(out, make([]byte, 16-len(out))...)
	out = append(out, payload...)
	return append(out, 0)
}

// KF8Author is one authored section: the final XHTML document and the
// cut ranges — disjoint, ascending, [start, end) within the document —
// removed as fragments. Cuts may fall inside tags; reassembly must
// restore the original bytes exactly.
type KF8Author struct {
	XHTML string
	Cuts  [][2]int
}

// SkelEntry is one skeleton-table row.
type SkelEntry struct {
	NumFrag int
	Offset  int // flow-0 relative
	Length  int
}

// FragEntry is one fragment-table row. Name defaults to InsertOffset
// in decimal; overriding it is the non-numeric-name corruption knob.
type FragEntry struct {
	Name         string
	InsertOffset int // flow-0 relative position in the final document
	Selector     string
	FileNum      int
	Seq          int
	Offset       int // from the end of the skeleton
	Length       int
}

// KF8Layout is a raw flow plus its skeleton and fragment tables, ready
// for BuildKF8. AuthorKF8 computes one from authored sections; tests
// can mutate entries and rebuild for corruption fixtures.
type KF8Layout struct {
	Flow0      string
	ExtraFlows []string
	Skel       []SkelEntry
	Frag       []FragEntry
}

// BuiltKF8Section records what AuthorKF8 computed for one section, so
// tests can derive kindle:pos targets and verify assembly.
type BuiltKF8Section struct {
	SkelOffset int
	SkelLength int
	NumFrag    int
	FragSeqs   []int // global sequence numbers, insertion order
	Inserts    []int // absolute (flow-0) insert offsets, insertion order
	SizeBytes  int   // the assembled document's length
}

// AuthorKF8 cuts each section at its ranges and lays out the raw flow:
// per section, the skeleton bytes then the fragment payloads, in order.
func AuthorKF8(sections []KF8Author, extraFlows []string) (KF8Layout, []BuiltKF8Section) {
	var raw []byte
	var skels []SkelEntry
	var frags []FragEntry
	var built []BuiltKF8Section
	seq := 0
	for si, sec := range sections {
		doc := []byte(sec.XHTML)
		pos := 0
		skelPos := len(raw)
		var skel []byte
		secBuilt := BuiltKF8Section{SkelOffset: skelPos}
		var payloads []byte
		for _, cut := range sec.Cuts {
			if cut[0] < pos || cut[1] < cut[0] || cut[1] > len(doc) {
				panic(fmt.Sprintf("testutil: section %d cuts %v are not disjoint/ascending/inside the document", si, cut))
			}
			skel = append(skel, doc[pos:cut[0]]...)
			f := FragEntry{
				InsertOffset: skelPos + cut[0],
				Selector:     fmt.Sprintf(`<p aid="f%03d">`, seq),
				FileNum:      si,
				Seq:          seq,
				Offset:       len(payloads),
				Length:       cut[1] - cut[0],
			}
			f.Name = strconv.Itoa(f.InsertOffset)
			frags = append(frags, f)
			secBuilt.FragSeqs = append(secBuilt.FragSeqs, seq)
			secBuilt.Inserts = append(secBuilt.Inserts, f.InsertOffset)
			payloads = append(payloads, doc[cut[0]:cut[1]]...)
			pos = cut[1]
			seq++
		}
		skel = append(skel, doc[pos:]...)
		if len(skel)+len(payloads) != len(doc) {
			panic("testutil: skeleton and payloads must cover the document")
		}
		raw = append(raw, skel...)
		raw = append(raw, payloads...)
		skels = append(skels, SkelEntry{NumFrag: len(sec.Cuts), Offset: skelPos, Length: len(skel)})
		secBuilt.SkelLength = len(skel)
		secBuilt.NumFrag = len(sec.Cuts)
		secBuilt.SizeBytes = len(doc)
		built = append(built, secBuilt)
	}
	return KF8Layout{Flow0: string(raw), ExtraFlows: extraFlows, Skel: skels, Frag: frags}, built
}

// KF8TOCEntry is one authored NCX entry: a label pointing at a byte
// offset inside a section's final document. The target must fall
// within one of the section's fragments (BuildKF8 panics otherwise).
type KF8TOCEntry struct {
	Label   string
	Section int
	Offset  int
}

// KF8BookSpec tweaks the KF8 files BuildKF8 renders. The zero value
// builds a valid uncompressed single-flow book around the layout.
type KF8BookSpec struct {
	Layout KF8Layout

	// Compression: 1 none (default), 2 PalmDOC — over the whole raw
	// flow (the text records).
	Compression uint16

	// RecordSize caps each text record's uncompressed size (default
	// 4096); small values force multi-record flows.
	RecordSize uint16

	// Resources are the image records; FirstImageIndex is computed to
	// point at the first one.
	Resources [][]byte

	// RESCSpine, when non-empty, lands as a RESC record after the
	// images.
	RESCSpine string

	// TOC, when non-empty, renders an NCX index the MOBI header's indx
	// field points at.
	TOC []KF8TOCEntry

	// ExtraRecords are appended after the fragment index (a KF8 guide
	// index, say). GuideIndex, when nil with extras present, is
	// computed to point at the first extra record.
	ExtraRecords [][]byte
	GuideIndex   *uint32

	// Corruption knobs: SkipFDST writes the fdst sentinel, SkelMissing
	// and FragMissing write their sentinels.
	SkipFDST    bool
	SkelMissing bool
	FragMissing bool

	// FirstImageOverride replaces the KF8 record-0 firstImageIndex
	// field (combo fixtures write the shared images' absolute index
	// here, matching real files whose KF8 half points at shared
	// resources).
	FirstImageOverride *uint32

	Title string
	EXTH  []EXTHRecord
}

// BuiltKF8 is a rendered KF8 book: the file bytes plus the facts tests
// need to author targets.
type BuiltKF8 struct {
	Data       []byte
	Rec0       []byte
	Records    [][]byte
	FlowRanges [][2]int
	FDSTIndex  int // -1 when skipped
	NCXIndex   int // -1 when no TOC
	FirstImage int // -1 when no resources
	NumImages  int
}

// BuildKF8 renders spec as complete file bytes, ready for Open.
func BuildKF8(spec KF8BookSpec) BuiltKF8 {
	rec0, records, facts := BuildKF8Parts(spec)
	data := Build(append([][]byte{rec0}, records...)...)
	return BuiltKF8{
		Data: data, Rec0: rec0, Records: records,
		FlowRanges: facts.FlowRanges, FDSTIndex: facts.FDSTIndex,
		NCXIndex: facts.NCXIndex, FirstImage: facts.FirstImage, NumImages: facts.NumImages,
	}
}

// KF8Facts carries the record indexes a test needs after
// BuildKF8Parts.
type KF8Facts struct {
	FlowRanges [][2]int
	FDSTIndex  int
	NCXIndex   int
	FirstImage int
	NumImages  int
}

// BuildKF8Parts renders spec into record 0 and the following records,
// for tests that need to mutate the pieces before wrapping the
// container. The record order is: text records, images, RESC, FDST,
// NCX index, skeleton index, fragment index, extra records.
func BuildKF8Parts(spec KF8BookSpec) ([]byte, [][]byte, KF8Facts) {
	layout := spec.Layout
	raw := []byte(layout.Flow0)
	for _, f := range layout.ExtraFlows {
		raw = append(raw, f...)
	}

	recordSize := int(spec.RecordSize)
	if recordSize == 0 {
		recordSize = 4096
	}
	var records [][]byte
	for start := 0; ; start += recordSize {
		end := min(start+recordSize, len(raw))
		rec := append([]byte(nil), raw[start:end]...)
		if spec.Compression == 2 {
			rec = CompressPalmDOC(rec)
		}
		records = append(records, rec)
		if end >= len(raw) {
			break
		}
	}
	numText := len(records)

	// Flow ranges: flow 0 covers the text flow, each extra flow the
	// bytes appended after it.
	flowEnd := len(layout.Flow0)
	ranges := [][2]int{{0, flowEnd}}
	for _, f := range layout.ExtraFlows {
		ranges = append(ranges, [2]int{flowEnd, flowEnd + len(f)})
		flowEnd += len(f)
	}

	resources := append([][]byte{}, spec.Resources...)
	if spec.RESCSpine != "" {
		resources = append(resources, RESCRecord(spec.RESCSpine))
	}
	firstImage := -1
	if len(spec.Resources) > 0 {
		firstImage = 1 + numText
	}
	records = append(records, resources...)

	facts := KF8Facts{FlowRanges: ranges, FDSTIndex: -1, NCXIndex: -1, FirstImage: firstImage, NumImages: len(spec.Resources)}
	base := 1 + numText + len(resources)

	var fdst, skel, frag, indx *uint32
	if !spec.SkipFDST {
		facts.FDSTIndex = base
		fdst = U32(uint32(base))
		records = append(records, FDSTRecord(ranges))
		base++
	}
	if len(spec.TOC) > 0 {
		facts.NCXIndex = base
		indx = U32(uint32(base))
		ncx := buildNCXIndex(spec.TOC, layout)
		records = append(records, ncx.Records...)
		base += len(ncx.Records)
	}
	if !spec.SkelMissing {
		skelIndex := BuildIndex(IndexConfig{TagTable: SkelTagTable, Entries: skelIndexEntries(layout.Skel)})
		skel = U32(uint32(base))
		records = append(records, skelIndex.Records...)
		base += len(skelIndex.Records)
	}
	if !spec.FragMissing {
		fragIndex := buildFragIndex(layout.Frag)
		frag = U32(uint32(base))
		records = append(records, fragIndex.Records...)
		base += len(fragIndex.Records)
	}
	if spec.GuideIndex == nil && len(spec.ExtraRecords) > 0 {
		spec.GuideIndex = U32(uint32(base))
	}
	guide := spec.GuideIndex
	records = append(records, spec.ExtraRecords...)

	rec0 := BuildRecord0(Record0Config{
		Compression:     spec.Compression,
		TextLength:      uint32(len(raw)),
		NumTextRecords:  uint16(numText),
		RecordSize:      spec.RecordSize,
		Version:         8,
		FirstImageIndex: firstImageField(spec, firstImage),
		FDST:            fdst,
		NumFDST:         uint32(len(ranges)),
		Skel:            skel,
		Frag:            frag,
		Indx:            indx,
		Guide:           guide,
		Title:           spec.Title,
		EXTH:            spec.EXTH,
	})
	return rec0, records, facts
}

func firstImageField(spec KF8BookSpec, computed int) *uint32 {
	if spec.FirstImageOverride != nil {
		return spec.FirstImageOverride
	}
	if computed < 0 {
		return nil
	}
	return U32(uint32(computed))
}

func skelIndexEntries(skels []SkelEntry) []IndexEntry {
	entries := make([]IndexEntry, len(skels))
	for i, s := range skels {
		entries[i] = IndexEntry{
			Name:   strconv.Itoa(i),
			Values: map[int][]int{1: {s.NumFrag}, 6: {s.Offset, s.Length}},
		}
	}
	return entries
}

// buildFragIndex renders the fragment index: entries reference their
// selector's CNCX offset, learned from a first (entry-less) pass over
// the same phrase pool.
func buildFragIndex(frags []FragEntry) BuiltIndex {
	selectors := fragSelectors(frags)
	base := IndexConfig{TagTable: FragTagTable}
	if selectors != nil {
		base.CNCX = [][]string{selectors}
	}
	first := BuildIndex(base)
	pos := make(map[string]int, len(selectors))
	for i, s := range selectors {
		pos[s] = first.CNCX[i]
	}
	entries := make([]IndexEntry, len(frags))
	for i, f := range frags {
		selectorOff := 0
		if off, ok := pos[f.Selector]; ok {
			selectorOff = off
		}
		entries[i] = IndexEntry{
			Name:   f.Name,
			Values: map[int][]int{2: {selectorOff}, 3: {f.FileNum}, 4: {f.Seq}, 6: {f.Offset, f.Length}},
		}
	}
	base.Entries = entries
	return BuildIndex(base)
}

// fragSelectors collects the fragments' selector phrases in first-seen
// order for the CNCX pool.
func fragSelectors(frags []FragEntry) []string {
	var selectors []string
	seen := make(map[string]bool)
	for _, f := range frags {
		if !seen[f.Selector] {
			seen[f.Selector] = true
			selectors = append(selectors, f.Selector)
		}
	}
	return selectors
}

// buildNCXIndex renders the KF8 NCX index: labels from the CNCX pool
// (two-pass), heading level 0, and the (fid, off) pair resolved from
// each entry's (section, offset) target.
func buildNCXIndex(toc []KF8TOCEntry, layout KF8Layout) BuiltIndex {
	labels := make([]string, len(toc))
	for i, e := range toc {
		labels[i] = e.Label
	}
	base := IndexConfig{TagTable: NCXTagTable, CNCX: [][]string{labels}}
	first := BuildIndex(base)
	entries := make([]IndexEntry, len(toc))
	for i, e := range toc {
		fid, off, ok := posFromTarget(layout, e.Section, e.Offset)
		if !ok {
			panic(fmt.Sprintf("testutil: TOC target (section %d, offset %d) does not fall inside a fragment", e.Section, e.Offset))
		}
		entries[i] = IndexEntry{
			Name:   strconv.Itoa(i),
			Values: map[int][]int{3: {first.CNCX[i]}, 4: {0}, 6: {fid, off}},
		}
	}
	base.Entries = entries
	return BuildIndex(base)
}

// posFromTarget finds the fragment covering (section, offset) in the
// assembled document and returns its (fid, off) pair.
func posFromTarget(layout KF8Layout, section, offset int) (int, int, bool) {
	skel := layout.Skel[section]
	row := 0
	for i := range section {
		row += layout.Skel[i].NumFrag
	}
	for k := range skel.NumFrag {
		f := layout.Frag[row+k]
		start := f.InsertOffset - skel.Offset
		if offset >= start && offset < start+f.Length {
			return f.Seq, offset - start, true
		}
	}
	return 0, 0, false
}

// BuildComboFile lays out a MOBI6 half, the shared images, the
// BOUNDARY marker record, and the KF8 half in one file: m6's records
// (with EXTH 121 naming the KF8 boundary), then a BOUNDARY record,
// then the KF8 half's records — the shape mobi_split carves apart.
// The images are shared: they are written once, among the MOBI6
// records, and the KF8 record-0 firstImageIndex field points at them
// absolutely (combo files point at shared resources), which is what
// the reader's combo override expects.
func BuildComboFile(m6 BookConfig, k8 KF8BookSpec) (data []byte, boundary int) {
	_, m6records := BuildBookParts(m6)
	boundary = 2 + len(m6records)
	m6.EXTH = append(m6.EXTH, EXTHUint(121, uint32(boundary)))
	m6rec0 := rebuildRecord0(m6)

	k8.FirstImageOverride = U32(uint32(1 + NumTextRecords(m6)))
	k8rec0, k8records, _ := BuildKF8Parts(k8)

	all := make([][]byte, 0, 3+len(m6records)+len(k8records))
	all = append(all, m6rec0)
	all = append(all, m6records...)
	all = append(all, []byte("BOUNDARY"))
	all = append(all, k8rec0)
	all = append(all, k8records...)
	return Build(all...), boundary
}

// rebuildRecord0 re-renders m6's record 0 after config edits (the
// combo builder adds EXTH 121 once the boundary is known).
func rebuildRecord0(cfg BookConfig) []byte {
	rec0, _ := BuildBookParts(cfg)
	return rec0
}
