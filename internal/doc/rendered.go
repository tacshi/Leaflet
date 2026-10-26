package doc

import "strings"

// Math and diagrams are blocks drawn from source: "math_equation", as in
// appflowy_editor, holds a TeX formula in data.formula, shown in display
// style; "diagram" holds Mermaid in data.source.

// MathNode is a math block of a TeX formula.
func MathNode(formula string) *Node {
	return &Node{ID: NewBlockID(), Type: "math_equation", Data: map[string]any{"formula": strings.TrimSpace(formula)}}
}

// DiagramNode is a diagram block of Mermaid source.
func DiagramNode(source string) *Node {
	return &Node{ID: NewBlockID(), Type: "diagram", Data: map[string]any{"source": strings.Trim(source, "\n")}}
}

// BlockSource is the source a math or diagram block is drawn from, and
// the key it is kept under.
func BlockSource(n *Node) (key, source string) {
	if n.Type == "math_equation" {
		return "formula", n.Str("formula")
	}
	return "source", n.Str("source")
}
