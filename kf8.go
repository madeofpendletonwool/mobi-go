// KF8 (AZW3, MOBI format version 8) reassembly: an EPUB flattened
// into the PDB container. XHTML and CSS live in one contiguous
// decompressed raw flow; FDST cuts that flow into pieces and the
// skeleton and fragment INDX indexes describe how to splice flow 0
// back into XHTML documents.
//
// Ported with attribution from KindleUnpack's K8Processor
// (lib/mobi_k8proc.py), K8RESCProcessor (lib/mobi_k8resc.py), and
// mobi_split (lib/mobi_split.py) (GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// KF8 class and FDST handling (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js) as the structural
// cross-check. Where the two differ the choice is recorded inline:
// skeleton and fragment offsets count from the start of FDST flow 0
// and fragment payloads are walked contiguously after their skeleton
// (KindleUnpack's reading; foliate-js's per-entry offsets agree on
// every well-formed file, since payload offset k is the cumulative
// length of the fragments before it).
//
// Memory: the raw flow is a book's whole text — a few MB — and is
// decompressed once, eagerly, into one buffer that every section
// assembly and flow read slices (loadRaw below). Replacing it with
// streaming later means swapping that one function, not the API.

package mobi

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// ErrNotKindleURI reports a kindle: URI that does not parse: wrong
// scheme shape, or a number the format cannot express.
var ErrNotKindleURI = errors.New("mobi: not a kindle URI")

// KindleURIKind discriminates the three kindle: URI forms.
type KindleURIKind uint8

const (
	// KindleURIFlow is kindle:flow:NNN — raw-flow piece NNN per FDST.
	KindleURIFlow KindleURIKind = iota
	// KindleURIEmbed is kindle:embed:NNN — resource NNN, 1-based
	// (Resource(n-1), the same arithmetic as MOBI6 recindex).
	KindleURIEmbed
	// KindleURIPos is kindle:pos:fid:XX:off:YY — a position inside
	// fragment XX at byte offset YY into its payload.
	KindleURIPos
)

// FDST record layout: magic, a reserved word, the entry count @8,
// then (start uint32, end uint32) pairs @12+8i — byte ranges into the
// raw flow (KindleUnpack's getFDSTInfo; foliate-js's FDST_HEADER).
const (
	fdstMinRecord = 12
	fdstWordCount = 8
)

// Skel and frag index tag numbers, per KindleUnpack's processSkelIdx /
// processFragIdx tag_fieldname_map.
const (
	skelTagNumFrag = 1 // fragment count
	skelTagPair    = 6 // (offset, length) of the skeleton bytes, flow-0 relative
	fragTagSel     = 2 // selector phrase, CNCX offset
	fragTagFileNum = 3 // file (section) number
	fragTagSeq     = 4 // sequence number
	fragTagPair    = 6 // (offset, length) of the payload, from the skeleton's end
)

// fdstRange is one FDST entry: [start, end) into the raw flow.
type fdstRange struct {
	start, end int
}

// kf8Skel is one skeleton-table row.
type kf8Skel struct {
	numFrag int
	offset  int // flow-0 relative
	length  int
}

// kf8Frag is one fragment-table row. The entry's name is its insert
// position: an absolute flow-0-relative byte offset naming where the
// fragment's bytes belong in the final document.
type kf8Frag struct {
	insertOffset int
	selector     string
	fileNum      int
	seq          int
	offset       int // from the end of the skeleton
	length       int
}

// KF8Section is one reassembled XHTML document of a KF8 book.
// SizeBytes is the assembled document's raw-byte length; XHTML returns
// those bytes decoded per the book's encoding. Linear reports whether
// the section carries fragments: a skeleton with zero fragments holds
// only structural markup (KindleUnpack and foliate-js both mark such
// sections linear="no"). PageSpread carries the section's RESC
// page-spread property ("left", "right", "center") when it has one.
type KF8Section struct {
	SizeBytes  int
	Linear     bool
	PageSpread string
	xhtml      string
}

