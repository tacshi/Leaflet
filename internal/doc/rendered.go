package doc

import "strings"

func MathNode(formula string) *Node {
	return &Node{ID: NewBlockID(), Type: "math_equation", Data: map[string]any{"formula": strings.TrimSpace(formula)}}
}

func DiagramNode(source string) *Node {
	return &Node{ID: NewBlockID(), Type: "diagram", Data: map[string]any{"source": strings.Trim(source, "\n")}}
}

func BlockSource(n *Node) (key, source string) {
	key = SourceKey(n.Type)
	return key, n.Str(key)
}

func SourceKey(typ string) string {
	if typ == "math_equation" {
		return "formula"
	}
	return "source"
}
