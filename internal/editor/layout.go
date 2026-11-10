package editor

import (
	"maps"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/internal/theme"
)

type blockLayout struct {
	kind    string
	rest    map[string]any
	width   float32
	code    string
	pretty  bool
	text    textID
	runs    doc.Delta
	pal     *theme.Palette
	layout  *ui.TextLayout
	x, y, h float32
	font    ui.Font
}

const indentStep = 26

func (e *Editor) blockDepths() map[string]int {
	depths := map[string]int{}
	var deep func([]*doc.Node, int)
	deep = func(nodes []*doc.Node, d int) {
		for _, n := range nodes {
			depths[n.ID] = d
			deep(n.Children, d+1)
		}
	}
	deep(e.Content.Children, 0)
	return depths
}

func (e *Editor) depth(id string) int {
	if e.order != nil {
		if d, ok := e.depths[id]; ok {
			return d
		}
	}
	d := 0
	loc := e.Content.Locate(id)
	for loc != nil && loc.Parent != nil {
		d++
		loc = e.Content.Locate(loc.Parent.ID)
	}
	return d
}

type textID struct {
	ops  *any
	runs *doc.Run
	n    int
}

func textIDOf(n *doc.Node) textID {
	switch s := n.Data["delta"].(type) {
	case []any:
		if len(s) > 0 {
			return textID{ops: &s[0], n: len(s)}
		}
	case doc.Delta:
		if len(s) > 0 {
			return textID{runs: &s[0], n: len(s)}
		}
	}
	return textID{}
}

type look struct {
	text         textID
	fonts        int
	plain        string
	family, code string
	leading      float32
}

func (e *Editor) lookOf(n *doc.Node) *look {
	id, fonts := textIDOf(n), theme.FontsChosen()
	l := e.looks[n.ID]
	if l != nil && l.text == id && l.fonts == fonts {
		return l
	}
	plain := n.Delta().Plain()
	if l != nil && l.plain == plain && l.fonts == fonts {
		l.text = id
		return l
	}
	l = &look{text: id, fonts: fonts, plain: plain, family: theme.PageFamily(plain), code: theme.CodeFamily(plain), leading: leading(plain)}
	if e.looks == nil {
		e.looks = map[string]*look{}
	}
	e.looks[n.ID] = l
	return l
}

func (e *Editor) style(n *doc.Node) (font ui.Font, top, bottom float32) {
	size := e.Size
	lk := e.lookOf(n)
	font = ui.Font{Family: lk.family, Size: size, LineHeight: lk.leading}
	top, bottom = 3, 3
	switch n.Type {
	case "heading":
		font.Weight, font.LineHeight = 650, 0
		switch n.Int("level", 1) {
		case 1:
			font.Size, top, bottom = size*1.75, 22, 6
		case 2:
			font.Size, top, bottom = size*1.4, 18, 4
		case 3:
			font.Size, top, bottom = size*1.18, 14, 2
		default:
			font.Size, top, bottom = size*1.05, 10, 2
		}
	case "quote":
		font.Italic = true
		top, bottom = 6, 6
	case "bulleted_list", "numbered_list", "todo_list":
		top, bottom = 2, 2
	case "code":
		font.Family, font.Size, font.LineHeight = lk.code, size*0.8, 1.5
		top, bottom = codeMargin+codeBar, codePad+codeMargin
	case "callout":
		top, bottom = calloutMargin+calloutPad, calloutPad+calloutMargin
	case "toggle_list":
		top, bottom = 2, 2
	}
	return
}

func leading(text string) float32 {
	if rules.MostlyCJK(text) {
		return 1.8
	}
	return 1.5
}

func markerWidth(n *doc.Node) float32 {
	switch n.Type {
	case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
		return 26
	case "callout":
		return 44
	case "quote":
		return 18
	case "code":
		return codePad
	}
	return 0
}

