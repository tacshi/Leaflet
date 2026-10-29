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
}

// PageHTML writes a whole page as a web page that stands alone: its own
// styles, for light and dark, and no script but the ones opts serves.
// Every block carries its ID, as data-block, for the comments script.
func PageHTML(title string, c *Content, opts HTMLOptions) string {
	var body strings.Builder
	math, diagram, embed := false, false, false
	c.Walk(func(n, _ *Node) {
		math = math || n.Type == "math_equation"
		diagram = diagram || n.Type == "diagram"
		embed = embed || n.Type == "embed" && opts.Embed != nil
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
	for _, name := range []string{"math", "diagram", "embed", "comments"} {
		if name == "math" && !math || name == "diagram" && !diagram || name == "embed" && !embed {
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
		w.write("<p", w.attrs(n, "card"), ">", html.EscapeString(cardTitle(n)), "</p>\n")
	case "recording":
		w.write("<p", w.attrs(n, "muted"), "><em>", html.EscapeString(n.Str("title")), "</em></p>\n")
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

// card writes a link card: its title, linked, and its description.
func (w htmlWriter) card(n *Node) {
	link := n.Str("url")
	title := cardTitle(n)
	if !WebURL(link) {
		w.write("<p", w.attrs(n, ""), ">", html.EscapeString(title), "</p>\n")
		return
	}
	w.write("<p", w.attrs(n, "card"), `><a href="`, html.EscapeString(link), `" rel="noopener noreferrer">`, html.EscapeString(title), "</a>")
	if d := n.Str("description"); d != "" {
		w.write("<br><span class=\"muted\">", html.EscapeString(d), "</span>")
	}
	w.write("</p>\n")
}

// shortCell is the most characters a cell holds for its column to count as
// short labels that stay on one line.
const shortCell = 20

// tableCells are a stored table's cells, row by row, as rich text.
func tableCells(n *Node) [][]Delta {
	cols, rows := n.Int("colsLen", 0), n.Int("rowsLen", 0)
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
--info-icon:currentColor;--tip-icon:currentColor;--warning-icon:currentColor;--danger-icon:currentColor}
@media (prefers-color-scheme:dark){:root{--fg:#e9ebef;--muted:#9ea4af;--bg:#16181d;--line:#2f343c;--code:#23272e;--link:#8a97ff;
--info:#22264a;--tip:#1c3326;--warning:#3a2f14;--danger:#3d1d1d;--th:#101216}}
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
figure.embed{border:1px solid var(--line);border-radius:10px;padding:12px 14px 14px}
figure.embed figcaption{margin:0 0 10px;color:var(--fg);font-weight:650;font-size:15px}
figure.embed iframe{display:block;width:100%;border:0}
figure.video video{width:100%;border-radius:8px;background:#000}
figure.video .frame{position:relative;aspect-ratio:16/9;border-radius:8px;overflow:hidden;background:#000}
figure.video iframe{position:absolute;inset:0;width:100%;height:100%;border:0}
.math,.diagram{text-align:center;overflow-x:auto}
.diagram svg{max-width:100%;height:auto}
@media (max-width:600px){main{padding:28px 16px 96px}h1.title{font-size:1.8em}pre{font-size:.92em}
th:not(.nowrap),td:not(.nowrap){min-width:9em}}
.pele{--pele-bg:var(--bg);--pele-fg:#16181d;--pele-muted:#565c67;--pele-line:#3d424c;--pele-surface:#f5f6f8;--pele-surface-alt:#eceef2;--pele-border:#c9ced8;--pele-accent:#3446c9;--pele-fg-on-light:#16181d;--pele-fg-on-dark:#e9ebef;--pele-font:-apple-system,system-ui,sans-serif}
@media (prefers-color-scheme:dark){.pele{--pele-fg:#e9ebef;--pele-muted:#9ea4af;--pele-line:#c0c5ce;--pele-surface:#23272e;--pele-surface-alt:#2b3038;--pele-border:#3a404a;--pele-accent:#8a97ff}}`
