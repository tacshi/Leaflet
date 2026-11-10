package doc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"leaflet/locales"
)

type MarkdownOptions struct {
	Lang      string
	PageTitle func(id string) string
	Link      func(href string) string
}

const DefaultEmbedTitle = "Interactive embed"

func PageMarkdown(title string, c *Content, o MarkdownOptions) string {
	msgs := locales.In(o.Lang)
	href := func(s string) string { return s }
	if o.Link != nil {
		href = o.Link
		c = withLinks(c, o.Link)
	}
	var b strings.Builder
	if title != "" {
		b.WriteString("# " + headingMarkdown(escapeCommonMark(title)) + "\n\n")
	}
	text := func(indent, lead, pad, md string) { b.WriteString(TextMarkdown(indent, lead, pad, md)) }
	block := func(indent, md string) {
		if md == "" {
			return
		}
		for _, l := range strings.Split(md, "\n") {
			if l != "" {
				l = indent + l
			}
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	link := func(text, to string) string {
		return "[" + escapeLinkText(text) + "](" + commonMarkDestination(href(to)) + ")"
	}
	var visit func(nodes []*Node, indent string)
	visit = func(nodes []*Node, indent string) {
		number := 0
		for _, n := range nodes {
			marker := 2
			md := n.Delta().ReadableMarkdown()
			if n.Type != "numbered_list" {
				number = 0
			}
			if n.Type == "paragraph" && len(n.Children) == 0 && strings.TrimSpace(TextMarkdown("", "", "", md)) == "" {
				continue
			}
			item := NestableTypes[n.Type] && n.Type != "toggle_list"
			if out := b.String(); !item && strings.HasSuffix(out, "\n") && !strings.HasSuffix(out, "\n\n") {
				b.WriteString("\n")
			}
			switch n.Type {
			case "paragraph":
				text(indent, "", "", md)
				b.WriteString("\n")
			case "heading":
				hashes := strings.Repeat("#", min(max(n.Int("level", 1), 1), 6))
				block(indent, hashes+" "+headingMarkdown(OneLine(n.Delta()).ReadableMarkdown()))
			case "bulleted_list":
				text(indent, "- ", "  ", md)
			case "numbered_list":
				number++
				lead := strconv.Itoa(number) + ". "
				marker = len(lead)
				text(indent, lead, strings.Repeat(" ", marker), md)
			case "todo_list":
				box := "[ ]"
				if n.Bool("checked") {
					box = "[x]"
				}
				text(indent, "- "+box+" ", "  ", md)
			case "quote":
				text(indent, "> ", "> ", md)
				b.WriteString("\n")
			case "code":
				block(indent, CodeMarkdown(n))
			case "callout":
				block(indent, CalloutMarkdown(n))
			case "toggle_list":
				details := "<details>"
				if !n.Bool("collapsed") {
					details = "<details open>"
				}
				block(indent, details+"\n<summary>"+summaryMarkdown(md)+"</summary>")
				visit(n.Children, indent)
				if out := b.String(); !strings.HasSuffix(out, "\n\n") {
					b.WriteString("\n")
				}
				block(indent, "</details>")
				continue
			case "divider":
				block(indent, "---")
			case "image":
				block(indent, "!"+link(n.Str("alt"), n.Str("url")))
			case "table":
				block(indent, tableMarkdown(ReadableTableRows(n)))
			case "chart":
				if t := n.Str("title"); t != "" {
					block(indent, "**"+escapeCommonMark(t)+"**")
				}
				chart := ChartOf(n)
				head := []string{""}
				for _, l := range chart.Labels {
					head = append(head, escapeCommonMark(l))
				}
				rows := [][]string{head}
				for _, s := range chart.Series {
					row := []string{escapeCommonMark(s.Name)}
					for _, v := range s.Values {
						row = append(row, trimFloat(v))
					}
					rows = append(rows, row)
				}
				block(indent, tableMarkdown(rows))
				if cap := n.Str("caption"); cap != "" {
					block(indent, "*"+escapeCommonMark(cap)+"*")
				}
			case "embed":
				if title := n.Str("title"); title == "" || defaultEmbedTitle(title) {
					block(indent, "*"+escapeCommonMark(msgs.ExportEmbed())+"*")
				} else {
					block(indent, "*"+escapeCommonMark(msgs.ExportEmbedTitled(title))+"*")
				}
			case "video":
				title := n.Str("title")
				if title == "" {
					title = msgs.ExportVideo()
				}
				block(indent, link(title, n.Str("url")))
				if v, ok := ParseVideoURL(n.Str("url")); ok {
					block(indent, notesMarkdown(n.Str("notes"), func(at int) string { return v.At(at).WatchURL() }))
				}
			case "math_equation":
				block(indent, mathMarkdown(n.Str("formula")))
			case "diagram":
				block(indent, CodeMarkdown(&Node{Type: "code", Data: map[string]any{"language": "mermaid", "delta": Delta{{Text: n.Str("source")}}.JSON()}}))
			case "link_preview":
				block(indent, link(cardTitle(n), n.Str("url")))
			case "file":
				block(indent, link(cardTitle(n), n.Str("url")))
				to := href(n.Str("url"))
				block(indent, notesMarkdown(ShownNotes(n), func(at int) string {
					if to == "" || strings.HasPrefix(to, "file:") {
						return ""
					}
					return to + "#t=" + strconv.Itoa(at)
				}))
			case "recording":
				name := msgs.CommonRecording()
				if title := n.Str("title"); title != "" {
					name = msgs.ExportRecordingTitled(title)
				}
				block(indent, "*"+escapeCommonMark(name)+"*")
			case "subpage":
				name := n.Str("page_id")
				if o.PageTitle != nil {
					name = o.PageTitle(name)
				}
				block(indent, link(name, PageLink(n.Str("page_id"))))
			}
			if n.Type != "table" && len(n.Children) > 0 {
				visit(n.Children, indent+strings.Repeat(" ", marker))
			}
		}
	}
	visit(c.Children, "")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func mathMarkdown(formula string) string {
	for l := range strings.Lines(formula) {
		if strings.TrimSpace(l) == "$$" {
			return CodeMarkdown(&Node{Type: "code", Data: map[string]any{"language": "latex", "delta": Delta{{Text: formula}}.JSON()}})
		}
	}
	return "$$\n" + formula + "\n$$"
}

func notesMarkdown(notes string, to func(at int) string) string {
	time := func(text string, at int) string {
		if dest := to(at); dest != "" {
			return "[" + escapeLinkText(text) + "](" + commonMarkDestination(dest) + ")"
		}
		return escapeCommonMark(text)
	}
	var lines []string
	for _, l := range NoteLines(notes) {
		var b strings.Builder
		for _, p := range l.Parts {
			if p.At >= 0 {
				b.WriteString(time(p.Text, p.At))
			} else {
				b.WriteString(escapeCommonMark(p.Text))
			}
		}
		text := b.String()
		switch {
		case l.At >= 0:
			text = strings.TrimRight(time(Clock(l.At), l.At)+" "+text, " ")
		default:
			if text = EscapeBlockStart(text); IsRule("- " + text) {
				text = `\` + text
			}
		}
		lines = append(lines, "- "+text)
	}
	return strings.Join(lines, "\n")
}

func withLinks(c *Content, link func(string) string) *Content {
	c = c.Copy()
	c.Walk(func(n, _ *Node) {
		d := n.Delta()
		if !slices.ContainsFunc(d, func(r Run) bool { return r.Attrs.Href != "" }) {
			return
		}
		for i := range d {
			if d[i].Attrs.Href != "" {
				d[i].Attrs.Href = link(d[i].Attrs.Href)
			}
		}
		n.SetDelta(d)
	})
	return c
}

func defaultEmbedTitle(title string) bool {
	if strings.EqualFold(title, DefaultEmbedTitle) {
		return true
	}
	for _, lang := range locales.Catalog.Languages() {
		if strings.EqualFold(title, locales.In(lang).DialogEmbedTitle()) {
			return true
		}
	}
	return false
}

func OneLine(d Delta) Delta {
	out := make(Delta, len(d))
	for i, r := range d {
		r.Text = strings.ReplaceAll(lineEnds.Replace(r.Text), "\n", " ")
		out[i] = r
	}
	return out
}

func TextMarkdown(indent, lead, pad, md string) string {
	var b strings.Builder
	lines := markdownLines(md)
	for len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		if i > 0 {
			lead = pad
		}
		if l = strings.TrimLeft(l, " \t"); strings.TrimSpace(l) == "" {
			l = ""
		}
		if l = EscapeBlockStart(l); l != "" && IsRule(lead+l) {
			l = `\` + l
		}
		if i < len(lines)-1 {
			if endsInAddress(strings.TrimRight(l, " ")) {
				l += " "
			}
			b.WriteString(indent + lead + l + "\\\n")
		} else {
			b.WriteString(strings.TrimRight(indent+lead+l, " ") + "\n")
		}
	}
	return b.String()
}

func markdownLines(md string) []string {
	md = lineEnds.Replace(md)
	if !strings.Contains(md, "\n") {
		return []string{md}
	}
	s := []rune(md)
	links := &linkScan{s: s}
	var lines []string
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && isPunct(s[i+1]) {
				b.WriteString(string(s[i : i+2]))
				i += 2
				continue
			}
		case '[':
			if text, _, n, ok := links.link(i); ok {
				parts := markdownLines(string(text))
				b.WriteString("[" + parts[0])
				for _, part := range parts[1:] {
					lines = append(lines, b.String())
					b.Reset()
					b.WriteString(part)
				}
				b.WriteString(string(s[i+1+len(text) : i+n]))
				i += n
				continue
			}
		case '`':
			code, n := codeSpanAt(s[i:])
			switch {
			case n == 0:
			case !strings.Contains(code, "\n"):
				b.WriteString(string(s[i : i+n]))
			default:
				for k, part := range strings.Split(code, "\n") {
					if k > 0 {
						lines = append(lines, b.String())
						b.Reset()
					}
					if part != "" {
						b.WriteString(codeSpan(part))
					}
				}
			}
			if n > 0 {
				i += n
				continue
			}
		case '\n':
			lines = append(lines, b.String())
			b.Reset()
			i++
			continue
		}
		b.WriteRune(s[i])
		i++
	}
	return append(lines, b.String())
}

func summaryMarkdown(md string) string {
	lines := markdownLines(md)
	for i, l := range lines {
		if i > 0 {
			l = strings.TrimLeft(l, " \t")
		}
		s := []rune(l)
		var b strings.Builder
		for j := 0; j < len(s); {
			switch s[j] {
			case '\\':
				if j+1 < len(s) && isPunct(s[j+1]) {
					b.WriteString(string(s[j : j+2]))
					j += 2
					continue
				}
			case '`':
				if _, n := codeSpanAt(s[j:]); n > 0 {
					b.WriteString(string(s[j : j+n]))
					j += n
					continue
				}
			case '<':
				if _, ok := cutFold(tagText(s, j), "</summary>"); ok {
					b.WriteByte('\\')
				}
			}
			b.WriteRune(s[j])
			j++
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

func tableMarkdown(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	line := func(r []string) string {
		cells := make([]string, cols)
		for i := range cells {
			if i < len(r) {
				cells[i] = cellMarkdown(strings.TrimSpace(r[i]))
			}
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	lines := []string{line(rows[0]), "|" + strings.Repeat(" --- |", cols)}
	for _, r := range rows[1:] {
		lines = append(lines, line(r))
	}
	return strings.Join(lines, "\n")
}

func cellMarkdown(md string) string {
	if !strings.ContainsAny(md, "|\n<") {
		return md
	}
	s := []rune(md)
	links := &linkScan{s: s}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\\':
			b.WriteString(strings.ReplaceAll(string(s[i:min(i+2, len(s))]), "|", `\|`))
			i += 2
			continue
		case '[':
			if text, _, n, ok := links.link(i); ok {
				b.WriteString("[" + cellMarkdown(string(text)) + strings.ReplaceAll(string(s[i+1+len(text):i+n]), "|", `\|`))
				i += n
				continue
			}
		case '`':
			if code, n := codeSpanAt(s[i:]); n > 0 {
				span := string(s[i : i+n])
				if strings.Contains(code, "\n") {
					span = codeSpan(strings.ReplaceAll(code, "\n", " "))
				}
				b.WriteString(strings.ReplaceAll(span, "|", `\|`))
				i += n
				continue
			}
		case '|':
			b.WriteString(`\|`)
			i++
			continue
		case '\n':
			b.WriteString("<br>")
			i++
			continue
		case '<':
			if brTagAt(tagText(s, i)) != "" {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(s[i])
		i++
	}
	return b.String()
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprintf("%g", f)
}
