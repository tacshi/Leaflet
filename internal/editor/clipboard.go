package editor

import (
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui/richtext"

	"leaflet/internal/doc"
)

// Copy and paste carry formats. A copy offers the selection as MyGo's
// rich text and as HTML, which MyGo's rich text writes, besides its
// Markdown; a paste from another app reads those, or RTF, as blocks of
// their formats. Leaflet's own copy pastes as the blocks it came from.

// copySelection puts the selection on the clipboard.
func (e *Editor) copySelection() {
	text := e.copyText()
	if text == "" {
		return
	}
	nodes := e.copyBlocks()
	clip.text, clip.blocks, clip.delta = text, nodes, nil
	if nodes == nil {
		// Text within one block keeps its formats too.
		a, b := e.ordered()
		clip.delta = e.node(a.ID).Delta().Slice(a.Off, b.Off)
		nodes = []*doc.Node{newText("paragraph", clip.delta)}
	}
	rich := richOf(nodes, e.Host.PageTitle)
	reps := []transfer.Representation{}
	if raw, err := rich.MarshalJSON(); err == nil {
		reps = append(reps, transfer.Bytes(richtext.Format, raw))
	}
	reps = append(reps, transfer.Bytes(transfer.HTML, []byte(rich.HTML())), transfer.Bytes(transfer.Text, []byte(text)))
	e.services.WriteClipboardData(transfer.New(transfer.NewItem(reps...)))
}

// pasteClipboard pastes what is on the clipboard: formatted text from
// another app as blocks in its formats, unless plain, and anything else
// as its text, which reads as Markdown.
func (e *Editor) pasteClipboard(plain bool) {
	data := e.services.ReadClipboardData(richtext.Format, transfer.HTML, richtext.RTF, transfer.Text)
	raw, _ := data.Read(transfer.Text)
	text := string(raw)
	own := text != "" && text == clip.text && (clip.blocks != nil || clip.delta != nil)
	// Code and table cells take text as it is.
	if !plain && !own && !e.ReadOnly && !e.inCode() && !e.inTable(e.focus.ID) {
		if rich, ok := richOfData(data); ok && formatted(rich) {
			e.pasteBlocks(blocksOf(rich))
			return
		}
	}
	e.paste(text)
}

// richOfData reads the richest format of data it has: MyGo's rich text,
// HTML or RTF.
func richOfData(data transfer.Data) (richtext.Text, bool) {
	if raw, err := data.Read(richtext.Format); err == nil {
		var t richtext.Text
		if t.UnmarshalJSON(raw) == nil {
			return t, true
		}
	}
	if raw, err := data.Read(transfer.HTML); err == nil {
		return richtext.ParseHTML(string(raw)), true
	}
	if raw, err := data.Read(richtext.RTF); err == nil {
		return richtext.ParseRTF(raw), true
	}
	return richtext.Text{}, false
}

// formatted reports whether rich text has any format Leaflet keeps. Text
// without one pastes as its plain text, which may be Markdown, as an
// editor of code copies it.
func formatted(t richtext.Text) bool {
	for _, p := range t.Paragraphs() {
		if s := p.Style; s.Heading > 0 || s.List != richtext.NoList || s.Quote || s.CodeBlock {
			return true
		}
		for _, r := range p.Runs {
			if !attrsOf(r.Attributes).Zero() {
				return true
			}
		}
	}
	return false
}

