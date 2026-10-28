package doc

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// ToolError is a tool call an agent made that cannot be applied. The
// message goes back to the model so it can correct itself.
type ToolError struct{ Message string }

func (e *ToolError) Error() string { return e.Message }

func toolErr(format string, args ...any) error {
	return &ToolError{Message: fmt.Sprintf(format, args...)}
}

// AgentTypes are the block types agents may write.
var AgentTypes = []string{
	"paragraph", "heading", "bulleted_list", "numbered_list", "todo_list", "quote",
	"divider", "image", "table", "chart", "embed", "video", "code", "callout", "toggle_list", "link_preview", "math_equation", "diagram",
}

var chartTypes = map[string]bool{"bar": true, "line": true, "pie": true}

// maxEmbedHTML caps an embed's source, as the server does.
const maxEmbedHTML = 200_000

// SpecToNode turns a BlockSpec (docs/protocol.md §2) into a block. id is
// kept when given.
func SpecToNode(spec map[string]any, id, author string) (*Node, error) {
	typ, _ := spec["type"].(string)
	if typ == "" {
		typ = "paragraph"
	}
	allowed := false
	for _, t := range AgentTypes {
		allowed = allowed || t == typ
	}
	if !allowed {
		return nil, toolErr("Unknown block type %q. Use one of: %s.", typ, strings.Join(AgentTypes, ", "))
	}
	if id == "" {
		id = NewBlockID()
	}
	text, _ := spec["text"].(string)
	if typ == "paragraph" && IsRule(text) {
		// A Markdown rule written as text is the divider it stands for.
		typ = "divider"
	}
	n := &Node{ID: id, Type: typ}
	switch typ {
	case "paragraph", "quote":
		n.SetDelta(FromMarkdown(text))
	case "heading":
		level := min(max(AsInt(spec["level"], 2), 1), 6)
		n.SetDelta(FromMarkdown(text))
		n.Set("level", level)
	case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
		n.SetDelta(FromMarkdown(text))
		if typ == "todo_list" {
			checked, _ := spec["checked"].(bool)
			n.Set("checked", checked)
			if due, _ := spec["due"].(string); due != "" {
				if t, err := time.ParseInLocation(DueLayout, due, time.Local); err == nil {
					n.SetDue(t)
				}
			}
		}
		if typ == "toggle_list" {
			collapsed, _ := spec["collapsed"].(bool)
			n.Set("collapsed", collapsed)
		}
		if kids, ok := spec["children"].([]any); ok {
			for _, k := range kids {
				child, err := SpecToNode(asMap(k), "", author)
				if err != nil {
					return nil, err
				}
				n.Children = append(n.Children, child)
			}
		}
	case "callout":
		tone, _ := spec["tone"].(string)
		n.SetDelta(FromMarkdown(text))
		n.Set("tone", CalloutTone(tone))
	case "code":
		// The code as it is: no markdown.
		language, _ := spec["language"].(string)
		n = CodeNode(strings.TrimSuffix(text, "\n"), language)
		n.ID = id
	case "divider":
	case "image":
		img := asMap(spec["image"])
		if img == nil {
			img = spec
		}
		url, _ := img["url"].(string)
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return nil, toolErr("An image block needs image.url (http or https).")
		}
		alt, _ := img["alt"].(string)
		n.Data = map[string]any{"url": url, "align": "center", "alt": alt}
	case "table":
		rows := rowsOf(spec["rows"])
		t, err := TableNode(rows, id)
		if err != nil {
			return nil, err
		}
		n = t
	case "chart":
		data, err := ChartData(asMap(spec["chart"]))
		if err != nil {
			return nil, err
		}
		data["author"] = author
		n.Data = data
	case "embed":
		e := asMap(spec["embed"])
		html, _ := e["html"].(string)
		if strings.TrimSpace(html) == "" {
			return nil, toolErr("An embed block needs embed.html.")
		}
		if len(html) > maxEmbedHTML {
			return nil, toolErr("embed.html must be under 200 KB.")
		}
		title, _ := e["title"].(string)
		if title == "" {
			title = "Interactive embed"
		}
		n.Data = map[string]any{
			"title":  title,
			"html":   html,
			"height": min(max(AsInt(e["height"], 320), 120), 1200),
			"author": author,
		}
	case "math_equation":
		formula, _ := spec["formula"].(string)
		if strings.TrimSpace(formula) == "" {
			return nil, toolErr("A math_equation block needs formula, in TeX.")
		}
		n.Data = MathNode(formula).Data
	case "diagram":
		source, _ := spec["source"].(string)
		if strings.TrimSpace(source) == "" {
			return nil, toolErr("A diagram block needs source, in Mermaid.")
		}
		n.Data = DiagramNode(source).Data
	case "link_preview":
		l := asMap(spec["link"])
		if l == nil {
			l = spec
		}
		link, _ := l["url"].(string)
		ln, err := LinkNode(link)
		if err != nil {
			return nil, err
		}
		n.Data = ln.Data
	case "video":
		v := asMap(spec["video"])
		if v == nil {
			v = spec
		}
		link, _ := v["url"].(string)
		title, _ := v["title"].(string)
		vn, err := VideoNode(link, title)
		if err != nil {
			return nil, err
		}
		n.Data = vn.Data
	}
	if kids, ok := spec["children"].([]any); ok && len(kids) > 0 && !NestableTypes[typ] {
		return nil, toolErr("Only list blocks can have children, not %q.", typ)
	}
	return n, nil
}

