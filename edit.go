package fgdb

import (
	"errors"
	"fmt"
	"math"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/geom"
	"github.com/Mikey1964x/fgdb/internal/index"
)

func (t *Table) checkEditable() error {
	if err := t.check(); err != nil {
		return err
	}
	if !t.IsEditable() {
		return ErrReadOnly
	}
	return nil
}

// CreateRowObject returns a new row with every field of the table, set to
// the field default values (or NULL). Fill it with the setters and pass it
// to Insert.
func (t *Table) CreateRowObject() (*Row, error) {
	if err := t.checkEditable(); err != nil {
		return nil, err
	}
	r := &Row{table: t, prec: t.prec}
	r.reset(t, t.allColumns())
	for i := range t.defs {
		if t.defs[i].DefaultValue == nil {
			continue
		}
		if err := r.SetValue(i, t.defs[i].DefaultValue); err != nil {
			r.vals[i] = nil // a default value that does not fit is ignored
		}
	}
	return r, nil
}

// rawValues returns the values of r for every field of the table. Fields
// that r does not hold are taken from base (nil for none).
func (t *Table) rawValues(r *Row, base []any) []any {
	vals := make([]any, len(t.raw.Fields))
	copy(vals, base)
	for i, c := range r.raw {
		if c >= 0 {
			vals[c] = r.vals[i]
		}
	}
	return vals
}

func (t *Table) checkRow(r *Row) error {
	if r == nil {
		return ErrInvalidArgument
	}
	if r.table != t {
		return fmt.Errorf("%w: row does not belong to table %q", ErrInvalidArgument, t.name)
	}
	return nil
}

// Insert adds a row to the table. The ObjectID is assigned by the table
// and stored in r; a NULL GlobalID field gets a new random value.
func (t *Table) Insert(r *Row) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	if err := t.checkRow(r); err != nil {
		return err
	}
	vals := t.rawValues(r, nil)
	for i, f := range t.raw.Fields {
		switch {
		case f.Type == gdbtable.TypeGlobalID && vals[i] == nil:
			vals[i] = gdbtable.GUID(NewGuid())
		case vals[i] == nil && !f.Nullable() && f.Type != gdbtable.TypeObjectID:
			return fmt.Errorf("%w: field %q", ErrNotNullable, f.Name)
		}
	}
	fid, err := t.raw.InsertRow(vals)
	if err != nil {
		return t.wrapWriteErr(err)
	}
	if err := t.extendExtent(vals); err != nil {
		return err
	}
	t.dirty = true
	r.oid = fid
	for i, c := range r.raw {
		if c >= 0 {
			switch t.raw.Fields[c].Type {
			case gdbtable.TypeObjectID:
				r.vals[i] = gdbtable.ObjectID(fid)
			case gdbtable.TypeGlobalID:
				r.vals[i] = vals[c]
			}
		}
	}
	return nil
}

// Update writes the values of a row obtained from Search or GetRow back to
// the table. Fields that are not in the row keep their stored values.
func (t *Table) Update(r *Row) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	if err := t.checkRow(r); err != nil {
		return err
	}
	if r.oid <= 0 {
		return fmt.Errorf("%w: row has no ObjectID", ErrInvalidArgument)
	}
	var base []any
	if len(r.raw) != len(t.raw.Fields) {
		var err error
		if base, err = t.raw.ReadRow(r.oid); err != nil {
			return t.wrapWriteErr(err)
		}
	}
	vals := t.rawValues(r, base)
	for i, f := range t.raw.Fields {
		if vals[i] == nil && !f.Nullable() && f.Type != gdbtable.TypeObjectID {
			return fmt.Errorf("%w: field %q", ErrNotNullable, f.Name)
		}
	}
	if err := t.raw.UpdateRow(r.oid, vals); err != nil {
		return t.wrapWriteErr(err)
	}
	if err := t.extendExtent(vals); err != nil {
		return err
	}
	t.dirty = true
	return nil
}

// Delete deletes a row obtained from Search or GetRow.
func (t *Table) Delete(r *Row) error {
	if err := t.checkRow(r); err != nil {
		return err
	}
	return t.DeleteByOID(r.oid)
}

// DeleteByOID deletes the row with the given ObjectID.
func (t *Table) DeleteByOID(oid int64) error {
	if err := t.checkEditable(); err != nil {
		return err
	}
	if err := t.raw.DeleteRow(oid); err != nil {
		return t.wrapWriteErr(err)
	}
	t.dirty = true
	return nil
}

func (t *Table) wrapWriteErr(err error) error {
	switch {
	case errors.Is(err, gdbtable.ErrRowNotFound):
		return fmt.Errorf("%w: %v", ErrRowNotFound, err)
	case errors.Is(err, gdbtable.ErrReadOnly):
		return ErrReadOnly
	}
	return err
}

// extendExtent grows the feature class extent to include the geometry in
// vals.
func (t *Table) extendExtent(vals []any) error {
	gi := t.raw.GeomField
	if gi < 0 || vals[gi] == nil {
		return nil
	}
	blob := []byte(vals[gi].(gdbtable.Geometry))
	xmin, ymin, xmax, ymax, ok := geom.Extent(blob, t.prec)
	if !ok {
		return nil
	}
	nan := math.NaN()
	e := gdbtable.Extent{XMin: xmin, YMin: ymin, XMax: xmax, YMax: ymax, ZMin: nan, ZMax: nan, MMin: nan, MMax: nan}
	if t.raw.HasZ() || t.raw.HasM() {
		s, err := geom.Decode(blob, t.prec)
		if err == nil {
			e.ZMin, e.ZMax = finiteMinMax(s.Z)
			e.MMin, e.MMax = finiteMinMax(s.M)
		}
	}
	return t.raw.ExtendExtent(e)
}

func finiteMinMax(v []float64) (lo, hi float64) {
	lo, hi = math.NaN(), math.NaN()
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			continue
		}
		if math.IsNaN(lo) || x < lo {
			lo = x
		}
		if math.IsNaN(hi) || x > hi {
			hi = x
		}
	}
	return lo, hi
}

// Flush writes pending changes of the table to disk, including its
// indexes. Close flushes too.
func (t *Table) Flush() error {
	if err := t.check(); err != nil {
		return err
	}
	return t.flush()
}

func (t *Table) flush() error {
	if !t.dirty && !t.raw.Dirty() {
		return nil
	}
	if err := t.raw.Flush(); err != nil {
		return err
	}
	if t.dirty {
		if err := index.Rebuild(t.raw); err != nil {
			return err
		}
		t.dirty = false
		// Rebuilding may store a computed spatial grid size.
		if err := t.raw.Flush(); err != nil {
			return err
		}
	}
	return nil
}
