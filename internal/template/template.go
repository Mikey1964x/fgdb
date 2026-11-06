// Package template holds the files of an empty File Geodatabase, used by
// CreateGeodatabase. They were written by GDAL's OpenFileGDB driver
// (version 3.12) and contain the system tables GDB_SystemCatalog,
// GDB_DBTune, GDB_SpatialRefs, GDB_Items, GDB_ItemTypes,
// GDB_ItemRelationships and GDB_ItemRelationshipTypes.
package template

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed empty.gdb
var files embed.FS

// Write copies the empty geodatabase files into dir, which must exist.
func Write(dir string) error {
	return fs.WalkDir(files, "empty.gdb", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, d.Name()), b, 0o644)
	})
}
