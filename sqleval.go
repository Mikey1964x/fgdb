package fgdb

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// evalValue converts a stored value to the representation used by the
// expression evaluator: nil, int64, float64, string, time.Time, bool, Guid
// or []byte.
func evalValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case gdbtable.ObjectID:
		return int64(x)
	case float32:
		return float64(x)
	case float64:
		return x
	case string:
		return x
	case gdbtable.GUID:
		return Guid(x)
	case gdbtable.DateTime, gdbtable.DateOnly, gdbtable.TimeOnly, gdbtable.DateTimeOffset:
		return toTime(x)
	case gdbtable.Geometry:
		return []byte(x)
	case []byte:
		return x
	case gdbtable.Raster:
		return x.Data
	}
	return v
}

type evalCtx struct {
	vals []any // raw table values
	// aggs holds precomputed aggregate results, keyed by node.
	aggs map[*funcExpr]any
}

func (c *evalCtx) eval(e expr) (any, error) {
	switch x := e.(type) {
	case *litExpr:
		return x.v, nil
	case *colExpr:
		if x.idx < 0 || x.idx >= len(c.vals) {
			return nil, fmt.Errorf("%w: unbound column %q", ErrInvalidSQL, x.name)
		}
		return evalValue(c.vals[x.idx]), nil
	case *unaryExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		if x.op == "NOT" {
			b, isNull, err := toBool(v)
			if err != nil || isNull {
				return nil, err
			}
			return !b, nil
		}
		switch n := v.(type) {
		case nil:
			return nil, nil
		case int64:
			return -n, nil
		case float64:
			return -n, nil
		}
		return nil, fmt.Errorf("%w: cannot negate %T", ErrInvalidSQL, v)
	case *binExpr:
		return c.evalBinary(x)
	case *isNullExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		return (v == nil) != x.not, nil
	case *likeExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		pv, err := c.eval(x.pat)
		if err != nil {
			return nil, err
		}
		if v == nil || pv == nil {
			return nil, nil
		}
		s, ok1 := asString(v)
		pat, ok2 := pv.(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%w: LIKE needs string operands", ErrInvalidSQL)
		}
		re, err := likeRegexp(pat, x.escape)
		if err != nil {
			return nil, err
		}
		return re.MatchString(s) != x.not, nil
	case *inExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, nil
		}
		sawNull := false
		for _, it := range x.list {
			w, err := c.eval(it)
			if err != nil {
				return nil, err
			}
			if w == nil {
				sawNull = true
				continue
			}
			cmp, ok, err := compare(v, w)
			if err != nil {
				return nil, err
			}
			if ok && cmp == 0 {
				return !x.not, nil
			}
		}
		if sawNull {
			return nil, nil
		}
		return x.not, nil
	case *betweenExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		lo, err := c.eval(x.lo)
		if err != nil {
			return nil, err
		}
		hi, err := c.eval(x.hi)
		if err != nil {
			return nil, err
		}
		if v == nil || lo == nil || hi == nil {
			return nil, nil
		}
		c1, ok1, err := compare(v, lo)
		if err != nil {
			return nil, err
		}
		c2, ok2, err := compare(v, hi)
		if err != nil {
			return nil, err
		}
		if !ok1 || !ok2 {
			return nil, nil
		}
		return (c1 >= 0 && c2 <= 0) != x.not, nil
	case *funcExpr:
		if c.aggs != nil {
			if v, ok := c.aggs[x]; ok {
				return v, nil
			}
		}
		return c.evalFunc(x)
	}
	return nil, fmt.Errorf("%w: unsupported expression", ErrInvalidSQL)
}

func (c *evalCtx) evalBinary(x *binExpr) (any, error) {
	if x.op == "AND" || x.op == "OR" {
		lv, err := c.eval(x.l)
		if err != nil {
			return nil, err
		}
		lb, lnull, err := toBool(lv)
		if err != nil {
			return nil, err
		}
		if x.op == "AND" && !lnull && !lb {
			return false, nil
		}
		if x.op == "OR" && !lnull && lb {
			return true, nil
		}
		rv, err := c.eval(x.r)
		if err != nil {
			return nil, err
		}
		rb, rnull, err := toBool(rv)
		if err != nil {
			return nil, err
		}
		if x.op == "AND" {
			if !rnull && !rb {
				return false, nil
			}
			if lnull || rnull {
				return nil, nil
			}
			return true, nil
		}
		if !rnull && rb {
			return true, nil
		}
		if lnull || rnull {
			return nil, nil
		}
		return false, nil
	}
	l, err := c.eval(x.l)
	if err != nil {
		return nil, err
	}
	r, err := c.eval(x.r)
	if err != nil {
		return nil, err
	}
	if l == nil || r == nil {
		return nil, nil
	}
	switch x.op {
	case "=", "<>", "<", "<=", ">", ">=":
		cmp, ok, err := compare(l, r)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		switch x.op {
		case "=":
			return cmp == 0, nil
		case "<>":
			return cmp != 0, nil
		case "<":
			return cmp < 0, nil
		case "<=":
			return cmp <= 0, nil
		case ">":
			return cmp > 0, nil
		default:
			return cmp >= 0, nil
		}
	case "||":
		ls, _ := asString(l)
		rs, _ := asString(r)
		return ls + rs, nil
	}
	return arith(x.op, l, r)
}

