// Package rules is how a page's blocks change as it is written, for the
// desktop's editor and the iOS app's alike: what Enter, Backspace and
// Delete do at a block's edges, the block a new line starts as, which
// blocks Tab nests in which, how a code block's lines go on and indent,
// what folded toggles and headings hide, and how high a paragraph's lines
// are set. The desktop's editor works on the page's tree, and the iOS
// app's on its text, a paragraph a block: each makes the change in its
// own model; the rules are here.
package rules

import (
	"unicode"

	"leaflet/internal/doc"
)

// Block is what the rules know of a block: its kind, whether it is
// folded or empty, and where it is.
type Block struct {
	Type string
	// Collapsed is a toggle or a heading folded.
	Collapsed bool
	// Empty is a block of text with none.
	Empty bool
	// Nested is a block in another block, which holds blocks: in a list
	// item or a toggle, not a table's cell. Last is the last of the blocks
	// in it.
	Nested, Last bool
	// Holds says blocks are nested in it.
	Holds bool
}

// text reports whether the block holds text.
func (b Block) text() bool { return doc.TextTypes[b.Type] }

// An Action is what a key does at a block's edge.
type Action int

const (
	// Key is the key's own edit: Backspace and Delete join the block's
	// text to the one before it, or after.
	Key Action = iota
	// Split splits the block at the caret: the text after it goes in a
	// new block after the block, and after what the block holds.
	Split
	// SplitInside splits the block at the caret: the text after it goes in
	// a new block first among the blocks it holds.
	SplitInside
	// LineBefore puts a new empty block before the block, the caret
	// staying in it, as Enter does at its start.
	LineBefore
	// LineAfter puts an empty paragraph after a block without text.
	LineAfter
	// CodeLine breaks the line in a code block (CodeEnter).
	CodeLine
	// Outdent takes the block out of the block it is in, after it.
	Outdent
	// Plain turns the block into a paragraph where it is.
	Plain
	// SelectBefore and SelectAfter choose the block without text before
	// the block, or after it, whole, to go with another key; SelectThis
	// chooses the block itself, which has no text.
	SelectBefore
	SelectAfter
	SelectThis
	// Remove takes the empty block away.
	Remove
	// Nothing leaves the page as it is.
	Nothing
)

// actionNames are the actions' names, as String gives them.
var actionNames = [...]string{
	Key: "key", Split: "split", SplitInside: "inside", LineBefore: "before", LineAfter: "after",
	CodeLine: "code", Outdent: "outdent", Plain: "plain", SelectBefore: "selectBefore",
	SelectAfter: "selectAfter", SelectThis: "selectThis", Remove: "remove", Nothing: "nothing",
}

// String is the action's name, as the iOS app reads it.
func (a Action) String() string {
	if a < 0 || int(a) >= len(actionNames) {
		return "nothing"
	}
	return actionNames[a]
}

// NewLine is the block a new line after a block of type typ starts as: a
// list goes on as a list, a to-do unchecked; anything else, a heading, a
// quote or a callout, ends with the line, for a paragraph.
func NewLine(typ string) (string, map[string]any) {
	switch typ {
	case "bulleted_list", "numbered_list", "toggle_list":
		return typ, nil
	case "todo_list":
		return typ, map[string]any{"checked": false}
	}
	return "paragraph", nil
}

// Line is what Enter makes: an Action, and for Split, SplitInside and
// LineBefore the type and data of the new block.
type Line struct {
	Action Action
	Type   string
	Data   map[string]any
	// Unfold says the block unfolds as it splits: a folded heading's new
	// line starts its section, which shows.
	Unfold bool
}

