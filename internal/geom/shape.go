// Package geom converts between the compressed geometry encoding used in
// .gdbtable files and the Esri shape buffer format (the shapefile record
// layout, extended with curves and multipatch parts as described in Esri's
// "extended shape buffer format" document).
package geom

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

var le = binary.LittleEndian

// ErrInvalid is returned for malformed geometry data.
var ErrInvalid = errors.New("geom: invalid geometry")

// Shape type codes (low byte of the shape type).
const (
	TypeNull                     = 0
	TypePoint                    = 1
	TypePolyline                 = 3
	TypePolygon                  = 5
	TypeMultipoint               = 8
	TypePointZ                   = 9
	TypePolylineZ                = 10
	TypePointZM                  = 11
	TypePolylineZM               = 13
	TypePolygonZM                = 15
	TypeMultipointZM             = 18
	TypePolygonZ                 = 19
	TypeMultipointZ              = 20
	TypePointM                   = 21
	TypePolylineM                = 23
	TypePolygonM                 = 25
	TypeMultipointM              = 28
	TypeMultiPatchM              = 31
	TypeMultiPatch               = 32
	TypeGeneralPolyline          = 50
	TypeGeneralPolygon           = 51
	TypeGeneralPoint             = 52
	TypeGeneralMultipoint        = 53
	TypeGeneralMultiPatch        = 54
	ModifierHasZ          uint32 = 0x80000000
	ModifierHasM          uint32 = 0x40000000
	ModifierCurves        uint32 = 0x20000000
	ModifierIDs           uint32 = 0x10000000
	ModifierNormals       uint32 = 0x08000000
	ModifierTexture       uint32 = 0x04000000
	ModifierPartIDs       uint32 = 0x02000000
	ModifierMater         uint32 = 0x01000000
	BasicTypeMask         uint32 = 0xFF
)

// Kind is the geometry family of a shape type.
type Kind int

const (
	KindNull Kind = iota
	KindPoint
	KindMultipoint
	KindPolyline
	KindPolygon
	KindMultiPatch
	KindUnknown
)

// Classify returns the geometry family and the Z/M presence of a shape type.
func Classify(shapeType uint32) (k Kind, hasZ, hasM bool) {
	hasZ = shapeType&ModifierHasZ != 0
	hasM = shapeType&ModifierHasM != 0
	switch shapeType & BasicTypeMask {
	case TypeNull:
		return KindNull, false, false
	case TypePoint, TypeGeneralPoint:
		k = KindPoint
	case TypePointZ:
		k, hasZ = KindPoint, true
	case TypePointZM:
		k, hasZ, hasM = KindPoint, true, true
	case TypePointM:
		k, hasM = KindPoint, true
	case TypeMultipoint, TypeGeneralMultipoint:
		k = KindMultipoint
	case TypeMultipointZ:
		k, hasZ = KindMultipoint, true
	case TypeMultipointZM:
		k, hasZ, hasM = KindMultipoint, true, true
	case TypeMultipointM:
		k, hasM = KindMultipoint, true
	case TypePolyline, TypeGeneralPolyline:
		k = KindPolyline
	case TypePolylineZ:
		k, hasZ = KindPolyline, true
	case TypePolylineZM:
		k, hasZ, hasM = KindPolyline, true, true
	case TypePolylineM:
		k, hasM = KindPolyline, true
	case TypePolygon, TypeGeneralPolygon:
		k = KindPolygon
	case TypePolygonZ:
		k, hasZ = KindPolygon, true
	case TypePolygonZM:
		k, hasZ, hasM = KindPolygon, true, true
	case TypePolygonM:
		k, hasM = KindPolygon, true
	case TypeMultiPatch:
		k, hasZ = KindMultiPatch, true
	case TypeMultiPatchM:
		k, hasZ, hasM = KindMultiPatch, true, true
	case TypeGeneralMultiPatch:
		k = KindMultiPatch
	default:
		return KindUnknown, hasZ, hasM
	}
	return k, hasZ, hasM
}

// HasCurves reports whether a general shape type carries curve segments.
func HasCurves(shapeType uint32) bool {
	b := shapeType & BasicTypeMask
	return shapeType&ModifierCurves != 0 && (b == TypeGeneralPolyline || b == TypeGeneralPolygon)
}

// Point is an XY coordinate.
type Point struct{ X, Y float64 }

// Curve is a non-linear segment description from the extended shape buffer
// format. Data holds the raw, type-specific structure:
//
//	type 1 (circular arc):  2 float64 + int32   (20 bytes)
//	type 4 (bezier):        4 float64           (32 bytes)
//	type 5 (elliptic arc):  5 float64 + int32   (44 bytes)
type Curve struct {
	StartIndex int32
	Type       int32
	Data       []byte
}

