package mobile

import (
	"encoding/json"

	"leaflet/internal/doc"
)

// outlineRow is a heading in a page's outline, as Outline returns it: its
// block, text and level, how deep it nests, the heading it is in, and
// whether any are in it.
type outlineRow struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Level       int    `json:"level"`
	Depth       int    `json:"depth"`
	Parent      string `json:"parent,omitempty"`
	HasChildren bool   `json:"has_children"`
}

// Outline returns the outline of a page's headings, given as a JSON array
// of their blocks in their order on the page, as the desktop's outline
// nests them (doc.Outline): each in the nearest heading before it of a
// higher level. It returns a JSON array of outlineRow, in the same order.
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
