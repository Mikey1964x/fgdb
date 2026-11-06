package gdbtable

import (
	"errors"
	"fmt"
	"math"
	"os"
)

// ErrReadOnly is returned when modifying a table that was not opened for
// update.
var ErrReadOnly = errors.New("gdbtable: table is read-only")

// writeState holds the state of a table being modified. The whole
// .gdbtablx offset array is kept in memory and rewritten by Flush; row blobs
// are written to the .gdbtable immediately.
type writeState struct {
	offsets  []int64 // offset of each row blob, indexed by fid-1; 0 = absent
	fileSize int64

	dirtyHeader bool
	dirtyTablx  bool
	dirtyFields bool
	hasDeleted  bool
}

func (w *writeState) rowOffset(t *Table, fid int64) (int64, error) {
	if fid < 1 || fid > int64(len(w.offsets)) {
		return 0, nil
	}
	return w.offsets[fid-1], nil
}

func (w *writeState) maxFID() int64 { return int64(len(w.offsets)) }

// OpenForUpdate opens a .gdbtable file for reading and writing.
func OpenForUpdate(path string) (*Table, error) { return open(path, true) }

// Writable reports whether the table was opened for update.
func (t *Table) Writable() bool { return t.writable }

// Dirty reports whether the table has changes that were not flushed.
func (t *Table) Dirty() bool {
	w := t.w
	return w != nil && (w.dirtyHeader || w.dirtyTablx || w.dirtyFields)
}

// beginWrite loads the offset index into memory before the first change.
func (t *Table) beginWrite() error {
	if !t.writable {
		return ErrReadOnly
	}
	if t.w != nil {
		return nil
	}
	st, err := t.f.Stat()
	if err != nil {
		return err
	}
	w := &writeState{fileSize: st.Size()}
	if t.offsetSize == 0 {
		t.offsetSize = 5
	}
	w.offsets = make([]int64, t.TotalRows)
	for fid := int64(1); fid <= t.TotalRows; fid++ {
		if !t.blockPresent(fid - 1) {
			continue
		}
		off, err := t.rowOffset(fid)
		if err != nil {
			return err
		}
		w.offsets[fid-1] = off
	}
	t.w = w
	return nil
}

func (t *Table) maxOffset() int64 { return int64(1)<<(8*t.offsetSize) - 1 }

// writeBlob appends a row blob at the end of the .gdbtable and returns its
// offset.
func (t *Table) appendBlob(blob []byte) (int64, error) {
	w := t.w
	off := w.fileSize
	if off+4+int64(len(blob)) > t.maxOffset() {
		return 0, fmt.Errorf("gdbtable: table file would exceed the maximum size for %d-byte offsets", t.offsetSize)
	}
	buf := make([]byte, 0, 4+len(blob))
	buf = appendU32(buf, uint32(len(blob)))
	buf = append(buf, blob...)
	if _, err := t.f.WriteAt(buf, off); err != nil {
		return 0, err
	}
	w.fileSize += int64(len(buf))
	if uint32(len(blob)) > t.MaxRowSize {
		t.MaxRowSize = uint32(len(blob))
	}
	w.dirtyHeader = true
	return off, nil
}

// blankBlob marks the row blob at off as free: its length is negated and its
// content zeroed, as Esri software does.
func (t *Table) blankBlob(off int64) error {
	var lb [4]byte
	if _, err := t.f.ReadAt(lb[:], off); err != nil {
		return ErrCorrupt
	}
	n := int32(le.Uint32(lb[:]))
	if n < 0 {
		return nil
	}
	buf := make([]byte, 4+int(n))
	le.PutUint32(buf, uint32(-n))
	_, err := t.f.WriteAt(buf, off)
	return err
}

