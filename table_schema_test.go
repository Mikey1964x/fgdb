package fgdb

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func openCities(t *testing.T, path string) (*Geodatabase, *Table) {
	t.Helper()
	g, err := OpenGeodatabaseForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := g.OpenTable(`\Cities`)
	if err != nil {
		t.Fatal(err)
	}
	return g, tbl
}

func searchNames(t *testing.T, tbl *Table, where string) []string {
	t.Helper()
	rows, err := tbl.Search("Name", where, nil, false)
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
	return names
}

func TestAddDeleteIndex(t *testing.T) {
	path := buildSampleGDB(t)
	g, tbl := openCities(t, path)
	must(t, tbl.AddIndex(IndexDef{Name: "NameIdx", Fields: "Name"}))
	must(t, tbl.AddIndex(IndexDef{Name: "PopIdx", Fields: "Population"}))
	for _, c := range []struct {
		def  IndexDef
		want error
	}{
		{IndexDef{Name: "NameIdx", Fields: "Area"}, ErrAlreadyExists},
		{IndexDef{Name: "Other", Fields: "Name"}, ErrAlreadyExists},
		{IndexDef{Name: "X", Fields: "Missing"}, ErrFieldNotFound},
		{IndexDef{Name: "1bad", Fields: "Area"}, ErrInvalidArgument},
		{IndexDef{Name: "Low", Fields: "LOWER(Area)"}, ErrInvalidArgument},
		{IndexDef{Name: "Oid", Fields: "OBJECTID"}, ErrAlreadyExists},
	} {
		if err := tbl.AddIndex(c.def); !errors.Is(err, c.want) {
			t.Errorf("AddIndex(%+v): %v, want %v", c.def, err, c.want)
		}
	}
	if err := tbl.DeleteIndex("FDO_OBJECTID"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("delete ObjectID index: %v", err)
	}
	if err := tbl.DeleteIndex("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing index: %v", err)
	}
	must(t, g.Close())

	g, tbl = openCities(t, path)
	idx, _ := tbl.GetIndexes()
	var names []string
	for _, d := range idx {
		names = append(names, d.Name+"="+d.Fields)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "NameIdx=Name") || !strings.Contains(got, "PopIdx=Population") {
		t.Errorf("indexes %s", got)
	}
	if got := searchNames(t, tbl, "Name = 'Lyon'"); len(got) != 1 {
		t.Errorf("indexed search %v", got)
	}
	// Inserting a row keeps the new index up to date.
	r, _ := tbl.CreateRowObject()
	must(t, r.SetString("Name", "Lille"))
	must(t, tbl.Insert(r))
	must(t, tbl.Flush())
	if got := searchNames(t, tbl, "Name = 'Lille'"); len(got) != 1 {
		t.Errorf("search after insert %v", got)
	}
	atx := tbl.raw.Path[:len(tbl.raw.Path)-len(".gdbtable")] + ".NameIdx.atx"
	if _, err := os.Stat(atx); err != nil {
		t.Errorf("index file: %v", err)
	}
	must(t, tbl.DeleteIndex("nameidx"))
	if _, err := os.Stat(atx); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("index file not removed: %v", err)
	}
	// The field can be indexed again, case-insensitively this time.
	must(t, tbl.AddIndex(IndexDef{Name: "NameLow", Fields: "LOWER(Name)"}))
	must(t, g.Close())
}

func TestAddField(t *testing.T) {
	path := buildSampleGDB(t)
	g, tbl := openCities(t, path)
	must(t, tbl.AddField(FieldDef{Name: "Country", Alias: "Country name", Type: FieldTypeString, Length: 20, DefaultValue: "France"}))
	must(t, tbl.AddField(FieldDef{Name: "Rank", Type: FieldTypeSmallInteger, IsNullable: true}))
	must(t, tbl.AddField(FieldDef{Name: "GUIDField", Type: FieldTypeGUID, IsNullable: true}))
	for _, c := range []struct {
		def  FieldDef
		want error
	}{
		{FieldDef{Name: "country", Type: FieldTypeInteger, IsNullable: true}, ErrAlreadyExists},
		{FieldDef{Name: "Req", Type: FieldTypeInteger}, ErrInvalidArgument},
		{FieldDef{Name: "Shape2", Type: FieldTypeGeometry}, ErrNotSupported},
		{FieldDef{Name: "9x", Type: FieldTypeInteger, IsNullable: true}, ErrInvalidArgument},
	} {
		if err := tbl.AddField(c.def); !errors.Is(err, c.want) {
			t.Errorf("AddField(%s): %v, want %v", c.def.Name, err, c.want)
		}
	}
	must(t, g.Close())

	g, tbl = openCities(t, path)
	defer g.Close()
	fields, _ := tbl.GetFields()
	if len(fields) != 10 || fields[7].Name != "Country" || fields[7].Alias != "Country name" || fields[7].Length != 20 {
		t.Fatalf("fields %+v", fields)
	}
	for oid := int64(1); oid <= 3; oid++ {
		r, err := tbl.GetRow(oid)
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := r.GetString("Country"); s != "France" {
			t.Errorf("row %d Country %q", oid, s)
		}
		if null, _ := r.IsNull("Rank"); !null {
			t.Errorf("row %d Rank not NULL", oid)
		}
		if s, _ := r.GetString("Name"); s == "" {
			t.Errorf("row %d lost its name", oid)
		}
		if sb, err := r.GetGeometry(); err != nil || sb == nil {
			t.Errorf("row %d geometry: %v", oid, err)
		}
	}
	// Indexes and the spatial filter still work after the rewrite.
	if got := searchNames(t, tbl, "Population > 400000"); len(got) != 1 || got[0] != "Lyon" {
		t.Errorf("search %v", got)
	}
	rows, err := tbl.Search("Name", "", &Envelope{XMin: 7, YMin: 43, XMax: 8, YMax: 44}, false)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, err := range rows.All() {
		must(t, err)
		n++
	}
	if n != 1 {
		t.Errorf("spatial search found %d rows", n)
	}
	def, _ := g.GetDatasetDefinition(`\Cities`, DatasetTypeFeatureClass)
	if !strings.Contains(def, "<Name>Country</Name>") || !strings.Contains(def, "<AliasName>City name</AliasName>") {
		t.Errorf("definition %s", def)
	}
}