// XHTML returns the section's assembled document, decoded per the
// book's encoding. kindle:flow/embed URIs are left exactly as stored —
// replacing them changes byte lengths, and every offset this library
// reports is a raw byte offset; rewriting belongs to callers.
func (s KF8Section) XHTML() string { return s.xhtml }

// KindleURI is a parsed kindle: reference. Which fields carry meaning
// depends on Kind: Flow for KindleURIFlow, Embed for KindleURIEmbed
// (1-based), Fid/Off for a bare ParseKindleURI of a pos URI — and,
// after ResolveKindleURI, Section and Offset for KindleURIPos. MIME
// carries the optional "?mime=" parameter verbatim when present.
type KindleURI struct {
	Kind    KindleURIKind
	Flow    int
	Embed   int
	Fid     int
	Off     int
	Section int
	Offset  int
	MIME    string
}

// The URI forms, ported from foliate-js's kindleResourceRegex and
// kindlePosRegex (KindleUnpack reads the same shapes with the same
// base-32 digits, 0-9 then A-V).
var (
	kindleResourceRE = regexp.MustCompile(`^kindle:(flow|embed):(\w+)(?:\?mime=([\w/+.-]+))?$`)
	kindlePosRE      = regexp.MustCompile(`^kindle:pos:fid:(\w+):off:(\w+)$`)
)

// ParseKindleURI parses a kindle: URI into its coordinates without
// resolving them against a book. Numbers are base-32 (0-9 then A-V,
// case-insensitive). A malformed URI fails with ErrNotKindleURI.
func ParseKindleURI(uri string) (KindleURI, error) {
	if m := kindleResourceRE.FindStringSubmatch(uri); m != nil {
		n, err := strconv.ParseInt(m[2], 32, 64)
		if err != nil {
			return KindleURI{}, fmt.Errorf("%w: %q number %q is not base-32", ErrNotKindleURI, uri, m[2])
		}
		u := KindleURI{MIME: m[3]}
		if m[1] == "flow" {
			u.Kind = KindleURIFlow
			u.Flow = int(n)
		} else {
			u.Kind = KindleURIEmbed
			u.Embed = int(n)
		}
		return u, nil
	}
	if m := kindlePosRE.FindStringSubmatch(uri); m != nil {
		fid, err1 := strconv.ParseInt(m[1], 32, 64)
		off, err2 := strconv.ParseInt(m[2], 32, 64)
		if err1 != nil || err2 != nil {
			return KindleURI{}, fmt.Errorf("%w: %q numbers are not base-32", ErrNotKindleURI, uri)
		}
		return KindleURI{Kind: KindleURIPos, Fid: int(fid), Off: int(off)}, nil
	}
	return KindleURI{}, fmt.Errorf("%w: %q", ErrNotKindleURI, uri)
}

// ResolveKindleURI parses uri and resolves it against the book:
// flow indexes are bounds-checked against FDST, embed numbers against
// the resource range, and pos pairs map to (Section, Offset) — the
// section's index in KF8Sections and a byte offset into its assembled
// XHTML. Out-of-range targets fail with ErrRecordRange (flow, embed)
// or ErrCorrupt (pos); KF8 books only.
func (b *Book) ResolveKindleURI(uri string) (KindleURI, error) {
	if !b.kf8Loaded {
		return KindleURI{}, fmt.Errorf("%w: kindle URI resolution on a MOBI6 book", ErrCorrupt)
	}
	u, err := ParseKindleURI(uri)
	if err != nil {
		return KindleURI{}, err
	}
	switch u.Kind {
	case KindleURIFlow:
		if u.Flow < 0 || u.Flow >= len(b.fdst) {
			return KindleURI{}, fmt.Errorf("%w: flow %d of %d",
				ErrRecordRange, u.Flow, len(b.fdst))
		}
	case KindleURIEmbed:
		if u.Embed < 1 || u.Embed > b.NumResources() {
			return KindleURI{}, fmt.Errorf("%w: embed %d, book has %d resources",
				ErrRecordRange, u.Embed, b.NumResources())
		}
	case KindleURIPos:
		section, offset, err := b.resolvePos(u.Fid, u.Off)
		if err != nil {
			return KindleURI{}, err
		}
		u.Section, u.Offset = section, offset
	}
	return u, nil
}

