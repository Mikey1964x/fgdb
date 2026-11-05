package gdbtable

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

var le = binary.LittleEndian

func f64(b []byte) float64 { return math.Float64frombits(le.Uint64(b)) }
func f32(b []byte) float32 { return math.Float32frombits(le.Uint32(b)) }

// DecodeUTF16 converts little-endian UTF-16 bytes to a string.
func DecodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// EncodeUTF16 converts a string to little-endian UTF-16 bytes.
func EncodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, v := range u {
		le.PutUint16(b[2*i:], v)
	}
	return b
}

// UTF16Len returns the number of UTF-16 code units needed for s.
func UTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func appendU16(b []byte, v uint16) []byte { return le.AppendUint16(b, v) }
func appendU32(b []byte, v uint32) []byte { return le.AppendUint32(b, v) }
func appendU64(b []byte, v uint64) []byte { return le.AppendUint64(b, v) }
func appendF64(b []byte, v float64) []byte {
	return le.AppendUint64(b, math.Float64bits(v))
}

func testBit(b []byte, i int) bool { return b[i/8]&(1<<(i%8)) != 0 }
func setBit(b []byte, i int)       { b[i/8] |= 1 << (i % 8) }
func clearBit(b []byte, i int)     { b[i/8] &^= 1 << (i % 8) }

func f32bits(v float32) uint32 { return math.Float32bits(v) }
