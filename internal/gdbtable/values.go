package gdbtable

import (
	"fmt"
	"unicode/utf8"
)

// Value types returned by DecodeRow, per field type:
//
//	Int16              int16
//	Int32              int32
//	Int64              int64
//	Float32            float32
//	Float64            float64
//	String, XML        string
//	DateTime           DateTime
//	DateOnly           DateOnly
//	TimeOnly           TimeOnly
//	DateTimeWithOffset DateTimeOffset
//	ObjectID           ObjectID
//	Geometry           Geometry (compressed geometry blob)
//	Binary             []byte
//	Raster             Raster
//	GUID, GlobalID     GUID
//
// A nil value means NULL.
type (
	DateTime float64 // days since 1899-12-30 00:00:00
	DateOnly float64 // days since 1899-12-30
	TimeOnly float64 // fraction of a day
	ObjectID int64
	Geometry []byte
	GUID     [16]byte
)

// DateTimeOffset is a date-time with a UTC offset in minutes.
type DateTimeOffset struct {
	Days   float64
	Offset int16
}

// Raster is the value of a raster field. Which member is set depends on the
// field's raster type.
type Raster struct {
	Path string // external raster (type 0)
	ID   int32  // managed raster (type 1)
	Data []byte // inline raster (type 2)
}

// String formats the GUID in registry format, e.g. {01234567-89AB-...}.
func (g GUID) String() string {
	return fmt.Sprintf("{%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g[3], g[2], g[1], g[0], g[5], g[4], g[7], g[6], g[8], g[9],
		g[10], g[11], g[12], g[13], g[14], g[15])
}

// ParseGUID parses a GUID in registry format (braces optional).
func ParseGUID(s string) (GUID, error) {
	var g GUID
	if len(s) == 38 && s[0] == '{' && s[37] == '}' {
		s = s[1:37]
	}
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return g, fmt.Errorf("invalid GUID %q", s)
	}
	hex := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	var raw [16]byte
	for i := 0; i < 16; i++ {
		hi, ok1 := unhex(hex[2*i])
		lo, ok2 := unhex(hex[2*i+1])
		if !ok1 || !ok2 {
			return g, fmt.Errorf("invalid GUID %q", s)
		}
		raw[i] = hi<<4 | lo
	}
	// raw is in display order; convert to on-disk (mixed-endian) order.
	g[0], g[1], g[2], g[3] = raw[3], raw[2], raw[1], raw[0]
	g[4], g[5] = raw[5], raw[4]
	g[6], g[7] = raw[7], raw[6]
	copy(g[8:], raw[8:])
	return g, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// DecodeRow decodes a row blob into one value per field.
func (t *Table) DecodeRow(blob []byte, fid int64) ([]any, error) {
	vals := make([]any, len(t.Fields))
	c := &Cursor{B: blob, Pos: t.nullBytes}
	nulls := blob[:t.nullBytes]
	iNull := 0
	for i, f := range t.Fields {
		if f.Type == TypeObjectID {
			vals[i] = ObjectID(fid)
			continue
		}
		if f.Nullable() {
			isNull := testBit(nulls, iNull)
			iNull++
			if isNull {
				continue
			}
		}
		v, err := t.decodeValue(c, f)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		vals[i] = v
	}
	return vals, nil
}

func (t *Table) decodeValue(c *Cursor, f *Field) (any, error) {
	var v any
	switch f.Type {
	case TypeInt16:
		v = int16(c.U16())
	case TypeInt32:
		v = int32(c.U32())
	case TypeInt64:
		v = int64(c.U64())
	case TypeFloat32:
		b := c.Bytes(4)
		if b != nil {
			v = f32(b)
		}
	case TypeFloat64:
		v = c.F64()
	case TypeDateTime:
		v = DateTime(c.F64())
	case TypeDateOnly:
		v = DateOnly(c.F64())
	case TypeTimeOnly:
		v = TimeOnly(c.F64())
	case TypeDateTimeWithOffset:
		d := c.F64()
		v = DateTimeOffset{Days: d, Offset: int16(c.U16())}
	case TypeString:
		b := c.Bytes(int(c.VarUint()))
		if t.StringsUTF8() {
			v = string(b)
		} else {
			v = DecodeUTF16(b)
		}
	case TypeXML:
		v = string(c.Bytes(int(c.VarUint())))
	case TypeGeometry:
		v = Geometry(append([]byte(nil), c.Bytes(int(c.VarUint()))...))
	case TypeBinary:
		v = append([]byte(nil), c.Bytes(int(c.VarUint()))...)
	case TypeGUID, TypeGlobalID:
		var g GUID
		copy(g[:], c.Bytes(16))
		v = g
	case TypeRaster:
		switch f.Raster.RasterType {
		case 1:
			v = Raster{ID: int32(c.U32())}
		case 0:
			v = Raster{Path: DecodeUTF16(c.Bytes(int(c.VarUint())))}
		default:
			v = Raster{Data: append([]byte(nil), c.Bytes(int(c.VarUint()))...)}
		}
	default:
		return nil, fmt.Errorf("%w: unsupported field type %v", ErrCorrupt, f.Type)
	}
	if c.Err != nil {
		return nil, c.Err
	}
	return v, nil
}

