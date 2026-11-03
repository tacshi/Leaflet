package mobile

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/locales"
)

// The rules of the page's editor, as the desktop's editor follows them
// (internal/rules, internal/doc): the app makes each change in its own
// text, a paragraph a block, and asks the core what the change is. Each
// is asked of one block, or a key, as it is typed; offsets are in UTF-16
// units, as UIKit counts them.

// blockTypes are the kinds of blocks, as BlockTypes gives them.
type blockTypes struct {
	// Text are the blocks that hold text (doc.TextTypes), and Nestable
	// those that hold blocks (doc.NestableTypes).
	Text     []string `json:"text"`
	Nestable []string `json:"nestable"`
}

// BlockTypes returns the kinds of blocks that hold text and those that
// hold blocks, as JSON {"text", "nestable"}, for the app to read once.
func BlockTypes() string {
	defer catch(nil)
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	raw, _ := json.Marshal(blockTypes{Text: keys(doc.TextTypes), Nestable: keys(doc.NestableTypes)})
	return string(raw)
}

// NewBlockID returns a new block's ID, as the core makes them.
func NewBlockID() string {
	defer catch(nil)
	return doc.NewBlockID()
}

// newBlock is a block's type and data, as NewLine and Enter give them.
type newBlock struct {
	Type string         `json:"type,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

// NewLine returns the block a new line after a block of type typ starts
// as (rules.NewLine), as JSON {"type", "data"}.
func NewLine(typ string) string {
	defer catch(nil)
	t, data := rules.NewLine(typ)
	raw, _ := encode(newBlock{Type: t, Data: data})
	return raw
}

// enter is what Enter does, as Enter gives it.
type enter struct {
	Action string `json:"action"`
	newBlock
	Unfold bool `json:"unfold,omitempty"`
}

// Enter returns what Enter does in a block of type typ (rules.Enter), as
// JSON {"action", "type", "data", "unfold"}: collapsed is a toggle or a
// heading folded, empty a block of no text, atStart the caret at its
// start; nested is a block in a list item or a toggle, last the last in
// it, and holds a block that holds blocks.
func Enter(typ string, collapsed, empty, atStart, nested, last, holds bool) string {
	defer catch(nil)
	line := rules.Enter(rules.Block{Type: typ, Collapsed: collapsed, Empty: empty, Nested: nested, Last: last, Holds: holds}, atStart)
	raw, _ := encode(enter{Action: line.Action.String(), newBlock: newBlock{Type: line.Type, Data: line.Data}, Unfold: line.Unfold})
	return raw
}

// Backspace returns what Backspace does at the start of a block of type
// typ, after a block of type prev, "" at the page's start (rules.Backspace):
// "outdent", "plain", "remove", "selectBefore", "nothing", or "key" for the
// key's own, which joins the two.
func Backspace(typ string, empty, nested, last bool, prev string) string {
	defer catch(nil)
	b := rules.Block{Type: typ, Empty: empty, Nested: nested, Last: last}
	return rules.Backspace(b, rules.Block{Type: prev}, prev != "").String()
}

// Delete returns what Delete does at the end of a block of type typ,
// before one of type next (rules.Delete): "selectAfter", "selectThis", or
// "key" for the key's own, which joins the two.
func Delete(typ, next string) string {
	defer catch(nil)
	return rules.Delete(rules.Block{Type: typ}, rules.Block{Type: next}).String()
}

// Nests reports whether a block of type typ nests in the block before it,
// of type under, as Tab nests it (rules.Nests).
func Nests(typ, under string) bool {
	defer catch(nil)
	return rules.Nests(typ, under)
}

// codeEnter is what Enter does in code, as CodeEnter gives it.
type codeEnter struct {
	Leave  bool   `json:"leave,omitempty"`
	Keep   int    `json:"keep,omitempty"`
	Insert string `json:"insert,omitempty"`
}

// CodeEnter returns what Enter does in a code block of code, its lines
// apart by "\n", at caret (rules.CodeEnter), as JSON {"leave", "keep",
// "insert"}: the block left, keeping its text up to keep, or insert put in
// at the caret.
func CodeEnter(code string, caret int) string {
	defer catch(nil)
	leave, keep, insert := rules.CodeEnter(code, runeOffset(code, caret))
	raw, _ := encode(codeEnter{Leave: leave, Keep: unitOffset(code, keep), Insert: insert})
	return raw
}

// indented is code indented, as IndentCode gives it.
type indented struct {
	Code   string `json:"code"`
	Anchor int    `json:"anchor"`
	Focus  int    `json:"focus"`
}

// IndentCode returns code, its lines apart by "\n", with the lines from
// anchor's to focus's indented, or outdented when out (rules.IndentCode),
// as JSON {"code", "anchor", "focus"}; "" when nothing changes.
func IndentCode(code string, anchor, focus int, out bool) string {
	defer catch(nil)
	made, a, f, ok := rules.IndentCode(code, runeOffset(code, anchor), runeOffset(code, focus), out)
	if !ok {
		return ""
	}
	raw, _ := encode(indented{Code: made, Anchor: unitOffset(made, a), Focus: unitOffset(made, f)})
	return raw
}

// Folded returns which of a page's blocks folds hide (rules.Hidden), a
// "1" or "0" for each. blocks are the page's in order, a line each of
// their type, depth, level and "1" when folded, apart by commas, as
// "heading,0,2,1".
func Folded(blocks string) string {
	defer catch(nil)
	var folds []rules.Fold
	for line := range strings.Lines(blocks) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), ",")
		if len(f) != 4 {
			continue
		}
		depth, _ := strconv.Atoi(f[1])
		level, _ := strconv.Atoi(f[2])
		folds = append(folds, rules.Fold{Type: f[0], Depth: depth, Level: level, Collapsed: f[3] == "1"})
	}
	var b strings.Builder
	for _, h := range rules.Hidden(folds) {
		if h {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// MostlyCJK reports whether text is mostly Chinese, Japanese or Korean,
// whose lines are set higher (rules.MostlyCJK).
func MostlyCJK(text string) bool {
	defer catch(nil)
	return rules.MostlyCJK(text)
}

// NextTone returns the tone a callout of tone takes as its icon is tapped
// (doc.NextTone).
func NextTone(tone string) string {
	defer catch(nil)
	return doc.NextTone(tone)
}

// SourceKey returns the key a math or diagram block of type typ keeps its
// source under (doc.SourceKey).
func SourceKey(typ string) string {
	defer catch(nil)
	return doc.SourceKey(typ)
}

// LinkTarget returns where a link to href goes when it is followed, as on
// the desktop: a page, or a block of one, or a web or mail link
// (doc.SafeHref); "" for a link that goes nowhere, as one to a file or an
// app, which a shared page or an agent can write.
func LinkTarget(href string) string {
	defer catch(nil)
	href = strings.TrimSpace(href)
	if _, ok := doc.PageLinkID(href); ok {
		return href
	}
	return doc.SafeHref(href)
}

// TableText returns a table block's cells as they show, their text
// without its formats (doc.PlainTableRows): a JSON array of rows. node is
// the block, as JSON.
func TableText(node string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	return encode(doc.PlainTableRows(&n))
}

// due is a due date's chip, as Due gives it.
type due struct {
	Label   string `json:"label"`
	Overdue bool   `json:"overdue,omitempty"`
	Today   bool   `json:"today,omitempty"`
}

// Due returns what a to-do's due date, as the block keeps it, says at
// now, in milliseconds since 1970, in the language locale, as JSON
// {"label", "overdue", "today"} (doc.DueLabel, doc.Overdue, doc.DueToday);
// "" for a date it can't read.
func Due(locale, date string, now int64) string {
	defer catch(nil)
	t, timed, ok := doc.ParseDue(date)
	if !ok {
		return ""
	}
	at := time.UnixMilli(now)
	raw, _ := encode(due{
		Label:   doc.DueLabel(locales.In(locale), t, timed, at),
		Overdue: doc.Overdue(t, timed, at),
		Today:   doc.DueToday(t, at),
	})
	return raw
}

// runeOffset is the rune offset in s of off, in UTF-16 units.
func runeOffset(s string, off int) int {
	units, runes := 0, 0
	for _, r := range s {
		if units >= off {
			break
		}
		units += utf16.RuneLen(r)
		runes++
	}
	return runes
}

// unitOffset is the UTF-16 offset in s of off, in runes.
func unitOffset(s string, off int) int {
	units, runes := 0, 0
	for _, r := range s {
		if runes >= off {
			break
		}
		units += utf16.RuneLen(r)
		runes++
	}
	return units
}
