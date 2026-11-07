package filepreview

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Text comes out as it reads, whatever its encoding, and a guess says so.
func TestDecode(t *testing.T) {
	for _, c := range []struct {
		name, lang string
		b          []byte
		want, enc  string
	}{
		{"utf-8", "en", []byte("héllo 字"), "héllo 字", ""},
		{"bom", "en", []byte("\xEF\xBB\xBFhello"), "hello", ""},
		{"utf-16", "en", []byte{0xFF, 0xFE, 'h', 0, 'i', 0}, "hi", ""},
		{"western", "en", []byte("caf\xE9"), "café", "Windows-1252"},
		{"chinese", "zh-Hans", []byte{0xD6, 0xD0, 0xCE, 0xC4}, "中文", "GB18030"},
		{"japanese", "ja", []byte{0x93, 0xFA, 0x96, 0x7B}, "日本", "Shift-JIS"},
	} {
		got, err := Decode(c.b, c.lang, false)
		if err != nil || got.Text != c.want || got.Encoding != c.enc {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
	if _, err := Decode([]byte("a\x00b"), "en", false); !errors.Is(err, ErrBinary) {
		t.Errorf("binary: %v", err)
	}
}

// A long file is read to its limit, not within a character.
func TestReadCut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("ab字字"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, 4, "en")
	if err != nil || got.Text != "ab" || !got.Cut {
		t.Errorf("%+v, %v", got, err)
	}
	if got, err = Read(path, 8, "en"); err != nil || got.Text != "ab字字" || got.Cut {
		t.Errorf("whole: %+v, %v", got, err)
	}
	if _, err := Read(path+"x", 8, "en"); err == nil {
		t.Error("a file that isn't there read")
	}
}

// A table takes uneven rows and its bounds, and shows what comes before a
// line it can't read.
func TestParseCSV(t *testing.T) {
	rows, cut := ParseCSV("a,b\n1\n\"x,y\",2,3\n", ',')
	if len(rows) != 3 || len(rows[1]) != 1 || rows[2][0] != "x,y" || cut {
		t.Errorf("%q, %v", rows, cut)
	}
	if rows, _ := ParseCSV("a\tb\n", '\t'); len(rows[0]) != 2 {
		t.Errorf("tabs: %q", rows)
	}
	long := strings.Repeat("x\n", MaxRows+1)
	if rows, cut := ParseCSV(long, ','); len(rows) != MaxRows || !cut {
		t.Errorf("%d rows, cut %v", len(rows), cut)
	}
	var wide []string
	for i := range MaxColumns + 1 {
		wide = append(wide, strconv.Itoa(i))
	}
	if rows, cut := ParseCSV(strings.Join(wide, ","), ','); len(rows[0]) != MaxColumns || !cut {
		t.Errorf("%d columns, cut %v", len(rows[0]), cut)
	}
}
