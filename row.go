package fgdb

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/geom"
)

// Row is a row of a table or of a query result (FileGDBAPI::Row).
//
// Field accessors take a field name (case-insensitive). Value and SetValue
// take a field number, matching the field order of GetFields.
type Row struct {
	table *Table // nil for computed query rows
	defs  []FieldDef
	raw   []int // raw table field index of each field, -1 if computed
	vals  []any // values in gdbtable representation; nil = NULL
	oid   int64
	prec  geom.Precision
}

func (t *Table) newRow(cols []int, vals []any, oid int64) *Row {
	r := &Row{table: t, oid: oid, prec: t.prec}
	r.reset(t, cols)
	for i, c := range cols {
		r.vals[i] = vals[c]
	}
	return r
}

func (r *Row) reset(t *Table, cols []int) {
	if len(r.defs) != len(cols) {
		r.defs = make([]FieldDef, len(cols))
		r.raw = make([]int, len(cols))
		r.vals = make([]any, len(cols))
	}
	for i, c := range cols {
		r.defs[i] = t.defs[c]
		r.raw[i] = c
	}
}

// GetFields returns the definitions of the fields of the row.
func (r *Row) GetFields() []FieldDef {
	out := make([]FieldDef, len(r.defs))
	copy(out, r.defs)
	return out
}

// GetFieldInformation returns indexed access to the fields of the row.
func (r *Row) GetFieldInformation() *FieldInfo { return &FieldInfo{defs: r.GetFields()} }

// FieldIndex returns the position of the named field in the row, or -1.
func (r *Row) FieldIndex(name string) int {
	name = unquoteIdent(strings.TrimSpace(name))
	for i := range r.defs {
		if strings.EqualFold(r.defs[i].Name, name) {
			return i
		}
	}
	return -1
}

func (r *Row) field(name string) (int, error) {
	i := r.FieldIndex(name)
	if i < 0 {
		return -1, fmt.Errorf("%w: %q", ErrFieldNotFound, name)
	}
	return i, nil
}

