// Package gdbtable reads and writes the .gdbtable / .gdbtablx files that hold
// the rows of a File Geodatabase table.
//
// The layout follows the reverse-engineered specification at
// https://github.com/rouault/dump_gdbtable/wiki/FGDB-Spec.
package gdbtable

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrRowNotFound is returned when a requested row does not exist or is deleted.
var ErrRowNotFound = errors.New("gdbtable: row not found")

// Table is an open .gdbtable file and its .gdbtablx offset index.
type Table struct {
	Path string // path of the .gdbtable file

	f  *os.File
	fx *os.File

	// Header (.gdbtable)
	Version          uint32 // 3 (32-bit OIDs) or 4 (64-bit OIDs)
	ValidRows        int64
	MaxRowSize       uint32 // maximum row blob size / field description size
	FieldDescOffset  int64
	FieldDescLen     uint32
	SecondaryVersion uint32 // 3 = FGDB 9.x, 4 = FGDB 10.x, 6 = Pro 3.2 extended types
	LayerFlags       uint32
	Fields           []*Field

	ObjectIDField int // index of the ObjectID field, or -1
	GeomField     int // index of the geometry field, or -1

	nullableCount int
	nullBytes     int

	// .gdbtablx
	TotalRows     int64 // number of row slots (= max ObjectID), including deleted
	offsetSize    int
	blocksPresent int64
	blockMap      []byte // nil if every 1024-row block is present
	blockPrefix   []int64

	writable bool
	w        *writeState
}

// GeomType returns the layer geometry type (low byte of the layer flags).
func (t *Table) GeomType() uint8 { return uint8(t.LayerFlags) }

// HasZ reports whether the layer geometries have Z values.
func (t *Table) HasZ() bool { return t.LayerFlags&(1<<31) != 0 }

// HasM reports whether the layer geometries have M values.
func (t *Table) HasM() bool { return t.LayerFlags&(1<<30) != 0 }

// StringsUTF8 reports whether string values are UTF-8 (otherwise UTF-16).
func (t *Table) StringsUTF8() bool { return t.LayerFlags&(1<<8) != 0 }

// FieldIndex returns the index of the named field (case-insensitive), or -1.
func (t *Table) FieldIndex(name string) int {
	for i, f := range t.Fields {
		if strings.EqualFold(f.Name, name) {
			return i
		}
	}
	return -1
}

// TablxPath returns the path of the .gdbtablx file for a .gdbtable path.
func TablxPath(p string) string {
	return strings.TrimSuffix(p, filepath.Ext(p)) + ".gdbtablx"
}

// Open opens a .gdbtable file for reading.
func Open(path string) (*Table, error) { return open(path, false) }

func open(path string, writable bool) (*Table, error) {
	flag := os.O_RDONLY
	if writable {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, err
	}
	t := &Table{Path: path, f: f, ObjectIDField: -1, GeomField: -1, writable: writable}
	if err := t.readHeader(); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	fx, err := os.OpenFile(TablxPath(path), flag, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && t.ValidRows == 0 && !writable {
			// An empty table without row index is acceptable.
			return t, nil
		}
		f.Close()
		return nil, err
	}
	t.fx = fx
	if err := t.readTablxHeader(); err != nil {
		t.Close()
		return nil, fmt.Errorf("%s: %w", TablxPath(path), err)
	}
	if t.ValidRows > t.TotalRows {
		t.ValidRows = t.TotalRows
	}
	return t, nil
}

// Close closes the underlying files.
func (t *Table) Close() error {
	var err error
	if t.w != nil {
		err = t.Flush()
	}
	if t.f != nil {
		if e := t.f.Close(); err == nil {
			err = e
		}
		t.f = nil
	}
	if t.fx != nil {
		if e := t.fx.Close(); err == nil {
			err = e
		}
		t.fx = nil
	}
	return err
}

func (t *Table) readAt(n int, off int64) ([]byte, error) {
	b := make([]byte, n)
	if _, err := t.f.ReadAt(b, off); err != nil {
		if err == io.EOF {
			return nil, ErrCorrupt
		}
		return nil, err
	}
	return b, nil
}

