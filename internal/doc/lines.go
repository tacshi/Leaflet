package doc

import (
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// MarkdownBlocks reads Markdown as blocks, as pasting it does.
func MarkdownBlocks(text string) []*Node {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return ParseLines(strings.Trim(text, "\n"))
}

// ParseLines reads pasted text as blocks: a line each, with markdown
// block prefixes. List items indented under one nest in it, lines of a
// markdown table make a table, and a fenced block a code block.
func ParseLines(text string) []*Node {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*Node
	// lists are the list items open at each depth, for nesting.
	var lists []*Node
	var table [][]string
	flushTable := func() {
		if len(table) > 0 {
			if t, err := TableNode(table, ""); err == nil {
				out = append(out, t)
			}
			table = nil
		}
	}
	// code is the fenced block open, and fence the backticks it opened
	// with; its indent comes off each of its lines.
	// A Mermaid fence makes a diagram, and lines between "$$" math: the
	// block in its place in out, at codeAt, once it closes.
	var code *Node
	var codeLines []string
	fence, fenceIndent := "", ""
	codeAt := 0
	closeCode := func() {
		text := strings.Join(codeLines, "\n")
		switch {
		case fence == "$$":
			out[codeAt] = MathNode(text)
		case code.Str("language") == "mermaid":
			out[codeAt] = DiagramNode(text)
		default:
			code.SetDelta(Delta{{Text: text}}.Compact())
		}
		code = nil
	}
	// callout is the GitHub alert being read: its lines go in it.
	var callout *Node
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if code != nil {
			if fence == "$$" && strings.TrimSpace(trimmed) == "$$" || fence != "$$" && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "` ") == "" {
				closeCode()
				continue
			}
			codeLines = append(codeLines, strings.TrimPrefix(line, fenceIndent))
			continue
		}
		if tone, ok := AlertTone(trimmed); ok {
			flushTable()
			lists = nil
			callout = textNode("callout", nil)
			callout.Set("tone", tone)
			out = append(out, callout)
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, ">"); ok && callout != nil {
			d := callout.Delta()
			if d.Len() > 0 {
				d = d.Concat(Delta{{Text: "\n"}})
			}
			callout.SetDelta(d.Concat(FromMarkdown(strings.TrimPrefix(rest, " "))))
			continue
		}
		callout = nil
		if strings.TrimSpace(line) == "" {
			// A blank line only separates blocks, as in Markdown; it makes
			// no empty paragraph. A list goes on across it.
			flushTable()
			continue
		}
		if language, ok := CodeFence(trimmed); ok || strings.TrimSpace(trimmed) == "$$" {
			flushTable()
			lists = nil
			code, codeLines = CodeNode("", language), nil
			fenceIndent = line[:len(line)-len(trimmed)]
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "`"))]
			if !ok {
				fence = "$$"
			}
			codeAt = len(out)
			out = append(out, code)
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			lists = nil
			if row := tableRow(trimmed); row != nil {
				table = append(table, row)
			}
			continue
		}
		flushTable()
		indent := strings.Count(line[:len(line)-len(trimmed)], " ") + 2*strings.Count(line[:len(line)-len(trimmed)], "\t")
		typ, rest := "paragraph", trimmed
		level, checked := 0, false
		hashes := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		bullet := len(trimmed) >= 2 && strings.ContainsRune("-*+", rune(trimmed[0])) && trimmed[1] == ' '
		switch {
		case hashes >= 1 && hashes <= 6 && strings.HasPrefix(trimmed[hashes:], " "):
			typ, level, rest = "heading", hashes, trimmed[hashes+1:]
		case bullet && strings.HasPrefix(trimmed[2:], "[ ] "):
			typ, rest = "todo_list", trimmed[6:]
		case bullet && (strings.HasPrefix(trimmed[2:], "[x] ") || strings.HasPrefix(trimmed[2:], "[X] ")):
			typ, rest, checked = "todo_list", trimmed[6:], true
		case bullet:
			typ, rest = "bulleted_list", trimmed[2:]
		case strings.HasPrefix(trimmed, "> "):
			typ, rest = "quote", trimmed[2:]
		case IsRule(trimmed):
			lists = nil
			out = append(out, &Node{ID: NewBlockID(), Type: "divider"})
			continue
		default:
			if img := imageLine(trimmed); img != nil {
				lists = nil
				out = append(out, img)
				continue
			}
			if i := strings.IndexAny(trimmed, ".)"); i > 0 && i < 4 && isDigits(trimmed[:i]) && strings.HasPrefix(trimmed[i+1:], " ") {
				typ, rest = "numbered_list", trimmed[i+2:]
			}
		}
		n := textNode(typ, FromMarkdown(rest))
		if level > 0 {
			n.Set("level", level)
		}
		if typ == "todo_list" {
			n.Set("checked", checked)
		}
		if !NestableTypes[typ] {
			lists = nil
			out = append(out, n)
			continue
		}
		// Two spaces, or a tab, a level: under the item open one level up.
		depth := min(indent/2, len(lists))
		lists = append(lists[:depth], n)
		if depth == 0 {
			out = append(out, n)
		} else {
			parent := lists[depth-1]
			parent.Children = append(parent.Children, n)
		}
	}
	flushTable()
	if code != nil {
		// A fence left open runs to the end.
		closeCode()
	}
	return out
}

// tableRow reads a line of a markdown table, or nil for the line under the
// header ("| --- | --- |"). As in GitHub's tables, a backslash keeps the
// character after it in the cell, and \| is a | of the cell's text.
func tableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line):
			i++
			if line[i] != '|' {
				cell.WriteByte('\\')
			}
			cell.WriteByte(line[i])
		case line[i] == '|':
			cells = append(cells, cell.String())
			cell.Reset()
		default:
			cell.WriteByte(line[i])
		}
	}
	// A closing | ends the last cell; without one, what is after the last
	// | is a cell.
	if rest := cell.String(); rest != "" || len(cells) == 0 {
		cells = append(cells, rest)
	}
	rule := true
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
		if strings.Trim(cells[i], "-: ") != "" || cells[i] == "" {
			rule = false
		}
	}
	if rule {
		return nil
	}
	return cells
}

// imageLine reads a line that is only a picture from the web, as
// "![alt](https://…/a.png)", as an image block; nil for any other line.
func imageLine(line string) *Node {
	if !strings.HasPrefix(line, "![") {
		return nil
	}
	md := parser.New(parser.GFM)
	md.Set(line)
	if md.Len() != 2 || md.Block(1).Kind != parser.Paragraph {
		return nil
	}
	in := md.Inlines(1)
	if len(in) == 0 || in[0].Kind != parser.Image || in[0].Next != len(in) {
		return nil
	}
	n, err := ImageNode(in[0].Dest)
	if err != nil {
		return nil
	}
	n.Set("alt", parser.PlainText(in[1:]))
	return n
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// textNode is a new text block of a type, holding d.
func textNode(typ string, d Delta) *Node {
	n := &Node{ID: NewBlockID(), Type: typ}
	n.SetDelta(d)
	return n
}
