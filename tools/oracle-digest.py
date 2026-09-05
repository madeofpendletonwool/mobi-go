#!/usr/bin/env python3
"""Oracle digest generator for the mobi-go golden corpus.

Runs KindleUnpack (the library the PyPI ``mobi`` package wraps) over
each fixture in testdata/books and emits testdata/golden/<name>.json:

  - resolved metadata (title, authors, language, publisher, isbn)
  - the raw markup's SHA-256 and length (mh.getRawML() — the same
    seam the mobi-go port mirrors)
  - section count (MOBI6 pagebreaks / KF8 skeletons) and, for KF8,
    per-part digests of the reassembled XHTML documents
  - chapter titles (INDX NCX, else the MOBI6 legacy <toc> block)
  - resource record digests and the cover digest

KindleUnpack location, in resolution order:

  1. $KINDLEUNPACK_PATH — a checkout of
     https://github.com/kevinhendricks/KindleUnpack
  2. an installed PyPI ``mobi`` package (``pip install mobi``), whose
     bundled KindleUnpack lives under mobi/KindleUnpack
  3. tools/third_party/KindleUnpack (a local checkout)

No third-party code is vendored in the Go module; this tool and the
committed digests are the only oracle artifacts. Run with ``make
regen-golden`` (offline, on demand); CI runs only the Go comparisons.
"""

import hashlib
import io
import json
import os
import re
import struct
import sys
import tempfile
from contextlib import redirect_stdout

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BOOKS = os.path.join(REPO, "testdata", "books")
GOLDEN = os.path.join(REPO, "testdata", "golden")

K8_BOUNDARY = b"BOUNDARY"

PAGEBREAK_RE = re.compile(rb"<\s*(?:mbp:)?pagebreak[^>]*>", re.IGNORECASE)
TOCPOINT_RE = re.compile(rb"<tocpoint\b[^>]*>([^<]*)", re.IGNORECASE | re.DOTALL)


def find_ku_lib():
    env = os.environ.get("KINDLEUNPACK_PATH")
    if env:
        lib = os.path.join(env, "lib")
        if os.path.isdir(lib):
            return lib
        if os.path.isdir(os.path.join(env, "mobi_header.py")):
            return env
    try:
        import mobi as pypi_mobi  # noqa: F401
        pkg = os.path.dirname(pypi_mobi.__file__)
        for cand in (os.path.join(pkg, "KindleUnpack", "lib"), pkg):
            if os.path.isfile(os.path.join(cand, "mobi_header.py")):
                return cand
    except ImportError:
        pass
    local = os.path.join(REPO, "tools", "third_party", "KindleUnpack", "lib")
    if os.path.isdir(local):
        return local
    sys.exit("oracle-digest: no KindleUnpack found; set KINDLEUNPACK_PATH, "
             "pip install mobi, or clone it to tools/third_party/KindleUnpack")


ku_lib = find_ku_lib()
sys.path.insert(0, os.path.dirname(ku_lib.rstrip(os.sep)))

from lib import kindleunpack  # noqa: E402
from lib.kindleunpack import MobiHeader, Sectionizer, fileNames, unpackException  # noqa: E402
from lib.mobi_k8proc import K8Processor  # noqa: E402
from lib.mobi_ncx import ncxExtract  # noqa: E402


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def half_digest(mh, sect, files, first_resource=None):
    """Digest one MOBI header's half the way processBook sees it.

    first_resource overrides the half's resource start: the KF8 half of
    a combo shares the MOBI6 half's images (stored before the BOUNDARY
    record, addressed absolutely — foliate-js keeps the m6 half's
    resourceStart when it opens the KF8 half), so the combo's KF8 half
    digests records from there rather than from its own k8-relative
    placeholder slot.
    """
    out = {}
    metadata = mh.getMetaData()

    def meta(key):
        values = metadata.get(key) or []
        return values[0] if values else None

    out["metadata"] = {
        "title": meta("Updated_Title") or meta("Title") or "",
        "authors": list(metadata.get("Creator") or []),  # EXTH 100 is 'Creator' here
        "language": (meta("Language") or "").lower(),  # KindleUnpack lowercases locales
        "publisher": meta("Publisher") or "",
        "isbn": meta("ISBN") or "",
    }

    raw_ml = mh.getRawML()
    out["raw_text_sha256"] = sha256(raw_ml)
    out["raw_text_len"] = len(raw_ml)

    # Cover: EXTH CoverOffset (201), else ThumbOffset (202), else none.
    cover_offset = metadata.get("CoverOffset") or metadata.get("ThumbOffset") or ["-1"]
    cover_sha = None
    try:
        cover_offset = int(cover_offset[0])
    except (TypeError, ValueError):
        cover_offset = -1
    resources = resource_records(mh, sect, first_resource)
    if 0 <= cover_offset < len(resources):
        cover_sha = sha256(resources[cover_offset])
    out["cover_sha256"] = cover_sha
    out["resource_sha256"] = [sha256(r) for r in resources]

    if mh.isK8():
        k8proc = K8Processor(mh, sect, files, False)
        with redirect_stdout(io.StringIO()):
            k8proc.buildParts(raw_ml)
        parts = [bytes(p) for p in k8proc.parts]
        out["section_count"] = len(k8proc.skeltbl)
        out["part_sha256"] = [sha256(p) for p in parts]
        out["part_len"] = [len(p) for p in parts]
        out["chapter_titles"] = [
            e["text"] for e in ncx_entries(mh, files)
        ]
        out["kind"] = "kf8"
    else:
        out["section_count"] = len(PAGEBREAK_RE.findall(raw_ml)) + 1
        titles = [e["text"] for e in ncx_entries(mh, files)]
        if not titles:
            # Legacy logical TOC block in the raw markup.
            for m in TOCPOINT_RE.finditer(raw_ml):
                titles.append(m.group(1).decode(mh.codec, errors="replace").strip())
        out["chapter_titles"] = titles
        out["kind"] = "mobi6"
    return out


