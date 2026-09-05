// Golden-corpus comparison: every fixture in testdata/books is opened
// with the frozen public API and checked against the oracle digest in
// testdata/golden — digests the KindleUnpack-based tools/oracle-digest.py
// emitted (make regen-golden). The comparison covers the full matrix:
// readable books byte-for-byte (raw text, reassembled KF8 parts,
// metadata, chapter titles, every resource record, the cover), and
// refusal fixtures against their typed sentinels.
package mobi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type goldenMetadata struct {
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Language  string   `json:"language"`
	Publisher string   `json:"publisher"`
	ISBN      string   `json:"isbn"`
}

type goldenHalf struct {
	Kind           string         `json:"kind"`
	Metadata       goldenMetadata `json:"metadata"`
	RawTextSHA256  string         `json:"raw_text_sha256"`
	RawTextLen     int            `json:"raw_text_len"`
	SectionCount   int            `json:"section_count"`
	PartSHA256     []string       `json:"part_sha256"`
	ChapterTitles  []string       `json:"chapter_titles"`
	ResourceSHA256 []string       `json:"resource_sha256"`
	CoverSHA256    string         `json:"cover_sha256"`
}

type golden struct {
	ExpectError string `json:"expect_error"`
	Kind        string `json:"kind"`
	IsKF8       bool   `json:"is_kf8"`
	HasMOBI6    bool   `json:"has_mobi6_half"`
	Book        *goldenHalf
	Halves      map[string]*goldenHalf `json:"halves"`
}

// json fields for the single-book form live under "book".
func (g *golden) UnmarshalJSON(data []byte) error {
	type raw golden
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*g = golden(r)
	var book struct {
		Book *goldenHalf `json:"book"`
	}
	if err := json.Unmarshal(data, &book); err != nil {
		return err
	}
	g.Book = book.Book
	return nil
}

