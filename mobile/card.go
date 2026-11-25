package mobile

import (
	"encoding/json"
	"fmt"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
)

type cardFactView struct {
	Label     string `json:"label"`
	Separator string `json:"separator"`
	Value     []any  `json:"value"`
}

type cardSourceView struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Site string `json:"site"`
}

type cardView struct {
	Layout  string           `json:"layout"`
	Icon    string           `json:"icon"`
	Title   string           `json:"title"`
	Image   string           `json:"image"`
	Facts   []cardFactView   `json:"facts"`
	Note    []any            `json:"note"`
	Sources []cardSourceView `json:"sources"`
}

// CardView is the card block node as the app shows it: its facts' values
// and its note as deltas, the separator after each label, and its sources
// by name and site.
func CardView(node string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	if n.Type != "card" {
		return "", fmt.Errorf("block %s is a %s, not a card", n.ID, n.Type)
	}
	c := doc.CardOf(&n)
	v := cardView{Layout: c.Layout, Icon: c.Icon, Title: c.Title, Image: c.Image, Note: doc.FromMarkdown(c.Note).JSON(),
		Facts: []cardFactView{}, Sources: []cardSourceView{}}
	for _, f := range c.Facts {
		sep := ""
		if f.Label != "" {
			sep = doc.FactSeparator(f.Label)
		}
		v.Facts = append(v.Facts, cardFactView{Label: f.Label, Separator: sep, Value: doc.FromMarkdown(f.Value).JSON()})
	}
	for _, s := range c.Sources {
		v.Sources = append(v.Sources, cardSourceView{URL: s.URL, Name: doc.SourceName(s), Site: doc.SiteName(s.URL)})
	}
	return encode(v)
}

type cardFields struct {
	Layout  string           `json:"layout"`
	Icon    string           `json:"icon"`
	Title   string           `json:"title"`
	Image   string           `json:"image"`
	Note    string           `json:"note"`
	Facts   []doc.CardFact   `json:"facts"`
	Sources []doc.CardSource `json:"sources"`
}

// CardData is a card block's data from the card editor's fields, as JSON
// ({layout, icon, title, image, note, facts: [{label, value}], sources:
// [{url, title}]}), or an error a person can read.
func CardData(fields string) (_ string, err error) {
	defer catch(&err)
	var f cardFields
	if err := json.Unmarshal([]byte(fields), &f); err != nil {
		return "", err
	}
	data, err := menu.CardFromFields(f.Layout, f.Icon, f.Title, f.Image, f.Note, f.Facts, f.Sources)
	if err != nil {
		return "", err
	}
	return encode(data)
}

// CardIcons are the icons a card's title can carry, by Lucide's names.
func CardIcons() (_ string, err error) {
	defer catch(&err)
	return encode(doc.CardIcons)
}
