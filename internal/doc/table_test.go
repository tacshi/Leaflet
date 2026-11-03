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

// A table's cells show as their text, without its formats, in the rows
// and columns TableCells finds: an empty row stays, as the table has it.
func TestPlainTableRows(t *testing.T) {
	table, _ := TableNode([][]string{{"**A**", "B"}, {"", ""}, {"1", "[two](https://example.com)"}}, "")
	want := [][]string{{"A", "B"}, {"", ""}, {"1", "two"}}
	if got := PlainTableRows(table); !reflect.DeepEqual(got, want) {
		t.Errorf("PlainTableRows = %q, want %q", got, want)
	}
}

// TableCells finds each cell where TableCell does, and nil for a missing
// one.
func TestTableCellsMatchTableCell(t *testing.T) {
	table, _ := TableNode(bigRows(3, 2), "")
	table.Children = table.Children[1:] // the cell at row 0, column 0 goes
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

// SetTableRows keeps the cells whose text is the same, and changes the
// others.
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

// BenchmarkSetTableRows sets a table of 2000 rows of 5 cells, as an agent's
// replace_block of a big table does.
func BenchmarkSetTableRows(b *testing.B) {
	rows := bigRows(2000, 5)
	table, _ := TableNode(rows, "")
	rows[1000][2] = "changed"
	for b.Loop() {
		_ = SetTableRows(table, rows)
	}
}

// BenchmarkTableCells finds every cell of a table of 2000 rows of 5 cells,
// as drawing it does each frame.
func BenchmarkTableCells(b *testing.B) {
	table, _ := TableNode(bigRows(2000, 5), "")
	for b.Loop() {
		_ = TableCells(table)
	}
}

// A cell at a position no JSON number holds exactly, as a hostile editor
// of a shared page can write, sizes no table from it: read as the largest
// int, as arm64 converts it, the table's size overflowed and every reader
// of it panicked.
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

// tableCell is the cell of a table at a row and column, or nil.
func tableCell(table *Node, row, col int) *Node {
	for _, cell := range table.Children {
		if cell.Int("colPosition", -1) == col && cell.Int("rowPosition", -1) == row {
			return cell
		}
	}
	return nil
}
