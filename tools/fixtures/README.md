# Fixture corpus

`testdata/books` holds the corpus `go test ./...` verifies against the
KindleUnpack oracle digests in `testdata/golden` (see `golden_test.go`).

## The rule

**No copyrighted books are committed, ever.** That rule is about
copyright, not license — even a freely-licensed book stays out unless
its copyright holder contributed it for this purpose.

Everything here is either:

- **Synthetic books authored for this repo** — generated
  deterministically by `tools/fixgen` (`go run ./tools/fixgen`), built
  from the same encoder the parser's round-trip tests use and verified
  against KindleUnpack through the oracle digests. Nothing copyrighted
  enters the repo through them; they are original works of this
  project. A maintainer with calibre installed may regenerate any of
  them equivalently from authored HTML with `ebook-convert` — the
  oracle accepts either provenance.
- **Real books whose text is public domain or freely redistributable**
  (see provenance below), provided by the project owner.

## Provenance

| Fixture | What it covers | Origin |
|---|---|---|
| `mobi6-none.mobi` | MOBI6 uncompressed, no EXTH, windows-1252 title, legacy `<toc>`/`<guide>` | synthetic (`fixgen`) |
| `mobi6-palmdoc.mobi` | PalmDOC, multi-record, trailing entries, full EXTH, 4 images + cover, hierarchical INDX NCX | synthetic (`fixgen`) |
| `mobi6-huffcdic.mobi` | HUFF/CDIC, nested phrase (recursive expansion + memoization), INDX NCX | synthetic (`fixgen`) |
| `azw3-kf8.azw3` | Pure KF8: cuts inside tags, multi-fragment sections, non-linear section, CSS extra flow, RESC page spreads, NCX, guide index | synthetic (`fixgen`) |
| `azw3-combo.azw3` | Combo file (EXTH 121 boundary, shared images) | synthetic (`fixgen`) |
| `mobi6-drm.mobi` | Refused with `ErrDRM` | synthetic (`fixgen`) |
| `corrupt-*.mobi/.azw3/.bin` | Refused with `ErrNotPalmDB` / `ErrCorrupt` | synthetic (`fixgen`) |
| `real-alice.mobi` | Real PalmDOC MOBI6: 200 KB text, 27 images, 12-chapter TOC, no cover | *Alice's Adventures in Wonderland* (Lewis Carroll, 1865 — public domain); conversion contributed by the project owner |
| `real-around-the-world.mobi` | Real PalmDOC MOBI6: 746 KB multilingual text (UTF-8 coverage across many scripts), cover | *Around the World in 28 Languages* (Infogrid Pacific — the classic freely-redistributed Unicode test document); contributed by the project owner |

Known gap: no *real* HUFF/CDIC-compressed file is in the corpus (the
format is Amazon-proprietary output; a legitimate DRM-free sample
hasn't surfaced). The synthetic HUFF/CDIC fixture is oracle-verified
against KindleUnpack, which validates both its structural validity and
our decoder against the canonical implementation.

## The oracle

`tools/oracle-digest.py` runs KindleUnpack (the library the PyPI `mobi`
package wraps) over each fixture and writes
`testdata/golden/<fixture>.json`: metadata, the raw markup's SHA-256
(`getRawML()` — the same seam this library's port mirrors), section
and part counts, chapter titles, resource digests, and the cover
digest.

```
git clone https://github.com/kevinhendricks/KindleUnpack /somewhere
make regen-golden KINDLEUNPACK_PATH=/somewhere
```

(or `pip install mobi`, which bundles the same code). Regeneration is
an offline, on-demand maintainer step; CI runs only the Go
comparisons. No third-party code is vendored in the module — only the
digests are committed.
