package fgdb

import (
	"github.com/Mikey1964x/fgdb/internal/geom"
)

// ShapeType is an Esri shape type code, possibly combined with modifier
// flags (FileGDBAPI::ShapeType and ShapeModifiers).
type ShapeType uint32

const (
	ShapeNull              ShapeType = 0
	ShapePoint             ShapeType = 1
	ShapePointM            ShapeType = 21
	ShapePointZM           ShapeType = 11
	ShapePointZ            ShapeType = 9
	ShapeMultipoint        ShapeType = 8
	ShapeMultipointM       ShapeType = 28
	ShapeMultipointZM      ShapeType = 18
	ShapeMultipointZ       ShapeType = 20
	ShapePolyline          ShapeType = 3
	ShapePolylineM         ShapeType = 23
	ShapePolylineZM        ShapeType = 13
	ShapePolylineZ         ShapeType = 10
	ShapePolygon           ShapeType = 5
	ShapePolygonM          ShapeType = 25
	ShapePolygonZM         ShapeType = 15
	ShapePolygonZ          ShapeType = 19
	ShapeMultiPatchM       ShapeType = 31
	ShapeMultiPatch        ShapeType = 32
	ShapeGeneralPolyline   ShapeType = 50
	ShapeGeneralPolygon    ShapeType = 51
	ShapeGeneralPoint      ShapeType = 52
	ShapeGeneralMultipoint ShapeType = 53
	ShapeGeneralMultiPatch ShapeType = 54
)

// Shape type modifier flags, combined with the General* shape types.
const (
	ShapeHasZs       ShapeType = 0x80000000
	ShapeHasMs       ShapeType = 0x40000000
	ShapeHasCurves   ShapeType = 0x20000000
	ShapeHasIDs      ShapeType = 0x10000000
	ShapeHasNormals  ShapeType = 0x08000000
	ShapeHasTextures ShapeType = 0x04000000
	ShapeHasPartIDs  ShapeType = 0x02000000
	ShapeHasMaterial ShapeType = 0x01000000

	ShapeBasicTypeMask ShapeType = 0xFF
)

// Multipatch part types.
const (
	PatchTriangleStrip = 0
	PatchTriangleFan   = 1
	PatchOuterRing     = 2
	PatchInnerRing     = 3
	PatchFirstRing     = 4
	PatchRing          = 5
	PatchTriangles     = 6
)

// Curve types of a curve segment.
const (
	CurveTypeCircularArc = 1
	CurveTypeBezier      = 4
	CurveTypeEllipticArc = 5
)

// Point is an XY coordinate.
type Point struct{ X, Y float64 }

// Curve is a non-linear segment of a polyline or polygon, as stored in the
// extended shape buffer format. StartIndex is the index of the segment's
// first point; Data holds the type-specific parameters (see Esri's
// extended_shape_buffer_format.pdf).
type Curve struct {
	StartIndex int32
	Type       int32
	Data       []byte
}

// Geometry is a decoded shape: the fields of the Esri shape buffer in Go
// form. Parts and PartTypes are used by polylines, polygons and multipatches.
type Geometry struct {
	ShapeType ShapeType
	Points    []Point
	Parts     []int32 // index of the first point of each part
	PartTypes []int32 // multipatch part types (Patch*)
	Zs        []float64
	Ms        []float64
	Curves    []Curve
}

// ShapeBuffer holds a geometry in Esri shape buffer format
// (FileGDBAPI::ShapeBuffer). Bytes can be passed to or obtained from other
// software that reads shapefile-style geometry records.
type ShapeBuffer struct {
	Bytes []byte
}

// NewShapeBuffer encodes g as a shape buffer. Extents are computed from the
// coordinates.
func NewShapeBuffer(g *Geometry) *ShapeBuffer {
	s := toInternalShape(g)
	s.ComputeExtent()
	return &ShapeBuffer{Bytes: s.Bytes()}
}

func toInternalShape(g *Geometry) *geom.Shape {
	s := &geom.Shape{
		Type:      uint32(g.ShapeType),
		Parts:     g.Parts,
		PartTypes: g.PartTypes,
		Z:         g.Zs,
		M:         g.Ms,
	}
	s.Points = make([]geom.Point, len(g.Points))
	for i, p := range g.Points {
		s.Points[i] = geom.Point(p)
	}
	for _, c := range g.Curves {
		s.Curves = append(s.Curves, geom.Curve(c))
	}
	return s
}

