// Package mobi reads DRM-free MOBI and AZW3 (KF8) ebooks.
//
// mobi is a pure Go reader library for Kindle ebook formats. It is being
// built in stages: the PalmDB container, MOBI and EXTH headers, PalmDOC
// and HUFF/CDIC decompression, resource records, indices and TOC, and
// KF8/AZW3 reassembly.
//
// # Scope
//
//	Format                                    Status
//	MOBI6 (uncompressed / PalmDOC / HUFF-CDIC)  planned
//	AZW3 / KF8 (including combo files)          planned
//	DRM (any scheme)                           refused with a typed error
//	KFX                                        out of scope
//
// DRM circumvention is permanently out of scope. Files protected by any
// DRM scheme are detected and refused with a typed error; no code in
// this package will ever decrypt DRM-protected content.
//
// # Concurrency
//
// A Book is single-goroutine state. HUFF/CDIC decompression memoizes
// expanded dictionary phrases as records are decoded, mutating shared
// state, so one Book must not be used from multiple goroutines at
// once; open one Book per goroutine instead.
//
// # Provenance
//
// The implementation is ported with attribution from KindleUnpack
// (GPL-3.0, https://github.com/kevinhendricks/KindleUnpack), the
// canonical MOBI/KF8 unpacker, with foliate-js
// (MIT, https://github.com/johnfactotum/foliate-js) as the structural
// reference. See LICENSE and README.md for details.
package mobi