// Flow returns raw-flow piece i per FDST: flow 0 is the text flow the
// sections assemble from; later flows hold the CSS, SVG, and other
// pieces kindle:flow URIs reference. The returned slice aliases the
// book's raw flow; callers must not modify it. KF8 books only.
func (b *Book) Flow(i int) ([]byte, error) {
	if !b.kf8Loaded {
		return nil, fmt.Errorf("%w: flow access on a MOBI6 book", ErrCorrupt)
	}
	if i < 0 || i >= len(b.fdst) {
		return nil, fmt.Errorf("%w: flow %d of %d", ErrRecordRange, i, len(b.fdst))
	}
	r := b.fdst[i]
	return b.rawText[r.start:r.end], nil
}

// KF8Sections returns the book's reassembled XHTML documents in spine
// order. KF8 books only; MOBI6 books return nil.
func (b *Book) KF8Sections() []KF8Section {
	if !b.kf8Loaded {
		return nil
	}
	return b.kf8Sections
}

// HasMOBI6Half reports whether the book is a combo file — a MOBI6 half
// followed by the KF8 half this book opened as. MOBI6Half returns that
// other view.
func (b *Book) HasMOBI6Half() bool { return b.m6 != nil }

// MOBI6Half returns the MOBI6 view of a combo file: the original
// record-0 headers with the MOBI6 half's text loaded, sharing this
// book's container. A book that is not a combo fails with ErrCorrupt.
func (b *Book) MOBI6Half() (*Book, error) {
	if b.m6 == nil {
		return nil, fmt.Errorf("%w: MOBI6Half on a book that is not a combo file", ErrCorrupt)
	}
	half := &Book{
		pdb:      b.pdb,
		palmdoc:  b.m6.palmdoc,
		mobi:     b.m6.mobi,
		kf8:      b.m6.kf8,
		exth:     b.m6.exth,
		title:    b.m6.title,
		boundary: -1,
		m6End:    b.boundary - 1, // MOBI6 records stop before the BOUNDARY record
	}
	if err := half.loadAllText(); err != nil {
		return nil, err
	}
	return half, nil
}

// kf8Boundary reports the combo-file KF8 half's record index from
// EXTH 121, when present and not the 0xFFFFFFFF sentinel.
func (b *Book) kf8Boundary() (int, bool) {
	if b.exth == nil {
		return 0, false
	}
	v, ok := b.exth.uint(exthBoundary)
	if !ok || v == indexAbsent {
		return 0, false
	}
	return int(v), true
}

// openKF8Half re-parses the header chain at the combo boundary and
// shifts every half-relative index by it — the port of foliate-js's
// #start remapping and KindleUnpack's mobi_split combo handling. The
// MOBI6 half's parse is retained for MOBI6Half, and its absolute
// firstImageIndex stays active: in a combo file the images are shared,
// physically stored in the MOBI6 half, and the KF8 header's own
// resourceStart points at the KF8-relative slot they would occupy in a
// split file (the 0x0800 "shared resources" flag KindleUnpack resets
// when splitting).
func (b *Book) openKF8Half(boundary int) error {
	m6 := &mobiHalf{
		palmdoc: b.palmdoc,
		mobi:    b.mobi,
		kf8:     b.kf8,
		exth:    b.exth,
		title:   b.title,
	}
	rec, err := b.pdb.Record(boundary)
	if err != nil {
		return fmt.Errorf("combo KF8 half at record %d: %w", boundary, err)
	}
	half := &Book{pdb: b.pdb}
	if err := half.parseRecord0(rec); err != nil {
		return fmt.Errorf("combo KF8 half at record %d: %w", boundary, err)
	}
	if half.mobi.Version < 8 {
		return fmt.Errorf("%w: combo boundary record %d is MOBI version %d, want >= 8",
			ErrCorrupt, boundary, half.mobi.Version)
	}
	b.palmdoc = half.palmdoc
	b.mobi = half.mobi
	b.kf8 = half.kf8
	b.exth = half.exth
	b.title = half.title
	if m6.mobi.FirstImageIndex >= 0 {
		b.mobi.FirstImageIndex = m6.mobi.FirstImageIndex
	}
	b.m6 = m6
	b.start = boundary
	b.boundary = boundary
	return nil
}

