// The top-level fuzz target: whole files through Open and, on
// success, through the read-side API surface. Any input must either
// error or parse — never panic, never hang.
package mobi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzOpen seeds from the whole committed corpus (the coverage matrix
// plus the real books) and a few hand shapes, then exercises every
// public accessor on whatever opens. The watchdog fails the run if a
// mutated input wedges Open — the parse must terminate on any bytes.
func FuzzOpen(f *testing.F) {
	for _, data := range corpusBookFiles(f) {
		f.Add(data)
	}
	f.Add([]byte("BOOKMOBI garbage"))
	f.Add(make([]byte, 78))
	f.Add([]byte{0x00, 'B', 'O', 'O', 'K', 'M', 'O', 'B', 'I'})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Absurd sizes are not interesting (the eager copy is linear)
		// and slow the fuzzer down.
		if len(data) > 4<<20 {
			t.Skip("oversized")
		}
		type result struct{}
		done := make(chan result, 1)
		go func() {
			b, err := OpenBytes(data)
			if err != nil {
				done <- result{}
				return
			}
			_ = b.Metadata()
			_ = b.Text()
			if sections := b.Sections(); len(sections) > 0 {
				_, _ = sections[0].ByteRange()
				_, _ = sections[0].Load()
			}
			_, _ = b.TOC()
			_, _ = b.Guide()
			_, _, _ = b.Resource(0)
			_, _, _ = b.Cover()
			_ = b.IsKF8()
			if b.HasMOBI6Half() {
				if half, err := b.MOBI6Half(); err == nil {
					_ = half.Text()
				}
			}
			if b.IsKF8() {
				for _, s := range b.KF8Sections() {
					_ = s.XHTML()
				}
			}
			done <- result{}
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("Open wedged on a %d-byte input", len(data))
		}
	})
}

// corpusBookFiles reads every fixture under testdata/books for fuzz
// seeds. Missing files are not fatal (a fresh checkout always has
// them committed, but fuzzing a partial tree is still useful).
func corpusBookFiles(t testing.TB) [][]byte {
	t.Helper()
	entries, err := os.ReadDir("testdata/books")
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata/books", e.Name()))
		if err != nil {
			continue
		}
		out = append(out, data)
	}
	return out
}