def ncx_entries(mh, files):
    try:
        ncx = ncxExtract(mh, files)
        with redirect_stdout(io.StringIO()):
            data = ncx.parseNCX()
        return data or []
    except Exception:
        return []


def resource_records(mh, sect, first_resource=None):
    """Raw resource records: [firstresource, first INDX or boundary).

    The same range both readers use — KindleUnpack's resource loop
    walks every section from firstresource to the half's end, treating
    index records and the BOUNDARY marker as placeholders; the digest
    stops at the first INDX record (or the boundary/EOF), which is
    where mobi-go's resource run ends too.
    """
    beg = mh.start + mh.firstresource if first_resource is None else first_resource
    end = len(sect.sectionoffsets) - 1
    if beg >= end:
        return []
    out = []
    for i in range(beg, end):
        data = sect.loadSection(i)
        if data[:4] == b"INDX" or data[:8] == K8_BOUNDARY:
            break
        out.append(data)
    return out


def digest_book(path):
    sect = Sectionizer(path)
    if sect.ident != b"BOOKMOBI" and sect.ident != b"TEXtREAd":
        return {"expect_error": "ErrNotPalmDB"}

    mhlst = [MobiHeader(sect, 0)]
    k8_boundary = -1
    with redirect_stdout(io.StringIO()):
        if not mhlst[0].isK8():
            for i in range(len(sect.sectionoffsets) - 1):
                before, after = sect.sectionoffsets[i:i + 2]
                if after - before == 8 and sect.loadSection(i) == K8_BOUNDARY:
                    mhlst.append(MobiHeader(sect, i + 1))
                    k8_boundary = i
                    break

        digests = []
        encrypted = False
        for mh in mhlst:
            if mh.isEncrypted():
                encrypted = True
                break
            shared = None
            if k8_boundary >= 0 and mh is not mhlst[0]:
                # The combo's KF8 half shares the MOBI6 half's images.
                shared = mhlst[0].start + mhlst[0].firstresource
            with tempfile.TemporaryDirectory() as tmp:
                files = fileNames(path, tmp)
                if mh.isK8():
                    files.makeK8Struct()
                digests.append(half_digest(mh, sect, files, shared))

    if encrypted:
        return {"expect_error": "ErrDRM"}

    if len(digests) == 2:
        return {
            "expect_error": None,
            "kind": "combo",
            "is_kf8": True,
            "has_mobi6_half": True,
            "halves": {"mobi6": digests[0], "kf8": digests[1]},
        }
    d = digests[0]
    return {
        "expect_error": None,
        "kind": d.pop("kind"),
        "is_kf8": mhlst[0].isK8(),
        "has_mobi6_half": False,
        "book": d,
    }


# Refusal fixtures never reach the oracle: they assert mobi-go's typed
# error taxonomy (the matrix's "→ typed errors" row), and KindleUnpack
# merely crashes or tolerates where mobi-go must return a sentinel.
EXPECT_ERROR = {
    "mobi6-drm.mobi": "ErrDRM",
    "corrupt-not-palm-db.bin": "ErrNotPalmDB",
    "corrupt-truncated.mobi": "ErrCorrupt",
    "corrupt-fdst.azw3": "ErrCorrupt",
}


def main():
    os.makedirs(GOLDEN, exist_ok=True)
    if len(sys.argv) > 1:
        names = sys.argv[1:]
    else:
        names = sorted(os.listdir(BOOKS))
    for name in names:
        path = os.path.join(BOOKS, name)
        if not os.path.isfile(path):
            continue
        if name in EXPECT_ERROR:
            digest = {"expect_error": EXPECT_ERROR[name]}
        else:
            try:
                digest = digest_book(path)
            except unpackException as exc:
                digest = {"expect_error": "ErrCorrupt", "oracle_exception": str(exc)}
        out = os.path.join(GOLDEN, os.path.splitext(name)[0] + ".json")
        with open(out, "w", encoding="utf-8") as f:
            json.dump(digest, f, indent=2, sort_keys=True, ensure_ascii=False)
            f.write("\n")
        label = digest.get("expect_error") or digest.get("kind")
        print(f"{out} ({label})")


if __name__ == "__main__":
    main()
