package doc

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HTMLOptions are what a page written as HTML gets from the app around it.
type HTMLOptions struct {
	// Lang is the page's language, for the html element; "" leaves it out.
	Lang string
	// Head is HTML of the app's own for the head: styles and meta tags.
	Head string
	// Image returns where a picture kept on this device is served, or ""
	// to leave it out; a picture on the web keeps its own link.
	Image func(n *Node) string
	// Script returns where a script is served: "math" draws formulas,
	// "diagram" draws diagrams and "comments" lets visitors comment. ""
	// leaves it out, and the block shows its source instead.
	Script func(name string) string
	// Embed returns where an embed's HTML is served, to frame in a
	// sandbox; "" or nil shows only its title.
	Embed func(n *Node) string
	// Tail is HTML of the app's own after the page: the comments.
	Tail string
	// PageTitle names a subpage; it may be nil.
	PageTitle func(id string) string
	// Code writes code of a language as HTML, colored; nil writes it
	// plain.
	Code func(code, language string) string
	// CalloutIcon is the icon, as HTML, of a callout's tone; it may be
	// nil.
	CalloutIcon func(tone string) string
	// Icon is the app's icon of a name, as HTML; it may be nil.
	Icon func(name string) string
	// Chart draws a chart's plot and legend as HTML, as the app does;
	// nil, or "", writes its numbers as a table.
	Chart func(n *Node) string
	// Due names a to-do's due date, or "" for none; it may be nil.
	Due func(n *Node) string
	// File returns where a file block's file is served to download, ""
	// for nowhere, its name and what it is: its type and size. It may be
	// nil.
	File func(n *Node) (href, name, detail string)
	// Recording returns what a recording block shows; nil shows only its
	// title.
	Recording func(n *Node) RecordingView
}

// RecordingView is what a recording block shows on a web page: its title,
// how long it is, where its audio is served, "" for nowhere, and its
// transcript.
type RecordingView struct {
	Title, Length, Audio string
	Lines                []TranscriptLine
}

// TranscriptLine is a line of a recording's transcript: where it starts,
// in seconds and as written, who says it, and what.
type TranscriptLine struct {
	Start               float64
	Time, Speaker, Text string
}

// PageHTML writes a whole page as a web page that stands alone: its own
// styles, for light and dark, and no script but the ones opts serves.
// Every block carries its ID, as data-block, for the comments script.
func PageHTML(title string, c *Content, opts HTMLOptions) string {
	var body strings.Builder
	math, diagram, embed, recording := false, false, false, false
	c.Walk(func(n, _ *Node) {
		math = math || n.Type == "math_equation"
		diagram = diagram || n.Type == "diagram"
		embed = embed || n.Type == "embed" && opts.Embed != nil
		recording = recording || n.Type == "recording"
	})
	script := func(name string) string {
		if opts.Script == nil {
			return ""
		}
		return opts.Script(name)
	}
	if !math || script("math") == "" {
		math = false
	}
	if !diagram || script("diagram") == "" {
		diagram = false
	}
	w := htmlWriter{b: &body, opts: opts, math: math, diagram: diagram}
	w.blocks(c.Children)

	var b strings.Builder
	b.WriteString("<!doctype html>\n<html")
	if opts.Lang != "" {
		b.WriteString(` lang="` + html.EscapeString(opts.Lang) + `"`)
	}
	b.WriteString(`><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="robots" content="noindex"><title>` + html.EscapeString(title) + `</title><style>` + pageCSS + `</style>` + opts.Head)
	for _, name := range []string{"math", "diagram", "embed", "recording", "comments"} {
		if name == "math" && !math || name == "diagram" && !diagram || name == "embed" && !embed || name == "recording" && !recording {
			continue
		}
		if src := script(name); src != "" {
			// Diagrams are drawn by a module: Pele is one.
			kind := " defer"
			if name == "diagram" {
				kind = ` type="module"`
			}
			b.WriteString(`<script src="` + html.EscapeString(src) + `"` + kind + `></script>`)
		}
	}
	b.WriteString("</head><body><main>\n<h1 class=\"title\">" + html.EscapeString(title) + "</h1>\n")
	b.WriteString(body.String())
	b.WriteString("</main>" + opts.Tail + "</body></html>\n")
	return b.String()
}

type htmlWriter struct {
	b             *strings.Builder
	opts          HTMLOptions
	math, diagram bool
}

