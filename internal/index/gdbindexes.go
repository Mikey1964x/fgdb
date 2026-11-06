// Package index reads and writes File Geodatabase index files
// (.gdbindexes, .atx and .spx).
package index

import (
	"errors"
	"os"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// Def describes one index declared in a .gdbindexes file.
type Def struct {
	Name       string // index name; also the .atx file suffix
	Expression string // indexed field name, or an expression like LOWER(Name)
	Magic2     uint32 // 16 for FDO_OBJECTID, 4 for the spatial index, else 2
	Magic3     uint16 // 0xFFFF for FDO_OBJECTID, else 0
}

// Kind values derived from Magic2.
const (
	KindAttribute = 2
	KindSpatial   = 4
	KindObjectID  = 16
)

// ReadDefs parses a .gdbindexes file. A missing file yields no indexes.
func ReadDefs(path string) ([]Def, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return ParseDefs(b)
}

// ParseDefs parses the content of a .gdbindexes file.
func ParseDefs(b []byte) ([]Def, error) {
	c := &gdbtable.Cursor{B: b}
	n := int(c.U32())
	if c.Err != nil || n > len(b) {
		return nil, gdbtable.ErrCorrupt
	}
	defs := make([]Def, 0, n)
	for i := 0; i < n; i++ {
		var d Def
		d.Name = c.UTF16(int(c.U32()))
		c.U16()
		d.Magic2 = c.U32()
		d.Magic3 = c.U16()
		c.U32()
		d.Expression = c.UTF16(int(c.U32()))
		c.U16()
		if c.Err != nil {
			return nil, gdbtable.ErrCorrupt
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// EncodeDefs serializes index definitions in .gdbindexes format.
func EncodeDefs(defs []Def) []byte {
	b := make([]byte, 0, 64*len(defs)+4)
	b = le32(b, uint32(len(defs)))
	for _, d := range defs {
		name := gdbtable.EncodeUTF16(d.Name)
		b = le32(b, uint32(len(name)/2))
		b = append(b, name...)
		b = le16(b, 0)
		b = le32(b, d.Magic2)
		b = le16(b, d.Magic3)
		b = le32(b, 1)
		expr := gdbtable.EncodeUTF16(d.Expression)
		b = le32(b, uint32(len(expr)/2))
		b = append(b, expr...)
		b = le16(b, 0)
	}
	return b
}

func le32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func le16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
