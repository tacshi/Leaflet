package doc

import (
	"fmt"
	"strconv"
	"strings"
)

// PageMarkdown writes a whole page as CommonMark with GitHub's tables and
// task lists. pageTitle resolves subpage links; it may be nil.
func PageMarkdown(title string, c *Content, pageTitle func(id string) string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString("# " + escapeText(title) + "\n\n")
	}
	// text writes rich text at an indent a line at a time, the first line
	// after lead, a list item's marker, and the others after pad, the room
	// it takes, so they stay in the block. A line that would start another
	// block, as a line break then "- x" would a list, is escaped.
	text := func(indent, lead, pad, md string) {
		for i, l := range strings.Split(md, "\n") {
			if i > 0 {
				lead = pad
			}
			b.WriteString(strings.TrimRight(indent+lead+EscapeBlockStart(l), " ") + "\n")
		}
	}
	// block writes Markdown that is already a block at an indent, every
	// line of it, so a block nested under a list item stays in the item,
	// and a blank line after it.
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
	link := func(text, href string) string { return "[" + escapeText(text) + "](" + LinkDestination(href) + ")" }
	// indent is the room before a block, nested under list items: as much
	// as each item's marker takes, as CommonMark nests by ("1. " three).
	var visit func(nodes []*Node, indent string)
	visit = func(nodes []*Node, indent string) {
		number := 0
		for _, n := range nodes {
			marker := 2 // "- ", and a task's "- " before its box
			md := n.Delta().ReadableMarkdown()
			if n.Type != "numbered_list" {
				number = 0
			}
			// A toggle is a <details>, not a list item. A block other than
			// an item starts after a blank line, at any depth: right after
			// an item, it would read as more of the item's text.
			item := NestableTypes[n.Type] && n.Type != "toggle_list"
			if out := b.String(); !item && strings.HasSuffix(out, "\n") && !strings.HasSuffix(out, "\n\n") {
				b.WriteString("\n")
			}
			switch n.Type {
			case "paragraph":
				text(indent, "", "", md)
				b.WriteString("\n")
			case "heading":
				// A heading is one line: a line break in it is a space.
				hashes := strings.Repeat("#", min(max(n.Int("level", 1), 1), 6))
				block(indent, hashes+" "+strings.ReplaceAll(md, "\n", " "))
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
				block(indent, "<details>\n<summary>"+md+"</summary>")
				visit(n.Children, indent)
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
					block(indent, "**"+escapeText(t)+"**")
				}
				var labels []string
				for _, l := range stringsOf(n.Data["labels"]) {
					labels = append(labels, escapeText(l))
				}
				rows := [][]string{append([]string{""}, labels...)}
				for _, s := range listOf(n.Data["series"]) {
					m := asMap(s)
					row := []string{escapeText(fmt.Sprint(m["name"]))}
					for _, v := range listOf(m["values"]) {
						f, _ := AsFloat(v)
						row = append(row, trimFloat(f))
					}
					rows = append(rows, row)
				}
				block(indent, tableMarkdown(rows))
				if cap := n.Str("caption"); cap != "" {
					block(indent, "*"+escapeText(cap)+"*")
				}
			case "embed":
				title := n.Str("title")
				if title == "" || strings.EqualFold(title, "interactive embed") {
					block(indent, "*Interactive embed*")
				} else {
					block(indent, "*"+escapeText(title)+" (interactive embed)*")
				}
			case "video":
				title := n.Str("title")
				if title == "" {
					title = "Video"
				}
				block(indent, link(title, n.Str("url")))
			case "math_equation":
				block(indent, "$$\n"+n.Str("formula")+"\n$$")
			case "diagram":
				block(indent, CodeMarkdown(&Node{Type: "code", Data: map[string]any{"language": "mermaid", "delta": Delta{{Text: n.Str("source")}}.JSON()}}))
			case "link_preview", "file":
				block(indent, link(cardTitle(n), n.Str("url")))
			case "recording":
				block(indent, "*Recording: "+escapeText(n.Str("title"))+"*")
			case "subpage":
				name := n.Str("page_id")
				if pageTitle != nil {
					name = pageTitle(name)
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

// tableMarkdown writes rows as a GitHub table, the first its header.
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
				cells[i] = strings.ReplaceAll(r[i], "|", `\|`)
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

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func stringsOf(v any) []string {
	var out []string
	for _, e := range listOf(v) {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprintf("%g", f)
}