// icon is the app's icon of a name, as HTML, or "".
func (w htmlWriter) icon(name string) string {
	if w.opts.Icon == nil {
		return ""
	}
	return w.opts.Icon(name)
}

func (w htmlWriter) write(s ...string) {
	for _, p := range s {
		w.b.WriteString(p)
	}
}

// attrs are a block's id and data-block, and a class when there is one.
func (w htmlWriter) attrs(n *Node, class string) string {
	id := html.EscapeString(n.ID)
	s := ` id="b-` + id + `" data-block="` + id + `"`
	if class != "" {
		s += ` class="` + class + `"`
	}
	return s
}

// listTag is the element a list block's items go in, or "" for a block
// that is no list item.
func listTag(typ string) string {
	switch typ {
	case "bulleted_list":
		return "ul"
	case "numbered_list":
		return "ol"
	case "todo_list":
		return `ul class="todos"`
	}
	return ""
}

func (w htmlWriter) blocks(nodes []*Node) {
	for i := 0; i < len(nodes); {
		if h := nodes[i]; h.Type == "heading" && h.Bool("collapsed") {
			// A folded heading folds its section away, as in the app: the
			// blocks after it up to the next heading of its level or
			// higher. The visitor opens it.
			end := i + 1
			for end < len(nodes) && (nodes[end].Type != "heading" || nodes[end].Int("level", 1) > h.Int("level", 1)) {
				end++
			}
			w.write(`<details class="fold"><summary>`)
			w.block(h)
			w.write("</summary>\n")
			w.blocks(nodes[i+1 : end])
			w.write("</details>\n")
			i = end
			continue
		}
		typ := nodes[i].Type
		tag := listTag(typ)
		if tag == "" {
			w.block(nodes[i])
			i++
			continue
		}
		// A run of items of one kind is one list.
		w.write("<" + tag + ">\n")
		for ; i < len(nodes) && nodes[i].Type == typ; i++ {
			w.item(nodes[i])
		}
		w.write("</" + strings.Fields(tag)[0] + ">\n")
	}
}

func (w htmlWriter) item(n *Node) {
	w.write("<li", w.attrs(n, ""), ">")
	if n.Type == "todo_list" {
		checked := ""
		if n.Bool("checked") {
			checked = " checked"
		}
		w.write(`<input type="checkbox" disabled` + checked + `> `)
	}
	w.write(n.Delta().HTML())
	if n.Type == "todo_list" && w.opts.Due != nil {
		if due := w.opts.Due(n); due != "" {
			w.write(` <span class="due">`, w.icon("calendar"), html.EscapeString(due), "</span>")
		}
	}
	if len(n.Children) > 0 {
		w.write("\n")
		w.blocks(n.Children)
	}
	w.write("</li>\n")
}

