package fgdb

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// copyGDB copies a sample geodatabase to a temporary directory.
func copyGDB(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
	return dst
}

func pointShape(x, y float64) *ShapeBuffer {
	return NewShapeBuffer(&Geometry{ShapeType: ShapePoint, Points: []Point{{x, y}}})
}

func TestEditRows(t *testing.T) {
	path := copyGDB(t, "Querying.gdb")
	g, err := OpenGeodatabaseForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := g.OpenTable("Cities")
	if err != nil {
		t.Fatal(err)
	}
	if !tbl.IsEditable() {
		t.Fatal("table not editable")
	}
	n, _ := tbl.GetRowCount()

	// Insert a city outside the current extent.
	r, err := tbl.CreateRowObject()
	if err != nil {
		t.Fatal(err)
	}
	must(t, r.SetString("CITY_NAME", "Null Island"))
	must(t, r.SetInteger("POP1990", 1))
	must(t, r.SetGeometry(pointShape(10, -5)))
	must(t, tbl.Insert(r))
	newOID, _ := r.GetOID()
	if newOID != 3150 {
		t.Errorf("new ObjectID %d, want 3150", newOID)
	}

	// Update with a longer value, so that the row moves.
	r, err = tbl.GetRow(1)
	if err != nil {
		t.Fatal(err)
	}
	must(t, r.SetString("CITY_NAME", "Bellingham by the Sea, Washington"))
	must(t, r.SetNull("ELEVATION"))
	must(t, tbl.Update(r))

	// Update a subset of the fields.
	rows, err := tbl.Search("OBJECTID, POP1990", "OBJECTID = 3", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err = rows.Next()
	if err != nil {
		t.Fatal(err)
	}
	must(t, r.SetInteger("POP1990", 42))
	must(t, tbl.Update(r))

	must(t, tbl.DeleteByOID(2))
	if err := tbl.DeleteByOID(2); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	must(t, g.Close())

	// Read back.
	g, err = OpenGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	tbl, err = g.OpenTable("Cities")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := tbl.GetRowCount(); got != n {
		t.Errorf("row count %d, want %d", got, n)
	}
	if _, err := tbl.GetRow(2); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("deleted row: %v", err)
	}
	r, err = tbl.GetRow(1)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := r.GetString("CITY_NAME"); s != "Bellingham by the Sea, Washington" {
		t.Errorf("updated name %q", s)
	}
	if null, _ := r.IsNull("ELEVATION"); !null {
		t.Error("ELEVATION should be NULL")
	}
	if s, _ := r.GetString("STATE_NAME"); s != "Washington" {
		t.Errorf("unchanged field %q", s)
	}
	r, _ = tbl.GetRow(3)
	if v, _ := r.GetInteger("POP1990"); v != 42 {
		t.Errorf("POP1990 = %d", v)
	}
	if s, _ := r.GetString("CITY_NAME"); s == "" {
		t.Error("partial update cleared CITY_NAME")
	}
	r, err = tbl.GetRow(newOID)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := r.GetString("CITY_NAME"); s != "Null Island" {
		t.Errorf("inserted name %q", s)
	}
	sb, _ := r.GetGeometry()
	if pts, _ := sb.Points(); len(pts) != 1 || pts[0] != (Point{10, -5}) {
		t.Errorf("inserted point %v", pts)
	}
	e, _ := tbl.GetExtent()
	if e.XMax != 10 || e.YMin != -5 || e.XMin > -158 {
		t.Errorf("extent %+v", e)
	}
	// The spatial filter must find the new point.
	rows, err = tbl.Search("*", "", &Envelope{XMin: 9, YMin: -6, XMax: 11, YMax: -4}, false)
	if err != nil {
		t.Fatal(err)
	}
	cnt := 0
	for _, err := range rows.All() {
		if err != nil {
			t.Fatal(err)
		}
		cnt++
	}
	if cnt != 1 {
		t.Errorf("spatial search found %d rows", cnt)
	}
}

func TestEditReadOnly(t *testing.T) {
	g, err := OpenGeodatabase("testdata/Querying.gdb")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	tbl, err := g.OpenTable("Cities")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.CreateRowObject(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("CreateRowObject: %v", err)
	}
	r, _ := tbl.GetRow(1)
	if err := tbl.Update(r); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Update: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
