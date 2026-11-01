// Package menu is what the editor's / and @ menus offer, and how what is
// typed after the "/" or "@" filters it, for the desktop and the iOS app
// alike: the / menu's blocks and templates, and the @ menu's agents, pages
// and people. Each app draws the menu and does what an item says.
package menu

import (
	"errors"
	"slices"
	"strings"

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
	// Pages are the user's pages, and Place says where one is.
	Pages []store.Page
	Place func(store.Page) string
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
	for _, p := range pages {
		if p.ID == m.PageID || p.Template {
			continue
		}
		place := ""
		if m.Place != nil {
			place = m.Place(p)
		}
		out = append(out, Item{Key: "page:" + p.ID, Label: p.DisplayTitle(), Hint: place, Icon: "file-text", Group: locales.HostGroupPages()})
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

// Filter keeps the items what is typed after the "/" or "@" finds: those
// whose label has it, in any case, or whose keywords do. Pages are kept
// to the first Pages that match, and the way to add an agent shows only
// before anything is typed.
func Filter(items []Item, query string) []Item {
	q := strings.ToLower(strings.TrimSpace(query))
	out := []Item{}
	pages := 0
	for _, it := range items {
		switch {
		case it.Key == "connect" && q != "":
			continue
		case q != "" && !strings.Contains(strings.ToLower(it.Label), q) && !strings.Contains(it.Keywords, q):
			continue
		case strings.HasPrefix(it.Key, "page:"):
			if pages == Pages {
				continue
			}
			pages++
		}
		out = append(out, it)
	}
	return out
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
