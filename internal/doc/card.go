package doc

import (
	"html"
	"slices"
	"strings"
	"unicode/utf8"
)

// A card block shows facts at a glance: a title, with an icon, and its
// facts, each a label and a value, either large side by side (figures, as
// a trip's time and fare) or a line each with a picture beside them
// (details, as a place's address and phone numbers), then a note and the
// pages they come from.
const (
	CardFigures = "figures"
	CardDetails = "details"
)

// What a card holds at most: characters of its title (and of a source's),
// a fact's label and value, its note and its picture's address, and how
// many facts and sources.
const (
	MaxCardTitle   = 200
	MaxCardLabel   = 100
	MaxCardValue   = 400
	MaxCardNote    = 1000
	MaxCardImage   = 2048
	MaxCardFacts   = 12
	MaxCardSources = 8
)

// CardIcons are the icons a card's title can carry, by Lucide's names, which
// the iPhone and iPad app draws as SF Symbols.
var CardIcons = []string{
	"bus", "train-front", "tram-front", "car", "bike", "footprints", "plane", "ship",
	"map-pin", "clock", "calendar", "ticket", "banknote", "wallet", "phone", "hospital",
	"utensils", "bed-double", "building-2", "graduation-cap", "shopping-bag", "info", "star",
	"sun", "cloud-rain", "thermometer", "globe", "user", "package", "trending-up",
}

// CardFact is a fact of a card: its label, plain text, and its value, in
// inline Markdown.
type CardFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CardSource is a page a card's facts come from.
type CardSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// Card is what a card block holds.
type Card struct {
	Layout  string       `json:"layout"`
	Icon    string       `json:"icon,omitempty"`
	Title   string       `json:"title,omitempty"`
	Image   string       `json:"image,omitempty"`
	Facts   []CardFact   `json:"facts,omitempty"`
	Note    string       `json:"note,omitempty"`
	Sources []CardSource `json:"sources,omitempty"`
}

// CardOf reads a card block, leaving out what it cannot show: an icon it
// does not know, a picture or a source that is not a web address.
func CardOf(n *Node) Card {
	c := Card{Layout: n.Str("layout"), Icon: n.Str("icon"), Title: n.Str("title"), Image: n.Str("image"), Note: n.Str("note")}
	if c.Layout != CardFigures {
		c.Layout = CardDetails
	}
	if !slices.Contains(CardIcons, c.Icon) {
		c.Icon = ""
	}
	if !WebURL(c.Image) {
		c.Image = ""
	}
	facts, _ := n.Data["facts"].([]any)
	for _, f := range facts {
		m := asMap(f)
		label, _ := m["label"].(string)
		value, _ := m["value"].(string)
		if label != "" || value != "" {
			c.Facts = append(c.Facts, CardFact{Label: label, Value: value})
		}
	}
	sources, _ := n.Data["sources"].([]any)
	for _, s := range sources {
		m := asMap(s)
		link, _ := m["url"].(string)
		title, _ := m["title"].(string)
		if WebURL(link) {
			c.Sources = append(c.Sources, CardSource{URL: link, Title: title})
		}
	}
	return c
}

// Data is the card as a block's data.
func (c Card) Data() map[string]any {
	d := map[string]any{"layout": c.Layout}
	for k, v := range map[string]string{"icon": c.Icon, "title": c.Title, "image": c.Image, "note": c.Note} {
		if v != "" {
			d[k] = v
		}
	}
	if len(c.Facts) > 0 {
		facts := make([]any, len(c.Facts))
		for i, f := range c.Facts {
			facts[i] = map[string]any{"label": f.Label, "value": f.Value}
		}
		d["facts"] = facts
	}
	if len(c.Sources) > 0 {
		sources := make([]any, len(c.Sources))
		for i, s := range c.Sources {
			m := map[string]any{"url": s.URL}
			if s.Title != "" {
				m["title"] = s.Title
			}
			sources[i] = m
		}
		d["sources"] = sources
	}
	return d
}

// View is the card as agents read it.
func (c Card) View() map[string]any {
	v := c.Data()
	if _, ok := v["facts"]; !ok {
		v["facts"] = []any{}
	}
	return v
}

