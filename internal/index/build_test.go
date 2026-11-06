package index

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// TestRebuildAttributeIndexes rebuilds every attribute index of the sample
// geodatabases and compares the entries with the files written by Esri
// software.
func TestRebuildAttributeIndexes(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/*.gdb/*.gdbindexes")
	n := 0
	for _, fn := range files {
		defs, err := ReadDefs(fn)
		if err != nil {
			t.Fatal(err)
		}
		base := strings.TrimSuffix(fn, ".gdbindexes")
		tb, err := gdbtable.Open(base + ".gdbtable")
		if err != nil {
			continue
		}
		for _, d := range defs {
			if d.Magic2 != KindAttribute {
				continue
			}
			atx := base + "." + d.Name + ".atx"
			keySize, want, err := ReadTree(atx)
			if err != nil {
				continue
			}
			got, err := BuildAttribute(tb, d.Expression)
			if err != nil {
				t.Errorf("%s: %v", atx, err)
				continue
			}
			n++
			if got.KeySize != keySize {
				t.Errorf("%s: key size %d, want %d", atx, got.KeySize, keySize)
				continue
			}
			if !sameEntries(got.Entries, want) {
				t.Errorf("%s: entries differ:\n got %s\nwant %s", atx, dump(got.Entries), dump(want))
			}
		}
		tb.Close()
	}
	if n == 0 {
		t.Fatal("no index compared")
	}
	t.Logf("compared %d indexes", n)
}

// sameEntries compares entries as sets of (key, oid): Esri may order equal
// keys differently.
func sameEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].Key, b[i].Key) {
			return false
		}
	}
	seen := map[string]int{}
	for _, e := range a {
		seen[string(e.Key)+"\x00"+string(rune(e.OID))]++
	}
	for _, e := range b {
		seen[string(e.Key)+"\x00"+string(rune(e.OID))]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

func dump(es []Entry) string {
	var sb strings.Builder
	for i, e := range es {
		if i == 8 {
			sb.WriteString("...")
			break
		}
		k := e.Key
		if len(k) > 16 {
			k = k[:16]
		}
		sb.WriteString(strings.ReplaceAll(string(k), "\x00", ""))
		sb.WriteString("/")
		sb.WriteString(string(rune('0' + e.OID%10)))
		sb.WriteString(" ")
	}
	return sb.String()
}

func TestTreeRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 340, 341, 1000, 340 * 341, 340*341 + 1, 340 * 341 * 3} {
		es := make([]Entry, n)
		for i := range es {
			k := make([]byte, 8)
			k[0], k[1], k[2] = byte(i>>16), byte(i>>8), byte(i)
			es[i] = Entry{Key: k, OID: int64(i + 1)}
		}
		var buf bytes.Buffer
		if err := WriteTree(&buf, es, 8, FlagNumeric); err != nil {
			t.Fatal(err)
		}
		fn := filepath.Join(t.TempDir(), "x.atx")
		if err := writeFile(fn, buf.Bytes()); err != nil {
			t.Fatal(err)
		}
		_, got, err := ReadTree(fn)
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("%d: read %d entries", n, len(got))
		}
		for i := range got {
			if got[i].OID != es[i].OID || !bytes.Equal(got[i].Key, es[i].Key) {
				t.Fatalf("%d: entry %d differs", n, i)
			}
		}
	}
}

func writeFile(fn string, b []byte) error { return os.WriteFile(fn, b, 0o644) }
