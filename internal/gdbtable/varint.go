package gdbtable

import "errors"

// ErrCorrupt is returned when a file does not follow the expected layout.
var ErrCorrupt = errors.New("gdbtable: corrupt or unsupported data")

// ReadVarUint decodes an unsigned LEB128-style integer (7 bits per byte,
// little-endian groups, msb set on all bytes except the last).
// It returns the value and the number of bytes consumed.
func ReadVarUint(b []byte) (uint64, int, error) {
	var v uint64
	var shift uint
	for i, c := range b {
		if shift >= 64 {
			return 0, 0, ErrCorrupt
		}
		v |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, ErrCorrupt
}

// ReadVarInt decodes a signed variable length integer. The first byte holds
// 6 value bits and a sign bit (0x40); following bytes hold 7 value bits.
func ReadVarInt(b []byte) (int64, int, error) {
	if len(b) == 0 {
		return 0, 0, ErrCorrupt
	}
	c := b[0]
	v := uint64(c & 0x3F)
	neg := c&0x40 != 0
	if c&0x80 == 0 {
		if neg {
			return -int64(v), 1, nil
		}
		return int64(v), 1, nil
	}
	shift := uint(6)
	for i := 1; i < len(b); i++ {
		if shift >= 64 {
			return 0, 0, ErrCorrupt
		}
		c = b[i]
		v |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			if neg {
				return -int64(v), i + 1, nil
			}
			return int64(v), i + 1, nil
		}
		shift += 7
	}
	return 0, 0, ErrCorrupt
}

// AppendVarUint appends the encoding of v to dst.
func AppendVarUint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// AppendVarInt appends the signed encoding of v to dst.
func AppendVarInt(dst []byte, v int64) []byte {
	var u uint64
	var sign byte
	if v < 0 {
		sign = 0x40
		u = uint64(-v)
	} else {
		u = uint64(v)
	}
	first := byte(u&0x3F) | sign
	u >>= 6
	if u == 0 {
		return append(dst, first)
	}
	dst = append(dst, first|0x80)
	for u >= 0x80 {
		dst = append(dst, byte(u)|0x80)
		u >>= 7
	}
	return append(dst, byte(u))
}

// Cursor is a small helper for sequential little-endian decoding with bounds
// checks. Any out-of-range read sets Err and returns zero values.
type Cursor struct {
	B   []byte
	Pos int
	Err error
}

func (c *Cursor) need(n int) bool {
	if c.Err != nil {
		return false
	}
	if n < 0 || c.Pos+n > len(c.B) {
		c.Err = ErrCorrupt
		return false
	}
	return true
}

func (c *Cursor) Remaining() int { return len(c.B) - c.Pos }

func (c *Cursor) Bytes(n int) []byte {
	if !c.need(n) {
		return nil
	}
	b := c.B[c.Pos : c.Pos+n]
	c.Pos += n
	return b
}

func (c *Cursor) U8() uint8 {
	if !c.need(1) {
		return 0
	}
	v := c.B[c.Pos]
	c.Pos++
	return v
}

func (c *Cursor) U16() uint16 {
	b := c.Bytes(2)
	if b == nil {
		return 0
	}
	return le.Uint16(b)
}

func (c *Cursor) U32() uint32 {
	b := c.Bytes(4)
	if b == nil {
		return 0
	}
	return le.Uint32(b)
}

func (c *Cursor) U64() uint64 {
	b := c.Bytes(8)
	if b == nil {
		return 0
	}
	return le.Uint64(b)
}

func (c *Cursor) F64() float64 {
	b := c.Bytes(8)
	if b == nil {
		return 0
	}
	return f64(b)
}

func (c *Cursor) VarUint() uint64 {
	if c.Err != nil {
		return 0
	}
	v, n, err := ReadVarUint(c.B[c.Pos:])
	if err != nil {
		c.Err = err
		return 0
	}
	c.Pos += n
	return v
}

func (c *Cursor) VarInt() int64 {
	if c.Err != nil {
		return 0
	}
	v, n, err := ReadVarInt(c.B[c.Pos:])
	if err != nil {
		c.Err = err
		return 0
	}
	c.Pos += n
	return v
}

// UTF16 reads n UTF-16 code units and returns them as a Go string.
func (c *Cursor) UTF16(n int) string {
	b := c.Bytes(2 * n)
	if b == nil {
		return ""
	}
	return DecodeUTF16(b)
}