func (e *Editor) spans(n *doc.Node, pal *theme.Palette, font ui.Font) []ui.Span {
	if n.Type == "code" {
		return codeSpans(n.Delta().Plain(), n.Str("language"), font, pal)
	}
	d := n.Delta()
	muted := n.Type == "todo_list" && n.Bool("checked")
	code := theme.CodeFamily(d.Plain())
	var out []ui.Span
	for _, r := range d {
		s := ui.Span{Text: r.Text}
		if r.Attrs.Bold {
			s.Weight = 700
		}
		if r.Attrs.Italic {
			s.Italic = true
		}
		if r.Attrs.Under {
			s.Underline = true
		}
		if r.Attrs.Strike || muted {
			s.Strikethrough = true
		}
		if muted {
			s.Color = pal.TextMuted
		}
		if r.Attrs.Code {
			s.Font = code
			s.Size = font.Size * 0.86
			s.Background = pal.Code
			s.Radius = 4
		}
		if r.Attrs.Href != "" {
			s.Color = pal.Accent
			s.Underline = true
			s.DecorationColor = pal.AccentSoftBorder
		}
		if r.Attrs.Color != "" {
			s.Color = textColor(r.Attrs.Color, pal)
		}
		if r.Attrs.Background != "" {
			s.Background = hexColor(r.Attrs.Background)
		}
		if r.Attrs.Mention != nil {
			s.Color = pal.AccentText
			s.Weight = 600
			s.Background = pal.AccentSoft
			s.Radius = 4
			s.Underline = false
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		out = []ui.Span{{Text: ""}}
	}
	return out
}

func (e *Editor) layoutOf(n *doc.Node) *blockLayout {
	if n == nil || !n.IsText() {
		return nil
	}
	if l := e.layouts[n.ID]; l != nil && l.text == textIDOf(n) {
		return l
	}
	return nil
}

func (e *Editor) layout(n *doc.Node, pal *theme.Palette) *blockLayout {
	font, top, bottom := e.style(n)
	depth := e.depth(n.ID)
	x := float32(depth)*indentStep + markerWidth(n)
	width := max(e.width-x, 40)
	switch n.Type {
	case "code":
		width = max(width-codePad, 40)
	case "callout":
		width = max(width-calloutPad, 40)
	}
	text, code := textIDOf(n), e.lookOf(n).code
	pretty := e.ReadOnly && n.Type != "code"
	var runs doc.Delta
	if l := e.layouts[n.ID]; l != nil && l.pal == pal && l.kind == n.Type && l.width == width && l.font == font &&
		l.code == code && l.pretty == pretty && l.x == x && l.y == top && sameRest(l.rest, n.Data) {
		if l.text == text {
			return l
		}
		if runs = n.Delta(); sameRuns(l.runs, runs) {
			l.text = text
			return l
		}
	} else {
		runs = n.Delta()
	}
	spans := e.spans(n, pal, font)
	tl := ui.ShapeRichTextWith(spans, font, ui.TextShape{Width: width, Pretty: pretty})
	_, h := tl.Size()
	if h == 0 {
		h = font.Size * max(font.LineHeight, 1.3)
	}
	l := &blockLayout{kind: n.Type, rest: restOf(n.Data), width: width, code: code, text: text, runs: runs, pal: pal,
		layout: tl, x: x, y: top, h: top + h + bottom, font: font, pretty: pretty}
	e.layouts[n.ID] = l
	return l
}

func restOf(data map[string]any) map[string]any {
	var rest map[string]any
	for k, v := range data {
		if k != "delta" {
			if rest == nil {
				rest = make(map[string]any, len(data))
			}
			rest[k] = v
		}
	}
	return cloneData(rest)
}

func sameRest(rest, data map[string]any) bool {
	n := 0
	for k, v := range data {
		if k == "delta" {
			continue
		}
		if w, ok := rest[k]; !ok || !sameValue(v, w) {
			return false
		}
		n++
	}
	return n == len(rest)
}

func sameRuns(a, b doc.Delta) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || !a[i].Attrs.Equal(b[i].Attrs) {
			return false
		}
	}
	return true
}

func utf16Off(n *doc.Node, off int) int {
	units := 0
	i := 0
	for _, r := range n.Delta().Plain() {
		if i >= off {
			break
		}
		units += utf16.RuneLen(r)
		i++
	}
	return units
}

func runeOff(n *doc.Node, units int) int {
	u := 0
	i := 0
	for _, r := range n.Delta().Plain() {
		if u >= units {
			break
		}
		u += utf16.RuneLen(r)
		i++
	}
	return i
}

var clip struct {
	text   string
	blocks []*doc.Node
	delta  doc.Delta
	token  string
}

const clipFormat transfer.Format = "application/x-leaflet-copy"

