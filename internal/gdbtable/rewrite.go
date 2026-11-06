package gdbtable

import (
	"errors"
	"os"
	"strings"
)

// Rewrite copies the table into new files with a new field list, converting
// each row with convert, and replaces the original files. ObjectIDs are
// preserved. t is closed; the returned table is the rewritten one, open for
// update. Index files are not rebuilt.
func (t *Table) Rewrite(fields []*Field, convert func(fid int64, vals []any) ([]any, error)) (*Table, error) {
	if !t.writable {
		return nil, ErrReadOnly
	}
	if err := t.Flush(); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(t.Path, ".gdbtable")
	tmp := base + ".tmp.gdbtable"
	os.Remove(tmp)
	os.Remove(TablxPath(tmp))
	offsetSize := t.offsetSize
	if offsetSize == 0 {
		offsetSize = 5
	}
	nt, err := Create(tmp, CreateOptions{
		Fields: fields, GeomType: t.GeomType(), HasZ: t.HasZ(), HasM: t.HasM(),
		UTF16: !t.StringsUTF8(), OffsetSize: offsetSize, Version: t.Version,
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Table, error) {
		nt.Close()
		os.Remove(tmp)
		os.Remove(TablxPath(tmp))
		return nil, err
	}
	if err := nt.beginWrite(); err != nil {
		return fail(err)
	}
	nt.LayerFlags = t.LayerFlags
	nt.SecondaryVersion = max(nt.SecondaryVersion, t.SecondaryVersion)
	nt.w.dirtyFields = true
	for fid, err := t.NextFID(1); fid != 0; fid, err = t.NextFID(fid + 1) {
		if err != nil {
			return fail(err)
		}
		vals, err := t.ReadRow(fid)
		if err != nil {
			return fail(err)
		}
		nv, err := convert(fid, vals)
		if err != nil {
			return fail(err)
		}
		if err := nt.InsertRowWithFID(fid, nv); err != nil {
			return fail(err)
		}
	}
	// Keep trailing deleted ObjectIDs reserved.
	for int64(len(nt.w.offsets)) < t.MaxFID() {
		nt.w.offsets = append(nt.w.offsets, 0)
		nt.w.dirtyTablx = true
	}
	if err := nt.Close(); err != nil {
		return fail(err)
	}
	path := t.Path
	if err := t.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	if err := os.Rename(TablxPath(tmp), TablxPath(path)); err != nil {
		return nil, err
	}
	// The free list refers to areas of the old file.
	if err := os.Remove(base + ".freelist"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return OpenForUpdate(path)
}

// DefaultValue decodes the default value of a field, in the representation
// used by DecodeRow, or returns nil.
func (t *Table) DefaultValue(f *Field) any {
	b := f.Default
	if len(b) == 0 {
		return nil
	}
	switch f.Type {
	case TypeString:
		if t.StringsUTF8() {
			return string(b)
		}
		return DecodeUTF16(b)
	case TypeInt16:
		if len(b) == 2 {
			return int16(le.Uint16(b))
		}
	case TypeInt32:
		if len(b) == 4 {
			return int32(le.Uint32(b))
		}
	case TypeInt64:
		if len(b) == 8 {
			return int64(le.Uint64(b))
		}
	case TypeFloat32:
		if len(b) == 4 {
			return f32(b)
		}
	case TypeFloat64:
		if len(b) == 8 {
			return f64(b)
		}
	case TypeDateTime:
		if len(b) == 8 {
			return DateTime(f64(b))
		}
	case TypeDateOnly:
		if len(b) == 8 {
			return DateOnly(f64(b))
		}
	case TypeTimeOnly:
		if len(b) == 8 {
			return TimeOnly(f64(b))
		}
	case TypeDateTimeWithOffset:
		if len(b) == 10 {
			return DateTimeOffset{Days: f64(b), Offset: int16(le.Uint16(b[8:]))}
		}
	}
	return nil
}
