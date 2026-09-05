// MOBI6 text assembly: trailing-entry stripping, per-record
// decompression, concatenation, and section splitting.
//
// Ported with attribution from KindleUnpack's MobiHeader.getRawML
// (lib/mobi_header.py, GPL-3.0,
// https://github.com/kevinhendricks/KindleUnpack) and foliate-js's
// MOBI6.init / #removeTrailingEntries / loadText (mobi.js, MIT,
// https://github.com/johnfactotum/foliate-js).

package mobi

import (
	"fmt"
	"math/bits"
	"regexp"
	"slices"
	"strconv"

	"github.com/madeofpendletonwool/mobi-go/internal/varlen"
)

// textSection is one <mbp:pagebreak>-delimited chunk of a MOBI6
// book's text, exposed through the public Section interface. Start and
// End are byte offsets into Book.RawText: sections are contiguous,
// gapless, non-overlapping, and together cover the whole text. The
// pagebreak tag that ends a section sits at the start of the section
// that follows it (both port sources split this way and drop the tags
// when rendering).
type textSection struct {
	b     *Book
	start int // byte offset into RawText, inclusive
	end   int // byte offset into RawText, exclusive
}

// ByteRange implements Section with the section's RawText range.
func (s textSection) ByteRange() (int, int) { return s.start, s.end }

// Load implements Section: the section's slice of the book text,
// decoded per the book's encoding, with every attribute left exactly
// as stored — including the MOBI6 <img recindex="..."> and
// mediarecindex forms. This layer never rewrites strings: the byte
// offsets the library reports index into RawText, and any replacement
// here would shift them. Callers resolve recindex values with
// Book.ResolveRecindex and do their own rewriting at render time.
func (s textSection) Load() (string, error) {
	raw := s.b.RawText()
	if raw == nil {
		return "", fmt.Errorf("%w: section Load on a book without text", ErrCorrupt)
	}
	start := clamp(s.start, 0, len(raw))
	end := clamp(s.end, 0, len(raw))
	if end < start {
		return "", nil
	}
	return decodeString(s.b.mobi.Encoding, raw[start:end]), nil
}

// Section-splitting and filepos patterns, ported verbatim (modulo RE2
// syntax) from foliate-js's mbpPagebreakRegex / fileposRegex, which
// match KindleUnpack's page_pattern / link_pattern. They run over the
// raw bytes before decoding: MOBI6 HTML may be windows-1252, and every
// offset they produce must stay a byte offset. Both patterns are
// ASCII-safe on arbitrary bytes (negated classes, \d, \s only).
var (
	mbpPagebreakRE = regexp.MustCompile(`(?i)<\s*(?:mbp:)?pagebreak[^>]*>`)
	fileposRE      = regexp.MustCompile(`(?i)<[^<>]+filepos=['"]?(\d+)[^<>]*>`)
)

// loadRecord returns record i counted from the active half's start
// (record 0 of a combo file's KF8 half sits at Book.start): the
// offset-shifted record access every half-relative index goes through.
func (b *Book) loadRecord(i int) ([]byte, error) {
	return b.pdb.Record(b.start + i)
}

// loadText returns text record i (0-based over the book's NumTextRecords
// text records): the raw record bytes with their trailing bookkeeping
// stripped, decompressed per the file's compression type. HUFF/CDIC
// records go through the book's cached decompressor (huffcdic.go); the
// returned slice aliases internal buffers and must not be modified.
func (b *Book) loadText(i int) ([]byte, error) {
	if i < 0 || i >= int(b.palmdoc.NumTextRecords) {
		return nil, fmt.Errorf("%w: text record %d of %d",
			ErrRecordRange, i, b.palmdoc.NumTextRecords)
	}
	rec, err := b.loadRecord(i + 1)
	if err != nil {
		return nil, err
	}
	rec, err = b.stripTrailingEntries(rec)
	if err != nil {
		return nil, err
	}
	switch b.palmdoc.Compression {
	case compressionNone:
		return rec, nil
	case compressionPalmDOC:
		return decompressPalmDOC(rec)
	case compressionHuffCDIC:
		decompress, err := b.huffCDICDecompressor()
		if err != nil {
			return nil, err
		}
		return decompress(rec)
	default:
		return nil, fmt.Errorf("%w: compression %d",
			ErrUnsupportedCompression, b.palmdoc.Compression)
	}
}

