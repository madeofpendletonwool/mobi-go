// Package mobi reads DRM-free MOBI and AZW3 (KF8) ebooks.
//
// mobi is a pure Go reader library for Kindle ebook formats: it opens
// a file, parses its metadata, decompresses its text (PalmDOC or
// Amazon's HUFF/CDIC), reassembles KF8 sections, and returns the
// table of contents, guide, cover, and image resources. The zero
// dependency set is deliberate — standard library only.
//
// # Scope
//
//	Format                                      Status
//	MOBI6 (uncompressed / PalmDOC / HUFF-CDIC)  supported
//	AZW3 / KF8 (including combo files)          supported
//	DRM (any scheme)                           refused with a typed error
//	KFX                                        out of scope, permanently
//
// DRM circumvention is permanently out of scope. Files protected by
// any DRM scheme are detected and refused with ErrDRM before any
// content is parsed; no code in this package will ever decrypt
// DRM-protected content.
//
// # Opening books
//
// Open takes a reader over the whole file plus its size and sniffs
// the format from the record-0 headers: MOBI6 books load their text
// eagerly, KF8 books decompress the raw flow and reassemble every
// section, and combo files (a MOBI6 half followed by a KF8 half) open
// as their KF8 half with the other view available through MOBI6Half.
// OpenBytes is the convenience form over an in-memory image. A book
// that fails any stage of that eager load is refused whole; the
// library never returns a half-open Book.
//
// # Reading
//
// Format-agnostic consumers use Sections — the same interface over
// MOBI6 pagebreak chunks and KF8 reassembled XHTML documents — plus
// Metadata, TOC, Guide, Resource, and Cover. Format-specific detail
// (RawText for MOBI6, KF8Sections, Flow, ResolveKindleURI for KF8) is
// available alongside.
//
// # Byte offsets
//
// Every position this library reports is a byte offset, never a rune
// count, and — unless a method says otherwise — indexes Book.RawText:
// TOCItem.StartByte and GuideEntry.Filepos on MOBI6 books, and the
// ranges of MOBI6 Sections. MOBI6 markup may be windows-1252, so do
// all byte-offset math before decoding. The stated exceptions are KF8
// positions: a KF8 Section's range indexes that section's own
// assembled document, and TOCItem.SectionOffset indexes the same
// bytes; both still measure bytes, before decoding.
//
// # Concurrency
//
// A Book is single-goroutine state. HUFF/CDIC decompression memoizes
// expanded dictionary phrases as records are decoded, mutating shared
// state, so one Book must not be used from multiple goroutines at
// once; open one Book per goroutine instead. Distinct Books over the
// same file are independent.
//
// # Errors
//
// No code path panics; malformed input is an error. Every error
// wraps one of the package sentinels — test with errors.Is, never
// string matching:
//
//	ErrDRM                    the file carries DRM; refused whole
//	ErrNotPalmDB              not a PalmDB container at all
//	ErrCorrupt                structurally invalid data
//	ErrUnsupportedCompression a compression type this library does not read
//	ErrNoCover                no cover image (non-fatal by nature)
//	ErrRecordRange            a record/resource/index outside the file
//
// # Provenance
//
// The implementation is ported with attribution from KindleUnpack
// (GPL-3.0, https://github.com/kevinhendricks/KindleUnpack), the
// canonical MOBI/KF8 unpacker, with foliate-js
// (MIT, https://github.com/johnfactotum/foliate-js) as the structural
// reference. See LICENSE and README.md for details.
package mobi
