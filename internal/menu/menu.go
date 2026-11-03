// Package menu is what the editor's / and @ menus offer, and how what is
// typed after the "/" or "@" filters it, for the desktop and the iOS app
// alike: the / menu's blocks and templates, and the @ menu's agents, pages
// and people. Each app draws the menu and does what an item says.
package menu

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/width"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Item is a choice of the / or @ menu.
type Item struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
	// Icon is a Lucide icon's name.
	Icon string `json:"icon"`
	// Group heads a run of items, as "Agents".
	Group string `json:"group"`
	// Keywords find the item besides its label: English, to find a block
	// by its English name in any language.
	Keywords string `json:"keywords,omitempty"`
}

// Pages is how many pages the @ menu offers at once: the first that match.
const Pages = 8

// Slash is the / menu: its blocks, said in the app's language, then the
// templates, to put one's blocks at the caret. A template is found by
// "template", in the app's language too.
func Slash(templates []Item) []Item {
	basic, blocks := locales.HostGroupBasic(), locales.HostGroupBlocks()
	out := []Item{
		{"paragraph", locales.HostSlashText(), locales.HostSlashTextHint(), "type", basic, "text paragraph plain"},
		{"h1", locales.HostSlashHeading1(), locales.HostSlashHeading1Hint(), "heading-1", basic, "h1 title heading"},
		{"h2", locales.HostSlashHeading2(), locales.HostSlashHeading2Hint(), "heading-2", basic, "h2 heading subtitle"},
		{"h3", locales.HostSlashHeading3(), locales.HostSlashHeading3Hint(), "heading-3", basic, "h3 heading"},
		{"bulleted_list", locales.HostSlashBulleted(), locales.HostSlashBulletedHint(), "list", basic, "bullet list ul"},
		{"numbered_list", locales.HostSlashNumbered(), locales.HostSlashNumberedHint(), "list-ordered", basic, "number ordered ol"},
		{"todo_list", locales.HostSlashTodo(), locales.HostSlashTodoHint(), "square-check", basic, "todo task check"},
		{"quote", locales.HostSlashQuote(), locales.HostSlashQuoteHint(), "quote", basic, "quote blockquote"},
		{"callout", locales.HostSlashCallout(), locales.HostSlashCalloutHint(), "info", basic, "callout note tip warning alert admonition"},
		{"toggle_list", locales.HostSlashToggle(), locales.HostSlashToggleHint(), "chevron-right", basic, "toggle collapse fold details disclosure"},
		{"code", locales.HostSlashCode(), locales.HostSlashCodeHint(), "code", basic, "code snippet program ```"},
		{"divider", locales.HostSlashDivider(), locales.HostSlashDividerHint(), "minus", basic, "divider line hr rule"},
		{"table", locales.HostSlashTable(), locales.HostSlashTableHint(), "table", blocks, "table grid"},
		{"chart", locales.HostSlashChart(), locales.HostSlashChartHint(), "chart-column", blocks, "chart graph bar line pie"},
		{"math_equation", locales.HostSlashMath(), locales.HostSlashMathHint(), "sigma", blocks, "math equation formula latex tex katex"},
		{"diagram", locales.HostSlashDiagram(), locales.HostSlashDiagramHint(), "workflow", blocks, "diagram mermaid flowchart sequence chart graph"},
		{"embed", locales.HostSlashEmbed(), locales.HostSlashEmbedHint(), "layout-grid", blocks, "embed html widget"},
		{"subpage", locales.HostSlashSubpage(), locales.HostSlashSubpageHint(), "file-plus", blocks, "page subpage child"},
		{"image", locales.HostSlashImage(), locales.HostSlashImageHint(), "image", blocks, "image picture photo"},
		{"video", locales.HostSlashVideo(), locales.HostSlashVideoHint(), "play", blocks, "video youtube vimeo movie film mp4"},
		{"link_preview", locales.HostSlashLinkCard(), locales.HostSlashLinkCardHint(), "globe", blocks, "link card bookmark preview url web"},
		{"file", locales.HostSlashFile(), locales.HostSlashFileHint(), "paperclip", blocks, "file attach attachment upload document pdf"},
		{"recording", locales.CommonRecording(), locales.HostSlashRecordingHint(), "audio-lines", blocks, "record audio voice transcript"},
	}
	group := locales.HostGroupTemplates()
	for _, t := range templates {
		t.Group, t.Keywords = group, "template "+strings.ToLower(group)
		out = append(out, t)
	}
	return out
}