// Curve type codes.
const (
	CurveCircularArc = 1
	CurveBezier      = 4
	CurveEllipticArc = 5
)

// CurveDataSize returns the size of the structure following a curve type, or
// 0 for unknown types.
func CurveDataSize(curveType int32) int {
	switch curveType {
	case CurveCircularArc:
		return 2*8 + 4
	case CurveBezier:
		return 4 * 8
	case CurveEllipticArc:
		return 5*8 + 4
	}
	return 0
}

// Shape is a decoded geometry, independent of encoding.
type Shape struct {
	Type      uint32 // shape type including modifier flags
	Points    []Point
	Parts     []int32 // start index of each part (polyline, polygon, multipatch)
	PartTypes []int32 // multipatch part types
	Z         []float64
	M         []float64
	Curves    []Curve

	// Extra holds undecoded multipatch data (normals, textures, materials)
	// found after the coordinates of a compressed geometry.
	Extra []byte

	// Extents. ZRange/MRange are meaningful when Z/M are present.
	XMin, YMin, XMax, YMax float64
	ZMin, ZMax             float64
	MMin, MMax             float64
}

// Kind returns the geometry family of the shape.
func (s *Shape) Kind() Kind { k, _, _ := Classify(s.Type); return k }

// HasZ reports whether the shape carries Z values.
func (s *Shape) HasZ() bool { _, z, _ := Classify(s.Type); return z }

// HasM reports whether the shape carries M values.
func (s *Shape) HasM() bool { _, _, m := Classify(s.Type); return m }

// IsEmpty reports whether the shape has no coordinates.
func (s *Shape) IsEmpty() bool {
	if s.Kind() == KindPoint {
		return len(s.Points) == 0 || math.IsNaN(s.Points[0].X)
	}
	return len(s.Points) == 0
}

// ComputeExtent recomputes the XY, Z and M extents from the coordinates.
func (s *Shape) ComputeExtent() {
	s.XMin, s.YMin, s.XMax, s.YMax = 0, 0, 0, 0
	s.ZMin, s.ZMax, s.MMin, s.MMax = 0, 0, 0, 0
	first := true
	for _, p := range s.Points {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) {
			continue
		}
		if first {
			s.XMin, s.XMax, s.YMin, s.YMax = p.X, p.X, p.Y, p.Y
			first = false
			continue
		}
		s.XMin = math.Min(s.XMin, p.X)
		s.XMax = math.Max(s.XMax, p.X)
		s.YMin = math.Min(s.YMin, p.Y)
		s.YMax = math.Max(s.YMax, p.Y)
	}
	s.ZMin, s.ZMax = minMax(s.Z)
	s.MMin, s.MMax = minMax(s.M)
}

func minMax(v []float64) (lo, hi float64) {
	first := true
	for _, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if first {
			lo, hi, first = x, x, false
			continue
		}
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	if first {
		// No values (or all NaN): use NaN for M, 0 for Z as Esri does.
		return 0, 0
	}
	return lo, hi
}

// Bytes encodes the shape as an Esri shape buffer.
func (s *Shape) Bytes() []byte {
	k, hasZ, hasM := Classify(s.Type)
	b := make([]byte, 0, 64+len(s.Points)*32)
	b = le.AppendUint32(b, s.Type)
	f := func(v float64) { b = le.AppendUint64(b, math.Float64bits(v)) }
	i32 := func(v int32) { b = le.AppendUint32(b, uint32(v)) }
	n := len(s.Points)
	zm := func() {
		if hasZ {
			f(s.ZMin)
			f(s.ZMax)
			for i := 0; i < n; i++ {
				f(at(s.Z, i, 0))
			}
		}
		if hasM {
			f(s.MMin)
			f(s.MMax)
			for i := 0; i < n; i++ {
				f(at(s.M, i, math.NaN()))
			}
		}
	}
	switch k {
	case KindNull, KindUnknown:
		return b
	case KindPoint:
		p := Point{math.NaN(), math.NaN()}
		if n > 0 {
			p = s.Points[0]
		}
		f(p.X)
		f(p.Y)
		if hasZ {
			f(at(s.Z, 0, 0))
		}
		if hasM {
			f(at(s.M, 0, math.NaN()))
		}
		return b
	case KindMultipoint:
		f(s.XMin)
		f(s.YMin)
		f(s.XMax)
		f(s.YMax)
		i32(int32(n))
		for _, p := range s.Points {
			f(p.X)
			f(p.Y)
		}
		zm()
		return b
	default:
		f(s.XMin)
		f(s.YMin)
		f(s.XMax)
		f(s.YMax)
		i32(int32(len(s.Parts)))
		i32(int32(n))
		for _, p := range s.Parts {
			i32(p)
		}
		if k == KindMultiPatch {
			for i := range s.Parts {
				i32(int32(at32(s.PartTypes, i)))
			}
		}
		for _, p := range s.Points {
			f(p.X)
			f(p.Y)
		}
		zm()
		if s.Type&ModifierCurves != 0 && k != KindMultiPatch {
			i32(int32(len(s.Curves)))
			for _, c := range s.Curves {
				i32(c.StartIndex)
				i32(c.Type)
				b = append(b, c.Data...)
			}
		}
		return b
	}
}

