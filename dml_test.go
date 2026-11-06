package fgdb

import (
	"errors"
	"testing"
)

func countWhere(t *testing.T, g *Geodatabase, table, where string) int64 {
	t.Helper()
	sql := "SELECT COUNT(*) FROM " + table
	if where != "" {
		sql += " WHERE " + where
	}
	rows, err := g.ExecuteSQL(sql, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := rows.Next()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := r.Value(0)
	n, _ := toInt64(v)
	return n
}

func TestExecuteDML(t *testing.T) {
	path := buildSampleGDB(t)
	g, err := OpenGeodatabaseForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO Cities (Name, Population) VALUES ('Lille', 230000), ('Brest', 140000)",
		"UPDATE Cities SET Population = Population * 2, Area = 9.5 WHERE Name = 'Nice'",
		"DELETE FROM Cities WHERE Name = 'Paris';",
		"INSERT INTO Notes VALUES ('note two', -5)",
	} {
		if _, err := g.ExecuteSQL(s, false); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, c := range []struct {
		sql  string
		want error
	}{
		{"INSERT INTO Cities (Nope) VALUES (1)", ErrFieldNotFound},
		{"INSERT INTO Cities (Name) VALUES (1, 2)", ErrInvalidSQL},
		{"INSERT INTO Cities (Name) VALUES ('a'), (Population)", ErrInvalidSQL},
		{"INSERT INTO Cities (Population) VALUES ('many')", ErrTypeMismatch},
		{"UPDATE Cities SET Population = 'x' WHERE Name = 'Lyon'", ErrTypeMismatch},
		{"UPDATE Cities SET OBJECTID = 5", ErrTypeMismatch},
		{"DELETE FROM Missing", ErrNotFound},
		{"DELETE FROM Cities WHERE", ErrInvalidSQL},
	} {
		if _, err := g.ExecuteSQL(c.sql, false); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.sql, err, c.want)
		}
	}
	// Failed statements changed nothing.
	if n := countWhere(t, g, "Cities", ""); n != 4 {
		t.Errorf("%d cities", n)
	}
	must(t, g.Close())

	g, err = OpenGeodatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.ExecuteSQL("DELETE FROM Cities", false); !errors.Is(err, ErrReadOnly) {
		t.Errorf("read-only delete: %v", err)
	}
	for where, want := range map[string]int64{
		"Name = 'Paris'": 0,
		"Name = 'Nice' AND Population = 680000 AND Area = 9.5": 1,
		"Population = 230000": 1,
		"GID IS NULL":         0,
		"":                    4,
	} {
		if n := countWhere(t, g, "Cities", where); n != want {
			t.Errorf("%q: %d rows, want %d", where, n, want)
		}
	}
	if n := countWhere(t, g, "Notes", "Big = -5 AND Text = 'note two'"); n != 1 {
		t.Errorf("notes %d", n)
	}
}