// Enter is what Enter does in block b, the caret at its start when
// atStart. A block without text gets a paragraph after it; Enter in an
// empty list item, quote or callout ends it, and an empty paragraph last
// in a toggle leaves it (Unstyle); at the start of a block, a new empty
// one goes before it; in an open toggle, or a list item holding items,
// the new line is the first in it; else it goes after the block.
func Enter(b Block, atStart bool) Line {
	switch {
	case !b.text():
		return Line{Action: LineAfter, Type: "paragraph"}
	case b.Type == "code":
		return Line{Action: CodeLine}
	case b.Empty && (b.Type != "paragraph" && b.Type != "heading" || b.Type == "paragraph" && b.Nested && b.Last):
		return Line{Action: Unstyle(b)}
	case atStart && !b.Empty:
		typ, data := NewLine(b.Type)
		return Line{Action: LineBefore, Type: typ, Data: data}
	case b.Type == "toggle_list" && !b.Collapsed:
		return Line{Action: SplitInside, Type: "paragraph"}
	}
	typ, data := NewLine(b.Type)
	if doc.NestableTypes[b.Type] && b.Holds && !b.Collapsed {
		return Line{Action: SplitInside, Type: typ, Data: data}
	}
	return Line{Action: Split, Type: typ, Data: data, Unfold: b.Type == "heading" && b.Collapsed}
}

// Unstyle is what ends a block of text other than a paragraph, as
// Backspace at its start and Enter in it empty do: a nested list item
// goes up a level, as does a paragraph last in what it is in, and any
// other block becomes a paragraph where it is.
func Unstyle(b Block) Action {
	if b.Nested && (doc.NestableTypes[b.Type] || b.Type == "paragraph" && b.Last) {
		return Outdent
	}
	return Plain
}

// Backspace is what Backspace does at the start of text block b, after
// prev, the block before it, if any (hasPrev). A block other than a
// paragraph, or a paragraph last in another block, ends (Unstyle). Else
// the block joins the one before, but a block without text before it is
// chosen first, or, after one, an empty block goes.
func Backspace(b Block, prev Block, hasPrev bool) Action {
	switch {
	case b.Type != "paragraph" || b.Nested && b.Last:
		return Unstyle(b)
	case !hasPrev:
		return Nothing
	case !prev.text() && b.Empty:
		return Remove
	case !prev.text():
		return SelectBefore
	}
	return Key
}

// Delete is what Delete does at the end of block b, before next: a block
// without text after it is chosen first, to go with another Delete, as is
// b itself without text; else the two join.
func Delete(b, next Block) Action {
	switch {
	case !next.text():
		return SelectAfter
	case !b.text():
		return SelectThis
	}
	return Key
}

// Nests reports whether a block of type typ nests in the block before it,
// of type under, as Tab nests it: list items in list items, and any text
// in a toggle.
func Nests(typ, under string) bool {
	return doc.TextTypes[typ] && doc.NestableTypes[under] && (doc.NestableTypes[typ] || under == "toggle_list")
}

// A Fold is a block as Hidden reads it: its type, how deep it is in the
// page, its level when a heading, and whether it is folded.
type Fold struct {
	Type      string
	Depth     int
	Level     int
	Collapsed bool
}

// Hidden reports which of blocks, a page's in the order it reads, folds
// hide: the blocks in a closed toggle, and a folded heading's section,
// the blocks after it at its depth and those in them, up to the next
// heading of its level or higher.
func Hidden(blocks []Fold) []bool {
	hidden := make([]bool, len(blocks))
	toggle, heading := -1, -1
	for i, b := range blocks {
		if heading >= 0 {
			h := blocks[heading]
			if b.Depth < h.Depth || b.Depth == h.Depth && b.Type == "heading" && level(b) <= level(h) {
				heading = -1
			}
		}
		if toggle >= 0 && b.Depth <= blocks[toggle].Depth {
			toggle = -1
		}
		if heading >= 0 || toggle >= 0 {
			hidden[i] = true
			continue
		}
		if b.Collapsed && b.Type == "toggle_list" {
			toggle = i
		}
		if b.Collapsed && b.Type == "heading" {
			heading = i
		}
	}
	return hidden
}

// level is a heading's level, 1 when it has none.
func level(b Fold) int {
	if b.Level == 0 {
		return 1
	}
	return b.Level
}

// MostlyCJK reports whether text is mostly Chinese, Japanese or Korean,
// whose lines are set higher than Latin text's, as their characters fill
// the em square. One of those characters counts as much as a word of four
// letters, so a few English terms or a link in a Chinese paragraph leave
// it Chinese.
func MostlyCJK(text string) bool {
	cjk, other := 0, 0
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		case unicode.IsLetter(r):
			other++
		}
	}
	return cjk > 0 && cjk*4 >= other
}
