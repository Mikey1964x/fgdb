package fgdb

import (
	"encoding/xml"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/geom"
	"github.com/Mikey1964x/fgdb/internal/index"
)

// Table is an open table or feature class (FileGDBAPI::Table).
// A Table must not be used from several goroutines at once.
type Table struct {
	gdb    *Geodatabase
	name   string
	item   *item
	raw    *gdbtable.Table
	defs   []FieldDef
	prec   geom.Precision
	closed bool
	dirty  bool // rows changed since the indexes were rebuilt
	refs   int  // number of OpenTable calls sharing this table (update mode)
}

func newTable(g *Geodatabase, name string, raw *gdbtable.Table, it *item) *Table {
	t := &Table{gdb: g, name: name, raw: raw, item: it}
	t.refreshSchema()
	return t
}

func (t *Table) refreshSchema() {
	var meta *itemDefinition
	if t.item != nil {
		meta = parseItemDefinition(t.item.definition)
	}
	t.defs = make([]FieldDef, len(t.raw.Fields))
	for i, f := range t.raw.Fields {
		t.defs[i] = fieldDefFromRaw(t.raw, f, meta)
	}
	if t.raw.GeomField >= 0 {
		t.prec = geom.PrecisionFrom(t.raw.Fields[t.raw.GeomField].Geom)
	}
}

// itemDefinition holds the parts of a GDB_Items definition XML used to
// complete the information stored in the table header.
type itemDefinition struct {
	Fields []struct {
		Name      string `xml:"Name"`
		AliasName string `xml:"AliasName"`
	} `xml:"GPFieldInfoExs>GPFieldInfoEx"`
	SpatialReference struct {
		WKID       int `xml:"WKID"`
		LatestWKID int `xml:"LatestWKID"`
	} `xml:"SpatialReference"`
	AliasName string `xml:"AliasName"`
}

func parseItemDefinition(s string) *itemDefinition {
	if s == "" {
		return nil
	}
	var d itemDefinition
	if err := xml.Unmarshal([]byte(s), &d); err != nil {
		return nil
	}
	return &d
}

func fieldDefFromRaw(t *gdbtable.Table, f *gdbtable.Field, meta *itemDefinition) FieldDef {
	d := FieldDef{
		Name:       f.Name,
		Alias:      f.Alias,
		Type:       FieldType(f.Type),
		Length:     int(f.Width),
		IsNullable: f.Nullable(),
		Required:   f.Required(),
		Editable:   f.Editable(),
	}
	if f.Type == gdbtable.TypeString {
		d.Length = int(f.MaxWidth)
	}
	if d.Alias == "" && meta != nil {
		for _, mf := range meta.Fields {
			if strings.EqualFold(mf.Name, f.Name) {
				d.Alias = mf.AliasName
			}
		}
	}
	if d.Alias == "" {
		d.Alias = f.Name
	}
	if len(f.Default) > 0 {
		d.DefaultValue = decodeDefault(t, f)
	}
	if f.Geom != nil {
		gd := &GeometryDef{
			GeometryType: GeometryType(t.GeomType()),
			HasZ:         t.HasZ(),
			HasM:         t.HasM(),
			GridSizes:    append([]float64(nil), f.Geom.GridSizes...),
		}
		gd.SpatialReference = spatialRefFromRaw(f.Geom)
		if meta != nil {
			gd.SpatialReference.WKID = meta.SpatialReference.LatestWKID
			if gd.SpatialReference.WKID == 0 {
				gd.SpatialReference.WKID = meta.SpatialReference.WKID
			}
		}
		d.GeometryDef = gd
	}
	return d
}

func spatialRefFromRaw(g *gdbtable.GeomInfo) SpatialReference {
	inv := func(s float64) float64 {
		if s == 0 {
			return 0
		}
		return 1 / s
	}
	sr := SpatialReference{
		WKT:          g.WKT,
		FalseX:       g.XOrigin,
		FalseY:       g.YOrigin,
		XYResolution: inv(g.XYScale),
		XYTolerance:  g.XYTolerance,
		FalseZ:       g.ZOrigin,
		ZResolution:  inv(g.ZScale),
		ZTolerance:   g.ZTolerance,
		FalseM:       g.MOrigin,
		MResolution:  inv(g.MScale),
		MTolerance:   g.MTolerance,
	}
	if sr.WKT == unknownSRS {
		sr.WKT = ""
	}
	return sr
}