// loadKF8 decompresses the raw flow and reassembles every section:
// FDST, the skeleton and fragment indexes, section assembly, RESC page
// spreads, and the fragment lookup tables pos URIs resolve through.
// Called eagerly at Open so a damaged KF8 book is refused whole.
func (b *Book) loadKF8() error {
	if b.kf8 == nil {
		return fmt.Errorf("%w: version %d book without KF8 headers", ErrCorrupt, b.mobi.Version)
	}
	// The raw flow is the concatenation of every text record through
	// the stage-4/5 loadText seam (trailing-entry stripping and
	// decompression included).
	if err := b.loadAllText(); err != nil {
		return err
	}
	fdst, err := b.parseFDST()
	if err != nil {
		return err
	}
	skels, err := b.loadSkelTable()
	if err != nil {
		return err
	}
	frags, err := b.loadFragTable()
	if err != nil {
		return err
	}
	if err := b.assembleKF8(fdst, skels, frags); err != nil {
		return err
	}
	b.pageSpreads = b.loadPageSpreads()
	for i, spread := range b.pageSpreads {
		if i < len(b.kf8Sections) && spread != "" {
			b.kf8Sections[i].PageSpread = spread
		}
	}
	b.kf8Loaded = true
	return nil
}

// loadRaw returns raw[start:end] — the single seam every flow read and
// section assembly goes through, so a streaming implementation can
// replace the eager whole-flow buffer later without an API change.
func (b *Book) loadRaw(start, end int) ([]byte, error) {
	if start < 0 || end < start || end > len(b.rawText) {
		return nil, fmt.Errorf("%w: raw flow range [%d, %d) of %d bytes",
			ErrCorrupt, start, end, len(b.rawText))
	}
	return b.rawText[start:end], nil
}

// parseFDST reads the FDST record at the header's fdst index: magic,
// entry count @8, then (start, end) pairs into the raw flow. A missing
// FDST (the 0xFFFFFFFF sentinel) degenerates to one flow covering the
// whole raw flow, KindleUnpack's [0, rawSize] default. The header's
// numFdst count is advisory and deliberately unchecked, as in both
// port sources.
func (b *Book) parseFDST() ([]fdstRange, error) {
	if b.kf8.FDST < 0 {
		return []fdstRange{{start: 0, end: len(b.rawText)}}, nil
	}
	rec, err := b.loadRecord(int(b.kf8.FDST))
	if err != nil {
		return nil, err
	}
	if len(rec) < fdstMinRecord || string(rec[:4]) != "FDST" {
		return nil, fmt.Errorf("%w: FDST record %d magic is %q",
			ErrCorrupt, b.kf8.FDST, preview(rec))
	}
	n := int(be32(rec, fdstWordCount))
	if n < 0 || n > (len(rec)-fdstMinRecord)/8 {
		return nil, fmt.Errorf("%w: FDST holds %d entries, overflows its %d-byte record",
			ErrCorrupt, n, len(rec))
	}
	ranges := make([]fdstRange, n)
	for i := range n {
		start := int(be32(rec, fdstMinRecord+8*i))
		end := int(be32(rec, fdstMinRecord+8*i+4))
		if start < 0 || end < start || end > len(b.rawText) {
			return nil, fmt.Errorf("%w: FDST entry %d is [%d, %d), outside the %d-byte raw flow",
				ErrCorrupt, i, start, end, len(b.rawText))
		}
		ranges[i] = fdstRange{start: start, end: end}
	}
	if n == 0 {
		ranges = append(ranges, fdstRange{start: 0, end: len(b.rawText)})
	}
	return ranges, nil
}

