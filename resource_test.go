package mobi

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

// Tiny hand-built raster fixtures — enough bytes for magic sniffing and
// byte-identity, no real artwork involved.
var (
	gif1x1 = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff" +
		"\x21\xf9\x04\x01\x00\x00\x00\x00\x2c\x00\x00\x00\x00\x01\x00\x01\x00" +
		"\x00\x02\x02\x44\x01\x00\x3b")
	png1x1 = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00" +
		"\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc" +
		"\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	jpegTiny = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00" +
		"\x01\x00\x00\xff\xd9")
	bmp1x1 = []byte("BM\x3a\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00" +
		"\x00\x00\x01\x00\x00\x00\x01\x00\x00\x00\x01\x00\x18\x00\x00\x00\x00" +
		"\x00\x04\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00" +
		"\x00\x00\x00\x00\x00\x00\x00\x00\x00\xff\x00\x00\x00")
)

func openResourceBook(t *testing.T, cfg testutil.BookConfig) *Book {
	t.Helper()
	data := testutil.BuildBook(cfg)
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

// openResourceKF8Book builds a minimal valid KF8 book around the
// given resources (a v8 book now eagerly reassembles, so it needs a
// real skeleton and fragment index).
func openResourceKF8Book(t *testing.T, resources [][]byte) *Book {
	t.Helper()
	layout, _ := testutil.AuthorKF8([]testutil.KF8Author{{XHTML: "<html><body><p>kf8</p></body></html>"}}, nil)
	data := testutil.BuildKF8(testutil.KF8BookSpec{Layout: layout, Resources: resources}).Data
	b, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

func TestResourceImageFormats(t *testing.T) {
	forms := []struct {
		name string
		data []byte
		mime string
	}{
		{"gif", gif1x1, "image/gif"},
		{"png", png1x1, "image/png"},
		{"jpeg", jpegTiny, "image/jpeg"},
		{"bmp", bmp1x1, "image/bmp"},
		{"raw", []byte("\x00\x01not-an-image"), "application/octet-stream"},
	}
	var resources [][]byte
	for _, f := range forms {
		resources = append(resources, f.data)
	}
	b := openResourceBook(t, testutil.BookConfig{Text: "hello", Resources: resources})

	if got := b.NumResources(); got != len(forms) {
		t.Fatalf("NumResources() = %d, want %d", got, len(forms))
	}
	for i, f := range forms {
		data, mime, err := b.Resource(i)
		if err != nil {
			t.Fatalf("%s: Resource(%d): %v", f.name, i, err)
		}
		if !bytes.Equal(data, f.data) {
			t.Fatalf("%s: Resource(%d) bytes differ from the record", f.name, i)
		}
		if mime != f.mime {
			t.Fatalf("%s: Resource(%d) mime = %q, want %q", f.name, i, mime, f.mime)
		}
	}
	for _, i := range []int{-1, len(forms), 1 << 30} {
		if _, _, err := b.Resource(i); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Resource(%d) error = %v, want ErrRecordRange", i, err)
		}
	}
}

func TestResolveRecindex(t *testing.T) {
	b := openResourceBook(t, testutil.BookConfig{
		Text:      `<html><body><img recindex="00001"><img recindex="00002"></body></html>`,
		Resources: [][]byte{gif1x1, png1x1},
	})

	// recindex is 1-based over the resource run: 1 → first record.
	data, mime, err := b.ResolveRecindex(1)
	if err != nil {
		t.Fatalf("ResolveRecindex(1): %v", err)
	}
	if !bytes.Equal(data, gif1x1) || mime != "image/gif" {
		t.Fatalf("ResolveRecindex(1) = (%d bytes, %q), want the GIF fixture", len(data), mime)
	}
	// mediarecindex uses the same arithmetic.
	if data, _, err = b.ResolveRecindex(2); err != nil || !bytes.Equal(data, png1x1) {
		t.Fatalf("ResolveRecindex(2) = (%d bytes, %v), want the PNG fixture", len(data), err)
	}
	// Boundaries: 0 is below the first resource, 3 one past the last.
	for _, n := range []int{0, -1, 3, 1 << 30} {
		if _, _, err := b.ResolveRecindex(n); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("ResolveRecindex(%d) error = %v, want ErrRecordRange", n, err)
		}
	}
}

