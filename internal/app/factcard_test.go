package app

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/netimage"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/menu"
	"leaflet/locales"
)

func TestAnswerCardFences(t *testing.T) {
	a := &App{}
	var o markdown.Options
	o.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"card": a.chatCard(nil, true)}
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

// A dialog saves nothing in a page that can no longer be changed, as once
// sync takes away the right to edit it while the dialog is open, and says
// so, the dialog open with what was written in it.
func TestDialogsSaveNothingOnceReadOnly(t *testing.T) {
	a := templatesApp(t)
	card, _ := doc.CardData(map[string]any{"title": "Old", "facts": []any{map[string]any{"label": "a", "value": "b"}}})
	chart, err := menu.ChartData("bar", "Old", "", "x,y\na,1")
	if err != nil {
		t.Fatal(err)
	}
	ed := editor.New(doc.FromBlocks([]*doc.Node{{ID: "c", Type: "card", Data: card}, {ID: "ch", Type: "chart", Data: chart}}), &host{a: a})
	d := &a.dialogs
	d.openCard("c", ed)
	d.cardTitle = "New"
	d.openChart("ch", ed)
	d.chartTitle = "New"
	ed.ReadOnly = true
	for _, save := range []string{"Save card", locales.DialogChartSave()} {
		a.toasts = nil
		tt := ui.NewTester(func(c *ui.Context) {
			if save == "Save card" {
				a.cardDialog(c)
			} else {
				a.chartDialog(c)
			}
		}, 900, 900)
		tt.Frame()
		if err := tt.Click(save); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if len(a.toasts) != 1 || a.toasts[0].text != locales.DialogPageReadOnly() {
			t.Errorf("%s: toasts %+v", save, a.toasts)
		}
	}
	if got := ed.Content.Find("c").Str("title"); got != "Old" || !d.cardOpen {
		t.Errorf("the card's title is %q, its dialog open %v", got, d.cardOpen)
	}
	if got := ed.Content.Find("ch").Str("title"); got != "Old" || !d.chartOpen {
		t.Errorf("the chart's title is %q, its dialog open %v", got, d.chartOpen)
	}
}

// A card that had no author gets none from its dialog's save.
func TestCardDialogKeepsNoAuthor(t *testing.T) {
	a := templatesApp(t)
	data, _ := doc.CardData(map[string]any{"title": "Old", "facts": []any{map[string]any{"label": "a", "value": "b"}}})
	ed := editor.New(doc.FromBlocks([]*doc.Node{{ID: "c", Type: "card", Data: data}}), &host{a: a})
	a.dialogs.openCard("c", ed)
	a.dialogs.cardTitle = "New"
	tt := ui.NewTester(func(c *ui.Context) { a.cardDialog(c) }, 900, 900)
	tt.Frame()
	if err := tt.Click("Save card"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	n := ed.Content.Find("c")
	if _, ok := n.Data["author"]; ok || n.Str("title") != "New" {
		t.Errorf("saved %v", n.Data)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A card in a comment, which anyone a page is shared with can write, shows
// without its picture, whose link is never fetched; an agent's answer's
// shows it.
func TestCommentCardsFetchNoPicture(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]bool{}
	netimage.SetClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		asked[r.URL.String()] = true
		return nil, errors.New("offline")
	})})
	t.Cleanup(func() { netimage.SetClient(http.DefaultClient) })
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	fence := func(picture string) string {
		return "```card\n{\"layout\": \"details\", \"title\": \"Hotel\", \"image\": \"" + picture + "\", \"facts\": [{\"label\": \"Check-in\", \"value\": \"3 pm\"}]}\n```\n"
	}
	comment, answer := "https://pictures.invalid/comment-"+stamp+".png", "https://pictures.invalid/answer-"+stamp+".png"
	a := &App{}
	var commentMD, answerMD markdown.Options
	commentMD.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"card": a.chatCard(nil, false)}
	answerMD.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"card": a.chatCard(func(*ui.Context) float32 { return 400 }, true)}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Gap(20).Children(func() {
			markdownView(c, fence(comment), &commentMD)
			markdownView(c, fence(answer), &answerMD)
		})
	}, 600, 800)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		tt.Frame()
		mu.Lock()
		done := asked[answer]
		mu.Unlock()
		if done {
			break
		}
	}
	if _, ok := tt.Find("Check-in: 3 pm"); !ok {
		t.Fatalf("no card in %v", tt.Texts())
	}
	mu.Lock()
	defer mu.Unlock()
	if !asked[answer] {
		t.Error("an answer's card did not fetch its picture")
	}
	if asked[comment] {
		t.Error("a comment's card fetched its picture")
	}
}

// Card icons go by names in the app's language, not Lucide's.
func TestCardIconsNamed(t *testing.T) {
	for _, name := range doc.CardIcons {
		if label := cardIconLabel(name); label == "" || label == name {
			t.Errorf("icon %q is named %q", name, label)
		}
	}
	if got := cardIconLabel("bed-double"); got != "Bed" {
		t.Errorf("bed-double is named %q", got)
	}
	a := templatesApp(t)
	data, _ := doc.CardData(map[string]any{"title": "Stay", "facts": []any{map[string]any{"label": "a", "value": "b"}}})
	ed := editor.New(doc.FromBlocks([]*doc.Node{{ID: "c", Type: "card", Data: data}}), &host{a: a})
	a.dialogs.openCard("c", ed)
	tt := ui.NewTester(func(c *ui.Context) { a.cardDialog(c) }, 900, 900)
	tt.Frame()
	if err := tt.Click("No icon"); err != nil {
		t.Fatal(err)
	}
	items := tt.Menu()
	if !slices.Contains(items, "Graduation cap") || slices.Contains(items, "graduation-cap") {
		t.Fatalf("the icon menu holds %v", items)
	}
	if err := tt.ChooseMenuItem("Bed"); err != nil {
		t.Fatal(err)
	}
	if a.dialogs.cardIcon != "bed-double" {
		t.Errorf("chose %q", a.dialogs.cardIcon)
	}
}

// A card's source chip tells screen readers the names of its sources.
func TestSourceChipLabel(t *testing.T) {
	sources := []doc.CardSource{{URL: "https://en.wikipedia.org/wiki/Bus", Title: "Wikipedia"}, {URL: "https://www.bbc.com/news", Title: "BBC"}}
	tt := ui.NewTester(func(c *ui.Context) { sourceChip(c, sources) }, 400, 100)
	tt.Frame()
	if _, ok := tt.Access("Sources: Wikipedia, BBC"); !ok {
		t.Errorf("no chip named for its sources in %+v", tt.AccessAll())
	}
}
