package doc

import (
	"errors"
	"reflect"
	"testing"
)

// A label or a series' name holding a comma, a quote or a leading space
// is quoted, and reads back as it was.
func TestChartCSVRoundTrips(t *testing.T) {
	labels := []any{"Jan, 2024", `5" screen`, " padded", "plain"}
	series := []any{
		map[string]any{"name": "Sales, net", "values": []any{1.0, 2.5, 3.0, 4.0}},
		map[string]any{"name": `say "hi"`, "values": []any{0.0, -1.0, 2.0, 1e6}},
	}
	n := &Node{Type: "chart", Data: map[string]any{"labels": labels, "series": series}}
	csv := ChartCSV(n, "Month, year")
	want := "\"Month, year\", \"Sales, net\", \"say \"\"hi\"\"\"\n" +
		"\"Jan, 2024\", 1, 0\n\"5\"\" screen\", 2.5, -1\n\" padded\", 3, 2\nplain, 4, 1000000"
	if csv != want {
		t.Fatalf("csv\n%s\nwant\n%s", csv, want)
	}
	gotLabels, gotSeries, err := ParseChartCSV(csv)
	// A label's leading space comes off, as every field's does.
	labels[2] = "padded"
	if err != nil || !reflect.DeepEqual(gotLabels, labels) || !reflect.DeepEqual(gotSeries, []any{
		map[string]any{"name": "Sales, net", "values": []any{1.0, 2.5, 3.0, 4.0}},
		map[string]any{"name": `say "hi"`, "values": []any{0.0, -1.0, 2.0, 1e6}},
	}) {
		t.Errorf("read back %q %v, %v", gotLabels, gotSeries, err)
	}
}

// Text typed before labels were quoted reads as it did: unquoted fields,
// a quote inside one, blank lines and missing values.
func TestParseChartCSVReadsTypedText(t *testing.T) {
	labels, series, err := ParseChartCSV("\n  \nMonth, Sales , Costs\nJan, 1\n\n6\" pipe,2, 3 \n")
	if err != nil || !reflect.DeepEqual(labels, []any{"Jan", `6" pipe`}) || !reflect.DeepEqual(series, []any{
		map[string]any{"name": "Sales", "values": []any{1.0, 2.0}},
		map[string]any{"name": "Costs", "values": []any{0.0, 3.0}},
	}) {
		t.Errorf("read %q %v, %v", labels, series, err)
	}
	_, _, err = ParseChartCSV("Month, Sales\n\nJan, 1\nFeb, lots")
	if number, ok := errors.AsType[*ChartNumberError](err); !ok || number.Line != 4 || number.Cell != "lots" {
		t.Errorf("error %v", err)
	}
	if _, _, err := ParseChartCSV("Month, Sales"); !errors.Is(err, ErrChartRows) {
		t.Errorf("no rows: %v", err)
	}
	if _, _, err := ParseChartCSV("Month\nJan"); !errors.Is(err, ErrChartSeries) {
		t.Errorf("no series: %v", err)
	}
}
