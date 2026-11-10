package doc

import (
	"fmt"
	"testing"
)

func TestOutline(t *testing.T) {
	h := func(id string, level int) *Node {
		n := &Node{ID: id, Type: "heading"}
		n.Set("level", level)
		return n
	}
	var parents, depths, kids []string
	for _, row := range Outline([]*Node{h("a", 2), h("b", 3), h("c", 3), h("d", 1), h("e", 3), h("f", 2), h("g", 3)}) {
		parents = append(parents, row.Parent)
		depths = append(depths, fmt.Sprint(row.Depth))
		kids = append(kids, fmt.Sprint(row.HasChildren))
	}
	if fmt.Sprint(parents) != "[ a a  d d f]" {
		t.Errorf("parents %q", parents)
	}
	if fmt.Sprint(depths) != "[0 1 1 0 1 1 2]" {
		t.Errorf("depths %v", depths)
	}
	if fmt.Sprint(kids) != "[true false false true false true false]" {
		t.Errorf("with headings in them %v", kids)
	}
	flat := Outline([]*Node{h("a", 2), h("b", 2), {ID: "c", Type: "heading"}})
	for _, row := range flat {
		if row.Parent != "" || row.Depth != 0 || row.HasChildren {
			t.Errorf("side by side: %+v", row)
		}
	}
	if flat[2].Level != 1 {
		t.Errorf("a heading of no level is of level %d", flat[2].Level)
	}
}