func TestResourceAbsent(t *testing.T) {
	b := openResourceBook(t, testutil.BookConfig{Text: "text only"})
	if got := b.NumResources(); got != 0 {
		t.Fatalf("NumResources() = %d, want 0", got)
	}
	if _, _, err := b.Resource(0); !errors.Is(err, ErrRecordRange) {
		t.Fatalf("Resource(0) error = %v, want ErrRecordRange", err)
	}
}

func TestCover(t *testing.T) {
	tests := []struct {
		name    string
		exth    []testutil.EXTHRecord
		want    []byte
		wantMim string
		wantErr error
	}{
		{
			name:    "cover offset",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(201, 1)},
			want:    png1x1,
			wantMim: "image/png",
		},
		{
			name:    "thumbnail fallback",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(202, 0)},
			want:    gif1x1,
			wantMim: "image/gif",
		},
		{
			name:    "sentinel cover offset falls back",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(201, 0xFFFFFFFF), testutil.EXTHUint(202, 1)},
			want:    png1x1,
			wantMim: "image/png",
		},
		{
			name:    "cover offset wins over thumbnail",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(201, 0), testutil.EXTHUint(202, 1)},
			want:    gif1x1,
			wantMim: "image/gif",
		},
		{
			name:    "neither present",
			exth:    []testutil.EXTHRecord{testutil.EXTHString(100, "Author")},
			wantErr: ErrNoCover,
		},
		{
			name:    "both sentinel",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(201, 0xFFFFFFFF), testutil.EXTHUint(202, 0xFFFFFFFF)},
			wantErr: ErrNoCover,
		},
		{
			name:    "no exth at all",
			wantErr: ErrNoCover,
		},
		{
			name:    "cover offset out of range",
			exth:    []testutil.EXTHRecord{testutil.EXTHUint(201, 99)},
			wantErr: ErrRecordRange,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := openResourceBook(t, testutil.BookConfig{
				Text:      "cover me",
				Resources: [][]byte{gif1x1, png1x1},
				EXTH:      tt.exth,
			})
			data, mime, err := b.Cover()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Cover() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Cover(): %v", err)
			}
			if !bytes.Equal(data, tt.want) || mime != tt.wantMim {
				t.Fatalf("Cover() = (%d bytes, %q), want (%d bytes, %q)",
					len(data), mime, len(tt.want), tt.wantMim)
			}
		})
	}
}

// fontPayload builds a deterministic fake font: an OTTO-magic start
// and enough nonzero bytes to cover both XOR extents (1024 and 1040).
func fontPayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*7%251 + 1)
	}
	copy(p, "OTTO")
	return p
}

// buildFontRecord renders a FONT record the decoder must invert:
// deflate when compressed, then XOR when obfuscated (the decoder runs
// XOR first, inflate second), with the key placed before the payload.
// keyOverride, when set, stores a key other than the one the payload
// was obfuscated with — the wrong-key case.
func buildFontRecord(font, key, keyOverride []byte, obfuscate, compress bool) []byte {
	data := append([]byte(nil), font...)
	if compress {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		zw.Write(data)
		zw.Close()
		data = buf.Bytes()
	}
	if obfuscate {
		n := 1040
		if len(key) == 16 {
			n = 1024
		}
		for i := range min(n, len(data)) {
			data[i] ^= key[i%len(key)]
		}
	}
	storedKey := key
	if keyOverride != nil {
		storedKey = keyOverride
	}
	var flags uint32
	if compress {
		flags |= fontFlagCompressed
	}
	if obfuscate {
		flags |= fontFlagObfuscated
	}
	rec := make([]byte, 0, fontHeaderLen+len(storedKey)+len(data))
	rec = append(rec, 'F', 'O', 'N', 'T')
	rec = binary.BigEndian.AppendUint32(rec, uint32(len(font))) // size field, unused
	rec = binary.BigEndian.AppendUint32(rec, flags)
	rec = binary.BigEndian.AppendUint32(rec, uint32(fontHeaderLen+len(storedKey))) // dataStart
	rec = binary.BigEndian.AppendUint32(rec, uint32(len(storedKey)))               // keyLength
	rec = binary.BigEndian.AppendUint32(rec, fontHeaderLen)                        // keyStart
	rec = append(rec, storedKey...)
	rec = append(rec, data...)
	return rec
}

