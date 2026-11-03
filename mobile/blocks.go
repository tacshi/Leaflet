package mobile

import (
	"encoding/json"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/locales"
)

// codeLanguage is a language code blocks offer, as CodeLanguages lists them.
type codeLanguage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CodeLanguages returns the languages a code block's bar offers, as the
// desktop's: a JSON array of {id, name}, plain text first.
func CodeLanguages() string {
	defer catch(nil)
	out := make([]codeLanguage, len(doc.CodeLanguages))
	for i, l := range doc.CodeLanguages {
		out[i] = codeLanguage{l.ID, l.Name}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ChartText returns a chart block's labels and series as its editor shows
// them, as the desktop's: a line per label, after a line naming the
// series. data is the block's data, as JSON.
func ChartText(data string) string {
	defer catch(nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(data), &m)
	return doc.ChartCSV(&doc.Node{Type: "chart", Data: m}, locales.DialogChartLabelColumn())
}

// ChartData returns a chart block's data, as JSON, from its editor: its
// kind (bar, line or pie), title and caption, and text as ChartText
// writes it. What is wrong with the text, it says in the app's language.
func ChartData(kind, title, caption, text string) (_ string, err error) {
	defer catch(&err)
	data, err := menu.ChartData(kind, title, caption, text)
	if err != nil {
		return "", err
	}
	return encode(data)
}

// TableRows returns a table block's cells, a JSON array of rows of their
// Markdown, as the desktop edits a cell. node is the block, as JSON.
func TableRows(node string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	return encode(doc.TableRows(&n))
}

// SetTableRows returns table block node with the cells rows, as TableRows
// gives them: what is the same keeps its cell and its text as it was.
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