func at(v []float64, i int, def float64) float64 {
	if i < len(v) {
		return v[i]
	}
	return def
}

func at32(v []int32, i int) int32 {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// ParseShape decodes an Esri shape buffer.
func ParseShape(buf []byte) (*Shape, error) {
	r := &reader{b: buf}
	s := &Shape{}
	s.Type = r.u32()
	if r.err != nil {
		return nil, ErrInvalid
	}
	k, hasZ, hasM := Classify(s.Type)
	zm := func(n int) {
		if hasZ && r.remaining() > 0 {
			s.ZMin, s.ZMax = r.f64(), r.f64()
			s.Z = r.f64s(n)
		}
		if hasM && r.remaining() > 0 {
			s.MMin, s.MMax = r.f64(), r.f64()
			s.M = r.f64s(n)
		}
	}
	switch k {
	case KindNull:
		return s, nil
	case KindUnknown:
		return nil, fmt.Errorf("%w: unsupported shape type %d", ErrInvalid, s.Type)
	case KindPoint:
		p := Point{r.f64(), r.f64()}
		s.Points = []Point{p}
		if hasZ {
			s.Z = []float64{r.f64()}
		}
		if hasM {
			if r.remaining() >= 8 {
				s.M = []float64{r.f64()}
			} else {
				s.M = []float64{math.NaN()}
			}
		}
		if r.err != nil {
			return nil, ErrInvalid
		}
		s.ComputeExtent()
	case KindMultipoint:
		s.XMin, s.YMin, s.XMax, s.YMax = r.f64(), r.f64(), r.f64(), r.f64()
		n := r.count()
		s.Points = r.points(n)
		zm(n)
	default:
		s.XMin, s.YMin, s.XMax, s.YMax = r.f64(), r.f64(), r.f64(), r.f64()
		nParts := r.count()
		n := r.count()
		s.Parts = r.i32s(nParts)
		if k == KindMultiPatch {
			s.PartTypes = r.i32s(nParts)
		}
		s.Points = r.points(n)
		zm(n)
		if s.Type&ModifierCurves != 0 && k != KindMultiPatch && r.remaining() >= 4 {
			nc := r.count()
			for i := 0; i < nc && r.err == nil; i++ {
				c := Curve{StartIndex: int32(r.u32()), Type: int32(r.u32())}
				sz := CurveDataSize(c.Type)
				if sz == 0 {
					return nil, fmt.Errorf("%w: unknown curve type %d", ErrInvalid, c.Type)
				}
				c.Data = append([]byte(nil), r.bytes(sz)...)
				s.Curves = append(s.Curves, c)
			}
		}
	}
	if r.err != nil {
		return nil, ErrInvalid
	}
	return s, nil
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) remaining() int { return len(r.b) - r.pos }

func (r *reader) bytes(n int) []byte {
	if r.err != nil || n < 0 || r.pos+n > len(r.b) {
		r.err = ErrInvalid
		return nil
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v
}

func (r *reader) u32() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return le.Uint32(b)
}

func (r *reader) count() int {
	v := int32(r.u32())
	if v < 0 || int(v) > len(r.b) {
		r.err = ErrInvalid
		return 0
	}
	return int(v)
}

func (r *reader) f64() float64 {
	b := r.bytes(8)
	if b == nil {
		return 0
	}
	return math.Float64frombits(le.Uint64(b))
}

func (r *reader) f64s(n int) []float64 {
	v := make([]float64, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		v = append(v, r.f64())
	}
	return v
}

func (r *reader) i32s(n int) []int32 {
	v := make([]int32, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		v = append(v, int32(r.u32()))
	}
	return v
}

func (r *reader) points(n int) []Point {
	v := make([]Point, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		v = append(v, Point{r.f64(), r.f64()})
	}
	return v
}
