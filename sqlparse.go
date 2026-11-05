package fgdb

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tQIdent // quoted identifier
	tNumber
	tString
	tOp
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := rune(s[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '\'':
			var sb strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("%w: unterminated string at %d", ErrInvalidSQL, i)
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						sb.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(s[j])
				j++
			}
			toks = append(toks, token{tString, sb.String(), i})
			i = j + 1
		case c == '"' || c == '[':
			end := byte('"')
			if c == '[' {
				end = ']'
			}
			j := strings.IndexByte(s[i+1:], end)
			if j < 0 {
				return nil, fmt.Errorf("%w: unterminated identifier at %d", ErrInvalidSQL, i)
			}
			toks = append(toks, token{tQIdent, s[i+1 : i+1+j], i})
			i += j + 2
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
				k := j + 1
				if k < len(s) && (s[k] == '+' || s[k] == '-') {
					k++
				}
				if k < len(s) && s[k] >= '0' && s[k] <= '9' {
					j = k
					for j < len(s) && s[j] >= '0' && s[j] <= '9' {
						j++
					}
				}
			}
			toks = append(toks, token{tNumber, s[i:j], i})
			i = j
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(s) {
				r := rune(s[j])
				if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r >= 0x80) {
					break
				}
				j++
			}
			toks = append(toks, token{tIdent, s[i:j], i})
			i = j
		default:
			if c >= 0x80 {
				// Non-ASCII letters in identifiers.
				j := i
				for j < len(s) && (s[j] >= 0x80 || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] == '_') {
					j++
				}
				toks = append(toks, token{tIdent, s[i:j], i})
				i = j
				continue
			}
			for _, op := range []string{"<>", "!=", "<=", ">=", "||", "=", "<", ">", "+", "-", "*", "/", "%", "(", ")", ",", ";"} {
				if strings.HasPrefix(s[i:], op) {
					toks = append(toks, token{tOp, op, i})
					i += len(op)
					goto next
				}
			}
			return nil, fmt.Errorf("%w: unexpected character %q at %d", ErrInvalidSQL, c, i)
		next:
		}
	}
	toks = append(toks, token{tEOF, "", len(s)})
	return toks, nil
}

// ---- AST ----

type expr interface{}

type (
	litExpr struct{ v any } // nil, int64, float64, string, time.Time, bool
	colExpr struct {
		name string
		idx  int // bound field index
	}
	unaryExpr struct {
		op string // "-", "NOT"
		x  expr
	}
	binExpr struct {
		op   string // = <> < <= > >= + - * / % || AND OR
		l, r expr
	}
	isNullExpr struct {
		x   expr
		not bool
	}
	likeExpr struct {
		x, pat expr
		escape string
		not    bool
	}
	inExpr struct {
		x    expr
		list []expr
		not  bool
	}
	betweenExpr struct {
		x, lo, hi expr
		not       bool
	}
	funcExpr struct {
		name     string // upper-cased
		args     []expr
		star     bool // COUNT(*)
		distinct bool
	}
)

// ---- parser ----

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}

func (p *parser) isKw(kw string) bool {
	t := p.peek()
	return t.kind == tIdent && strings.EqualFold(t.text, kw)
}

func (p *parser) acceptKw(kw string) bool {
	if p.isKw(kw) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) acceptOp(op string) bool {
	t := p.peek()
	if t.kind == tOp && t.text == op {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expectKw(kw string) error {
	if !p.acceptKw(kw) {
		return p.errf("expected %s", kw)
	}
	return nil
}

func (p *parser) expectOp(op string) error {
	if !p.acceptOp(op) {
		return p.errf("expected %q", op)
	}
	return nil
}

func (p *parser) errf(format string, args ...any) error {
	t := p.peek()
	near := t.text
	if t.kind == tEOF {
		near = "end of statement"
	}
	return fmt.Errorf("%w: %s near %q (position %d)", ErrInvalidSQL, fmt.Sprintf(format, args...), near, t.pos)
}

func parseWhere(s string) (expr, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, p.errf("unexpected token")
	}
	return e, nil
}

func (p *parser) parseExpr() (expr, error) { return p.parseOr() }

func (p *parser) parseOr() (expr, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptKw("OR") {
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &binExpr{op: "OR", l: l, r: r}
	}
	return l, nil
}

func (p *parser) parseAnd() (expr, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.acceptKw("AND") {
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &binExpr{op: "AND", l: l, r: r}
	}
	return l, nil
}

func (p *parser) parseNot() (expr, error) {
	if p.acceptKw("NOT") {
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &unaryExpr{op: "NOT", x: x}, nil
	}
	return p.parsePredicate()
}

func (p *parser) parsePredicate() (expr, error) {
	x, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tOp {
		switch t.text {
		case "=", "<>", "!=", "<", "<=", ">", ">=":
			p.next()
			r, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			op := t.text
			if op == "!=" {
				op = "<>"
			}
			return &binExpr{op: op, l: x, r: r}, nil
		}
		return x, nil
	}
	if p.acceptKw("IS") {
		not := p.acceptKw("NOT")
		if err := p.expectKw("NULL"); err != nil {
			return nil, err
		}
		return &isNullExpr{x: x, not: not}, nil
	}
	not := p.acceptKw("NOT")
	switch {
	case p.acceptKw("LIKE"):
		pat, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		le := &likeExpr{x: x, pat: pat, not: not}
		if p.acceptKw("ESCAPE") {
			t := p.next()
			if t.kind != tString || len([]rune(t.text)) != 1 {
				return nil, p.errf("ESCAPE expects a one-character string")
			}
			le.escape = t.text
		}
		return le, nil
	case p.acceptKw("IN"):
		if err := p.expectOp("("); err != nil {
			return nil, err
		}
		ie := &inExpr{x: x, not: not}
		for {
			e, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			ie.list = append(ie.list, e)
			if !p.acceptOp(",") {
				break
			}
		}
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
		return ie, nil
	case p.acceptKw("BETWEEN"):
		lo, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		if err := p.expectKw("AND"); err != nil {
			return nil, err
		}
		hi, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		return &betweenExpr{x: x, lo: lo, hi: hi, not: not}, nil
	}
	if not {
		return nil, p.errf("expected LIKE, IN or BETWEEN after NOT")
	}
	return x, nil
}

func (p *parser) parseAdd() (expr, error) {
	l, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || (t.text != "+" && t.text != "-" && t.text != "||") {
			return l, nil
		}
		p.next()
		r, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		l = &binExpr{op: t.text, l: l, r: r}
	}
}

