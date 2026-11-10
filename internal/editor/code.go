package editor

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/internal/theme"
	"leaflet/locales"
)

const (
	codeMargin = 6
	codeBar    = 32
	codePad    = 14
)

func codeSpans(code, language string, font ui.Font, pal *theme.Palette) []ui.Span {
	plain := []ui.Span{{Text: code, Font: font.Family, Size: font.Size}}
	lang := codeeditor.LanguageNamed(language)
	if lang == nil || lang.Tokenizer == nil || code == "" {
		return plain
	}
	ct := CodeTheme(pal)
	var out []ui.Span
	add := func(text string, k codeeditor.TokenKind, colored bool) {
		if text == "" {
			return
		}
		s := ui.Span{Text: text, Font: font.Family, Size: font.Size}
		if colored {
			st := ct.Tokens[k]
			s.Color, s.Italic = ct.TokenColor(k), st.Italic
			if st.Bold {
				s.Weight = 700
			}
		}
		out = append(out, s)
	}
	var state uint64
	var tokens []codeeditor.Token
	lines := strings.Split(code, "\n")
	for i, line := range lines {
		tokens, state = lang.Tokenizer.Tokenize(line, state, tokens[:0])
		at := 0
		for _, t := range tokens {
			end := min(max(t.End, at), len(line))
			add(line[at:end], t.Kind, true)
			at = end
		}
		add(line[at:], 0, false)
		if i < len(lines)-1 {
			add("\n", 0, false)
		}
	}
	return out
}

var codeThemes = map[*theme.Palette]*codeeditor.Theme{}

func CodeTheme(pal *theme.Palette) *codeeditor.Theme {
	if ct, ok := codeThemes[pal]; ok {
		return ct
	}
	ct := codeeditor.DefaultTheme(&ui.Theme{Dark: pal.Dark, Background: pal.Code, Text: pal.Text,
		TextMuted: pal.TextMuted, Selection: pal.Selection, Accent: pal.Accent, Border: pal.Border})
	codeThemes[pal] = ct
	return ct
}

func (e *Editor) codeBarView(c *ui.Context, n *doc.Node, left float32, hovered bool, pal *theme.Palette) {
	ui.Toolbar(c.Key("code-bar"), func() {
		name := codeLanguageName(n.Str("language"))
		if e.ReadOnly {
			ui.Text(c, name).FontSize(12).TextColor(pal.TextMuted).Padding(0, 6)
		} else {
			lang := ui.MenuButton(c.Key("language"), name, func(m *ui.Menu) {
				for _, l := range doc.CodeLanguages {
					if m.Item(codeLanguageName(l.ID)).Radio(l.ID == n.Str("language")).Chosen() {
						e.SetCodeLanguage(n.ID, l.ID)
					}
				}
			}).Label(locales.EditorCodeLanguage(name)).FontSize(12).Padding(4, 6).TextColor(pal.TextMuted)
			if lang.Hovered() {
				lang.TextColor(pal.Text)
			}
		}
		ui.Spacer(c)
		copied := e.copied == n.ID && time.Since(e.copiedAt) < 1500*time.Millisecond
		icon, label := "copy", locales.EditorCopyCode()
		if copied {
			icon, label = "check", locales.CommonCopied()
		}
		b := ui.ButtonBase(c.Key("copy")).Size(26, 24).Center().Radius(6).TextColor(pal.TextMuted).Label(label).Tooltip(label).
			Opacity(map[bool]float32{true: 1, false: 0}[hovered || copied])
		if b.Hovered() {
			b.Background(pal.Hover).TextColor(pal.Text)
		}
		b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(13) })
		if copied {
			wait := 1500*time.Millisecond - time.Since(e.copiedAt)
			b.Draw(func(p *ui.Painter, _ ui.Rect) { p.After(wait) })
		}
		if b.Clicked() {
			e.services.WriteClipboard(n.Delta().Plain())
			e.copied, e.copiedAt = n.ID, time.Now()
		}
	}).Label(locales.HostSlashCode()).Absolute().Top(codeMargin).Left(left + codePad - 6).Right(codePad - 6).Height(codeBar).Padding(0).Gap(2)
}

func (e *Editor) inCodeBar(n *doc.Node, wy float32) bool {
	box, ok := e.box(n.ID)
	return ok && n.Type == "code" && wy >= box.Y && wy < box.Y+codeMargin+codeBar
}

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

func (e *Editor) inCode() bool {
	a, b := e.ordered()
	n := e.node(a.ID)
	return a.ID == b.ID && n != nil && n.Type == "code"
}

func (e *Editor) codeEnter(n *doc.Node) {
	runes := []rune(n.Delta().Plain())
	off := e.focus.Off
	leave, keep, ins := rules.CodeEnter(string(runes), off)
	if leave {
		n.SetDelta(doc.Delta{{Text: string(runes[:keep])}}.Compact())
		e.invalidate(n.ID)
		var p *doc.Node
		if loc := e.Content.Locate(n.ID); loc != nil && loc.Index+1 < len(loc.Siblings) {
			if next := loc.Siblings[loc.Index+1]; next.Type == "paragraph" && textLen(next) == 0 {
				p = next
			}
		}
		if p == nil {
			p = newText("paragraph", nil)
			_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		}
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	}
	n.SetDelta(n.Delta().Insert(off, ins, doc.Attrs{}))
	e.invalidate(n.ID)
	at := off + utf8.RuneCountInString(ins)
	e.Select(Pos{n.ID, at}, Pos{n.ID, at})
	e.changed()
}

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
	e.roomAfterCode(n)
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
	e.changed()
	return true
}

func (e *Editor) roomAfterCode(n *doc.Node) {
	if us := e.units(); len(us) > 0 && us[len(us)-1].ID == n.ID {
		_ = e.Content.Insert(doc.Position{After: n.ID}, newText("paragraph", nil))
	}
}

func (e *Editor) indentCode(n *doc.Node, out bool) {
	code, anchor, focus, ok := rules.IndentCode(n.Delta().Plain(), e.anchor.Off, e.focus.Off, out)
	if !ok {
		return
	}
	e.pushUndo(false)
	e.coalescing = false
	n.SetDelta(doc.Delta{{Text: code}}.Compact())
	e.invalidate(n.ID)
	e.Select(Pos{n.ID, anchor}, Pos{n.ID, focus})
	e.changed()
}

func codeLanguageName(id string) string {
	if id == "" {
		return locales.EditorCodePlainText()
	}
	return doc.CodeLanguageName(id)
}
