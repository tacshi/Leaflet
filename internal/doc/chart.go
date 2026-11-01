package doc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A chart's data as people edit it: one line per label, its label then a
// value for each series, after a first line naming the series, the
// values apart by commas.

// ErrChartRows and ErrChartSeries are what ParseChartCSV finds missing:
// a label under the first line, or a series beside the labels.
var (
	ErrChartRows   = errors.New("chart: no rows")
	ErrChartSeries = errors.New("chart: no series")
)

// ChartNumberError is a value ParseChartCSV couldn't read as a number, on
// Line (counting from 1).
type ChartNumberError struct {
	Line int
	Cell string
}

func (e *ChartNumberError) Error() string {
	return fmt.Sprintf("chart: line %d: %q is not a number", e.Line, e.Cell)
}

// ChartCSV is a chart block's labels and series as ParseChartCSV reads
// them, its first line headed labelColumn.
func ChartCSV(n *Node, labelColumn string) string {
	var labels []string
	if ls, ok := n.Data["labels"].([]any); ok {
		for _, l := range ls {
			labels = append(labels, fmt.Sprint(l))
		}
	}
	type series struct {
		name   string
		values []float64
	}
	var all []series
	if ss, ok := n.Data["series"].([]any); ok {
		for _, s := range ss {
			m, _ := s.(map[string]any)
			cs := series{}
			cs.name, _ = m["name"].(string)
			if vs, ok := m["values"].([]any); ok {
				for _, v := range vs {
					f, _ := AsFloat(v)
					cs.values = append(cs.values, f)
				}
			}
			all = append(all, cs)
		}
	}
	var b strings.Builder
	b.WriteString(labelColumn)
	for _, s := range all {
		b.WriteString(", " + s.name)
	}
	for i, l := range labels {
		b.WriteString("\n" + l)
		for _, s := range all {
			v := 0.0
			if i < len(s.values) {
				v = s.values[i]
			}
			b.WriteString(", " + strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	return b.String()
}

// ParseChartCSV reads a chart's labels and series, as ChartData takes
// them, from text as ChartCSV writes it. An empty value is 0.
func ParseChartCSV(csv string) (labels []any, series []any, err error) {
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if len(lines) < 2 {
		return nil, nil, ErrChartRows
	}
	split := func(s string) []string {
		parts := strings.Split(s, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	header := split(lines[0])
	if len(header) < 2 {
		return nil, nil, ErrChartSeries
	}
	values := make([][]any, len(header)-1)
	for i, line := range lines[1:] {
		cells := split(line)
		labels = append(labels, cells[0])
		for s := range values {
			v := 0.0
			if s+1 < len(cells) && cells[s+1] != "" {
				if v, err = strconv.ParseFloat(cells[s+1], 64); err != nil {
					return nil, nil, &ChartNumberError{Line: i + 2, Cell: cells[s+1]}
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
