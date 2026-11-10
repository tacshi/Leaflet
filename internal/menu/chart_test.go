package menu

import (
	"reflect"
	"testing"

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
