package menu

import (
	"errors"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// ChartData makes a chart's data from the chart dialog's fields, its data
// CSV text, cutting the texts longer than a chart keeps (doc.MaxChartTitle
// and the rest) rather than refusing them in words written for agents,
// with an error a person can read.
func ChartData(kind, title, caption, text string) (map[string]any, error) {
	labels, series, err := doc.ParseChartCSV(text)
	if number, ok := errors.AsType[*doc.ChartNumberError](err); ok {
		return nil, errors.New(locales.DialogChartNotNumber(number.Line, number.Cell))
	}
	switch {
	case errors.Is(err, doc.ErrChartRows):
		return nil, errors.New(locales.DialogChartNeedRows())
	case errors.Is(err, doc.ErrChartSeries):
		return nil, errors.New(locales.DialogChartNeedSeries())
	case err != nil:
		return nil, err
	case len(labels) > doc.MaxChartLabels:
		return nil, errors.New(locales.DialogChartTooManyRows(doc.MaxChartLabels))
	case len(series) > doc.MaxChartSeries:
		return nil, errors.New(locales.DialogChartTooManySeries(doc.MaxChartSeries))
	}
	for i, l := range labels {
		labels[i] = doc.ClipText(l.(string), doc.MaxChartLabel)
	}
	for _, s := range series {
		m := s.(map[string]any)
		m["name"] = doc.ClipText(m["name"].(string), doc.MaxChartName)
	}
	return doc.ChartData(map[string]any{
		"chart_type": kind, "title": doc.ClipText(title, doc.MaxChartTitle), "labels": labels, "series": series,
		"caption": doc.ClipText(caption, doc.MaxChartCaption),
	})
}