func (w htmlWriter) block(n *Node) {
	switch n.Type {
	case "paragraph":
		w.write("<p", w.attrs(n, ""), ">", n.Delta().HTML(), "</p>\n")
	case "heading":
		h := "h" + strconv.Itoa(min(max(n.Int("level", 1), 1), 6))
		w.write("<", h, w.attrs(n, ""), ">", n.Delta().HTML(), "</", h, ">\n")
	case "quote":
		w.write("<blockquote", w.attrs(n, ""), ">", n.Delta().HTML(), "</blockquote>\n")
	case "callout":
		tone := CalloutTone(n.Str("tone"))
		icon := ""
		if w.opts.CalloutIcon != nil {
			icon = `<span class="icon">` + w.opts.CalloutIcon(tone) + `</span>`
		}
		w.write("<div", w.attrs(n, "callout "+tone), ">", icon, "<div>", n.Delta().HTML(), "</div></div>\n")
	case "code":
		lang := n.Str("language")
		class := ""
		if lang != "" {
			class = ` class="language-` + html.EscapeString(lang) + `"`
		}
		code := html.EscapeString(n.Delta().Plain())
		if w.opts.Code != nil {
			code = w.opts.Code(n.Delta().Plain(), lang)
		}
		w.write("<pre", w.attrs(n, ""), "><code", class, ">", code, "</code></pre>\n")
	case "toggle_list":
		open := " open"
		if n.Bool("collapsed") {
			open = ""
		}
		w.write("<details", w.attrs(n, ""), open, "><summary>", n.Delta().HTML(), "</summary>\n")
		w.blocks(n.Children)
		w.write("</details>\n")
	case "divider":
		w.write("<hr", w.attrs(n, ""), ">\n")
	case "image":
		src := n.Str("url")
		if !WebURL(src) {
			src = ""
			if w.opts.Image != nil {
				src = w.opts.Image(n)
			}
		}
		if src == "" {
			return
		}
		style := ""
		if width := n.Int("width", 0); width > 0 {
			style = fmt.Sprintf(` style="max-width:min(100%%,%dpx)"`, width)
		}
		class := "image"
		if n.Str("align") == "left" {
			class += " left"
		}
		w.write("<figure", w.attrs(n, class), `><img src="`, html.EscapeString(src), `" alt="`, html.EscapeString(n.Str("alt")), `"`, style, ` loading="lazy"></figure>`+"\n")
	case "table":
		w.write("<div", w.attrs(n, "table"), "><table>\n")
		grid := tableCells(n)
		// A column of short labels (a step, a time) keeps each on one line,
		// rather than wrapping so a column of sentences can be wider.
		short := map[int]bool{}
		if len(grid) > 0 {
			for c := range grid[0] {
				short[c] = true
				for _, row := range grid {
					if utf8.RuneCountInString(row[c].Plain()) > shortCell {
						short[c] = false
						break
					}
				}
			}
		}
		for r, row := range grid {
			cell := "td"
			if r == 0 {
				cell = "th"
			}
			w.write("<tr>")
			for c, d := range row {
				open := "<" + cell + ">"
				if short[c] {
					open = "<" + cell + ` class="nowrap">`
				}
				w.write(open, d.HTML(), "</", cell, ">")
			}
			w.write("</tr>\n")
		}
		w.write("</table></div>\n")
	case "chart":
		w.write("<figure", w.attrs(n, "chart"), ">")
		if t := n.Str("title"); t != "" {
			w.write("<figcaption><strong>", html.EscapeString(t), "</strong></figcaption>")
		}
		if w.opts.Chart != nil {
			if plot := w.opts.Chart(n); plot != "" {
				w.write(plot)
				if cap := n.Str("caption"); cap != "" {
					w.write("<figcaption>", html.EscapeString(cap), "</figcaption>")
				}
				w.write("</figure>\n")
				break
			}
		}
		w.write("<div class=\"table\"><table><tr><th></th>")
		for _, l := range stringsOf(n.Data["labels"]) {
			w.write("<th>", html.EscapeString(l), "</th>")
		}
		w.write("</tr>")
		for _, s := range listOf(n.Data["series"]) {
			m := asMap(s)
			w.write("<tr><th>", html.EscapeString(fmt.Sprint(m["name"])), "</th>")
			for _, v := range listOf(m["values"]) {
				f, _ := AsFloat(v)
				w.write("<td>", trimFloat(f), "</td>")
			}
			w.write("</tr>")
		}
		w.write("</table></div>")
		if cap := n.Str("caption"); cap != "" {
			w.write("<figcaption>", html.EscapeString(cap), "</figcaption>")
		}
		w.write("</figure>\n")
	case "embed":
		title := n.Str("title")
		if title == "" {
			title = "Interactive embed"
		}
		src := ""
		if w.opts.Embed != nil {
			src = w.opts.Embed(n)
		}
		if src == "" {
			w.write("<p", w.attrs(n, "muted"), "><em>", html.EscapeString(title), "</em></p>\n")
			break
		}
		// A card as in the app: the title, over the embed in a sandbox of
		// its own, which the embed script grows to fit its content.
		height := min(max(n.Int("height", 320), 120), 1200)
		w.write("<figure", w.attrs(n, "embed"), "><figcaption>", html.EscapeString(title), `</figcaption><iframe sandbox="allow-scripts" src="`,
			html.EscapeString(src), `" title="`, html.EscapeString(title), `" data-height="`, strconv.Itoa(height), `" style="height:`, strconv.Itoa(height),
			`px" loading="lazy"></iframe></figure>`+"\n")
	case "video":
		v, ok := ParseVideoURL(n.Str("url"))
		if !ok {
			w.card(n)
			break
		}
		// It plays in place, as in the app, but only once the visitor
		// asks: the page's player does not start as it loads.
		src := strings.Replace(strings.Replace(v.PlayerURL(), "autoplay=1&", "", 1), "?autoplay=1", "", 1)
		w.write("<figure", w.attrs(n, "video"), ">")
		if v.Provider == "file" {
			w.write(`<video controls preload="metadata" src="`, html.EscapeString(src), `"></video>`)
		} else {
			// The sites' players need to know the page they are on.
			w.write(`<div class="frame"><iframe src="`, html.EscapeString(src), `" title="`, html.EscapeString(cardTitle(n)),
				`" allow="fullscreen; picture-in-picture; encrypted-media" allowfullscreen loading="lazy" referrerpolicy="strict-origin-when-cross-origin"></iframe></div>`)
		}
		if t := n.Str("title"); t != "" {
			w.write(`<figcaption><a href="`, html.EscapeString(v.WatchURL()), `" rel="noopener noreferrer">`, html.EscapeString(t), "</a></figcaption>")
		}
		w.write("</figure>\n")
	case "link_preview":
		w.card(n)
	case "file":
		w.file(n)
	case "recording":
		w.recording(n)
	case "subpage":
		// Only this page is shared: its subpages are names, not links.
		name := n.Str("page_id")
		if w.opts.PageTitle != nil {
			name = w.opts.PageTitle(name)
		}
		w.write("<p", w.attrs(n, "card"), ">", html.EscapeString(name), "</p>\n")
	case "math_equation":
		f := html.EscapeString(n.Str("formula"))
		if w.math {
			w.write("<div", w.attrs(n, "math"), ` data-tex="`, f, `"><pre>`, f, "</pre></div>\n")
		} else {
			w.write("<pre", w.attrs(n, ""), "><code>", f, "</code></pre>\n")
		}
	case "diagram":
		src := html.EscapeString(n.Str("source"))
		if w.diagram {
			w.write("<div", w.attrs(n, "diagram"), ` data-source="`, src, `"><pre>`, src, "</pre></div>\n")
		} else {
			w.write("<pre", w.attrs(n, ""), "><code>", src, "</code></pre>\n")
		}
	default:
		if n.IsText() {
			w.write("<p", w.attrs(n, ""), ">", n.Delta().HTML(), "</p>\n")
		}
	}
}

