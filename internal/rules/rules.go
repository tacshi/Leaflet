package rules

import (
	"unicode"

	"leaflet/internal/doc"
)

type Block struct {
	Type         string
	Collapsed    bool
	Empty        bool
	Nested, Last bool
	Holds        bool
}

func (b Block) text() bool { return doc.TextTypes[b.Type] }

type Action int

const (
	Key Action = iota
	Split
	SplitInside
	LineBefore
	LineAfter
	CodeLine
	Outdent
	Plain
	SelectBefore
	SelectAfter
	SelectThis
	Remove
	Nothing
)

var actionNames = [...]string{
	Key: "key", Split: "split", SplitInside: "inside", LineBefore: "before", LineAfter: "after",
	CodeLine: "code", Outdent: "outdent", Plain: "plain", SelectBefore: "selectBefore",
	SelectAfter: "selectAfter", SelectThis: "selectThis", Remove: "remove", Nothing: "nothing",
}

func (a Action) String() string {
	if a < 0 || int(a) >= len(actionNames) {
		return "nothing"
	}
	return actionNames[a]
}

func NewLine(typ string) (string, map[string]any) {
	switch typ {
	case "bulleted_list", "numbered_list", "toggle_list":
		return typ, nil
	case "todo_list":
		return typ, map[string]any{"checked": false}
	}
	return "paragraph", nil
}

type Line struct {
	Action Action
	Type   string
	Data   map[string]any
	Unfold bool
}

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

func Unstyle(b Block) Action {
	if b.Nested && (doc.NestableTypes[b.Type] || b.Type == "paragraph" && b.Last) {
		return Outdent
	}
	return Plain
}

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

func Delete(b, next Block) Action {
	switch {
	case !next.text():
		return SelectAfter
	case !b.text():
		return SelectThis
	}
	return Key
}

func Nests(typ, under string) bool {
	return doc.TextTypes[typ] && doc.NestableTypes[under] && (doc.NestableTypes[typ] || under == "toggle_list")
}

type Fold struct {
	Type      string
	Depth     int
	Level     int
	Collapsed bool
}

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

func level(b Fold) int {
	if b.Level == 0 {
		return 1
	}
	return b.Level
}

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