// InsertRow appends a row and returns its ObjectID. vals has one entry per
// field, as for EncodeRow; the ObjectID entry is ignored.
func (t *Table) InsertRow(vals []any) (int64, error) {
	if err := t.beginWrite(); err != nil {
		return 0, err
	}
	fid := int64(len(t.w.offsets)) + 1
	if t.Version == 3 && fid > math.MaxInt32 {
		return 0, fmt.Errorf("gdbtable: too many rows for a 32-bit ObjectID table")
	}
	return fid, t.InsertRowWithFID(fid, vals)
}

// InsertRowWithFID writes a row with a given ObjectID, which must not be
// in use.
func (t *Table) InsertRowWithFID(fid int64, vals []any) error {
	if err := t.beginWrite(); err != nil {
		return err
	}
	w := t.w
	if fid < 1 || (t.Version == 3 && fid > math.MaxInt32) {
		return fmt.Errorf("gdbtable: invalid ObjectID %d", fid)
	}
	if fid <= int64(len(w.offsets)) && w.offsets[fid-1] != 0 {
		return fmt.Errorf("gdbtable: ObjectID %d already exists", fid)
	}
	blob, err := t.EncodeRow(vals)
	if err != nil {
		return err
	}
	off, err := t.appendBlob(blob)
	if err != nil {
		return err
	}
	for int64(len(w.offsets)) < fid {
		w.offsets = append(w.offsets, 0)
	}
	w.offsets[fid-1] = off
	t.ValidRows++
	w.dirtyTablx = true
	return nil
}

// UpdateRow replaces the values of an existing row.
func (t *Table) UpdateRow(fid int64, vals []any) error {
	if err := t.beginWrite(); err != nil {
		return err
	}
	w := t.w
	off, _ := w.rowOffset(t, fid)
	if off == 0 {
		return ErrRowNotFound
	}
	blob, err := t.EncodeRow(vals)
	if err != nil {
		return err
	}
	var lb [4]byte
	if _, err := t.f.ReadAt(lb[:], off); err != nil {
		return ErrCorrupt
	}
	old := int32(le.Uint32(lb[:]))
	if old >= 0 && len(blob) <= int(old) {
		// Rewrite in place and blank the unused tail.
		buf := make([]byte, 4+int(old))
		le.PutUint32(buf, uint32(len(blob)))
		copy(buf[4:], blob)
		_, err := t.f.WriteAt(buf, off)
		return err
	}
	newOff, err := t.appendBlob(blob)
	if err != nil {
		return err
	}
	if err := t.blankBlob(off); err != nil {
		return err
	}
	w.offsets[fid-1] = newOff
	w.dirtyTablx = true
	return nil
}

// DeleteRow deletes a row.
func (t *Table) DeleteRow(fid int64) error {
	if err := t.beginWrite(); err != nil {
		return err
	}
	w := t.w
	off, _ := w.rowOffset(t, fid)
	if off == 0 {
		return ErrRowNotFound
	}
	if err := t.blankBlob(off); err != nil {
		return err
	}
	w.offsets[fid-1] = 0
	t.ValidRows--
	w.hasDeleted = true
	w.dirtyTablx = true
	w.dirtyHeader = true
	return nil
}

// Extent is a geometry field extent.
type Extent struct {
	XMin, YMin, XMax, YMax float64
	ZMin, ZMax, MMin, MMax float64
}

