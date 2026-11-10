package doc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func bigRows(rows, cols int) [][]string {
	out := make([][]string, rows)
	for r := range out {
		out[r] = make([]string, cols)
		for c := range out[r] {
			out[r][c] = fmt.Sprintf("r%dc%d", r, c)
		}
	}
	return out
}

func TestPlainTableRows(t *testing.T) {
	table, _ := TableNode([][]string{{"**A**", "B"}, {"", ""}, {"1", "[two](https://example.com)"}}, "")
	want := [][]string{{"A", "B"}, {"", ""}, {"1", "two"}}
	if got := PlainTableRows(table); !reflect.DeepEqual(got, want) {
		t.Errorf("PlainTableRows = %q, want %q", got, want)
	}
}

func TestTableCellsMatchTableCell(t *testing.T) {
	table, _ := TableNode(bigRows(3, 2), "")
	table.Children = table.Children[1:]
	grid := TableCells(table)
	for r := range 3 {
		for c := range 2 {
			if grid[r][c] != tableCell(table, r, c) {
				t.Errorf("cell %d,%d", r, c)
			}
		}
	}
	if grid[0][0] != nil {
		t.Error("a missing cell is there")
	}
}

func TestSetTableRowsKeepsCells(t *testing.T) {
	table, _ := TableNode(bigRows(3, 2), "")
	kept := tableCell(table, 2, 1)
	rows := bigRows(4, 2)
	rows[0][0] = "changed"
	if err := SetTableRows(table, rows); err != nil {
		t.Fatal(err)
	}
	if tableCell(table, 2, 1) != kept || !reflect.DeepEqual(TableRows(table), rows) {
		t.Errorf("rows %q", TableRows(table))
	}
}

func BenchmarkSetTableRows(b *testing.B) {
	rows := bigRows(2000, 5)
	table, _ := TableNode(rows, "")
	rows[1000][2] = "changed"
	for b.Loop() {
		_ = SetTableRows(table, rows)
	}
}

func BenchmarkTableCells(b *testing.B) {
	table, _ := TableNode(bigRows(2000, 5), "")
	for b.Loop() {
		_ = TableCells(table)
	}
}

func TestHugeCellPositionsReadAsNone(t *testing.T) {
	for _, pos := range []string{"1e19", "-1e19", "1e16", "1e308"} {
		c, err := Decode(`{"document":{"type":"page","id":"root","children":[{"id":"t","type":"table","data":{"rowsLen":2,"colsLen":1},"children":[
			{"id":"c1","type":"table/cell","data":{"rowPosition":0,"colPosition":0},"children":[{"id":"p1","type":"paragraph","data":{"delta":[{"insert":"kept"}]}}]},
			{"id":"c2","type":"table/cell","data":{"rowPosition":` + pos + `,"colPosition":0},"children":[{"id":"p2","type":"paragraph","data":{"delta":[{"insert":"far"}]}}]}]}]}}`)
		if err != nil {
			t.Fatal(err)
		}
		table := c.Find("t")
		if rows, cols := TableSize(table); rows != 1 || cols != 1 {
			t.Errorf("position %s: size %d×%d, want 1×1", pos, rows, cols)
		}
		if got := TableRows(table); !reflect.DeepEqual(got, [][]string{{"kept"}}) {
			t.Errorf("position %s: rows %q", pos, got)
		}
		_ = TableCells(table)
		_ = BlockViews(c)
		_ = PageHTML("T", c, HTMLOptions{})
		_ = PageMarkdown("T", c, MarkdownOptions{})
	}
}

func TestAsIntKeepsExactIntegers(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want int
	}{
		{float64(3), 3}, {2.7, 2}, {float64(1 << 53), 1 << 53}, {1e19, -1}, {-1e19, -1},
		{int64(1 << 62), -1}, {json.Number("12"), 12}, {json.Number("9223372036854775807"), -1}, {"3", -1}, {nil, -1},
	} {
		if got := AsInt(tc.v, -1); got != tc.want {
			t.Errorf("AsInt(%v) = %d, want %d", tc.v, got, tc.want)
		}
	}
}

func tableCell(table *Node, row, col int) *Node {
	for _, cell := range table.Children {
		if cell.Int("colPosition", -1) == col && cell.Int("rowPosition", -1) == row {
			return cell
		}
	}
	return nil
}