// IsRule tells whether a line is a Markdown horizontal rule: three or
// more of -, * or _, spaces between them allowed.
func IsRule(line string) bool {
	s := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
	if len(s) < 3 || strings.Trim(s, s[:1]) != "" {
		return false
	}
	return s[0] == '-' || s[0] == '*' || s[0] == '_'
}

// ChartData validates a chart spec and returns its stored data.
func ChartData(chart map[string]any) (map[string]any, error) {
	kind, _ := chart["chart_type"].(string)
	if kind == "" {
		kind = "bar"
	}
	if !chartTypes[kind] {
		return nil, toolErr("chart.chart_type must be bar, line, or pie.")
	}
	var labels []any
	if raw, ok := chart["labels"].([]any); ok {
		for _, l := range raw {
			labels = append(labels, fmt.Sprint(l))
		}
	}
	var series []any
	if raw, ok := chart["series"].([]any); ok {
		for _, r := range raw {
			s := asMap(r)
			var values []any
			if vs, ok := s["values"].([]any); ok {
				for _, v := range vs {
					f, _ := AsFloat(v)
					values = append(values, f)
				}
			}
			name, _ := s["name"].(string)
			series = append(series, map[string]any{"name": name, "values": values})
		}
	}
	if len(labels) == 0 || len(series) == 0 {
		return nil, toolErr("A chart needs chart.labels and at least one series.")
	}
	for _, s := range series {
		m := s.(map[string]any)
		vs := m["values"].([]any)
		if len(vs) != len(labels) {
			return nil, toolErr("Series %q has %d values but there are %d labels.", m["name"], len(vs), len(labels))
		}
	}
	title, _ := chart["title"].(string)
	caption, _ := chart["caption"].(string)
	return map[string]any{
		"chart_type": kind,
		"title":      title,
		"labels":     labels,
		"series":     series,
		"caption":    caption,
	}, nil
}

func rowsOf(v any) [][]string {
	var rows [][]string
	raw, _ := v.([]any)
	for _, r := range raw {
		cells, _ := r.([]any)
		row := []string{}
		for _, c := range cells {
			row = append(row, fmt.Sprint(c))
		}
		rows = append(rows, row)
	}
	return rows
}

// TableNode builds a table block from rows of cells, in appflowy's order:
// column by column, each cell a table/cell holding one paragraph.
func TableNode(rows [][]string, id string) (*Node, error) {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if len(rows) == 0 || cols == 0 {
		return nil, toolErr("A table needs rows: a list of rows, each a list of cells.")
	}
	if id == "" {
		id = NewBlockID()
	}
	n := &Node{ID: id, Type: "table", Data: map[string]any{
		"colsLen":          cols,
		"rowsLen":          len(rows),
		"colDefaultWidth":  160.0,
		"rowDefaultHeight": 40.0,
		"colMinimumWidth":  40.0,
	}}
	for c := 0; c < cols; c++ {
		for r := range rows {
			text := ""
			if c < len(rows[r]) {
				text = rows[r][c]
			}
			p := &Node{ID: NewBlockID(), Type: "paragraph"}
			p.SetDelta(FromMarkdown(text))
			n.Children = append(n.Children, &Node{
				ID:       NewBlockID(),
				Type:     "table/cell",
				Data:     map[string]any{"colPosition": c, "rowPosition": r},
				Children: []*Node{p},
			})
		}
	}
	return n, nil
}

