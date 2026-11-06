package fgdb

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cityFields(wkid int) []FieldDef {
	return []FieldDef{
		{Name: "OBJECTID", Type: FieldTypeOID},
		{Name: "SHAPE", Type: FieldTypeGeometry, IsNullable: true, GeometryDef: &GeometryDef{
			GeometryType: GeometryPoint, SpatialReference: SpatialReference{WKID: wkid},
		}},
		{Name: "Name", Alias: "City name", Type: FieldTypeString, Length: 50, IsNullable: true},
		{Name: "Population", Type: FieldTypeInteger, IsNullable: true, DefaultValue: int32(0)},
		{Name: "Area", Type: FieldTypeDouble, IsNullable: true},
		{Name: "Founded", Type: FieldTypeDate, IsNullable: true},
		{Name: "GID", Type: FieldTypeGlobalID},
	}
}

// buildSampleGDB creates a geodatabase with a feature dataset, two feature
// classes and a table, and returns its path.
func buildSampleGDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "new.gdb")
	g, err := CreateGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, g.CreateFeatureDataset(`\Transport`, SpatialReference{WKID: 3857}))

	cities, err := g.CreateTable(`\Cities`, cityFields(4326), "")
	if err != nil {
		t.Fatal(err)
	}
	founded := time.Date(1850, 3, 4, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		name string
		x, y float64
		pop  int32
	}{{"Paris", 2.35, 48.85, 2100000}, {"Lyon", 4.83, 45.76, 520000}, {"Nice", 7.26, 43.7, 340000}} {
		r, err := cities.CreateRowObject()
		if err != nil {
			t.Fatal(err)
		}
		must(t, r.SetString("Name", c.name))
		if i > 0 {
			must(t, r.SetInteger("Population", c.pop))
		}
		must(t, r.SetDouble("Area", float64(i)*1.5))
		must(t, r.SetDate("Founded", founded))
		must(t, r.SetGeometry(pointShape(c.x, c.y)))
		must(t, cities.Insert(r))
	}
	must(t, cities.Close())

	roads, err := g.CreateTable(`\Transport\Roads`, []FieldDef{
		{Name: "Shape", Type: FieldTypeGeometry, GeometryDef: &GeometryDef{GeometryType: GeometryPolyline, HasZ: true}},
		{Name: "Lanes", Type: FieldTypeSmallInteger, IsNullable: true},
	}, ConfigMaxFileSize4GB)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := roads.CreateRowObject()
	must(t, r.SetShort("Lanes", 2))
	must(t, r.SetGeometry(NewShapeBuffer(&Geometry{
		ShapeType: ShapePolylineZ,
		Points:    []Point{{0, 0}, {1000, 500}, {2000, 0}},
		Parts:     []int32{0},
		Zs:        []float64{10, 20, 30},
	})))
	must(t, roads.Insert(r))
	must(t, roads.Close())

	notes, err := g.CreateTable(`\Notes`, []FieldDef{
		{Name: "Text", Type: FieldTypeString, IsNullable: true},
		{Name: "Big", Type: FieldTypeBigInteger, IsNullable: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = notes.CreateRowObject()
	must(t, r.SetString("Text", "héllo"))
	must(t, r.SetBigInteger("Big", 1<<40))
	must(t, notes.Insert(r))
	must(t, g.Close())
	return path
}

func TestCreateGeodatabase(t *testing.T) {
	path := buildSampleGDB(t)
	g, err := OpenGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	fcs, _ := g.GetChildDatasets(`\`, DatasetTypeFeatureClass)
	tables, _ := g.GetChildDatasets(`\`, DatasetTypeTable)
	fds, _ := g.GetChildDatasets(`\`, DatasetTypeFeatureDataset)
	inFDS, _ := g.GetChildDatasets(`\Transport`, DatasetTypeFeatureClass)
	if len(fcs) != 1 || fcs[0] != `\Cities` || len(tables) != 1 || tables[0] != `\Notes` ||
		len(fds) != 1 || fds[0] != `\Transport` || len(inFDS) != 1 || inFDS[0] != `\Transport\Roads` {
		t.Errorf("datasets: fc %v, tables %v, fds %v, in fds %v", fcs, tables, fds, inFDS)
	}

	cities, err := g.OpenTable(`\Cities`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := cities.GetRowCount(); n != 3 {
		t.Errorf("rows = %d", n)
	}
	fields, _ := cities.GetFields()
	if len(fields) != 7 || fields[2].Alias != "City name" || fields[2].Length != 50 {
		t.Errorf("fields %+v", fields)
	}
	if gd := fields[1].GeometryDef; gd == nil || gd.SpatialReference.WKID != 4326 {
		t.Errorf("geometry def %+v", gd)
	}
	r, _ := cities.GetRow(1)
	if pop, _ := r.GetInteger("Population"); pop != 0 {
		t.Errorf("default population %d", pop)
	}
	if gid, err := r.GetGlobalID(); err != nil || gid.IsNull() {
		t.Errorf("GlobalID %v %v", gid, err)
	}
	rows, err := cities.Search("Name", "Population > 400000", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for r, err := range rows.All() {
		if err != nil {
			t.Fatal(err)
		}
		s, _ := r.GetString("Name")
		names = append(names, s)
	}
	if len(names) != 1 || names[0] != "Lyon" {
		t.Errorf("search: %v", names)
	}
	e, _ := cities.GetExtent()
	if math.Abs(e.XMin-2.35) > 1e-9 || math.Abs(e.YMax-48.85) > 1e-9 {
		t.Errorf("extent %+v", e)
	}

	roads, err := g.OpenTable(`\Transport\Roads`)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ = roads.GetFields()
	if fields[0].Name != "OBJECTID" || fields[1].GeometryDef.SpatialReference.WKID != 3857 {
		t.Errorf("roads fields %+v %+v", fields[0], fields[1].GeometryDef)
	}
	r, _ = roads.GetRow(1)
	sb, _ := r.GetGeometry()
	if zs, _ := sb.Zs(); len(zs) != 3 || zs[2] != 30 {
		t.Errorf("Z values %v", zs)
	}
	notes, _ := g.OpenTable("Notes")
	r, _ = notes.GetRow(1)
	if s, _ := r.GetString("Text"); s != "héllo" {
		t.Errorf("text %q", s)
	}
	if v, _ := r.GetBigInteger("Big"); v != 1<<40 {
		t.Errorf("big %d", v)
	}
}

func TestCreateTableErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.gdb")
	g, err := CreateGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := CreateGeodatabase(path); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("create twice: %v", err)
	}
	tbl, err := g.CreateTable(`\T`, []FieldDef{{Name: "A", Type: FieldTypeInteger, IsNullable: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	tbl.Close()
	for _, c := range []struct {
		path   string
		fields []FieldDef
		want   error
	}{
		{`\T`, nil, ErrAlreadyExists},
		{`\t`, nil, ErrAlreadyExists},
		{`\1abc`, nil, ErrInvalidArgument},
		{`\GDB_Foo`, nil, ErrInvalidArgument},
		{`\NoParent\X`, nil, ErrNotFound},
		{`\X`, []FieldDef{{Name: "A", Type: FieldTypeInteger}, {Name: "a", Type: FieldTypeInteger}}, ErrInvalidArgument},
		{`\X`, []FieldDef{{Name: "S", Type: FieldTypeGeometry}}, ErrInvalidArgument},
	} {
		if _, err := g.CreateTable(c.path, c.fields, ""); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.path, err, c.want)
		}
	}
}

func TestDeleteAndRename(t *testing.T) {
	path := buildSampleGDB(t)
	g, err := OpenGeodatabaseForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, g.Rename(`\Cities`, DatasetTypeFeatureClass, "Towns"))
	must(t, g.Rename(`\Transport`, DatasetTypeFeatureDataset, "Network"))
	must(t, g.Delete(`\Notes`, DatasetTypeTable))
	must(t, g.Close())

	g, err = OpenGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.OpenTable(`\Cities`); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name: %v", err)
	}
	towns, err := g.OpenTable(`\Towns`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := towns.GetRowCount(); n != 3 {
		t.Errorf("towns rows %d", n)
	}
	if _, err := g.OpenTable(`\Network\Roads`); err != nil {
		t.Errorf("roads after renaming dataset: %v", err)
	}
	if _, err := g.OpenTable(`\Notes`); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted table: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(path, "*"))
	for _, f := range files {
		if fi, _ := os.Stat(f); fi.Size() == 0 {
			t.Errorf("empty file %s", f)
		}
	}
	def, _ := g.GetDatasetDefinition(`\Towns`, DatasetTypeFeatureClass)
	if !strings.Contains(def, `<CatalogPath>\Towns</CatalogPath>`) {
		t.Errorf("definition not updated: %.200s", def)
	}
}