// ExtendExtent grows the extent of the geometry field stored in the field
// description so that it includes e. NaN members of e are ignored.
func (t *Table) ExtendExtent(e Extent) error {
	if t.GeomField < 0 {
		return nil
	}
	if err := t.beginWrite(); err != nil {
		return err
	}
	g := t.Fields[t.GeomField].Geom
	empty := math.IsNaN(g.XMin) || (g.XMin == 0 && g.YMin == 0 && g.XMax == 0 && g.YMax == 0 && t.ValidRows <= 1)
	grow := func(lo, hi *float64, nlo, nhi float64, empty bool) {
		if math.IsNaN(nlo) || math.IsNaN(nhi) {
			return
		}
		if empty || math.IsNaN(*lo) || nlo < *lo {
			*lo = nlo
			t.w.dirtyFields = true
		}
		if empty || math.IsNaN(*hi) || nhi > *hi {
			*hi = nhi
			t.w.dirtyFields = true
		}
	}
	if math.IsNaN(e.XMin) {
		return nil
	}
	grow(&g.XMin, &g.XMax, e.XMin, e.XMax, empty)
	grow(&g.YMin, &g.YMax, e.YMin, e.YMax, empty)
	if t.HasZ() {
		grow(&g.ZMin, &g.ZMax, e.ZMin, e.ZMax, empty)
	}
	if t.HasM() {
		grow(&g.MMin, &g.MMax, e.MMin, e.MMax, empty)
	}
	return nil
}

// SetExtent replaces the stored extent of the geometry field.
func (t *Table) SetExtent(e Extent) error {
	if t.GeomField < 0 {
		return nil
	}
	if err := t.beginWrite(); err != nil {
		return err
	}
	g := t.Fields[t.GeomField].Geom
	g.XMin, g.YMin, g.XMax, g.YMax = e.XMin, e.YMin, e.XMax, e.YMax
	g.ZMin, g.ZMax, g.MMin, g.MMax = e.ZMin, e.ZMax, e.MMin, e.MMax
	t.w.dirtyFields = true
	return nil
}

// encodeFieldDesc encodes the field description section.
func (t *Table) encodeFieldDesc() []byte {
	b := make([]byte, 4, 256)
	b = appendU32(b, t.SecondaryVersion)
	b = appendU32(b, t.LayerFlags)
	b = appendU16(b, uint16(len(t.Fields)))
	b = appendFields(b, t.Fields, t.HasZ(), t.HasM())
	le.PutUint32(b, uint32(len(b)-4))
	return b
}

// writeFieldDesc writes the field description in place when it fits, or at
// the end of the file otherwise.
func (t *Table) writeFieldDesc() error {
	w := t.w
	b := t.encodeFieldDesc()
	off := t.FieldDescOffset
	if uint32(len(b)-4) > t.FieldDescLen {
		off = w.fileSize
		w.fileSize += int64(len(b))
	}
	if _, err := t.f.WriteAt(b, off); err != nil {
		return err
	}
	t.FieldDescOffset = off
	t.FieldDescLen = uint32(len(b) - 4)
	if t.FieldDescLen > t.MaxRowSize {
		t.MaxRowSize = t.FieldDescLen
	}
	w.dirtyFields = false
	w.dirtyHeader = true
	return nil
}

func (t *Table) writeHeader() error {
	w := t.w
	var h [40]byte
	if _, err := t.f.ReadAt(h[:], 0); err != nil {
		return err
	}
	le.PutUint32(h[8:], t.MaxRowSize)
	if t.Version == 3 {
		le.PutUint32(h[4:], uint32(t.ValidRows))
	} else {
		if w.hasDeleted {
			le.PutUint32(h[4:], 1)
		}
		le.PutUint64(h[16:], uint64(t.ValidRows))
	}
	le.PutUint64(h[24:], uint64(w.fileSize))
	le.PutUint64(h[32:], uint64(t.FieldDescOffset))
	if _, err := t.f.WriteAt(h[:], 0); err != nil {
		return err
	}
	w.dirtyHeader = false
	return nil
}

