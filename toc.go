// The NCX table of contents and the guide, built on the INDX index
// reader (indx.go).
//
// Ported with attribution from KindleUnpack's ncxExtract.parseNCX /
// buildNCX and its MOBI6 <guide> extraction (lib/mobi_ncx.py,
// lib/kindleunpack.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack), with foliate-js's
// getNCX / getGuide (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js) as the structural
// cross-check.
//
// NCX tag meanings, as read by both sources: 1 = filepos / offset,
// 2 = length, 3 = label (CNCX offset), 4 = heading level, 5 = kind
// (CNCX offset), 6 = pos pair (fid, off — KF8), 21 = parent entry,
// 22 = first child, 23 = last child.

package mobi

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// NCX tag numbers, per the port sources' tag_fieldname_map and
// foliate-js's getNCX mapping.
const (
	ncxTagOffset     = 1 // filepos (MOBI6) byte offset into RawText
	ncxTagLength     = 2
	ncxTagLabel      = 3 // CNCX phrase offset
	ncxTagLevel      = 4 // heading level; 0 marks roots
	ncxTagKind       = 5 // CNCX phrase offset
	ncxTagPos        = 6 // (fid, off) pair, KF8 kindle:pos
	ncxTagParent     = 21
	ncxTagFirstChild = 22
	ncxTagLastChild  = 23

	guideTagLabel = 1 // CNCX phrase offset (KF8 guide)
	guideTagFile  = 3 // fragment/file number
	guideTagPos   = 6 // pos pair, preferred over tag 3 when present
)

// TOCItem is one table-of-contents entry. StartByte is the MOBI6
// filepos: a byte offset into Book.RawText, so callers map TOC entries
// to canonical text offsets directly; it is -1 when the entry carries
// no position (and for KF8 entries, whose kindle:pos references resolve
// to sections in the KF8 reassembly stage). Length is the entry's tag-2
// length when present, else -1. Fid and Off carry the KF8 pos pair
// (tag 6), -1 when absent. For KF8 books, Section and SectionOffset
// resolve that pair: the reassembled section's index (KF8Sections) and
// a byte offset into that section's assembled XHTML (the same raw-byte
// coordinate system as StartByte — decode after measuring); both stay
// -1 on MOBI6 and on unresolvable KF8 entries. Children recurse in
// document order.
type TOCItem struct {
	Label         string
	StartByte     int
	Length        int
	Fid           int
	Off           int
	Section       int
	SectionOffset int
	Children      []TOCItem
}

// GuideEntry is one guide (landmark) reference. For KF8 books Href is
// the entry's kindle:pos:fid:XXXX:off:YYYYYYYYYY URI; for the MOBI6
// HTML fallback the position is a filepos byte offset into RawText,
// carried in Filepos with Href empty.
type GuideEntry struct {
	Label   string
	Type    string
	Href    string
	Filepos int
}

// Legacy HTML blocks and attributes, for books with no INDX index.
// KindleUnpack reads the <guide> block out of the raw MOBI6 HTML with
// a case-insensitive regex and pulls filepos values with
// filepos=['"]{0,1}0*(\d+) (optional quotes, leading zeros); the
// <toc>/<tocpoint> markup is the Mobipocket logical TOC the backhog
// integration documented as the chapter fallback. Patterns are
// ASCII-safe over arbitrary bytes.
var (
	tocBlockRE     = regexp.MustCompile(`(?is)<toc>(.*?)</toc>`)
	tocPointRE     = regexp.MustCompile(`(?is)<tocpoint\b([^>]*)>([^<]*)`)
	guideBlockRE   = regexp.MustCompile(`(?is)<guide>(.*?)</guide>`)
	referenceRE    = regexp.MustCompile(`(?is)<reference\b([^>]*?)/?>`)
	fileposAttrRE  = regexp.MustCompile(`(?i)filepos\s*=\s*['"]?0*(\d+)`)
	tocdepthAttrRE = regexp.MustCompile(`(?i)tocdepth\s*=\s*['"]?(\d+)`)
	typeAttrRE     = regexp.MustCompile(`(?i)type\s*=\s*['"]([^'"]*)['"]`)
	titleAttrRE    = regexp.MustCompile(`(?i)title\s*=\s*['"]([^'"]*)['"]`)
)

