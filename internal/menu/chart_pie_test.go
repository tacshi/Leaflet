package menu

import "testing"

// The chart editor takes a pie with several columns of values, drawing
// the first, as it always has; only agents are held to one series.
func TestChartEditorTakesAPieOfSeveralSeries(t *testing.T) {
	data, err := ChartData("pie", "Share", "", ",A,B\nx,1,2\ny,3,4")
	if err != nil {
		t.Fatalf("a pie of two series: %v", err)
	}
	if series, _ := data["series"].([]any); data["chart_type"] != "pie" || len(series) != 2 {
		t.Errorf("chart %v", data)
	}
}