// Agent is an agent the @ menu offers to ask at the caret.
type Agent struct {
	ProviderID string
	Name       string
	Model      string
}

// Mentions is what the @ menu offers in a page.
type Mentions struct {
	// PageID is the page the menu is in, which it doesn't offer.
	PageID string
	Agents []Agent
	// Pages are the user's pages, and Folders their folders, which say
	// where a page is.
	Pages   []store.Page
	Folders []store.Folder
	// Members are who the page is shared with, and UserID the user, whom
	// the menu leaves out.
	Members []store.Member
	UserID  string
}

// Mention is the @ menu: the agents, or a way to add one while there is
// none; the pages, most recently edited first; and the people the page is
// shared with.
func Mention(m Mentions) []Item {
	var out []Item
	for _, a := range m.Agents {
		out = append(out, Item{Key: "agent:" + a.ProviderID, Label: a.Name, Hint: a.Model, Icon: "sparkles", Group: locales.CommonAgents()})
	}
	if len(m.Agents) == 0 {
		out = append(out, Item{Key: "connect", Label: locales.HostAddAgentProvider(), Hint: locales.HostAddAgentProviderHint(), Icon: "sparkles", Group: locales.CommonAgents()})
	}
	pages := slices.Clone(m.Pages)
	slices.SortStableFunc(pages, func(x, y store.Page) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	place := m.places()
	for _, p := range pages {
		if p.ID == m.PageID || p.Template {
			continue
		}
		out = append(out, Item{Key: "page:" + p.ID, Label: p.DisplayTitle(), Hint: place(p), Icon: "file-text", Group: locales.HostGroupPages()})
	}
	for _, mb := range m.Members {
		// Someone invited who has not accepted is no account yet: their
		// ID is the invite's.
		if mb.UserID == m.UserID || !mb.Accepted && mb.Role != "owner" {
			continue
		}
		out = append(out, Item{Key: "user:" + mb.UserID, Label: mb.Name(), Hint: mb.Role, Icon: "user", Group: locales.HostGroupPeople()})
	}
	return out
}

// places says where a page is: in its parent page, else its folder, else
// nothing at the top of the space.
func (m Mentions) places() func(store.Page) string {
	titles := map[string]string{}
	for _, p := range m.Pages {
		titles[p.ID] = p.DisplayTitle()
	}
	folders := map[string]string{}
	for _, f := range m.Folders {
		folders[f.ID] = f.Name
	}
	return func(p store.Page) string {
		if t, ok := titles[p.ParentID]; ok {
			return t
		}
		return folders[p.FolderID]
	}
}

// Filter keeps the items what is typed after the "/" or "@" finds: those
// whose label has it, in any case, or whose keywords do. The best come
// first, for Enter to take: a label that starts with it, then one with a
// word that does, then keywords one of which does, then those that have
// it anywhere ("/h" a heading before Text, for "paragraph"). The groups
// stay whole, the one with the best first, and in each the items keep
// their order among those as good. Full-width letters and digits, as an
// input method for Chinese or Japanese types them, find what their ASCII
// does. Pages are kept to the best Pages that match, and the way to add
// an agent shows only before anything is typed.
func Filter(items []Item, query string) []Item {
	q := strings.ToLower(width.Fold.String(strings.TrimSpace(query)))
	type hit struct {
		it               Item
		rank, group, top int
	}
	var hits []hit
	groups := map[string]int{} // a group's first place
	best := map[string]int{}   // its best rank
	for i, it := range items {
		if it.Key == "connect" && q != "" {
			continue
		}
		r := rank(it, q)
		if r < 0 {
			continue
		}
		if _, ok := groups[it.Group]; !ok {
			groups[it.Group], best[it.Group] = i, r
		}
		best[it.Group] = min(best[it.Group], r)
		hits = append(hits, hit{it: it, rank: r})
	}
	for i := range hits {
		hits[i].group, hits[i].top = groups[hits[i].it.Group], best[hits[i].it.Group]
	}
	slices.SortStableFunc(hits, func(x, y hit) int {
		if c := cmp.Compare(x.top, y.top); c != 0 {
			return c
		}
		if c := cmp.Compare(x.group, y.group); c != 0 {
			return c
		}
		return cmp.Compare(x.rank, y.rank)
	})
	out := []Item{}
	pages := 0
	for _, h := range hits {
		if strings.HasPrefix(h.it.Key, "page:") {
			if pages == Pages {
				continue
			}
			pages++
		}
		out = append(out, h.it)
	}
	return out
}

// rank is how well an item matches q, lowered: 0 best, -1 not at all.
func rank(it Item, q string) int {
	if q == "" {
		return 0
	}
	label := strings.ToLower(width.Fold.String(it.Label))
	words := func(s string) bool {
		for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if strings.HasPrefix(w, q) {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(label, q):
		return 0
	case words(label):
		return 1
	case words(it.Keywords):
		return 2
	case strings.Contains(label, q):
		return 3
	case strings.Contains(it.Keywords, q):
		return 4
	}
	return -1
}

// Turn is the kind of text block a / menu key makes of the block at the
// caret, with a heading's level, or ok false for a key that does
// something else.
func Turn(key string) (typ string, level int, ok bool) {
	switch key {
	case "paragraph", "bulleted_list", "numbered_list", "todo_list", "quote", "code", "callout", "toggle_list":
		return key, 0, true
	case "h1", "h2", "h3":
		return "heading", int(key[1] - '0'), true
	}
	return "", 0, false
}

// Block is the block a / menu key puts at the caret, ready to show: a
// divider, a table, a chart that author made, or an empty formula or
// diagram, which opens to be written. ok is false for other keys.
func Block(key, author string) (n *doc.Node, ok bool) {
	switch key {
	case "divider":
		return &doc.Node{ID: doc.NewBlockID(), Type: "divider"}, true
	case "table":
		n, _ := doc.TableNode([][]string{{locales.HostTableColumn(1), locales.HostTableColumn(2), locales.HostTableColumn(3)}, {"", "", ""}, {"", "", ""}}, "")
		return n, true
	case "chart":
		data, _ := doc.ChartData(map[string]any{
			"chart_type": "bar", "title": locales.HostChartTitle(), "labels": []any{"A", "B", "C"},
			"series": []any{map[string]any{"name": locales.HostChartSeries(), "values": []any{3.0, 5.0, 2.0}}},
		})
		data["author"] = author
		return &doc.Node{ID: doc.NewBlockID(), Type: "chart", Data: data}, true
	case "math_equation":
		return doc.MathNode(""), true
	case "diagram":
		return doc.DiagramNode(""), true
	}
	return nil, false
}

// LinkBlock is the block of kind "image", "video" or "link_preview" the /
// menu puts at the caret for link, the link checked as the kind needs: a
// web link, a video's of a site Leaflet plays. What is wrong with it, the
// error says in the app's language.
func LinkBlock(kind, link string) (*doc.Node, error) {
	link = strings.TrimSpace(link)
	if kind == "video" {
		if _, ok := doc.ParseVideoURL(link); !ok {
			return nil, errors.New(locales.DialogLinkNotVideo())
		}
		return doc.VideoNode(link, "")
	}
	if !doc.WebURL(link) {
		return nil, errors.New(locales.DialogLinkNotWeb())
	}
	if kind == "image" {
		return doc.ImageNode(link)
	}
	return doc.LinkNode(link)
}

// EmbedStart is the page a new embed starts from, to change.
const EmbedStart = "<!doctype html>\n<html><body style=\"font-family: system-ui; padding: 16px\">\n  <button onclick=\"this.textContent = Number(this.textContent) + 1\">0</button>\n</body></html>"

// EmbedBlock is an embed of html, as tall as height, that author made.
func EmbedBlock(title, html string, height int, author string) (*doc.Node, error) {
	return doc.SpecToNode(map[string]any{"type": "embed", "embed": map[string]any{"title": title, "html": html, "height": float64(height)}}, "", author)
}
