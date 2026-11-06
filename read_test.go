package fgdb

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTestGDB(t *testing.T, name string) *Geodatabase {
	t.Helper()
	g, err := OpenGeodatabase("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func openTestTable(t *testing.T, g *Geodatabase, path string) *Table {
	t.Helper()
	tbl, err := g.OpenTable(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tbl.Close() })
	return tbl
}

func collect(t *testing.T, rows *EnumRows) []*Row {
	t.Helper()
	var out []*Row
	for r, err := range rows.All() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestCatalog(t *testing.T) {
	g := openTestGDB(t, "gdal.gdb")
	fcs, err := g.GetChildDatasets(`\`, DatasetTypeFeatureClass)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`\points_zm`, `\polygons`, `\multipatch`} {
		if !slices.Contains(fcs, want) {
			t.Errorf("GetChildDatasets(root, Feature Class) = %v, missing %s", fcs, want)
		}
	}
	tables, _ := g.GetChildDatasets(`\`, DatasetTypeTable)
	if !slices.Equal(tables, []string{`\alltypes`}) {
		t.Errorf("tables = %v", tables)
	}
	fds, _ := g.GetChildDatasets(`\`, DatasetTypeFeatureDataset)
	if !slices.Equal(fds, []string{`\fds`}) {
		t.Errorf("feature datasets = %v", fds)
	}
	inFds, _ := g.GetChildDatasets(`\fds`, "")
	if !slices.Equal(inFds, []string{`\fds\in_dataset`}) {
		t.Errorf("children of fds = %v", inFds)
	}
	// "Dataset" is an ancestor type of feature classes and tables.
	all, _ := g.GetChildDatasets(`\`, "Dataset")
	if len(all) != len(fcs)+len(tables)+len(fds) {
		t.Errorf("Dataset children = %v", all)
	}
	def, err := g.GetDatasetDefinition(`\polygons`, DatasetTypeFeatureClass)
	if err != nil || !strings.Contains(def, "DEFeatureClassInfo") {
		t.Errorf("GetDatasetDefinition = %.80q, %v", def, err)
	}
	if _, err := g.GetDatasetDefinition(`\nope`, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing dataset: err = %v", err)
	}
	types, _ := g.GetDatasetTypes()
	if !slices.Contains(types, DatasetTypeFeatureClass) {
		t.Errorf("GetDatasetTypes = %v", types)
	}
	rel, _ := g.GetRelatedDatasets(`\fds\in_dataset`, "DatasetInFeatureDataset", "")
	if !slices.Equal(rel, []string{`\fds`}) {
		t.Errorf("GetRelatedDatasets = %v", rel)
	}
	tbl := openTestTable(t, g, `\fds\in_dataset`)
	if tbl.Path() != `\fds\in_dataset` || tbl.Name() != "in_dataset" {
		t.Errorf("Path/Name = %s %s", tbl.Path(), tbl.Name())
	}
	if _, err := g.OpenTable("in_dataset"); err != nil {
		t.Errorf("OpenTable by name: %v", err)
	}
}

func TestDomains(t *testing.T) {
	g := openTestGDB(t, "Domains.gdb")
	d, err := g.GetDomains()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(d)
	if !slices.Equal(d, []string{"MedianType", "RoadSurfaceType", "SpeedLimit"}) {
		t.Errorf("GetDomains = %v", d)
	}
	def, err := g.GetDomainDefinition("speedlimit")
	if err != nil || !strings.Contains(def, "RangeDomain") {
		t.Errorf("GetDomainDefinition = %.100q, %v", def, err)
	}
}

func TestFieldsAndSpatialReference(t *testing.T) {
	g := openTestGDB(t, "Querying.gdb")
	tbl := openTestTable(t, g, `\Cities`)
	fields, _ := tbl.GetFields()
	var shape *FieldDef
	for i := range fields {
		if fields[i].Type == FieldTypeGeometry {
			shape = &fields[i]
		}
	}
	if shape == nil || shape.GeometryDef == nil {
		t.Fatal("no geometry field")
	}
	gd := shape.GeometryDef
	sr := gd.SpatialReference
	if gd.GeometryType != GeometryPoint || gd.HasZ || gd.HasM || sr.WKID != 4269 ||
		!strings.HasPrefix(sr.WKT, `GEOGCS["GCS_North_American_1983"`) || sr.FalseX != -400 {
		t.Errorf("geometry def = %+v", gd)
	}
	fi, _ := tbl.GetFieldInformation()
	if fi.FieldCount() != len(fields) {
		t.Errorf("FieldCount = %d", fi.FieldCount())
	}
	if n, _ := fi.FieldName(3); n != "CITY_NAME" {
		t.Errorf("FieldName(3) = %s", n)
	}
	if l, _ := fi.FieldLength(3); l != 40 {
		t.Errorf("FieldLength(3) = %d", l)
	}
	idx, _ := tbl.GetIndexes()
	if len(idx) != 2 || idx[0].Name != "FDO_OBJECTID" || idx[1].Fields != "Shape" {
		t.Errorf("GetIndexes = %+v", idx)
	}
	ext, _ := tbl.GetExtent()
	if ext.XMin > -158 || ext.XMax < -68 {
		t.Errorf("GetExtent = %+v", ext)
	}
	// Alias from the item definition.
	for _, f := range fields {
		if f.Name == "ELEVATION" && f.Alias != "Elevation" {
			t.Errorf("alias of ELEVATION = %q", f.Alias)
		}
	}
}

func TestSearch(t *testing.T) {
	g := openTestGDB(t, "Querying.gdb")
	tbl := openTestTable(t, g, "Cities")
	rows, err := tbl.Search("OBJECTID, CITY_NAME, POP1990", "STATE_NAME = 'California' AND POP1990 >= 500000", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if f := rows.GetFields(); len(f) != 3 || f[1].Name != "CITY_NAME" {
		t.Errorf("fields = %+v", f)
	}
	var names []string
	for _, r := range collect(t, rows) {
		n, _ := r.GetString("city_name")
		names = append(names, n)
		if _, err := r.GetString("STATE_NAME"); !errors.Is(err, ErrFieldNotFound) {
			t.Errorf("field outside subfields: err = %v", err)
		}
	}
	slices.Sort(names)
	want := []string{"Los Angeles", "San Diego", "San Francisco", "San Jose"}
	if !slices.Equal(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}

	// Spatial filter: cities in a box around Denver.
	env := NewEnvelope(-105.2, 39.5, -104.6, 40)
	rows, err = tbl.Search("CITY_NAME", "", &env, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	n := 0
	for r, err := range rows.All() {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if s, _ := r.GetString("CITY_NAME"); s == "Denver" {
			found = true
		}
	}
	if !found || n > 50 {
		t.Errorf("spatial search: %d rows, Denver found = %v", n, found)
	}

	for _, where := range []string{
		"CITY_NAME LIKE 'San %' AND NOT STATE_NAME IN ('California', 'Texas')",
		"POP1990 BETWEEN 1000000 AND 2000000",
		"UPPER(CITY_NAME) = 'BOSTON' OR CITY_NAME IS NULL",
		"ELEVATION + 1 > 5000 / 2",
	} {
		rows, err := tbl.Search("*", where, nil, true)
		if err != nil {
			t.Errorf("%s: %v", where, err)
			continue
		}
		if len(collect(t, rows)) == 0 {
			t.Errorf("%s: no rows", where)
		}
	}
	for _, where := range []string{"POP1990 >", "NOPE = 1", "CITY_NAME = 'x"} {
		if _, err := tbl.Search("*", where, nil, true); err == nil {
			t.Errorf("%s: expected an error", where)
		}
	}
}

func TestEnumRowsNextAndRecycling(t *testing.T) {
	g := openTestGDB(t, "Querying.gdb")
	tbl := openTestTable(t, g, "Cities")
	rows, _ := tbl.Search("OBJECTID", "OBJECTID <= 3", nil, true)
	var first *Row
	var oids []int64
	for {
		r, err := rows.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = r
		} else if r != first {
			t.Error("recycling should reuse the row")
		}
		oid, _ := r.GetOID()
		oids = append(oids, oid)
	}
	if !slices.Equal(oids, []int64{1, 2, 3}) {
		t.Errorf("oids = %v", oids)
	}
	rows.Close()
	if _, err := rows.Next(); err != io.EOF {
		t.Errorf("Next after Close: %v", err)
	}
}

func TestRowAccessors(t *testing.T) {
	g := openTestGDB(t, "gdal.gdb")
	tbl := openTestTable(t, g, "alltypes")
	r, err := tbl.GetRow(1)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.GetShort("f_int16"); v != 1 {
		t.Errorf("GetShort = %v", v)
	}
	if v, _ := r.GetInteger("f_int16"); v != 1 {
		t.Errorf("GetInteger(int16) = %v", v)
	}
	if v, _ := r.GetBigInteger("f_int64"); v != 1 {
		t.Errorf("GetBigInteger = %v", v)
	}
	if v, _ := r.GetFloat("f_float32"); v != 1.5 {
		t.Errorf("GetFloat = %v", v)
	}
	if v, _ := r.GetDouble("f_float64"); v != 1.0/3 {
		t.Errorf("GetDouble = %v", v)
	}
	if v, _ := r.GetDate("f_date"); !v.Equal(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("GetDate(date only) = %v", v)
	}
	if v, _ := r.GetDate("f_datetime"); v.Year() != 2020 || v.Nanosecond() != 678e6 {
		t.Errorf("GetDate(timestamp offset) = %v", v)
	}
	if _, err := r.GetString("f_int32"); !errors.Is(err, ErrTypeMismatch) {
		t.Errorf("GetString on int: err = %v", err)
	}
	if _, err := r.GetGeometry(); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("GetGeometry on a table: err = %v", err)
	}
	r4, _ := tbl.GetRow(4)
	if null, _ := r4.IsNull("f_string"); !null {
		t.Error("row 4 f_string should be NULL")
	}
	if _, err := r4.GetString("f_string"); !errors.Is(err, ErrNullValue) {
		t.Errorf("GetString on NULL: err = %v", err)
	}
	if _, err := tbl.GetRow(99); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("GetRow(99): err = %v", err)
	}
}

func TestGeometryTypes(t *testing.T) {
	g := openTestGDB(t, "gdal.gdb")
	get := func(layer string, oid int64) *Geometry {
		t.Helper()
		tbl := openTestTable(t, g, layer)
		r, err := tbl.GetRow(oid)
		if err != nil {
			t.Fatal(err)
		}
		sb, err := r.GetGeometry()
		if err != nil {
			t.Fatal(err)
		}
		geo, err := sb.Geometry()
		if err != nil {
			t.Fatal(err)
		}
		return geo
	}
	p := get("points_zm", 1)
	if !p.ShapeType.HasZs() || !p.ShapeType.HasMs() || p.Points[0] != (Point{1.5, 2.5}) || p.Zs[0] != 10 || p.Ms[0] != 100 {
		t.Errorf("point ZM = %+v", p)
	}
	l := get("lines_m", 2)
	if l.ShapeType.GeometryType() != GeometryPolyline || !l.ShapeType.HasMs() || len(l.Parts) != 2 || l.Ms[4] != 5 {
		t.Errorf("polyline M = %+v", l)
	}
	c := get("curves_line", 1)
	if !c.ShapeType.HasCurves() || len(c.Curves) != 1 || c.Curves[0].Type != CurveTypeCircularArc || len(c.Points) != 3 {
		t.Errorf("curve = %+v", c)
	}
	mp := get("multipatch", 1)
	if mp.ShapeType.GeometryType() != GeometryMultiPatch || len(mp.Points) != 6 || len(mp.PartTypes) != 1 || mp.Zs[5] != 3 {
		t.Errorf("multipatch = %+v", mp)
	}
	poly := get("polygons", 2)
	sb := NewShapeBuffer(poly)
	ext, _ := sb.Extent()
	if len(poly.Parts) != 3 || ext != (Envelope{XMax: 100, YMax: 100}) {
		t.Errorf("polygon with holes: parts %v extent %+v", poly.Parts, ext)
	}
	// Null geometry.
	tbl := openTestTable(t, g, "points_zm")
	r, _ := tbl.GetRow(3)
	if _, err := r.GetGeometry(); !errors.Is(err, ErrNullValue) {
		t.Errorf("null geometry: err = %v", err)
	}
}

func TestExecuteSQL(t *testing.T) {
	g := openTestGDB(t, "Querying.gdb")
	rows, err := g.ExecuteSQL("SELECT COUNT(*) AS n, MIN(POP1990), MAX(POP1990), AVG(ELEVATION) FROM Cities WHERE STATE_NAME = 'Texas'", true)
	if err != nil {
		t.Fatal(err)
	}
	all := collect(t, rows)
	if len(all) != 1 {
		t.Fatalf("got %d rows", len(all))
	}
	if n, _ := all[0].GetInteger("n"); n == 0 {
		t.Errorf("COUNT(*) = %d", n)
	}
	vals, _ := all[0].Values()
	if vals[1].(int32) > vals[2].(int32) {
		t.Errorf("MIN > MAX: %v", vals)
	}

	rows, err = g.ExecuteSQL(`SELECT CITY_NAME, POP1990 * 2 AS dbl FROM Cities WHERE CITY_NAME LIKE 'Bos%' ORDER BY POP1990 DESC`, false)
	if err != nil {
		t.Fatal(err)
	}
	all = collect(t, rows)
	if len(all) == 0 {
		t.Fatal("no rows")
	}
	name, _ := all[0].GetString("CITY_NAME")
	dbl, _ := all[0].GetInteger("dbl")
	if name != "Boston" || dbl != 574283*2 {
		t.Errorf("first row = %s %d", name, dbl)
	}

	rows, err = g.ExecuteSQL("SELECT DISTINCT STATE_NAME FROM Cities ORDER BY STATE_NAME", false)
	if err != nil {
		t.Fatal(err)
	}
	states := collect(t, rows)
	if len(states) < 48 || len(states) > 52 {
		t.Errorf("%d distinct states", len(states))
	}

	rows, err = g.ExecuteSQL("SELECT * FROM CitiesInfo WHERE OBJECTID < 3", true)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(collect(t, rows)); n != 2 {
		t.Errorf("SELECT *: %d rows", n)
	}

	if _, err := g.ExecuteSQL("SELECT * FROM Nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown table: err = %v", err)
	}
	if _, err := g.ExecuteSQL("SELEC * FROM Cities", true); !errors.Is(err, ErrInvalidSQL) {
		t.Errorf("bad SQL: err = %v", err)
	}
	if _, err := g.OpenTable("CitiesCDF"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("compressed table: err = %v", err)
	}
}

func TestGuid(t *testing.T) {
	s := "{B286C06B-0879-11D2-AACA-00C04FA33C20}"
	g, err := ParseGuid(s)
	if err != nil || g.String() != s {
		t.Errorf("ParseGuid round trip: %v %v", g, err)
	}
	if _, err := ParseGuid("nope"); err == nil {
		t.Error("expected error")
	}
	a, b := NewGuid(), NewGuid()
	if a == b || a.IsNull() {
		t.Error("NewGuid should be random")
	}
	if s := a.String(); s[15] != '4' || !strings.ContainsRune("89AB", rune(s[20])) {
		t.Errorf("NewGuid %s is not a version 4 UUID", s)
	}
}
