// Command gdbinfo prints the contents of a File Geodatabase.
//
//	gdbinfo [-n rows] [-where clause] [-sql statement] path.gdb [table]
//
// Without a table name it lists the datasets of the geodatabase. With a
// table name it prints the fields and the first rows of that table.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Mikey1964x/fgdb"
)

func main() {
	n := flag.Int("n", 10, "number of rows to print (-1 for all)")
	where := flag.String("where", "", "where clause to filter rows")
	sqlStmt := flag.String("sql", "", "SQL statement to execute")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: gdbinfo [flags] path.gdb [table]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Arg(1), *where, *sqlStmt, *n); err != nil {
		fmt.Fprintln(os.Stderr, "gdbinfo:", err)
		os.Exit(1)
	}
}

func run(path, table, where, sqlStmt string, n int) error {
	gdb, err := fgdb.OpenGeodatabase(path)
	if err != nil {
		return err
	}
	defer gdb.Close()

	if sqlStmt != "" {
		rows, err := gdb.ExecuteSQL(sqlStmt, true)
		if err != nil {
			return err
		}
		defer rows.Close()
		return printRows(rows, n)
	}
	if table == "" {
		return listDatasets(gdb, `\`, "")
	}
	t, err := gdb.OpenTable(table)
	if err != nil {
		return err
	}
	defer t.Close()
	count, _ := t.GetRowCount()
	fmt.Printf("Table %s (%s): %d rows\n", t.Name(), t.Path(), count)
	fields, _ := t.GetFields()
	for _, f := range fields {
		extra := ""
		if f.GeometryDef != nil {
			gd := f.GeometryDef
			extra = fmt.Sprintf(" %v Z=%v M=%v WKID=%d", gd.GeometryType, gd.HasZ, gd.HasM, gd.SpatialReference.WKID)
		}
		fmt.Printf("  %-20s %-28v len=%-4d nullable=%v%s\n", f.Name, f.Type, f.Length, f.IsNullable, extra)
	}
	if e, err := t.GetExtent(); err == nil {
		fmt.Printf("  extent: %g %g %g %g\n", e.XMin, e.YMin, e.XMax, e.YMax)
	}
	if idx, _ := t.GetIndexes(); len(idx) > 0 {
		for _, d := range idx {
			fmt.Printf("  index %s on %s\n", d.Name, d.Fields)
		}
	}
	rows, err := t.Search("*", where, nil, true)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printRows(rows, n)
}

func listDatasets(gdb *fgdb.Geodatabase, parent, indent string) error {
	for _, typ := range []string{fgdb.DatasetTypeFeatureDataset, fgdb.DatasetTypeFeatureClass, fgdb.DatasetTypeTable} {
		children, err := gdb.GetChildDatasets(parent, typ)
		if err != nil {
			return err
		}
		for _, c := range children {
			fmt.Printf("%s%s  [%s]\n", indent, c, typ)
			if typ == fgdb.DatasetTypeFeatureDataset {
				if err := listDatasets(gdb, c, indent+"  "); err != nil {
					return err
				}
			}
		}
	}
	if parent == `\` {
		if domains, _ := gdb.GetDomains(); len(domains) > 0 {
			fmt.Printf("domains: %s\n", strings.Join(domains, ", "))
		}
	}
	return nil
}

func printRows(rows *fgdb.EnumRows, n int) error {
	fields := rows.GetFields()
	for i := 0; n < 0 || i < n; i++ {
		row, err := rows.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var parts []string
		for j, f := range fields {
			v, err := row.Value(j)
			if err != nil {
				return err
			}
			parts = append(parts, f.Name+"="+formatValue(v))
		}
		fmt.Println(strings.Join(parts, " | "))
	}
	return nil
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case *fgdb.ShapeBuffer:
		t, _ := x.ShapeType()
		pts, _ := x.Points()
		if len(pts) == 1 {
			return fmt.Sprintf("%v(%g %g)", t.GeometryType(), pts[0].X, pts[0].Y)
		}
		return fmt.Sprintf("%v[%d points]", t.GeometryType(), len(pts))
	case []byte:
		return fmt.Sprintf("<%d bytes>", len(x))
	case string:
		if len(x) > 60 {
			return x[:57] + "..."
		}
		return x
	}
	return fmt.Sprint(v)
}