// loadSkelTable reads the skeleton index: one entry per section, tag 1
// its fragment count and tag 6 the (offset, length) of its skeleton
// bytes in flow 0.
func (b *Book) loadSkelTable() ([]kf8Skel, error) {
	if b.kf8.Skel < 0 {
		return nil, fmt.Errorf("%w: KF8 book without a skeleton index", ErrCorrupt)
	}
	entries, _, err := b.readIndex(int(b.kf8.Skel))
	if err != nil {
		return nil, fmt.Errorf("skeleton index: %w", err)
	}
	skels := make([]kf8Skel, 0, len(entries))
	for i, e := range entries {
		numFrag, ok1 := tagValue(e, skelTagNumFrag)
		pair, ok6 := tagPair(e, skelTagPair)
		if !ok1 || !ok6 {
			return nil, fmt.Errorf("%w: skeleton entry %d misses its fragment count or offset pair",
				ErrCorrupt, i)
		}
		if numFrag < 0 || pair[0] < 0 || pair[1] < 0 {
			return nil, fmt.Errorf("%w: skeleton entry %d has negative values (count %d, offset %d, length %d)",
				ErrCorrupt, i, numFrag, pair[0], pair[1])
		}
		skels = append(skels, kf8Skel{numFrag: numFrag, offset: pair[0], length: pair[1]})
	}
	return skels, nil
}

// loadFragTable reads the fragment index: the entry name is the insert
// position (a decimal flow-0-relative byte offset), tag 2 the selector
// phrase, tag 3 the file number, tag 4 the sequence number, and tag 6
// the (offset, length) of the payload counted from the skeleton's end.
func (b *Book) loadFragTable() ([]kf8Frag, error) {
	if b.kf8.Frag < 0 {
		return nil, fmt.Errorf("%w: KF8 book without a fragment index", ErrCorrupt)
	}
	entries, pool, err := b.readIndex(int(b.kf8.Frag))
	if err != nil {
		return nil, fmt.Errorf("fragment index: %w", err)
	}
	frags := make([]kf8Frag, 0, len(entries))
	for i, e := range entries {
		insert, err := strconv.Atoi(e.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: fragment entry %d name %q is not an insert position",
				ErrCorrupt, i, e.Name)
		}
		f := kf8Frag{insertOffset: insert, fileNum: -1, seq: -1, offset: -1, length: -1}
		if vs := e.Values[fragTagSel]; len(vs) > 0 {
			if sel, ok := pool.lookup(vs[0]); ok {
				f.selector = sel
			}
		}
		if v, ok := tagValue(e, fragTagFileNum); ok {
			f.fileNum = v
		}
		if v, ok := tagValue(e, fragTagSeq); ok {
			f.seq = v
		}
		if pair, ok := tagPair(e, fragTagPair); ok {
			f.offset, f.length = pair[0], pair[1]
		}
		frags = append(frags, f)
	}
	return frags, nil
}

// tagValue returns an entry's first value for tag.
func tagValue(e indexEntry, tag int) (int, bool) {
	vs := e.Values[tag]
	if len(vs) == 0 {
		return 0, false
	}
	return vs[0], true
}

// tagPair returns an entry's tag pair, when present with both values.
func tagPair(e indexEntry, tag int) ([2]int, bool) {
	vs := e.Values[tag]
	if len(vs) < 2 {
		return [2]int{}, false
	}
	return [2]int{vs[0], vs[1]}, true
}

