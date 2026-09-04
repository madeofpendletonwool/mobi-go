package mobi

import (
	"bytes"
	"errors"
	"testing"

	"github.com/madeofpendletonwool/mobi-go/internal/testutil"
)

func open(t *testing.T, data []byte) (*pdbFile, error) {
	t.Helper()
	return openPDB(bytes.NewReader(data), int64(len(data)))
}

func TestOpenPDBRoundTrip(t *testing.T) {
	records := [][]byte{
		[]byte("MOBIthis is record zero"),
		{}, // empty records are legal (adjacent offsets)
		bytes.Repeat([]byte{0xAB}, 5000),
		[]byte("FDST"),
	}
	data := testutil.Build(records...)

	f, err := open(t, data)
	if err != nil {
		t.Fatalf("openPDB: %v", err)
	}
	if got := f.NumRecords(); got != len(records) {
		t.Fatalf("NumRecords = %d, want %d", got, len(records))
	}
	if got, want := f.Name(), "Test Book"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got := string(f.ident[:]); got != "BOOKMOBI" {
		t.Errorf("ident = %q, want BOOKMOBI", got)
	}
	for i, want := range records {
		got, err := f.Record(i)
		if err != nil {
			t.Fatalf("Record(%d): %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Record(%d) = %d bytes, want %d bytes (byte-equal)", i, len(got), len(want))
		}
	}
	if got, err := f.RecordMagic(0); err != nil || string(got[:]) != "MOBI" {
		t.Errorf("RecordMagic(0) = %q, %v; want MOBI, <nil>", got, err)
	}
	if got, err := f.RecordMagic(3); err != nil || string(got[:]) != "FDST" {
		t.Errorf("RecordMagic(3) = %q, %v; want FDST, <nil>", got, err)
	}
}

func TestOpenPDBLegacyTEXtREAd(t *testing.T) {
	records := [][]byte{[]byte("TEXt"), []byte("old-style database")}
	data := testutil.BuildWith(testutil.PDBConfig{
		Name:    "Legacy",
		Type:    "TEXt",
		Creator: "REAd",
	}, records...)

	f, err := open(t, data)
	if err != nil {
		t.Fatalf("openPDB on TEXt/REAd: %v", err)
	}
	if got, want := f.Name(), "Legacy"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	rec, err := f.Record(1)
	if err != nil || !bytes.Equal(rec, records[1]) {
		t.Errorf("Record(1) = %q, %v", rec, err)
	}
}

