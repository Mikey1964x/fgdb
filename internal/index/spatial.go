package index

import (
	"cmp"
	"encoding/binary"
	"math"
	"slices"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/geom"
)

// Spatial index (.spx) keys are grid cells. The geometry field defines up to
// three grid levels of increasing cell size; each feature is stored at the
// finest level where its envelope covers few cells, under every cell of its
// envelope, as Esri software does. A key packs the grid level in bits 62-63,
// the cell column in bits 31-61 and the cell row in bits 0-30.

// OptimalGridSize computes a grid size for a feature class from its
// content, like GDAL does when it creates a spatial index. It returns 0
// when no size can be computed.
func OptimalGridSize(t *gdbtable.Table) (float64, error) {
	if t.GeomField < 0 || t.RowCount() == 0 {
		return 0, nil
	}
	gi := t.Fields[t.GeomField].Geom
	p := geom.PrecisionFrom(gi)
	kind, _, _ := geom.Classify(uint32(t.GeomType()))
	var nPoints int64
	maxSize := 0.0
	err := forEachRow(t, func(fid int64, vals []any) error {
		g, ok := vals[t.GeomField].(gdbtable.Geometry)
		if !ok {
			return nil
		}
		switch kind {
		case geom.KindPoint:
			nPoints++
		case geom.KindMultipoint:
			s, err := geom.Decode(g, p)
			if err == nil {
				nPoints += int64(len(s.Points))
			}
		default:
			xmin, ymin, xmax, ymax, ok := geom.Extent(g, p)
			if ok {
				maxSize = math.Max(maxSize, math.Max(xmax-xmin, ymax-ymin))
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if kind != geom.KindPoint && kind != geom.KindMultipoint {
		return maxSize, nil
	}
	if nPoints == 0 {
		return 0, nil
	}
	w, h := gi.XMax-gi.XMin, gi.YMax-gi.YMin
	switch {
	case w*h != 0:
		return math.Sqrt(w * h / float64(nPoints)), nil
	case w > 0:
		return w / float64(nPoints), nil
	case h > 0:
		return h / float64(nPoints), nil
	}
	return 0, nil
}

// grid maps coordinates to cell keys.
type grid struct {
	step  float64
	shift float64
	level uint64
	cells []int64
}

func newGrid(sizes []float64, level int) *grid {
	step := sizes[level]
	return &grid{
		step:  step,
		shift: float64(1<<29) / (step / sizes[0]),
		level: uint64(level),
	}
}

// maxCellsPerFeature is the number of cells above which a feature is moved
// to the next (coarser) grid level. Esri does not document the rule; the
// sample data shows features of 12 cells kept and of 23000 cells moved.
const maxCellsPerFeature = 16

// levelFor returns the finest grid level at which an envelope covers at
// most maxCellsPerFeature cells, or the coarsest level.
func levelFor(grids []*grid, xmin, ymin, xmax, ymax float64) *grid {
	for _, g := range grids[:len(grids)-1] {
		nx := math.Floor(g.gx(xmax)) - math.Floor(g.gx(xmin)) + 1
		ny := math.Floor(g.gx(ymax)) - math.Floor(g.gx(ymin)) + 1
		if nx*ny <= maxCellsPerFeature {
			return g
		}
	}
	return grids[len(grids)-1]
}

func (g *grid) gx(x float64) float64 { return x/g.step + g.shift }

func (g *grid) add(ix, iy int) {
	v := g.level<<62 | uint64(uint32(int32(ix)))<<31 | uint64(uint32(int32(iy)))
	g.cells = append(g.cells, int64(v))
}

const lim = 1 << 30

// box burns every cell of an envelope.
func (g *grid) box(xmin, ymin, xmax, ymax float64) {
	x0, y0, x1, y1 := g.gx(xmin), g.gx(ymin), g.gx(xmax), g.gx(ymax)
	if !(math.Abs(x0) < lim && math.Abs(y0) < lim && math.Abs(x1) < lim && math.Abs(y1) < lim) {
		return
	}
	for ix := int(math.Floor(x0)); ix <= int(math.Floor(x1)); ix++ {
		for iy := int(math.Floor(y0)); iy <= int(math.Floor(y1)); iy++ {
			g.add(ix, iy)
		}
	}
}

// BuildSpatial computes the entries of the spatial index of a feature
// class, using the grid sizes stored in its geometry field.
func BuildSpatial(t *gdbtable.Table) ([]Entry, error) {
	if t.GeomField < 0 {
		return nil, ErrUnsupported
	}
	gi := t.Fields[t.GeomField].Geom
	if len(gi.GridSizes) == 0 || gi.GridSizes[len(gi.GridSizes)-1] <= 0 || gi.GridSizes[0] <= 0 {
		return nil, ErrUnsupported
	}
	p := geom.PrecisionFrom(gi)
	grids := make([]*grid, len(gi.GridSizes))
	for l := range grids {
		grids[l] = newGrid(gi.GridSizes, l)
	}
	var cells []int64
	type pair struct{ v, oid int64 }
	var pairs []pair
	err := forEachRow(t, func(fid int64, vals []any) error {
		blob, ok := vals[t.GeomField].(gdbtable.Geometry)
		if !ok {
			return nil
		}
		xmin, ymin, xmax, ymax, ok := geom.Extent(blob, p)
		if !ok {
			return nil
		}
		g := levelFor(grids, xmin, ymin, xmax, ymax)
		g.cells = g.cells[:0]
		g.box(xmin, ymin, xmax, ymax)
		cells = append(cells[:0], g.cells...)
		slices.Sort(cells)
		for i, c := range cells {
			if i == 0 || c != cells[i-1] {
				pairs = append(pairs, pair{c, fid})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(pairs, func(a, b pair) int { return cmp.Or(cmp.Compare(a.v, b.v), cmp.Compare(a.oid, b.oid)) })
	entries := make([]Entry, len(pairs))
	keys := make([]byte, 8*len(pairs))
	for i, pr := range pairs {
		k := keys[8*i : 8*i+8 : 8*i+8]
		binary.LittleEndian.PutUint64(k, uint64(pr.v))
		entries[i] = Entry{Key: k, OID: pr.oid}
	}
	return entries, nil
}
