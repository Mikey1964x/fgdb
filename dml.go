package fgdb

import (
	"fmt"
	"io"
)

// runDML runs an INSERT, UPDATE or DELETE statement:
//
//	INSERT INTO <table> [(<field>, ...)] VALUES (<value>, ...)[, (...)]
//	UPDATE <table> SET <field> = <expression>[, ...] [WHERE <condition>]
//	DELETE FROM <table> [WHERE <condition>]
//
// Every row is checked before the table is changed, so a statement that
// fails because of a bad value changes nothing.
func (g *Geodatabase) runDML(p *parser) (*EnumRows, error) {
	if err := g.checkWritable(); err != nil {
		return nil, err
	}
	var run func(*Table) error
	var name string
	var err error
	switch {
	case p.acceptKw("INSERT"):
		if err := p.expectKw("INTO"); err != nil {
			return nil, err
		}
		if name, err = p.tableName(); err != nil {
			return nil, err
		}
		run, err = parseInsert(p)
	case p.acceptKw("UPDATE"):
		if name, err = p.tableName(); err != nil {
			return nil, err
		}
		run, err = parseUpdate(p)
	case p.acceptKw("DELETE"):
		if err := p.expectKw("FROM"); err != nil {
			return nil, err
		}
		if name, err = p.tableName(); err != nil {
			return nil, err
		}
		run, err = parseDelete(p)
	}
	if err != nil {
		return nil, err
	}
	p.acceptOp(";")
	if p.peek().kind != tEOF {
		return nil, p.errf("unexpected token")
	}
	t, err := g.OpenTable(name)
	if err != nil {
		return nil, err
	}
	err = run(t)
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return newSliceRows(nil, nil), nil
}

func (p *parser) tableName() (string, error) {
	t := p.next()
	if t.kind != tIdent && t.kind != tQIdent {
		return "", p.errf("expected table name")
	}
	return t.text, nil
}

func (p *parser) identList() ([]string, error) {
	var names []string
	for {
		t := p.next()
		if t.kind != tIdent && t.kind != tQIdent {
			return nil, p.errf("expected field name")
		}
		names = append(names, t.text)
		if !p.acceptOp(",") {
			return names, nil
		}
	}
}

func parseInsert(p *parser) (func(*Table) error, error) {
	var cols []string
	if p.acceptOp("(") {
		var err error
		if cols, err = p.identList(); err != nil {
			return nil, err
		}
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
	}
	if err := p.expectKw("VALUES"); err != nil {
		return nil, err
	}
	var tuples [][]expr
	for {
		if err := p.expectOp("("); err != nil {
			return nil, err
		}
		var vals []expr
		for {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			vals = append(vals, e)
			if !p.acceptOp(",") {
				break
			}
		}
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
		tuples = append(tuples, vals)
		if !p.acceptOp(",") {
			break
		}
	}
	return func(t *Table) error {
		idx, err := insertColumns(t, cols)
		if err != nil {
			return err
		}
		rows := make([]*Row, len(tuples))
		for i, vals := range tuples {
			if len(vals) != len(idx) {
				return fmt.Errorf("%w: %d values for %d fields", ErrInvalidSQL, len(vals), len(idx))
			}
			r, err := t.CreateRowObject()
			if err != nil {
				return err
			}
			for j, e := range vals {
				if err := noColumns(e); err != nil {
					return err
				}
				v, err := (&evalCtx{}).eval(e)
				if err != nil {
					return err
				}
				if err := r.SetValue(idx[j], v); err != nil {
					return err
				}
			}
			if err := t.checkRequired(r); err != nil {
				return err
			}
			rows[i] = r
		}
		for _, r := range rows {
			if err := t.Insert(r); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// insertColumns returns the field numbers an INSERT assigns: the listed
// fields, or every field except the ObjectID and GlobalID.
func insertColumns(t *Table, cols []string) ([]int, error) {
	var idx []int
	if cols == nil {
		for i, d := range t.defs {
			if d.Type != FieldTypeOID && d.Type != FieldTypeGlobalID {
				idx = append(idx, i)
			}
		}
		return idx, nil
	}
	seen := map[int]bool{}
	for _, c := range cols {
		i := t.fieldIndex(c)
		if i < 0 {
			return nil, fmt.Errorf("%w: %q", ErrFieldNotFound, c)
		}
		if seen[i] {
			return nil, fmt.Errorf("%w: field %q is listed twice", ErrInvalidSQL, c)
		}
		seen[i] = true
		idx = append(idx, i)
	}
	return idx, nil
}

// checkRequired reports a NULL in a non-nullable field that Insert would
// otherwise reject after earlier rows have been written.
func (t *Table) checkRequired(r *Row) error {
	for i, d := range t.defs {
		if r.vals[i] == nil && !d.IsNullable && d.Type != FieldTypeOID && d.Type != FieldTypeGlobalID {
			return fmt.Errorf("%w: field %q", ErrNotNullable, d.Name)
		}
	}
	return nil
}

func noColumns(e expr) error {
	return walk(e, func(n expr) error {
		if c, ok := n.(*colExpr); ok {
			return fmt.Errorf("%w: field %q cannot be used in VALUES", ErrInvalidSQL, c.name)
		}
		return nil
	})
}

func parseUpdate(p *parser) (func(*Table) error, error) {
	if err := p.expectKw("SET"); err != nil {
		return nil, err
	}
	var names []string
	var vals []expr
	for {
		t := p.next()
		if t.kind != tIdent && t.kind != tQIdent {
			return nil, p.errf("expected field name")
		}
		if err := p.expectOp("="); err != nil {
			return nil, err
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		names, vals = append(names, t.text), append(vals, e)
		if !p.acceptOp(",") {
			break
		}
	}
	where, err := parseOptionalWhere(p)
	if err != nil {
		return nil, err
	}
	return func(t *Table) error {
		idx, err := insertColumns(t, names)
		if err != nil {
			return err
		}
		for _, e := range vals {
			if err := t.bindExpr(e); err != nil {
				return err
			}
		}
		rows, err := matchingRows(t, where)
		if err != nil {
			return err
		}
		// SET expressions see the values from before the update.
		for _, r := range rows {
			old := append([]any(nil), r.vals...)
			for j, e := range vals {
				v, err := (&evalCtx{vals: old}).eval(e)
				if err != nil {
					return err
				}
				if err := r.SetValue(idx[j], v); err != nil {
					return fmt.Errorf("row %d: %w", r.oid, err)
				}
			}
		}
		for _, r := range rows {
			if err := t.Update(r); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func parseDelete(p *parser) (func(*Table) error, error) {
	where, err := parseOptionalWhere(p)
	if err != nil {
		return nil, err
	}
	return func(t *Table) error {
		rows, err := matchingRows(t, where)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := t.DeleteByOID(r.oid); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func parseOptionalWhere(p *parser) (expr, error) {
	if !p.acceptKw("WHERE") {
		return nil, nil
	}
	return p.parseExpr()
}

// matchingRows reads every row that matches where, before any is changed.
func matchingRows(t *Table, where expr) ([]*Row, error) {
	if where != nil {
		if err := t.bindExpr(where); err != nil {
			return nil, err
		}
	}
	scan := newTableScan(t, t.allColumns(), where, nil, false)
	var rows []*Row
	for {
		r, err := scan.Next()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
}