func TestResourceFont(t *testing.T) {
	font := fontPayload(1200)
	key16 := bytes.Repeat([]byte{0xAA}, 16)
	key20 := bytes.Repeat([]byte{0x5A}, 20)
	tests := []struct {
		name      string
		key       []byte
		obfuscate bool
		compress  bool
	}{
		{"obfuscated and compressed, key 16", key16, true, true},
		{"obfuscated and compressed, key 20", key20, true, true},
		{"obfuscated only", key16, true, false},
		{"compressed only", key16, false, true},
		{"neither", key16, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := buildFontRecord(font, tt.key, nil, tt.obfuscate, tt.compress)
			b := openResourceBook(t, testutil.BookConfig{
				Text:      "fonts",
				Resources: [][]byte{rec},
			})
			data, mime, err := b.Resource(0)
			if err != nil {
				t.Fatalf("Resource(0): %v", err)
			}
			if !bytes.Equal(data, font) {
				t.Fatalf("recovered font differs: got %d bytes starting %q, want %d bytes starting %q",
					len(data), data[:4], len(font), font[:4])
			}
			if mime != "application/octet-stream" {
				t.Fatalf("font mime = %q, want application/octet-stream", mime)
			}
		})
	}

	// KF8 books keep their fonts unreadable to the text path but the
	// resource path is record-based and must work there too.
	t.Run("kf8 font", func(t *testing.T) {
		rec := buildFontRecord(font, key16, nil, true, true)
		b := openResourceKF8Book(t, [][]byte{rec})
		if !b.IsKF8() {
			t.Fatal("fixture is not KF8")
		}
		data, _, err := b.Resource(0)
		if err != nil {
			t.Fatalf("Resource(0) on KF8: %v", err)
		}
		if !bytes.Equal(data, font) {
			t.Fatal("KF8 font bytes differ")
		}
	})
}

func TestResourceFontWrongKeyFailsLoud(t *testing.T) {
	font := fontPayload(64)
	good := bytes.Repeat([]byte{0xAA}, 16)
	bad := bytes.Repeat([]byte{0xBB}, 16)
	rec := buildFontRecord(font, good, bad, true, true)
	b := openResourceBook(t, testutil.BookConfig{Text: "fonts", Resources: [][]byte{rec}})
	// The payload was obfuscated with `good` but the record stores
	// `bad`: after the wrong XOR the zlib header cannot validate
	// (first byte 0x78^0xAA^0xBB = 0x69, not a CMF byte), so decoding
	// must fail with a typed corruption error, not return junk.
	if _, _, err := b.Resource(0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong-key font error = %v, want ErrCorrupt", err)
	}
}

