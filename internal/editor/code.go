package editor

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// Code blocks: a card of monospace text, colored by its language, with the
// language to choose and a copy button in a bar above the code.

const (
	// codeMargin is the room above and below a code block's card,
	// codeBar the height of the bar in it above the code, and codePad
	// the room around the code.
	codeMargin = 6
	codeBar    = 32
	codePad    = 14
)

// codeSpans colors a code block's text by its language's tokens. The spans
// hold exactly the block's text, so offsets in the layout stay the text's.
func codeSpans(code, language string, font ui.Font, pal *theme.Palette) []ui.Span {
	plain := []ui.Span{{Text: code, Font: font.Family, Size: font.Size}}
	lexer := lexers.Get(language)
	if lexer == nil || language == "" || code == "" {
		return plain
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return plain
	}
	var out []ui.Span
	rest := code
	for _, t := range it.Tokens() {
		// Some lexers end the text with a line break it did not have.
		text := t.Value
		if len(text) > len(rest) {
			text = rest
		}
		if !strings.HasPrefix(rest, text) {
			return plain
		}
		rest = rest[len(text):]
		if text == "" {
			continue
		}
		s := ui.Span{Text: text, Font: font.Family, Size: font.Size}
		s.Color, s.Italic = tokenColor(t.Type, pal)
		out = append(out, s)
	}
	if rest != "" {
		return plain
	}
	return out
}

// tokenColor is the color of a kind of token, and whether it is italic;
// a zero color is the text's.
func tokenColor(t chroma.TokenType, pal *theme.Palette) (ui.Color, bool) {
	switch {
	case t.InCategory(chroma.Comment):
		return pal.CodeComment, true
	case t == chroma.KeywordType || t == chroma.NameClass || t == chroma.NameNamespace:
		return pal.CodeType, false
	case t == chroma.KeywordConstant || t == chroma.NameConstant || t == chroma.NameBuiltin || t == chroma.NameAttribute:
		return pal.CodeNumber, false
	case t.InCategory(chroma.Keyword) || t == chroma.OperatorWord || t == chroma.GenericDeleted:
		return pal.CodeKeyword, false
	case t.InSubCategory(chroma.LiteralString):
		return pal.CodeString, false
	case t.InSubCategory(chroma.LiteralNumber) || t == chroma.GenericHeading || t == chroma.GenericSubheading:
		return pal.CodeNumber, false
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic || t == chroma.NameDecorator:
		return pal.CodeFunction, false
	case t == chroma.NameTag || t == chroma.GenericInserted:
		return pal.CodeTag, false
	}
	return ui.Color{}, false
}

// codeBarView builds a code block's bar: its language, to choose while
// editing, and a button that copies the code.
func (e *Editor) codeBarView(c *ui.Context, n *doc.Node, left float32, hovered bool, pal *theme.Palette) {
	bar := ui.Row(c.Key("code-bar")).Absolute().Top(codeMargin).Left(left + codePad - 6).Right(codePad - 6).Height(codeBar).AlignItems(ui.Center)
	bar.Children(func() {
		name := doc.CodeLanguageName(n.Str("language"))
		if e.ReadOnly {
			ui.Text(c, name).FontSize(12).TextColor(pal.TextMuted).Padding(0, 6)
		} else {
			b := ui.ButtonBase(c.Key("language")).Height(24).Padding(0, 6).Gap(4).Radius(6).TextColor(pal.TextMuted).
				AlignItems(ui.Center).Label("Language: " + name)
			if b.Hovered() {
				b.Background(pal.Hover).TextColor(pal.Text)
			}
			b.Children(func() {
				ui.Text(c, name).FontSize(12)
				ui.Icon(c, theme.Icon("chevron-down")).FontSize(11)
			})
			b.Menu(func(m *ui.Menu) {
				for _, l := range doc.CodeLanguages {
					if m.Item(l.Name).Checked(l.ID == n.Str("language")).Chosen() {
						e.SetCodeLanguage(n.ID, l.ID)
					}
				}
			})
		}
		ui.Spacer(c)
		copied := e.copied == n.ID && time.Since(e.copiedAt) < 1500*time.Millisecond
		icon, label := "copy", "Copy code"
		if copied {
			icon, label = "check", "Copied"
		}
		b := ui.ButtonBase(c.Key("copy")).Size(26, 24).Center().Radius(6).TextColor(pal.TextMuted).Label(label).Tooltip(label).
			Opacity(map[bool]float32{true: 1, false: 0}[hovered || copied])
		if b.Hovered() {
			b.Background(pal.Hover).TextColor(pal.Text)
		}
		b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(13) })
		if copied {
			// Show the copy icon again once the check has been seen.
			wait := 1500*time.Millisecond - time.Since(e.copiedAt)
			b.Draw(func(p *ui.Painter, _ ui.Rect) { p.After(wait) })
		}
		if b.Clicked() {
			e.services.WriteClipboard(n.Delta().Plain())
			e.copied, e.copiedAt = n.ID, time.Now()
		}
	})
}

// inCodeBar reports whether a point in the window is on a code block's
// bar, whose controls take the press.
func (e *Editor) inCodeBar(n *doc.Node, wy float32) bool {
	box, ok := e.geom[n.ID]
	return ok && n.Type == "code" && wy >= box.Y && wy < box.Y+codeMargin+codeBar
}