// assembleKF8 rebuilds one XHTML document per skeleton: the skeleton
// bytes from the raw flow, with each of the skeleton's fragments —
// walked in table order, their payloads contiguous after the skeleton
// — spliced in at (insertOffset - skeleton offset) in the growing
// document. Insert positions name where content belongs in the final
// document; because fragments splice in order, each insert offset
// naturally accounts for the fragments before it, and byte-exact
// reassembly holds for cuts anywhere, including inside tags.
func (b *Book) assembleKF8(fdst []fdstRange, skels []kf8Skel, frags []kf8Frag) error {
	base := fdst[0].start // skeleton/fragment offsets count from flow 0's start
	raw := b.rawText
	fragPtr := 0
	sections := make([]KF8Section, 0, len(skels))
	sectionOfFrag := make([]int, 0, len(frags))
	fragBySeq := make(map[int]int, len(frags))
	for row, f := range frags {
		if f.seq >= 0 {
			if _, dup := fragBySeq[f.seq]; !dup {
				fragBySeq[f.seq] = row
			}
		}
	}
	for si, skel := range skels {
		skelStart := base + skel.offset
		if skelStart+skel.length > len(raw) {
			return fmt.Errorf("%w: skeleton %d spans [%d, %d), past the %d-byte raw flow",
				ErrCorrupt, si, skelStart, skelStart+skel.length, len(raw))
		}
		if fragPtr+skel.numFrag > len(frags) {
			return fmt.Errorf("%w: skeleton %d claims %d fragments, %d remain in the table",
				ErrCorrupt, si, skel.numFrag, len(frags)-fragPtr)
		}
		skeleton, err := b.loadRaw(skelStart, skelStart+skel.length)
		if err != nil {
			return fmt.Errorf("skeleton %d: %w", si, err)
		}
		assembled := make([]byte, 0, skel.length)
		assembled = append(assembled, skeleton...)
		payload := skelStart + skel.length
		for k := range skel.numFrag {
			f := frags[fragPtr+k]
			if f.length < 0 || payload+f.length > len(raw) {
				return fmt.Errorf("%w: fragment %d payload at %d spans %d bytes, past the %d-byte raw flow",
					ErrCorrupt, fragPtr+k, payload, f.length, len(raw))
			}
			fragRaw, err := b.loadRaw(payload, payload+f.length)
			if err != nil {
				return fmt.Errorf("fragment %d: %w", fragPtr+k, err)
			}
			insert := f.insertOffset - base - skel.offset
			if insert < 0 || insert > len(assembled) {
				return fmt.Errorf("%w: fragment %d inserts at %d, outside the %d-byte document",
					ErrCorrupt, fragPtr+k, f.insertOffset, len(assembled))
			}
			assembled = spliceBytes(assembled, insert, fragRaw)
			payload += f.length
			sectionOfFrag = append(sectionOfFrag, si)
		}
		fragPtr += skel.numFrag
		sections = append(sections, KF8Section{
			SizeBytes: len(assembled),
			Linear:    skel.numFrag > 0,
			xhtml:     decodeString(b.mobi.Encoding, assembled),
		})
	}
	b.fdst = fdst
	b.skels = skels
	b.frags = frags
	b.kf8Sections = sections
	b.sectionOfFrag = sectionOfFrag
	b.fragBySeq = fragBySeq
	b.fragBase = base
	return nil
}

// spliceBytes returns s with ins inserted at byte position at.
func spliceBytes(s []byte, at int, ins []byte) []byte {
	out := make([]byte, 0, len(s)+len(ins))
	out = append(out, s[:at]...)
	out = append(out, ins...)
	out = append(out, s[at:]...)
	return out
}