// DropCells takes table cells out of a block that is no longer a table,
// as when a replace makes a list of one: its nested items, if any, stay.
func DropCells(n *Node) {
	if n.Type == "table" {
		return
	}
	n.Children = slices.DeleteFunc(n.Children, func(k *Node) bool { return k.Type == "table/cell" })
	if len(n.Children) == 0 {
		n.Children = nil
	}
}

// TableCell returns the cell of a table at a row and column, or nil.
func TableCell(table *Node, row, col int) *Node {
	for _, cell := range table.Children {
		if cell.Int("colPosition", -1) == col && cell.Int("rowPosition", -1) == row {
			return cell
		}
	}
	return nil
}

// SortTableCells puts a table's cells in appflowy's column-major order.
func SortTableCells(table *Node) {
	sort.SliceStable(table.Children, func(i, j int) bool {
		a, b := table.Children[i], table.Children[j]
		ca, cb := a.Int("colPosition", 0), b.Int("colPosition", 0)
		if ca != cb {
			return ca < cb
		}
		return a.Int("rowPosition", 0) < b.Int("rowPosition", 0)
	})
}

// TableRows rebuilds the rows of a stored table, as inline markdown.
func TableRows(n *Node) [][]string {
	cols := n.Int("colsLen", 0)
	rows := n.Int("rowsLen", 0)
	grid := make([][]string, rows)
	for i := range grid {
		grid[i] = make([]string, cols)
	}
	for _, cell := range n.Children {
		c := cell.Int("colPosition", -1)
		r := cell.Int("rowPosition", -1)
		if c < 0 || r < 0 || r >= rows || c >= cols {
			continue
		}
		var parts []string
		for _, k := range cell.Children {
			parts = append(parts, k.Delta().ToMarkdown())
		}
		grid[r][c] = strings.Join(parts, " ")
	}
	return grid
}

// BlockViews is the flattened, model-readable view of a page (BlockView in
// the protocol).
func BlockViews(c *Content) []map[string]any {
	var out []map[string]any
	var visit func([]*Node, int, string)
	visit = func(nodes []*Node, depth int, parent string) {
		for _, n := range nodes {
			v := map[string]any{"id": n.ID, "type": n.Type, "depth": depth}
			if parent != "" {
				v["parent_id"] = parent
			}
			if n.IsText() {
				v["text"] = n.Delta().ToMarkdown()
			}
			switch n.Type {
			case "code":
				v["text"] = n.Delta().Plain()
				v["language"] = n.Str("language")
			case "callout":
				v["tone"] = CalloutTone(n.Str("tone"))
			case "toggle_list":
				v["collapsed"] = n.Bool("collapsed")
			case "heading":
				v["level"] = n.Int("level", 1)
			case "todo_list":
				v["checked"] = n.Bool("checked")
				if due := n.Str("due"); due != "" {
					v["due"] = due
				}
			case "table":
				v["rows"] = TableRows(n)
			case "chart":
				ch := map[string]any{}
				for _, k := range []string{"chart_type", "title", "labels", "series", "caption"} {
					ch[k] = n.Data[k]
				}
				v["chart"] = ch
			case "embed":
				// The source too, so an agent can change an embed with
				// replace_block rather than write a new one blind.
				v["embed"] = map[string]any{"title": n.Data["title"], "height": n.Data["height"], "html": n.Data["html"]}
			case "image":
				v["image"] = map[string]any{"url": n.Data["url"]}
			case "video":
				v["video"] = map[string]any{"url": n.Data["url"], "title": n.Data["title"]}
			case "math_equation":
				v["formula"] = n.Str("formula")
			case "diagram":
				v["source"] = n.Str("source")
			case "link_preview":
				v["link"] = map[string]any{"url": n.Data["url"], "title": n.Data["title"], "description": n.Data["description"]}
			case "file":
				v["file"] = map[string]any{"name": n.Data["name"], "mime": n.Data["mime"], "size": n.Data["size"]}
			case "recording":
				v["recording"] = map[string]any{"recording_id": n.Data["recording_id"], "title": n.Data["title"]}
			case "subpage":
				v["page_id"] = n.Data["page_id"]
			}
			out = append(out, v)
			if n.Type != "table" && len(n.Children) > 0 {
				visit(n.Children, depth+1, n.ID)
			}
		}
	}
	visit(c.Children, 0, "")
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
