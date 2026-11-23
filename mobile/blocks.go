package mobile

import (
	"encoding/json"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/locales"
)

type codeLanguage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func CodeLanguages() string {
	defer catch(nil)
	out := make([]codeLanguage, len(doc.CodeLanguages))
	for i, l := range doc.CodeLanguages {
		out[i] = codeLanguage{l.ID, l.Name}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func ChartText(data string) string {
	defer catch(nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(data), &m)
	return doc.ChartCSV(&doc.Node{Type: "chart", Data: m}, locales.DialogChartLabelColumn())
}

func ChartData(kind, title, caption, text string) (_ string, err error) {
	defer catch(&err)
	data, err := menu.ChartData(kind, title, caption, text)
	if err != nil {
		return "", err
	}
	return encode(data)
}

func TableRows(node string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	return encode(doc.TableRows(&n))
}

func SetTableRows(node, rows string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	var grid [][]string
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(rows), &grid); err != nil {
		return "", err
	}
	if err := doc.SetTableRows(&n, grid); err != nil {
		return "", err
	}
	return encode(&n)
}

// ChangeTableRows saves a table sheet into the table as the page has it
// now, node, which may have changed since the sheet opened on the rows
// was: of the sheet's rows, only the cells it changed go in, with the rows
// and columns it added at the end or took off it, and the rest of the
// table stays as it is.
func ChangeTableRows(node, was, rows string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	var before, after [][]string
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(was), &before); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(rows), &after); err != nil {
		return "", err
	}
	if err := doc.SetTableRows(&n, changedRows(doc.TableRows(&n), before, after)); err != nil {
		return "", err
	}
	return encode(&n)
}

// changedRows is a table's rows now, with a sheet's change from was to
// edited in them. What was changed elsewhere in the meantime stays, the
// rows and columns added after the end the sheet opened on come after the
// sheet's own, and a row or column taken away in the meantime comes back
// only for a cell the sheet changed in it.
func changedRows(now, was, edited [][]string) [][]string {
	changed := func(r, c int) bool {
		e, _ := cellAt(edited, r, c)
		o, _ := cellAt(was, r, c)
		return e != o
	}
	cols := width(edited)
	rows := tableLines(len(now), len(was), len(edited), func(r int) bool {
		for c := range cols {
			if changed(r, c) {
				return true
			}
		}
		return false
	})
	columns := tableLines(width(now), width(was), cols, func(c int) bool {
		for r := range edited {
			if changed(r, c) {
				return true
			}
		}
		return false
	})
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(columns))
		for j, c := range columns {
			if r.edit >= 0 && c.edit >= 0 && changed(r.edit, c.edit) {
				out[i][j], _ = cellAt(edited, r.edit, c.edit)
			} else if v, ok := cellAt(now, r.now, c.now); ok {
				out[i][j] = v
			} else {
				out[i][j], _ = cellAt(edited, r.edit, c.edit)
			}
		}
	}
	return out
}

// A tableLine is a row or a column of a table a sheet saves into: the one
// it is of the table now, and of the sheet's, -1 for none.
type tableLine struct{ now, edit int }

// tableLines lines up the rows, or the columns, of a table that has n of
// them now, as a sheet that opened on o made them e: the sheet's own first,
// but those it took off the end and those gone from the table it changed
// nothing in, then those added after the end it opened on.
func tableLines(n, o, e int, changedIn func(int) bool) []tableLine {
	var out []tableLine
	for i := range e {
		switch {
		case i >= o:
			out = append(out, tableLine{-1, i})
		case i < n:
			out = append(out, tableLine{i, i})
		case changedIn(i):
			out = append(out, tableLine{-1, i})
		}
	}
	for i := o; i < n; i++ {
		out = append(out, tableLine{i, -1})
	}
	return out
}

func cellAt(rows [][]string, r, c int) (string, bool) {
	if r < 0 || r >= len(rows) || c < 0 || c >= len(rows[r]) {
		return "", false
	}
	return rows[r][c], true
}

func width(rows [][]string) int {
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	return w
}