func fromInternalShape(s *geom.Shape) *Geometry {
	g := &Geometry{
		ShapeType: ShapeType(s.Type),
		Parts:     s.Parts,
		PartTypes: s.PartTypes,
		Zs:        s.Z,
		Ms:        s.M,
	}
	g.Points = make([]Point, len(s.Points))
	for i, p := range s.Points {
		g.Points[i] = Point(p)
	}
	for _, c := range s.Curves {
		g.Curves = append(g.Curves, Curve(c))
	}
	return g
}

func (sb *ShapeBuffer) parse() (*geom.Shape, error) {
	if sb == nil || len(sb.Bytes) < 4 {
		return nil, ErrInvalidArgument
	}
	s, err := geom.ParseShape(sb.Bytes)
	if err != nil {
		return nil, ErrInvalidArgument
	}
	return s, nil
}

// Geometry decodes the shape buffer.
func (sb *ShapeBuffer) Geometry() (*Geometry, error) {
	s, err := sb.parse()
	if err != nil {
		return nil, err
	}
	return fromInternalShape(s), nil
}

// ShapeType returns the shape type, including modifier flags.
func (sb *ShapeBuffer) ShapeType() (ShapeType, error) {
	if sb == nil || len(sb.Bytes) < 4 {
		return 0, ErrInvalidArgument
	}
	return ShapeType(le32(sb.Bytes)), nil
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// GeometryType returns the geometry family of the shape.
func (sb *ShapeBuffer) GeometryType() (GeometryType, error) {
	t, err := sb.ShapeType()
	if err != nil {
		return 0, err
	}
	return t.GeometryType(), nil
}

// GeometryType returns the geometry family of a shape type.
func (t ShapeType) GeometryType() GeometryType {
	k, _, _ := geom.Classify(uint32(t))
	switch k {
	case geom.KindPoint:
		return GeometryPoint
	case geom.KindMultipoint:
		return GeometryMultipoint
	case geom.KindPolyline:
		return GeometryPolyline
	case geom.KindPolygon:
		return GeometryPolygon
	case geom.KindMultiPatch:
		return GeometryMultiPatch
	}
	return GeometryNull
}

// HasZs reports whether a shape type carries Z values.
func (t ShapeType) HasZs() bool { _, z, _ := geom.Classify(uint32(t)); return z }

// HasMs reports whether a shape type carries M values.
func (t ShapeType) HasMs() bool { _, _, m := geom.Classify(uint32(t)); return m }

// HasCurves reports whether a shape type carries curve segments.
func (t ShapeType) HasCurves() bool { return geom.HasCurves(uint32(t)) }

// IsEmpty reports whether the shape is null or has no coordinates.
func (sb *ShapeBuffer) IsEmpty() bool {
	s, err := sb.parse()
	return err != nil || s.Kind() == geom.KindNull || s.IsEmpty()
}

// Extent returns the XY extent stored in the shape buffer.
func (sb *ShapeBuffer) Extent() (Envelope, error) {
	s, err := sb.parse()
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{XMin: s.XMin, YMin: s.YMin, XMax: s.XMax, YMax: s.YMax, ZMin: s.ZMin, ZMax: s.ZMax}, nil
}

// ZExtent returns the Z range of the shape.
func (sb *ShapeBuffer) ZExtent() (zmin, zmax float64, err error) {
	s, err := sb.parse()
	if err != nil {
		return 0, 0, err
	}
	return s.ZMin, s.ZMax, nil
}

// MExtent returns the M range of the shape.
func (sb *ShapeBuffer) MExtent() (mmin, mmax float64, err error) {
	s, err := sb.parse()
	if err != nil {
		return 0, 0, err
	}
	return s.MMin, s.MMax, nil
}

// Points returns the XY coordinates of the shape.
func (sb *ShapeBuffer) Points() ([]Point, error) {
	g, err := sb.Geometry()
	if err != nil {
		return nil, err
	}
	return g.Points, nil
}

// Parts returns the start index of each part.
func (sb *ShapeBuffer) Parts() ([]int32, error) {
	g, err := sb.Geometry()
	if err != nil {
		return nil, err
	}
	return g.Parts, nil
}

// Zs returns the Z values, or nil if the shape has none.
func (sb *ShapeBuffer) Zs() ([]float64, error) {
	g, err := sb.Geometry()
	if err != nil {
		return nil, err
	}
	return g.Zs, nil
}

// Ms returns the M values, or nil if the shape has none.
func (sb *ShapeBuffer) Ms() ([]float64, error) {
	g, err := sb.Geometry()
	if err != nil {
		return nil, err
	}
	return g.Ms, nil
}

// Curves returns the curve segments of the shape.
func (sb *ShapeBuffer) Curves() ([]Curve, error) {
	g, err := sb.Geometry()
	if err != nil {
		return nil, err
	}
	return g.Curves, nil
}