func decodeDefault(t *gdbtable.Table, f *gdbtable.Field) any {
	b := f.Default
	c := &gdbtable.Cursor{B: b}
	switch f.Type {
	case gdbtable.TypeString:
		if t.StringsUTF8() {
			return string(b)
		}
		return gdbtable.DecodeUTF16(b)
	case gdbtable.TypeInt16:
		if len(b) == 2 {
			return int16(c.U16())
		}
	case gdbtable.TypeInt32:
		if len(b) == 4 {
			return int32(c.U32())
		}
	case gdbtable.TypeInt64:
		if len(b) == 8 {
			return int64(c.U64())
		}
	case gdbtable.TypeFloat32:
		if len(b) == 4 {
			return math.Float32frombits(c.U32())
		}
	case gdbtable.TypeFloat64:
		if len(b) == 8 {
			return c.F64()
		}
	case gdbtable.TypeDateTime, gdbtable.TypeDateOnly:
		if len(b) == 8 {
			return daysToTime(c.F64())
		}
	case gdbtable.TypeTimeOnly:
		if len(b) == 8 {
			return daysToTime(c.F64())
		}
	case gdbtable.TypeDateTimeWithOffset:
		if len(b) == 10 {
			d := c.F64()
			return offsetTime(d, int16(c.U16()))
		}
	}
	return nil
}

// Name returns the table name.
func (t *Table) Name() string { return t.name }

