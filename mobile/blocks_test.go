package mobile

import (
	"encoding/json"
	"reflect"
	"testing"

	"leaflet/internal/doc"
)

func TestChangedRowsKeepWhatChangedMeanwhile(t *testing.T) {
	was := [][]string{{"A", "B", "C"}, {"1", "2", "3"}, {"4", "5", "6"}}
	for _, c := range []struct {
		name        string
		now, edited [][]string
		want        [][]string
	}{
		{
			"a row and a cell written meanwhile",
			[][]string{{"A", "B", "C"}, {"1", "2", "3"}, {"4", "5", "6"}, {"7", "8", "9"}},
			[][]string{{"A", "B", "C"}, {"1", "two", "3"}, {"4", "5", "6"}},
			[][]string{{"A", "B", "C"}, {"1", "two", "3"}, {"4", "5", "6"}, {"7", "8", "9"}},
		},
		{
			"a cell changed on both sides",
			[][]string{{"A", "B", "C"}, {"1", "deux", "3"}, {"4", "five", "6"}},
			[][]string{{"A", "B", "C"}, {"1", "two", "3"}, {"4", "5", "6"}},
			[][]string{{"A", "B", "C"}, {"1", "two", "3"}, {"4", "five", "6"}},
		},
		{
			"a row and a column added, before those added meanwhile",
			[][]string{{"A", "B", "C", "X"}, {"1", "2", "3", "x"}, {"4", "5", "6", "y"}, {"7", "8", "9", "z"}},
			[][]string{{"A", "B", "C", "D"}, {"1", "2", "3", ""}, {"4", "5", "6", ""}, {"new", "", "", "d"}},
			[][]string{
				{"A", "B", "C", "D", "X"}, {"1", "2", "3", "", "x"}, {"4", "5", "6", "", "y"},
				{"new", "", "", "d", ""}, {"7", "8", "9", "", "z"},
			},
		},
		{
			"the last row and column taken off",
			[][]string{{"A", "B", "C"}, {"1", "two", "3"}, {"4", "5", "6"}, {"7", "8", "9"}},
			[][]string{{"A", "B"}, {"1", "2"}},
			[][]string{{"A", "B"}, {"1", "two"}, {"7", "8"}},
		},
		{
			"rows taken away meanwhile",
			[][]string{{"A", "B", "C"}},
			[][]string{{"A", "B", "C"}, {"1", "2", "3"}, {"4", "5", "six"}},
			[][]string{{"A", "B", "C"}, {"4", "5", "six"}},
		},
	} {
		if got := changedRows(c.now, was, c.edited); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestATableSheetSavesIntoTheTableAsItIsNow(t *testing.T) {
	opened, _ := doc.TableNode([][]string{{"A", "B"}, {"1", "2"}}, "t")
	now, _ := doc.TableNode([][]string{{"A", "B"}, {"1", "2"}, {"3", "**4**"}}, "t")
	raw, _ := json.Marshal(now)
	was, _ := json.Marshal(doc.TableRows(opened))
	out, err := ChangeTableRows(string(raw), string(was), `[["A","Bee"],["1","2"]]`)
	if err != nil {
		t.Fatal(err)
	}
	var got doc.Node
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if rows := doc.TableRows(&got); got.ID != "t" || !reflect.DeepEqual(rows, [][]string{{"A", "Bee"}, {"1", "2"}, {"3", "**4**"}}) {
		t.Fatalf("saved %s with rows %q", got.ID, rows)
	}
	kept := map[string]bool{}
	for _, cell := range now.Children {
		kept[cell.ID] = true
	}
	for _, cell := range got.Children {
		if !kept[cell.ID] {
			t.Errorf("cell %s at %d,%d is new", cell.ID, cell.Int("rowPosition", -1), cell.Int("colPosition", -1))
		}
	}
	if _, err := ChangeTableRows(string(raw), string(was), "not rows"); err == nil {
		t.Error("rows that aren't JSON saved")
	}
}

func TestATableSheetSavesTheTablesLook(t *testing.T) {
	table, _ := doc.TableNode([][]string{{"Time", "Price", "Note"}, {"8am", "$400", "x"}}, "t")
	raw, _ := json.Marshal(table)
	out, err := SetTableLook(string(raw), doc.TableLines, `["","right","center","left"]`)
	if err != nil {
		t.Fatal(err)
	}
	var got doc.Node
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if doc.TableStyle(&got) != doc.TableLines || !reflect.DeepEqual(doc.TableAligns(&got, 3), []string{"", "right", "center"}) {
		t.Errorf("saved %v", got.Data)
	}
	was, _ := json.Marshal(doc.TableRows(&got))
	out, err = ChangeTableRows(out, string(was), `[["Time","Price"],["8am","$400"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if aligns, _ := got.Data["aligns"].([]any); len(aligns) != 2 {
		t.Errorf("after the sheet took a column off, aligns %v", got.Data["aligns"])
	}
	para, _ := json.Marshal(&doc.Node{ID: "p", Type: "paragraph"})
	if _, err := SetTableLook(string(para), doc.TableLines, "[]"); err == nil {
		t.Error("a paragraph took a table's look")
	}
}
