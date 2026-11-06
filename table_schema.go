package fgdb

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/index"
)

// AddIndex creates an attribute index on one field. Fields may be a field
// name or LOWER(name) for a case-insensitive string index. IsUnique is
// recorded by Esri software only for the ObjectID index and is ignored.
func (t *Table) AddIndex(def IndexDef) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	if err := validIndexName(def.Name); err != nil {
		return err
	}
	defs, err := index.ReadDefs(t.indexesPath())
	if err != nil {
		return err
	}
	for _, d := range defs {
		if strings.EqualFold(d.Name, def.Name) {
			return fmt.Errorf("%w: index %q", ErrAlreadyExists, def.Name)
		}
	}
	expr := strings.TrimSpace(def.Fields)
	fi := t.raw.FieldIndex(index.FieldName(expr))
	if fi < 0 {
		return fmt.Errorf("%w: %q", ErrFieldNotFound, index.FieldName(expr))
	}
	f := t.raw.Fields[fi]
	for _, d := range defs {
		if strings.EqualFold(index.FieldName(d.Expression), f.Name) {
			return fmt.Errorf("%w: field %q is already indexed by %q", ErrAlreadyExists, f.Name, d.Name)
		}
	}
	if expr != f.Name && (f.Type != gdbtable.TypeString || !strings.HasPrefix(strings.ToUpper(expr), "LOWER(")) {
		return fmt.Errorf("%w: index expression %q", ErrInvalidArgument, expr)
	}
	switch f.Type {
	case gdbtable.TypeObjectID, gdbtable.TypeGeometry:
		return fmt.Errorf("%w: field %q already has its system index", ErrInvalidArgument, f.Name)
	}
	if err := t.flush(); err != nil {
		return err
	}
	d := index.Def{Name: def.Name, Expression: expr, Magic2: index.KindAttribute}
	if err := index.RebuildAttribute(t.raw, d); err != nil {
		return err
	}
	if _, err := os.Stat(index.AttributePath(t.raw, d.Name)); err != nil {
		return fmt.Errorf("%w: indexes on %v fields", ErrNotSupported, FieldType(f.Type))
	}
	defs = append(defs, d)
	return os.WriteFile(t.indexesPath(), index.EncodeDefs(defs), 0o644)
}

// DeleteIndex deletes an attribute or spatial index. The ObjectID index
// cannot be deleted.
func (t *Table) DeleteIndex(name string) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	defs, err := index.ReadDefs(t.indexesPath())
	if err != nil {
		return err
	}
	for i, d := range defs {
		if !strings.EqualFold(d.Name, name) {
			continue
		}
		if d.Magic2 == index.KindObjectID {
			return fmt.Errorf("%w: the ObjectID index cannot be deleted", ErrInvalidArgument)
		}
		path := index.AttributePath(t.raw, d.Name)
		if index.IsSpatial(t.raw, d) {
			path = index.SpatialPath(t.raw)
		}
		defs = append(defs[:i], defs[i+1:]...)
		if err := os.WriteFile(t.indexesPath(), index.EncodeDefs(defs), 0o644); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: index %q", ErrNotFound, name)
}

func validIndexName(name string) error {
	if name == "" || len(name) > 16 {
		return fmt.Errorf("%w: index names have 1 to 16 characters", ErrInvalidArgument)
	}
	for i, r := range name {
		if r > unicode.MaxASCII || !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return fmt.Errorf("%w: invalid index name %q", ErrInvalidArgument, name)
		}
	}
	return nil
}

// AddField adds a field to the table. Existing rows get the field default
// value, or NULL. A field that is not nullable needs a default value when
// the table has rows. The table files are rewritten.
func (t *Table) AddField(def FieldDef) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	if err := validFieldName(def.Name); err != nil {
		return err
	}
	if t.raw.FieldIndex(def.Name) >= 0 {
		return fmt.Errorf("%w: field %q", ErrAlreadyExists, def.Name)
	}
	switch def.Type {
	case FieldTypeOID, FieldTypeGeometry, FieldTypeRaster:
		return fmt.Errorf("%w: adding %v fields", ErrNotSupported, def.Type)
	}
	f, err := rawField(def, t.raw.StringsUTF8())
	if err != nil {
		return err
	}
	fill := t.raw.DefaultValue(f)
	if def.Type == FieldTypeGlobalID {
		fill = nil // generated per row below
	} else if fill == nil && !f.Nullable() && t.raw.RowCount() > 0 {
		return fmt.Errorf("%w: a non-nullable field added to a table with rows needs a default value", ErrInvalidArgument)
	}
	fields := append(append([]*gdbtable.Field(nil), t.raw.Fields...), f)
	err = t.rewrite(fields, func(_ int64, vals []any) ([]any, error) {
		v := fill
		if def.Type == FieldTypeGlobalID {
			v = gdbtable.GUID(NewGuid())
		}
		return append(vals, v), nil
	})
	if err != nil {
		return err
	}
	return t.updateFieldsXML(map[string]FieldDef{strings.ToUpper(def.Name): def}, "")
}