// CardData checks a card an agent or a person wrote, as a spec's card, and
// gives it as a block's data.
func CardData(spec map[string]any) (map[string]any, error) {
	if spec == nil {
		return nil, toolErr("A card block needs card: its title and facts.")
	}
	text := func(key string, most int) (string, error) {
		v, ok := spec[key]
		if !ok || v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", toolErr("card.%s must be text.", key)
		}
		if s = strings.TrimSpace(s); utf8.RuneCountInString(s) > most {
			return "", toolErr("card.%s takes at most %d characters.", key, most)
		}
		return s, nil
	}
	var c Card
	var err error
	if c.Layout, err = text("layout", 20); err != nil {
		return nil, err
	}
	switch c.Layout {
	case "":
		c.Layout = CardDetails
	case CardFigures, CardDetails:
	default:
		return nil, toolErr("card.layout must be figures or details.")
	}
	if c.Icon, err = text("icon", 40); err != nil {
		return nil, err
	}
	if c.Icon != "" && !slices.Contains(CardIcons, c.Icon) {
		return nil, toolErr("card.icon must be one of: %s.", strings.Join(CardIcons, ", "))
	}
	if c.Title, err = text("title", MaxCardTitle); err != nil {
		return nil, err
	}
	if c.Image, err = text("image", MaxCardImage); err != nil {
		return nil, err
	}
	if c.Image != "" && !WebURL(c.Image) {
		return nil, toolErr("card.image must be a picture's web link, http or https.")
	}
	if c.Note, err = text("note", MaxCardNote); err != nil {
		return nil, err
	}
	raw, ok := spec["facts"].([]any)
	if v := spec["facts"]; v != nil && !ok {
		return nil, toolErr("card.facts must be a list of {label, value}.")
	}
	if len(raw) > MaxCardFacts {
		return nil, toolErr("A card holds at most %d facts.", MaxCardFacts)
	}
	for _, f := range raw {
		m, ok := f.(map[string]any)
		if !ok {
			return nil, toolErr("card.facts must be a list of {label, value}.")
		}
		label, value := strings.TrimSpace(JSONText(m["label"])), strings.TrimSpace(JSONText(m["value"]))
		if utf8.RuneCountInString(label) > MaxCardLabel || utf8.RuneCountInString(value) > MaxCardValue {
			return nil, toolErr("A card's fact takes a label of at most %d characters and a value of at most %d.", MaxCardLabel, MaxCardValue)
		}
		if label != "" || value != "" {
			c.Facts = append(c.Facts, CardFact{Label: label, Value: value})
		}
	}
	if c.Title == "" && len(c.Facts) == 0 {
		return nil, toolErr("A card needs a title or facts.")
	}
	sources, ok := spec["sources"].([]any)
	if v := spec["sources"]; v != nil && !ok {
		return nil, toolErr("card.sources must be a list of {url, title}.")
	}
	if len(sources) > MaxCardSources {
		return nil, toolErr("A card names at most %d sources.", MaxCardSources)
	}
	for _, s := range sources {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, toolErr("card.sources must be a list of {url, title}.")
		}
		link, _ := m["url"].(string)
		title, _ := m["title"].(string)
		if link = strings.TrimSpace(link); !WebURL(link) {
			return nil, toolErr("A card's source needs url, the web address of the page its facts come from.")
		}
		// A page named twice is one source: the first names it.
		if !slices.ContainsFunc(c.Sources, func(s CardSource) bool { return s.URL == link }) {
			c.Sources = append(c.Sources, CardSource{URL: link, Title: ClipText(title, MaxCardTitle)})
		}
	}
	return c.Data(), nil
}

// CardText is the card's words, as search and a plain copy read it.
func CardText(c Card) string {
	lines := []string{c.Title}
	for _, f := range c.Facts {
		lines = append(lines, strings.TrimSpace(f.Label+" "+FromMarkdown(f.Value).Plain()))
	}
	if c.Note != "" {
		lines = append(lines, FromMarkdown(c.Note).Plain())
	}
	return strings.TrimSpace(strings.Join(slices.DeleteFunc(lines, func(s string) bool { return s == "" }), "\n"))
}

// SourceName is how a source is named on its chip: its title, or its
// site's name.
func SourceName(s CardSource) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return SiteName(s.URL)
}

// ClipText is s without the spaces at its ends, cut to its first most
// characters, as a card keeps what is longer than it holds.
func ClipText(s string, most int) string {
	if s = strings.TrimSpace(s); utf8.RuneCountInString(s) <= most {
		return s
	}
	return string([]rune(s)[:most])
}

// FactSeparator is what goes between a fact's label and its value on a
// line: a full-width colon after East Asian text, as "地址：", else ": ".
func FactSeparator(label string) string {
	r, _ := utf8.DecodeLastRuneInString(strings.TrimSpace(label))
	if r >= 0x2e80 && r <= 0x9fff || r >= 0xac00 && r <= 0xd7af || r >= 0xff00 && r <= 0xffef {
		return "："
	}
	return ": "
}

