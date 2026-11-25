package menu

import (
	"strings"
	"testing"

	"leaflet/internal/doc"
)

func TestSplitFact(t *testing.T) {
	for line, want := range map[string][2]string{
		"Address: 9 Chak Cheung St": {"Address", "9 Chak Cheung St"},
		"地址：9號 Chak Cheung St":      {"地址", "9號 Chak Cheung St"},
		"Open: 8:30–17:00":          {"Open", "8:30–17:00"},
		"Time:8:30":                 {"Time", "8:30"},
		"8:30 to 17:00":             {"", "8:30 to 17:00"},
		"just a value":              {"", "just a value"},
	} {
		if label, value := SplitFact(line); label != want[0] || value != want[1] {
			t.Errorf("%q: %q, %q; want %q, %q", line, label, value, want[0], want[1])
		}
	}
}

func TestCardFromText(t *testing.T) {
	data, err := CardFromText("details", "hospital", "CUHK Medical Centre", "https://example.com/front.jpg",
		"地址：9號 Chak Cheung St\n\n总机：**3946 6888**\n", "A private teaching hospital.",
		"CUHK Medical Centre | https://www.cuhkmc.hk\nhttps://example.com/fees")
	if err != nil {
		t.Fatal(err)
	}
	c := doc.CardOf(&doc.Node{Type: "card", Data: data})
	if len(c.Facts) != 2 || c.Facts[1].Label != "总机" || c.Facts[1].Value != "**3946 6888**" || len(c.Sources) != 2 || c.Sources[0].Title != "CUHK Medical Centre" {
		t.Fatalf("card %+v", c)
	}
	if got := CardFactsText(c); got != "地址：9號 Chak Cheung St\n总机：**3946 6888**" {
		t.Errorf("facts text %q", got)
	}
	if got := CardSourcesText(c); got != "CUHK Medical Centre | https://www.cuhkmc.hk\nhttps://example.com/fees" {
		t.Errorf("sources text %q", got)
	}
	for _, tc := range []struct{ title, image, facts, sources string }{
		{"", "", "", ""},
		{"x", "ftp://example.com/a.png", "", ""},
		{"x", "", "", "not a link"},
		{"x", "", strings.Repeat("a: b\n", 13), ""},
	} {
		if _, err := CardFromText("details", "", tc.title, tc.image, tc.facts, "", tc.sources); err == nil {
			t.Errorf("%+v: no error", tc)
		}
	}
	if data, err := CardFromText("figures", "", strings.Repeat("长", 300), "", "", "", ""); err != nil || len([]rune(data["title"].(string))) != 200 {
		t.Errorf("a long title: %v, %v", err, data)
	}
}

func TestSlashCard(t *testing.T) {
	n, ok := Block("card", "Rocky")
	if !ok || n.Type != "card" || n.Str("author") != "Rocky" || len(doc.CardOf(n).Facts) != 1 {
		t.Errorf("slash card %+v", n)
	}
}