func TestResourceFontCorrupt(t *testing.T) {
	key16 := bytes.Repeat([]byte{0x77}, 16)
	font := fontPayload(32)
	tests := []struct {
		name string
		rec  []byte
	}{
		{"shorter than header", []byte("FONT\x00\x00\x00\x00")},
		{"data start past end", func() []byte {
			rec := buildFontRecord(font, key16, nil, false, false)
			binary.BigEndian.PutUint32(rec[fontDataStart:], 1<<20)
			return rec
		}()},
		{"zero key length", func() []byte {
			rec := buildFontRecord(font, key16, nil, true, false)
			binary.BigEndian.PutUint32(rec[fontKeyLength:], 0)
			return rec
		}()},
		{"key past end", func() []byte {
			rec := buildFontRecord(font, key16, nil, true, false)
			binary.BigEndian.PutUint32(rec[fontKeyStart:], 1<<20)
			return rec
		}()},
		{"garbage zlib stream", func() []byte {
			rec := make([]byte, fontHeaderLen)
			copy(rec, "FONT")
			binary.BigEndian.PutUint32(rec[fontFlags:], fontFlagCompressed)
			binary.BigEndian.PutUint32(rec[fontDataStart:], fontHeaderLen)
			rec = append(rec, bytes.Repeat([]byte{0x01}, 16)...)
			return rec
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := openResourceBook(t, testutil.BookConfig{Text: "fonts", Resources: [][]byte{tt.rec}})
			if _, _, err := b.Resource(0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestResourceVideoAudio(t *testing.T) {
	video := append([]byte("VIDE\x00\x00\x00\x01\x00\x00\x00\x02"), []byte("video payload")...)
	audio := append([]byte("AUDI\x00\x00\x00\x03\x00\x00\x00\x04"), []byte("audio payload")...)
	short := []byte("VIDE short")
	b := openResourceBook(t, testutil.BookConfig{
		Text:      "media",
		Resources: [][]byte{video, audio, short},
	})

	data, mime, err := b.Resource(0)
	if err != nil || !bytes.Equal(data, []byte("video payload")) || mime != "application/octet-stream" {
		t.Fatalf("VIDE = (%q, %q, %v), want the 12-byte-stripped payload", data, mime, err)
	}
	if data, mime, err = b.Resource(1); err != nil || !bytes.Equal(data, []byte("audio payload")) || mime != "application/octet-stream" {
		t.Fatalf("AUDI = (%q, %q, %v), want the 12-byte-stripped payload", data, mime, err)
	}
	if _, _, err = b.Resource(2); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short VIDE error = %v, want ErrCorrupt", err)
	}
}

func TestResourceINDXBoundary(t *testing.T) {
	indxRec := append([]byte("INDX"), bytes.Repeat([]byte{0x00}, 8)...)
	flisRec := append([]byte("FLIS"), bytes.Repeat([]byte{0x11}, 8)...)
	resources := [][]byte{gif1x1, png1x1}

	t.Run("indx header field bounds the run", func(t *testing.T) {
		b := openResourceBook(t, testutil.BookConfig{
			Text:            "book",
			Resources:       resources,
			TrailingRecords: [][]byte{indxRec, indxRec},
			// record 0 + 1 text record + 2 resources = first INDX at 4
			Indx: testutil.U32(4),
		})
		if got := b.NumResources(); got != 2 {
			t.Fatalf("NumResources() = %d, want 2", got)
		}
		if _, _, err := b.Resource(2); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Resource(2) error = %v, want ErrRecordRange", err)
		}
	})

	t.Run("indx magic probed without the header field", func(t *testing.T) {
		b := openResourceBook(t, testutil.BookConfig{
			Text:            "book",
			Resources:       resources,
			TrailingRecords: [][]byte{indxRec},
		})
		if got := b.NumResources(); got != 2 {
			t.Fatalf("NumResources() = %d, want 2 (trailing INDX probed away)", got)
		}
		if _, _, err := b.Resource(2); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Resource(2) error = %v, want ErrRecordRange", err)
		}
	})

	t.Run("unclaimed trailing records stay resources", func(t *testing.T) {
		b := openResourceBook(t, testutil.BookConfig{
			Text:            "book",
			Resources:       resources,
			TrailingRecords: [][]byte{flisRec},
		})
		if got := b.NumResources(); got != 3 {
			t.Fatalf("NumResources() = %d, want 3 (FLIS is unclaimed)", got)
		}
		data, _, err := b.Resource(2)
		if err != nil || !bytes.Equal(data, flisRec) {
			t.Fatalf("Resource(2) = (%d bytes, %v), want the raw FLIS record", len(data), err)
		}
	})
}

func TestSectionHTML(t *testing.T) {
	text := `<html><body><img recindex="00001"><mbp:pagebreak><p>chapter two</p></body></html>`
	b := openResourceBook(t, testutil.BookConfig{
		Text:      text,
		Resources: [][]byte{gif1x1},
	})

	sections := b.Sections()
	if len(sections) != 2 {
		t.Fatalf("len(Sections()) = %d, want 2", len(sections))
	}
	for i, s := range sections {
		html := s.HTML(b)
		if html != decodeString(b.mobi.Encoding, b.RawText()[s.Start:s.End]) {
			t.Fatalf("section %d HTML is not the decoded raw slice", i)
		}
		// Decoding must not shift anything: UTF-8 sections map
		// byte-for-byte, so len(html) == End-Start.
		if len(html) != s.End-s.Start {
			t.Fatalf("section %d len(HTML) = %d, want %d", i, len(html), s.End-s.Start)
		}
	}
	if want := `<html><body><img recindex="00001">`; sections[0].HTML(b)[:len(want)] != want {
		t.Fatalf("section 0 HTML starts %q, want the raw recindex attribute left in place",
			sections[0].HTML(b)[:len(want)])
	}

	// Out-of-range sections clamp instead of panicking.
	if got := (Section{Start: 1 << 30, End: 1<<30 + 4}).HTML(b); got != "" {
		t.Fatalf("bogus section HTML = %q, want empty", got)
	}
	if got := (Section{Start: 4, End: 2}).HTML(b); got != "" {
		t.Fatalf("inverted section HTML = %q, want empty", got)
	}

	// KF8 books do not load MOBI6 text; HTML yields "".
	kf8 := openResourceKF8Book(t, [][]byte{gif1x1})
	if got := (Section{Start: 0, End: 4}).HTML(kf8); got != "" {
		t.Fatalf("KF8 section HTML = %q, want empty", got)
	}
}

func FuzzResource(f *testing.F) {
	font := fontPayload(1200)
	key16 := bytes.Repeat([]byte{0x99}, 16)
	f.Add(gif1x1)
	f.Add(png1x1)
	f.Add(buildFontRecord(font, key16, nil, true, true))
	f.Add(buildFontRecord(font, key16, nil, true, false))
	f.Add([]byte("FONT short"))
	f.Add([]byte("VIDE\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("AUDI"))
	f.Add([]byte(nil))
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Fuzz(func(t *testing.T, rec []byte) {
		data := testutil.BuildBook(testutil.BookConfig{
			Text:      "x",
			Resources: [][]byte{rec},
			EXTH:      []testutil.EXTHRecord{testutil.EXTHUint(201, 0)},
		})
		b, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		for i := range b.NumResources() {
			_, mime, err := b.Resource(i)
			if err == nil {
				if mime == "" {
					t.Fatalf("Resource(%d) succeeded with empty mime", i)
				}
				continue
			}
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
				t.Fatalf("Resource(%d) error = %v, want a typed sentinel", i, err)
			}
		}
		// One past the end must error rather than panic.
		if _, _, err := b.Resource(b.NumResources()); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Resource(count) error = %v, want ErrRecordRange", err)
		}
		// recindex resolution and the cover path share the machinery.
		if _, _, err := b.ResolveRecindex(1); err != nil &&
			!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrRecordRange) {
			t.Fatalf("ResolveRecindex(1) error = %v, want a typed sentinel", err)
		}
		if _, _, err := b.ResolveRecindex(0); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("ResolveRecindex(0) error = %v, want ErrRecordRange", err)
		}
		if _, _, err := b.Cover(); err != nil && !errors.Is(err, ErrCorrupt) &&
			!errors.Is(err, ErrRecordRange) && !errors.Is(err, ErrNoCover) {
			t.Fatalf("Cover() error = %v, want a typed sentinel", err)
		}
	})
}