// stripTrailingEntries removes the per-record trailing bookkeeping
// bytes from a raw (still-compressed) text record: one varlen-sized
// trailing data entry per bit set in trailingFlags>>1, then — when
// trailingFlags bit 0 is set — the multibyte overlap bytes counted by
// the final byte's low two bits.
//
// Strip-ordering verdict, recorded per the stage plan: KindleUnpack's
// getRawML strips BOTH kinds from the compressed record BEFORE
// decompression (trimTrailingDataEntries wraps loadSection, and its
// result feeds self.unpack), entries first, multibyte bytes second.
// foliate-js does the same in the same order (loadRecord →
// removeTrailingEntries → decompress). The plan's caveat that
// KindleUnpack strips the entries after decompression is mistaken:
// the two port sources agree, and this implementation follows that
// common order. KindleUnpack also skips stripping entirely for MOBI
// header versions below 5 and for headers too short to carry the
// flags field (the stage-3 parser already leaves such flags at zero);
// the version gate is kept here.
func (b *Book) stripTrailingEntries(rec []byte) ([]byte, error) {
	if b.mobi.Version < 5 {
		return rec, nil
	}
	for range bits.OnesCount32(b.mobi.TrailingFlags >> 1) {
		n := varlen.FromEnd(rec)
		if n <= 0 || n > len(rec) {
			return nil, fmt.Errorf("%w: trailing entry size %d exceeds the %d-byte record",
				ErrCorrupt, n, len(rec))
		}
		rec = rec[:len(rec)-n]
	}
	if b.mobi.TrailingFlags&1 != 0 {
		if len(rec) == 0 {
			return nil, fmt.Errorf("%w: multibyte overlap bytes on an empty record", ErrCorrupt)
		}
		n := int(rec[len(rec)-1]&3) + 1
		if n > len(rec) {
			return nil, fmt.Errorf("%w: multibyte overlap length %d exceeds the %d-byte record",
				ErrCorrupt, n, len(rec))
		}
		rec = rec[:len(rec)-n]
	}
	return rec, nil
}

// The variable-length quantity reads this file used to own moved to
// internal/varlen (varlen.FromEnd here, varlen.Read in the index
// layer) so text and INDX parsing share one codec.

// loadAllText assembles the book's raw text: every text record through
// loadText, concatenated in order. The PalmDOC header's textLength is
// advisory — the observed total can differ from it by record-boundary
// slack — so it is deliberately not checked here.
func (b *Book) loadAllText() error {
	if b.textLoaded {
		return nil
	}
	n := int(b.palmdoc.NumTextRecords)
	parts := make([][]byte, 0, n)
	total := 0
	for i := range n {
		part, err := b.loadText(i)
		if err != nil {
			return fmt.Errorf("text record %d: %w", i+1, err)
		}
		parts = append(parts, part)
		total += len(part)
	}
	raw := make([]byte, 0, total)
	for _, part := range parts {
		raw = append(raw, part...)
	}
	b.rawText = raw
	b.textLoaded = true
	return nil
}

// RawText returns the book's full text as raw bytes: every text record
// decompressed and concatenated in order, byte-exact. filepos values,
// Section offsets, and every other position the library reports index
// into these bytes, and MOBI6 text may be windows-1252 — do all
// byte-offset math before decoding. The returned slice aliases the
// Book; callers must not modify it.
//
// MOBI6 only: AZW3/KF8 books reassemble their text as sections
// (KF8Sections) and expose flow bytes through Flow; RawText returns
// nil for them. The MOBI6 half of a combo file reads normally through
// MOBI6Half.
func (b *Book) RawText() []byte {
	if b.mobi.Version >= 8 || !b.textLoaded {
		return nil
	}
	return b.rawText
}

// Text returns the book's full text decoded per the declared encoding
// (UTF-8 or windows-1252). MOBI6 only; KF8 files return "".
func (b *Book) Text() string {
	if b.mobi.Version >= 8 || !b.textLoaded {
		return ""
	}
	return decodeString(b.mobi.Encoding, b.rawText)
}

// textSections splits the MOBI6 text at <mbp:pagebreak> tags and
// returns the ranges as byte offsets into RawText. A book with no
// pagebreaks is one section covering everything. Called by the
// unified Book.Sections.
func (b *Book) textSections() []Section {
	raw := b.rawText
	if !b.textLoaded {
		return nil
	}
	breaks := mbpPagebreakRE.FindAllIndex(raw, -1)
	starts := make([]int, 0, len(breaks)+1)
	starts = append(starts, 0)
	for _, m := range breaks {
		starts = append(starts, m[0])
	}
	sections := make([]Section, 0, len(starts))
	for i, start := range starts {
		end := len(raw)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		sections = append(sections, textSection{b: b, start: start, end: end})
	}
	return sections
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// FileposTargets returns every filepos attribute value in the text —
// byte offsets into RawText — deduplicated and sorted ascending.
// Callers map them to anchors. Values too large to represent are
// skipped. MOBI6 only; KF8 files return nil.
func (b *Book) FileposTargets() []int {
	if b.mobi.Version >= 8 || !b.textLoaded {
		return nil
	}
	var targets []int
	seen := make(map[int]struct{})
	for _, m := range fileposRE.FindAllSubmatchIndex(b.rawText, -1) {
		v, err := strconv.ParseInt(string(b.rawText[m[2]:m[3]]), 10, 63)
		if err != nil {
			continue
		}
		t := int(v)
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			targets = append(targets, t)
		}
	}
	slices.Sort(targets)
	return targets
}
