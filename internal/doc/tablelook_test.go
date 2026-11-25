package doc

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownTableAlignsItsColumns(t *testing.T) {
	nodes := MarkdownBlocks("| Time | Mon–Fri | Sat | Sun |\n|:--|--:|:-:|---|\n| 8am | $400 | $400 | $600 |")
	if len(nodes) != 1 || nodes[0].Type != "table" {
		t.Fatalf("nodes %+v", nodes)
	}
	if got, want := TableAligns(nodes[0], 4), []string{"left", "right", "center", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("aligns %q, want %q", got, want)
	}
	if got, want := PageMarkdown("", &Content{Children: nodes}, MarkdownOptions{}), "| Time | Mon–Fri | Sat | Sun |\n| :--- | ---: | :---: | --- |\n| 8am | $400 | $400 | $600 |\n"; got != want {
		t.Errorf("markdown\n%s\nwant\n%s", got, want)
	}
	plain := MarkdownBlocks("| a | b |\n| --- | --- |\n| 1 | 2 |")
	if _, ok := plain[0].Data["aligns"]; ok {
		t.Errorf("a table with no alignment keeps aligns: %v", plain[0].Data)
	}
}

func TestSpecTableLook(t *testing.T) {
	rows := []any{[]any{"Item", "Price"}, []any{"Tea", "$3"}}
	n, err := SpecToNode(map[string]any{"type": "table", "rows": rows, "table_style": "lines", "align": []any{"", "right", "left"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if TableStyle(n) != TableLines {
		t.Errorf("style %q", TableStyle(n))
	}
	if got := TableAligns(n, 2); !reflect.DeepEqual(got, []string{"", "right"}) {
		t.Errorf("aligns %q", got)
	}
	views := BlockViews(&Content{Children: []*Node{n}})
	if views[0]["table_style"] != TableLines || !reflect.DeepEqual(views[0]["align"], []string{"", "right"}) {
		t.Errorf("view %+v", views[0])
	}
	grid, err := SpecToNode(map[string]any{"type": "table", "rows": rows, "table_style": "grid"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if v := BlockViews(&Content{Children: []*Node{grid}})[0]; v["table_style"] != nil || v["align"] != nil || TableStyle(grid) != "" {
		t.Errorf("a grid table's view %+v", v)
	}
	for _, bad := range []map[string]any{
		{"type": "table", "rows": rows, "table_style": "dotted"},
		{"type": "table", "rows": rows, "table_style": 1.0},
		{"type": "table", "rows": rows, "align": []any{"middle"}},
		{"type": "table", "rows": rows, "align": "right"},
		{"type": "table", "rows": rows, "align": []any{1.0, "right"}},
	} {
		if _, err := SpecToNode(bad, "", ""); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	// A null aligns its column as text does.
	n, err = SpecToNode(map[string]any{"type": "table", "rows": rows, "table_style": nil, "align": []any{nil, "right"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := TableAligns(n, 2); !reflect.DeepEqual(got, []string{"", "right"}) || TableStyle(n) != "" {
		t.Errorf("with nulls: aligns %q, style %q", got, TableStyle(n))
	}
}

func TestPageHTMLTableLook(t *testing.T) {
	table, err := TableNode([][]string{{"Time", "Price"}, {"8am", "$400"}}, "tb")
	if err != nil {
		t.Fatal(err)
	}
	SetTableStyle(table, TableLines)
	SetTableAlign(table, 1, AlignRight)
	out := PageHTML("T", &Content{Children: []*Node{table}}, HTMLOptions{})
	for _, want := range []string{
		`class="table lines"`,
		`<th class="nowrap" style="text-align:right">Price</th>`,
		`<td class="nowrap" style="text-align:right">$400</td>`,
		`.lines th,.lines td{border:0;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestTableAlignsFollowTheirColumns(t *testing.T) {
	table, err := TableNode([][]string{{"a", "b", "c"}}, "tb")
	if err != nil {
		t.Fatal(err)
	}
	SetTableAlign(table, 1, AlignCenter)
	SetTableAlign(table, 2, AlignRight)
	ShiftTableAligns(table, 1, 1)
	if got := TableAligns(table, 4); !reflect.DeepEqual(got, []string{"", "", "center", "right"}) {
		t.Errorf("after a column put in: %q", got)
	}
	ShiftTableAligns(table, 0, -2)
	if got := TableAligns(table, 2); !reflect.DeepEqual(got, []string{"center", "right"}) {
		t.Errorf("after two columns taken out: %q", got)
	}
	SetTableAlign(table, 0, "")
	SetTableAlign(table, 1, "")
	if _, ok := table.Data["aligns"]; ok {
		t.Errorf("no alignment left keeps aligns: %v", table.Data["aligns"])
	}
	SetTableAlign(table, 7, AlignRight)
	if _, ok := table.Data["aligns"]; ok {
		t.Errorf("a column past the table's end aligned: %v", table.Data["aligns"])
	}
}

func TestMergeTableLookAndRowsApart(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}, {"c", "d"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	look := func(at int64) *Content {
		c := base.Copy()
		SetTableStyle(c.Find("T"), TableLines)
		SetTableAlign(c.Find("T"), 1, AlignRight)
		return c.Stamped(base, at)
	}
	addRow := func(at int64) *Content {
		c := base.Copy()
		tt := c.Find("T")
		for col, text := range []string{"e", "f"} {
			tt.Children = append(tt.Children, TableCellNode(2, col, Delta{{Text: text}}))
		}
		tt.Set("rowsLen", 3)
		SortTableCells(tt)
		return c.Stamped(base, at)
	}
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}}
	for _, tc := range []struct {
		name         string
		mine, theirs *Content
	}{
		{"the look later", look(2000), addRow(1500)},
		{"the row later", look(1500), addRow(2000)},
	} {
		var first *Content
		for _, sides := range [][2]*Content{{tc.mine, tc.theirs}, {tc.theirs, tc.mine}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			tt := got.Find("T")
			if rows := TableRows(tt); !reflect.DeepEqual(rows, want) {
				t.Errorf("%s: rows %q, want %q", tc.name, rows, want)
			}
			if TableStyle(tt) != TableLines || !reflect.DeepEqual(TableAligns(tt, 2), []string{"", "right"}) {
				t.Errorf("%s: look lost: %v", tc.name, tt.Data)
			}
			if len(conflicts) != 0 {
				t.Errorf("%s: conflicts %+v", tc.name, conflicts)
			}
			if first == nil {
				first = got
			} else if got.Encode() != first.Encode() {
				t.Errorf("%s: the merge differs as the sides swap", tc.name)
			}
		}
		for _, side := range []*Content{tc.mine, tc.theirs} {
			again, conflicts := Merge(base, side, first)
			if again.Encode() != first.Encode() || len(conflicts) != 0 {
				t.Errorf("%s: merged again, %s, conflicts %+v", tc.name, again.Encode(), conflicts)
			}
		}
	}
}

func TestMergeTableStyleBothChangedKeepsAConflict(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	mine := base.Copy()
	SetTableAlign(mine.Find("T"), 0, AlignRight)
	theirs := base.Copy()
	SetTableAlign(theirs.Find("T"), 0, AlignCenter)
	got, conflicts := Merge(base, mine.Stamped(base, 2000), theirs.Stamped(base, 1500))
	if len(conflicts) != 1 || conflicts[0].BlockID != "T" {
		t.Fatalf("conflicts %+v", conflicts)
	}
	if a := TableAligns(got.Find("T"), 2); a[0] != AlignRight {
		t.Errorf("the later change lost: %q", a)
	}
}

// putColumn puts a column of texts into the table at at, as the editor
// does, its cells new and the alignments after it moved on.
func putColumn(tt *Node, at int, texts ...string) {
	rows, cols := TableSize(tt)
	for _, c := range tt.Children {
		if p := c.Int("colPosition", 0); p >= at {
			c.Set("colPosition", p+1)
		}
	}
	for r := range rows {
		tt.Children = append(tt.Children, TableCellNode(r, at, Delta{{Text: texts[r]}}))
	}
	ShiftTableAligns(tt, at, 1)
	tt.Set("colsLen", cols+1)
	SortTableCells(tt)
}

// takeColumn takes the table's column at out, as the editor does.
func takeColumn(tt *Node, at int) {
	_, cols := TableSize(tt)
	kept := tt.Children[:0]
	for _, c := range tt.Children {
		switch p := c.Int("colPosition", 0); {
		case p == at:
			continue
		case p > at:
			c.Set("colPosition", p-1)
		}
		kept = append(kept, c)
	}
	tt.Children = kept
	ShiftTableAligns(tt, at, -1)
	tt.Set("colsLen", cols-1)
}

func TestMergeTableAlignsGoWithTheirColumns(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rows         [][]string
		base         func(*Node)
		mine, theirs func(*Node)
		want         []string
	}{
		{
			name:   "aligned on one side, a column put before it on the other",
			rows:   [][]string{{"a", "b"}, {"c", "d"}},
			mine:   func(tt *Node) { SetTableAlign(tt, 1, AlignRight) },
			theirs: func(tt *Node) { putColumn(tt, 0, "N", "n") },
			want:   []string{"", "", "right"},
		},
		{
			name:   "an aligned column, a column put before it on both sides",
			rows:   [][]string{{"a", "b"}, {"c", "d"}},
			base:   func(tt *Node) { SetTableAlign(tt, 1, AlignRight) },
			mine:   func(tt *Node) { putColumn(tt, 0, "M", "m") },
			theirs: func(tt *Node) { putColumn(tt, 0, "N", "n") },
			want:   []string{"", "", "", "right"},
		},
		{
			name:   "aligned on one side, a column before it taken out on the other",
			rows:   [][]string{{"Item", "Note", "Price"}, {"tea", "hot", "$3"}},
			mine:   func(tt *Node) { SetTableAlign(tt, 2, AlignRight) },
			theirs: func(tt *Node) { takeColumn(tt, 1) },
			want:   []string{"", "right"},
		},
	} {
		table, _ := TableNode(tc.rows, "T")
		base := FromBlocks([]*Node{table})
		for id := range base.Blocks {
			base.Blocks[id] = 1000
		}
		if tc.base != nil {
			was := base
			base = base.Copy()
			tc.base(base.Find("T"))
			base = base.Stamped(was, 1100)
		}
		mine, theirs := base.Copy(), base.Copy()
		tc.mine(mine.Find("T"))
		tc.theirs(theirs.Find("T"))
		mine, theirs = mine.Stamped(base, 2000), theirs.Stamped(base, 1500)
		for _, sides := range [][2]*Content{{mine, theirs}, {theirs, mine}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			tt := got.Find("T")
			_, cols := TableSize(tt)
			if aligns := TableAligns(tt, cols); !reflect.DeepEqual(aligns, tc.want) || len(conflicts) != 0 {
				t.Errorf("%s: aligns %q, want %q; conflicts %d", tc.name, aligns, tc.want, len(conflicts))
			}
			if raw, _ := tt.Data["aligns"].([]any); len(raw) > cols {
				t.Errorf("%s: %d alignments for %d columns", tc.name, len(raw), cols)
			}
		}
	}
}