func (r *Row) typed(name string, types ...FieldType) (any, error) {
	i, err := r.field(name)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, t := range types {
		if r.defs[i].Type == t {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w: field %q is %v", ErrTypeMismatch, r.defs[i].Name, r.defs[i].Type)
	}
	if r.vals[i] == nil {
		return nil, fmt.Errorf("%w: field %q", ErrNullValue, r.defs[i].Name)
	}
	return r.vals[i], nil
}

// IsNull reports whether the field is NULL.
func (r *Row) IsNull(field string) (bool, error) {
	i, err := r.field(field)
	if err != nil {
		return false, err
	}
	return r.vals[i] == nil, nil
}

// GetOID returns the ObjectID of the row.
func (r *Row) GetOID() (int64, error) {
	if r.oid <= 0 {
		return 0, fmt.Errorf("%w: row has no ObjectID", ErrNullValue)
	}
	return r.oid, nil
}

// GetGlobalID returns the value of the GlobalID field.
func (r *Row) GetGlobalID() (Guid, error) {
	for i, d := range r.defs {
		if d.Type == FieldTypeGlobalID {
			if r.vals[i] == nil {
				return Guid{}, fmt.Errorf("%w: field %q", ErrNullValue, d.Name)
			}
			return Guid(r.vals[i].(gdbtable.GUID)), nil
		}
	}
	return Guid{}, fmt.Errorf("%w: no GlobalID field", ErrFieldNotFound)
}

func (r *Row) geometryIndex() (int, error) {
	for i, d := range r.defs {
		if d.Type == FieldTypeGeometry {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%w: no geometry field", ErrFieldNotFound)
}

// GetGeometry returns the geometry of a feature as a shape buffer.
func (r *Row) GetGeometry() (*ShapeBuffer, error) {
	i, err := r.geometryIndex()
	if err != nil {
		return nil, err
	}
	if r.vals[i] == nil {
		return nil, fmt.Errorf("%w: field %q", ErrNullValue, r.defs[i].Name)
	}
	s, err := geom.Decode(r.vals[i].(gdbtable.Geometry), r.prec)
	if err != nil {
		return nil, err
	}
	return &ShapeBuffer{Bytes: s.Bytes()}, nil
}

// GetShort returns the value of a SmallInteger field.
func (r *Row) GetShort(field string) (int16, error) {
	v, err := r.typed(field, FieldTypeSmallInteger)
	if err != nil {
		return 0, err
	}
	return v.(int16), nil
}

// GetInteger returns the value of an Integer (or SmallInteger) field.
func (r *Row) GetInteger(field string) (int32, error) {
	v, err := r.typed(field, FieldTypeInteger, FieldTypeSmallInteger)
	if err != nil {
		return 0, err
	}
	switch x := v.(type) {
	case int16:
		return int32(x), nil
	default:
		return x.(int32), nil
	}
}

// GetBigInteger returns the value of a BigInteger, Integer or SmallInteger
// field.
func (r *Row) GetBigInteger(field string) (int64, error) {
	v, err := r.typed(field, FieldTypeBigInteger, FieldTypeInteger, FieldTypeSmallInteger)
	if err != nil {
		return 0, err
	}
	switch x := v.(type) {
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	default:
		return x.(int64), nil
	}
}

// GetFloat returns the value of a Single field.
func (r *Row) GetFloat(field string) (float32, error) {
	v, err := r.typed(field, FieldTypeSingle)
	if err != nil {
		return 0, err
	}
	return v.(float32), nil
}

// GetDouble returns the value of a Double (or Single) field.
func (r *Row) GetDouble(field string) (float64, error) {
	v, err := r.typed(field, FieldTypeDouble, FieldTypeSingle)
	if err != nil {
		return 0, err
	}
	if f, ok := v.(float32); ok {
		return float64(f), nil
	}
	return v.(float64), nil
}

// GetDate returns the value of a Date, DateOnly, TimeOnly or
// TimestampOffset field. Date values have no time zone and are returned in
// UTC; TimeOnly values are returned on 1899-12-30.
func (r *Row) GetDate(field string) (time.Time, error) {
	v, err := r.typed(field, FieldTypeDate, FieldTypeDateOnly, FieldTypeTimeOnly, FieldTypeTimestampOffset)
	if err != nil {
		return time.Time{}, err
	}
	return toTime(v), nil
}

func toTime(v any) time.Time {
	switch x := v.(type) {
	case gdbtable.DateTime:
		return daysToTime(float64(x))
	case gdbtable.DateOnly:
		return daysToTime(float64(x))
	case gdbtable.TimeOnly:
		return daysToTime(float64(x))
	case gdbtable.DateTimeOffset:
		return offsetTime(x.Days, x.Offset)
	}
	return time.Time{}
}

// GetString returns the value of a String field.
func (r *Row) GetString(field string) (string, error) {
	v, err := r.typed(field, FieldTypeString)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// GetGUID returns the value of a GUID or GlobalID field.
func (r *Row) GetGUID(field string) (Guid, error) {
	v, err := r.typed(field, FieldTypeGUID, FieldTypeGlobalID)
	if err != nil {
		return Guid{}, err
	}
	return Guid(v.(gdbtable.GUID)), nil
}

// GetXML returns the value of an XML field.
func (r *Row) GetXML(field string) (string, error) {
	v, err := r.typed(field, FieldTypeXML)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// GetBinary returns the value of a Blob field.
func (r *Row) GetBinary(field string) ([]byte, error) {
	v, err := r.typed(field, FieldTypeBlob)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), v.([]byte)...), nil
}

// Raster is the value of a raster field. Which member is set depends on the
// raster storage type of the field.
type Raster struct {
	Path string // externally stored raster
	ID   int32  // raster managed by the geodatabase (id in its raster tables)
	Data []byte // raster stored inline
}

// GetRaster returns the value of a Raster field.
func (r *Row) GetRaster(field string) (Raster, error) {
	v, err := r.typed(field, FieldTypeRaster)
	if err != nil {
		return Raster{}, err
	}
	return Raster(v.(gdbtable.Raster)), nil
}

// Get returns the value of a field in Go form (see Value).
func (r *Row) Get(field string) (any, error) {
	i, err := r.field(field)
	if err != nil {
		return nil, err
	}
	return r.Value(i)
}

// Value returns the value of field number i in Go form: int16, int32,
// int64, float32, float64, string, time.Time, Guid, []byte, Raster,
// *ShapeBuffer (geometry) or int64 (ObjectID). NULL is returned as nil.
func (r *Row) Value(i int) (any, error) {
	if i < 0 || i >= len(r.vals) {
		return nil, ErrInvalidArgument
	}
	switch v := r.vals[i].(type) {
	case nil:
		return nil, nil
	case gdbtable.ObjectID:
		return int64(v), nil
	case gdbtable.GUID:
		return Guid(v), nil
	case gdbtable.DateTime, gdbtable.DateOnly, gdbtable.TimeOnly, gdbtable.DateTimeOffset:
		return toTime(v), nil
	case gdbtable.Geometry:
		s, err := geom.Decode(v, r.prec)
		if err != nil {
			return nil, err
		}
		return &ShapeBuffer{Bytes: s.Bytes()}, nil
	case gdbtable.Raster:
		return Raster(v), nil
	case []byte:
		return append([]byte(nil), v...), nil
	default:
		return v, nil
	}
}

// Values returns all field values (see Value).
func (r *Row) Values() ([]any, error) {
	out := make([]any, len(r.vals))
	for i := range r.vals {
		v, err := r.Value(i)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// --- setters ---

func (r *Row) settable(field string) (int, error) {
	i, err := r.field(field)
	if err != nil {
		return -1, err
	}
	if r.table == nil || r.raw[i] < 0 {
		return -1, fmt.Errorf("%w: computed fields cannot be set", ErrNotSupported)
	}
	return i, nil
}

// SetNull sets a nullable field to NULL.
func (r *Row) SetNull(field string) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	if !r.defs[i].IsNullable {
		return fmt.Errorf("%w: %q", ErrNotNullable, r.defs[i].Name)
	}
	r.vals[i] = nil
	return nil
}

func (r *Row) set(field string, v any, types ...FieldType) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	return r.setAt(i, v, types...)
}

func (r *Row) setAt(i int, v any, types ...FieldType) error {
	ok := false
	for _, t := range types {
		if r.defs[i].Type == t {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%w: field %q is %v", ErrTypeMismatch, r.defs[i].Name, r.defs[i].Type)
	}
	r.vals[i] = v
	return nil
}

// SetShort sets a SmallInteger field.
func (r *Row) SetShort(field string, v int16) error {
	return r.set(field, v, FieldTypeSmallInteger)
}

// SetInteger sets an Integer field.
func (r *Row) SetInteger(field string, v int32) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	if r.defs[i].Type == FieldTypeSmallInteger && v >= math.MinInt16 && v <= math.MaxInt16 {
		return r.setAt(i, int16(v), FieldTypeSmallInteger)
	}
	return r.setAt(i, v, FieldTypeInteger)
}

// SetBigInteger sets a BigInteger field.
func (r *Row) SetBigInteger(field string, v int64) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	switch r.defs[i].Type {
	case FieldTypeInteger:
		if v >= math.MinInt32 && v <= math.MaxInt32 {
			return r.setAt(i, int32(v), FieldTypeInteger)
		}
	case FieldTypeSmallInteger:
		if v >= math.MinInt16 && v <= math.MaxInt16 {
			return r.setAt(i, int16(v), FieldTypeSmallInteger)
		}
	}
	return r.setAt(i, v, FieldTypeBigInteger)
}

// SetFloat sets a Single field.
func (r *Row) SetFloat(field string, v float32) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	if r.defs[i].Type == FieldTypeDouble {
		return r.setAt(i, float64(v), FieldTypeDouble)
	}
	return r.setAt(i, v, FieldTypeSingle)
}

// SetDouble sets a Double (or Single) field.
func (r *Row) SetDouble(field string, v float64) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	if r.defs[i].Type == FieldTypeSingle {
		return r.setAt(i, float32(v), FieldTypeSingle)
	}
	return r.setAt(i, v, FieldTypeDouble)
}

