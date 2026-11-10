package menu

import (
	"errors"

	"leaflet/internal/doc"
	"leaflet/locales"
)

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
	}
	return doc.ChartData(map[string]any{
		"chart_type": kind, "title": title, "labels": labels, "series": series, "caption": caption,
	})
}
