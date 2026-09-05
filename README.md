# mobi-go

[![CI](https://github.com/madeofpendletonwool/mobi-go/actions/workflows/ci.yml/badge.svg)](https://github.com/madeofpendletonwool/mobi-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/madeofpendletonwool/mobi-go.svg)](https://pkg.go.dev/github.com/madeofpendletonwool/mobi-go)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0--only-blue.svg)](LICENSE)

Pure-Go reader for DRM-free MOBI and AZW3 (KF8) ebooks. Standard
library only, no CGO.

## Scope

| Format | Status |
|---|---|
| MOBI6 (uncompressed / PalmDOC / HUFF-CDIC) | ✅ supported |
| AZW3 / KF8 (including combo files) | ✅ supported |
| DRM (any scheme) | ❌ refused with a typed error |
| KFX | ❌ out of scope, permanently |

DRM circumvention is permanently out of scope. DRM-protected files are
detected and refused with a typed error — never decrypted. This policy
is identical to how the consumer of this library (Backhog) treats
`.aax` audiobooks.

## Quickstart

```go
package main

import (
	"errors"
	"fmt"
	"os"

	mobi "github.com/madeofpendletonwool/mobi-go"
)

func main() {
	data, err := os.ReadFile("book.mobi")
	if err != nil {
		panic(err)
	}
	book, err := mobi.OpenBytes(data)
	if err != nil {
		if errors.Is(err, mobi.ErrDRM) {
			fmt.Println("DRM-protected file: skipped")
			return
		}
		panic(err)
	}

	md := book.Metadata()
	fmt.Println(md.Title, "by", md.Authors)

	// Format-agnostic chapter walk: MOBI6 pagebreak sections and KF8
	// reassembled XHTML documents are the same interface.
	for _, section := range book.Sections() {
		start, end := section.ByteRange()
		html, err := section.Load()
		if err != nil {
			panic(err)
		}
		fmt.Printf("section [%d, %d): %d bytes\n", start, end, len(html))
	}

	// The cover, if any.
	if data, mime, err := book.Cover(); err == nil {
		fmt.Printf("cover: %d bytes of %s\n", len(data), mime)
	}
}
```

The read-side API: `Metadata`, `Text` (MOBI6 raw markup), `Sections`
(the unified chapter view), `TOC`, `Guide`, `Resource`, `Cover`,
`IsKF8`, `HasMOBI6Half`. Format-specific detail: `RawText` /
`FileposTargets` / `ResolveRecindex` (MOBI6), `KF8Sections`, `Flow`,
`ParseKindleURI` / `ResolveKindleURI` (KF8), `MOBI6Half` (combo
files). Every position the library reports is a byte offset — into
`RawText` for MOBI6, into the section's own assembled document for
KF8; see the [package documentation](https://pkg.go.dev/github.com/madeofpendletonwool/mobi-go)
for the full contract.

## Correctness

The implementation is ported from [KindleUnpack](https://github.com/kevinhendricks/KindleUnpack)
and verified against it as an oracle: a committed fixture corpus
(synthetic books authored for this repo plus real public-domain books)
is digested through KindleUnpack (`make regen-golden`, offline), and
`go test ./...` compares this library's output against the committed
digests — raw markup, KF8 section reassembly, metadata, chapter
titles, every resource record, and covers, byte for byte. Every
decoder also runs under native Go fuzzing in CI.

## Licensing

mobi-go is licensed **GPL-3.0-only** — see [LICENSE](LICENSE).

The implementation is ported with attribution from:

- **[KindleUnpack](https://github.com/kevinhendricks/KindleUnpack)** (GPL-3.0) —
  the canonical implementation, unpacking MOBI/KF8 since 2009 (calibre embeds
  it; the PyPI `mobi` package wraps it). Primary port source: algorithms and
  edge cases come from here.
- **[foliate-js](https://github.com/johnfactotum/foliate-js)** (MIT) — the
  structural reference for the reader-shaped API. MIT is GPL-compatible
  one-way: ported portions retain their copyright notice inside this
  GPL-3.0-only whole.

### Fixture rule

Test fixtures must be authored for this repo (synthetic files,
regenerable via `tools/fixgen`, or converted offline with tools like
`ebook-convert`) or genuinely public-domain / freely redistributable —
see `tools/fixtures/README.md` for the corpus provenance.
**No copyrighted books are committed, ever.** This rule is about
copyright, not license.

## Conventions

- **Conventional Commits** for commit messages.
- **Small PRs**, each carrying tests for what it changes.
- **Every parser error is a typed sentinel or wraps one** — callers use
  `errors.Is`, never string matching.
- **No `panic` in library code paths** — malformed input returns an error.

## Development

CI (gofmt, vet, staticcheck, `go test -race ./...`) runs on every push
and PR to `main`; pushes to `main` additionally fuzz every decoder for
30 s per target. `main` is protected and requires a PR plus a green CI
run.

```sh
make check          # everything CI checks, locally
make test           # go test -race ./...
make regen-golden   # offline: rebuild fixtures + oracle digests
make fuzz-short     # ~10s per fuzz target, locally
```
