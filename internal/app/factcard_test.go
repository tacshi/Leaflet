package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
)

func TestAnswerCardFences(t *testing.T) {
	a := &App{}
	var o markdown.Options
	o.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"card": a.chatCard}
	md := "The bus:\n\n```card\n{\"layout\": \"figures\", \"icon\": \"bus\", \"title\": \"Citybus B8\", \"facts\": [{\"label\": \"Ride\", \"value\": \"40–50 min\"}, {\"label\": \"Fare\", \"value\": \"HK$18.1\"}], \"sources\": [{\"url\": \"https://example.com/b8\", \"title\": \"Get Around HK\"}]}\n```\n\n```card\n{not a card}\n```\n"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { markdownView(c, md, &o).FontSize(13.5) })
	}, 600, 500)
	tt.Frame()
	tt.Frame()
	for _, text := range []string{"Citybus B8", "Ride", "40–50 min", "HK$18.1", "Get Around HK", "{not a card}"} {
		if _, ok := tt.Find(text); !ok {
			t.Errorf("no %q in %v", text, tt.Texts())
		}
	}
	if _, ok := tt.Find(`"layout": "figures"`); ok {
		t.Error("a card's fence shows its JSON")
	}
}

func TestCardDialogSaves(t *testing.T) {
	a := templatesApp(t)
	data, _ := doc.CardData(map[string]any{"title": "Old", "facts": []any{map[string]any{"label": "a", "value": "b"}}})
	data["author"] = "Rocky"
	ed := editor.New(doc.FromBlocks([]*doc.Node{{ID: "c", Type: "card", Data: data}}), &host{a: a})
	d := &a.dialogs
	d.openCard("c", ed)
	if d.cardTitle != "Old" || d.cardFacts != "a: b" || d.cardLayout != 1 {
		t.Fatalf("opened on %q, %q, layout %d", d.cardTitle, d.cardFacts, d.cardLayout)
	}
	d.cardTitle, d.cardLayout, d.cardIcon = "Fees", 0, "bus"
	d.cardFacts, d.cardSources = "Ride: 40 min\nFare: **HK$18.1**", "not a link"
	tt := ui.NewTester(func(c *ui.Context) { a.cardDialog(c) }, 900, 900)
	tt.Frame()
	if err := tt.Click("Save card"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.cardOpen || d.cardError != "Source 1 is not a web link." {
		t.Fatalf("a bad source: open %v, error %q", d.cardOpen, d.cardError)
	}
	d.cardSources = "Get Around HK | https://example.com/b8"
	if err := tt.Click("Save card"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	card := doc.CardOf(ed.Content.Find("c"))
	if d.cardOpen || card.Layout != doc.CardFigures || card.Icon != "bus" || card.Title != "Fees" || len(card.Facts) != 2 || len(card.Sources) != 1 {
		t.Errorf("saved %+v, open %v", card, d.cardOpen)
	}
	if ed.Content.Find("c").Str("author") != "Rocky" {
		t.Error("the card lost its author")
	}
}
