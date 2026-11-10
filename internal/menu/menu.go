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

type Item struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Hint     string `json:"hint"`
	Icon     string `json:"icon"`
	Group    string `json:"group"`
	Keywords string `json:"keywords,omitempty"`
}

const Pages = 8

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
		{"video", locales.HostSlashVideo(), locales.HostSlashVideoHint(), "play", blocks, "video youtube vimeo bilibili dailymotion niconico movie film mp4"},
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

type Agent struct {
	ProviderID string
	Name       string
	Model      string
}

type Mentions struct {
	PageID  string
	Agents  []Agent
	Pages   []store.Page
	Folders []store.Folder
	Members []store.Member
	UserID  string
}

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
		if mb.UserID == m.UserID || !mb.Accepted && mb.Role != "owner" {
			continue
		}
		out = append(out, Item{Key: "user:" + mb.UserID, Label: mb.Name(), Hint: mb.Role, Icon: "user", Group: locales.HostGroupPeople()})
	}
	return out
}

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

func Filter(items []Item, query string) []Item { return (&Folded{items: items}).Filter(query) }

type Folded struct {
	items  []Item
	labels []string
}

func Fold(items []Item) *Folded { return &Folded{items: items} }

func fold(s string) string { return strings.ToLower(width.Fold.String(s)) }

func (f *Folded) Filter(query string) []Item {
	items := f.items
	q := fold(strings.TrimSpace(query))
	if q != "" && f.labels == nil {
		f.labels = make([]string, len(items))
		for i, it := range items {
			f.labels[i] = fold(it.Label)
		}
	}
	type hit struct{ i, rank, group, top int }
	var hits []hit
	groups := map[string]int{}
	best := map[string]int{}
	for i, it := range items {
		if it.Key == "connect" && q != "" {
			continue
		}
		label := ""
		if q != "" {
			label = f.labels[i]
		}
		r := rank(it, label, q)
		if r < 0 {
			continue
		}
		if _, ok := groups[it.Group]; !ok {
			groups[it.Group], best[it.Group] = i, r
		}
		best[it.Group] = min(best[it.Group], r)
		hits = append(hits, hit{i: i, rank: r})
	}
	for i := range hits {
		g := items[hits[i].i].Group
		hits[i].group, hits[i].top = groups[g], best[g]
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
		it := items[h.i]
		if strings.HasPrefix(it.Key, "page:") {
			if pages == Pages {
				continue
			}
			pages++
		}
		out = append(out, it)
	}
	return out
}

func rank(it Item, label, q string) int {
	if q == "" {
		return 0
	}
	words := func(s string) bool {
		start := -1
		for i, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 && strings.HasPrefix(s[start:i], q) {
				return true
			}
			start = -1
		}
		return start >= 0 && strings.HasPrefix(s[start:], q)
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

func Turn(key string) (typ string, level int, ok bool) {
	switch key {
	case "paragraph", "bulleted_list", "numbered_list", "todo_list", "quote", "code", "callout", "toggle_list":
		return key, 0, true
	case "h1", "h2", "h3":
		return "heading", int(key[1] - '0'), true
	}
	return "", 0, false
}

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

func LinkBlock(kind, link string) (*doc.Node, error) {
	link = strings.TrimSpace(link)
	if kind == "video" {
		if doc.ShortVideoLink(link) {
			return nil, errors.New(locales.DialogLinkNotFollowed())
		}
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

const EmbedStart = "<!doctype html>\n<html><body style=\"font-family: system-ui; padding: 16px\">\n  <button onclick=\"this.textContent = Number(this.textContent) + 1\">0</button>\n</body></html>"

func EmbedBlock(title, html string, height int, author string) (*doc.Node, error) {
	return doc.SpecToNode(map[string]any{"type": "embed", "embed": map[string]any{"title": title, "html": html, "height": float64(height)}}, "", author)
}