// TOC returns the book's table of contents as a tree.
//
// Fallback order: the INDX index named by the MOBI header's indx field
// (present on essentially every MOBI6 book from this century and every
// KF8 file) is authoritative; when it is the 0xFFFFFFFF sentinel the
// MOBI6 legacy HTML <toc> block in the raw text is parsed instead.
// A book with neither carries no table of contents and returns
// (nil, nil).
//
// MOBI6 entries anchor at StartByte, a byte offset into RawText; KF8
// entries resolve their kindle:pos pairs to a section index and byte
// offset (TOCItem.Section, TOCItem.SectionOffset).
//
// Results are cached: the index is parsed once per Book.
func (b *Book) TOC() ([]TOCItem, error) {
	if b.tocLoaded {
		return b.toc, b.tocErr
	}
	b.tocLoaded = true
	if b.mobi.Indx >= 0 {
		b.toc, b.tocErr = b.parseNCXIndex(int(b.mobi.Indx))
		return b.toc, b.tocErr
	}
	b.toc = b.legacyTOC()
	return b.toc, nil
}

// parseNCXIndex reads the INDX NCX index at firstIdx and assembles the
// TOC tree. Roots are the heading-level-0 entries; children come from
// the parent-entry tags (foliate-js's mapping) or, when no entry
// carries a parent, from the first/last-child index ranges
// (KindleUnpack's recursINDX). When no entry declares a heading level
// either, the entries form a flat list.
func (b *Book) parseNCXIndex(firstIdx int) ([]TOCItem, error) {
	entries, pool, err := b.readIndex(firstIdx)
	if err != nil {
		return nil, fmt.Errorf("ncx index: %w", err)
	}
	flat := make([]ncxFlat, len(entries))
	for i, e := range entries {
		f := ncxFlat{
			start:         -1,
			length:        -1,
			fid:           -1,
			off:           -1,
			level:         -1,
			parent:        -1,
			firstChild:    -1,
			lastChild:     -1,
			section:       -1,
			sectionOffset: -1,
		}
		if vs := e.Values[ncxTagOffset]; len(vs) > 0 {
			f.start = vs[0]
		}
		if vs := e.Values[ncxTagLength]; len(vs) > 0 {
			f.length = vs[0]
		}
		if vs := e.Values[ncxTagLabel]; len(vs) > 0 {
			if label, ok := pool.lookup(vs[0]); ok {
				f.label = html.UnescapeString(label)
			}
		}
		if vs := e.Values[ncxTagLevel]; len(vs) > 0 {
			f.level = vs[0]
		}
		if vs := e.Values[ncxTagPos]; len(vs) > 0 {
			f.fid = vs[0]
			if len(vs) > 1 {
				f.off = vs[1]
			}
		}
		if vs := e.Values[ncxTagParent]; len(vs) > 0 {
			f.parent = vs[0]
		}
		if vs := e.Values[ncxTagFirstChild]; len(vs) > 0 {
			f.firstChild = vs[0]
		}
		if vs := e.Values[ncxTagLastChild]; len(vs) > 0 {
			f.lastChild = vs[0]
		}
		// KF8 entries carry their target as a pos pair; resolve it to
		// (section, offset) through the reassembly tables. KindleUnpack
		// retargets dangling links to the top of their file and
		// foliate-js leaves them undefined; an unresolvable pair keeps
		// Section -1 rather than failing the whole TOC.
		if b.kf8Loaded && f.fid >= 0 {
			if section, offset, err := b.resolvePos(f.fid, f.off); err == nil {
				f.section, f.sectionOffset = section, offset
			}
		}
		flat[i] = f
	}
	return buildTOCTree(flat), nil
}

// ncxFlat is one NCX entry before tree assembly.
type ncxFlat struct {
	label         string
	start         int
	length        int
	fid, off      int
	level         int
	parent        int
	firstChild    int
	lastChild     int
	section       int
	sectionOffset int
}

