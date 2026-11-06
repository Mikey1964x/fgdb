package gdbtable

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

// copyTable copies a .gdbtable and its .gdbtablx to a temporary directory.
func copyTable(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	copyFile(t, src, dst)
	copyFile(t, TablxPath(src), TablxPath(dst))
	return dst
}

func TestFieldDescRoundTrip(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/*.gdb/*.gdbtable")
	for _, fn := range files {
		tb, err := Open(fn)
		if err != nil {
			continue // e.g. compressed (CDF) tables
		}
		want, err := tb.readAt(int(tb.FieldDescLen)+4, tb.FieldDescOffset)
		if err != nil {
			t.Fatal(err)
		}
		// GDAL pads the section with 0xDEADBEEF filler bytes.
		got := tb.encodeFieldDesc()
		if len(got) > len(want) || !bytes.Equal(got[4:], want[4:len(got)]) {
			t.Errorf("%s: field description does not round trip", fn)
		}
		tb.Close()
	}
}

func readAll(t *testing.T, tb *Table) map[int64][]any {
	t.Helper()
	rows := map[int64][]any{}
	for fid := int64(0); ; {
		var err error
		fid, err = tb.NextFID(fid + 1)
		if err != nil {
			t.Fatal(err)
		}
		if fid == 0 {
			break
		}
		v, err := tb.ReadRow(fid)
		if err != nil {
			t.Fatal(err)
		}
		rows[fid] = v
	}
	return rows
}

func TestInsertUpdateDelete(t *testing.T) {
	for _, src := range []string{
		"../../testdata/Querying.gdb/a00000009.gdbtable", // Cities
		"../../testdata/gdal.gdb/a00000009.gdbtable",
	} {
		path := copyTable(t, src)
		tb, err := OpenForUpdate(path)
		if err != nil {
			t.Fatal(err)
		}
		orig := readAll(t, tb)
		n := tb.RowCount()
		first, _ := tb.NextFID(1)
		second, _ := tb.NextFID(first + 1)

		// Insert a copy of the first row, twice.
		fid1, err := tb.InsertRow(orig[first])
		if err != nil {
			t.Fatal(err)
		}
		fid2, err := tb.InsertRow(orig[second])
		if err != nil {
			t.Fatal(err)
		}
		if fid2 != fid1+1 || fid1 != tb.TotalRows+1 {
			t.Errorf("new fids %d, %d (max was %d)", fid1, fid2, tb.TotalRows)
		}
		// Update the first row with the content of the second one (may grow)
		// and the second with the first.
		if err := tb.UpdateRow(first, orig[second]); err != nil {
			t.Fatal(err)
		}
		if err := tb.UpdateRow(second, orig[first]); err != nil {
			t.Fatal(err)
		}
		if err := tb.DeleteRow(fid1); err != nil {
			t.Fatal(err)
		}
		if err := tb.DeleteRow(fid1); err != ErrRowNotFound {
			t.Errorf("second delete: %v", err)
		}
		if err := tb.Close(); err != nil {
			t.Fatal(err)
		}

		tb, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := tb.RowCount(); got != n+1 {
			t.Errorf("row count %d, want %d", got, n+1)
		}
		got := readAll(t, tb)
		want := map[int64][]any{}
		for k, v := range orig {
			want[k] = v
		}
		want[first], want[second] = orig[second], orig[first]
		want[fid2] = orig[second]
		for k := range want {
			if tb.ObjectIDField >= 0 {
				want[k] = append([]any(nil), want[k]...)
				want[k][tb.ObjectIDField] = ObjectID(k)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%d rows, want %d", len(got), len(want))
		}
		for k, v := range want {
			if !reflect.DeepEqual(got[k], v) {
				t.Errorf("row %d: got %v, want %v", k, got[k], v)
			}
		}
		if tb.MaxFID() != fid2 {
			t.Errorf("max fid %d, want %d", tb.MaxFID(), fid2)
		}
		tb.Close()
	}
}

func TestReadOnly(t *testing.T) {
	tb, err := Open("../../testdata/Querying.gdb/a00000009.gdbtable")
	if err != nil {
		t.Fatal(err)
	}
	defer tb.Close()
	if err := tb.DeleteRow(1); err != ErrReadOnly {
		t.Errorf("got %v", err)
	}
}
