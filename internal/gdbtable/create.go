package gdbtable

import (
	"errors"
	"fmt"
	"os"
)

// Layer flag bits of the field description section.
const (
	LayerFlagUTF8          = 1 << 8
	LayerFlagHighPrecision = 1 << 9
	LayerFlagHasM          = 1 << 30
	LayerFlagHasZ          = 1 << 31
)

// CreateOptions describes a new table.
type CreateOptions struct {
	Fields     []*Field
	GeomType   uint8 // 0 for a table without geometry
	HasZ, HasM bool
	UTF16      bool   // store strings as UTF-16 instead of UTF-8
	OffsetSize int    // size of .gdbtablx offsets: 4, 5 (default) or 6
	Version    uint32 // 3 (32-bit ObjectIDs, default) or 4 (64-bit)
}

// Create writes a new, empty table (.gdbtable and .gdbtablx) and opens it
// for update. Existing files are not overwritten.
func Create(path string, o CreateOptions) (*Table, error) {
	if o.OffsetSize == 0 {
		o.OffsetSize = 5
	}
	if o.OffsetSize < 4 || o.OffsetSize > 6 {
		return nil, fmt.Errorf("gdbtable: invalid offset size %d", o.OffsetSize)
	}
	if len(o.Fields) == 0 || len(o.Fields) > 65535 {
		return nil, errors.New("gdbtable: a table needs between 1 and 65535 fields")
	}
	if o.Version == 0 {
		o.Version = 3
	}
	if o.Version != 3 && o.Version != 4 {
		return nil, fmt.Errorf("gdbtable: invalid version %d", o.Version)
	}
	t := &Table{Path: path, Version: o.Version, SecondaryVersion: 4, Fields: o.Fields, offsetSize: o.OffsetSize}
	t.LayerFlags = uint32(o.GeomType)
	if !o.UTF16 {
		t.LayerFlags |= LayerFlagUTF8
	}
	if o.GeomType != 0 {
		t.LayerFlags |= LayerFlagHighPrecision
	}
	if o.HasM {
		t.LayerFlags |= LayerFlagHasM
	}
	if o.HasZ {
		t.LayerFlags |= LayerFlagHasZ
	}
	for _, f := range o.Fields {
		if f.Type > TypeXML && f.Type != TypeObjectID {
			t.SecondaryVersion = 6 // ArcGIS Pro 3.2 field types
		}
		if f.Geom != nil {
			f.Geom.HasZ, f.Geom.HasM = o.HasZ, o.HasM
		}
	}
	t.indexFields()
	if t.ObjectIDField < 0 {
		return nil, errors.New("gdbtable: a table needs an ObjectID field")
	}

	fd := t.encodeFieldDesc()
	h := make([]byte, 40, 40+len(fd))
	le.PutUint32(h, o.Version)
	le.PutUint32(h[8:], uint32(len(fd)-4))
	le.PutUint32(h[12:], 5)
	le.PutUint64(h[24:], uint64(40+len(fd)))
	le.PutUint64(h[32:], 40)
	h = append(h, fd...)

	x := make([]byte, 32)
	le.PutUint32(x, o.Version)
	le.PutUint32(x[12:], uint32(o.OffsetSize))
	if o.Version == 4 {
		x = x[:16] // no trailer when no block is present
	}

	if err := writeNew(path, h); err != nil {
		return nil, err
	}
	if err := writeNew(TablxPath(path), x); err != nil {
		os.Remove(path)
		return nil, err
	}
	return OpenForUpdate(path)
}

func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