// buildTOCTree assembles the entry list into a tree, preferring parent
// pointers, then child ranges, then flat. Assembly uses a consumed set:
// every entry appears at most once, so malformed indexes (cycles,
// self-parents, overlapping ranges) lose entries instead of looping.
func buildTOCTree(flat []ncxFlat) []TOCItem {
	hasParent, hasRange := false, false
	for _, f := range flat {
		if f.parent >= 0 {
			hasParent = true
		}
		if f.firstChild >= 0 || f.lastChild >= 0 {
			hasRange = true
		}
	}

	var children [][]int
	switch {
	case hasParent:
		children = make([][]int, len(flat))
		for j, f := range flat {
			p := f.parent
			if p >= 0 && p < len(flat) && p != j {
				children[p] = append(children[p], j)
			}
		}
	case hasRange:
		children = make([][]int, len(flat))
		covered := make([]bool, len(flat))
		for i, f := range flat {
			if f.firstChild < 0 && f.lastChild < 0 {
				// No range tags on this entry: it claims nothing (a
				// missing range is not the full span).
				continue
			}
			lo, hi := f.firstChild, f.lastChild
			if lo < 0 {
				lo = 0
			}
			if hi < lo || hi >= len(flat) {
				hi = len(flat) - 1
			}
			for j := lo; j <= hi; j++ {
				if j != i {
					children[i] = append(children[i], j)
					covered[j] = true
				}
			}
		}
		// Without a heading level to anchor recursion (KindleUnpack
		// starts at level 0), the roots are the uncovered entries.
		roots := make([]int, 0, len(flat))
		anyLevel := false
		for _, f := range flat {
			if f.level >= 0 {
				anyLevel = true
				break
			}
		}
		for j, f := range flat {
			if covered[j] {
				continue
			}
			if anyLevel && f.level > 0 {
				continue
			}
			roots = append(roots, j)
		}
		used := make([]bool, len(flat))
		var out []TOCItem
		for _, r := range roots {
			if used[r] {
				continue
			}
			out = append(out, assembleTOC(flat, children, r, used))
		}
		return out
	default:
		// Flat: no hierarchy tags at all.
		items := make([]TOCItem, len(flat))
		for i, f := range flat {
			items[i] = f.item()
		}
		return items
	}

	// Parent-pointer mode: roots are the level-0 entries, falling back
	// to parentless entries when no entry declares a level.
	roots := make([]int, 0, len(flat))
	for j, f := range flat {
		if f.level == 0 {
			roots = append(roots, j)
		}
	}
	if len(roots) == 0 {
		for j, f := range flat {
			if f.parent < 0 {
				roots = append(roots, j)
			}
		}
	}
	used := make([]bool, len(flat))
	var out []TOCItem
	for _, r := range roots {
		if used[r] {
			continue
		}
		out = append(out, assembleTOC(flat, children, r, used))
	}
	return out
}

// assembleTOC converts flat[i] and its (transitive) children into
// TOCItems, marking everything it consumes in used.
func assembleTOC(flat []ncxFlat, children [][]int, i int, used []bool) TOCItem {
	used[i] = true
	item := flat[i].item()
	for _, c := range children[i] {
		if used[c] {
			continue
		}
		item.Children = append(item.Children, assembleTOC(flat, children, c, used))
	}
	return item
}

func (f ncxFlat) item() TOCItem {
	return TOCItem{
		Label:         f.label,
		StartByte:     f.start,
		Length:        f.length,
		Fid:           f.fid,
		Off:           f.off,
		Section:       f.section,
		SectionOffset: f.sectionOffset,
	}
}

// legacyTOC parses the Mobipocket logical TOC from the MOBI6 raw text:
// a <toc> block of <tocpoint filepos="..." tocdepth="N">label</tocpoint>
// entries. Depth N nests below the most recent entry at depth N-1.
// Entries without a filepos are skipped — there is nothing to anchor.
func (b *Book) legacyTOC() []TOCItem {
	raw := b.RawText()
	if raw == nil {
		return nil
	}
	block := tocBlockRE.FindSubmatch(raw)
	if block == nil {
		return nil
	}
	type node struct {
		item  TOCItem
		depth int
		kids  []*node
	}
	var roots []*node
	var stack []*node
	for _, m := range tocPointRE.FindAllSubmatch(block[1], -1) {
		attrs, label := m[1], m[2]
		posMatch := fileposAttrRE.FindSubmatch(attrs)
		if posMatch == nil {
			continue
		}
		start, err := strconv.Atoi(string(posMatch[1]))
		if err != nil {
			continue
		}
		depth := 0
		if d := tocdepthAttrRE.FindSubmatch(attrs); d != nil {
			if v, err := strconv.Atoi(string(d[1])); err == nil {
				depth = v
			}
		}
		n := &node{
			item: TOCItem{
				Label:     html.UnescapeString(b.decodeText(label)),
				StartByte: start,
				Length:    -1,
				Fid:       -1,
				Off:       -1,
			},
			depth: depth,
		}
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			parent := stack[len(stack)-1]
			parent.kids = append(parent.kids, n)
		}
		stack = append(stack, n)
	}
	var convert func(n *node) TOCItem
	convert = func(n *node) TOCItem {
		item := n.item
		for _, k := range n.kids {
			item.Children = append(item.Children, convert(k))
		}
		return item
	}
	out := make([]TOCItem, 0, len(roots))
	for _, r := range roots {
		out = append(out, convert(r))
	}
	return out
}

// decodeText decodes raw book-text bytes with the book's encoding —
// the same single translation function Metadata uses.
func (b *Book) decodeText(raw []byte) string {
	return decodeString(b.mobi.Encoding, raw)
}

