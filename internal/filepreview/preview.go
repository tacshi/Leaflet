package filepreview

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

const (
	MaxText     = 4 << 20
	MaxMarkdown = 1 << 20
)

const (
	MaxRows    = 100_000
	MaxColumns = 256
)

var ErrBinary = errors.New("the file isn't text")

type Text struct {
	Text     string
	Encoding string
	Cut      bool
}

func Read(path string, limit int64, lang string) (Text, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return Text{}, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return Text{}, err
	}
	cut := int64(len(b)) > limit
	if cut {
		b = b[:limit]
	}
	t, err := Decode(b, lang, cut)
	t.Cut = cut
	return t, err
}

func Decode(b []byte, lang string, cut bool) (Text, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return Text{Text: string(trimPartial(b[3:], cut))}, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(trimPartialUTF16(b, cut))
		if err != nil {
			return Text{}, err
		}
		return Text{Text: string(out)}, nil
	}
	head := b[:min(len(b), 8<<10)]
	if bytes.IndexByte(head, 0) >= 0 {
		return Text{}, ErrBinary
	}
	if s := trimPartial(b, cut); utf8.Valid(s) {
		return Text{Text: string(s)}, nil
	}
	for _, e := range legacy(lang) {
		for drop := 0; drop <= partialMax && drop <= len(b); drop++ {
			out, err := e.enc.NewDecoder().Bytes(b[:len(b)-drop])
			if err == nil && !bytes.ContainsRune(out, utf8.RuneError) {
				return Text{Text: string(out), Encoding: e.name}, nil
			}
			if !cut {
				break
			}
		}
	}
	return Text{}, ErrBinary
}

const partialMax = 3

// trimPartialUTF16 drops what a cut left of a character at the end: an odd
// byte, and the first half of a surrogate pair.
func trimPartialUTF16(b []byte, cut bool) []byte {
	if !cut {
		return b
	}
	b = b[:len(b)&^1]
	if len(b) < 4 {
		return b
	}
	high := b[len(b)-1]
	if b[0] == 0xFE {
		high = b[len(b)-2]
	}
	if high >= 0xD8 && high <= 0xDB {
		b = b[:len(b)-2]
	}
	return b
}

func trimPartial(b []byte, cut bool) []byte {
	if !cut {
		return b
	}
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, n := utf8.DecodeLastRune(b); r != utf8.RuneError || n != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

type legacyEncoding struct {
	name string
	enc  encoding.Encoding
}

func legacy(lang string) []legacyEncoding {
	gb := legacyEncoding{"GB18030", simplifiedchinese.GB18030}
	sjis := legacyEncoding{"Shift-JIS", japanese.ShiftJIS}
	euc := legacyEncoding{"EUC-KR", korean.EUCKR}
	west := legacyEncoding{"Windows-1252", charmap.Windows1252}
	switch {
	case strings.HasPrefix(lang, "ja"):
		return []legacyEncoding{sjis, gb, euc, west}
	case strings.HasPrefix(lang, "ko"):
		return []legacyEncoding{euc, gb, sjis, west}
	case strings.HasPrefix(lang, "zh"):
		return []legacyEncoding{gb, sjis, euc, west}
	}
	return []legacyEncoding{west}
}

func ParseCSV(text string, comma rune) (rows [][]string, cut bool) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	for {
		row, err := r.Read()
		if err != nil {
			return rows, cut || !errors.Is(err, io.EOF)
		}
		if len(rows) == MaxRows {
			return rows, true
		}
		if len(row) > MaxColumns {
			row, cut = row[:MaxColumns], true
		}
		rows = append(rows, row)
	}
}
