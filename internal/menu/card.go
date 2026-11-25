package menu

import (
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// CardFromText makes a card's data from the card dialog's fields: facts
// a line each, as "Label: Value" or "地址：…", and sources a line each, a
// link or "Title | link", the title any text before the line's last |.
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
		if i := strings.LastIndex(line, "|"); i >= 0 && !doc.WebURL(link) {
			name, link = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		ss = append(ss, doc.CardSource{URL: link, Title: name})
	}
	return CardFromFields(layout, icon, title, image, note, fs, ss)
}

// CardFromFields makes a card's data from what a person wrote for it,
// cutting what is longer than a card keeps (doc.MaxCardTitle and the
// rest) rather than refusing it in words written for agents, with an error
// they can read. Facts and sources left empty are left out.
func CardFromFields(layout, icon, title, image, note string, facts []doc.CardFact, sources []doc.CardSource) (map[string]any, error) {
	spec := map[string]any{
		"layout": layout, "icon": icon, "title": doc.ClipText(title, doc.MaxCardTitle),
		"image": strings.TrimSpace(image), "note": doc.ClipText(note, doc.MaxCardNote),
	}
	if img := strings.TrimSpace(image); img != "" && !doc.WebURL(img) {
		return nil, errors.New(locales.DialogCardBadPicture())
	} else if utf8.RuneCountInString(img) > doc.MaxCardImage {
		return nil, errors.New(locales.DialogCardLongPicture(doc.MaxCardImage))
	}
	var list []any
	for _, f := range facts {
		if strings.TrimSpace(f.Label) != "" || strings.TrimSpace(f.Value) != "" {
			list = append(list, map[string]any{"label": doc.ClipText(f.Label, doc.MaxCardLabel), "value": doc.ClipText(f.Value, doc.MaxCardValue)})
		}
	}
	if len(list) > doc.MaxCardFacts {
		return nil, errors.New(locales.DialogCardTooManyFacts(doc.MaxCardFacts))
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
		links = append(links, map[string]any{"url": link, "title": doc.ClipText(s.Title, doc.MaxCardTitle)})
	}
	if len(links) > doc.MaxCardSources {
		links = links[:doc.MaxCardSources]
	}
	spec["sources"] = links
	if strings.TrimSpace(title) == "" && len(list) == 0 {
		return nil, errors.New(locales.DialogCardEmpty())
	}
	return doc.CardData(spec)
}

// SplitFact parts a line of facts into its label and its value at the
// colon factColon finds, so that "Open: 8:30" is Open and 8:30, and
// "8:30", "https://example.com" and "Book at [the site](https://…)" are
// values alone. The label is plain text: a backslash before a colon or a
// backslash in it, as CardFactsText writes them, is taken out.
func SplitFact(line string) (label, value string) {
	i := factColon(line)
	if i < 0 {
		return "", strings.TrimSpace(line)
	}
	_, size := utf8.DecodeRuneInString(line[i:])
	return labelUnescaper.Replace(strings.TrimSpace(line[:i])), strings.TrimSpace(line[i+size:])
}

// factColon is where in line, a byte, its label ends: at its first colon,
// a full-width one or one not between two digits, that is of the line's
// text. A colon a web address's scheme ends, as https://'s, is not, nor
// one in an autolink or a tag, as <span style="color:…">, nor one a link,
// a code span, a formula or bold holds, nor one escaped, \:, all of which
// the line's text would read otherwise parted there. -1 for none.
func factColon(line string) int {
	whole := doc.FromMarkdown(line)
	inTag := -1
	for i, r := range line {
		switch {
		case i <= inTag:
			continue
		case r == '<':
			if end := strings.IndexByte(line[i:], '>'); end > 0 && i+1 < len(line) && (isASCIILetter(line[i+1]) || line[i+1] == '/') {
				inTag = i + end
			}
			continue
		case r != ':' && r != '：':
			continue
		}
		rest := line[i+utf8.RuneLen(r):]
		before, _ := utf8.DecodeLastRuneInString(line[:i])
		after, _ := utf8.DecodeRuneInString(rest)
		if r == ':' && (unicode.IsDigit(before) && unicode.IsDigit(after) || strings.HasPrefix(rest, "//")) {
			continue
		}
		parted := doc.FromMarkdown(line[:i]).Concat(doc.Delta{{Text: string(r)}}).Concat(doc.FromMarkdown(rest))
		if reflect.DeepEqual(parted, whole) {
			return i
		}
	}
	return -1
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// labelEscaper and labelUnescaper write a label's colons and backslashes
// with a backslash before them, and read them back, for a label with a
// colon to end at the colon after it.
var (
	labelEscaper   = strings.NewReplacer(`\`, `\\`, ":", `\:`, "：", `\：`)
	labelUnescaper = strings.NewReplacer(`\\`, `\`, `\:`, ":", `\：`, "：")
)

// CardFactsText is a card's facts as the dialog shows them, a line each,
// that CardFromText reads back as they are: a label with a colon or a
// backslash in it written escaped, and a value without a label that would
// be parted at a colon of its own after an empty label, ": mailto:…".
func CardFactsText(c doc.Card) string {
	lines := make([]string, len(c.Facts))
	for i, f := range c.Facts {
		label := f.Label
		if strings.ContainsAny(label, `\:：`) {
			label = labelEscaper.Replace(label)
		}
		switch {
		case f.Label != "":
			lines[i] = label + doc.FactSeparator(f.Label) + f.Value
		case factColon(f.Value) >= 0:
			lines[i] = doc.FactSeparator("") + f.Value
		default:
			lines[i] = f.Value
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
