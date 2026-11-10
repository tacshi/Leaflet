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
