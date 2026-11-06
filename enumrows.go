package fgdb

import (
	"io"
	"iter"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
	"github.com/Mikey1964x/fgdb/internal/geom"
)

// EnumRows iterates over the result of Table.Search or
// Geodatabase.ExecuteSQL (FileGDBAPI::EnumRows).
//
//	rows, err := table.Search("*", "POP1990 > 100000", nil, true)
//	...
//	defer rows.Close()
//	for {
//		row, err := rows.Next()
//		if err == io.EOF {
//			break
//		}
//		...
//	}
//
// or, with range-over-func:
//
//	for row, err := range rows.All() { ... }
type EnumRows struct {
	defs   []FieldDef
	next   func() (*Row, error)
	closed bool
	err    error
}

// Next returns the next row, or io.EOF when there are no more rows.
func (e *EnumRows) Next() (*Row, error) {
	if e.closed {
		return nil, io.EOF
	}
	if e.err != nil {
		return nil, e.err
	}
	r, err := e.next()
	if err != nil {
		if err != io.EOF {
			e.err = err
		}
		return nil, err
	}
	return r, nil
}

// All returns an iterator over the remaining rows. Iteration stops after
// the first error, which is yielded with a nil row.
func (e *EnumRows) All() iter.Seq2[*Row, error] {
	return func(yield func(*Row, error) bool) {
		for {
			r, err := e.Next()
			if err == io.EOF {
				return
			}
			if !yield(r, err) || err != nil {
				return
			}
		}
	}
}

// Close releases the enumeration. Next returns io.EOF afterwards.
func (e *EnumRows) Close() { e.closed = true }

// GetFields returns the definitions of the fields of the result rows.
func (e *EnumRows) GetFields() []FieldDef {
	out := make([]FieldDef, len(e.defs))
	copy(out, e.defs)
	return out
}

// GetFieldInformation returns indexed access to the result fields.
func (e *EnumRows) GetFieldInformation() *FieldInfo { return &FieldInfo{defs: e.GetFields()} }

// newTableScan builds an enumeration over a table with optional attribute
// and spatial filters.
func newTableScan(t *Table, cols []int, where expr, env *Envelope, recycling bool) *EnumRows {
	defs := make([]FieldDef, len(cols))
	for i, c := range cols {
		defs[i] = t.defs[c]
	}
	var fid int64
	var recycled *Row
	geomIdx := t.raw.GeomField
	e := &EnumRows{defs: defs}
	e.next = func() (*Row, error) {
		if t.closed {
			return nil, ErrClosed
		}
		for {
			var err error
			fid, err = t.raw.NextFID(fid + 1)
			if err != nil {
				return nil, err
			}
			if fid == 0 {
				return nil, io.EOF
			}
			vals, err := t.raw.ReadRow(fid)
			if err == gdbtable.ErrRowNotFound {
				continue
			}
			if err != nil {
				return nil, err
			}
			if env != nil {
				g, _ := vals[geomIdx].(gdbtable.Geometry)
				if g == nil {
					continue
				}
				xmin, ymin, xmax, ymax, ok := geom.Extent(g, t.prec)
				if !ok || !env.Intersects(Envelope{XMin: xmin, YMin: ymin, XMax: xmax, YMax: ymax}) {
					continue
				}
			}
			ok, err := matches(where, vals)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if recycling {
				if recycled == nil {
					recycled = t.newRow(cols, vals, fid)
				} else {
					recycled.oid = fid
					for i, c := range cols {
						recycled.vals[i] = vals[c]
					}
				}
				return recycled, nil
			}
			return t.newRow(cols, vals, fid), nil
		}
	}
	return e
}

// newSliceRows enumerates precomputed rows.
func newSliceRows(defs []FieldDef, rows []*Row) *EnumRows {
	i := 0
	return &EnumRows{defs: defs, next: func() (*Row, error) {
		if i >= len(rows) {
			return nil, io.EOF
		}
		i++
		return rows[i-1], nil
	}}
}