// SetDate sets a Date, DateOnly, TimeOnly or TimestampOffset field. For
// Date, DateOnly and TimeOnly fields the wall clock time of v is stored
// (its time zone is dropped); TimestampOffset fields keep the UTC offset.
func (r *Row) SetDate(field string, v time.Time) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	var x any
	switch r.defs[i].Type {
	case FieldTypeDate:
		x = gdbtable.DateTime(timeToDays(v))
	case FieldTypeDateOnly:
		y, m, d := v.Date()
		x = gdbtable.DateOnly(timeToDays(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)))
	case FieldTypeTimeOnly:
		hh, mm, ss := v.Clock()
		x = gdbtable.TimeOnly(timeToDays(time.Date(1899, 12, 30, hh, mm, ss, v.Nanosecond(), time.UTC)))
	case FieldTypeTimestampOffset:
		_, off := v.Zone()
		x = gdbtable.DateTimeOffset{Days: timeToDays(v), Offset: int16(off / 60)}
	}
	return r.setAt(i, x, FieldTypeDate, FieldTypeDateOnly, FieldTypeTimeOnly, FieldTypeTimestampOffset)
}

// SetString sets a String field.
func (r *Row) SetString(field string, v string) error {
	i, err := r.settable(field)
	if err != nil {
		return err
	}
	if d := r.defs[i]; d.Type == FieldTypeString && d.Length > 0 && len([]rune(v)) > d.Length {
		return fmt.Errorf("%w: value longer than %d characters for field %q", ErrInvalidArgument, d.Length, d.Name)
	}
	return r.setAt(i, v, FieldTypeString)
}

