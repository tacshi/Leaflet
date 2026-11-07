// Package filepreview reads files to show them in the desktop app: as text
// in whatever encoding they have, up to a limit, or as a table. It only
// reads: what a file says is shown, never acted on.
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

// The most of a file a preview reads.
const (
	MaxText     = 4 << 20
	MaxMarkdown = 1 << 20
)

// The most of a table a preview shows.
const (
	MaxRows    = 100_000
	MaxColumns = 256
)

// ErrBinary says a file isn't text, whatever its name says.
var ErrBinary = errors.New("the file isn't text")

// Text is a file read as text.
type Text struct {
	Text string
	// Encoding names the encoding the file was guessed to have, "" for a
	// file that says or is plainly UTF-8.
	Encoding string
	// Cut says the file goes on past Text.
	Cut bool
}

// Read reads up to limit bytes of the file at path as text; lang, the
// app's language, says which legacy encoding to try first.
func Read(path string, limit int64, lang string) (Text, error) {
	f, err := os.Open(path) // #nosec G304 -- a file of the store's.
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

// Decode reads b as text. A BOM says its encoding, else UTF-8, else the
// first of the legacy encodings to read it whole, or ErrBinary for bytes
// that are no text. cut says b ends mid-file, perhaps within a character.
func Decode(b []byte, lang string, cut bool) (Text, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return Text{Text: string(trimPartial(b[3:], cut))}, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(b)
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
		out, err := e.enc.NewDecoder().Bytes(b)
		if err == nil && !bytes.ContainsRune(out, utf8.RuneError) {
			return Text{Text: string(out), Encoding: e.name}, nil
		}
	}
	return Text{}, ErrBinary
}

// trimPartial leaves out a character cut in two at the end of b, where cut
// says it may be.
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

// legacy are the encodings to guess a file has, the likeliest in lang
// first. Windows-1252 reads any bytes, so it comes last.
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

// ParseCSV reads text as rows of values separated by comma, of at most
// MaxRows rows and MaxColumns columns; cut says it left some out. A line
// that can't be read ends it: what came before still shows.
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
