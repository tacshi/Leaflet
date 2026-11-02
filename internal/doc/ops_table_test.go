package doc

import "testing"

// An insert put in a table, or next to one of its cells, goes after the
// table: a table holds only its cells. One in a cell stays there, and a
// cell put back next to another stays in its table.
func TestInsertAnchoredInATable(t *testing.T) {
	table, err := TableNode([][]string{{"a", "b"}}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	cell := table.Children[0]
	para := func(id string) *Node { return &Node{ID: id, Type: "paragraph"} }
	for _, tc := range []struct {
		name  string
		p     Position
		node  *Node
		after bool
	}{
		{"in the table", Position{Parent: "t1"}, para("x"), true},
		{"after a cell", Position{After: cell.ID}, para("x"), true},
		{"before a cell", Position{Before: cell.ID}, para("x"), true},
		{"in a cell", Position{Parent: cell.ID}, para("x"), false},
		{"a cell after a cell", Position{After: cell.ID}, &Node{ID: "x", Type: "table/cell"}, false},
	} {
		c := FromBlocks([]*Node{table.Clone(), para("p1")})
		if err := c.Apply(InsertOp(tc.p, tc.node)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		loc := c.Locate("x")
		afterTable := loc.Parent == nil && loc.Index == 1
		if afterTable != tc.after {
			t.Errorf("%s: went in %v at %d", tc.name, loc.Parent, loc.Index)
		}
		for _, k := range c.Children[0].Children {
			if k.Type != "table/cell" {
				t.Errorf("%s: the table holds a %s", tc.name, k.Type)
			}
		}
	}
}
