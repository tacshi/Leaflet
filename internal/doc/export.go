package doc

import (
	"fmt"
	"strings"
)

// PageMarkdown writes a whole page as CommonMark with GitHub's tables and
// task lists. pageTitle resolves subpage links; it may be nil.
func PageMarkdown(title string, c *Content, pageTitle func(id string) string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString("# " + title + "\n\n")
	}
	var visit func(nodes []*Node, depth int)
	visit = func(nodes []*Node, depth int) {
		number := 0
		inList := false
		for _, n := range nodes {
			indent := strings.Repeat("  ", depth)
			text := n.Delta().ToMarkdown()
			if n.Type != "numbered_list" {
				number = 0
			}
			// A toggle is a <details>, not a list item.
			item := NestableTypes[n.Type] && n.Type != "toggle_list"
			if depth == 0 && inList && !item {
				b.WriteString("\n")
			}
			inList = item
			switch n.Type {
			case "paragraph":
				b.WriteString(indent + text + "\n\n")
			case "heading":
				b.WriteString(strings.Repeat("#", min(max(n.Int("level", 1), 1), 6)) + " " + text + "\n\n")
			case "bulleted_list":
				b.WriteString(indent + "- " + text + "\n")
			case "numbered_list":
				number++
				fmt.Fprintf(&b, "%s%d. %s\n", indent, number, text)
			case "todo_list":
				box := "[ ]"
				if n.Bool("checked") {
					box = "[x]"
				}
				b.WriteString(indent + "- " + box + " " + text + "\n")
			case "quote":
				b.WriteString("> " + text + "\n\n")
			case "code":
				b.WriteString(indent + strings.ReplaceAll(CodeMarkdown(n), "\n", "\n"+indent) + "\n\n")
			case "callout":
				b.WriteString(CalloutMarkdown(n) + "\n\n")
			case "toggle_list":
				b.WriteString("<details>\n<summary>" + text + "</summary>\n\n")
				visit(n.Children, 0)
				b.WriteString("</details>\n\n")
				continue
			case "divider":
				b.WriteString("---\n\n")
			case "image":
				fmt.Fprintf(&b, "![%s](%s)\n\n", n.Str("alt"), n.Str("url"))
			case "table":
				writeTable(&b, TableRows(n))
			case "chart":
				if t := n.Str("title"); t != "" {
					b.WriteString("**" + t + "**\n\n")
				}
				rows := [][]string{append([]string{""}, stringsOf(n.Data["labels"])...)}
				for _, s := range listOf(n.Data["series"]) {
					m := asMap(s)
					row := []string{fmt.Sprint(m["name"])}
					for _, v := range listOf(m["values"]) {
						f, _ := AsFloat(v)
						row = append(row, trimFloat(f))
					}
					rows = append(rows, row)
				}
				writeTable(&b, rows)
				if cap := n.Str("caption"); cap != "" {
					b.WriteString("*" + cap + "*\n\n")
				}
			case "embed":
				title := n.Str("title")
				if title == "" || strings.EqualFold(title, "interactive embed") {
					b.WriteString("*Interactive embed*\n\n")
				} else {
					b.WriteString("*" + title + " (interactive embed)*\n\n")
				}
			case "video":
				title := n.Str("title")
				if title == "" {
					title = "Video"
				}
				fmt.Fprintf(&b, "[%s](%s)\n\n", title, n.Str("url"))
			case "math_equation":
				b.WriteString("$$\n" + n.Str("formula") + "\n$$\n\n")
			case "diagram":
				b.WriteString(CodeMarkdown(&Node{Type: "code", Data: map[string]any{"language": "mermaid", "delta": Delta{{Text: n.Str("source")}}.JSON()}}) + "\n\n")
			case "link_preview", "file":
				fmt.Fprintf(&b, "[%s](%s)\n\n", cardTitle(n), n.Str("url"))
			case "recording":
				b.WriteString("*Recording: " + n.Str("title") + "*\n\n")
			case "subpage":
				name := n.Str("page_id")
				if pageTitle != nil {
					name = pageTitle(name)
				}
				b.WriteString("[" + name + "](leaflet://page/" + n.Str("page_id") + ")\n\n")
			}
			if n.Type != "table" && len(n.Children) > 0 {
				visit(n.Children, depth+1)
			}
		}
	}
	visit(c.Children, 0)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	line := func(r []string) {
		cells := make([]string, cols)
		for i := range cells {
			if i < len(r) {
				cells[i] = strings.ReplaceAll(r[i], "|", `\|`)
			}
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	line(rows[0])
	b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	b.WriteString("\n")
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
