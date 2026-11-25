package menu

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// Card limits, as doc.CardData keeps to them: what a person types past
// them is cut, rather than refused in words written for agents.
const (
	cardTitleMost = 200
	cardLabelMost = 100
	cardValueMost = 400
	cardNoteMost  = 1000
	cardFactsMost = 12
	cardLinksMost = 8
)

// CardFromText makes a card's data from the card dialog's fields: facts
// a line each, as "Label: Value" or "地址：…", and sources a line each, a
// link or "Title | link".
func CardFromText(layout, icon, title, image, facts, note, sources string) (map[string]any, error) {
	var fs []doc.CardFact
	for _, line := range strings.Split(facts, "\n") {
		if strings.TrimSpace(line) != "" {
			label, value := SplitFact(line)
			fs = append(fs, doc.CardFact{Label: label, Value: value})
		}
	}
	var ss []doc.CardSource
	for _, line := range strings.Split(sources, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, link := "", strings.TrimSpace(line)
		if t, l, ok := strings.Cut(line, "|"); ok {
			name, link = strings.TrimSpace(t), strings.TrimSpace(l)
		}
		ss = append(ss, doc.CardSource{URL: link, Title: name})
	}
	return CardFromFields(layout, icon, title, image, note, fs, ss)
}

// CardFromFields makes a card's data from what a person wrote for it,
// cutting what is longer than a card keeps, with an error they can read.
// Facts and sources left empty are left out.
func CardFromFields(layout, icon, title, image, note string, facts []doc.CardFact, sources []doc.CardSource) (map[string]any, error) {
	spec := map[string]any{
		"layout": layout, "icon": icon, "title": clip(title, cardTitleMost),
		"image": strings.TrimSpace(image), "note": clip(note, cardNoteMost),
	}
	if img := strings.TrimSpace(image); img != "" && !doc.WebURL(img) {
		return nil, errors.New(locales.DialogCardBadPicture())
	}
	var list []any
	for _, f := range facts {
		if strings.TrimSpace(f.Label) != "" || strings.TrimSpace(f.Value) != "" {
			list = append(list, map[string]any{"label": clip(f.Label, cardLabelMost), "value": clip(f.Value, cardValueMost)})
		}
	}
	if len(list) > cardFactsMost {
		return nil, errors.New(locales.DialogCardTooManyFacts(cardFactsMost))
	}
	spec["facts"] = list
	var links []any
	for _, s := range sources {
		link := strings.TrimSpace(s.URL)
		if link == "" && strings.TrimSpace(s.Title) == "" {
			continue
		}
		if !doc.WebURL(link) {
			return nil, errors.New(locales.DialogCardBadSource(len(links) + 1))
		}
		links = append(links, map[string]any{"url": link, "title": clip(s.Title, cardTitleMost)})
	}
	if len(links) > cardLinksMost {
		links = links[:cardLinksMost]
	}
	spec["sources"] = links
	if strings.TrimSpace(title) == "" && len(list) == 0 {
		return nil, errors.New(locales.DialogCardEmpty())
	}
	return doc.CardData(spec)
}

// SplitFact parts a line of facts into its label and its value at its
// first colon: a full-width one, one before a space, or one not between
// two digits, so that "Open: 8:30" is Open and 8:30 and "8:30" is a value
// alone.
func SplitFact(line string) (label, value string) {
	runes := []rune(line)
	for i, r := range runes {
		if r != ':' && r != '：' {
			continue
		}
		digits := i > 0 && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1])
		if r == '：' || !digits {
			return strings.TrimSpace(string(runes[:i])), strings.TrimSpace(string(runes[i+1:]))
		}
	}
	return "", strings.TrimSpace(line)
}

// CardFactsText is a card's facts as the dialog shows them, a line each.
func CardFactsText(c doc.Card) string {
	lines := make([]string, len(c.Facts))
	for i, f := range c.Facts {
		lines[i] = f.Value
		if f.Label != "" {
			lines[i] = f.Label + doc.FactSeparator(f.Label) + f.Value
		}
	}
	return strings.Join(lines, "\n")
}

// CardSourcesText is a card's sources as the dialog shows them, a line
// each.
func CardSourcesText(c doc.Card) string {
	lines := make([]string, len(c.Sources))
	for i, s := range c.Sources {
		lines[i] = s.URL
		if s.Title != "" {
			lines[i] = s.Title + " | " + s.URL
		}
	}
	return strings.Join(lines, "\n")
}

func clip(s string, most int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= most {
		return s
	}
	return string([]rune(s)[:most])
}
