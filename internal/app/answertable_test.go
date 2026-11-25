package app

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestAnswerTablesHaveSimpleLines(t *testing.T) {
	var o, cells markdown.Options
	linesTables(&o, &cells)
	md := "Fees:\n\n| Time | Weekdays | Sunday |\n|:--|--:|:-:|\n| 8am to 6pm | **$400** | $600 |\n| 10pm to 8am | $800 | $800 |\n"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { markdownView(c, md, &o).FontSize(13.5) })
	}, 600, 300)
	tt.Frame()
	tt.Frame()
	find := func(text string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("no %q in %v", text, tt.Texts())
		}
		return r
	}
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1.5 }
	if a, b := find("Time"), find("10pm to 8am"); !near(a.X, b.X) {
		t.Errorf("left column: header at %v, cell at %v", a.X, b.X)
	}
	if a, b := find("Weekdays"), find("$800"); !near(a.X+a.W, b.X+b.W) {
		t.Errorf("right column: header ends at %v, cell at %v", a.X+a.W, b.X+b.W)
	}
	if a, b := find("Sunday"), find("$600"); !near(a.X+a.W/2, b.X+b.W/2) {
		t.Errorf("center column: header's middle at %v, cell's at %v", a.X+a.W/2, b.X+b.W/2)
	}
	if fees, cell := find("Fees:"), find("Time"); !near(fees.X, cell.X) {
		t.Errorf("the table's text starts at %v, the text above it at %v", cell.X, fees.X)
	}
}
