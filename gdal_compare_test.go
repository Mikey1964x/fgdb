package fgdb

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The files in testdata/expected were produced by
// testdata/tools/make_expected.py, which reads the test geodatabases with
// GDAL's OpenFileGDB driver. This test checks that this package reads the
// same ObjectIDs, attribute values and coordinates.

type expectedLayer struct {
	Layer  string   `json:"layer"`
	Fields []string `json:"fields"`
	Rows   []struct {
		FID   int64          `json:"fid"`
		Attrs map[string]any `json:"attrs"`
		Geom  *struct {
			Type  string         `json:"type"`
			Parts [][][]*float64 `json:"parts"`
		} `json:"geom"`
	} `json:"rows"`
}

func loadExpected(t *testing.T, path string) *expectedLayer {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(zr)
	dec.UseNumber()
	var e expectedLayer
	if err := dec.Decode(&e); err != nil {
		t.Fatal(err)
	}
	return &e
}

func TestCompareWithGDAL(t *testing.T) {
	files, _ := filepath.Glob("testdata/expected/*/*.json.gz")
	if len(files) == 0 {
		t.Skip("no expected files")
	}
	gdbs := map[string]*Geodatabase{}
	defer func() {
		for _, g := range gdbs {
			g.Close()
		}
	}()
	for _, fn := range files {
		gdbName := filepath.Base(filepath.Dir(fn))
		exp := loadExpected(t, fn)
		t.Run(gdbName+"/"+exp.Layer, func(t *testing.T) {
			g := gdbs[gdbName]
			if g == nil {
				var err error
				g, err = OpenGeodatabase(filepath.Join("testdata", gdbName))
				if err != nil {
					t.Fatal(err)
				}
				gdbs[gdbName] = g
			}
			compareLayer(t, g, exp)
		})
	}
}

func compareLayer(t *testing.T, g *Geodatabase, exp *expectedLayer) {
	tbl, err := g.OpenTable(exp.Layer)
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()
	n, _ := tbl.GetRowCount()
	if int(n) != len(exp.Rows) {
		t.Errorf("row count = %d, GDAL has %d", n, len(exp.Rows))
	}
	rows, err := tbl.Search("*", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]*Row{}
	for r, err := range rows.All() {
		if err != nil {
			t.Fatal(err)
		}
		oid, _ := r.GetOID()
		got[oid] = r
	}
	if len(got) != len(exp.Rows) {
		t.Errorf("read %d rows, GDAL has %d", len(got), len(exp.Rows))
	}
	errs := 0
	report := func(format string, args ...any) {
		errs++
		if errs <= 10 {
			t.Errorf(format, args...)
		}
	}
	for _, er := range exp.Rows {
		r := got[er.FID]
		if r == nil {
			report("ObjectID %d missing", er.FID)
			continue
		}
		for name, want := range er.Attrs {
			v, err := r.Get(name)
			if err != nil {
				report("fid %d field %s: %v", er.FID, name, err)
				continue
			}
			if msg := compareValue(v, want); msg != "" {
				report("fid %d field %s: %s", er.FID, name, msg)
			}
		}
		if er.Geom == nil {
			if null, _ := r.IsNull(geomFieldName(r)); geomFieldName(r) != "" && !null {
				report("fid %d: geometry should be NULL", er.FID)
			}
			continue
		}
		if er.Geom.Parts == nil {
			continue // a type GDAL's reference reader could not parse
		}
		sb, err := r.GetGeometry()
		if err != nil {
			report("fid %d: %v", er.FID, err)
			continue
		}
		if msg := compareGeometry(sb, er.Geom.Parts); msg != "" {
			report("fid %d (%s): %s", er.FID, er.Geom.Type, msg)
		}
	}
}

func geomFieldName(r *Row) string {
	for _, d := range r.defs {
		if d.Type == FieldTypeGeometry {
			return d.Name
		}
	}
	return ""
}

