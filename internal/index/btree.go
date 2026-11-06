package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// Layout of .atx and .spx files, as written by GDAL's OpenFileGDB driver
// (filegdbindex_write.cpp): a B-tree of 4096-byte pages with fixed-size
// keys, followed by a 22-byte trailer.
const (
	PageSize    = 4096
	pageHeader  = 12 // next page id, entry count, unknown (0)
	trailerSize = 22
)

// Trailer flag byte, by key type.
const (
	FlagGUID    = 0x00
	FlagString  = 0x20
	FlagNumeric = 0x40
)

// Entry is an index key and the ObjectID of the row it refers to.
type Entry struct {
	Key []byte
	OID int64
}

func maxPerPage(keySize int) int { return (PageSize - pageHeader) / (4 + keySize) }

// WriteTree writes entries, which must be sorted by key then ObjectID, as
// an index B-tree of fixed-size keys.
func WriteTree(w io.Writer, entries []Entry, keySize int, flag byte) error {
	if keySize <= 0 || keySize > 255 {
		return fmt.Errorf("index: invalid key size %d", keySize)
	}
	per := maxPerPage(keySize)
	valOff := pageHeader + per*4
	n := len(entries)
	if uint64(n) > (((uint64(per)+1)*uint64(per)+1)*uint64(per)+1)*uint64(per) || n > 1<<31-1 {
		return fmt.Errorf("index: too many entries (%d)", n)
	}
	for _, e := range entries {
		if len(e.Key) != keySize {
			return fmt.Errorf("index: key of %d bytes, want %d", len(e.Key), keySize)
		}
		if e.OID < 0 || e.OID > 1<<32-1 {
			return fmt.Errorf("index: ObjectID %d does not fit in 32 bits", e.OID)
		}
	}

	bw := &pageWriter{w: w}
	page := make([]byte, 0, PageSize)
	u32 := func(v int) { page = binary.LittleEndian.AppendUint32(page, uint32(v)) }
	pad := func(to int) {
		for len(page) < to {
			page = append(page, 0)
		}
	}
	finish := func() {
		for len(page) < PageSize {
			page = append(page, 0)
		}
		bw.write(page)
		page = page[:0]
	}

	root := func(nChildren, mult int) {
		u32(0)
		if nChildren == 1 {
			u32(1)
		} else {
			u32(nChildren - 1)
		}
		for i := 0; i < nChildren; i++ {
			u32(2 + i)
		}
		pad(valOff)
		if nChildren == 1 {
			page = append(page, entries[n-1].Key...)
		} else {
			for i := 0; i < nChildren-1; i++ {
				page = append(page, entries[(i+1)*mult-1].Key...)
			}
		}
		finish()
	}
	leaves := func(base, nPages int) {
		for i := 0; i < nPages; i++ {
			cnt := per
			if i+1 < nPages {
				u32(base + i + 1)
			} else {
				u32(0)
				cnt = n - i*per
			}
			u32(cnt)
			u32(0)
			for j := 0; j < cnt; j++ {
				u32(int(entries[i*per+j].OID))
			}
			pad(valOff)
			for j := 0; j < cnt; j++ {
				page = append(page, entries[i*per+j].Key...)
			}
			finish()
		}
	}
	intermediate := func(base, nThis, nNext, mult int) {
		for i := 0; i < nThis; i++ {
			cnt := per
			if i+1 < nThis {
				u32(base + i + 1)
			} else {
				u32(0)
				cnt = nNext - i*per - 1
			}
			u32(cnt)
			for j := 0; j < 1+cnt; j++ {
				u32(base + nThis + i*per + j)
			}
			pad(valOff)
			for j := 0; j < cnt; j++ {
				page = append(page, entries[(i*per+j+1)*mult-1].Key...)
			}
			finish()
		}
	}
	divUp := func(a, b int) int { return (a + b - 1) / b }

	var depth int
	nLeaf := divUp(n, per)
	switch {
	case n <= per:
		depth = 1
		leaves(1, 1)
	case n <= (per+1)*per:
		depth = 2
		root(nLeaf, per)
		leaves(2, nLeaf)
	case n <= ((per+1)*per+1)*per:
		depth = 3
		nL2 := divUp(nLeaf-1, per)
		root(nL2, per*per)
		intermediate(2, nL2, nLeaf, per)
		leaves(2+nL2, nLeaf)
	default:
		depth = 4
		nL3 := divUp(nLeaf-1, per)
		nL2 := divUp(nL3-1, per)
		root(nL2, per*per*per)
		intermediate(2, nL2, nL3, per*per)
		intermediate(2+nL2, nL3, nLeaf, per)
		leaves(2+nL2+nL3, nLeaf)
	}

	t := []byte{byte(keySize), flag}
	t = binary.LittleEndian.AppendUint32(t, 1)
	t = binary.LittleEndian.AppendUint32(t, uint32(depth))
	t = binary.LittleEndian.AppendUint32(t, uint32(n))
	t = binary.LittleEndian.AppendUint32(t, 0)
	t = binary.LittleEndian.AppendUint32(t, 1)
	bw.write(t)
	return bw.err
}

type pageWriter struct {
	w   io.Writer
	err error
}

func (p *pageWriter) write(b []byte) {
	if p.err == nil {
		_, p.err = p.w.Write(b)
	}
}

// WriteTreeFile writes an index B-tree to a file, replacing it atomically.
func WriteTreeFile(path string, entries []Entry, keySize int, flag byte) error {
	var buf bytes.Buffer
	if err := WriteTree(&buf, entries, keySize, flag); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadTree returns the key size and the entries of an index file, in leaf
// order.
func ReadTree(path string) (keySize int, entries []Entry, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	if len(b) < PageSize+trailerSize {
		return 0, nil, gdbtable.ErrCorrupt
	}
	tr := b[len(b)-trailerSize:]
	keySize = int(tr[0])
	depth := int(binary.LittleEndian.Uint32(tr[6:]))
	nPages := (len(b) - trailerSize) / PageSize
	if keySize == 0 || depth < 1 || depth > 4 {
		return 0, nil, gdbtable.ErrCorrupt
	}
	per := maxPerPage(keySize)
	valOff := pageHeader + per*4
	pageAt := func(id int) []byte {
		if id < 1 || id > nPages {
			return nil
		}
		return b[(id-1)*PageSize : id*PageSize]
	}
	id := 1
	for l := 1; l < depth; l++ {
		p := pageAt(id)
		if p == nil {
			return 0, nil, gdbtable.ErrCorrupt
		}
		id = int(binary.LittleEndian.Uint32(p[8:]))
	}
	for seen := 0; id != 0; seen++ {
		p := pageAt(id)
		if p == nil || seen > nPages {
			return 0, nil, gdbtable.ErrCorrupt
		}
		cnt := int(binary.LittleEndian.Uint32(p[4:]))
		if cnt > per {
			return 0, nil, gdbtable.ErrCorrupt
		}
		for j := 0; j < cnt; j++ {
			oid := int64(binary.LittleEndian.Uint32(p[pageHeader+4*j:]))
			k := p[valOff+j*keySize : valOff+(j+1)*keySize]
			entries = append(entries, Entry{Key: append([]byte(nil), k...), OID: oid})
		}
		id = int(binary.LittleEndian.Uint32(p))
	}
	return keySize, entries, nil
}