// Guide returns the book's guide (landmark) references.
//
// Fallback order: the KF8 guide INDX index named by the MOBI header's
// KF8-only guide field is authoritative; MOBI6 books fall back to the
// <guide> block in the raw text, whose <reference type="..."
// title="..." filepos="..."> entries KindleUnpack extracts with a
// case-insensitive regex. A book with neither returns (nil, nil).
// Results are cached.
func (b *Book) Guide() ([]GuideEntry, error) {
	if b.guideLoaded {
		return b.guide, b.guideErr
	}
	b.guideLoaded = true
	if b.kf8 != nil && b.kf8.Guide >= 0 {
		b.guide, b.guideErr = b.parseGuideIndex(int(b.kf8.Guide))
		return b.guide, b.guideErr
	}
	b.guide = b.legacyGuide()
	return b.guide, nil
}

// parseGuideIndex reads the KF8 guide INDX index: the entry name is the
// reference type, tag 1 the label (CNCX), and tag 6's pos pair (or
// tag 3's file number) the target, rendered as a kindle:pos URI —
// foliate-js's getGuide mapping, which matches KindleUnpack's KF8
// guidetbl (ref_type, ref_title, file number).
func (b *Book) parseGuideIndex(firstIdx int) ([]GuideEntry, error) {
	entries, pool, err := b.readIndex(firstIdx)
	if err != nil {
		return nil, fmt.Errorf("guide index: %w", err)
	}
	var out []GuideEntry
	for _, e := range entries {
		g := GuideEntry{Type: firstToken(e.Name), Filepos: -1}
		if vs := e.Values[guideTagLabel]; len(vs) > 0 {
			if label, ok := pool.lookup(vs[0]); ok {
				g.Label = html.UnescapeString(label)
			}
		}
		fid, off := -1, 0
		if vs := e.Values[guideTagPos]; len(vs) > 0 {
			fid = vs[0]
			if len(vs) > 1 {
				off = vs[1]
			}
		} else if vs := e.Values[guideTagFile]; len(vs) > 0 {
			fid = vs[0]
		}
		if fid >= 0 {
			g.Href = kindlePosURI(fid, off)
		}
		out = append(out, g)
	}
	return out, nil
}

// legacyGuide extracts <reference> entries from the MOBI6 raw text's
// <guide> block. Attributes type and title are quoted in every real
// file (KindleUnpack normalizes to that before reading); filepos
// tolerates missing quotes and leading zeros exactly as KindleUnpack's
// filepos pattern does.
func (b *Book) legacyGuide() []GuideEntry {
	raw := b.RawText()
	if raw == nil {
		return nil
	}
	block := guideBlockRE.FindSubmatch(raw)
	if block == nil {
		return nil
	}
	var out []GuideEntry
	for _, m := range referenceRE.FindAllSubmatch(block[1], -1) {
		attrs := m[1]
		pos := fileposAttrRE.FindSubmatch(attrs)
		if pos == nil {
			continue
		}
		filepos, err := strconv.Atoi(string(pos[1]))
		if err != nil {
			continue
		}
		g := GuideEntry{Filepos: filepos}
		if t := typeAttrRE.FindSubmatch(attrs); t != nil {
			g.Type = string(t[1])
		}
		if t := titleAttrRE.FindSubmatch(attrs); t != nil {
			g.Label = html.UnescapeString(b.decodeText(t[1]))
		}
		out = append(out, g)
	}
	return out
}

// firstToken returns the first whitespace-separated token of s — the
// guide reference type proper (extra tokens are secondary types).
func firstToken(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// base32Digits is KindleUnpack's toBase32 alphabet, which foliate-js's
// makePosURI reproduces with toString(32).toUpperCase(): 0-9 then A-V.
const base32Digits = "0123456789ABCDEFGHIJKLMNOPQRSTUV"

// toBase32 renders v in base 32, zero-padded to npad digits — the
// kindle:pos:fid:XXXX:off:YYYYYYYYYY form's number encoding, ported
// from KindleUnpack's toBase32 (lib/mobi_utils.py).
func toBase32(v, npad int) string {
	if v == 0 {
		return strings.Repeat("0", npad)
	}
	var digits []byte
	for v > 0 {
		digits = append(digits, base32Digits[v%32])
		v /= 32
	}
	for len(digits) < npad {
		digits = append(digits, '0')
	}
	// digits holds least-significant first; reverse into place.
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// kindlePosURI renders a KF8 position reference: fid zero-padded to 4
// base-32 digits, off to 10 (foliate-js's makePosURI, matching
// KindleUnpack's kindle:pos formatting).
func kindlePosURI(fid, off int) string {
	return "kindle:pos:fid:" + toBase32(fid, 4) + ":off:" + toBase32(off, 10)
}
