package doc

import (
	"slices"
	"strings"
)

// TableLines is the table style of simple lines: a bold header with a rule
// under it and a hairline between rows, with no outer border, no column
// lines and no shaded header. A table without a style is a grid.
const TableLines = "lines"

// How a table's column aligns, as Markdown's delimiter row says it: ""
// leaves it to the reading direction, as "---" does.
const (
	AlignLeft   = "left"
	AlignCenter = "center"
	AlignRight  = "right"
)

// TableStyle is the table's style: TableLines, or "" for a grid.
func TableStyle(n *Node) string {
	if n.Str("style") == TableLines {
		return TableLines
	}
	return ""
}

// SetTableStyle gives the table a style, TableLines or "" for a grid.
func SetTableStyle(n *Node, style string) {
	if style == TableLines {
		n.Set("style", TableLines)
		return
	}
	delete(n.Data, "style")
}

// TableAlign is the alignment s names, or "" for none it knows.
func TableAlign(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case AlignLeft, AlignCenter, AlignRight:
		return s
	}
	return ""
}

// TableAligns is how each of the table's cols columns aligns.
func TableAligns(n *Node, cols int) []string {
	out := make([]string, max(cols, 0))
	raw, _ := n.Data["aligns"].([]any)
	for i := range min(len(out), len(raw)) {
		s, _ := raw[i].(string)
		out[i] = TableAlign(s)
	}
	return out
}

// SetTableAligns keeps aligns as the table's, without the columns at its
// end that align as the reading direction does, and none when no column
// aligns otherwise.
func SetTableAligns(n *Node, aligns []string) {
	end := len(aligns)
	for end > 0 && TableAlign(aligns[end-1]) == "" {
		end--
	}
	if end == 0 {
		delete(n.Data, "aligns")
		return
	}
	out := make([]any, end)
	for i, a := range aligns[:end] {
		out[i] = TableAlign(a)
	}
	n.Set("aligns", out)
}

// SetTableAlign aligns the table's column col.
func SetTableAlign(n *Node, col int, align string) {
	_, cols := TableSize(n)
	if col < 0 || col >= cols {
		return
	}
	aligns := TableAligns(n, cols)
	aligns[col] = TableAlign(align)
	SetTableAligns(n, aligns)
}

// ShiftTableAligns moves the alignments of the columns from at on by by,
// as columns are put in there (by > 0) or taken out (by < 0), so that each
// stays with its column.
func ShiftTableAligns(n *Node, at, by int) {
	raw, _ := n.Data["aligns"].([]any)
	aligns := TableAligns(n, len(raw))
	if at < 0 || at > len(aligns) || by == 0 {
		return
	}
	if by > 0 {
		aligns = slices.Insert(aligns, at, make([]string, by)...)
	} else {
		aligns = slices.Delete(aligns, at, min(at-by, len(aligns)))
	}
	SetTableAligns(n, aligns)
}

// KeepTableLook gives n, the table a spec made to replace the table prev,
// prev's style and column alignments where the spec names none (no key, a
// null, "" or an empty list), as a model rewriting a table's cells leaves
// its look as it was.
func KeepTableLook(prev, n *Node, spec map[string]any) {
	if prev.Type != "table" || n.Type != "table" {
		return
	}
	if style, _ := spec["table_style"].(string); strings.TrimSpace(style) == "" {
		SetTableStyle(n, TableStyle(prev))
	}
	if align, _ := spec["align"].([]any); len(align) == 0 {
		_, was := TableSize(prev)
		_, cols := TableSize(n)
		aligns := TableAligns(prev, was)
		SetTableAligns(n, aligns[:min(len(aligns), cols)])
	}
}

// tableRuleAligns is the alignment of each column a table's delimiter row
// gives, as "|:---|:---:|---:|" does left, center and right.
func tableRuleAligns(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		c = strings.TrimSpace(c)
		left, right := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":") && len(c) > 1
		switch {
		case left && right:
			out[i] = AlignCenter
		case right:
			out[i] = AlignRight
		case left:
			out[i] = AlignLeft
		}
	}
	return out
}

// tableRuleCell is the delimiter row's cell for a column of this alignment.
func tableRuleCell(align string) string {
	switch align {
	case AlignLeft:
		return ":---"
	case AlignCenter:
		return ":---:"
	case AlignRight:
		return "---:"
	}
	return "---"
}

// mergedTableData is the own data of a table both sides changed since
// base, merged key by key: a key only one side changed takes that side's
// value, and one both changed takes the winner's, the other side's when
// otherWins. clash reports a key both changed, not alike, other than the
// table's size, which its cells settle (placeCells): a look one side gave
// the table and a row the other added are both kept.
func mergedTableData(base, primary, other *Node, otherWins bool) (data map[string]any, clash bool) {
	keys := map[string]bool{}
	for _, n := range []*Node{base, primary, other} {
		for k := range n.Data {
			keys[k] = true
		}
	}
	data = map[string]any{}
	for k := range keys {
		b, bok := base.Data[k]
		p, pok := primary.Data[k]
		o, ook := other.Data[k]
		same := func(x any, xok bool, y any, yok bool) bool { return xok == yok && canonical(x) == canonical(y) }
		var v any
		var ok bool
		switch {
		case same(p, pok, o, ook), same(o, ook, b, bok):
			v, ok = p, pok
		case same(p, pok, b, bok):
			v, ok = o, ook
		default:
			clash = clash || k != "rowsLen" && k != "colsLen"
			v, ok = p, pok
			if otherWins {
				v, ok = o, ook
			}
		}
		if ok {
			data[k] = cloneValue(v)
		}
	}
	return data, clash
}