// resolvePos maps a kindle:pos (fid, off) pair to its section and the
// byte offset into that section's assembled XHTML. fid names a
// fragment — by its sequence number when the table carries one
// (foliate-js's matching), else by row (KindleUnpack's fragtbl
// indexing); off counts bytes into that fragment's payload from its
// insert position (KindleUnpack's pos = insertpos + off).
func (b *Book) resolvePos(fid, off int) (int, int, error) {
	row := -1
	if r, ok := b.fragBySeq[fid]; ok {
		row = r
	} else if fid >= 0 && fid < len(b.frags) {
		row = fid
	}
	if row < 0 {
		return 0, 0, fmt.Errorf("%w: kindle:pos references fragment %d, table has %d",
			ErrCorrupt, fid, len(b.frags))
	}
	f := b.frags[row]
	si := b.sectionOfFrag[row]
	if off < 0 || off > f.length {
		return 0, 0, fmt.Errorf("%w: kindle:pos offset %d outside the %d-byte fragment %d",
			ErrCorrupt, off, f.length, fid)
	}
	at := f.insertOffset - b.fragBase - b.skels[si].offset + off
	if at < 0 || at > b.kf8Sections[si].SizeBytes {
		return 0, 0, fmt.Errorf("%w: kindle:pos resolves to %d, outside the %d-byte section %d",
			ErrCorrupt, at, b.kf8Sections[si].SizeBytes, si)
	}
	return si, at, nil
}

// loadPageSpreads finds the RESC record — scanning the resource range
// by magic, the only way to locate it (both port sources do the same)
// — and reads its OPF spine for page-spread properties, keyed by the
// itemrefs' skelid: the 0-based skeleton index (KindleUnpack's
// getPart(int(skelid)), foliate-js's pageSpreads.set(parseInt)).
// A missing or unreadable RESC simply yields no properties; both port
// sources tolerate the damage rather than fail.
func (b *Book) loadPageSpreads() map[int]string {
	start := int(b.mobi.FirstImageIndex)
	if start < 0 {
		return nil
	}
	end := b.resourceEnd()
	for p := start; p < end; p++ {
		magic, err := b.pdb.RecordMagic(p)
		if err != nil || magic != identRESC {
			continue
		}
		rec, err := b.pdb.Record(p)
		if err != nil {
			return nil
		}
		return parseRESC(rec)
	}
	return nil
}

// rescSpine is the minimal OPF shape the RESC record carries: the
// spine's itemrefs with their skelid and properties attributes.
type rescSpine struct {
	Itemrefs []struct {
		SkelID     string `xml:"skelid,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"spine>itemref"`
}

// parseRESC reads a RESC record's spine: the header (magic plus a
// size word the parser does not need) runs to the first '<', the XML
// after it may be NUL-padded, and some records lack the <package>
// root — wrapping repairs those (foliate-js wraps unconditionally).
// A document that matches nothing decodes without error but yields no
// spreads, so the wrap cannot hang off an Unmarshal error; it keys on
// the root element instead. Returns nil on any parse failure, matching
// both sources' tolerance.
func parseRESC(rec []byte) map[int]string {
	lt := bytes.IndexByte(rec, '<')
	if lt < 0 {
		return nil
	}
	payload := bytes.TrimRight(rec[lt:], "\x00")
	doc := payload
	if !bytes.Contains(doc, []byte("<package")) {
		doc = append(append([]byte("<package>"), payload...), []byte("</package>")...)
	}
	var spine rescSpine
	if err := xml.Unmarshal(doc, &spine); err != nil {
		return nil
	}
	spreads := make(map[int]string)
	for _, ref := range spine.Itemrefs {
		skelid, err := strconv.Atoi(ref.SkelID)
		if err != nil {
			continue
		}
		if spread := pageSpread(ref.Properties); spread != "" {
			spreads[skelid] = spread
		}
	}
	if len(spreads) == 0 {
		return nil
	}
	return spreads
}

// pageSpread maps an OPF properties list to its page-spread value,
// accepting the EPUB 2 and rendition: prefixed forms (both port
// sources' lists).
func pageSpread(properties string) string {
	for _, p := range bytes.Fields([]byte(properties)) {
		switch string(p) {
		case "page-spread-left", "rendition:page-spread-left":
			return "left"
		case "page-spread-right", "rendition:page-spread-right":
			return "right"
		case "rendition:page-spread-center":
			return "center"
		}
	}
	return ""
}

// preview returns rec's first bytes for error messages, empty-safe.
func preview(rec []byte) string {
	if len(rec) > 4 {
		rec = rec[:4]
	}
	return string(rec)
}
