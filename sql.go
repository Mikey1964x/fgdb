package fgdb

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

type selectItem struct {
	e     expr
	alias string
}

type orderItem struct {
	e    expr
	desc bool
}

type selectStmt struct {
	distinct bool
	star     bool
	items    []selectItem
	table    string
	where    expr
	order    []orderItem
}

func parseSelect(p *parser) (*selectStmt, error) {
	s := &selectStmt{}
	s.distinct = p.acceptKw("DISTINCT")
	for {
		if p.acceptOp("*") {
			s.items = append(s.items, selectItem{e: &colExpr{name: "*", idx: -1}})
		} else {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			it := selectItem{e: e}
			if p.acceptKw("AS") {
				t := p.next()
				if t.kind != tIdent && t.kind != tQIdent {
					return nil, p.errf("expected alias")
				}
				it.alias = t.text
			} else if t := p.peek(); t.kind == tQIdent || (t.kind == tIdent && !reservedWords[strings.ToUpper(t.text)]) {
				p.next()
				it.alias = t.text
			}
			s.items = append(s.items, it)
		}
		if !p.acceptOp(",") {
			break
		}
	}
	if err := p.expectKw("FROM"); err != nil {
		return nil, err
	}
	t := p.next()
	if t.kind != tIdent && t.kind != tQIdent {
		return nil, p.errf("expected table name")
	}
	s.table = t.text
	if p.acceptKw("WHERE") {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.where = e
	}
	if p.acceptKw("ORDER") {
		if err := p.expectKw("BY"); err != nil {
			return nil, err
		}
		for {
			e, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			oi := orderItem{e: e}
			if p.acceptKw("DESC") {
				oi.desc = true
			} else {
				p.acceptKw("ASC")
			}
			s.order = append(s.order, oi)
			if !p.acceptOp(",") {
				break
			}
		}
	}
	p.acceptOp(";")
	if p.peek().kind != tEOF {
		return nil, p.errf("unexpected token")
	}
	return s, nil
}

// ExecuteSQL runs an SQL statement and returns its result rows.
//
// Supported: SELECT [DISTINCT] <* | expressions [AS alias]> FROM <table>
// [WHERE <condition>] [ORDER BY <expression> [ASC|DESC], ...], with the
// aggregate functions COUNT, SUM, AVG, MIN and MAX (without GROUP BY).
// On a geodatabase opened for update, UPDATE, DELETE and INSERT are
// supported as well; they return an empty result.
func (g *Geodatabase) ExecuteSQL(stmt string, recycling bool) (*EnumRows, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	toks, err := lex(stmt)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	switch {
	case p.acceptKw("SELECT"):
		s, err := parseSelect(p)
		if err != nil {
			return nil, err
		}
		return g.runSelect(s, recycling)
	case p.isKw("UPDATE"), p.isKw("DELETE"), p.isKw("INSERT"):
		return g.runDML(p)
	}
	return nil, p.errf("unsupported statement")
}

