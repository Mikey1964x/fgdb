# fgdb

Package `fgdb` reads and writes Esri File Geodatabases (`.gdb` folders) in
pure Go, with no cgo and no dependency on the Esri or GDAL libraries.

The API follows the [Esri File Geodatabase API 1.5](https://github.com/Esri/file-geodatabase-api)
(`Geodatabase`, `Table`, `Row`, `EnumRows`, `ShapeBuffer`, `FieldDef`, …),
adapted to Go conventions: methods return values and errors instead of
output parameters, and errors can be tested with `errors.Is` against
`ErrNotFound`, `ErrReadOnly`, `ErrNotNullable` and the other values in
[errors.go](errors.go). The file format is the one documented by
[the FGDB spec](https://github.com/rouault/dump_gdbtable/wiki/FGDB-Spec).

```
go get github.com/Mikey1964x/fgdb
```

Go 1.24 or later is required.

## Reading

```go
g, err := fgdb.OpenGeodatabase("data/Cities.gdb")
if err != nil {
	log.Fatal(err)
}
defer g.Close()

tbl, err := g.OpenTable(`\Cities`)
if err != nil {
	log.Fatal(err)
}
env := fgdb.NewEnvelope(-125, 30, -110, 50)
rows, err := tbl.Search("CITY_NAME, POP1990", "POP1990 > 100000", &env, false)
if err != nil {
	log.Fatal(err)
}
for r, err := range rows.All() {
	if err != nil {
		log.Fatal(err)
	}
	name, _ := r.GetString("CITY_NAME")
	pop, _ := r.GetInteger("POP1990")
	fmt.Println(name, pop)
}
```

Geometry is returned as a `ShapeBuffer` (the Esri shapefile binary layout).
`ShapeBuffer.Geometry()` decodes it into points, parts, Z, M and curves.

The catalog can be browsed with `GetChildDatasets`, `GetDatasetDefinition`
(the XML definition), `GetRelatedDatasets` and `GetDomains`.

## Writing

```go
g, err := fgdb.CreateGeodatabase("out.gdb") // or OpenGeodatabaseForUpdate
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
```

An OBJECTID field is added when the field list has none.

What can be written:

- Geodatabases: `CreateGeodatabase`, `DeleteGeodatabase`.
- Datasets: `CreateTable` (tables and feature classes), `CreateFeatureDataset`,
  `Delete`, `Rename`.
- Rows: `Insert`, `Update`, `Delete`, `DeleteByOID`.
- Fields and indexes on existing tables: `AddField`, `DeleteField`,
  `AlterField`, `AddIndex`, `DeleteIndex`.
- SQL: `ExecuteSQL` runs `SELECT` (with `DISTINCT`, `ORDER BY`, and the
  aggregates COUNT, SUM, AVG, MIN and MAX) and, on a geodatabase opened
  for update, `INSERT INTO … VALUES`, `UPDATE … SET … WHERE` and
  `DELETE FROM … WHERE`.

Changes are written to disk on `Table.Flush`, `Table.Close` or
`Geodatabase.Close`. Attribute and spatial indexes are rebuilt at that
point, and the table extent is kept up to date.

The files written follow the layout of GDAL's OpenFileGDB driver, and
GDAL reads them. The attribute and spatial indexes written are
byte-for-byte identical to the ones Esri software wrote for the sample
data.

## Command-line tool

`cmd/gdbinfo` lists the datasets of a geodatabase, prints the fields and
rows of a table, or runs an SQL statement:

```
go run ./cmd/gdbinfo -where "POP1990 > 1000000" testdata/Querying.gdb Cities
go run ./cmd/gdbinfo -sql "SELECT COUNT(*) FROM Cities" testdata/Querying.gdb
```

## Limitations

- Tables in the compressed (CDF) format cannot be read.
- Raster fields can be read but not created.
- Domains, subtypes, relationship classes, topologies and other
  geodatabase behaviors can be read as XML definitions but not created
  or changed.
- Space freed by deleted or grown rows is not reused, and there is no
  compaction yet.
- `AddField`, `DeleteField` and `AlterField` rewrite the whole table, and
  are refused while the table is open more than once.
- `AlterField` cannot change the type of a field, and ObjectID, geometry
  and raster fields cannot be altered.
- SQL has no joins, `GROUP BY` or subqueries. Each statement works on one table.
- A geodatabase must not be changed by two programs at once. There is no
  file locking.

## Tests

`go test ./...` runs against the sample geodatabases in `testdata`, and
needs nothing else. Values read are compared with reference values that
GDAL produced from the same files (`testdata/expected`). See
[testdata/README.md](testdata/README.md) to regenerate them.