func compareValue(got, want any) string {
	if want == nil {
		if got != nil {
			return fmt.Sprintf("got %v, want NULL", got)
		}
		return ""
	}
	if got == nil {
		return fmt.Sprintf("got NULL, want %v", want)
	}
	switch w := want.(type) {
	case json.Number:
		wf, _ := w.Float64()
		switch g := got.(type) {
		case int16, int32, int64:
			gi, _ := toInt64(g)
			if wi, err := w.Int64(); err == nil {
				if gi != wi {
					return fmt.Sprintf("got %v, want %v", g, w)
				}
			} else if float64(gi) != wf {
				// GDAL returns nullable integer columns as float64.
				return fmt.Sprintf("got %v, want %v", g, w)
			}
		case float32:
			if float64(g) != wf {
				return fmt.Sprintf("got %v, want %v", g, w)
			}
		case float64:
			if g != wf && math.Abs(g-wf) > 1e-12*math.Abs(wf) {
				return fmt.Sprintf("got %v, want %v", g, w)
			}
		default:
			return fmt.Sprintf("got %T %v, want number %v", got, got, w)
		}
	case string:
		switch g := got.(type) {
		case string:
			if g != w {
				return fmt.Sprintf("got %q, want %q", g, w)
			}
		case Guid:
			if !strings.EqualFold(g.String(), w) {
				return fmt.Sprintf("got %v, want %v", g, w)
			}
		case time.Time:
			wt, err := parseGDALTime(w)
			if err != nil {
				return err.Error()
			}
			// GDAL drops the time zone: compare wall clock times.
			gw := time.Date(g.Year(), g.Month(), g.Day(), g.Hour(), g.Minute(), g.Second(), g.Nanosecond(), time.UTC)
			if d := gw.Sub(wt); d > time.Millisecond || d < -time.Millisecond {
				return fmt.Sprintf("got %v, want %v", gw, wt)
			}
		default:
			return fmt.Sprintf("got %T %v, want %q", got, got, w)
		}
	case map[string]any:
		h, _ := w["hex"].(string)
		b, ok := got.([]byte)
		if !ok || hex.EncodeToString(b) != h {
			return fmt.Sprintf("got %v, want hex %s", got, h)
		}
	default:
		return fmt.Sprintf("unexpected reference value %T", want)
	}
	return ""
}

func parseGDALTime(s string) (time.Time, error) {
	s = strings.TrimSuffix(s, "Z")
	if i := strings.LastIndexAny(s, "+-"); i > 10 {
		s = s[:i] // drop UTC offset
	}
	for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02", "15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse GDAL time %q", s)
}

func compareGeometry(sb *ShapeBuffer, want [][][]*float64) string {
	g, err := sb.Geometry()
	if err != nil {
		return err.Error()
	}
	if len(g.Curves) > 0 {
		// The reference reader linearizes curves; only the control points
		// are stored in the shape buffer.
		return ""
	}
	hasZ, hasM := g.ShapeType.HasZs(), g.ShapeType.HasMs()
	var parts [][][]float64
	starts := g.Parts
	if len(starts) == 0 {
		starts = []int32{0}
	}
	for i, s := range starts {
		end := len(g.Points)
		if i+1 < len(starts) {
			end = int(starts[i+1])
		}
		var part [][]float64
		for j := int(s); j < end; j++ {
			c := []float64{g.Points[j].X, g.Points[j].Y}
			if hasZ {
				c = append(c, g.Zs[j])
			}
			if hasM {
				c = append(c, g.Ms[j])
			}
			part = append(part, c)
		}
		parts = append(parts, part)
	}
	if len(parts) != len(want) {
		return fmt.Sprintf("got %d parts, want %d", len(parts), len(want))
	}
	key := func(p [][]float64) string {
		if len(p) == 0 {
			return ""
		}
		return fmt.Sprintf("%.6f %.6f %d", p[0][0], p[0][1], len(p))
	}
	wantParts := make([][][]float64, len(want))
	for i, p := range want {
		for _, c := range p {
			var cc []float64
			for _, v := range c {
				if v == nil {
					cc = append(cc, math.NaN())
				} else {
					cc = append(cc, *v)
				}
			}
			wantParts[i] = append(wantParts[i], cc)
		}
	}
	// GDAL may reorder polygon rings: compare parts as sets.
	sort.Slice(parts, func(a, b int) bool { return key(parts[a]) < key(parts[b]) })
	sort.Slice(wantParts, func(a, b int) bool { return key(wantParts[a]) < key(wantParts[b]) })
	for i := range parts {
		if len(parts[i]) != len(wantParts[i]) {
			return fmt.Sprintf("part %d: got %d points, want %d", i, len(parts[i]), len(wantParts[i]))
		}
		for j := range parts[i] {
			gc, wc := parts[i][j], wantParts[i][j]
			n := min(len(gc), len(wc))
			if n < 2 {
				return fmt.Sprintf("part %d point %d: bad coordinate", i, j)
			}
			for k := 0; k < n; k++ {
				a, b := gc[k], wc[k]
				if math.IsNaN(a) && math.IsNaN(b) {
					continue
				}
				if math.Abs(a-b) > 1e-9*math.Max(1, math.Abs(b)) {
					return fmt.Sprintf("part %d point %d coord %d: got %v, want %v", i, j, k, a, b)
				}
			}
		}
	}
	return ""
}