// card writes a link card as the app shows it: its title, description
// and site, beside its picture, all a link.
func (w htmlWriter) card(n *Node) {
	link := n.Str("url")
	title := cardTitle(n)
	if !WebURL(link) {
		w.write("<p", w.attrs(n, ""), ">", html.EscapeString(title), "</p>\n")
		return
	}
	site := n.Str("site")
	if u, err := url.Parse(link); err == nil && site == "" {
		site = strings.TrimPrefix(u.Hostname(), "www.")
	}
	w.write("<a", w.attrs(n, "card link"), ` href="`, html.EscapeString(link), `" rel="noopener noreferrer"><span class="text"><strong>`, html.EscapeString(title), "</strong>")
	if d := n.Str("description"); d != "" {
		w.write(`<span class="desc">`, html.EscapeString(d), "</span>")
	}
	w.write(`<span class="site">`, w.icon("globe"), html.EscapeString(site), "</span></span>")
	if img := n.Str("image"); WebURL(img) {
		w.write(`<img src="`, html.EscapeString(img), `" alt="" loading="lazy">`)
	}
	w.write("</a>\n")
}

// file writes a file's card: its name and what it is, a link that
// downloads it where it is served.
func (w htmlWriter) file(n *Node) {
	name, href, detail := cardTitle(n), "", ""
	if w.opts.File != nil {
		var named string
		href, named, detail = w.opts.File(n)
		if named != "" {
			name = named
		}
	}
	tag := "div"
	if href != "" {
		tag = "a"
	}
	w.write("<", tag, w.attrs(n, "card file"))
	if href != "" {
		w.write(` href="`, html.EscapeString(href), `" download="`, html.EscapeString(name), `"`)
	}
	w.write(`><span class="ficon">`, w.icon("paperclip"), `</span><span class="text"><strong>`, html.EscapeString(name), "</strong>")
	if detail != "" {
		w.write(`<span class="muted">`, html.EscapeString(detail), "</span>")
	}
	w.write("</span></", tag, ">\n")
}