func arith(op string, l, r any) (any, error) {
	if op == "+" {
		if ls, ok := l.(string); ok {
			if rs, ok := r.(string); ok {
				return ls + rs, nil
			}
		}
	}
	li, lInt := l.(int64)
	ri, rInt := r.(int64)
	if lInt && rInt {
		switch op {
		case "+":
			return li + ri, nil
		case "-":
			return li - ri, nil
		case "*":
			return li * ri, nil
		case "/":
			if ri == 0 {
				return nil, nil
			}
			if li%ri == 0 {
				return li / ri, nil
			}
			return float64(li) / float64(ri), nil
		case "%":
			if ri == 0 {
				return nil, nil
			}
			return li % ri, nil
		}
	}
	lf, ok1 := toFloat64(l)
	rf, ok2 := toFloat64(r)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("%w: operator %s needs numeric operands", ErrInvalidSQL, op)
	}
	switch op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, nil
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, nil
		}
		return math.Mod(lf, rf), nil
	}
	return nil, fmt.Errorf("%w: unknown operator %s", ErrInvalidSQL, op)
}

func toBool(v any) (b, isNull bool, err error) {
	switch x := v.(type) {
	case nil:
		return false, true, nil
	case bool:
		return x, false, nil
	case int64:
		return x != 0, false, nil
	case float64:
		return x != 0, false, nil
	}
	return false, false, fmt.Errorf("%w: expected a boolean expression, got %T", ErrInvalidSQL, v)
}

func asString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case Guid:
		return x.String(), true
	case int64:
		return fmt.Sprint(x), true
	case float64:
		return fmt.Sprint(x), true
	case time.Time:
		return x.Format("2006-01-02 15:04:05"), true
	}
	return "", false
}

// compare orders two non-NULL values. ok is false if they are not
// comparable.
func compare(a, b any) (int, bool, error) {
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return cmpOrdered(x, y), true, nil
		case float64:
			return cmpOrdered(float64(x), y), true, nil
		}
	case float64:
		if y, ok := toFloat64(b); ok {
			return cmpOrdered(x, y), true, nil
		}
	case string:
		switch y := b.(type) {
		case string:
			return strings.Compare(x, y), true, nil
		case Guid:
			return strings.Compare(strings.ToUpper(normGuidString(x)), y.String()), true, nil
		case time.Time:
			t, err := parseDateLiteral(x)
			if err != nil {
				return 0, false, nil
			}
			return cmpTime(t, y), true, nil
		}
	case time.Time:
		switch y := b.(type) {
		case time.Time:
			return cmpTime(x, y), true, nil
		case string:
			t, err := parseDateLiteral(y)
			if err != nil {
				return 0, false, nil
			}
			return cmpTime(x, t), true, nil
		}
	case Guid:
		switch y := b.(type) {
		case Guid:
			return strings.Compare(x.String(), y.String()), true, nil
		case string:
			return strings.Compare(x.String(), strings.ToUpper(normGuidString(y))), true, nil
		}
	case bool:
		if y, ok := b.(bool); ok {
			xi, yi := 0, 0
			if x {
				xi = 1
			}
			if y {
				yi = 1
			}
			return xi - yi, true, nil
		}
	}
	if _, ok := a.([]byte); ok {
		return 0, false, fmt.Errorf("%w: binary and geometry fields can only be tested with IS NULL", ErrInvalidSQL)
	}
	return 0, false, fmt.Errorf("%w: cannot compare %T with %T", ErrInvalidSQL, a, b)
}

func normGuidString(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		s = "{" + s + "}"
	}
	return s
}

