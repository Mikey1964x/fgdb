package geom

import (
	"fmt"
	"math"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// Precision holds the coordinate quantization parameters of a geometry field.
type Precision struct {
	XOrigin, YOrigin, XYScale float64
	ZOrigin, ZScale           float64
	MOrigin, MScale           float64
}

// PrecisionFrom extracts the precision model of a geometry field.
func PrecisionFrom(g *gdbtable.GeomInfo) Precision {
	return Precision{
		XOrigin: g.XOrigin, YOrigin: g.YOrigin, XYScale: sanitize(g.XYScale),
		ZOrigin: g.ZOrigin, ZScale: sanitize(g.ZScale),
		MOrigin: g.MOrigin, MScale: sanitize(g.MScale),
	}
}

func sanitize(s float64) float64 {
	if s == 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		return 1
	}
	return s
}

const mAbsentMarker = 0x42

type dec struct {
	c *gdbtable.Cursor
	p Precision
}

// Decode converts a compressed geometry blob (as stored in a row) into a
// Shape.
func Decode(blob []byte, p Precision) (*Shape, error) {
	d := &dec{c: &gdbtable.Cursor{B: blob}, p: p}
	s, err := d.decode()
	if err != nil {
		return nil, err
	}
	if d.c.Err != nil {
		return nil, fmt.Errorf("%w: truncated geometry", ErrInvalid)
	}
	return s, nil
}

func (d *dec) decode() (*Shape, error) {
	c, p := d.c, d.p
	s := &Shape{Type: uint32(c.VarUint())}
	k, hasZ, hasM := Classify(s.Type)
	switch k {
	case KindNull:
		return s, nil
	case KindUnknown:
		return nil, fmt.Errorf("%w: unsupported geometry type %d", ErrInvalid, s.Type)
	case KindPoint:
		x, y := c.VarUint(), c.VarUint()
		pt := Point{math.NaN(), math.NaN()}
		if x != 0 {
			pt.X = float64(x-1)/p.XYScale + p.XOrigin
		}
		if y != 0 {
			pt.Y = float64(y-1)/p.XYScale + p.YOrigin
		}
		s.Points = []Point{pt}
		if hasZ {
			z := c.VarUint()
			zv := math.NaN()
			if z != 0 {
				zv = float64(z-1)/p.ZScale + p.ZOrigin
			}
			s.Z = []float64{zv}
		}
		if hasM {
			m := c.VarUint()
			mv := math.NaN()
			if m != 0 {
				mv = float64(m-1)/p.MScale + p.MOrigin
			}
			s.M = []float64{mv}
		}
		s.ComputeExtent()
		return s, nil
	case KindMultipoint:
		n := int(c.VarUint())
		if n == 0 {
			return s, nil
		}
		if n > c.Remaining() {
			return nil, ErrInvalid
		}
		d.bbox(s)
		var dx, dy int64
		s.Points = d.xy(n, &dx, &dy)
		if hasZ {
			s.Z = d.zm(n, p.ZScale, p.ZOrigin)
		}
		if hasM {
			s.M = d.optionalM(n)
		}
	case KindPolyline, KindPolygon:
		n := int(c.VarUint())
		if n == 0 {
			return s, nil
		}
		if n > c.Remaining() {
			return nil, ErrInvalid
		}
		nParts := int(c.VarUint())
		curves := HasCurves(s.Type)
		nCurves := 0
		if curves {
			nCurves = int(c.VarUint())
		}
		if nParts == 0 || nParts > c.Remaining() || nCurves > c.Remaining() {
			return nil, ErrInvalid
		}
		d.bbox(s)
		s.Parts = d.parts(nParts, n)
		var dx, dy int64
		s.Points = d.xy(n, &dx, &dy)
		if hasZ {
			s.Z = d.zm(n, p.ZScale, p.ZOrigin)
		}
		if !curves {
			if hasM {
				s.M = d.optionalM(n)
			}
			break
		}
		// With curves, the M array is followed by curve descriptions, so the
		// "M absent" marker cannot be detected from the remaining length.
		// Try with an M array first, then without.
		save := c.Pos
		if hasM {
			s.M = d.zm(n, p.MScale, p.MOrigin)
		}
		var err error
		s.Curves, err = d.curves(nCurves)
		if (err != nil || c.Err != nil || c.Remaining() != 0) && hasM && save < len(c.B) && c.B[save] == mAbsentMarker {
			c.Err, c.Pos = nil, save+1
			s.M = nanSlice(n)
			s.Curves, err = d.curves(nCurves)
		}
		if err != nil {
			return nil, err
		}
	case KindMultiPatch:
		n := int(c.VarUint())
		if n == 0 {
			return s, nil
		}
		if n > c.Remaining() {
			return nil, ErrInvalid
		}
		c.VarUint() // size of the uncompressed extended shape buffer
		nParts := int(c.VarUint())
		if nParts == 0 || nParts > c.Remaining() {
			return nil, ErrInvalid
		}
		d.bbox(s)
		s.Parts = d.parts(nParts, n)
		s.PartTypes = make([]int32, nParts)
		for i := range s.PartTypes {
			s.PartTypes[i] = int32(c.VarUint())
		}
		var dx, dy int64
		s.Points = d.xy(n, &dx, &dy)
		if hasZ {
			s.Z = d.zm(n, p.ZScale, p.ZOrigin)
		}
		const extras = ModifierNormals | ModifierTexture | ModifierMater | ModifierPartIDs | ModifierIDs
		if s.Type&extras != 0 {
			// Normals, texture coordinates and materials follow in an
			// undocumented encoding. They are kept as opaque bytes and
			// dropped from the shape type.
			s.Extra = append([]byte(nil), c.Bytes(c.Remaining())...)
			s.Type &^= extras
			if hasM {
				s.M = nanSlice(n)
			}
		} else if hasM {
			s.M = d.optionalM(n)
		}
	}
	s.ZMin, s.ZMax = minMax(s.Z)
	s.MMin, s.MMax = minMax(s.M)
	return s, nil
}