// SetCodeLanguage sets the language of a code block.
func (e *Editor) SetCodeLanguage(id, language string) {
	n := e.node(id)
	if n == nil || n.Type != "code" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n.Set("language", language)
	e.invalidate(id)
	e.changed()
}

// inCode reports whether the selection is within one code block.
func (e *Editor) inCode() bool {
	a, b := e.ordered()
	n := e.node(a.ID)
	return a.ID == b.ID && n != nil && n.Type == "code"
}

// codeEnter breaks the line at the caret, indenting the new line as the
// one it leaves. Enter on the second blank line at the end leaves the
// block, without the blank lines, for a paragraph after it.
func (e *Editor) codeEnter(n *doc.Node) {
	runes := []rune(n.Delta().Plain())
	off := e.focus.Off
	lines := strings.Split(string(runes), "\n")
	last := len(lines) - 1
	if off == len(runes) && last >= 2 && strings.TrimSpace(lines[last]) == "" && strings.TrimSpace(lines[last-1]) == "" {
		n.SetDelta(doc.Delta{{Text: strings.Join(lines[:last-1], "\n")}}.Compact())
		e.invalidate(n.ID)
		p := newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	}
	start := lineStart(runes, off)
	indent := start
	for indent < off && (runes[indent] == ' ' || runes[indent] == '\t') {
		indent++
	}
	ins := "\n" + string(runes[start:indent])
	n.SetDelta(n.Delta().Insert(off, ins, doc.Attrs{}))
	e.invalidate(n.ID)
	at := off + utf8.RuneCountInString(ins)
	e.Select(Pos{n.ID, at}, Pos{n.ID, at})
	e.changed()
}

// fenceEnter turns a paragraph of a Markdown fence, as "```go", into a
// code block in that language, and reports whether it did. A Mermaid
// fence makes a diagram, and "$$" math, each opened to write.
func (e *Editor) fenceEnter(n *doc.Node) bool {
	text := n.Delta().Plain()
	language, ok := doc.CodeFence(text)
	if n.Type != "paragraph" || e.focus.Off != utf8.RuneCountInString(text) || !ok && strings.TrimSpace(text) != "$$" {
		return false
	}
	if !ok || language == "mermaid" {
		b := doc.MathNode("")
		if ok {
			b = doc.DiagramNode("")
		}
		_ = e.Content.Insert(doc.Position{Before: n.ID}, b)
		n.SetDelta(nil)
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		e.Host.EditBlock(b.ID)
		return true
	}
	n.Type = "code"
	n.SetDelta(nil)
	n.Set("language", language)
	e.invalidate(n.ID)
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
	e.changed()
	return true
}

func lineStart(runes []rune, off int) int {
	for off > 0 && runes[off-1] != '\n' {
		off--
	}
	return off
}

// indentCode indents the lines of the selection in a code block, or
// outdents them, by the block's own step: a tab when its lines are
// indented with tabs, else two spaces. Tab at a caret puts a step there.
func (e *Editor) indentCode(n *doc.Node, out bool) {
	runes := []rune(n.Delta().Plain())
	unit := []rune("  ")
	if strings.Contains("\n"+string(runes), "\n\t") {
		unit = []rune("\t")
	}
	a, b := e.ordered()
	e.pushUndo(false)
	e.coalescing = false
	if !out && a.Off == b.Off {
		n.SetDelta(doc.Delta{{Text: string(runes[:a.Off]) + string(unit) + string(runes[a.Off:])}})
		e.invalidate(n.ID)
		at := a.Off + len(unit)
		e.Select(Pos{n.ID, at}, Pos{n.ID, at})
		e.changed()
		return
	}
	// The lines from the selection's first to its last, leaving out a line
	// the selection only reaches the start of.
	starts := []int{lineStart(runes, a.Off)}
	for i := starts[0]; i < len(runes); i++ {
		if runes[i] == '\n' && i+1 < b.Off {
			starts = append(starts, i+1)
		}
	}
	type edit struct{ at, removed, added int }
	var edits []edit
	for _, s := range starts {
		if !out {
			edits = append(edits, edit{s, 0, len(unit)})
			continue
		}
		k := 0
		for k < len(unit) && s+k < len(runes) && (runes[s+k] == ' ' || runes[s+k] == '\t') {
			k++
			if runes[s+k-1] == '\t' {
				break
			}
		}
		if k > 0 {
			edits = append(edits, edit{s, k, 0})
		}
	}
	if len(edits) == 0 {
		return
	}
	moved := func(x int) int {
		y := x
		for _, ed := range edits {
			if ed.at <= x {
				y += ed.added - min(ed.removed, x-ed.at)
			}
		}
		return y
	}
	for i := len(edits) - 1; i >= 0; i-- {
		ed := edits[i]
		var add []rune
		if ed.added > 0 {
			add = unit
		}
		runes = append(runes[:ed.at], append(add, runes[ed.at+ed.removed:]...)...)
	}
	n.SetDelta(doc.Delta{{Text: string(runes)}}.Compact())
	e.invalidate(n.ID)
	e.Select(Pos{n.ID, moved(e.anchor.Off)}, Pos{n.ID, moved(e.focus.Off)})
	e.changed()
}