func cmpOrdered[T int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpTime(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

var likeCache sync.Map // pattern -> *regexp.Regexp

func likeRegexp(pat, escape string) (*regexp.Regexp, error) {
	key := escape + "\x00" + pat
	if re, ok := likeCache.Load(key); ok {
		return re.(*regexp.Regexp), nil
	}
	var sb strings.Builder
	sb.WriteString("(?s)^")
	esc, _ := utf8.DecodeRuneInString(escape)
	rs := []rune(pat)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case escape != "" && r == esc && i+1 < len(rs):
			i++
			sb.WriteString(regexp.QuoteMeta(string(rs[i])))
		case r == '%':
			sb.WriteString(".*")
		case r == '_':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, fmt.Errorf("%w: bad LIKE pattern", ErrInvalidSQL)
	}
	likeCache.Store(key, re)
	return re, nil
}

func (c *evalCtx) evalFunc(f *funcExpr) (any, error) {
	if isAggregate(f.name) {
		return nil, fmt.Errorf("%w: aggregate %s not allowed here", ErrInvalidSQL, f.name)
	}
	args := make([]any, len(f.args))
	for i, a := range f.args {
		v, err := c.eval(a)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%w: %s expects %d argument(s)", ErrInvalidSQL, f.name, n)
		}
		return nil
	}
	str := func(v any) (string, bool) { s, ok := asString(v); return s, ok }
	switch f.name {
	case "UPPER", "LOWER", "TRIM", "LTRIM", "RTRIM":
		if err := need(1); err != nil {
			return nil, err
		}
		if args[0] == nil {
			return nil, nil
		}
		s, ok := str(args[0])
		if !ok {
			return nil, fmt.Errorf("%w: %s expects a string", ErrInvalidSQL, f.name)
		}
		switch f.name {
		case "UPPER":
			return strings.ToUpper(s), nil
		case "LOWER":
			return strings.ToLower(s), nil
		case "TRIM":
			return strings.TrimSpace(s), nil
		case "LTRIM":
			return strings.TrimLeft(s, " \t"), nil
		default:
			return strings.TrimRight(s, " \t"), nil
		}
	case "CHAR_LENGTH", "CHARACTER_LENGTH", "LENGTH", "LEN":
		if err := need(1); err != nil {
			return nil, err
		}
		if args[0] == nil {
			return nil, nil
		}
		s, _ := str(args[0])
		return int64(utf8.RuneCountInString(s)), nil
	case "SUBSTRING", "SUBSTR":
		if len(args) < 2 || len(args) > 3 {
			return nil, fmt.Errorf("%w: SUBSTRING expects 2 or 3 arguments", ErrInvalidSQL)
		}
		if args[0] == nil || args[1] == nil {
			return nil, nil
		}
		rs := []rune(fmt.Sprint(args[0]))
		start, _ := toInt64(args[1])
		start-- // 1-based
		if start < 0 {
			start = 0
		}
		if int(start) > len(rs) {
			return "", nil
		}
		end := int64(len(rs))
		if len(args) == 3 && args[2] != nil {
			n, _ := toInt64(args[2])
			if start+n < end {
				end = start + n
			}
		}
		return string(rs[start:end]), nil
	case "ABS", "FLOOR", "CEILING", "CEIL", "ROUND":
		if len(args) < 1 || args[0] == nil {
			return nil, nil
		}
		if n, ok := args[0].(int64); ok {
			if f.name == "ABS" && n < 0 {
				return -n, nil
			}
			return n, nil
		}
		x, ok := toFloat64(args[0])
		if !ok {
			return nil, fmt.Errorf("%w: %s expects a number", ErrInvalidSQL, f.name)
		}
		switch f.name {
		case "ABS":
			return math.Abs(x), nil
		case "FLOOR":
			return math.Floor(x), nil
		case "CEILING", "CEIL":
			return math.Ceil(x), nil
		default:
			digits := int64(0)
			if len(args) > 1 {
				digits, _ = toInt64(args[1])
			}
			p := math.Pow(10, float64(digits))
			return math.Round(x*p) / p, nil
		}
	case "MOD":
		if err := need(2); err != nil {
			return nil, err
		}
		if args[0] == nil || args[1] == nil {
			return nil, nil
		}
		return arith("%", args[0], args[1])
	case "COALESCE":
		for _, a := range args {
			if a != nil {
				return a, nil
			}
		}
		return nil, nil
	case "EXTRACT_YEAR", "YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "SECOND":
		if err := need(1); err != nil {
			return nil, err
		}
		t, ok := args[0].(time.Time)
		if !ok {
			return nil, nil
		}
		switch f.name {
		case "MONTH":
			return int64(t.Month()), nil
		case "DAY":
			return int64(t.Day()), nil
		case "HOUR":
			return int64(t.Hour()), nil
		case "MINUTE":
			return int64(t.Minute()), nil
		case "SECOND":
			return int64(t.Second()), nil
		}
		return int64(t.Year()), nil
	}
	return nil, fmt.Errorf("%w: unknown function %s", ErrInvalidSQL, f.name)
}

// matches evaluates a where clause against raw row values.
func matches(where expr, vals []any) (bool, error) {
	if where == nil {
		return true, nil
	}
	c := &evalCtx{vals: vals}
	v, err := c.eval(where)
	if err != nil {
		return false, err
	}
	b, isNull, err := toBool(v)
	if err != nil {
		return false, err
	}
	return b && !isNull, nil
}