func nanSlice(n int) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = math.NaN()
	}
	return v
}

func (d *dec) bbox(s *Shape) {
	c, p := d.c, d.p
	s.XMin = float64(c.VarUint())/p.XYScale + p.XOrigin
	s.YMin = float64(c.VarUint())/p.XYScale + p.YOrigin
	s.XMax = float64(c.VarUint())/p.XYScale + s.XMin
	s.YMax = float64(c.VarUint())/p.XYScale + s.YMin
}

func (d *dec) parts(nParts, n int) []int32 {
	parts := make([]int32, nParts)
	sum := 0
	for i := 1; i < nParts; i++ {
		cnt := int(d.c.VarUint())
		sum += cnt
		if sum > n {
			d.c.Err = ErrInvalid
			return parts
		}
		parts[i] = int32(sum)
	}
	return parts
}

func (d *dec) xy(n int, dx, dy *int64) []Point {
	c, p := d.c, d.p
	pts := make([]Point, n)
	for i := 0; i < n && c.Err == nil; i++ {
		*dx += c.VarInt()
		*dy += c.VarInt()
		pts[i] = Point{float64(*dx)/p.XYScale + p.XOrigin, float64(*dy)/p.XYScale + p.YOrigin}
	}
	return pts
}

func (d *dec) zm(n int, scale, origin float64) []float64 {
	c := d.c
	v := make([]float64, n)
	var acc int64
	for i := 0; i < n && c.Err == nil; i++ {
		acc += c.VarInt()
		v[i] = float64(acc)/scale + origin
	}
	return v
}

// optionalM reads an M array that may be replaced by the single "absent"
// marker byte 0x42.
func (d *dec) optionalM(n int) []float64 {
	c := d.c
	rem := c.Remaining()
	if rem < n || (rem == 1 && c.B[c.Pos] == mAbsentMarker) {
		if rem > 0 && c.B[c.Pos] == mAbsentMarker {
			c.Pos++
		}
		return nanSlice(n)
	}
	return d.zm(n, d.p.MScale, d.p.MOrigin)
}

func (d *dec) curves(n int) ([]Curve, error) {
	c := d.c
	out := make([]Curve, 0, n)
	for i := 0; i < n; i++ {
		cv := Curve{StartIndex: int32(c.VarUint()), Type: int32(c.VarUint())}
		sz := CurveDataSize(cv.Type)
		if sz == 0 || c.Err != nil {
			return nil, fmt.Errorf("%w: unknown curve type %d", ErrInvalid, cv.Type)
		}
		cv.Data = append([]byte(nil), c.Bytes(sz)...)
		if c.Err != nil {
			return nil, ErrInvalid
		}
		out = append(out, cv)
	}
	return out, nil
}

