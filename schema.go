package fgdb

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/index"
	"github.com/Mikey1964x/fgdb/internal/template"
)

// Well-known relationship type UUIDs of GDB_ItemRelationshipTypes.
var (
	relDatasetInFeatureDataset = mustGuid("{A1633A59-46BA-4448-8706-D8ABE2B2B02E}")
	relDatasetInFolder         = mustGuid("{DC78F1AB-34E4-43AC-BA47-1C4EABD0E7C7}")
	relDomainInDataset         = mustGuid("{17E08ADB-2B31-4DCD-8FDD-DF529E88F843}")
)

// CreateGeodatabase creates a new, empty geodatabase directory and opens it
// for update. The directory must not exist.
func CreateGeodatabase(path string) (*Geodatabase, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	if err := template.Write(path); err != nil {
		os.RemoveAll(path)
		return nil, err
	}
	g, err := openGeodatabase(path, true)
	if err != nil {
		os.RemoveAll(path)
		return nil, err
	}
	// Give the root folder and workspace items their own identifiers.
	err = g.editSystem("GDB_Items", func(t *gdbtable.Table) error {
		for _, it := range g.cat.items {
			vals, err := t.ReadRow(it.fid)
			if err != nil {
				return err
			}
			setRaw(t, vals, "UUID", gdbtable.GUID(NewGuid()))
			if err := t.UpdateRow(it.fid, vals); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		err = g.loadCatalog()
	}
	if err != nil {
		g.Close()
		os.RemoveAll(path)
		return nil, err
	}
	return g, nil
}

// DeleteGeodatabase deletes a geodatabase directory and all its files. The
// geodatabase must not be open.
func DeleteGeodatabase(path string) error {
	if _, err := os.Stat(filepath.Join(path, "a00000001.gdbtable")); err != nil {
		return fmt.Errorf("%w: %s is not a file geodatabase", ErrNotFound, path)
	}
	return os.RemoveAll(path)
}

func (g *Geodatabase) checkWritable() error {
	if err := g.check(); err != nil {
		return err
	}
	if !g.writable {
		return ErrReadOnly
	}
	return nil
}

// editSystem runs fn on a system table opened for update, then flushes it
// and rebuilds its indexes.
func (g *Geodatabase) editSystem(name string, fn func(t *gdbtable.Table) error) error {
	g.mu.Lock()
	var shared *Table
	for t := range g.tables {
		if strings.EqualFold(t.name, name) {
			shared = t
		}
	}
	g.mu.Unlock()
	if shared != nil {
		if err := fn(shared.raw); err != nil {
			return err
		}
		shared.dirty = true
		return shared.flush()
	}
	e, ok := g.findCatalog(name)
	if !ok {
		return fmt.Errorf("%w: system table %s", ErrNotFound, name)
	}
	t, err := gdbtable.OpenForUpdate(g.tableFile(e.id))
	if err != nil {
		return err
	}
	err = fn(t)
	if err == nil {
		err = t.Flush()
	}
	if err == nil {
		err = index.Rebuild(t)
	}
	if e := t.Close(); err == nil {
		err = e
	}
	return err
}

// setRaw sets a field of a raw row by name, if the field exists.
func setRaw(t *gdbtable.Table, vals []any, name string, v any) {
	if i := t.FieldIndex(name); i >= 0 {
		vals[i] = v
	}
}

// insertSystemRow inserts a row into a system table from named values and
// returns its ObjectID.
func (g *Geodatabase) insertSystemRow(table string, values map[string]any) (int64, error) {
	var fid int64
	err := g.editSystem(table, func(t *gdbtable.Table) error {
		vals := make([]any, len(t.Fields))
		for k, v := range values {
			setRaw(t, vals, k, v)
		}
		var err error
		fid, err = t.InsertRow(vals)
		return err
	})
	return fid, err
}

// deleteSystemRows deletes the rows of a system table for which match
// returns true.
func (g *Geodatabase) deleteSystemRows(table string, match func(r rawRow) bool) error {
	return g.editSystem(table, func(t *gdbtable.Table) error {
		for fid, err := t.NextFID(1); fid != 0; fid, err = t.NextFID(fid + 1) {
			if err != nil {
				return err
			}
			vals, err := t.ReadRow(fid)
			if err != nil {
				return err
			}
			if match(rawRow{t, vals}) {
				if err := t.DeleteRow(fid); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// updateSystemRows rewrites the rows of a system table for which update
// returns true after changing vals.
func (g *Geodatabase) updateSystemRows(table string, update func(r rawRow) bool) error {
	return g.editSystem(table, func(t *gdbtable.Table) error {
		for fid, err := t.NextFID(1); fid != 0; fid, err = t.NextFID(fid + 1) {
			if err != nil {
				return err
			}
			vals, err := t.ReadRow(fid)
			if err != nil {
				return err
			}
			if update(rawRow{t, vals}) {
				if err := t.UpdateRow(fid, vals); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// validName checks a table, feature class or feature dataset name.
func validName(name string) error {
	if name == "" || len([]rune(name)) > 160 {
		return fmt.Errorf("%w: invalid name %q", ErrInvalidArgument, name)
	}
	for i, r := range name {
		ok := r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))
		if !ok {
			return fmt.Errorf("%w: invalid name %q: use letters, digits and underscores, starting with a letter", ErrInvalidArgument, name)
		}
	}
	if strings.HasPrefix(strings.ToUpper(name), "GDB_") {
		return fmt.Errorf("%w: names starting with GDB_ are reserved", ErrInvalidArgument)
	}
	return nil
}

// splitPath splits `\Parent\Name` into the parent item and the name.
func (g *Geodatabase) splitPath(path string) (parent *item, name string, err error) {
	p := normPath(path)
	i := strings.LastIndex(p, `\`)
	name = p[i+1:]
	parentPath := p[:i]
	if parentPath == "" {
		parentPath = `\`
	}
	if g.cat == nil {
		return nil, "", fmt.Errorf("%w: geodatabase has no item catalog", ErrNotSupported)
	}
	parent = g.cat.itemByPath(parentPath)
	if parent == nil {
		return nil, "", fmt.Errorf("%w: parent %q", ErrNotFound, parentPath)
	}
	if parentPath != `\` && !g.cat.isOfType(parent.typeID, DatasetTypeFeatureDataset) {
		return nil, "", fmt.Errorf("%w: parent %q is not a feature dataset", ErrInvalidArgument, parentPath)
	}
	return parent, name, nil
}

func (g *Geodatabase) nameInUse(name string) bool {
	if _, ok := g.findCatalog(name); ok {
		return true
	}
	if g.cat != nil {
		for _, it := range g.cat.items {
			if strings.EqualFold(it.name, name) && (g.cat.isOfType(it.typeID, DatasetTypeFeatureDataset) ||
				g.cat.isOfType(it.typeID, DatasetTypeTable) || g.cat.isOfType(it.typeID, DatasetTypeFeatureClass)) {
				return true
			}
		}
	}
	return false
}

// CreateFeatureDataset creates a feature dataset at path (e.g. `\Roads`).
func (g *Geodatabase) CreateFeatureDataset(path string, sr SpatialReference) error {
	if err := g.checkWritable(); err != nil {
		return err
	}
	parent, name, err := g.splitPath(path)
	if err != nil {
		return err
	}
	if parent.path != `\` {
		return fmt.Errorf("%w: feature datasets cannot be nested", ErrInvalidArgument)
	}
	if err := validName(name); err != nil {
		return err
	}
	if g.nameInUse(name) {
		return fmt.Errorf("%w: %q", ErrAlreadyExists, name)
	}
	gi, err := geomInfoFromSR(sr, false, false)
	if err != nil {
		return err
	}
	id := NewGuid()
	p := `\` + name
	def := featureDatasetXML(p, name, len(g.cat.items)+1, gi, sr.WKID)
	if _, err := g.insertSystemRow("GDB_Items", map[string]any{
		"UUID": gdbtable.GUID(id), "Type": gdbtable.GUID(itemTypeFeatureDataset),
		"Name": name, "PhysicalName": strings.ToUpper(name), "Path": p, "URL": "",
		"Definition": def, "Properties": int32(1),
	}); err != nil {
		return err
	}
	if err := g.addRelationship(parent.uuid, id, relDatasetInFolder); err != nil {
		return err
	}
	if err := g.addSpatialRef(gi); err != nil {
		return err
	}
	return g.loadCatalog()
}

func (g *Geodatabase) addRelationship(origin, dest, typ Guid) error {
	_, err := g.insertSystemRow("GDB_ItemRelationships", map[string]any{
		"UUID": gdbtable.GUID(NewGuid()), "OriginID": gdbtable.GUID(origin),
		"DestID": gdbtable.GUID(dest), "Type": gdbtable.GUID(typ), "Properties": int32(1),
	})
	return err
}

// addSpatialRef records a coordinate system in GDB_SpatialRefs unless an
// identical one is there.
func (g *Geodatabase) addSpatialRef(gi *gdbtable.GeomInfo) error {
	if _, ok := g.findCatalog("GDB_SpatialRefs"); !ok {
		return nil
	}
	values := map[string]any{
		"SRTEXT": gi.WKT, "FalseX": gi.XOrigin, "FalseY": gi.YOrigin, "XYUnits": gi.XYScale,
		"FalseZ": gi.ZOrigin, "ZUnits": gi.ZScale, "FalseM": gi.MOrigin, "MUnits": gi.MScale,
		"XYTolerance": gi.XYTolerance, "ZTolerance": gi.ZTolerance, "MTolerance": gi.MTolerance,
	}
	found := false
	err := scanRaw(g, "GDB_SpatialRefs", func(_ int64, r rawRow) {
		same := true
		for k, v := range values {
			if r.get(k) != v {
				same = false
			}
		}
		found = found || same
	})
	if err != nil || found {
		return err
	}
	_, err = g.insertSystemRow("GDB_SpatialRefs", values)
	return err
}

// Configuration keywords accepted by CreateTable.
const (
	ConfigDefaults        = "DEFAULTS"
	ConfigMaxFileSize4GB  = "MAX_FILE_SIZE_4GB"
	ConfigMaxFileSize256T = "MAX_FILE_SIZE_256TB"
	ConfigTextUTF16       = "TEXT_UTF16"
)

// CreateTable creates a table, or a feature class when fieldDefs include a
// geometry field, at path (`\Name`, or `\Dataset\Name` inside a feature
// dataset) and opens it. An ObjectID field named OBJECTID is added when
// fieldDefs have none. configKeyword is "" or one of the Config constants.
//
// Fields other than the ObjectID are always created editable. String fields
// with Length 0 get a length of 255.
func (g *Geodatabase) CreateTable(path string, fieldDefs []FieldDef, configKeyword string) (*Table, error) {
	if err := g.checkWritable(); err != nil {
		return nil, err
	}
	parent, name, err := g.splitPath(path)
	if err != nil {
		return nil, err
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	if g.nameInUse(name) {
		return nil, fmt.Errorf("%w: %q", ErrAlreadyExists, name)
	}
	opts := gdbtable.CreateOptions{}
	switch strings.ToUpper(configKeyword) {
	case "", ConfigDefaults:
	case ConfigMaxFileSize4GB:
		opts.OffsetSize = 4
	case ConfigMaxFileSize256T:
		opts.OffsetSize = 6
	case ConfigTextUTF16:
		opts.UTF16 = true
	default:
		return nil, fmt.Errorf("%w: configuration keyword %q", ErrInvalidArgument, configKeyword)
	}

	defs := append([]FieldDef(nil), fieldDefs...)
	oid := -1
	var geomDef *FieldDef
	seen := map[string]bool{}
	for i := range defs {
		d := &defs[i]
		if err := validFieldName(d.Name); err != nil {
			return nil, err
		}
		if seen[strings.ToUpper(d.Name)] {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrInvalidArgument, d.Name)
		}
		seen[strings.ToUpper(d.Name)] = true
		switch d.Type {
		case FieldTypeOID:
			if oid >= 0 {
				return nil, fmt.Errorf("%w: several ObjectID fields", ErrInvalidArgument)
			}
			oid = i
		case FieldTypeGeometry:
			if geomDef != nil {
				return nil, fmt.Errorf("%w: several geometry fields", ErrInvalidArgument)
			}
			if d.GeometryDef == nil {
				return nil, fmt.Errorf("%w: geometry field %q has no GeometryDef", ErrInvalidArgument, d.Name)
			}
			geomDef = d
		case FieldTypeRaster:
			return nil, fmt.Errorf("%w: raster fields", ErrNotSupported)
		}
	}
	if oid < 0 {
		if seen["OBJECTID"] {
			return nil, fmt.Errorf("%w: field OBJECTID is not an ObjectID field", ErrInvalidArgument)
		}
		defs = append([]FieldDef{{Name: "OBJECTID", Type: FieldTypeOID}}, defs...)
	} else if oid > 0 {
		d := defs[oid]
		copy(defs[1:oid+1], defs[:oid])
		defs[0] = d
	}

	if geomDef != nil {
		gd := geomDef.GeometryDef
		switch gd.GeometryType {
		case GeometryPoint, GeometryMultipoint, GeometryPolyline, GeometryPolygon, GeometryMultiPatch:
		default:
			return nil, fmt.Errorf("%w: geometry type %v", ErrInvalidArgument, gd.GeometryType)
		}
		opts.GeomType, opts.HasZ, opts.HasM = uint8(gd.GeometryType), gd.HasZ, gd.HasM
		if parent.path != `\` && gd.SpatialReference.WKT == "" && gd.SpatialReference.WKID == 0 {
			// Inherit the coordinate system of the feature dataset.
			if sr, ok := datasetSpatialRef(parent.definition); ok {
				gd.SpatialReference = sr
			}
		}
	} else if parent.path != `\` {
		return nil, fmt.Errorf("%w: only feature classes can be created in a feature dataset", ErrInvalidArgument)
	}
	for _, d := range defs {
		f, err := rawField(d, !opts.UTF16)
		if err != nil {
			return nil, err
		}
		opts.Fields = append(opts.Fields, f)
	}

	// Register the table in the system catalog to get its file number.
	id, err := g.insertSystemRow("GDB_SystemCatalog", map[string]any{"Name": name, "FileFormat": int32(0)})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Table, error) {
		_ = g.deleteSystemRows("GDB_SystemCatalog", func(r rawRow) bool { return strings.EqualFold(r.str("Name"), name) })
		removeTableFiles(g.tableFile(id))
		_ = g.loadCatalog()
		return nil, err
	}
	raw, err := gdbtable.Create(g.tableFile(id), opts)
	if err != nil {
		return fail(err)
	}
	idxDefs := []index.Def{{Name: "FDO_" + raw.Fields[raw.ObjectIDField].Name, Expression: raw.Fields[raw.ObjectIDField].Name, Magic2: index.KindObjectID, Magic3: 0xFFFF}}
	if raw.GeomField >= 0 {
		gname := raw.Fields[raw.GeomField].Name
		idxDefs = append(idxDefs, index.Def{Name: "FDO_" + gname, Expression: gname, Magic2: index.KindSpatial})
	}
	if err := os.WriteFile(index.BasePath(raw)+".gdbindexes", index.EncodeDefs(idxDefs), 0o644); err != nil {
		raw.Close()
		return fail(err)
	}
	if raw.GeomField >= 0 {
		if err := index.RebuildSpatial(raw); err != nil {
			raw.Close()
			return fail(err)
		}
	}
	if err := raw.Close(); err != nil {
		return fail(err)
	}

	itemPath := `\` + name
	if parent.path != `\` {
		itemPath = normPath(parent.path) + `\` + name
	}
	uuid := NewGuid()
	values := map[string]any{
		"UUID": gdbtable.GUID(uuid), "Name": name, "PhysicalName": strings.ToUpper(name),
		"Path": itemPath, "URL": "", "Properties": int32(1),
		"Definition": tableXML(itemPath, name, len(g.cat.items)+1, opts, configKeyword, defs),
	}
	if geomDef != nil {
		values["Type"] = gdbtable.GUID(itemTypeFeatureClass)
		values["DatasetSubtype1"] = int32(1)
		values["DatasetSubtype2"] = int32(opts.GeomType)
		values["DatasetInfo1"] = geomDef.Name
	} else {
		values["Type"] = gdbtable.GUID(itemTypeTable)
	}
	if _, err := g.insertSystemRow("GDB_Items", values); err != nil {
		return fail(err)
	}
	relType := relDatasetInFolder
	if parent.path != `\` {
		relType = relDatasetInFeatureDataset
	}
	if err := g.addRelationship(parent.uuid, uuid, relType); err != nil {
		return nil, err
	}
	for _, f := range opts.Fields {
		if f.Geom != nil {
			if err := g.addSpatialRef(f.Geom); err != nil {
				return nil, err
			}
		}
	}
	if err := g.loadCatalog(); err != nil {
		return nil, err
	}
	return g.OpenTable(itemPath)
}

func validFieldName(name string) error {
	if name == "" || len([]rune(name)) > 64 {
		return fmt.Errorf("%w: invalid field name %q", ErrInvalidArgument, name)
	}
	for i, r := range name {
		if !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return fmt.Errorf("%w: invalid field name %q", ErrInvalidArgument, name)
		}
	}
	return nil
}

func removeTableFiles(tablePath string) {
	base := strings.TrimSuffix(tablePath, ".gdbtable")
	matches, _ := filepath.Glob(base + ".*")
	for _, m := range matches {
		// a00000010.* also matches a000000100.*: check the separator.
		if rest := strings.TrimPrefix(m, base); strings.HasPrefix(rest, ".") {
			os.Remove(m)
		}
	}
}

// rawField converts a field definition to its on-disk description.
func rawField(d FieldDef, utf8 bool) (*gdbtable.Field, error) {
	f := &gdbtable.Field{Name: d.Name, Type: gdbtable.FieldType(d.Type)}
	if d.Alias != d.Name {
		f.Alias = d.Alias
	}
	flags := uint8(gdbtable.FlagEditable)
	if d.IsNullable {
		flags |= gdbtable.FlagNullable
	}
	if d.Required {
		flags |= gdbtable.FlagRequired
	}
	switch d.Type {
	case FieldTypeOID:
		f.Width, f.Flags = 4, gdbtable.FlagRequired
		return f, nil
	case FieldTypeGeometry:
		gi, err := geomInfoFromSR(d.GeometryDef.SpatialReference, d.GeometryDef.HasZ, d.GeometryDef.HasM)
		if err != nil {
			return nil, err
		}
		if len(d.GeometryDef.GridSizes) > 0 {
			if len(d.GeometryDef.GridSizes) > 3 {
				return nil, fmt.Errorf("%w: at most 3 grid sizes", ErrInvalidArgument)
			}
			gi.GridSizes = append([]float64(nil), d.GeometryDef.GridSizes...)
		}
		f.Geom = gi
		f.Flags = flags | gdbtable.FlagRequired
		return f, nil
	case FieldTypeString:
		f.MaxWidth = int32(d.Length)
		if f.MaxWidth <= 0 {
			f.MaxWidth = 255
		}
	case FieldTypeSmallInteger:
		f.Width = 2
	case FieldTypeInteger, FieldTypeSingle:
		f.Width = 4
	case FieldTypeDouble, FieldTypeDate, FieldTypeBigInteger, FieldTypeDateOnly, FieldTypeTimeOnly:
		f.Width = 8
	case FieldTypeTimestampOffset:
		f.Width = 10
	case FieldTypeGUID, FieldTypeGlobalID:
		f.Width = 38
	case FieldTypeBlob, FieldTypeXML:
	default:
		return nil, fmt.Errorf("%w: field type %v", ErrNotSupported, d.Type)
	}
	if d.Type == FieldTypeGlobalID {
		flags &^= gdbtable.FlagEditable
		flags |= gdbtable.FlagRequired
	}
	f.Flags = flags
	if d.DefaultValue != nil {
		b, err := encodeDefault(d, utf8)
		if err != nil {
			return nil, err
		}
		f.Default = b
	}
	return f, nil
}

func encodeDefault(d FieldDef, utf8 bool) ([]byte, error) {
	bad := fmt.Errorf("%w: default value %v (%T) for %v field %q", ErrTypeMismatch, d.DefaultValue, d.DefaultValue, d.Type, d.Name)
	var b []byte
	switch d.Type {
	case FieldTypeSmallInteger, FieldTypeInteger, FieldTypeBigInteger:
		n, ok := toInt64(d.DefaultValue)
		if !ok {
			return nil, bad
		}
		switch d.Type {
		case FieldTypeSmallInteger:
			if n < math.MinInt16 || n > math.MaxInt16 {
				return nil, bad
			}
			b = put16(b, uint16(n))
		case FieldTypeInteger:
			if n < math.MinInt32 || n > math.MaxInt32 {
				return nil, bad
			}
			b = put32(b, uint32(n))
		default:
			b = put64(b, uint64(n))
		}
	case FieldTypeSingle:
		x, ok := toFloat64(d.DefaultValue)
		if !ok {
			return nil, bad
		}
		b = put32(b, math.Float32bits(float32(x)))
	case FieldTypeDouble:
		x, ok := toFloat64(d.DefaultValue)
		if !ok {
			return nil, bad
		}
		b = put64(b, math.Float64bits(x))
	case FieldTypeString:
		s, ok := d.DefaultValue.(string)
		if !ok {
			return nil, bad
		}
		if utf8 {
			b = []byte(s)
		} else {
			b = gdbtable.EncodeUTF16(s)
		}
	case FieldTypeDate, FieldTypeDateOnly, FieldTypeTimeOnly, FieldTypeTimestampOffset:
		tm, ok := d.DefaultValue.(time.Time)
		if !ok {
			return nil, bad
		}
		days := timeToDays(tm)
		switch d.Type {
		case FieldTypeDateOnly:
			days = math.Floor(days)
		case FieldTypeTimeOnly:
			days -= math.Floor(days)
		}
		b = put64(b, math.Float64bits(days))
		if d.Type == FieldTypeTimestampOffset {
			_, off := tm.Zone()
			b = put16(b, uint16(int16(off/60)))
		}
	default:
		return nil, fmt.Errorf("%w: default values for %v fields", ErrNotSupported, d.Type)
	}
	return b, nil
}

func put16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func put32(b []byte, v uint32) []byte { return put16(put16(b, uint16(v)), uint16(v>>16)) }
func put64(b []byte, v uint64) []byte { return put32(put32(b, uint32(v)), uint32(v>>32)) }

// geomInfoFromSR builds the precision model of a geometry field. Zero
// resolutions and tolerances get the defaults GDAL uses: 0.0001 units
// (projected) or 1e-9 degrees (geographic) resolution.
func geomInfoFromSR(sr SpatialReference, hasZ, hasM bool) (*gdbtable.GeomInfo, error) {
	wkt := strings.TrimSpace(sr.WKT)
	if wkt == "" && sr.WKID != 0 {
		wkt = wellKnownWKT[sr.WKID]
		if wkt == "" {
			return nil, fmt.Errorf("%w: no WKT given for WKID %d", ErrInvalidArgument, sr.WKID)
		}
	}
	geographic := strings.HasPrefix(strings.ToUpper(wkt), "GEOGCS")
	if wkt == "" {
		wkt = unknownSRS
	}
	nan := math.NaN()
	gi := &gdbtable.GeomInfo{
		WKT: wkt, Flags: 7,
		XMin: nan, YMin: nan, XMax: nan, YMax: nan, ZMin: nan, ZMax: nan, MMin: nan, MMax: nan,
		GridSizes: []float64{0},
		HasZ:      hasZ, HasM: hasM,
	}
	inv := func(r float64) float64 { return 1 / r }
	if sr.XYResolution > 0 {
		gi.XOrigin, gi.YOrigin, gi.XYScale = sr.FalseX, sr.FalseY, inv(sr.XYResolution)
		gi.XYTolerance = sr.XYTolerance
		if gi.XYTolerance <= 0 {
			gi.XYTolerance = sr.XYResolution * 10
		}
	} else if geographic {
		gi.XOrigin, gi.YOrigin, gi.XYScale, gi.XYTolerance = -400, -400, 1e9, 8.983153e-9
	} else {
		gi.XOrigin, gi.YOrigin, gi.XYScale, gi.XYTolerance = -2147483647, -2147483647, 10000, 0.001
	}
	gi.ZOrigin, gi.ZScale, gi.ZTolerance = -100000, 10000, 0.001
	if sr.ZResolution > 0 {
		gi.ZOrigin, gi.ZScale, gi.ZTolerance = sr.FalseZ, inv(sr.ZResolution), sr.ZTolerance
		if gi.ZTolerance <= 0 {
			gi.ZTolerance = sr.ZResolution * 10
		}
	}
	gi.MOrigin, gi.MScale, gi.MTolerance = -100000, 10000, 0.001
	if sr.MResolution > 0 {
		gi.MOrigin, gi.MScale, gi.MTolerance = sr.FalseM, inv(sr.MResolution), sr.MTolerance
		if gi.MTolerance <= 0 {
			gi.MTolerance = sr.MResolution * 10
		}
	}
	return gi, nil
}

// wellKnownWKT holds the Esri WKT of common coordinate systems, used when
// a SpatialReference only gives a WKID.
var wellKnownWKT = map[int]string{
	4326: `GEOGCS["GCS_WGS_1984",DATUM["D_WGS_1984",SPHEROID["WGS_1984",6378137.0,298.257223563]],PRIMEM["Greenwich",0.0],UNIT["Degree",0.0174532925199433]]`,
	4269: `GEOGCS["GCS_North_American_1983",DATUM["D_North_American_1983",SPHEROID["GRS_1980",6378137.0,298.257222101]],PRIMEM["Greenwich",0.0],UNIT["Degree",0.0174532925199433]]`,
	3857: `PROJCS["WGS_1984_Web_Mercator_Auxiliary_Sphere",GEOGCS["GCS_WGS_1984",DATUM["D_WGS_1984",SPHEROID["WGS_1984",6378137.0,298.257223563]],PRIMEM["Greenwich",0.0],UNIT["Degree",0.0174532925199433]],PROJECTION["Mercator_Auxiliary_Sphere"],PARAMETER["False_Easting",0.0],PARAMETER["False_Northing",0.0],PARAMETER["Central_Meridian",0.0],PARAMETER["Standard_Parallel_1",0.0],PARAMETER["Auxiliary_Sphere_Type",0.0],UNIT["Meter",1.0]]`,
}

// Delete deletes a table, feature class or feature dataset (with its
// content) or a domain. datasetType is one of the DatasetType constants, or
// "" for any.
func (g *Geodatabase) Delete(path, datasetType string) error {
	if err := g.checkWritable(); err != nil {
		return err
	}
	it, err := g.lookupItem(path, datasetType)
	if err != nil {
		return err
	}
	if it.path == `\` || g.cat.isOfType(it.typeID, DatasetTypeWorkspace) {
		return fmt.Errorf("%w: the root folder cannot be deleted", ErrInvalidArgument)
	}
	if err := g.deleteItem(it); err != nil {
		return err
	}
	return g.loadCatalog()
}

func (g *Geodatabase) deleteItem(it *item) error {
	if g.cat.isOfType(it.typeID, DatasetTypeFeatureDataset) {
		for _, c := range g.cat.children(it, "") {
			if err := g.deleteItem(c); err != nil {
				return err
			}
		}
	}
	if e, ok := g.findCatalog(it.name); ok && (g.cat.isOfType(it.typeID, DatasetTypeTable) || g.cat.isOfType(it.typeID, DatasetTypeFeatureClass)) {
		g.mu.Lock()
		for t := range g.tables {
			if strings.EqualFold(t.name, it.name) {
				g.mu.Unlock()
				return fmt.Errorf("%w: table %q is open", ErrInvalidArgument, it.name)
			}
		}
		g.mu.Unlock()
		if err := g.deleteSystemRows("GDB_SystemCatalog", func(r rawRow) bool { return strings.EqualFold(r.str("Name"), it.name) }); err != nil {
			return err
		}
		removeTableFiles(g.tableFile(e.id))
		os.Remove(g.tableFile(e.id) + ".cdf")
	}
	if err := g.deleteSystemRows("GDB_ItemRelationships", func(r rawRow) bool {
		return r.guid("OriginID") == it.uuid || r.guid("DestID") == it.uuid
	}); err != nil {
		return err
	}
	return g.deleteSystemRows("GDB_Items", func(r rawRow) bool { return r.guid("UUID") == it.uuid })
}

// Rename renames a table, feature class or feature dataset.
func (g *Geodatabase) Rename(path, datasetType, newName string) error {
	if err := g.checkWritable(); err != nil {
		return err
	}
	it, err := g.lookupItem(path, datasetType)
	if err != nil {
		return err
	}
	if err := validName(newName); err != nil {
		return err
	}
	if !strings.EqualFold(newName, it.name) && g.nameInUse(newName) {
		return fmt.Errorf("%w: %q", ErrAlreadyExists, newName)
	}
	isTable := g.cat.isOfType(it.typeID, DatasetTypeTable) || g.cat.isOfType(it.typeID, DatasetTypeFeatureClass)
	if !isTable && !g.cat.isOfType(it.typeID, DatasetTypeFeatureDataset) {
		return fmt.Errorf("%w: only tables, feature classes and feature datasets can be renamed", ErrNotSupported)
	}
	if isTable {
		g.mu.Lock()
		for t := range g.tables {
			if strings.EqualFold(t.name, it.name) {
				g.mu.Unlock()
				return fmt.Errorf("%w: table %q is open", ErrInvalidArgument, it.name)
			}
		}
		g.mu.Unlock()
		if err := g.updateSystemRows("GDB_SystemCatalog", func(r rawRow) bool {
			if !strings.EqualFold(r.str("Name"), it.name) {
				return false
			}
			setRaw(r.t, r.vals, "Name", newName)
			return true
		}); err != nil {
			return err
		}
	}
	oldPath := normPath(it.path)
	newPath := oldPath[:strings.LastIndex(oldPath, `\`)+1] + newName
	err = g.updateSystemRows("GDB_Items", func(r rawRow) bool {
		p := normPath(r.str("Path"))
		switch {
		case r.guid("UUID") == it.uuid:
			setRaw(r.t, r.vals, "Name", newName)
			setRaw(r.t, r.vals, "PhysicalName", strings.ToUpper(newName))
			setRaw(r.t, r.vals, "Path", newPath)
			def := replaceXMLElement(r.str("Definition"), "Name", newName)
			setRaw(r.t, r.vals, "Definition", replaceXMLElement(def, "CatalogPath", newPath))
			return true
		case strings.HasPrefix(strings.ToUpper(p), strings.ToUpper(oldPath)+`\`):
			// Items inside a renamed feature dataset.
			np := newPath + p[len(oldPath):]
			setRaw(r.t, r.vals, "Path", np)
			setRaw(r.t, r.vals, "Definition", replaceXMLElement(r.str("Definition"), "CatalogPath", np))
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	return g.loadCatalog()
}

// replaceXMLElement replaces the text of the first top-level-ish <name>
// element of an item definition.
func replaceXMLElement(doc, name, value string) string {
	open, close := "<"+name+">", "</"+name+">"
	i := strings.Index(doc, open)
	if i < 0 {
		return doc
	}
	j := strings.Index(doc[i:], close)
	if j < 0 {
		return doc
	}
	return doc[:i+len(open)] + xmlEscape(value) + doc[i+j:]
}

// datasetSpatialRef extracts the coordinate system of a feature dataset
// definition.
func datasetSpatialRef(def string) (SpatialReference, bool) {
	sr, err := parseSpatialReferenceXML(def)
	if err != nil {
		return SpatialReference{}, false
	}
	return sr, true
}