func TestOpenPDBNameField(t *testing.T) {
	data := testutil.BuildWith(testutil.PDBConfig{
		Name: "A Very Long Book Name That Goes Past The 32 Byte Limit Surely",
	}, []byte("MOBI"))
	f, err := open(t, data)
	if err != nil {
		t.Fatalf("openPDB: %v", err)
	}
	want := "A Very Long Book Name That Goes" // exactly the 32-byte field
	if got := f.Name(); got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

func TestOpenPDBErrors(t *testing.T) {
	records := [][]byte{[]byte("MOBI"), []byte("0123456789"), []byte("third record")}
	// File layout for these records: header 78, table 24 (tableEnd 102),
	// record data 26 bytes, total 128. Offsets: 102, 106, 116.
	absurd := uint16(0xFFFF)
	zero := uint16(0)

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{
			name: "wrong creator",
			data: testutil.BuildWith(testutil.PDBConfig{Creator: "READ"}, records...),
			want: ErrNotPalmDB,
		},
		{
			name: "wrong type",
			data: testutil.BuildWith(testutil.PDBConfig{Type: "APPL"}, records...),
			want: ErrNotPalmDB,
		},
		{
			name: "shorter than header",
			data: testutil.Build(records...)[:60],
			want: ErrNotPalmDB,
		},
		{
			name: "empty file",
			data: nil,
			want: ErrNotPalmDB,
		},
		{
			name: "truncated into header",
			data: testutil.BuildWith(testutil.PDBConfig{Truncate: 30}, records...),
			want: ErrNotPalmDB,
		},
		{
			name: "zero record count",
			data: testutil.BuildWith(testutil.PDBConfig{NumRecords: &zero}, records...),
			want: ErrCorrupt,
		},
		{
			name: "no records at all",
			data: testutil.Build(),
			want: ErrCorrupt,
		},
		{
			name: "absurd record count",
			data: testutil.BuildWith(testutil.PDBConfig{NumRecords: &absurd}, records...),
			want: ErrCorrupt,
		},
		{
			name: "record table past EOF",
			data: testutil.BuildWith(testutil.PDBConfig{Truncate: 100}, records...),
			want: ErrCorrupt,
		},
		{
			name: "offset beyond file size",
			data: testutil.BuildWith(testutil.PDBConfig{RecordOffsets: map[int]uint32{2: 0xFFFF0000}}, records...),
			want: ErrCorrupt,
		},
		{
			name: "non-monotonic offsets",
			data: testutil.BuildWith(testutil.PDBConfig{RecordOffsets: map[int]uint32{1: 10}}, records...),
			want: ErrCorrupt,
		},
		{
			name: "first offset inside record table",
			data: testutil.BuildWith(testutil.PDBConfig{RecordOffsets: map[int]uint32{0: 40}}, records...),
			want: ErrCorrupt,
		},
		{
			name: "truncated past declared offset",
			data: testutil.BuildWith(testutil.PDBConfig{Truncate: 110}, records...),
			want: ErrCorrupt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := open(t, tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("openPDB error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestOpenPDBRecordRange(t *testing.T) {
	f, err := open(t, testutil.Build([]byte("MOBI"), []byte("two")))
	if err != nil {
		t.Fatalf("openPDB: %v", err)
	}
	for _, i := range []int{-1, 2, 100} {
		if _, err := f.Record(i); !errors.Is(err, ErrRecordRange) {
			t.Errorf("Record(%d) error = %v, want ErrRecordRange", i, err)
		}
		if _, err := f.RecordMagic(i); !errors.Is(err, ErrRecordRange) {
			t.Errorf("RecordMagic(%d) error = %v, want ErrRecordRange", i, err)
		}
	}
}

func TestOpenPDBLastRecordRunsToEOF(t *testing.T) {
	// Declared offsets stop at the last record's start; its data must
	// extend to the end of the buffer exactly.
	last := []byte("the final record, running to EOF")
	f, err := open(t, testutil.Build([]byte("MOBI"), last))
	if err != nil {
		t.Fatalf("openPDB: %v", err)
	}
	got, err := f.Record(f.NumRecords() - 1)
	if err != nil {
		t.Fatalf("Record(last): %v", err)
	}
	if !bytes.Equal(got, last) {
		t.Fatalf("last record = %q, want %q", got, last)
	}
}

func TestOpenPDBShortRead(t *testing.T) {
	// Claim more bytes than the reader holds: the container layer must
	// report corruption, not panic or truncate silently.
	data := testutil.Build([]byte("MOBI"), bytes.Repeat([]byte("x"), 200))
	_, err := openPDB(bytes.NewReader(data), int64(len(data)+512))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("openPDB with inflated size: error = %v, want ErrCorrupt", err)
	}
}

func FuzzPDB(f *testing.F) {
	f.Add(testutil.Build([]byte("MOBI"), []byte("0123456789")))
	f.Add(testutil.Build([]byte("MOBI")))
	f.Add(testutil.BuildWith(testutil.PDBConfig{Creator: "READ"}, []byte("MOBI")))
	f.Add(testutil.BuildWith(testutil.PDBConfig{Truncate: 40}, []byte("MOBI"), []byte("data")))
	f.Add([]byte("BOOKMOBI junk junk junk"))
	f.Add([]byte(nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := openPDB(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		// Whatever the parser accepted must be fully walkable.
		for i := range p.NumRecords() {
			if _, err := p.Record(i); err != nil {
				t.Fatalf("Record(%d) on accepted file: %v", i, err)
			}
			if _, err := p.RecordMagic(i); err != nil {
				t.Fatalf("RecordMagic(%d) on accepted file: %v", i, err)
			}
		}
		if _, err := p.Record(p.NumRecords()); !errors.Is(err, ErrRecordRange) {
			t.Fatalf("Record(count) error = %v, want ErrRecordRange", err)
		}
	})
}