// Extent returns the XY bounding box stored in a compressed geometry blob
// without decoding the coordinates. ok is false for null or empty geometries.
func Extent(blob []byte, p Precision) (xmin, ymin, xmax, ymax float64, ok bool) {
	c := &gdbtable.Cursor{B: blob}
	t := uint32(c.VarUint())
	k, _, _ := Classify(t)
	s := &Shape{}
	d := &dec{c: c, p: p}
	switch k {
	case KindPoint:
		x, y := c.VarUint(), c.VarUint()
		if x == 0 || y == 0 || c.Err != nil {
			return 0, 0, 0, 0, false
		}
		px := float64(x-1)/p.XYScale + p.XOrigin
		py := float64(y-1)/p.XYScale + p.YOrigin
		return px, py, px, py, true
	case KindMultipoint:
		if c.VarUint() == 0 {
			return 0, 0, 0, 0, false
		}
	case KindPolyline, KindPolygon:
		if c.VarUint() == 0 {
			return 0, 0, 0, 0, false
		}
		c.VarUint()
		if HasCurves(t) {
			c.VarUint()
		}
	case KindMultiPatch:
		if c.VarUint() == 0 {
			return 0, 0, 0, 0, false
		}
		c.VarUint()
		c.VarUint()
	default:
		return 0, 0, 0, 0, false
	}
	d.bbox(s)
	if c.Err != nil {
		return 0, 0, 0, 0, false
	}
	return s.XMin, s.YMin, s.XMax, s.YMax, true
}

