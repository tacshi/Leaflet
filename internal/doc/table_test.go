package doc

import (
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

// TableCells finds each cell where TableCell does, and nil for a missing
// one.
func TestTableCellsMatchTableCell(t *testing.T) {
	table, _ := TableNode(bigRows(3, 2), "")
	table.Children = table.Children[1:] // the cell at row 0, column 0 goes
	grid := TableCells(table)
	for r := range 3 {
		for c := range 2 {
			if grid[r][c] != TableCell(table, r, c) {
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
	kept := TableCell(table, 2, 1)
	rows := bigRows(4, 2)
	rows[0][0] = "changed"
	if err := SetTableRows(table, rows); err != nil {
		t.Fatal(err)
	}
	if TableCell(table, 2, 1) != kept || !reflect.DeepEqual(TableRows(table), rows) {
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
