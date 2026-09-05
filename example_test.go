// Runnable examples for the frozen public API. They read the
// committed fixture corpus (a real, public-domain PalmDOC MOBI6 book
// and a synthetic AZW3), so everything here executes in every test
// run, -race included.
package mobi_test

import (
	"errors"
	"fmt"
	"os"
	"strings"

	mobi "github.com/madeofpendletonwool/mobi-go"
)

// Example opens a book from disk and reads its metadata.
func Example() {
	data, err := os.ReadFile("testdata/books/real-alice.mobi")
	if err != nil {
		panic(err)
	}
	book, err := mobi.OpenBytes(data)
	if err != nil {
		panic(err)
	}
	md := book.Metadata()
	fmt.Println(md.Title)
	fmt.Println(md.Authors[0])
	fmt.Println(md.Language)
	// Output:
	// Alice's Adventures in Wonderland
	// Lewis Carroll
	// en
}

// ExampleBook_Sections walks a book's sections — the same interface
// over MOBI6 pagebreak chunks and KF8 reassembled XHTML documents.
func ExampleBook_Sections() {
	data, _ := os.ReadFile("testdata/books/real-alice.mobi")
	book, err := mobi.OpenBytes(data)
	if err != nil {
		panic(err)
	}
	sections := book.Sections()
	start, end := sections[0].ByteRange()
	html, err := sections[0].Load()
	fmt.Println(len(sections), "sections")
	fmt.Printf("section 0 spans [%d, %d)\n", start, end)
	fmt.Println(strings.HasPrefix(html, "<html"))
	// Output:
	// 15 sections
	// section 0 spans [0, 455)
	// true
}

// ExampleBook_TOC walks the table of contents, mapping every entry to
// the section it lands in.
func ExampleBook_TOC() {
	data, _ := os.ReadFile("testdata/books/azw3-kf8.azw3")
	book, err := mobi.OpenBytes(data)
	if err != nil {
		panic(err)
	}
	items, err := book.TOC()
	if err != nil {
		panic(err)
	}
	for _, it := range items {
		target := "unresolved"
		if it.Section >= 0 {
			target = fmt.Sprintf("section %d +%d bytes", it.Section, it.SectionOffset)
		}
		fmt.Printf("%s -> %s\n", it.Label, target)
	}
	// Output:
	// Cover -> section 0 +62 bytes
	// Chapter 1 -> section 1 +201 bytes
	// Chapter 2 -> section 2 +101 bytes
}

// ExampleOpen refuses DRM-protected files with a typed error before
// parsing any content.
func ExampleOpen() {
	data, _ := os.ReadFile("testdata/books/mobi6-drm.mobi")
	_, err := mobi.OpenBytes(data)
	fmt.Println(errors.Is(err, mobi.ErrDRM))
	// Output:
	// true
}
