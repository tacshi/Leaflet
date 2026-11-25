package doc

import (
	"reflect"
	"strings"
	"testing"
)

func busCard() map[string]any {
	return map[string]any{
		"layout": "figures", "icon": "bus", "title": "Citybus B8 to Heung Yuen Wai",
		"facts": []any{
			map[string]any{"label": "Ride", "value": "about 40–50 min"},
			map[string]any{"label": "Adult fare", "value": "**HK$18.1**"},
		},
		"note":    "Ride time leaves out waiting and the border.",
		"sources": []any{map[string]any{"url": "https://example.com/b8", "title": "Get Around HK"}, map[string]any{"url": "https://www.citybus.com.hk/b8"}},
	}
}

func TestSpecCard(t *testing.T) {
	n, err := SpecToNode(map[string]any{"type": "card", "card": busCard()}, "", "Leaflet")
	if err != nil {
		t.Fatal(err)
	}
	c := CardOf(n)
	if c.Layout != CardFigures || c.Icon != "bus" || len(c.Facts) != 2 || c.Facts[1].Value != "**HK$18.1**" || len(c.Sources) != 2 {
		t.Errorf("card %+v", c)
	}
	if n.Str("author") != "Leaflet" {
		t.Errorf("author %q", n.Str("author"))
	}
	view := BlockViews(&Content{Children: []*Node{n}})[0]["card"].(map[string]any)
	if view["title"] != "Citybus B8 to Heung Yuen Wai" || view["layout"] != "figures" || view["author"] != nil {
		t.Errorf("view %+v", view)
	}
	if got := SourceName(c.Sources[1]); got != "citybus.com.hk" {
		t.Errorf("a source without a title is named %q", got)
	}
	if text := CardText(c); !strings.Contains(text, "Adult fare HK$18.1") || !strings.Contains(text, "leaves out waiting") {
		t.Errorf("text %q", text)
	}
	for _, bad := range []map[string]any{
		{},
		{"title": "x", "layout": "grid"},
		{"title": "x", "icon": "rocket-ship"},
		{"title": "x", "image": "javascript:alert(1)"},
		{"title": "x", "facts": "label: value"},
		{"title": "x", "sources": []any{map[string]any{"url": "ftp://example.com"}}},
		{"title": strings.Repeat("x", 201)},
	} {
		if _, err := SpecToNode(map[string]any{"type": "card", "card": bad}, "", ""); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	if _, err := SpecToNode(map[string]any{"type": "card"}, "", ""); err == nil {
		t.Error("a card with no card made")
	}
	twice := busCard()
	twice["sources"] = []any{map[string]any{"url": "https://example.com/b8", "title": "Get Around HK"}, map[string]any{"url": "https://example.com/b8", "title": "Again"}}
	n, err = SpecToNode(map[string]any{"type": "card", "card": twice}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s := CardOf(n).Sources; len(s) != 1 || s[0].Title != "Get Around HK" {
		t.Errorf("a page named twice: %+v", s)
	}
}

func TestCardFenceMakesACard(t *testing.T) {
	nodes := MarkdownBlocks("Here it is:\n\n```card\n{\"layout\": \"details\", \"title\": \"CUHK Medical Centre\", \"facts\": [{\"label\": \"地址\", \"value\": \"9 Chak Cheung St\"}, {\"label\": \"总机\", \"value\": \"**3946 6888**\"}]}\n```\n\n```card\nnot json\n```")
	if len(nodes) != 3 || nodes[1].Type != "card" || nodes[2].Type != "code" {
		t.Fatalf("nodes %+v", nodes)
	}
	c := CardOf(nodes[1])
	if c.Layout != CardDetails || c.Title != "CUHK Medical Centre" || len(c.Facts) != 2 {
		t.Errorf("card %+v", c)
	}
}

func TestCardMarkdownAndHTML(t *testing.T) {
	data, err := CardData(busCard())
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{ID: "c1", Type: "card", Data: data}
	md := PageMarkdown("", &Content{Children: []*Node{n}}, MarkdownOptions{})
	for _, want := range []string{
		"**Citybus B8 to Heung Yuen Wai**",
		"- Ride: about 40–50 min\n- Adult fare: **HK$18.1**",
		"Ride time leaves out waiting and the border. [Get Around HK](https://example.com/b8) · [citybus.com.hk](https://www.citybus.com.hk/b8)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	out := PageHTML("T", &Content{Children: []*Node{n}}, HTMLOptions{})
	for _, want := range []string{
		`class="factcard figures"`,
		`<span class="fc-label">Adult fare</span><span class="fc-value"><strong>HK$18.1</strong></span>`,
		`<a class="fc-src" href="https://example.com/b8" rel="noopener noreferrer">Get Around HK</a>`,
		`.figures .fc-value{`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("html lacks %q", want)
		}
	}
}

func TestFactSeparator(t *testing.T) {
	for label, want := range map[string]string{"地址": "：", "総機": "：", "전화": "：", "Address": ": ", "": ": ", "Price (HK$)": ": "} {
		if got := FactSeparator(label); got != want {
			t.Errorf("%q: %q, want %q", label, got, want)
		}
	}
}

func TestLoadedURL(t *testing.T) {
	data, _ := CardData(map[string]any{"title": "x", "image": "https://example.com/p.jpg"})
	for _, tc := range []struct {
		n    *Node
		want string
	}{
		{&Node{Type: "image", Data: map[string]any{"url": "https://example.com/a.png"}}, "https://example.com/a.png"},
		{&Node{Type: "card", Data: data}, "https://example.com/p.jpg"},
		{&Node{Type: "paragraph", Data: map[string]any{"url": "https://example.com/x"}}, ""},
	} {
		if got := LoadedURL(tc.n); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.n.Type, got, tc.want)
		}
	}
	if !reflect.DeepEqual(CardOf(&Node{Type: "card", Data: map[string]any{"image": "file:///etc/passwd", "icon": "nope"}}), Card{Layout: CardDetails}) {
		t.Error("a card shows a picture that is not on the web, or an icon it does not know")
	}
}
