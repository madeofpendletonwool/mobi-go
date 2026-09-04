# mobi-go

Pure-Go reader for DRM-free MOBI and AZW3 (KF8) ebooks.

**Status:** scaffold. The parser is built in stages (headers, decompression,
resources, indices, KF8 reassembly); see the scope table below for what is
planned. Development is verified against [KindleUnpack](https://github.com/kevinhendricks/KindleUnpack)
as both the port source and the oracle.

## Scope

| Format | Status |
|---|---|
| MOBI6 (uncompressed / PalmDOC / HUFF-CDIC) | ✅ planned |
| AZW3 / KF8 (including combo files) | ✅ planned |
| DRM (any scheme) | ❌ refused with a typed error |
| KFX | ❌ out of scope |

DRM circumvention is permanently out of scope. DRM-protected files are
detected and refused with a typed error — never decrypted. This policy is
identical to how the consumer of this library (Backhog) treats `.aax`
audiobooks.

## Quickstart (sketch)

The public API lands with the final stage; this is the intended shape:

```go
import "github.com/madeofpendletonwool/mobi-go"

book, err := mobi.OpenReader(r)
if err != nil {
    // DRM-protected files fail with a typed error:
    // errors.Is(err, mobi.ErrDRMProtected)
    log.Fatal(err)
}
fmt.Println(book.Metadata.Title, book.Metadata.Author)
```

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

Test fixtures must be authored for this repo (synthetic files, converted
offline with tools like `ebook-convert`) and verified against golden digests.
**No copyrighted books are committed, ever.** This rule is about copyright,
not license — even a freely-licensed book stays out of the fixture corpus
unless its copyright holder contributed it for this purpose.

## Conventions

- **Conventional Commits** for commit messages.
- **Small PRs**, each carrying tests for what it changes.
- **Every parser error is a typed sentinel or wraps one** — callers use
  `errors.Is`, never string matching.
- **No `panic` in library code paths** — malformed input returns an error.

## Development

CI (gofmt, vet, staticcheck, `go test -race ./...`) runs on every push and
PR to `main`; `main` is protected and requires a PR plus a green CI run.

```sh
gofmt -l .
go vet ./...
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck ./...
go test -race ./...
```