// DeleteField deletes a field and the indexes on it. The ObjectID and
// geometry fields cannot be deleted. The table files are rewritten.
func (t *Table) DeleteField(name string) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	fi := t.raw.FieldIndex(unquoteIdent(name))
	if fi < 0 {
		return fmt.Errorf("%w: %q", ErrFieldNotFound, name)
	}
	f := t.raw.Fields[fi]
	if f.Type == gdbtable.TypeObjectID || f.Type == gdbtable.TypeGeometry {
		return fmt.Errorf("%w: field %q cannot be deleted", ErrInvalidArgument, f.Name)
	}
	defs, err := index.ReadDefs(t.indexesPath())
	if err != nil {
		return err
	}
	kept := defs[:0]
	for _, d := range defs {
		if strings.EqualFold(index.FieldName(d.Expression), f.Name) {
			os.Remove(index.AttributePath(t.raw, d.Name))
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) != len(defs) {
		if err := os.WriteFile(t.indexesPath(), index.EncodeDefs(kept), 0o644); err != nil {
			return err
		}
	}
	fields := append(append([]*gdbtable.Field(nil), t.raw.Fields[:fi]...), t.raw.Fields[fi+1:]...)
	err = t.rewrite(fields, func(_ int64, vals []any) ([]any, error) {
		return append(vals[:fi:fi], vals[fi+1:]...), nil
	})
	if err != nil {
		return err
	}
	return t.updateFieldsXML(nil, f.Name)
}

// AlterField changes the alias, string length, nullability or default
// value of the field named def.Name. The type of a field cannot change, and
// a string field cannot be made shorter than its longest value. The table
// files are rewritten.
func (t *Table) AlterField(def FieldDef) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	fi := t.raw.FieldIndex(def.Name)
	if fi < 0 {
		return fmt.Errorf("%w: %q", ErrFieldNotFound, def.Name)
	}
	old := t.raw.Fields[fi]
	if FieldType(old.Type) != def.Type {
		return fmt.Errorf("%w: the type of field %q cannot change", ErrInvalidArgument, old.Name)
	}
	if old.Type == gdbtable.TypeObjectID || old.Type == gdbtable.TypeGeometry || old.Type == gdbtable.TypeRaster {
		return fmt.Errorf("%w: field %q cannot be altered", ErrInvalidArgument, old.Name)
	}
	def.Name = old.Name
	f, err := rawField(def, t.raw.StringsUTF8())
	if err != nil {
		return err
	}
	fields := append([]*gdbtable.Field(nil), t.raw.Fields...)
	fields[fi] = f
	err = t.rewrite(fields, func(fid int64, vals []any) ([]any, error) {
		v := vals[fi]
		if v == nil && !f.Nullable() {
			return nil, fmt.Errorf("%w: field %q is NULL in row %d", ErrNotNullable, f.Name, fid)
		}
		if s, ok := v.(string); ok && f.Type == gdbtable.TypeString && len([]rune(s)) > int(f.MaxWidth) {
			return nil, fmt.Errorf("%w: row %d has a value longer than %d characters", ErrInvalidArgument, fid, f.MaxWidth)
		}
		return vals, nil
	})
	if err != nil {
		return err
	}
	return t.updateFieldsXML(map[string]FieldDef{strings.ToUpper(def.Name): def}, "")
}

// rewrite rewrites the table files with new fields and rebuilds the indexes.
func (t *Table) rewrite(fields []*gdbtable.Field, convert func(int64, []any) ([]any, error)) error {
	if t.refs > 1 {
		return fmt.Errorf("%w: table %q is opened more than once", ErrInvalidArgument, t.name)
	}
	if err := t.flush(); err != nil {
		return err
	}
	raw, err := t.raw.Rewrite(fields, convert)
	if err != nil {
		if raw == nil && t.raw != nil {
			// The original table is still in place: reopen it.
			if r, e := gdbtable.OpenForUpdate(t.raw.Path); e == nil {
				t.raw = r
			}
		}
		return err
	}
	t.raw = raw
	if err := index.Rebuild(t.raw); err != nil {
		return err
	}
	if err := t.raw.Flush(); err != nil {
		return err
	}
	t.refreshSchema()
	return nil
}

// updateFieldsXML updates the field list of the table definition in
// GDB_Items: fields in changed get a new description, deleted is removed,
// and the other descriptions are kept as they are.
func (t *Table) updateFieldsXML(changed map[string]FieldDef, deleted string) error {
	if t.item == nil {
		return nil
	}
	def := t.item.definition
	start := strings.Index(def, "<GPFieldInfoExs")
	end := strings.Index(def, "</GPFieldInfoExs>")
	if start < 0 || end < 0 {
		return nil
	}
	open := strings.Index(def[start:], ">") + start + 1
	existing := map[string]string{}
	for rest := def[open:end]; ; {
		i := strings.Index(rest, "<GPFieldInfoEx")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "</GPFieldInfoEx>")
		if j < 0 {
			break
		}
		chunk := rest[i : i+j+len("</GPFieldInfoEx>")]
		if n := xmlElementText(chunk, "Name"); n != "" {
			existing[strings.ToUpper(n)] = chunk
		}
		rest = rest[i+j:]
	}
	var w xmlWriter
	for i, f := range t.raw.Fields {
		key := strings.ToUpper(f.Name)
		if d, ok := changed[key]; ok || existing[key] == "" {
			if !ok {
				d = t.defs[i]
			}
			fieldXML(&w, d, f)
			continue
		}
		w.WriteString(existing[key])
	}
	_ = deleted // deleted fields are simply not written back
	newDef := def[:open] + w.String() + def[end:]
	uuid := t.item.uuid
	err := t.gdb.updateSystemRows("GDB_Items", func(r rawRow) bool {
		if r.guid("UUID") != uuid {
			return false
		}
		setRaw(r.t, r.vals, "Definition", newDef)
		return true
	})
	if err != nil {
		return err
	}
	t.item.definition = newDef
	t.refreshSchema()
	return nil
}

// xmlElementText returns the text of the first <name> element of doc.
func xmlElementText(doc, name string) string {
	open, close := "<"+name+">", "</"+name+">"
	i := strings.Index(doc, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(doc[i:], close)
	if j < 0 {
		return ""
	}
	return doc[i+len(open) : i+j]
}