// Encode converts a Shape to the compressed blob stored in rows.
func Encode(s *Shape, p Precision) ([]byte, error) {
	k, hasZ, hasM := Classify(s.Type)
	b := gdbtable.AppendVarUint(nil, uint64(s.Type))
	var err error
	quant := func(v, origin, scale float64) uint64 {
		q := (v-origin)*scale + 0.5
		if math.IsNaN(q) || q < 0 || q >= 1<<63 {
			if err == nil {
				err = fmt.Errorf("%w: coordinate %v cannot be encoded with origin %v and scale %v", ErrInvalid, v, origin, scale)
			}
			return 0
		}
		return uint64(q)
	}
	n := len(s.Points)
	switch k {
	case KindNull:
		return b, nil
	case KindUnknown:
		return nil, fmt.Errorf("%w: unsupported shape type %d", ErrInvalid, s.Type)
	case KindPoint:
		if s.IsEmpty() {
			b = append(b, 0, 0)
			if hasZ {
				b = append(b, 0)
			}
			if hasM {
				b = append(b, 0)
			}
			return b, nil
		}
		pt := s.Points[0]
		b = gdbtable.AppendVarUint(b, quant(pt.X, p.XOrigin, p.XYScale)+1)
		b = gdbtable.AppendVarUint(b, quant(pt.Y, p.YOrigin, p.XYScale)+1)
		if hasZ {
			b = gdbtable.AppendVarUint(b, quant(at(s.Z, 0, 0), p.ZOrigin, p.ZScale)+1)
		}
		if hasM {
			m := at(s.M, 0, math.NaN())
			if math.IsNaN(m) {
				b = append(b, 0)
			} else {
				b = gdbtable.AppendVarUint(b, quant(m, p.MOrigin, p.MScale)+1)
			}
		}
		return b, err
	}

	b = gdbtable.AppendVarUint(b, uint64(n))
	if n == 0 {
		return b, nil
	}
	ext := *s
	ext.ComputeExtent()
	if s.XMin <= ext.XMin && s.YMin <= ext.YMin && s.XMax >= ext.XMax && s.YMax >= ext.YMax &&
		(s.XMin != 0 || s.YMin != 0 || s.XMax != 0 || s.YMax != 0) {
		// Keep the stored envelope: for curves it includes the arcs, not
		// just the control points.
		ext.XMin, ext.YMin, ext.XMax, ext.YMax = s.XMin, s.YMin, s.XMax, s.YMax
	}
	bboxEnc := func() {
		b = gdbtable.AppendVarUint(b, quant(ext.XMin, p.XOrigin, p.XYScale))
		b = gdbtable.AppendVarUint(b, quant(ext.YMin, p.YOrigin, p.XYScale))
		b = gdbtable.AppendVarUint(b, quant(ext.XMax, ext.XMin, p.XYScale))
		b = gdbtable.AppendVarUint(b, quant(ext.YMax, ext.YMin, p.XYScale))
	}
	partCounts := func() error {
		parts := s.Parts
		if len(parts) == 0 {
			parts = []int32{0}
		}
		for i := 0; i+1 < len(parts); i++ {
			c := parts[i+1] - parts[i]
			if c < 0 {
				return fmt.Errorf("%w: part start indexes are not increasing", ErrInvalid)
			}
			b = gdbtable.AppendVarUint(b, uint64(c))
		}
		if int(parts[len(parts)-1]) > n {
			return fmt.Errorf("%w: part start index beyond point count", ErrInvalid)
		}
		return nil
	}
	nParts := len(s.Parts)
	if nParts == 0 {
		nParts = 1
	}
	writeXY := func() {
		var lx, ly int64
		for _, pt := range s.Points {
			x := int64(math.Round((pt.X - p.XOrigin) * p.XYScale))
			y := int64(math.Round((pt.Y - p.YOrigin) * p.XYScale))
			b = gdbtable.AppendVarInt(b, x-lx)
			b = gdbtable.AppendVarInt(b, y-ly)
			lx, ly = x, y
		}
	}
	writeArr := func(v []float64, origin, scale float64, def float64) {
		var last int64
		for i := 0; i < n; i++ {
			x := at(v, i, def)
			q := int64(math.Round((x - origin) * scale))
			b = gdbtable.AppendVarInt(b, q-last)
			last = q
		}
	}
	allNaN := func(v []float64) bool {
		for i := 0; i < n; i++ {
			if !math.IsNaN(at(v, i, math.NaN())) {
				return false
			}
		}
		return true
	}
	writeM := func() {
		if allNaN(s.M) {
			b = append(b, mAbsentMarker)
			return
		}
		writeArr(s.M, p.MOrigin, p.MScale, 0)
	}

	switch k {
	case KindMultipoint:
		bboxEnc()
		writeXY()
		if hasZ {
			writeArr(s.Z, p.ZOrigin, p.ZScale, 0)
		}
		if hasM {
			writeM()
		}
	case KindPolyline, KindPolygon:
		curves := HasCurves(s.Type)
		b = gdbtable.AppendVarUint(b, uint64(nParts))
		if curves {
			b = gdbtable.AppendVarUint(b, uint64(len(s.Curves)))
		}
		bboxEnc()
		if e := partCounts(); e != nil {
			return nil, e
		}
		writeXY()
		if hasZ {
			writeArr(s.Z, p.ZOrigin, p.ZScale, 0)
		}
		if hasM {
			writeM()
		}
		if curves {
			for _, c := range s.Curves {
				if len(c.Data) != CurveDataSize(c.Type) {
					return nil, fmt.Errorf("%w: bad curve data for type %d", ErrInvalid, c.Type)
				}
				b = gdbtable.AppendVarUint(b, uint64(c.StartIndex))
				b = gdbtable.AppendVarUint(b, uint64(c.Type))
				b = append(b, c.Data...)
			}
		}
	case KindMultiPatch:
		size := 4 + 32 + 4 + 4 + 4*nParts + 4*nParts + 16*n
		if hasZ {
			size += 16 + 8*n
		}
		if hasM {
			size += 16 + 8*n
		}
		b = gdbtable.AppendVarUint(b, uint64(size))
		b = gdbtable.AppendVarUint(b, uint64(nParts))
		bboxEnc()
		if e := partCounts(); e != nil {
			return nil, e
		}
		for i := 0; i < nParts; i++ {
			b = gdbtable.AppendVarUint(b, uint64(uint32(at32(s.PartTypes, i))))
		}
		writeXY()
		if hasZ {
			writeArr(s.Z, p.ZOrigin, p.ZScale, 0)
		}
		if hasM {
			writeM()
		}
	}
	return b, err
}