func (p *parser) parseMul() (expr, error) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || (t.text != "*" && t.text != "/" && t.text != "%") {
			return l, nil
		}
		p.next()
		r, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l = &binExpr{op: t.text, l: l, r: r}
	}
}

func (p *parser) parseUnary() (expr, error) {
	if p.acceptOp("-") {
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if l, ok := x.(*litExpr); ok {
			switch v := l.v.(type) {
			case int64:
				return &litExpr{-v}, nil
			case float64:
				return &litExpr{-v}, nil
			}
		}
		return &unaryExpr{op: "-", x: x}, nil
	}
	if p.acceptOp("+") {
		return p.parseUnary()
	}
	return p.parsePrimary()
}

var reservedWords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "NULL": true, "LIKE": true, "IN": true,
	"BETWEEN": true, "ESCAPE": true, "SELECT": true, "FROM": true, "WHERE": true, "ORDER": true,
	"BY": true, "ASC": true, "DESC": true, "AS": true, "DISTINCT": true, "GROUP": true,
}

func (p *parser) parsePrimary() (expr, error) {
	t := p.next()
	switch t.kind {
	case tNumber:
		if !strings.ContainsAny(t.text, ".eE") {
			if n, err := strconv.ParseInt(t.text, 10, 64); err == nil {
				return &litExpr{n}, nil
			}
		}
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: bad number %q", ErrInvalidSQL, t.text)
		}
		return &litExpr{f}, nil
	case tString:
		return &litExpr{t.text}, nil
	case tQIdent:
		return &colExpr{name: t.text, idx: -1}, nil
	case tOp:
		if t.text == "(" {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectOp(")"); err != nil {
				return nil, err
			}
			return e, nil
		}
		if t.text == "*" {
			return &colExpr{name: "*", idx: -1}, nil
		}
	case tIdent:
		up := strings.ToUpper(t.text)
		switch up {
		case "NULL":
			return &litExpr{nil}, nil
		case "TRUE":
			return &litExpr{true}, nil
		case "FALSE":
			return &litExpr{false}, nil
		case "DATE", "TIMESTAMP", "TIME":
			if s := p.peek(); s.kind == tString {
				p.next()
				tm, err := parseDateLiteral(s.text)
				if err != nil {
					return nil, err
				}
				return &litExpr{tm}, nil
			}
		case "CURRENT_DATE":
			y, m, d := time.Now().Date()
			return &litExpr{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}, nil
		case "CURRENT_TIMESTAMP", "CURRENT_TIME":
			now := time.Now()
			y, m, d := now.Date()
			hh, mm, ss := now.Clock()
			return &litExpr{time.Date(y, m, d, hh, mm, ss, 0, time.UTC)}, nil
		}
		if p.acceptOp("(") {
			f := &funcExpr{name: up}
			if p.acceptOp("*") {
				f.star = true
			} else if !p.acceptOp(")") {
				f.distinct = p.acceptKw("DISTINCT")
				for {
					e, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					f.args = append(f.args, e)
					if !p.acceptOp(",") {
						break
					}
				}
			} else {
				return f, nil
			}
			if err := p.expectOp(")"); err != nil {
				return nil, err
			}
			return f, nil
		}
		if reservedWords[up] {
			p.pos--
			return nil, p.errf("unexpected keyword")
		}
		return &colExpr{name: t.text, idx: -1}, nil
	}
	p.pos--
	return nil, p.errf("unexpected token")
}

func parseDateLiteral(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04", "2006-01-02", "01/02/2006 15:04:05", "01/02/2006", "15:04:05", "15:04",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			if layout == "15:04:05" || layout == "15:04" {
				t = time.Date(1899, 12, 30, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
			}
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%w: bad date literal %q", ErrInvalidSQL, s)
}

// walk calls fn for every node of e.
func walk(e expr, fn func(expr) error) error {
	if err := fn(e); err != nil {
		return err
	}
	var kids []expr
	switch x := e.(type) {
	case *unaryExpr:
		kids = []expr{x.x}
	case *binExpr:
		kids = []expr{x.l, x.r}
	case *isNullExpr:
		kids = []expr{x.x}
	case *likeExpr:
		kids = []expr{x.x, x.pat}
	case *inExpr:
		kids = append([]expr{x.x}, x.list...)
	case *betweenExpr:
		kids = []expr{x.x, x.lo, x.hi}
	case *funcExpr:
		kids = x.args
	}
	for _, k := range kids {
		if err := walk(k, fn); err != nil {
			return err
		}
	}
	return nil
}

// bindExpr resolves column names against the table fields.
func (t *Table) bindExpr(e expr) error {
	return walk(e, func(n expr) error {
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
				return fmt.Errorf("%w: aggregate %s not allowed here", ErrInvalidSQL, c.name)
			}
		}
		return nil
	})
}

func isAggregate(name string) bool {
	switch name {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
		return true
	}
	return false
}
