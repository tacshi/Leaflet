package doc

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ToolError struct{ Message string }

func (e *ToolError) Error() string { return e.Message }

func toolErr(format string, args ...any) error {
	return &ToolError{Message: fmt.Sprintf(format, args...)}
}

var AgentTypes = []string{
	"paragraph", "heading", "bulleted_list", "numbered_list", "todo_list", "quote",
	"divider", "image", "table", "chart", "embed", "video", "code", "callout", "toggle_list", "link_preview", "math_equation", "diagram",
}

var chartTypes = map[string]bool{"bar": true, "line": true, "pie": true}

const maxEmbedHTML = 200_000

const (
	maxSpecDepth = 16
	maxSpecNodes = 2000
)

func SpecToNode(spec map[string]any, id, author string) (*Node, error) {
	made := 0
	return specToNode(spec, id, author, 0, &made)
}

func SpecsToNodes(specs []any, author string) ([]*Node, error) {
	made := 0
	var out []*Node
	for _, s := range specs {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, toolErr("Each block must be an object.")
		}
		n, err := specToNode(m, "", author, 0, &made)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func specToNode(spec map[string]any, id, author string, depth int, made *int) (*Node, error) {
	if depth >= maxSpecDepth {
		return nil, toolErr("Blocks nest at most %d deep.", maxSpecDepth)
	}
	if *made++; *made > maxSpecNodes {
		return nil, toolErr("At most %d blocks in all per call.", maxSpecNodes)
	}
	typ, _ := spec["type"].(string)
	if typ == "" {
		typ = "paragraph"
	}
	if kids, ok := spec["children"]; ok && kids != nil {
		if _, ok := kids.([]any); !ok {
			return nil, toolErr("children must be a list of blocks.")
		}
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
		typ = "divider"
	}
	n := &Node{ID: id, Type: typ}
	switch typ {
	case "paragraph", "quote":
		n.SetDelta(FromMarkdown(text))
	case "heading":
		level := clampInt(spec["level"], 2, 1, 6)
		n.SetDelta(FromMarkdown(text))
		n.Set("level", level)
	case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
		n.SetDelta(FromMarkdown(text))
		if typ == "todo_list" {
			checked, _ := spec["checked"].(bool)
			n.Set("checked", checked)
			if due, _ := spec["due"].(string); validDue(due) {
				n.Set("due", due)
			}
		}
		if typ == "toggle_list" {
			collapsed, _ := spec["collapsed"].(bool)
			n.Set("collapsed", collapsed)
		}
		if kids, ok := spec["children"].([]any); ok {
			for _, k := range kids {
				m, ok := k.(map[string]any)
				if !ok {
					return nil, toolErr("Each block must be an object.")
				}
				child, err := specToNode(m, "", author, depth+1, made)
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
		if url = strings.TrimSpace(url); !agentURL(url) {
			return nil, toolErr("An image block needs image.url (http or https).")
		}
		alt, _ := img["alt"].(string)
		n.Data = map[string]any{"url": url, "align": "center", "alt": alt}
	case "table":
		rows, ok := rowsOf(spec["rows"])
		if !ok {
			return nil, toolErr("A table needs rows: a list of rows, each a list of cells.")
		}
		cols := 0
		for _, r := range rows {
			cols = max(cols, len(r))
		}
		if *made += len(rows) * cols; *made > maxSpecNodes {
			return nil, toolErr("At most %d blocks in all per call.", maxSpecNodes)
		}
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
		if series, _ := data["series"].([]any); data["chart_type"] == "pie" && len(series) > 1 {
			return nil, toolErr("A pie chart takes exactly one series.")
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
			title = DefaultEmbedTitle
		}
		n.Data = map[string]any{
			"title":  title,
			"html":   html,
			"height": clampInt(e["height"], 320, 120, 1200),
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
		if !agentURL(strings.TrimSpace(link)) {
			link = ""
		}
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
		raw, _ := v["notes"].(string)
		notes, err := checkedNotes(raw, "video")
		if err != nil {
			return nil, err
		}
		if notes != "" {
			n.Data["notes"] = notes
		}
	}
	if kids, ok := spec["children"].([]any); ok && len(kids) > 0 && !NestableTypes[typ] {
		return nil, toolErr("Only list blocks can have children, not %q.", typ)
	}
	return n, nil
}

func IsRule(line string) bool {
	s := strings.Trim(line, " \t")
	if s == "" || !strings.ContainsRune("-*_", rune(s[0])) {
		return false
	}
	marks := 0
	for _, c := range s {
		switch {
		case c == rune(s[0]):
			marks++
		case c != ' ' && c != '\t':
			return false
		}
	}
	return marks >= 3
}

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
			labels = append(labels, JSONText(l))
		}
	}
	var series []any
	if raw, ok := chart["series"].([]any); ok {
		for i, r := range raw {
			s := asMap(r)
			var values []any
			if vs, ok := s["values"].([]any); ok {
				for _, v := range vs {
					values = append(values, chartValue(v))
				}
			}
			name := fmt.Sprintf("Series %d", i+1)
			if v, ok := s["name"]; ok && v != nil {
				name = JSONText(v)
			}
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

func rowsOf(v any) ([][]string, bool) {
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var rows [][]string
	for _, r := range raw {
		cells, ok := r.([]any)
		if !ok {
			return nil, false
		}
		row := []string{}
		for _, c := range cells {
			row = append(row, JSONText(c))
		}
		rows = append(rows, row)
	}
	return rows, true
}

func JSONText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return jsNumber(x)
	case []any, map[string]any:
		return ""
	}
	return fmt.Sprint(v)
}

func jsNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	if a := math.Abs(f); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	mant, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	sign, digits := exp[:1], strings.TrimLeft(exp[1:], "0")
	return mant + "e" + sign + digits
}

var decimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

func chartValue(v any) float64 {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if !decimal.MatchString(s) {
			return 0
		}
		v = s
	}
	f, _ := AsFloat(v)
	return f
}