var goldenSentinels = map[string]error{
	"ErrDRM":         ErrDRM,
	"ErrNotPalmDB":   ErrNotPalmDB,
	"ErrCorrupt":     ErrCorrupt,
	"ErrRecordRange": ErrRecordRange,
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestGoldenCorpus(t *testing.T) {
	entries, err := os.ReadDir("testdata/golden")
	if err != nil {
		t.Fatalf("reading testdata/golden: %v", err)
	}
	tested := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/golden", e.Name()))
			if err != nil {
				t.Fatalf("reading golden: %v", err)
			}
			var g golden
			if err := json.Unmarshal(data, &g); err != nil {
				t.Fatalf("parsing golden: %v", err)
			}
			bookPath := findBookFile(t, name)
			bookData, err := os.ReadFile(bookPath)
			if err != nil {
				t.Fatalf("reading book: %v", err)
			}
			b, err := OpenBytes(bookData)
			if g.ExpectError != "" {
				if err == nil {
					t.Fatalf("Open succeeded, want %s", g.ExpectError)
				}
				want := goldenSentinels[g.ExpectError]
				if want == nil {
					t.Fatalf("unknown expected error %q", g.ExpectError)
				}
				if !errors.Is(err, want) {
					t.Fatalf("Open error = %v, want %s", err, g.ExpectError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if b.IsKF8() != g.IsKF8 {
				t.Errorf("IsKF8 = %v, want %v", b.IsKF8(), g.IsKF8)
			}
			if b.HasMOBI6Half() != g.HasMOBI6 {
				t.Errorf("HasMOBI6Half = %v, want %v", b.HasMOBI6Half(), g.HasMOBI6)
			}
			if g.Kind == "combo" {
				kf8 := g.Halves["kf8"]
				m6 := g.Halves["mobi6"]
				if kf8 == nil || m6 == nil {
					t.Fatalf("combo golden missing halves: %v", g.Halves)
				}
				compareGoldenHalf(t, "kf8", kf8, b)
				half, err := b.MOBI6Half()
				if err != nil {
					t.Fatalf("MOBI6Half: %v", err)
				}
				compareGoldenHalf(t, "mobi6", m6, half)
			} else {
				compareGoldenHalf(t, "book", g.Book, b)
			}
		})
		tested++
	}
	// The corpus must cover the matrix; a silent skip is a bug.
	if tested < 11 {
		t.Errorf("golden corpus tested %d fixtures, want at least 11", tested)
	}
}

func findBookFile(t *testing.T, name string) string {
	t.Helper()
	for _, ext := range []string{".mobi", ".azw3", ".bin"} {
		p := filepath.Join("testdata/books", name+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("no book file for golden %s", name)
	return ""
}

func compareGoldenHalf(t *testing.T, label string, g *goldenHalf, b *Book) {
	t.Helper()
	if g == nil {
		t.Fatalf("%s: golden half is nil", label)
	}

	md := b.Metadata()
	if md.Title != g.Metadata.Title {
		t.Errorf("%s: title = %q, want %q", label, md.Title, g.Metadata.Title)
	}
	if strings.Join(md.Authors, "\x00") != strings.Join(g.Metadata.Authors, "\x00") {
		t.Errorf("%s: authors = %q, want %q", label, md.Authors, g.Metadata.Authors)
	}
	if strings.ToLower(md.Language) != g.Metadata.Language {
		t.Errorf("%s: language = %q, want %q", label, md.Language, g.Metadata.Language)
	}
	if md.Publisher != g.Metadata.Publisher {
		t.Errorf("%s: publisher = %q, want %q", label, md.Publisher, g.Metadata.Publisher)
	}
	if md.ISBN != g.Metadata.ISBN {
		t.Errorf("%s: isbn = %q, want %q", label, md.ISBN, g.Metadata.ISBN)
	}

	// MOBI6 halves compare their whole raw markup byte-for-byte — the
	// end-to-end check over trailing-entry stripping, decompression,
	// and concatenation. KF8 halves skip it (the flow is compared per
	// reassembled part below, a strictly stronger cut).
	if g.RawTextSHA256 != "" && !b.IsKF8() {
		raw := b.RawText()
		if len(raw) != g.RawTextLen || sha256Hex(raw) != g.RawTextSHA256 {
			t.Errorf("%s: raw text = (%d bytes, %s), want (%d, %s)",
				label, len(raw), sha256Hex(raw), g.RawTextLen, g.RawTextSHA256)
		}
	}

	sections := b.Sections()
	if len(sections) != g.SectionCount {
		t.Errorf("%s: sections = %d, want %d", label, len(sections), g.SectionCount)
	}
	if len(g.PartSHA256) > 0 {
		kf8Sections := b.KF8Sections()
		if len(kf8Sections) != len(g.PartSHA256) {
			t.Fatalf("%s: KF8 parts = %d, want %d", label, len(kf8Sections), len(g.PartSHA256))
		}
		for i, want := range g.PartSHA256 {
			// Fixtures are UTF-8: the decoded XHTML maps byte-for-byte
			// onto the raw assembly both readers hash.
			if got := sha256Hex([]byte(kf8Sections[i].XHTML())); got != want {
				t.Errorf("%s: KF8 part %d = %s, want %s", label, i, got, want)
			}
		}
	}

	var labels []string
	var walk func([]TOCItem)
	walk = func(items []TOCItem) {
		for _, it := range items {
			labels = append(labels, it.Label)
			walk(it.Children)
		}
	}
	items, err := b.TOC()
	if err != nil {
		t.Fatalf("%s: TOC: %v", label, err)
	}
	walk(items)
	if strings.Join(labels, "\x00") != strings.Join(g.ChapterTitles, "\x00") {
		t.Errorf("%s: chapter titles = %q, want %q", label, labels, g.ChapterTitles)
	}

	if b.NumResources() != len(g.ResourceSHA256) {
		t.Fatalf("%s: resources = %d, want %d", label, b.NumResources(), len(g.ResourceSHA256))
	}
	for i, want := range g.ResourceSHA256 {
		data, _, err := b.Resource(i)
		if err != nil {
			t.Fatalf("%s: resource %d: %v", label, i, err)
		}
		if got := sha256Hex(data); got != want {
			t.Errorf("%s: resource %d = %s, want %s", label, i, got, want)
		}
	}

	cover, _, err := b.Cover()
	if g.CoverSHA256 == "" {
		if !errors.Is(err, ErrNoCover) {
			t.Errorf("%s: cover = (%d bytes, %v), want ErrNoCover", label, len(cover), err)
		}
	} else if err != nil {
		t.Fatalf("%s: cover: %v", label, err)
	} else if got := sha256Hex(cover); got != g.CoverSHA256 {
		t.Errorf("%s: cover = %s, want %s", label, got, g.CoverSHA256)
	}
}