// richOf writes blocks as rich text: a paragraph of its kind for each text
// block, its list items indented as they nest, and the Markdown of the
// others.
func richOf(nodes []*doc.Node, title func(string) string) richtext.Text {
	var paras []richtext.Paragraph
	var walk func(nodes []*doc.Node, depth int)
	walk = func(nodes []*doc.Node, depth int) {
		for _, n := range nodes {
			if !n.IsText() {
				md := doc.PageMarkdown("", &doc.Content{Children: []*doc.Node{n}}, title)
				paras = append(paras, richtext.Paragraph{Runs: []richtext.Run{{Text: strings.TrimRight(md, "\n")}}})
				continue
			}
			var st richtext.ParagraphStyle
			switch n.Type {
			case "heading":
				st.Heading = min(max(n.Int("level", 1), 1), 6)
			case "bulleted_list", "toggle_list":
				st.List = richtext.Bullets
			case "numbered_list":
				st.List = richtext.Numbers
			case "todo_list":
				st.List, st.Checked = richtext.Tasks, n.Bool("checked")
			case "quote", "callout":
				st.Quote = true
			case "code":
				st.CodeBlock = true
			}
			if st.List != richtext.NoList {
				st.Indent = depth
			}
			var runs []richtext.Run
			for _, r := range n.Delta() {
				runs = append(runs, richtext.Run{Text: r.Text, Attributes: richAttrs(r.Attrs)})
			}
			paras = append(paras, richtext.Paragraph{Style: st, Runs: runs})
			walk(n.Children, depth+1)
		}
	}
	walk(nodes, 0)
	return richtext.TextOf(paras...)
}

// richAttrs are a run's formats in rich text. A link to a page of
// Leaflet's goes: other apps can't open it.
func richAttrs(a doc.Attrs) richtext.Attributes {
	out := richtext.Attributes{Italic: a.Italic, Underline: a.Under, Strikethrough: a.Strike, Code: a.Code,
		Color: hexColor(a.Color), Background: hexColor(a.Background)}
	if a.Bold {
		out.Weight = 700
	}
	if _, page := doc.PageLinkID(a.Href); !page {
		out.Link = a.Href
	}
	return out
}

// attrsOf are the formats of a run of rich text that Leaflet keeps: a
// link only to the web or a mail address.
func attrsOf(a richtext.Attributes) doc.Attrs {
	out := doc.Attrs{Bold: a.Bold(), Italic: a.Italic, Under: a.Underline, Strike: a.Strikethrough, Code: a.Code}
	if doc.WebURL(a.Link) || strings.HasPrefix(a.Link, "mailto:") {
		out.Href = strings.TrimSpace(a.Link)
	}
	return out
}

// blocksOf reads rich text as blocks: a block for each paragraph, list
// items nested under the item one level up, and the lines of code next
// to each other one code block. Attachments, as pictures, are left out.
func blocksOf(t richtext.Text) []*doc.Node {
	var out []*doc.Node
	// lists are the list items open at each level; code the code block
	// open.
	var lists []*doc.Node
	var code *doc.Node
	for _, p := range t.Paragraphs() {
		var d doc.Delta
		for _, r := range p.Runs {
			if r.Attributes.Attachment == nil {
				d = append(d, doc.Run{Text: r.Text, Attrs: attrsOf(r.Attributes)})
			}
		}
		d = d.Compact()
		st := p.Style
		if st.CodeBlock {
			if code != nil {
				code.SetDelta(doc.Delta{{Text: code.Delta().Plain() + "\n" + d.Plain()}})
			} else {
				code = doc.CodeNode(d.Plain(), "")
				lists = nil
				out = append(out, code)
			}
			continue
		}
		code = nil
		if d.Len() == 0 {
			// An empty line only separates blocks, as in Markdown.
			continue
		}
		var n *doc.Node
		switch {
		case st.Heading > 0:
			n = newText("heading", d)
			n.Set("level", st.Heading)
		case st.List == richtext.Bullets:
			n = newText("bulleted_list", d)
		case st.List == richtext.Numbers:
			n = newText("numbered_list", d)
		case st.List == richtext.Tasks:
			n = newText("todo_list", d)
			n.Set("checked", st.Checked)
		case st.Quote:
			n = newText("quote", d)
		default:
			n = newText("paragraph", d)
		}
		if !doc.NestableTypes[n.Type] {
			lists = nil
			out = append(out, n)
			continue
		}
		depth := min(st.Indent, len(lists))
		lists = append(lists[:depth], n)
		if depth == 0 {
			out = append(out, n)
		} else {
			parent := lists[depth-1]
			parent.Children = append(parent.Children, n)
		}
	}
	return out
}