func TestAddGlobalIDField(t *testing.T) {
	path := buildSampleGDB(t)
	g, err := OpenGeodatabaseForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	notes, err := g.OpenTable(`\Notes`)
	if err != nil {
		t.Fatal(err)
	}
	must(t, notes.AddField(FieldDef{Name: "GID", Type: FieldTypeGlobalID}))
	r, _ := notes.GetRow(1)
	if gid, err := r.GetGlobalID(); err != nil || gid.IsNull() {
		t.Errorf("GlobalID %v %v", gid, err)
	}
}

func TestDeleteField(t *testing.T) {
	path := buildSampleGDB(t)
	g, tbl := openCities(t, path)
	must(t, tbl.AddIndex(IndexDef{Name: "AreaIdx", Fields: "Area"}))
	must(t, tbl.DeleteField("Area"))
	if err := tbl.DeleteField("Area"); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if err := tbl.DeleteField("OBJECTID"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("delete OBJECTID: %v", err)
	}
	if err := tbl.DeleteField("SHAPE"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("delete SHAPE: %v", err)
	}
	must(t, g.Close())

	g, tbl = openCities(t, path)
	defer g.Close()
	fields, _ := tbl.GetFields()
	for _, f := range fields {
		if f.Name == "Area" {
			t.Error("Area still present")
		}
	}
	if len(fields) != 6 {
		t.Errorf("%d fields", len(fields))
	}
	idx, _ := tbl.GetIndexes()
	for _, d := range idx {
		if d.Name == "AreaIdx" {
			t.Error("index on deleted field kept")
		}
	}
	r, _ := tbl.GetRow(2)
	if s, _ := r.GetString("Name"); s != "Lyon" {
		t.Errorf("name %q", s)
	}
	if v, _ := r.GetInteger("Population"); v != 520000 {
		t.Errorf("population %d", v)
	}
	def, _ := g.GetDatasetDefinition(`\Cities`, DatasetTypeFeatureClass)
	if strings.Contains(def, "<Name>Area</Name>") {
		t.Error("definition still lists Area")
	}
}

func TestAlterField(t *testing.T) {
	path := buildSampleGDB(t)
	g, tbl := openCities(t, path)
	must(t, tbl.AlterField(FieldDef{Name: "name", Alias: "Town", Type: FieldTypeString, Length: 100, IsNullable: true}))
	if err := tbl.AlterField(FieldDef{Name: "Name", Type: FieldTypeString, Length: 3, IsNullable: true}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("shorten below longest value: %v", err)
	}
	if err := tbl.AlterField(FieldDef{Name: "Name", Type: FieldTypeInteger, IsNullable: true}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("change type: %v", err)
	}
	// Founded is set on every row, Area is not NULL either, so both can
	// become required.
	must(t, tbl.AlterField(FieldDef{Name: "Area", Type: FieldTypeDouble}))
	must(t, g.Close())

	g, tbl = openCities(t, path)
	defer g.Close()
	fields, _ := tbl.GetFields()
	if f := fields[2]; f.Name != "Name" || f.Alias != "Town" || f.Length != 100 {
		t.Errorf("altered field %+v", f)
	}
	if fields[4].IsNullable {
		t.Errorf("Area still nullable")
	}
	r, err := tbl.CreateRowObject()
	if err != nil {
		t.Fatal(err)
	}
	must(t, r.SetString("Name", strings.Repeat("x", 90)))
	if err := tbl.Insert(r); !errors.Is(err, ErrNotNullable) {
		t.Errorf("insert without Area: %v", err)
	}
	must(t, r.SetDouble("Area", 1))
	must(t, tbl.Insert(r))
}

func TestSchemaChangeOnSharedTable(t *testing.T) {
	path := buildSampleGDB(t)
	g, tbl := openCities(t, path)
	defer g.Close()
	other, err := g.OpenTable(`\Cities`)
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.AddField(FieldDef{Name: "X", Type: FieldTypeInteger, IsNullable: true}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("rewrite while opened twice: %v", err)
	}
	must(t, other.Close())
	must(t, tbl.AddField(FieldDef{Name: "X", Type: FieldTypeInteger, IsNullable: true}))
}

func TestSchemaReadOnly(t *testing.T) {
	g, err := OpenGeodatabase("testdata/Querying.gdb")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	tbl, _ := g.OpenTable("Cities")
	if err := tbl.AddField(FieldDef{Name: "X", Type: FieldTypeInteger, IsNullable: true}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("AddField: %v", err)
	}
	if err := tbl.AddIndex(IndexDef{Name: "X", Fields: "CITY_NAME"}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("AddIndex: %v", err)
	}
}
