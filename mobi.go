// The frozen public API facade. The parsing layers live in their own
// files (pdb.go, headers.go, text.go, resource.go, toc.go, kf8.go);
// this file holds the entry points and the type every caller sees:
// Open/OpenBytes, the Section interface, and the unified Sections
// view over both formats.

package mobi

import "bytes"

// OpenBytes opens a book from an in-memory file image. It is the
// convenience form of Open over a bytes.Reader.
func OpenBytes(b []byte) (*Book, error) {
	return Open(bytes.NewReader(b), int64(len(b)))
}

// Section is one chapter-shaped chunk of a book: a MOBI6
// <mbp:pagebreak>-delimited section or a KF8 reassembled XHTML
// document. The two are the same contract, so format-agnostic callers
// (chapter extraction, readers) never branch on IsKF8.
type Section interface {
	// ByteRange returns the section's [start, end) byte range. For a
	// MOBI6 section the offsets index Book.RawText; for a KF8 section
	// they index that section's own assembled document, so every KF8
	// section starts at 0 and ends at its byte length.
	ByteRange() (start, end int)
	// Load returns the section's content — decoded MOBI6 HTML or
	// reassembled KF8 XHTML — with every attribute left exactly as
	// stored. Byte offsets never shift at this layer: rewriting
	// recindex attributes or kindle: URIs belongs to callers.
	Load() (string, error)
}

// Sections returns the book's sections in order: a MOBI6 book's
// pagebreak-delimited byte ranges (into RawText) or a KF8 book's
// reassembled XHTML documents. A book with no pagebreaks is one
// section covering everything. KF8-specific detail (linearity, page
// spreads) is available through KF8Sections.
func (b *Book) Sections() []Section {
	if b.mobi.Version >= 8 {
		if !b.kf8Loaded || len(b.kf8Sections) == 0 {
			return nil
		}
		out := make([]Section, len(b.kf8Sections))
		for i := range b.kf8Sections {
			out[i] = kf8View{b: b, i: i}
		}
		return out
	}
	if !b.textLoaded {
		return nil
	}
	return b.textSections()
}

// kf8View is the Section view of one KF8 section.
type kf8View struct {
	b *Book
	i int
}

func (v kf8View) ByteRange() (int, int) {
	s := v.b.kf8Sections[v.i]
	return 0, s.SizeBytes
}

func (v kf8View) Load() (string, error) {
	s := v.b.kf8Sections[v.i]
	return s.xhtml, nil
}
