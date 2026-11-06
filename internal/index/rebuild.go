package index

import (
	"errors"
	"os"
	"strings"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// BasePath returns the path of a table's files without extension, e.g.
// dir/a00000009 for dir/a00000009.gdbtable.
func BasePath(t *gdbtable.Table) string { return strings.TrimSuffix(t.Path, ".gdbtable") }

// AttributePath returns the path of the .atx file of an index.
func AttributePath(t *gdbtable.Table, name string) string {
	return BasePath(t) + "." + name + ".atx"
}

// SpatialPath returns the path of the .spx file of a feature class.
func SpatialPath(t *gdbtable.Table) string { return BasePath(t) + ".spx" }

// IsSpatial reports whether d is the spatial index of table t.
func IsSpatial(t *gdbtable.Table, d Def) bool {
	return d.Magic2 == KindSpatial ||
		(t.GeomField >= 0 && strings.EqualFold(d.Expression, t.Fields[t.GeomField].Name))
}

// Rebuild regenerates the .atx and .spx files of every index declared in the
// .gdbindexes file of t. The ObjectID index has no file. When the feature
// class has no spatial index grid size yet, one is computed and stored in
// the table (which must then be flushed). Index files that cannot be built
// are removed rather than left out of date.
func Rebuild(t *gdbtable.Table) error {
	defs, err := ReadDefs(BasePath(t) + ".gdbindexes")
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range defs {
		if d.Magic2 == KindObjectID {
			continue
		}
		if IsSpatial(t, d) {
			errs = append(errs, RebuildSpatial(t))
			continue
		}
		errs = append(errs, RebuildAttribute(t, d))
	}
	return errors.Join(errs...)
}

// RebuildSpatial regenerates the .spx file of a feature class.
func RebuildSpatial(t *gdbtable.Table) error {
	path := SpatialPath(t)
	if t.GeomField < 0 {
		return nil
	}
	gi := t.Fields[t.GeomField].Geom
	if len(gi.GridSizes) == 1 && gi.GridSizes[0] <= 0 {
		size, err := OptimalGridSize(t)
		if err != nil {
			return err
		}
		if size > 0 {
			if err := t.SetGridSizes([]float64{size}); err != nil {
				return err
			}
		}
	}
	entries, err := BuildSpatial(t)
	if errors.Is(err, ErrUnsupported) {
		if t.RowCount() == 0 {
			entries, err = nil, nil
		} else {
			return removeIfExists(path)
		}
	}
	if err != nil {
		return err
	}
	return WriteTreeFile(path, entries, 8, FlagNumeric)
}

// RebuildAttribute regenerates the .atx file of an attribute index.
func RebuildAttribute(t *gdbtable.Table, d Def) error {
	path := AttributePath(t, d.Name)
	a, err := BuildAttribute(t, d.Expression)
	if errors.Is(err, ErrUnsupported) {
		return removeIfExists(path)
	}
	if err != nil {
		return err
	}
	return WriteTreeFile(path, a.Entries, a.KeySize, a.Flag)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