func agentURL(s string) bool {
	if !WebURL(s) || strings.ContainsFunc(s, unicode.IsSpace) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Hostname() != ""
}

func CallNodes(nodes []*Node) error {
	made, deep := 0, 0
	var count func([]*Node, int)
	count = func(nodes []*Node, depth int) {
		for _, n := range nodes {
			made++
			deep = max(deep, depth+1)
			if n.Type == "table" {
				made += len(n.Children)
				continue
			}
			count(n.Children, depth+1)
		}
	}
	count(nodes, 0)
	if deep > maxSpecDepth {
		return toolErr("Blocks nest at most %d deep.", maxSpecDepth)
	}
	if made > maxSpecNodes {
		return toolErr("At most %d blocks in all per call.", maxSpecNodes)
	}
	return nil
}

func clampInt(v any, def, lo, hi int) int {
	if f, ok := v.(float64); ok {
		if math.IsNaN(f) {
			return def
		}
		return int(min(max(math.Trunc(f), float64(lo)), float64(hi)))
	}
	return min(max(AsInt(v, def), lo), hi)
}

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
			n.Children = append(n.Children, TableCellNode(r, c, FromMarkdown(text)))
		}
	}
	return n, nil
}

func TableCellNode(row, col int, text Delta) *Node {
	p := &Node{ID: NewBlockID(), Type: "paragraph"}
	p.SetDelta(text)
	return &Node{ID: NewBlockID(), Type: "table/cell", Data: map[string]any{"colPosition": col, "rowPosition": row}, Children: []*Node{p}}
}

func SetTableRows(n *Node, rows [][]string) error {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if len(rows) == 0 || cols == 0 {
		return toolErr("A table needs rows: a list of rows, each a list of cells.")
	}
	grid := TableCells(n)
	var kept []*Node
	for c := 0; c < cols; c++ {
		for r := range rows {
			text := ""
			if c < len(rows[r]) {
				text = rows[r][c]
			}
			var cell *Node
			if r < len(grid) && c < len(grid[r]) {
				cell = grid[r][c]
			}
			if cell == nil {
				cell = TableCellNode(r, c, nil)
			}
			if len(cell.Children) == 0 {
				cell.Children = []*Node{{ID: NewBlockID(), Type: "paragraph"}}
			}
			var parts []string
			for _, k := range cell.Children {
				parts = append(parts, k.Delta().ToMarkdown())
			}
			if strings.Join(parts, " ") != text {
				cell.Children = cell.Children[:1]
				cell.Children[0].SetDelta(FromMarkdown(text))
			}
			kept = append(kept, cell)
		}
	}
	n.Children = kept
	n.Set("rowsLen", len(rows))
	n.Set("colsLen", cols)
	return nil
}

