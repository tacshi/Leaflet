package menu

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/locales"
)

func TestChartData(t *testing.T) {
	data, err := ChartData("line", "Sales", "By month", "Month, Sales\nJan, 1\nFeb, 2")
	if err != nil {
		t.Fatal(err)
	}
	if data["chart_type"] != "line" || data["title"] != "Sales" || data["caption"] != "By month" ||
		!reflect.DeepEqual(data["labels"], []any{"Jan", "Feb"}) {
		t.Errorf("data %v", data)
	}
	for text, want := range map[string]string{
		"Month, Sales":                 locales.DialogChartNeedRows(),
		"Month\nJan":                   locales.DialogChartNeedSeries(),
		"Month, Sales\nJan, 1\nFeb, x": locales.DialogChartNotNumber(3, "x"),
	} {
		if _, err := ChartData("bar", "", "", text); err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
	if _, err := ChartData("donut", "", "", "Month, Sales\nJan, 1"); err == nil {
		t.Error("a donut chart was made")
	}
}

func TestChartDataKeepsToWhatAChartHolds(t *testing.T) {
	rows := []string{"Day, Visits"}
	for i := range doc.MaxChartLabels + 1 {
		rows = append(rows, fmt.Sprintf("d%d, %d", i, i))
	}
	if _, err := ChartData("line", "", "", strings.Join(rows, "\n")); err == nil || err.Error() != locales.DialogChartTooManyRows(doc.MaxChartLabels) {
		t.Errorf("too many rows: %v", err)
	}
	header := "Label"
	for i := range doc.MaxChartSeries + 1 {
		header += fmt.Sprintf(", s%d", i)
	}
	if _, err := ChartData("bar", "", "", header+"\na"+strings.Repeat(", 1", doc.MaxChartSeries+1)); err == nil || err.Error() != locales.DialogChartTooManySeries(doc.MaxChartSeries) {
		t.Errorf("too many series: %v", err)
	}
	long := strings.Repeat("长", 3000)
	data, err := ChartData("bar", long, long, "Label, "+long+"\n"+long+", 1")
	if err != nil {
		t.Fatal(err)
	}
	ch := doc.ChartOf(&doc.Node{Type: "chart", Data: data})
	if len([]rune(ch.Title)) != doc.MaxChartTitle || len([]rune(ch.Caption)) != doc.MaxChartCaption ||
		len([]rune(ch.Labels[0])) != doc.MaxChartLabel || len([]rune(ch.Series[0].Name)) != doc.MaxChartName {
		t.Errorf("texts kept at %d, %d, %d and %d characters", len([]rune(ch.Title)), len([]rune(ch.Caption)), len([]rune(ch.Labels[0])), len([]rune(ch.Series[0].Name)))
	}
}
