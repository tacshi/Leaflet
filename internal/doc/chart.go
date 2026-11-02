package doc

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// A chart's data as people edit it: one line per label, its label then a
// value for each series, after a first line naming the series, the
// values apart by commas: CSV, a label or a name in quotes where it holds
// a comma.

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

// A Chart is a chart block's data, as ChartOf reads it.
type Chart struct {
	Kind, Title, Caption string
	Labels               []string
	Series               []ChartSeries
}

// ChartSeries is one series of a chart.
type ChartSeries struct {
	Name   string
	Values []float64
}

// ChartOf reads a chart block's data, as everything that shows one does:
// a bar chart unless it is another, a value that is no number 0, a label
// as specText writes it, and a series with no name, or one that is no
// text, nameless.
func ChartOf(n *Node) Chart {
	ch := Chart{Kind: n.Str("chart_type"), Title: n.Str("title"), Caption: n.Str("caption")}
	if ch.Kind == "" {
		ch.Kind = "bar"
	}
	for _, l := range listOf(n.Data["labels"]) {
		// A label is text as the server writes it: a number as JavaScript
		// writes one, null, a list or an object nothing.
		ch.Labels = append(ch.Labels, specText(l))
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

// ChartCSV is a chart block's labels and series as ParseChartCSV reads
// them, its first line headed labelColumn.
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

// csvField writes a label or a series' name as a field of CSV, in quotes
// when it holds a comma, a quote or a line break, or starts with a space.
func csvField(s string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{s})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// ParseChartCSV reads a chart's labels and series, as ChartData takes
// them, from text as ChartCSV writes it: CSV, with spaces after the
// commas, a field in quotes where it holds a comma, and a quote in a field
// not quoted as it is, as people type one. An empty value is 0.
func ParseChartCSV(text string) (labels []any, series []any, err error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	r.LazyQuotes = true
	// lines are where each row's fields start, counting from 1, for what
	// is wrong in one.
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
		// A line of only spaces is no row, as a blank one is none.
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
				if v, err = strconv.ParseFloat(cells[s+1], 64); err != nil {
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