// Path returns the catalog path of the table, e.g. `\Dataset\Roads`.
func (t *Table) Path() string {
	if t.item != nil && t.item.path != "" {
		return t.item.path
	}
	return `\` + t.name
}

// Close closes the table. Pending writes are flushed and the indexes of
// the table are rebuilt.
func (t *Table) Close() error {
	if t.closed {
		return nil
	}
	if t.gdb.release(t) {
		return nil // still opened elsewhere
	}
	t.closed = true
	err := t.flush()
	if e := t.raw.Close(); err == nil {
		err = e
	}
	return err
}

func (t *Table) check() error {
	if t.closed {
		return ErrClosed
	}
	return nil
}

// GetDefinition returns the XML definition of the table, as stored in the
// geodatabase item catalog.
func (t *Table) GetDefinition() (string, error) {
	if err := t.check(); err != nil {
		return "", err
	}
	if t.item == nil {
		return "", fmt.Errorf("%w: table %q has no catalog definition", ErrNotSupported, t.name)
	}
	return t.item.definition, nil
}

// GetDocumentation returns the XML metadata of the table.
func (t *Table) GetDocumentation() (string, error) {
	if err := t.check(); err != nil {
		return "", err
	}
	if t.item == nil {
		return "", nil
	}
	return t.item.documentation, nil
}

// GetFields returns the field definitions.
func (t *Table) GetFields() ([]FieldDef, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	out := make([]FieldDef, len(t.defs))
	copy(out, t.defs)
	return out, nil
}

// GetFieldInformation returns indexed access to the fields.
func (t *Table) GetFieldInformation() (*FieldInfo, error) {
	defs, err := t.GetFields()
	if err != nil {
		return nil, err
	}
	return &FieldInfo{defs: defs}, nil
}

// GetIndexes returns the indexes of the table, including the ObjectID
// (FDO_OBJECTID) and spatial (FDO_SHAPE) indexes when present.
func (t *Table) GetIndexes() ([]IndexDef, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	defs, err := index.ReadDefs(t.indexesPath())
	if err != nil {
		return nil, err
	}
	out := make([]IndexDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, IndexDef{Name: d.Name, Fields: d.Expression, IsUnique: d.Magic2 == index.KindObjectID})
	}
	return out, nil
}

func (t *Table) indexesPath() string {
	return strings.TrimSuffix(t.raw.Path, ".gdbtable") + ".gdbindexes"
}

// GetRowCount returns the number of rows.
func (t *Table) GetRowCount() (int64, error) {
	if err := t.check(); err != nil {
		return 0, err
	}
	return t.raw.RowCount(), nil
}

// GetExtent returns the extent of a feature class as stored in its header.
func (t *Table) GetExtent() (Envelope, error) {
	if err := t.check(); err != nil {
		return Envelope{}, err
	}
	if t.raw.GeomField < 0 {
		return Envelope{}, fmt.Errorf("%w: %q is not a feature class", ErrInvalidArgument, t.name)
	}
	g := t.raw.Fields[t.raw.GeomField].Geom
	e := Envelope{XMin: g.XMin, YMin: g.YMin, XMax: g.XMax, YMax: g.YMax, ZMin: g.ZMin, ZMax: g.ZMax}
	if t.raw.RowCount() == 0 && (e.XMin == 0 && e.XMax == 0) {
		nan := math.NaN()
		e = Envelope{nan, nan, nan, nan, nan, nan}
	}
	return e, nil
}

// IsEditable reports whether rows can be inserted, updated or deleted.
func (t *Table) IsEditable() bool { return !t.closed && t.gdb.writable && t.raw.Writable() }

// fieldIndex returns the index of a field by name (case-insensitive).
func (t *Table) fieldIndex(name string) int { return t.raw.FieldIndex(name) }

// GetRow returns the row with the given ObjectID, with all fields.
func (t *Table) GetRow(oid int64) (*Row, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	cols := t.allColumns()
	vals, err := t.raw.ReadRow(oid)
	if err != nil {
		if err == gdbtable.ErrRowNotFound {
			return nil, fmt.Errorf("%w: ObjectID %d", ErrRowNotFound, oid)
		}
		return nil, err
	}
	return t.newRow(cols, vals, oid), nil
}

func (t *Table) allColumns() []int {
	cols := make([]int, len(t.defs))
	for i := range cols {
		cols[i] = i
	}
	return cols
}

// Search returns the rows matching a where clause and, for feature classes,
// whose geometry envelope intersects envelope (nil for no spatial filter).
//
// subfields is "*" or a comma-separated list of field names; whereClause is
// an SQL expression (empty for all rows). When recycling is true, the same
// Row value is reused by successive calls to EnumRows.Next.
func (t *Table) Search(subfields, whereClause string, envelope *Envelope, recycling bool) (*EnumRows, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	cols, err := t.parseSubfields(subfields)
	if err != nil {
		return nil, err
	}
	var where expr
	if strings.TrimSpace(whereClause) != "" {
		where, err = parseWhere(whereClause)
		if err != nil {
			return nil, err
		}
		if err := t.bindExpr(where); err != nil {
			return nil, err
		}
	}
	if envelope != nil && t.raw.GeomField < 0 {
		return nil, fmt.Errorf("%w: spatial filter on table %q without geometry", ErrInvalidArgument, t.name)
	}
	return newTableScan(t, cols, where, envelope, recycling), nil
}

func (t *Table) parseSubfields(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return t.allColumns(), nil
	}
	var cols []int
	for _, name := range strings.Split(s, ",") {
		name = unquoteIdent(strings.TrimSpace(name))
		if name == "*" {
			cols = append(cols, t.allColumns()...)
			continue
		}
		i := t.fieldIndex(name)
		if i < 0 {
			return nil, fmt.Errorf("%w: %q", ErrFieldNotFound, name)
		}
		cols = append(cols, i)
	}
	return cols, nil
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '[' && s[len(s)-1] == ']') {
		return s[1 : len(s)-1]
	}
	return s
}

// Date conversion helpers. FileGDB dates are days since 1899-12-30.

var gdbEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

func daysToTime(d float64) time.Time {
	ms := math.Round(d * 86400e3)
	return gdbEpoch.Add(time.Duration(ms) * time.Millisecond)
}

func timeToDays(t time.Time) float64 {
	y, m, d := t.Date()
	hh, mm, ss := t.Clock()
	wall := time.Date(y, m, d, hh, mm, ss, t.Nanosecond(), time.UTC)
	return float64(wall.Sub(gdbEpoch)) / float64(24*time.Hour)
}

func offsetTime(days float64, offsetMinutes int16) time.Time {
	t := daysToTime(days)
	loc := time.FixedZone("", int(offsetMinutes)*60)
	y, m, d := t.Date()
	hh, mm, ss := t.Clock()
	return time.Date(y, m, d, hh, mm, ss, t.Nanosecond(), loc)
}