// writeTablx rewrites the whole .gdbtablx from the in-memory offsets.
func (t *Table) writeTablx() error {
	w := t.w
	// Trailing deleted rows are kept so that ObjectIDs are not reused.
	total := int64(len(w.offsets))
	nBlocks := (total + 1023) / 1024
	sz := t.offsetSize

	present := make([]bool, nBlocks)
	nPresent := int64(0)
	for b := int64(0); b < nBlocks; b++ {
		end := min((b+1)*1024, total)
		for i := b * 1024; i < end; i++ {
			if w.offsets[i] != 0 {
				present[b] = true
				break
			}
		}
		if present[b] {
			nPresent++
		}
	}
	sparse := nPresent < nBlocks && t.Version == 3
	if !sparse {
		for i := range present {
			present[i] = true
		}
		nPresent = nBlocks
	}

	buf := make([]byte, 16, 16+nPresent*1024*int64(sz)+16)
	le.PutUint32(buf, t.Version)
	if t.Version == 3 {
		le.PutUint32(buf[4:], uint32(nPresent))
		le.PutUint32(buf[8:], uint32(total))
	} else {
		le.PutUint64(buf[4:], uint64(nPresent))
	}
	le.PutUint32(buf[12:], uint32(sz))
	var ob [8]byte
	for b := int64(0); b < nBlocks; b++ {
		if !present[b] {
			continue
		}
		for i := b * 1024; i < (b+1)*1024; i++ {
			var off int64
			if i < total {
				off = w.offsets[i]
			}
			le.PutUint64(ob[:], uint64(off))
			buf = append(buf, ob[:sz]...)
		}
	}
	if t.Version == 3 {
		if sparse {
			words := ((nBlocks+31)/32 + 31) / 32 * 32
			bm := make([]byte, words*4)
			for b, p := range present {
				if p {
					setBit(bm, b)
				}
			}
			nonZero := words
			for nonZero > 0 && le.Uint32(bm[(nonZero-1)*4:]) == 0 {
				nonZero--
			}
			buf = appendU32(buf, uint32(words))
			buf = appendU32(buf, uint32(nBlocks))
			buf = appendU32(buf, uint32(nPresent))
			buf = appendU32(buf, uint32(nonZero))
			buf = append(buf, bm...)
		} else {
			buf = appendU32(buf, 0)
			buf = appendU32(buf, uint32(nBlocks))
			buf = appendU32(buf, uint32(nPresent))
			buf = appendU32(buf, 0)
		}
	} else if nPresent > 0 {
		buf = appendU64(buf, uint64(total))
		buf = appendU32(buf, 0)
	}

	if t.fx == nil {
		fx, err := os.OpenFile(TablxPath(t.Path), os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		t.fx = fx
	}
	if _, err := t.fx.WriteAt(buf, 0); err != nil {
		return err
	}
	if err := t.fx.Truncate(int64(len(buf))); err != nil {
		return err
	}

	// Keep the read-side view consistent with what was written.
	t.TotalRows = total
	t.blocksPresent = nPresent
	t.blockMap = nil
	t.blockPrefix = nil
	if sparse {
		t.blockMap = make([]byte, (nBlocks+7)/8)
		for b, p := range present {
			if p {
				setBit(t.blockMap, b)
			}
		}
		t.buildBlockPrefix()
	}
	w.dirtyTablx = false
	return nil
}

// Flush writes the pending header, field description and .gdbtablx changes
// to disk.
func (t *Table) Flush() error {
	w := t.w
	if w == nil {
		return nil
	}
	if w.dirtyFields {
		if err := t.writeFieldDesc(); err != nil {
			return err
		}
	}
	if w.dirtyTablx {
		if err := t.writeTablx(); err != nil {
			return err
		}
	}
	if w.dirtyHeader {
		if err := t.writeHeader(); err != nil {
			return err
		}
	}
	if err := t.f.Sync(); err != nil {
		return err
	}
	if t.fx != nil {
		return t.fx.Sync()
	}
	return nil
}

// SetGridSizes replaces the spatial index grid sizes of the geometry field.
func (t *Table) SetGridSizes(sizes []float64) error {
	if t.GeomField < 0 {
		return nil
	}
	if err := t.beginWrite(); err != nil {
		return err
	}
	t.Fields[t.GeomField].Geom.GridSizes = append([]float64(nil), sizes...)
	t.w.dirtyFields = true
	return nil
}