// recording writes a recording as the app shows it: its title and length,
// a player, and its transcript, whose lines play from where they start.
func (w htmlWriter) recording(n *Node) {
	v := RecordingView{Title: n.Str("title")}
	if w.opts.Recording != nil {
		v = w.opts.Recording(n)
	}
	w.write("<figure", w.attrs(n, "recording"), `><div class="head"><span class="ricon">`, w.icon("mic"), `</span><span class="text"><strong>`, html.EscapeString(v.Title), "</strong>")
	if v.Length != "" {
		w.write(`<span class="muted">`, html.EscapeString(v.Length), "</span>")
	}
	w.write("</span></div>")
	if v.Audio != "" {
		w.write(`<audio controls preload="metadata" src="`, html.EscapeString(v.Audio), `"></audio>`)
	}
	if len(v.Lines) > 0 {
		w.write(`<div class="transcript">`)
		for _, l := range v.Lines {
			if v.Audio != "" {
				w.write(`<p data-start="`, strconv.FormatFloat(l.Start, 'f', -1, 64), `">`)
			} else {
				w.write("<p>")
			}
			w.write(`<span class="ts">`, html.EscapeString(l.Time), `</span><span class="line">`)
			if l.Speaker != "" {
				w.write(`<span class="speaker">`, html.EscapeString(l.Speaker), "</span>")
			}
			w.write(html.EscapeString(l.Text), "</span></p>")
		}
		w.write("</div>")
	}
	w.write("</figure>\n")
}

// shortCell is the most characters a cell holds for its column to count as
// short labels that stay on one line.
const shortCell = 20

// tableCells are a stored table's cells, row by row, as rich text.
func tableCells(n *Node) [][]Delta {
	rows, cols := TableSize(n)
	grid := make([][]Delta, rows)
	for i := range grid {
		grid[i] = make([]Delta, cols)
	}
	for _, cell := range n.Children {
		c, r := cell.Int("colPosition", -1), cell.Int("rowPosition", -1)
		if c < 0 || r < 0 || r >= rows || c >= cols {
			continue
		}
		for i, k := range cell.Children {
			if i > 0 {
				grid[r][c] = append(grid[r][c], Run{Text: " "})
			}
			grid[r][c] = append(grid[r][c], k.Delta()...)
		}
	}
	return grid
}

// HTML writes rich text as inline HTML. A link keeps only a web or mail
// address, and a mention is its name.
func (d Delta) HTML() string {
	var b strings.Builder
	for _, r := range d {
		text := strings.ReplaceAll(html.EscapeString(r.Text), "\n", "<br>")
		a := r.Attrs
		if a.Mention != nil {
			text = "@" + html.EscapeString(a.Mention.Name)
		}
		var open, close []string
		wrap := func(o, c string) {
			open = append(open, o)
			close = append([]string{c}, close...)
		}
		if href := safeHref(a.Href); href != "" {
			wrap(`<a href="`+html.EscapeString(href)+`" rel="noopener noreferrer">`, "</a>")
		}
		if a.Bold {
			wrap("<strong>", "</strong>")
		}
		if a.Italic {
			wrap("<em>", "</em>")
		}
		if a.Under {
			wrap("<u>", "</u>")
		}
		if a.Strike {
			wrap("<s>", "</s>")
		}
		if a.Code {
			wrap("<code>", "</code>")
		}
		var style []string
		if ValidColor(a.Color) {
			style = append(style, "color:"+a.Color)
		}
		if ValidColor(a.Background) {
			style = append(style, "background:"+a.Background)
		}
		if len(style) > 0 {
			wrap(`<span style="`+strings.Join(style, ";")+`">`, "</span>")
		}
		b.WriteString(strings.Join(open, "") + text + strings.Join(close, ""))
	}
	return b.String()
}

// safeHref returns a link a visitor may follow: http, https or mailto,
// else "".
func safeHref(s string) string {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return s
	}
	return ""
}