func (g *Geodatabase) runSelect(s *selectStmt, recycling bool) (*EnumRows, error) {
	t, err := g.OpenTable(s.table)
	if err != nil {
		return nil, err
	}
	// Bind columns.
	aliases := map[string]expr{}
	hasAgg := false
	var outCols []selectItem
	for _, it := range s.items {
		if c, ok := it.e.(*colExpr); ok && c.name == "*" {
			for i, d := range t.defs {
				outCols = append(outCols, selectItem{e: &colExpr{name: d.Name, idx: i}})
			}
			continue
		}
		err := walk(it.e, func(n expr) error {
			switch c := n.(type) {
			case *colExpr:
				if c.name == "*" {
					return nil
				}
				c.idx = t.fieldIndex(c.name)
				if c.idx < 0 {
					return fmt.Errorf("%w: %q", ErrFieldNotFound, c.name)
				}
			case *funcExpr:
				if isAggregate(c.name) {
					hasAgg = true
				}
			}
			return nil
		})
		if err != nil {
			t.Close()
			return nil, err
		}
		if it.alias != "" {
			aliases[strings.ToUpper(it.alias)] = it.e
		}
		outCols = append(outCols, it)
	}
	if s.where != nil {
		if err := t.bindExpr(s.where); err != nil {
			t.Close()
			return nil, err
		}
	}
	for i, o := range s.order {
		// ORDER BY ordinal or alias.
		if l, ok := o.e.(*litExpr); ok {
			if n, ok := l.v.(int64); ok && n >= 1 && int(n) <= len(outCols) {
				s.order[i].e = outCols[n-1].e
				continue
			}
		}
		if c, ok := o.e.(*colExpr); ok {
			if a, ok := aliases[strings.ToUpper(c.name)]; ok && t.fieldIndex(c.name) < 0 {
				s.order[i].e = a
				continue
			}
		}
		if err := t.bindExpr(o.e); err != nil {
			t.Close()
			return nil, err
		}
	}

	// Fast path: plain column projection, streamed.
	plain := !s.distinct && !hasAgg && len(s.order) == 0
	var cols []int
	for _, it := range outCols {
		c, ok := it.e.(*colExpr)
		if !ok || it.alias != "" {
			plain = false
			break
		}
		cols = append(cols, c.idx)
	}
	if plain {
		e := newTableScan(t, cols, s.where, nil, recycling)
		inner := e.next
		e.next = func() (*Row, error) {
			r, err := inner()
			if err == io.EOF {
				t.Close()
			}
			return r, err
		}
		return e, nil
	}
	defer t.Close()

	// Materialize matching rows.
	var matched [][]any
	var fids []int64
	scan := newTableScan(t, t.allColumns(), s.where, nil, false)
	for {
		r, err := scan.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		matched = append(matched, r.vals)
		fids = append(fids, r.oid)
	}

	if hasAgg {
		return g.aggregate(t, outCols, matched)
	}

	if len(s.order) > 0 {
		keys := make([][]any, len(matched))
		for i, vals := range matched {
			c := &evalCtx{vals: vals}
			keys[i] = make([]any, len(s.order))
			for j, o := range s.order {
				v, err := c.eval(o.e)
				if err != nil {
					return nil, err
				}
				keys[i][j] = v
			}
		}
		idx := make([]int, len(matched))
		for i := range idx {
			idx[i] = i
		}
		var sortErr error
		sort.SliceStable(idx, func(a, b int) bool {
			for j, o := range s.order {
				ka, kb := keys[idx[a]][j], keys[idx[b]][j]
				var c int
				switch {
				case ka == nil && kb == nil:
					c = 0
				case ka == nil:
					c = -1
				case kb == nil:
					c = 1
				default:
					var err error
					c, _, err = compare(ka, kb)
					if err != nil && sortErr == nil {
						sortErr = err
					}
				}
				if o.desc {
					c = -c
				}
				if c != 0 {
					return c < 0
				}
			}
			return false
		})
		if sortErr != nil {
			return nil, sortErr
		}
		m2 := make([][]any, len(idx))
		f2 := make([]int64, len(idx))
		for i, k := range idx {
			m2[i], f2[i] = matched[k], fids[k]
		}
		matched, fids = m2, f2
	}

	defs, rows, err := project(t, outCols, matched, fids)
	if err != nil {
		return nil, err
	}
	if s.distinct {
		rows = distinctRows(rows)
	}
	return newSliceRows(defs, rows), nil
}

// project evaluates the select list for each matched row.
func project(t *Table, items []selectItem, matched [][]any, fids []int64) ([]FieldDef, []*Row, error) {
	defs := make([]FieldDef, len(items))
	raw := make([]int, len(items))
	out := make([][]any, len(matched))
	for i := range out {
		out[i] = make([]any, len(items))
	}
	for j, it := range items {
		if c, ok := it.e.(*colExpr); ok {
			defs[j] = t.defs[c.idx]
			if it.alias != "" {
				defs[j].Name, defs[j].Alias = it.alias, it.alias
			}
			raw[j] = c.idx
			for i, vals := range matched {
				out[i][j] = vals[c.idx]
			}
			continue
		}
		raw[j] = -1
		name := it.alias
		if name == "" {
			name = fmt.Sprintf("EXPR%d", j+1)
		}
		var evals []any
		for _, vals := range matched {
			v, err := (&evalCtx{vals: vals}).eval(it.e)
			if err != nil {
				return nil, nil, err
			}
			evals = append(evals, v)
		}
		ft := inferType(evals)
		defs[j] = FieldDef{Name: name, Alias: name, Type: ft, IsNullable: true, Length: typeLength(ft)}
		for i, v := range evals {
			out[i][j] = storeValue(v, ft)
		}
	}
	rows := make([]*Row, len(matched))
	for i := range matched {
		rows[i] = &Row{table: t, defs: defs, raw: raw, vals: out[i], oid: fids[i], prec: t.prec}
	}
	return defs, rows, nil
}

