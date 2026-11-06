package gdbtable

import (
	"math"
	"testing"
)

func TestVarUintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 32, math.MaxUint64} {
		b := AppendVarUint(nil, v)
		got, n, err := ReadVarUint(b)
		if err != nil || got != v || n != len(b) {
			t.Errorf("%d: got %d (%d bytes, %v)", v, got, n, err)
		}
	}
	if _, _, err := ReadVarUint([]byte{0x80}); err == nil {
		t.Error("truncated varuint: expected error")
	}
}

func TestVarIntRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 63, -63, 64, -64, 1 << 40, -(1 << 40), math.MaxInt64, -math.MaxInt64} {
		b := AppendVarInt(nil, v)
		got, n, err := ReadVarInt(b)
		if err != nil || got != v || n != len(b) {
			t.Errorf("%d: got %d (%d bytes, %v)", v, got, n, err)
		}
	}
	// Example from the specification: 0x42 is -2.
	if v, _, _ := ReadVarInt([]byte{0x42}); v != -2 {
		t.Errorf("0x42 = %d", v)
	}
}

func TestGUIDFormat(t *testing.T) {
	s := "{70737809-852C-4A03-9E22-2CECEA5B9BFA}"
	g, err := ParseGUID(s)
	if err != nil || g.String() != s {
		t.Errorf("round trip: %s %v", g, err)
	}
	// On-disk byte order is mixed-endian.
	if g[0] != 0x09 || g[3] != 0x70 || g[8] != 0x9E {
		t.Errorf("byte order: % x", g[:])
	}
}

func TestUTF16(t *testing.T) {
	for _, s := range []string{"", "abc", "héllo", "𝄞 clef"} {
		if got := DecodeUTF16(EncodeUTF16(s)); got != s {
			t.Errorf("%q -> %q", s, got)
		}
	}
}
