package fgdb_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Mikey1964x/fgdb"
)

func Example() {
	dir, _ := os.MkdirTemp("", "ex")
	defer os.RemoveAll(dir)
	g, err := fgdb.CreateGeodatabase(filepath.Join(dir, "out.gdb"))
	if err != nil {
		log.Fatal(err)
	}
	defer g.Close()
	tbl, err := g.CreateTable(`\Places`, []fgdb.FieldDef{
		{Name: "SHAPE", Type: fgdb.FieldTypeGeometry, GeometryDef: &fgdb.GeometryDef{
			GeometryType: fgdb.GeometryPoint, SpatialReference: fgdb.SpatialReference{WKID: 4326},
		}},
		{Name: "Name", Type: fgdb.FieldTypeString, Length: 50, IsNullable: true},
	}, "")
	if err != nil {
		log.Fatal(err)
	}
	r, _ := tbl.CreateRowObject()
	r.SetString("Name", "Paris")
	r.SetGeometry(fgdb.NewShapeBuffer(&fgdb.Geometry{
		ShapeType: fgdb.ShapePoint, Points: []fgdb.Point{{X: 2.35, Y: 48.85}},
	}))
	if err := tbl.Insert(r); err != nil {
		log.Fatal(err)
	}
	env := fgdb.NewEnvelope(0, 40, 10, 50)
	rows, err := tbl.Search("Name", "Name LIKE 'P%'", &env, false)
	if err != nil {
		log.Fatal(err)
	}
	for r, err := range rows.All() {
		if err != nil {
			log.Fatal(err)
		}
		name, _ := r.GetString("Name")
		fmt.Println(name)
	}
	// Output: Paris
}