// cardMarkdown is a card as Markdown reads: its title in bold, its
// picture, its facts as a list, then its note and its sources' links. A
// fact's value and the note, inline Markdown, read as a paragraph's text
// does, in a line, their links led where href leads them, and none of
// them starts a block of its own.
func cardMarkdown(c Card, link func(text, to string) string, href func(string) string) string {
	inline := func(md string) string {
		d := OneLine(FromMarkdown(md))
		for i := range d {
			if d[i].Attrs.Href != "" {
				d[i].Attrs.Href = href(d[i].Attrs.Href)
			}
		}
		return d.ReadableMarkdown()
	}
	var parts []string
	if c.Title != "" {
		parts = append(parts, "**"+escapeCommonMark(c.Title)+"**")
	}
	if c.Image != "" {
		parts = append(parts, "!"+link(c.Title, c.Image))
	}
	var facts []string
	for _, f := range c.Facts {
		text := inline(f.Value)
		if f.Label != "" {
			text = strings.TrimRight(escapeCommonMark(f.Label)+FactSeparator(f.Label)+text, " ")
		}
		if text = EscapeBlockStart(text); IsRule("- " + text) {
			text = `\` + text
		}
		facts = append(facts, "- "+text)
	}
	if len(facts) > 0 {
		parts = append(parts, strings.Join(facts, "\n"))
	}
	var tail []string
	if c.Note != "" {
		tail = append(tail, EscapeBlockStart(inline(c.Note)))
	}
	var links []string
	for _, s := range c.Sources {
		links = append(links, link(SourceName(s), s.URL))
	}
	if len(links) > 0 {
		tail = append(tail, strings.Join(links, " · "))
	}
	if len(tail) > 0 {
		parts = append(parts, strings.Join(tail, " "))
	}
	return strings.Join(parts, "\n\n")
}

// factCard writes a card block: its head, its facts beside its picture in
// details, and its note with a chip for each source.
func (w htmlWriter) factCard(n *Node) {
	c := CardOf(n)
	w.write("<figure", w.attrs(n, "factcard "+c.Layout), ">")
	if c.Title != "" || c.Icon != "" {
		w.write(`<div class="fc-head">`)
		if c.Icon != "" {
			w.write(w.icon(c.Icon))
		}
		w.write("<strong>", html.EscapeString(c.Title), "</strong></div>")
	}
	w.write(`<div class="fc-body">`)
	if c.Image != "" && c.Layout == CardDetails {
		w.write(`<img class="fc-pic" src="`, html.EscapeString(c.Image), `" alt="`, html.EscapeString(c.Title), `"`, w.lazy(), `>`)
	}
	w.write(`<div class="fc-main"><div class="fc-facts">`)
	for _, f := range c.Facts {
		w.write(`<div class="fc-fact">`)
		switch {
		case c.Layout == CardFigures:
			w.write(`<span class="fc-label">`, html.EscapeString(f.Label), `</span><span class="fc-value">`, w.text(FromMarkdown(f.Value)), `</span>`)
		case f.Label != "":
			w.write(`<span class="fc-label">`, html.EscapeString(f.Label+FactSeparator(f.Label)), `</span>`, w.text(FromMarkdown(f.Value)))
		default:
			w.write(w.text(FromMarkdown(f.Value)))
		}
		w.write(`</div>`)
	}
	w.write(`</div>`)
	if c.Note != "" || len(c.Sources) > 0 {
		w.write(`<p class="fc-note">`, w.text(FromMarkdown(c.Note)))
		for _, s := range c.Sources {
			w.write(` <a class="fc-src" href="`, html.EscapeString(s.URL), `" rel="noopener noreferrer">`, html.EscapeString(SourceName(s)), `</a>`)
		}
		w.write(`</p>`)
	}
	w.write("</div></div></figure>\n")
}

const cardCSS = `.factcard{border:1px solid var(--line);border-radius:10px;padding:14px 16px;margin:1em 0}
.fc-head{display:flex;align-items:center;gap:8px;margin-bottom:10px}
.fc-head svg{width:18px;height:18px;flex:none;color:var(--muted)}
.fc-body{display:flex;gap:16px;align-items:flex-start}
.fc-main{flex:1;min-width:0}
.fc-pic{width:30%;max-width:220px;aspect-ratio:4/5;object-fit:cover;border-radius:12px}
.figures .fc-facts{display:flex;flex-wrap:wrap;justify-content:space-between;gap:10px 32px}
.figures .fc-fact{display:flex;flex-direction:column;gap:2px}
.figures .fc-label{color:var(--muted);font-size:.85em}
.figures .fc-value{font-size:1.6em;font-weight:700;line-height:1.25}
.details .fc-fact{margin:.35em 0}
.fc-note{color:var(--muted);font-size:.85em;margin:.8em 0 0}
.fc-src{display:inline-block;max-width:14em;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:bottom;padding:0 8px;border-radius:999px;background:var(--code);color:var(--muted);text-decoration:none}
`

// EscapeInline is plain text as inline Markdown that reads as that text,
// its marks escaped, as a card's label before its value.
func EscapeInline(s string) string { return escapeCommonMark(s) }