// SetGUID sets a GUID field.
func (r *Row) SetGUID(field string, v Guid) error {
	return r.set(field, gdbtable.GUID(v), FieldTypeGUID)
}

// SetXML sets an XML field.
func (r *Row) SetXML(field string, v string) error {
	return r.set(field, v, FieldTypeXML)
}

// SetBinary sets a Blob field.
func (r *Row) SetBinary(field string, v []byte) error {
	return r.set(field, append([]byte(nil), v...), FieldTypeBlob)
}

// SetRaster sets a Raster field.
func (r *Row) SetRaster(field string, v Raster) error {
	return r.set(field, gdbtable.Raster(v), FieldTypeRaster)
}

// SetGeometry sets the geometry of a feature from a shape buffer.
func (r *Row) SetGeometry(sb *ShapeBuffer) error {
	i, err := r.geometryIndex()
	if err != nil {
		return err
	}
	if r.table == nil {
		return fmt.Errorf("%w: computed fields cannot be set", ErrNotSupported)
	}
	if sb == nil {
		return r.SetNull(r.defs[i].Name)
	}
	s, err := sb.parse()
	if err != nil {
		return err
	}
	gd := r.defs[i].GeometryDef
	if k := s.Kind(); k != geom.KindNull && gd != nil && ShapeType(s.Type).GeometryType() != gd.GeometryType {
		return fmt.Errorf("%w: %v shape for a %v feature class", ErrTypeMismatch, ShapeType(s.Type).GeometryType(), gd.GeometryType)
	}
	blob, err := geom.Encode(s, r.prec)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	r.vals[i] = gdbtable.Geometry(blob)
	return nil
}

// SetValue sets field number i from a Go value (the types returned by
// Value; nil sets NULL). Integer and float values are converted to the
// field type when they fit.
func (r *Row) SetValue(i int, v any) error {
	if i < 0 || i >= len(r.defs) {
		return ErrInvalidArgument
	}
	name := r.defs[i].Name
	if v == nil {
		return r.SetNull(name)
	}
	switch r.defs[i].Type {
	case FieldTypeSmallInteger, FieldTypeInteger, FieldTypeBigInteger:
		n, ok := toInt64(v)
		if !ok {
			break
		}
		return r.SetBigInteger(name, n)
	case FieldTypeSingle, FieldTypeDouble:
		f, ok := toFloat64(v)
		if !ok {
			break
		}
		return r.SetDouble(name, f)
	case FieldTypeString:
		if s, ok := v.(string); ok {
			return r.SetString(name, s)
		}
	case FieldTypeXML:
		if s, ok := v.(string); ok {
			return r.SetXML(name, s)
		}
	case FieldTypeDate, FieldTypeDateOnly, FieldTypeTimeOnly, FieldTypeTimestampOffset:
		if t, ok := v.(time.Time); ok {
			return r.SetDate(name, t)
		}
	case FieldTypeGUID:
		switch g := v.(type) {
		case Guid:
			return r.SetGUID(name, g)
		case string:
			x, err := ParseGuid(g)
			if err != nil {
				return err
			}
			return r.SetGUID(name, x)
		}
	case FieldTypeBlob:
		if b, ok := v.([]byte); ok {
			return r.SetBinary(name, b)
		}
	case FieldTypeGeometry:
		if sb, ok := v.(*ShapeBuffer); ok {
			return r.SetGeometry(sb)
		}
	case FieldTypeRaster:
		if x, ok := v.(Raster); ok {
			return r.SetRaster(name, x)
		}
	}
	return fmt.Errorf("%w: cannot assign %T to %v field %q", ErrTypeMismatch, v, r.defs[i].Type, name)
}

// Set sets a field by name from a Go value (see SetValue).
func (r *Row) Set(field string, v any) error {
	i, err := r.field(field)
	if err != nil {
		return err
	}
	return r.SetValue(i, v)
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	}
	if n, ok := toInt64(v); ok {
		return float64(n), true
	}
	return 0, false
}