func (g *Geodatabase) aggregate(t *Table, items []selectItem, matched [][]any) (*EnumRows, error) {
	aggs := map[*funcExpr]any{}
	for _, it := range items {
		err := walk(it.e, func(n expr) error {
			f, ok := n.(*funcExpr)
			if !ok || !isAggregate(f.name) {
				return nil
			}
			v, err := computeAggregate(f, matched)
			if err != nil {
				return err
			}
			aggs[f] = v
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	defs := make([]FieldDef, len(items))
	raw := make([]int, len(items))
	vals := make([]any, len(items))
	var first []any
	if len(matched) > 0 {
		first = matched[0]
	} else {
		first = make([]any, len(t.defs))
	}
	for j, it := range items {
		v, err := (&evalCtx{vals: first, aggs: aggs}).eval(it.e)
		if err != nil {
			return nil, err
		}
		name := it.alias
		if name == "" {
			if f, ok := it.e.(*funcExpr); ok {
				name = f.name
			} else if c, ok := it.e.(*colExpr); ok {
				name = t.defs[c.idx].Name
			} else {
				name = fmt.Sprintf("EXPR%d", j+1)
			}
		}
		ft := inferType([]any{v})
		if f, ok := it.e.(*funcExpr); ok && f.name == "COUNT" {
			ft = FieldTypeInteger
		}
		defs[j] = FieldDef{Name: name, Alias: name, Type: ft, IsNullable: true, Length: typeLength(ft)}
		raw[j] = -1
		vals[j] = storeValue(v, ft)
	}
	row := &Row{defs: defs, raw: raw, vals: vals}
	return newSliceRows(defs, []*Row{row}), nil
}

func computeAggregate(f *funcExpr, matched [][]any) (any, error) {
	if f.name == "COUNT" && f.star {
		return int64(len(matched)), nil
	}
	if len(f.args) != 1 {
		return nil, fmt.Errorf("%w: %s expects one argument", ErrInvalidSQL, f.name)
	}
	var count int64
	var sum float64
	sumInt, allInt := int64(0), true
	var best any
	seen := map[any]bool{}
	for _, vals := range matched {
		v, err := (&evalCtx{vals: vals}).eval(f.args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		if f.distinct {
			k := v
			if b, ok := v.([]byte); ok {
				k = string(b)
			}
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		count++
		switch f.name {
		case "SUM", "AVG":
			if n, ok := v.(int64); ok {
				sumInt += n
			} else {
				allInt = false
			}
			x, ok := toFloat64(v)
			if !ok {
				return nil, fmt.Errorf("%w: %s needs numeric values", ErrInvalidSQL, f.name)
			}
			sum += x
		case "MIN", "MAX":
			if best == nil {
				best = v
				continue
			}
			c, ok, err := compare(v, best)
			if err != nil {
				return nil, err
			}
			if ok && (f.name == "MIN" && c < 0 || f.name == "MAX" && c > 0) {
				best = v
			}
		}
	}
	switch f.name {
	case "COUNT":
		return count, nil
	case "SUM":
		if count == 0 {
			return nil, nil
		}
		if allInt {
			return sumInt, nil
		}
		return sum, nil
	case "AVG":
		if count == 0 {
			return nil, nil
		}
		return sum / float64(count), nil
	}
	return best, nil
}

func inferType(vals []any) FieldType {
	for _, v := range vals {
		switch x := v.(type) {
		case int64:
			if x >= math.MinInt32 && x <= math.MaxInt32 {
				continue
			}
			return FieldTypeBigInteger
		case float64:
			return FieldTypeDouble
		case string:
			return FieldTypeString
		case time.Time:
			return FieldTypeDate
		case Guid:
			return FieldTypeGUID
		case bool:
			return FieldTypeSmallInteger
		case []byte:
			return FieldTypeBlob
		}
	}
	for _, v := range vals {
		if _, ok := v.(int64); ok {
			return FieldTypeInteger
		}
	}
	return FieldTypeString
}

func typeLength(t FieldType) int {
	switch t {
	case FieldTypeSmallInteger:
		return 2
	case FieldTypeInteger, FieldTypeSingle:
		return 4
	case FieldTypeDouble, FieldTypeDate, FieldTypeBigInteger:
		return 8
	case FieldTypeGUID:
		return 38
	}
	return 0
}

// storeValue converts an evaluator value to the stored representation of
// the given field type.
func storeValue(v any, t FieldType) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		switch t {
		case FieldTypeInteger:
			return int32(x)
		case FieldTypeDouble:
			return float64(x)
		case FieldTypeString:
			return fmt.Sprint(x)
		}
		return x
	case float64:
		if t == FieldTypeString {
			return fmt.Sprint(x)
		}
		return x
	case bool:
		if x {
			return int16(1)
		}
		return int16(0)
	case time.Time:
		if t == FieldTypeString {
			return x.Format("2006-01-02 15:04:05")
		}
		return gdbtable.DateTime(timeToDays(x))
	case Guid:
		if t == FieldTypeString {
			return x.String()
		}
		return gdbtable.GUID(x)
	case string:
		return x
	case []byte:
		return x
	}
	return v
}

func distinctRows(rows []*Row) []*Row {
	seen := map[string]bool{}
	var out []*Row
	for _, r := range rows {
		var sb strings.Builder
		for _, v := range r.vals {
			fmt.Fprintf(&sb, "%T:%v\x00", v, v)
		}
		k := sb.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}
