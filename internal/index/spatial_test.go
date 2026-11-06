package index

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// TestRebuildSpatialIndexes compares rebuilt spatial indexes with the
// sample files. Indexes written by Esri software must match exactly; GDAL
// rasterizes lines and polygons more finely, so for its files the rebuilt
// cells must only cover the original ones.
func TestRebuildSpatialIndexes(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/*.gdb/*.spx")
	for _, fn := range files {
		base := strings.TrimSuffix(fn, ".spx")
		tb, err := gdbtable.Open(base + ".gdbtable")
		if err != nil {
			continue
		}
		_, want, err := ReadTree(fn)
		if err != nil {
			t.Errorf("%s: %v", fn, err)
			continue
		}
		got, err := BuildSpatial(tb)
		if err != nil {
			tb.Close() // no grid defined
			continue
		}
		have := map[string]bool{}
		for _, e := range got {
			have[string(e.Key)+string(rune(e.OID))] = true
		}
		missing := 0
		for _, e := range want {
			if !have[string(e.Key)+string(rune(e.OID))] {
				missing++
			}
		}
		if missing > 0 {
			t.Errorf("%s: %d of %d entries missing", fn, missing, len(want))
		}
		if !strings.Contains(fn, "gdal.gdb") && !sameEntries(got, want) {
			t.Errorf("%s: rebuilt %d entries, want %d", fn, len(got), len(want))
		}
		tb.Close()
	}
}
