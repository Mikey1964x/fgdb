package fgdb

import (
	"crypto/rand"
	"math"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// FieldType is the type of a field (FileGDBAPI::FieldType).
type FieldType int

const (
	FieldTypeSmallInteger    FieldType = 0
	FieldTypeInteger         FieldType = 1
	FieldTypeSingle          FieldType = 2
	FieldTypeDouble          FieldType = 3
	FieldTypeString          FieldType = 4
	FieldTypeDate            FieldType = 5
	FieldTypeOID             FieldType = 6
	FieldTypeGeometry        FieldType = 7
	FieldTypeBlob            FieldType = 8
	FieldTypeRaster          FieldType = 9
	FieldTypeGUID            FieldType = 10
	FieldTypeGlobalID        FieldType = 11
	FieldTypeXML             FieldType = 12
	FieldTypeBigInteger      FieldType = 13 // ArcGIS Pro 3.2+
	FieldTypeDateOnly        FieldType = 14 // ArcGIS Pro 3.2+
	FieldTypeTimeOnly        FieldType = 15 // ArcGIS Pro 3.2+
	FieldTypeTimestampOffset FieldType = 16 // ArcGIS Pro 3.2+
)

var fieldTypeNames = map[FieldType]string{
	FieldTypeSmallInteger:    "esriFieldTypeSmallInteger",
	FieldTypeInteger:         "esriFieldTypeInteger",
	FieldTypeSingle:          "esriFieldTypeSingle",
	FieldTypeDouble:          "esriFieldTypeDouble",
	FieldTypeString:          "esriFieldTypeString",
	FieldTypeDate:            "esriFieldTypeDate",
	FieldTypeOID:             "esriFieldTypeOID",
	FieldTypeGeometry:        "esriFieldTypeGeometry",
	FieldTypeBlob:            "esriFieldTypeBlob",
	FieldTypeRaster:          "esriFieldTypeRaster",
	FieldTypeGUID:            "esriFieldTypeGUID",
	FieldTypeGlobalID:        "esriFieldTypeGlobalID",
	FieldTypeXML:             "esriFieldTypeXML",
	FieldTypeBigInteger:      "esriFieldTypeBigInteger",
	FieldTypeDateOnly:        "esriFieldTypeDateOnly",
	FieldTypeTimeOnly:        "esriFieldTypeTimeOnly",
	FieldTypeTimestampOffset: "esriFieldTypeTimestampOffset",
}

// String returns the Esri name of the type, e.g. "esriFieldTypeInteger".
func (t FieldType) String() string {
	if s, ok := fieldTypeNames[t]; ok {
		return s
	}
	return "esriFieldTypeUnknown"
}

// ParseFieldType converts an Esri type name such as "esriFieldTypeInteger"
// to a FieldType.
func ParseFieldType(s string) (FieldType, bool) {
	for t, n := range fieldTypeNames {
		if n == s {
			return t, true
		}
	}
	return 0, false
}

// GeometryType is the geometry type of a feature class
// (FileGDBAPI::GeometryType).
type GeometryType int

const (
	GeometryNull       GeometryType = 0
	GeometryPoint      GeometryType = 1
	GeometryMultipoint GeometryType = 2
	GeometryPolyline   GeometryType = 3
	GeometryPolygon    GeometryType = 4
	GeometryMultiPatch GeometryType = 9
)

var geometryTypeNames = map[GeometryType]string{
	GeometryNull:       "esriGeometryNull",
	GeometryPoint:      "esriGeometryPoint",
	GeometryMultipoint: "esriGeometryMultipoint",
	GeometryPolyline:   "esriGeometryPolyline",
	GeometryPolygon:    "esriGeometryPolygon",
	GeometryMultiPatch: "esriGeometryMultiPatch",
}

// String returns the Esri name of the geometry type.
func (g GeometryType) String() string {
	if s, ok := geometryTypeNames[g]; ok {
		return s
	}
	return "esriGeometryAny"
}

// ParseGeometryType converts an Esri name such as "esriGeometryPolygon".
func ParseGeometryType(s string) (GeometryType, bool) {
	for t, n := range geometryTypeNames {
		if n == s {
			return t, true
		}
	}
	return 0, false
}

// SpatialReference describes a coordinate system and the precision model of
// a geometry field (FileGDBAPI::SpatialReference).
//
// Resolutions are the inverse of the on-disk scale factors: a coordinate is
// stored as round((x - FalseX) / XYResolution).
type SpatialReference struct {
	WKT  string // Esri WKT; empty for an unknown coordinate system
	WKID int    // well-known ID, when known (0 otherwise)

	FalseX, FalseY float64
	XYResolution   float64
	XYTolerance    float64
	FalseZ         float64
	ZResolution    float64
	ZTolerance     float64
	FalseM         float64
	MResolution    float64
	MTolerance     float64
}

// unknownSRS is the WKT placeholder used for an unknown coordinate system.
const unknownSRS = "{B286C06B-0879-11D2-AACA-00C04FA33C20}"

// GeometryDef describes the geometry of a feature class
// (FileGDBAPI::GeometryDef).
type GeometryDef struct {
	GeometryType     GeometryType
	HasZ             bool
	HasM             bool
	SpatialReference SpatialReference
	// GridSizes holds the spatial index grid sizes (1 to 3 values).
	GridSizes []float64
}

// FieldDef describes a field (FileGDBAPI::FieldDef).
type FieldDef struct {
	Name       string
	Alias      string
	Type       FieldType
	Length     int // maximum length of strings; byte width of other types
	IsNullable bool
	Required   bool
	Editable   bool
	// DefaultValue is the field default, typed like the Row getter of the
	// field (int16, int32, int64, float32, float64, string, time.Time), or
	// nil.
	DefaultValue any
	// GeometryDef is set for geometry fields.
	GeometryDef *GeometryDef
}

// IndexDef describes an attribute or spatial index (FileGDBAPI::IndexDef).
type IndexDef struct {
	Name     string
	Fields   string // indexed field (FileGDB indexes are single-column)
	IsUnique bool
}

// Envelope is an axis-aligned bounding box (FileGDBAPI::Envelope).
type Envelope struct {
	XMin, YMin, XMax, YMax float64
	ZMin, ZMax             float64
}

// NewEnvelope returns an envelope with the given XY bounds.
func NewEnvelope(xmin, ymin, xmax, ymax float64) Envelope {
	return Envelope{XMin: xmin, YMin: ymin, XMax: xmax, YMax: ymax}
}

// IsEmpty reports whether the envelope has NaN bounds or min > max.
func (e Envelope) IsEmpty() bool {
	return math.IsNaN(e.XMin) || math.IsNaN(e.YMin) || math.IsNaN(e.XMax) || math.IsNaN(e.YMax) ||
		e.XMin > e.XMax || e.YMin > e.YMax
}

// Intersects reports whether two envelopes overlap in XY (touching counts).
func (e Envelope) Intersects(o Envelope) bool {
	return !e.IsEmpty() && !o.IsEmpty() &&
		e.XMin <= o.XMax && o.XMin <= e.XMax && e.YMin <= o.YMax && o.YMin <= e.YMax
}

// Guid is a 128-bit identifier stored in GUID and GlobalID fields
// (FileGDBAPI::Guid). The byte layout is the Windows GUID layout used on
// disk.
type Guid [16]byte

// String formats the GUID as {XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}.
func (g Guid) String() string { return gdbtable.GUID(g).String() }

// IsNull reports whether all bytes are zero.
func (g Guid) IsNull() bool { return g == Guid{} }

// ParseGuid parses a GUID string with or without braces.
func ParseGuid(s string) (Guid, error) {
	g, err := gdbtable.ParseGUID(s)
	if err != nil {
		return Guid{}, ErrInvalidArgument
	}
	return Guid(g), nil
}

// NewGuid returns a random (version 4) GUID.
func NewGuid() Guid {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	raw[7] = raw[7]&0x0F | 0x40 // version 4 (high nibble of the little-endian Data3)
	raw[8] = raw[8]&0x3F | 0x80
	return Guid(raw)
}

// FieldInfo gives indexed access to field properties
// (FileGDBAPI::FieldInfo).
type FieldInfo struct {
	defs []FieldDef
}

// FieldCount returns the number of fields.
func (fi *FieldInfo) FieldCount() int { return len(fi.defs) }

func (fi *FieldInfo) get(i int) (*FieldDef, error) {
	if i < 0 || i >= len(fi.defs) {
		return nil, ErrInvalidArgument
	}
	return &fi.defs[i], nil
}

// FieldName returns the name of field i.
func (fi *FieldInfo) FieldName(i int) (string, error) {
	d, err := fi.get(i)
	if err != nil {
		return "", err
	}
	return d.Name, nil
}

// FieldType returns the type of field i.
func (fi *FieldInfo) FieldType(i int) (FieldType, error) {
	d, err := fi.get(i)
	if err != nil {
		return 0, err
	}
	return d.Type, nil
}

// FieldLength returns the length of field i.
func (fi *FieldInfo) FieldLength(i int) (int, error) {
	d, err := fi.get(i)
	if err != nil {
		return 0, err
	}
	return d.Length, nil
}

// FieldIsNullable reports whether field i accepts NULL.
func (fi *FieldInfo) FieldIsNullable(i int) (bool, error) {
	d, err := fi.get(i)
	if err != nil {
		return false, err
	}
	return d.IsNullable, nil
}
