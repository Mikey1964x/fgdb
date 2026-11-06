package geom

import (
	"bytes"
	"math"
	"path/filepath"
	"testing"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// TestRoundTripTestdata decodes every geometry of the test geodatabases,
// converts it to a shape buffer and back, and checks that re-encoding gives
// back the original bytes.
func TestRoundTripTestdata(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/*.gdb/*.gdbtable")
	n := 0
	for _, fn := range files {
		tbl, err := gdbtable.Open(fn)
		if err != nil {
			continue // e.g. compressed tables
		}
		if tbl.GeomField < 0 {
			tbl.Close()
			continue
		}
		p := PrecisionFrom(tbl.Fields[tbl.GeomField].Geom)
		for fid, _ := tbl.NextFID(1); fid > 0; fid, _ = tbl.NextFID(fid + 1) {
			vals, err := tbl.ReadRow(fid)
			if err != nil {
				t.Fatalf("%s %d: %v", fn, fid, err)
			}
			blob, _ := vals[tbl.GeomField].(gdbtable.Geometry)
			if blob == nil {
				continue
			}
			s, err := Decode(blob, p)
			if err != nil {
				t.Fatalf("%s %d: decode: %v", fn, fid, err)
			}
			s2, err := ParseShape(s.Bytes())
			if err != nil {
				t.Fatalf("%s %d: parse: %v", fn, fid, err)
			}
			enc, err := Encode(s2, p)
			if err != nil {
				t.Fatalf("%s %d: encode: %v", fn, fid, err)
			}
			if !bytes.Equal(enc, blob) && s.Kind() != KindMultiPatch {
				t.Errorf("%s %d: round trip differs\n%x\n%x", filepath.Base(fn), fid, blob, enc)
			}
			n++
		}
		tbl.Close()
	}
	if n == 0 {
		t.Skip("no geometries found")
	}
}

func TestEncodeDecode(t *testing.T) {
	p := Precision{XOrigin: -1000, YOrigin: -1000, XYScale: 1000, ZOrigin: -100, ZScale: 100, MOrigin: -100, MScale: 100}
	shapes := []*Shape{
		{Type: TypePointZM, Points: []Point{{1.5, 2.25}}, Z: []float64{3}, M: []float64{math.NaN()}},
		{Type: TypeMultipointZ, Points: []Point{{1, 2}, {3, 4}}, Z: []float64{5, 6}},
		{Type: TypePolylineM, Points: []Point{{0, 0}, {1, 1}, {2, 0}, {5, 5}, {6, 6}}, Parts: []int32{0, 3}, M: []float64{1, 2, 3, 4, 5}},
		{Type: TypePolylineM, Points: []Point{{0, 0}, {1, 1}}, Parts: []int32{0}, M: []float64{math.NaN(), math.NaN()}},
		{Type: TypePolygon, Points: []Point{{0, 0}, {0, 10}, {10, 10}, {10, 0}, {0, 0}}, Parts: []int32{0}},
		{Type: TypeGeneralPolyline | ModifierCurves, Points: []Point{{0, 0}, {2, 0}}, Parts: []int32{0},
			Curves: []Curve{{StartIndex: 0, Type: CurveCircularArc, Data: make([]byte, 20)}}},
		{Type: TypeMultiPatch, Points: []Point{{0, 0}, {0, 1}, {1, 0}}, Parts: []int32{0}, PartTypes: []int32{0}, Z: []float64{1, 2, 3}},
		{Type: TypeNull},
	}
	for _, s := range shapes {
		blob, err := Encode(s, p)
		if err != nil {
			t.Fatalf("type %d: %v", s.Type, err)
		}
		d, err := Decode(blob, p)
		if err != nil {
			t.Fatalf("type %d: %v", s.Type, err)
		}
		if d.Type != s.Type || len(d.Points) != len(s.Points) || len(d.Curves) != len(s.Curves) {
			t.Errorf("type %d: got %+v", s.Type, d)
			continue
		}
		for i := range s.Points {
			if d.Points[i] != s.Points[i] {
				t.Errorf("type %d point %d: %v != %v", s.Type, i, d.Points[i], s.Points[i])
			}
		}
		for i := range s.M {
			if !(d.M[i] == s.M[i] || math.IsNaN(d.M[i]) && math.IsNaN(s.M[i])) {
				t.Errorf("type %d m %d: %v != %v", s.Type, i, d.M[i], s.M[i])
			}
		}
		// Shape buffer round trip.
		ps, err := ParseShape(d.Bytes())
		if err != nil || ps.Type != s.Type || len(ps.Points) != len(s.Points) {
			t.Errorf("type %d: shape buffer round trip: %+v %v", s.Type, ps, err)
		}
	}
	if _, err := Encode(&Shape{Type: TypePoint, Points: []Point{{-5000, 0}}}, p); err == nil {
		t.Error("coordinate below the origin should fail")
	}
}