func (t *Table) readHeader() error {
	h, err := t.readAt(40, 0)
	if err != nil {
		return err
	}
	t.Version = le.Uint32(h)
	switch t.Version {
	case 3:
		t.ValidRows = int64(le.Uint32(h[4:]))
	case 4:
		t.ValidRows = int64(le.Uint64(h[16:]))
	default:
		return fmt.Errorf("%w: unsupported .gdbtable version %d", ErrCorrupt, t.Version)
	}
	t.MaxRowSize = le.Uint32(h[8:])
	t.FieldDescOffset = int64(le.Uint64(h[32:]))

	fh, err := t.readAt(14, t.FieldDescOffset)
	if err != nil {
		return err
	}
	t.FieldDescLen = le.Uint32(fh)
	if t.FieldDescLen < 10 || t.FieldDescLen > 10<<20 {
		return fmt.Errorf("%w: invalid field description length", ErrCorrupt)
	}
	t.SecondaryVersion = le.Uint32(fh[4:])
	t.LayerFlags = le.Uint32(fh[8:])
	nFields := int(le.Uint16(fh[12:]))

	body, err := t.readAt(int(t.FieldDescLen)-10, t.FieldDescOffset+14)
	if err != nil {
		return err
	}
	c := &Cursor{B: body}
	t.Fields, err = parseFields(c, nFields, t.HasZ(), t.HasM())
	if err != nil {
		return err
	}
	t.indexFields()
	return nil
}

func (t *Table) indexFields() {
	t.ObjectIDField, t.GeomField, t.nullableCount = -1, -1, 0
	for i, f := range t.Fields {
		switch f.Type {
		case TypeObjectID:
			if t.ObjectIDField < 0 {
				t.ObjectIDField = i
			}
		case TypeGeometry:
			if t.GeomField < 0 {
				t.GeomField = i
			}
		}
		if f.Nullable() {
			t.nullableCount++
		}
	}
	t.nullBytes = (t.nullableCount + 7) / 8
}

func (t *Table) readTablxHeader() error {
	h := make([]byte, 16)
	if _, err := t.fx.ReadAt(h, 0); err != nil {
		return ErrCorrupt
	}
	if v := le.Uint32(h); v != t.Version {
		return fmt.Errorf("%w: .gdbtablx version %d does not match .gdbtable version %d", ErrCorrupt, v, t.Version)
	}
	t.offsetSize = int(le.Uint32(h[12:]))
	if t.offsetSize < 4 || t.offsetSize > 6 {
		return fmt.Errorf("%w: invalid offset size %d", ErrCorrupt, t.offsetSize)
	}
	if t.Version == 3 {
		t.blocksPresent = int64(le.Uint32(h[4:]))
		t.TotalRows = int64(int32(le.Uint32(h[8:])))
	} else {
		t.blocksPresent = int64(le.Uint64(h[4:]))
	}
	if t.blocksPresent == 0 {
		t.TotalRows = 0
		return nil
	}
	trailer := 16 + int64(t.offsetSize)*1024*t.blocksPresent
	if t.Version == 3 {
		tr := make([]byte, 16)
		if _, err := t.fx.ReadAt(tr, trailer); err != nil {
			return ErrCorrupt
		}
		bitmapWords := le.Uint32(tr)
		nBits := int64(le.Uint32(tr[4:]))
		if int64(le.Uint32(tr[8:])) != t.blocksPresent {
			return fmt.Errorf("%w: inconsistent block count", ErrCorrupt)
		}
		if bitmapWords != 0 {
			if t.TotalRows > nBits*1024 {
				return ErrCorrupt
			}
			m := make([]byte, (nBits+7)/8)
			if _, err := t.fx.ReadAt(m, trailer+16); err != nil {
				return ErrCorrupt
			}
			t.blockMap = m
		}
	} else {
		tr := make([]byte, 12)
		if _, err := t.fx.ReadAt(tr, trailer); err != nil {
			return ErrCorrupt
		}
		t.TotalRows = int64(le.Uint64(tr))
		size := le.Uint32(tr[8:])
		if size != 0 {
			const bitmapLen = 32768
			if size != 22+bitmapLen+52 || t.TotalRows > bitmapLen*8*1024 {
				return fmt.Errorf("%w: unsupported sparse row map in version 4 .gdbtablx", ErrCorrupt)
			}
			sec := make([]byte, size)
			if _, err := t.fx.ReadAt(sec, trailer+12); err != nil {
				return ErrCorrupt
			}
			if string(sec[:6]) != "\x01\x00\x01\x00\x00\x00" ||
				string(sec[22+bitmapLen:22+bitmapLen+12]) != "\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00" {
				return fmt.Errorf("%w: unsupported sparse row map in version 4 .gdbtablx", ErrCorrupt)
			}
			t.blockMap = sec[22 : 22+bitmapLen]
		}
	}
	if t.blockMap != nil {
		t.buildBlockPrefix()
	}
	return nil
}

