package index

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// ErrUnsupported is returned for index kinds that cannot be built.
var ErrUnsupported = errors.New("index: unsupported index type")

// MaxIndexedChars is the number of UTF-16 characters stored in a string
// index key; longer strings are truncated.
const MaxIndexedChars = 80

// FieldName returns the field an index expression refers to, e.g. Name for
// LOWER(Name).
func FieldName(expr string) string {
	if len(expr) > 7 && strings.EqualFold(expr[:6], "LOWER(") && strings.HasSuffix(expr, ")") {
		return expr[6 : len(expr)-1]
	}
	return expr
}

func isLower(expr string) bool { return FieldName(expr) != expr }

// forEachRow calls fn with the ObjectID and decoded values of every row.
func forEachRow(t *gdbtable.Table, fn func(fid int64, vals []any) error) error {
	for fid := int64(0); ; {
		var err error
		if fid, err = t.NextFID(fid + 1); err != nil {
			return err
		}
		if fid == 0 {
			return nil
		}
		vals, err := t.ReadRow(fid)
		if err != nil {
			return err
		}
		if err := fn(fid, vals); err != nil {
			return err
		}
	}
}

// Attribute is a built attribute index, ready for WriteTree.
type Attribute struct {
	Entries []Entry
	KeySize int
	Flag    byte
}

// BuildAttribute computes the entries of the attribute index with the given
// expression (a field name, or LOWER(field) for strings).
func BuildAttribute(t *gdbtable.Table, expr string) (*Attribute, error) {
	fi := t.FieldIndex(FieldName(expr))
	if fi < 0 {
		return nil, errors.New("index: field " + FieldName(expr) + " not found")
	}
	f := t.Fields[fi]
	lower := isLower(expr)
	a := &Attribute{Flag: FlagNumeric}
	type num struct {
		v   float64
		i   int64
		oid int64
	}
	var nums []num
	var strs []struct {
		s   []uint16
		oid int64
	}
	var guids []Entry

	switch f.Type {
	case gdbtable.TypeInt16:
		a.KeySize = 2
	case gdbtable.TypeInt32, gdbtable.TypeFloat32:
		a.KeySize = 4
	case gdbtable.TypeInt64, gdbtable.TypeFloat64, gdbtable.TypeDateTime,
		gdbtable.TypeDateOnly, gdbtable.TypeTimeOnly, gdbtable.TypeDateTimeWithOffset:
		a.KeySize = 8
	case gdbtable.TypeString:
		a.KeySize, a.Flag = 2*MaxIndexedChars, FlagString
	case gdbtable.TypeGUID, gdbtable.TypeGlobalID:
		a.KeySize, a.Flag = 38, FlagGUID
	default:
		return nil, ErrUnsupported
	}

	err := forEachRow(t, func(fid int64, vals []any) error {
		v := vals[fi]
		if v == nil {
			return nil
		}
		switch x := v.(type) {
		case int16:
			nums = append(nums, num{i: int64(x), oid: fid})
		case int32:
			nums = append(nums, num{i: int64(x), oid: fid})
		case int64:
			nums = append(nums, num{i: x, oid: fid})
		case float32:
			nums = append(nums, num{v: float64(x), oid: fid})
		case float64:
			nums = append(nums, num{v: x, oid: fid})
		case gdbtable.DateTime:
			nums = append(nums, num{v: float64(x), oid: fid})
		case gdbtable.DateOnly:
			nums = append(nums, num{v: float64(x), oid: fid})
		case gdbtable.TimeOnly:
			nums = append(nums, num{v: float64(x), oid: fid})
		case gdbtable.DateTimeOffset:
			nums = append(nums, num{v: x.Days, oid: fid})
		case string:
			u := utf16.Encode([]rune(x))
			if len(u) > MaxIndexedChars {
				u = u[:MaxIndexedChars]
			}
			if lower {
				for i, c := range u {
					if c >= 'A' && c <= 'Z' {
						u[i] = c + 'a' - 'A'
					}
				}
			}
			strs = append(strs, struct {
				s   []uint16
				oid int64
			}{u, fid})
		case gdbtable.GUID:
			guids = append(guids, Entry{Key: []byte(strings.ToUpper(x.String())), OID: fid})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	switch {
	case strs != nil:
		slices.SortFunc(strs, func(a, b struct {
			s   []uint16
			oid int64
		}) int {
			return cmp.Or(slices.Compare(a.s, b.s), cmp.Compare(a.oid, b.oid))
		})
		for _, s := range strs {
			k := make([]byte, 0, a.KeySize)
			for _, c := range s.s {
				k = binary.LittleEndian.AppendUint16(k, c)
			}
			for len(k) < a.KeySize {
				k = binary.LittleEndian.AppendUint16(k, ' ')
			}
			a.Entries = append(a.Entries, Entry{Key: k, OID: s.oid})
		}
	case guids != nil:
		slices.SortFunc(guids, func(a, b Entry) int {
			return cmp.Or(bytes.Compare(a.Key, b.Key), cmp.Compare(a.OID, b.OID))
		})
		a.Entries = guids
	default:
		isFloat := f.Type != gdbtable.TypeInt16 && f.Type != gdbtable.TypeInt32 && f.Type != gdbtable.TypeInt64
		slices.SortFunc(nums, func(a, b num) int {
			if isFloat {
				return cmp.Or(cmp.Compare(a.v, b.v), cmp.Compare(a.oid, b.oid))
			}
			return cmp.Or(cmp.Compare(a.i, b.i), cmp.Compare(a.oid, b.oid))
		})
		for _, n := range nums {
			k := make([]byte, 0, a.KeySize)
			switch f.Type {
			case gdbtable.TypeInt16:
				k = binary.LittleEndian.AppendUint16(k, uint16(n.i))
			case gdbtable.TypeInt32:
				k = binary.LittleEndian.AppendUint32(k, uint32(n.i))
			case gdbtable.TypeInt64:
				k = binary.LittleEndian.AppendUint64(k, uint64(n.i))
			case gdbtable.TypeFloat32:
				k = binary.LittleEndian.AppendUint32(k, math.Float32bits(float32(n.v)))
			default:
				k = binary.LittleEndian.AppendUint64(k, math.Float64bits(n.v))
			}
			a.Entries = append(a.Entries, Entry{Key: k, OID: n.oid})
		}
	}
	return a, nil
}