const pageCSS = `:root{color-scheme:light dark;--fg:#16181d;--muted:#565c67;--bg:#fff;--line:#e3e6eb;--code:#f3f4f6;--link:#3446c9;
--info:#eef1ff;--tip:#e9f7ef;--warning:#fff6e0;--danger:#fdecec;--th:#f5f6f8;
--info-line:transparent;--tip-line:transparent;--warning-line:transparent;--danger-line:transparent;
--info-icon:currentColor;--tip-icon:currentColor;--warning-icon:currentColor;--danger-icon:currentColor;
--fg2:#3d424c;--accent:#3446c9;--chart-muted:#9aa1ad;--hover:rgba(22,24,29,.05);--due:#f0f1f4}
@media (prefers-color-scheme:dark){:root{--fg:#e9ebef;--muted:#9ea4af;--bg:#16181d;--line:#2f343c;--code:#23272e;--link:#8a97ff;
--info:#22264a;--tip:#1c3326;--warning:#3a2f14;--danger:#3d1d1d;--th:#101216;
--fg2:#c0c5ce;--accent:#4f5fd9;--chart-muted:#6b7280;--hover:rgba(233,235,239,.06);--due:#23272e}}
html{background:var(--bg);color:var(--fg)}
body{margin:0;font:17px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;-webkit-text-size-adjust:100%}
main{max-width:720px;margin:0 auto;padding:48px 16px 120px;overflow-wrap:anywhere}
h1.title{font-size:2.2em;line-height:1.2;margin:0 0 .8em}
h1,h2,h3,h4,h5,h6{line-height:1.3;margin:1.4em 0 .4em}
p,ul,ol,blockquote,pre,figure,details,.callout,.table{margin:.5em 0}
a{color:var(--link)}
code{font:.88em ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--code);padding:.1em .3em;border-radius:4px}
pre{background:var(--code);padding:12px 14px;border-radius:8px;overflow-x:auto}
pre code{background:none;padding:0}
blockquote{border-left:3px solid var(--line);padding-left:14px;color:var(--muted)}
hr{border:0;border-top:1px solid var(--line);margin:1.5em 0}
ul.todos{list-style:none;padding-left:4px}
ul.todos>li{padding-left:1.5em;text-indent:-1.5em}
ul.todos>li>*{text-indent:0}
ul.todos ul.todos{padding-left:24px}
.callout{display:flex;gap:10px;align-items:flex-start;padding:12px 14px;border-radius:8px;
background:var(--info);border:1px solid var(--info-line);--icon:var(--info-icon)}
.callout.tip{background:var(--tip);border-color:var(--tip-line);--icon:var(--tip-icon)}
.callout.warning{background:var(--warning);border-color:var(--warning-line);--icon:var(--warning-icon)}
.callout.danger{background:var(--danger);border-color:var(--danger-line);--icon:var(--danger-icon)}
.callout .icon{flex:none;display:flex;height:1.65em;align-items:center;color:var(--icon)}
.callout .icon svg{width:18px;height:18px}
.callout>div{min-width:0}
figure{margin:1em 0}
figure.image{text-align:center}figure.image.left{text-align:left}
figure.image img{max-width:100%;height:auto;border-radius:6px}
figcaption{color:var(--muted);font-size:.9em;margin:.3em 0}
.table{overflow-x:auto}
table{border-collapse:collapse;font-size:.95em;overflow-wrap:break-word}
table a,table code{overflow-wrap:anywhere}
.nowrap{white-space:nowrap}
th,td{border:1px solid var(--line);padding:6px 10px;text-align:left;vertical-align:top}
th{background:var(--th);font-weight:600}
.muted{color:var(--muted)}
.card{border:1px solid var(--line);border-radius:8px;padding:10px 14px}
a.card,div.card{display:flex;align-items:center;gap:12px;margin:.5em 0;color:inherit;text-decoration:none;overflow:hidden}
a.card:hover{background:var(--hover)}
.card .text{display:flex;flex-direction:column;gap:2px;min-width:0;flex:1}
.card strong{font-size:15px;font-weight:600}
.card .text>span{font-size:13px;line-height:1.45}
.card.link{padding:0 0 0 14px;align-items:stretch}
.card.link .text{padding:12px 14px 12px 0}
.card.link .desc{color:var(--muted);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.card.link .site,.due{display:inline-flex;align-items:center;gap:5px;color:var(--muted);font-size:12px}
.card svg,.due svg,.recording .ricon svg{width:12px;height:12px;flex:none}
.card.link img{flex:none;width:150px;min-height:96px;object-fit:cover;align-self:stretch}
.card .ficon{flex:none;display:flex;width:36px;height:36px;border-radius:8px;background:var(--code);align-items:center;justify-content:center}
.card .ficon svg{width:17px;height:17px}
.due{margin-left:.3em;padding:0 7px;line-height:20px;border-radius:6px;background:var(--due);font-weight:550;text-indent:0;vertical-align:.1em}
details.fold>summary{list-style:none;cursor:pointer;display:flex;align-items:baseline}
details.fold>summary::-webkit-details-marker{display:none}
details.fold>summary::before{content:"";flex:none;width:.38em;height:.38em;margin:0 .45em 0 -.05em;border:solid var(--muted);border-width:0 2px 2px 0;transform:translateY(-.15em) rotate(-45deg)}
details.fold[open]>summary::before{transform:translateY(-.25em) rotate(45deg)}
figure.chart{border:1px solid var(--line);border-radius:10px;padding:16px 18px}
figure.chart>figcaption:first-child{margin:0 0 10px;color:var(--fg);font-size:15px}
figure.chart>figcaption:first-child strong{font-weight:650}
figure.chart>figcaption:last-child:not(:first-child){font-size:12px;margin:10px 0 0}
.plot{position:relative;height:220px;box-sizing:border-box;padding:18px 0 24px;font-size:12px;line-height:1;color:var(--muted)}
.plot.line{padding:8px 0 24px}
.plot .area{position:relative;height:100%}
.plot .area>*{position:absolute;white-space:nowrap}
.plot .g{left:0;right:0;height:1px;background:var(--line)}
.plot .b{border-radius:4px}
.plot .v{transform:translate(-50%,-3px);color:var(--fg2)}
.plot .x{top:calc(100% + 8px);transform:translateX(-50%)}
.plot .y{right:calc(100% + 8px);transform:translateY(50%)}
.plot svg{inset:0;width:100%;height:100%;overflow:visible}
.plot .d{width:7px;height:7px;border-radius:50%;transform:translate(-50%,50%)}
svg.pie{display:block;height:200px;width:100%}
.legend{display:flex;flex-wrap:wrap;gap:4px 14px;margin-top:10px;font-size:12px;color:var(--muted)}
.legend span{display:inline-flex;align-items:center;gap:6px}
.legend i{width:9px;height:9px;border-radius:2px}
figure.recording{border:1px solid var(--line);border-radius:10px;padding:14px 16px;display:flex;flex-direction:column;gap:12px}
.recording .head{display:flex;align-items:center;gap:10px}
.recording .head .text{display:flex;flex-direction:column}
.recording .head strong{font-size:14px;font-weight:600}
.recording .head .muted{font-size:12px}
.recording .ricon{flex:none;display:flex;width:30px;height:30px;border-radius:50%;background:var(--info);color:var(--link);align-items:center;justify-content:center}
.recording .ricon svg{width:15px;height:15px}
.recording audio{width:100%}
.transcript{display:flex;flex-direction:column;gap:8px}
.transcript p{display:flex;gap:12px;align-items:baseline;margin:0;font-size:14px;line-height:1.5}
.transcript p[data-start]{cursor:pointer}
.transcript .ts{flex:none;width:46px;font-size:12px;color:var(--link);font-variant-numeric:tabular-nums}
.transcript .line{flex:1;min-width:0;padding:0 6px;margin:0 -6px;border-radius:6px}
.transcript p[data-start]:hover .line{background:var(--hover)}
.transcript .speaker{display:block;font-size:11.5px;font-weight:600;color:var(--muted)}
figure.embed{border:1px solid var(--line);border-radius:10px;padding:12px 14px 14px}
figure.embed figcaption{margin:0 0 10px;color:var(--fg);font-weight:650;font-size:15px}
figure.embed iframe{display:block;width:100%;border:0}
figure.video video{width:100%;border-radius:8px;background:#000}
figure.video .frame{position:relative;aspect-ratio:16/9;border-radius:8px;overflow:hidden;background:#000}
figure.video iframe{position:absolute;inset:0;width:100%;height:100%;border:0}
.math,.diagram{text-align:center;overflow-x:auto}
.diagram svg{max-width:100%;height:auto}
@media (max-width:600px){main{padding:28px 16px 96px}h1.title{font-size:1.8em}pre{font-size:.92em}
th:not(.nowrap),td:not(.nowrap){min-width:9em}.card.link img{width:96px}}
.pele{--pele-bg:var(--bg);--pele-fg:#16181d;--pele-muted:#565c67;--pele-line:#3d424c;--pele-surface:#f5f6f8;--pele-surface-alt:#eceef2;--pele-border:#c9ced8;--pele-accent:#3446c9;--pele-fg-on-light:#16181d;--pele-fg-on-dark:#e9ebef;--pele-font:-apple-system,system-ui,sans-serif}
@media (prefers-color-scheme:dark){.pele{--pele-fg:#e9ebef;--pele-muted:#9ea4af;--pele-line:#c0c5ce;--pele-surface:#23272e;--pele-surface-alt:#2b3038;--pele-border:#3a404a;--pele-accent:#8a97ff}}`
