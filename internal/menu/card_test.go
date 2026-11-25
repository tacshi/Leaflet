package menu

import (
	"reflect"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/locales"
)

func TestSplitFact(t *testing.T) {
	for line, want := range map[string][2]string{
		"Address: 9 Chak Cheung St":                          {"Address", "9 Chak Cheung St"},
		"地址：9號 Chak Cheung St":                               {"地址", "9號 Chak Cheung St"},
		"Open: 8:30–17:00":                                   {"Open", "8:30–17:00"},
		"Time:8:30":                                          {"Time", "8:30"},
		"8:30 to 17:00":                                      {"", "8:30 to 17:00"},
		"just a value":                                       {"", "just a value"},
		"https://hotel.example.com":                          {"", "https://hotel.example.com"},
		"Website: https://hotel.example.com":                 {"Website", "https://hotel.example.com"},
		"Book at [the site](https://citybus.com.hk)":         {"", "Book at [the site](https://citybus.com.hk)"},
		"[Note: see](https://example.com)":                   {"", "[Note: see](https://example.com)"},
		"<https://example.com>":                              {"", "<https://example.com>"},
		`<span style="color:#ef4444">late</span>`:            {"", `<span style="color:#ef4444">late</span>`},
		`Fare: <span style="color:#ef4444">**HK$18**</span>`: {"Fare", `<span style="color:#ef4444">**HK$18**</span>`},
		"`a:b`":                   {"", "`a:b`"},
		"**Note: this**":          {"", "**Note: this**"},
		`a\:b`:                    {"", `a\:b`},
		": mailto:me@example.com": {"", "mailto:me@example.com"},
		`Time (HH\:MM): 08:30`:    {"Time (HH:MM)", "08:30"},
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

func TestCardDialogReadsBackWhatItShows(t *testing.T) {
	facts := []doc.CardFact{
		{Value: "Book at [the site](https://citybus.com.hk)"},
		{Value: "https://hotel.example.com"},
		{Value: "mailto:me@example.com"},
		{Value: "Note: a value written without a label"},
		{Value: "8:30"},
		{Label: "Fare", Value: `<span style="color:#ef4444">**HK$18**</span>`},
		{Label: "Time (HH:MM)", Value: "08:30"},
		{Label: `C:\`, Value: "a drive"},
		{Label: `a\b`, Value: "c"},
		{Label: "地址", Value: "9號 Chak Cheung St"},
	}
	sources := []doc.CardSource{
		{URL: "https://www.citybus.com.hk/b8", Title: "Route B8 | Citybus"},
		{URL: "https://example.com/fees"},
	}
	c := doc.Card{Layout: doc.CardDetails, Title: "Citybus B8", Facts: facts, Sources: sources}
	data, err := CardFromText(c.Layout, c.Icon, c.Title, c.Image, CardFactsText(c), c.Note, CardSourcesText(c))
	if err != nil {
		t.Fatalf("%v, facts shown as\n%s", err, CardFactsText(c))
	}
	back := doc.CardOf(&doc.Node{Type: "card", Data: data})
	if !reflect.DeepEqual(back.Facts, facts) || !reflect.DeepEqual(back.Sources, sources) {
		t.Errorf("read back as %q, %q\nfrom\n%s\n%s", back.Facts, back.Sources, CardFactsText(c), CardSourcesText(c))
	}
}

func TestCardDialogSaysWhyInTheAppsLanguage(t *testing.T) {
	for _, tc := range []struct {
		image   string
		sources []doc.CardSource
		want    string
	}{
		{image: "https://example.com/a\u00a0b.jpg", want: locales.DialogCardBadPicture()},
		{image: "https://:80/a.jpg", want: locales.DialogCardBadPicture()},
		{image: "https://example.com/" + strings.Repeat("a", doc.MaxCardImage) + ".jpg", want: locales.DialogCardLongPicture(doc.MaxCardImage)},
		{sources: []doc.CardSource{{URL: "https://example.com/a\u3000b"}}, want: locales.DialogCardBadSource(1)},
	} {
		if _, err := CardFromFields("details", "", "Hotel", tc.image, "", nil, tc.sources); err == nil || err.Error() != tc.want {
			t.Errorf("%q %v: %v, want %q", tc.image, tc.sources, err, tc.want)
		}
	}
}