func DropCells(n *Node) {
	if n.Type == "table" {
		return
	}
	n.Children = slices.DeleteFunc(n.Children, func(k *Node) bool { return k.Type == "table/cell" })
	if len(n.Children) == 0 {
		n.Children = nil
	}
}

func TableCells(table *Node) [][]*Node {
	rows, cols := TableSize(table)
	grid := make([][]*Node, rows)
	for r := range grid {
		grid[r] = make([]*Node, cols)
	}
	for _, cell := range table.Children {
		r, c := cell.Int("rowPosition", -1), cell.Int("colPosition", -1)
		if r >= 0 && c >= 0 && r < rows && c < cols && grid[r][c] == nil {
			grid[r][c] = cell
		}
	}
	return grid
}

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

const maxTableCells = 1 << 16

func TableSize(n *Node) (rows, cols int) {
	lastRow, lastCol := -1, -1
	for _, cell := range n.Children {
		if r, c := cell.Int("rowPosition", -1), cell.Int("colPosition", -1); r >= 0 && c >= 0 {
			lastRow, lastCol = max(lastRow, r), max(lastCol, c)
		}
	}
	rows = min(max(n.Int("rowsLen", 0), 0), lastRow+1)
	cols = min(max(n.Int("colsLen", 0), 0), lastCol+1, maxTableCells)
	if rows == 0 || cols == 0 {
		return 0, 0
	}
	return min(rows, maxTableCells/cols), cols
}

func TableRows(n *Node) [][]string { return tableRows(n, Delta.ToMarkdown) }

func ReadableTableRows(n *Node) [][]string { return tableRows(n, Delta.ReadableMarkdown) }

func PlainTableRows(n *Node) [][]string { return tableRows(n, Delta.Plain) }

func tableRows(n *Node, markdown func(Delta) string) [][]string {
	cells := TableCells(n)
	grid := make([][]string, len(cells))
	for r, row := range cells {
		grid[r] = make([]string, len(row))
		for c, cell := range row {
			if cell == nil {
				continue
			}
			var parts []string
			for _, k := range cell.Children {
				parts = append(parts, markdown(k.Delta()))
			}
			grid[r][c] = strings.Join(parts, " ")
		}
	}
	return grid
}

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
				v["level"] = clampInt(n.Data["level"], 1, 1, 6)
			case "todo_list":
				v["checked"] = n.Bool("checked")
				if due := n.Str("due"); due != "" {
					v["due"] = due
				}
			case "table":
				v["rows"] = TableRows(n)
			case "chart":
				v["chart"] = present(n, "chart_type", "title", "labels", "series", "caption")
			case "embed":
				v["embed"] = present(n, "title", "height", "html")
			case "image":
				v["image"] = present(n, "url", "alt")
			case "video":
				v["video"] = present(n, "url", "title", "notes")
			case "math_equation":
				v["formula"] = n.Str("formula")
			case "diagram":
				v["source"] = n.Str("source")
			case "link_preview":
				v["link"] = present(n, "url", "title", "description")
			case "file":
				v["file"] = present(n, "name", "mime", "size", "notes")
			case "recording":
				v["recording"] = present(n, "recording_id", "title", "duration_ms")
			case "subpage":
				if id, ok := n.Data["page_id"]; ok {
					v["page_id"] = id
				}
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

func checkedNotes(notes, kind string) (string, error) {
	if notes = NormalizeNotes(notes); utf8.RuneCountInString(notes) > MaxNotes {
		return "", toolErr("A %s's notes take at most %d characters.", kind, MaxNotes)
	}
	return notes, nil
}

func present(n *Node, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := n.Data[k]; ok {
			out[k] = v
		}
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