func (e *Editor) copyBlocks() []*doc.Node {
	if e.Collapsed() && !e.blockSelected() {
		return nil
	}
	a, b := e.ordered()
	if a.ID == b.ID {
		if n := e.node(a.ID); n == nil || n.IsText() {
			return nil
		}
	}
	us := e.selectedUnits(a)
	var out []*doc.Node
	type open struct {
		depth int
		n     *doc.Node
	}
	var stack []open
	depths := e.blockDepths()
	ia, ib := indexIn(us, a.ID), e.selectedEnd(us, b)
	for i := ia; i >= 0 && i <= ib && i < len(us); i++ {
		n := us[i].Clone()
		if n.Type != "table" && (!collapsed(n) || i == ia || i == ib) {
			n.Children = nil
		}
		if n.IsText() {
			from, to := 0, textLen(n)
			if n.ID == a.ID {
				from = a.Off
			}
			if n.ID == b.ID {
				to = b.Off
			}
			n.SetDelta(n.Delta().Slice(from, to))
		}
		depth := depths[n.ID]
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && doc.NestableTypes[stack[len(stack)-1].n.Type] {
			p := stack[len(stack)-1].n
			p.Children = append(p.Children, n)
		} else {
			out = append(out, n)
		}
		stack = append(stack, open{depth, n})
	}
	return out
}

func (e *Editor) tableOf(id string) *doc.Node {
	for loc := e.Content.Locate(id); loc != nil && loc.Parent != nil; loc = e.Content.Locate(loc.Parent.ID) {
		if loc.Parent.Type == "table" {
			return loc.Parent
		}
	}
	return nil
}

func freshBlocks(nodes []*doc.Node) []*doc.Node {
	out := make([]*doc.Node, len(nodes))
	for i, n := range nodes {
		c := n.Clone()
		var renew func(*doc.Node)
		renew = func(n *doc.Node) {
			n.ID = doc.NewBlockID()
			for _, k := range n.Children {
				renew(k)
			}
		}
		renew(c)
		out[i] = c
	}
	return out
}

func (e *Editor) copyText() string { return e.markdownOf(e.copyBlocks()) }

func (e *Editor) markdownOf(nodes []*doc.Node) string {
	if nodes == nil {
		if e.Collapsed() {
			return ""
		}
		a, b := e.ordered()
		return e.node(a.ID).Delta().Slice(a.Off, b.Off).Plain()
	}
	md := doc.PageMarkdown("", &doc.Content{Children: nodes}, markdownOptions(e.Host.PageTitle))
	return strings.TrimRight(md, "\n")
}

func joins(n *doc.Node) bool { return n.IsText() && n.Type != "code" }

var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u2029", "\n", "\u0085", "\n")

func (e *Editor) paste(text string) { e.pasteAs(text, false) }

func (e *Editor) pasteAs(text string, plain bool) {
	if e.ReadOnly || text == "" {
		return
	}
	text = lineBreaks.Replace(text)
	if a, _ := e.ordered(); e.node(a.ID) != nil && e.node(a.ID).Type == "code" {
		e.pushUndo(false)
		e.coalescing = false
		e.deleteSelection()
		n := e.node(e.focus.ID)
		n.SetDelta(doc.Delta{{Text: string([]rune(n.Delta().Plain())[:e.focus.Off]) + text + string([]rune(n.Delta().Plain())[e.focus.Off:])}})
		e.invalidate(n.ID)
		off := e.focus.Off + utf8.RuneCountInString(text)
		e.Select(Pos{n.ID, off}, Pos{n.ID, off})
		e.changed()
		return
	}
	if n := e.node(e.focus.ID); n != nil && n.Type == "paragraph" && textLen(n) == 0 && e.Collapsed() {
		if v, err := doc.VideoNode(text, ""); err == nil {
			e.InsertBlocks(v)
			return
		}
		if doc.ImageLink(text) {
			img, _ := doc.ImageNode(text)
			e.InsertBlocks(img)
			return
		}
		if link := strings.TrimSpace(text); doc.ShortVideoLink(link) {
			id := n.ID
			e.insertInline(text, link)
			e.Host.FollowVideoLink(link, func(video string) { e.shareToVideo(id, link, video) })
			return
		}
	}
	var blocks []*doc.Node
	if clip.blocks != nil && text == clip.text {
		blocks = freshBlocks(clip.blocks)
		if plain {
			plainText(blocks)
		}
	} else if clip.delta != nil && text == clip.text && !plain {
		blocks = []*doc.Node{newText("paragraph", clip.delta)}
	}
	if blocks == nil && !strings.Contains(text, "\n") {
		if a, b := e.ordered(); doc.WebURL(text) && a.ID == b.ID && a.Off < b.Off && e.node(a.ID).IsText() {
			e.SetLink(strings.TrimSpace(text))
			return
		}
		href := ""
		if doc.WebURL(text) {
			href = strings.TrimSpace(text)
		}
		e.insertInline(text, href)
		return
	}
	if blocks == nil {
		blocks = doc.ParseLines(strings.TrimRight(text, "\n"))
	}
	e.pasteBlocks(blocks)
}

