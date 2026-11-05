package gdbtable

import "fmt"

// FieldType is the on-disk field type code.
type FieldType uint8

const (
	TypeInt16              FieldType = 0
	TypeInt32              FieldType = 1
	TypeFloat32            FieldType = 2
	TypeFloat64            FieldType = 3
	TypeString             FieldType = 4
	TypeDateTime           FieldType = 5
	TypeObjectID           FieldType = 6
	TypeGeometry           FieldType = 7
	TypeBinary             FieldType = 8
	TypeRaster             FieldType = 9
	TypeGUID               FieldType = 10
	TypeGlobalID           FieldType = 11
	TypeXML                FieldType = 12
	TypeInt64              FieldType = 13
	TypeDateOnly           FieldType = 14
	TypeTimeOnly           FieldType = 15
	TypeDateTimeWithOffset FieldType = 16
)

func (t FieldType) String() string {
	names := [...]string{"Int16", "Int32", "Float32", "Float64", "String", "DateTime",
		"ObjectID", "Geometry", "Binary", "Raster", "GUID", "GlobalID", "XML",
		"Int64", "DateOnly", "TimeOnly", "DateTimeWithOffset"}
	if int(t) < len(names) {
		return names[t]
	}
	return fmt.Sprintf("FieldType(%d)", uint8(t))
}

// Field flag bits.
const (
	FlagNullable = 1
	FlagRequired = 2
	FlagEditable = 4
)

// Layer geometry types (low byte of the layer flags).
const (
	GeomNone       = 0
	GeomPoint      = 1
	GeomMultipoint = 2
	GeomPolyline   = 3
	GeomPolygon    = 4
	GeomMultiPatch = 9
)

// Field describes one column of a table.
type Field struct {
	Name     string
	Alias    string
	Type     FieldType
	Width    uint8 // width byte as stored (0 for geometry/raster)
	MaxWidth int32 // maximum string length (strings only)
	Flags    uint8
	Default  []byte // raw default value, if any

	Geom   *GeomInfo   // set for geometry fields
	Raster *RasterInfo // set for raster fields
}

func (f *Field) Nullable() bool { return f.Flags&FlagNullable != 0 }
func (f *Field) Required() bool { return f.Flags&FlagRequired != 0 }
func (f *Field) Editable() bool { return f.Flags&FlagEditable != 0 }

// GeomInfo holds the spatial reference and precision model of a geometry
// (or raster) field.
type GeomInfo struct {
	WKT   string
	Flags uint8 // bit 1: has M origin/scale, bit 2: has Z origin/scale

	XOrigin, YOrigin, XYScale float64
	MOrigin, MScale           float64
	ZOrigin, ZScale           float64
	XYTolerance               float64
	MTolerance, ZTolerance    float64

	XMin, YMin, XMax, YMax float64
	ZMin, ZMax             float64
	MMin, MMax             float64

	GridSizes []float64

	// Layer-level dimension flags (copied from the table header).
	HasZ, HasM bool
}

func (g *GeomInfo) hasMOrigin() bool { return g.Flags&2 != 0 }
func (g *GeomInfo) hasZOrigin() bool { return g.Flags&4 != 0 }

// RasterInfo holds raster-specific column information.
type RasterInfo struct {
	ColumnName string
	Geom       GeomInfo
	RasterType uint8 // 0 external, 1 managed, 2 inline
}

