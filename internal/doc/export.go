package doc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"leaflet/locales"
)

// MarkdownOptions are what a page written as Markdown gets from the app
// around it.
type MarkdownOptions struct {
	// Lang is the language of what stands for a block Markdown can't
	// show, as an embed, which can't run there: "" for the catalogs' own.
	Lang string
	// PageTitle names a page a subpage block links to; it may be nil.
	PageTitle func(id string) string
	// Link is where a link of the page goes, in its text and of its
	// blocks alike: an export's go to its own files and pages. nil keeps
	// each as the page has it.
	Link func(href string) string
}

// DefaultEmbedTitle is the title an agent's embed takes when it gives
// none; the app gives a new one the same, in its own language.
const DefaultEmbedTitle = "Interactive embed"

// PageMarkdown writes a whole page as CommonMark with GitHub's tables and
// task lists.
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
	link := func(text, to string) string {
		return "[" + escapeLinkText(text) + "](" + commonMarkDestination(href(to)) + ")"
	}
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
			// An empty paragraph, or one only of spaces, is no block in
			// Markdown: it is not written, nor the blank line before it.
			if n.Type == "paragraph" && len(n.Children) == 0 && strings.TrimSpace(TextMarkdown("", "", "", md)) == "" {
				continue
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
				// A toggle open on the page is open where the Markdown shows.
				details := "<details>"
				if !n.Bool("collapsed") {
					details = "<details open>"
				}
				block(indent, details+"\n<summary>"+summaryMarkdown(md)+"</summary>")
				visit(n.Children, indent)
				// After a blank line, so it ends the toggle, not a list in it.
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
				// An embed with no title, or the one a new embed takes, in
				// any language, is named only for what it is.
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
				// Its times link into the file where it has an address that
				// goes anywhere, as a zip's does.
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

// mathMarkdown writes a formula between lines of $$. One with a line of
// $$ in it, which no TeX draws, goes in a code block instead: that line
// would end it, and the last open a formula of the blocks after it.
func mathMarkdown(formula string) string {
	for l := range strings.Lines(formula) {
		if strings.TrimSpace(l) == "$$" {
			return CodeMarkdown(&Node{Type: "code", Data: map[string]any{"language": "latex", "delta": Delta{{Text: formula}}.JSON()}})
		}
	}
	return "$$\n" + formula + "\n$$"
}

// notesMarkdown writes a block's notes as a list, a line an item, its times
// links to where to gives for each, which plays from there, or plain where
// it gives none. A line's text that would start another kind of block in
// its item, or make the item a rule, is escaped, as TextMarkdown escapes a
// list item's.
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

// withLinks is a copy of c whose text links go where link says.
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

// defaultEmbedTitle reports whether title is the one a new embed takes,
// as an agent gives it or the app in any of its languages: an embed made
// in one language keeps it when the page is read in another.
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

// OneLine is text on one line, as a heading's Markdown is: a line break
// in it is a space.
func OneLine(d Delta) Delta {
	out := make(Delta, len(d))
	for i, r := range d {
		r.Text = strings.ReplaceAll(lineEnds.Replace(r.Text), "\n", " ")
		out[i] = r
	}
	return out
}

// TextMarkdown writes a text block's Markdown at an indent a line at a
// time, the first line after lead, a list item's marker, and the others
// after pad, the room it takes, so they stay in the block. A line break
// is a hard break, a backslash ending the line, as ParseLines reads one,
// after a space where the line ends in a web address, which GitHub's
// Markdown would take the backslash into, and a line that would start
// another block, as a line break then "- x" would a list, is escaped.
// Markdown keeps no spaces at a line's start:
// they would indent it, under a list item before it, so they go, as do
// line breaks at the text's end, which a hard break can't be, and a line
// only of spaces, which would be blank.
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
		// A line of spaces of any kind is a blank one, which ends a block.
		if l = strings.TrimLeft(l, " \t"); strings.TrimSpace(l) == "" {
			l = ""
		}
		// After a list item's "- ", text such as "--" would make a rule.
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

// markdownLines splits inline Markdown into its lines. A code span over a
// line break is a span on each line: a line can't start inside one, where
// its text would read as the start of a block, or end inside one, where
// its hard break would be code.
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
			// A link's text may go over lines; its address, where a
			// backtick opens no code span, is on the line its text ends.
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

// summaryMarkdown writes a toggle's text for its <summary>, a </summary>
// in its text escaped, as summaryEnd reads it; one in a code span, which
// keeps its text as it is, summaryEnd passes over. Lines after the first
// start without spaces, as ParseLines reads them.
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
			// A cell keeps no spaces at its ends, which a row's |s take.
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

// cellMarkdown writes a table cell's Markdown to stay in its cell of a
// GitHub table: a | escaped, even in a code span, and a line break a
// <br>, as ParseLines reads them, a <br> of the text escaped. In a code
// span, where a <br> is text, a line break is a space, as CommonMark
// reads one there.
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
			// A link's text is the cell's Markdown, and its address only
			// has its |s escaped: a backtick there opens no code span.
			if text, _, n, ok := links.link(i); ok {
				b.WriteString("[" + cellMarkdown(string(text)) + strings.ReplaceAll(string(s[i+1+len(text):i+n]), "|", `\|`))
				i += n
				continue
			}
		case '`':
			if code, n := codeSpanAt(s[i:]); n > 0 {
				span := string(s[i : i+n])
				if strings.Contains(code, "\n") {
					// Written again, as a space at either end now may
					// need one more inside the backticks.
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