func plainText(blocks []*doc.Node) {
	for _, b := range blocks {
		if b.IsText() {
			b.SetDelta(doc.Delta{{Text: b.Delta().Plain()}})
		}
		plainText(b.Children)
	}
}

func (e *Editor) insertInline(text, href string) {
	if e.ReadOnly || text == "" {
		return
	}
	e.pushUndo(false)
	e.coalescing = false
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if !n.IsText() {
		p := newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		n = p
		e.focus = Pos{p.ID, 0}
	}
	attrs := doc.Attrs{}
	if n.Type != "code" {
		attrs = n.Delta().AttrsAt(e.focus.Off)
		attrs.Href = href
	}
	n.SetDelta(n.Delta().Insert(e.focus.Off, text, attrs))
	e.invalidate(n.ID)
	off := e.focus.Off + utf8.RuneCountInString(text)
	e.Select(Pos{n.ID, off}, Pos{n.ID, off})
	e.changed()
}

func (e *Editor) pasteBlocks(blocks []*doc.Node) {
	if len(blocks) == 0 {
		return
	}
	e.pushUndo(false)
	e.coalescing = false
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		e.insertBlocks(blocks...)
		return
	}
	d := n.Delta()
	head, tail := d.Slice(0, e.focus.Off), d.Slice(e.focus.Off, d.Len())
	first := blocks[0]
	if !joins(first) && head.Len() == 0 {
		_ = e.Content.Insert(doc.Position{Before: n.ID}, blocks...)
		e.keepShown(first.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	}
	typ, data := n.Type, maps.Clone(n.Data)
	kids := n.Children
	n.Children = nil
	var last *doc.Node
	at := n.ID
	if !joins(first) {
		n.SetDelta(head)
		_ = e.Content.Insert(doc.Position{After: at}, blocks...)
		at = blocks[len(blocks)-1].ID
		last = newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: at}, last)
	} else {
		n.SetDelta(head.Concat(first.Delta()))
		if head.Len() == 0 && first.Type != "paragraph" {
			n.Type = first.Type
			for _, k := range doc.TypedKeys {
				delete(n.Data, k)
			}
			for k, v := range first.Data {
				if k != "delta" {
					n.Data[k] = v
				}
			}
		}
		last = n
		if len(first.Children) > 0 {
			if nestable(n) {
				n.Children = first.Children
			} else {
				_ = e.Content.Insert(doc.Position{After: at}, first.Children...)
				at = first.Children[len(first.Children)-1].ID
			}
			if !collapsed(n) {
				last = first.Children[len(first.Children)-1]
			}
		}
		if rest := blocks[1:]; len(rest) > 0 {
			_ = e.Content.Insert(doc.Position{After: at}, rest...)
			last = rest[len(rest)-1]
			at = last.ID
		}
		for len(last.Children) > 0 && last.Type != "table" && !collapsed(last) {
			last = last.Children[len(last.Children)-1]
		}
		if !joins(last) || collapsed(last) && len(last.Children) > 0 {
			last = newText("paragraph", nil)
			_ = e.Content.Insert(doc.Position{After: at}, last)
		}
	}
	if last != n {
		if tail.Len() > 0 && typ != "code" {
			last.Type = typ
			for _, k := range doc.TypedKeys {
				delete(last.Data, k)
			}
			for k, v := range data {
				if k != "delta" && k != "due" {
					last.Set(k, v)
				}
			}
		}
		if foldedHeading(n) {
			n.Set("collapsed", false)
		}
	}
	off := textLen(last)
	last.SetDelta(last.Delta().Concat(tail))
	if nestable(last) {
		last.Children = append(last.Children, kids...)
	} else {
		_ = e.Content.Insert(doc.Position{After: last.ID}, kids...)
	}
	for _, id := range []string{n.ID, first.ID, last.ID} {
		e.keepShown(id)
	}
	e.Select(Pos{last.ID, off}, Pos{last.ID, off})
	e.changed()
}