// parseFields decodes the repeated per-field part of the field description
// section.
func parseFields(c *Cursor, nFields int, hasZ, hasM bool) ([]*Field, error) {
	fields := make([]*Field, 0, nFields)
	for i := 0; i < nFields; i++ {
		f := &Field{}
		f.Name = c.UTF16(int(c.U8()))
		f.Alias = c.UTF16(int(c.U8()))
		f.Type = FieldType(c.U8())
		if c.Err != nil {
			return nil, c.Err
		}
		if f.Type > TypeDateTimeWithOffset {
			return nil, fmt.Errorf("%w: unknown field type %d for field %q", ErrCorrupt, f.Type, f.Name)
		}
		switch f.Type {
		case TypeGeometry, TypeRaster:
			f.Width = c.U8()
			f.Flags = c.U8()
			var gi *GeomInfo
			if f.Type == TypeRaster {
				f.Raster = &RasterInfo{}
				f.Raster.ColumnName = c.UTF16(int(c.U8()))
				gi = &f.Raster.Geom
			} else {
				f.Geom = &GeomInfo{HasZ: hasZ, HasM: hasM}
				gi = f.Geom
			}
			wktLen := int(c.U16())
			gi.WKT = DecodeUTF16(c.Bytes(wktLen))
			gi.Flags = c.U8()
			if f.Type == TypeGeometry || gi.Flags != 0 {
				gi.XOrigin = c.F64()
				gi.YOrigin = c.F64()
				gi.XYScale = c.F64()
				if gi.hasMOrigin() {
					gi.MOrigin = c.F64()
					gi.MScale = c.F64()
				}
				if gi.hasZOrigin() {
					gi.ZOrigin = c.F64()
					gi.ZScale = c.F64()
				}
				gi.XYTolerance = c.F64()
				if gi.hasMOrigin() {
					gi.MTolerance = c.F64()
				}
				if gi.hasZOrigin() {
					gi.ZTolerance = c.F64()
				}
			}
			if f.Type == TypeRaster {
				f.Raster.RasterType = c.U8()
			} else {
				gi.XMin = c.F64()
				gi.YMin = c.F64()
				gi.XMax = c.F64()
				gi.YMax = c.F64()
				if hasZ {
					gi.ZMin = c.F64()
					gi.ZMax = c.F64()
				}
				if hasM {
					gi.MMin = c.F64()
					gi.MMax = c.F64()
				}
				c.U8() // always 0
				n := c.U32()
				if c.Err == nil && (n == 0 || n > 3) {
					return nil, fmt.Errorf("%w: invalid spatial index grid count %d", ErrCorrupt, n)
				}
				for j := uint32(0); j < n && c.Err == nil; j++ {
					gi.GridSizes = append(gi.GridSizes, c.F64())
				}
			}
		case TypeString:
			f.MaxWidth = int32(c.U32())
			f.Flags = c.U8()
			n := c.VarUint()
			if n > 0 {
				f.Default = append([]byte(nil), c.Bytes(int(n))...)
			}
		case TypeObjectID, TypeBinary, TypeGUID, TypeGlobalID, TypeXML:
			f.Width = c.U8()
			f.Flags = c.U8()
		default:
			f.Width = c.U8()
			f.Flags = c.U8()
			n := int(c.U8())
			if n > 0 {
				f.Default = append([]byte(nil), c.Bytes(n)...)
			}
		}
		if c.Err != nil {
			return nil, fmt.Errorf("%w: truncated description of field %q", ErrCorrupt, f.Name)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// appendFields encodes field descriptions (inverse of parseFields).
func appendFields(b []byte, fields []*Field, hasZ, hasM bool) []byte {
	putName := func(s string) {
		u := EncodeUTF16(s)
		b = append(b, byte(len(u)/2))
		b = append(b, u...)
	}
	for _, f := range fields {
		putName(f.Name)
		putName(f.Alias)
		b = append(b, byte(f.Type))
		switch f.Type {
		case TypeGeometry, TypeRaster:
			b = append(b, f.Width, f.Flags)
			var gi *GeomInfo
			if f.Type == TypeRaster {
				putName(f.Raster.ColumnName)
				gi = &f.Raster.Geom
			} else {
				gi = f.Geom
			}
			w := EncodeUTF16(gi.WKT)
			b = appendU16(b, uint16(len(w)))
			b = append(b, w...)
			b = append(b, gi.Flags)
			if f.Type == TypeGeometry || gi.Flags != 0 {
				b = appendF64(b, gi.XOrigin)
				b = appendF64(b, gi.YOrigin)
				b = appendF64(b, gi.XYScale)
				if gi.hasMOrigin() {
					b = appendF64(b, gi.MOrigin)
					b = appendF64(b, gi.MScale)
				}
				if gi.hasZOrigin() {
					b = appendF64(b, gi.ZOrigin)
					b = appendF64(b, gi.ZScale)
				}
				b = appendF64(b, gi.XYTolerance)
				if gi.hasMOrigin() {
					b = appendF64(b, gi.MTolerance)
				}
				if gi.hasZOrigin() {
					b = appendF64(b, gi.ZTolerance)
				}
			}
			if f.Type == TypeRaster {
				b = append(b, f.Raster.RasterType)
			} else {
				b = appendF64(b, gi.XMin)
				b = appendF64(b, gi.YMin)
				b = appendF64(b, gi.XMax)
				b = appendF64(b, gi.YMax)
				if hasZ {
					b = appendF64(b, gi.ZMin)
					b = appendF64(b, gi.ZMax)
				}
				if hasM {
					b = appendF64(b, gi.MMin)
					b = appendF64(b, gi.MMax)
				}
				b = append(b, 0)
				b = appendU32(b, uint32(len(gi.GridSizes)))
				for _, g := range gi.GridSizes {
					b = appendF64(b, g)
				}
			}
		case TypeString:
			b = appendU32(b, uint32(f.MaxWidth))
			b = append(b, f.Flags)
			b = AppendVarUint(b, uint64(len(f.Default)))
			b = append(b, f.Default...)
		case TypeObjectID, TypeBinary, TypeGUID, TypeGlobalID, TypeXML:
			b = append(b, f.Width, f.Flags)
		default:
			b = append(b, f.Width, f.Flags, byte(len(f.Default)))
			b = append(b, f.Default...)
		}
	}
	return b
}
