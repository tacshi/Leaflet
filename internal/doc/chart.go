package doc

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

var (
	ErrChartRows   = errors.New("chart: no rows")
	ErrChartSeries = errors.New("chart: no series")
)

type ChartNumberError struct {
	Line int
	Cell string
}

func (e *ChartNumberError) Error() string {
	return fmt.Sprintf("chart: line %d: %q is not a number", e.Line, e.Cell)
}

type Chart struct {
	Kind, Title, Caption string
	Labels               []string
	Series               []ChartSeries
}

type ChartSeries struct {
	Name   string
	Values []float64
}

func ChartOf(n *Node) Chart {
	ch := Chart{Kind: n.Str("chart_type"), Title: n.Str("title"), Caption: n.Str("caption")}
	if ch.Kind == "" {
		ch.Kind = "bar"
	}
	for _, l := range listOf(n.Data["labels"]) {
		ch.Labels = append(ch.Labels, JSONText(l))
	}
	for _, s := range listOf(n.Data["series"]) {
		m := asMap(s)
		cs := ChartSeries{}
		cs.Name, _ = m["name"].(string)
		for _, v := range listOf(m["values"]) {
			f, _ := AsFloat(v)
			cs.Values = append(cs.Values, f)
		}
		ch.Series = append(ch.Series, cs)
	}
	return ch
}

func ChartCSV(n *Node, labelColumn string) string {
	ch := ChartOf(n)
	var b strings.Builder
	b.WriteString(csvField(labelColumn))
	for _, s := range ch.Series {
		b.WriteString(", " + csvField(s.Name))
	}
	for i, l := range ch.Labels {
		b.WriteString("\n" + csvField(l))
		for _, s := range ch.Series {
			v := 0.0
			if i < len(s.Values) {
				v = s.Values[i]
			}
			b.WriteString(", " + strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	return b.String()
}

func csvField(s string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{s})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

func ParseChartCSV(text string) (labels []any, series []any, err error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	r.LazyQuotes = true
	var rows [][]string
	var lines [][]int
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		at := make([]int, len(row))
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
			at[i], _ = r.FieldPos(i)
		}
		if len(row) > 1 || row[0] != "" {
			rows, lines = append(rows, row), append(lines, at)
		}
	}
	if len(rows) < 2 {
		return nil, nil, ErrChartRows
	}
	header := rows[0]
	if len(header) < 2 {
		return nil, nil, ErrChartSeries
	}
	values := make([][]any, len(header)-1)
	for i, cells := range rows[1:] {
		labels = append(labels, cells[0])
		for s := range values {
			v := 0.0
			if s+1 < len(cells) && cells[s+1] != "" {
				if v, err = strconv.ParseFloat(cells[s+1], 64); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, nil, &ChartNumberError{Line: lines[i+1][s+1], Cell: cells[s+1]}
				}
			}
			values[s] = append(values[s], v)
		}
	}
	for s, name := range header[1:] {
		series = append(series, map[string]any{"name": name, "values": values[s]})
	}
	return labels, series, nil
}