func (t *Table) buildBlockPrefix() {
	nBlocks := int((t.TotalRows + 1023) / 1024)
	t.blockPrefix = make([]int64, nBlocks+1)
	var n int64
	for i := 0; i < nBlocks; i++ {
		t.blockPrefix[i] = n
		if i/8 < len(t.blockMap) && testBit(t.blockMap, i) {
			n++
		}
	}
	t.blockPrefix[nBlocks] = n
}

// blockPresent reports whether the 1024-row block holding row index iRow
// has entries in the .gdbtablx.
func (t *Table) blockPresent(iRow int64) bool {
	if t.blockMap == nil {
		return true
	}
	b := int(iRow / 1024)
	return b/8 < len(t.blockMap) && testBit(t.blockMap, b)
}

// tablxPos returns the position in .gdbtablx of the offset slot for row
// index iRow (= fid-1), or -1 if the block is absent.
func (t *Table) tablxPos(iRow int64) int64 {
	if t.blockMap != nil {
		if !t.blockPresent(iRow) {
			return -1
		}
		iRow = t.blockPrefix[iRow/1024]*1024 + iRow%1024
	}
	return 16 + int64(t.offsetSize)*iRow
}

// rowOffset returns the offset of the row blob for fid in the .gdbtable, or 0
// if the row is absent or deleted.
func (t *Table) rowOffset(fid int64) (int64, error) {
	if t.w != nil {
		return t.w.rowOffset(t, fid)
	}
	if fid < 1 || fid > t.TotalRows || t.fx == nil {
		return 0, nil
	}
	pos := t.tablxPos(fid - 1)
	if pos < 0 {
		return 0, nil
	}
	var b [8]byte
	if _, err := t.fx.ReadAt(b[:t.offsetSize], pos); err != nil {
		return 0, ErrCorrupt
	}
	return int64(le.Uint64(b[:])), nil
}

// NextFID returns the smallest fid >= from whose row exists, or 0 when there
// are no more rows.
func (t *Table) NextFID(from int64) (int64, error) {
	if from < 1 {
		from = 1
	}
	for fid := from; fid <= t.MaxFID(); fid++ {
		if t.w == nil && !t.blockPresent(fid-1) {
			// Skip the whole absent block.
			fid = (fid-1)/1024*1024 + 1024
			continue
		}
		off, err := t.rowOffset(fid)
		if err != nil {
			return 0, err
		}
		if off > 0 {
			return fid, nil
		}
	}
	return 0, nil
}

// MaxFID returns the largest ObjectID slot of the table.
func (t *Table) MaxFID() int64 {
	if t.w != nil {
		return t.w.maxFID()
	}
	return t.TotalRows
}

// RowCount returns the number of valid rows.
func (t *Table) RowCount() int64 { return t.ValidRows }

// ReadRowBlob returns the raw row blob (without its 4-byte length prefix).
func (t *Table) ReadRowBlob(fid int64) ([]byte, error) {
	off, err := t.rowOffset(fid)
	if err != nil {
		return nil, err
	}
	if off == 0 {
		return nil, ErrRowNotFound
	}
	var lb [4]byte
	if _, err := t.f.ReadAt(lb[:], off); err != nil {
		return nil, ErrCorrupt
	}
	n := int32(le.Uint32(lb[:]))
	if n < 0 {
		// Negative length marks a deleted row whose index is out of sync.
		return nil, ErrRowNotFound
	}
	if int(n) < t.nullBytes || n > 1<<30 {
		return nil, ErrCorrupt
	}
	return t.readAt(int(n), off+4)
}

// ReadRow decodes the row with the given ObjectID. The returned slice has
// one entry per field; nil means NULL. See DecodeRow for the value types.
func (t *Table) ReadRow(fid int64) ([]any, error) {
	blob, err := t.ReadRowBlob(fid)
	if err != nil {
		return nil, err
	}
	vals, err := t.DecodeRow(blob, fid)
	if err != nil {
		return nil, fmt.Errorf("%s: row %d: %w", filepath.Base(t.Path), fid, err)
	}
	return vals, nil
}