// EncodeRow encodes values (one per field, nil for NULL) into a row blob.
// The ObjectID value is ignored.
func (t *Table) EncodeRow(vals []any) ([]byte, error) {
	if len(vals) != len(t.Fields) {
		return nil, fmt.Errorf("gdbtable: got %d values for %d fields", len(vals), len(t.Fields))
	}
	b := make([]byte, t.nullBytes, 64)
	for i := range b {
		b[i] = 0xFF
	}
	iNull := 0
	for i, f := range t.Fields {
		if f.Type == TypeObjectID {
			continue
		}
		v := vals[i]
		if f.Nullable() {
			if v == nil {
				iNull++
				continue
			}
			clearBit(b[:t.nullBytes], iNull)
			iNull++
		} else if v == nil {
			return nil, fmt.Errorf("gdbtable: field %q is not nullable", f.Name)
		}
		var err error
		b, err = t.encodeValue(b, f, v)
		if err != nil {
			return nil, fmt.Errorf("gdbtable: field %q: %w", f.Name, err)
		}
	}
	return b, nil
}

func (t *Table) encodeValue(b []byte, f *Field, v any) ([]byte, error) {
	bad := func() ([]byte, error) {
		return nil, fmt.Errorf("value of type %T not valid for %v field", v, f.Type)
	}
	switch f.Type {
	case TypeInt16:
		x, ok := v.(int16)
		if !ok {
			return bad()
		}
		return appendU16(b, uint16(x)), nil
	case TypeInt32:
		x, ok := v.(int32)
		if !ok {
			return bad()
		}
		return appendU32(b, uint32(x)), nil
	case TypeInt64:
		x, ok := v.(int64)
		if !ok {
			return bad()
		}
		return appendU64(b, uint64(x)), nil
	case TypeFloat32:
		x, ok := v.(float32)
		if !ok {
			return bad()
		}
		return le.AppendUint32(b, f32bits(x)), nil
	case TypeFloat64:
		x, ok := v.(float64)
		if !ok {
			return bad()
		}
		return appendF64(b, x), nil
	case TypeDateTime:
		x, ok := v.(DateTime)
		if !ok {
			return bad()
		}
		return appendF64(b, float64(x)), nil
	case TypeDateOnly:
		x, ok := v.(DateOnly)
		if !ok {
			return bad()
		}
		return appendF64(b, float64(x)), nil
	case TypeTimeOnly:
		x, ok := v.(TimeOnly)
		if !ok {
			return bad()
		}
		return appendF64(b, float64(x)), nil
	case TypeDateTimeWithOffset:
		x, ok := v.(DateTimeOffset)
		if !ok {
			return bad()
		}
		b = appendF64(b, x.Days)
		return appendU16(b, uint16(x.Offset)), nil
	case TypeString, TypeXML:
		s, ok := v.(string)
		if !ok {
			return bad()
		}
		var raw []byte
		if f.Type == TypeXML || t.StringsUTF8() {
			if !utf8.ValidString(s) {
				return nil, fmt.Errorf("string is not valid UTF-8")
			}
			raw = []byte(s)
		} else {
			raw = EncodeUTF16(s)
		}
		b = AppendVarUint(b, uint64(len(raw)))
		return append(b, raw...), nil
	case TypeGeometry:
		g, ok := v.(Geometry)
		if !ok {
			return bad()
		}
		b = AppendVarUint(b, uint64(len(g)))
		return append(b, g...), nil
	case TypeBinary:
		x, ok := v.([]byte)
		if !ok {
			return bad()
		}
		b = AppendVarUint(b, uint64(len(x)))
		return append(b, x...), nil
	case TypeGUID, TypeGlobalID:
		g, ok := v.(GUID)
		if !ok {
			return bad()
		}
		return append(b, g[:]...), nil
	case TypeRaster:
		r, ok := v.(Raster)
		if !ok {
			return bad()
		}
		switch f.Raster.RasterType {
		case 1:
			return appendU32(b, uint32(r.ID)), nil
		case 0:
			p := EncodeUTF16(r.Path)
			b = AppendVarUint(b, uint64(len(p)))
			return append(b, p...), nil
		default:
			b = AppendVarUint(b, uint64(len(r.Data)))
			return append(b, r.Data...), nil
		}
	}
	return bad()
}
