package mobile

import (
	"encoding/json"

	"leaflet/internal/doc"
)

type outlineRow struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Level       int    `json:"level"`
	Depth       int    `json:"depth"`
	Parent      string `json:"parent,omitempty"`
	HasChildren bool   `json:"has_children"`
}

func Outline(headings string) (_ string, err error) {
	defer catch(&err)
	var heads []*doc.Node
	if err := json.Unmarshal([]byte(headings), &heads); err != nil {
		return "", err
	}
	out := make([]outlineRow, 0, len(heads))
	for i, h := range doc.Outline(heads) {
		out = append(out, outlineRow{
			ID: h.ID, Text: heads[i].Delta().Plain(), Level: h.Level, Depth: h.Depth, Parent: h.Parent, HasChildren: h.HasChildren,
		})
	}
	return encode(out)
}
