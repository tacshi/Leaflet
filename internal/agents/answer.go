package agents

import (
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"

	"leaflet/internal/doc"
)

// unloadedCards is md with the picture of each card it shows taken out where
// the run may not load it, as its URL guard says: the app draws a ```card
// fence of an answer as its card, loading the picture by itself, and a
// picture's address could carry what the run read to whoever runs its site.
// The cards are found as each of the app's views finds them: the Markdown
// view of the Mac and PC app, and the blocks the phone's answer is made of.
func (x *Executor) unloadedCards(md string) string {
	if !x.Guarded() || !strings.Contains(md, "```") && !strings.Contains(md, "~~~") {
		return md
	}
	for range 3 {
		refused := x.refusedPictures(md)
		if len(refused) == 0 {
			return md
		}
		md = blankStrings(md, refused)
	}
	if len(x.refusedPictures(md)) == 0 {
		return md
	}
	// A picture no string of md spells out is past finding: no fence is left
	// to show a card at all.
	return unfenced.Replace(md)
}

var unfenced = strings.NewReplacer("```", "``\u200b`", "~~~", "~~\u200b~")

// refusedPictures is the pictures of the cards md shows that the run may not
// load.
func (x *Executor) refusedPictures(md string) map[string]bool {
	refused := map[string]bool{}
	check := func(n *doc.Node) {
		if pic := n.Str("image"); pic != "" && !x.mayLoad(pic) {
			refused[pic] = true
		}
	}
	d := parser.New(parser.Default)
	d.Set(md)
	for i := range d.Len() {
		if b := d.Block(i); b.Kind == parser.CodeBlock && b.Fenced && b.Language() == "card" {
			if n, ok := doc.CardNodeFromJSON(d.Lines(i)); ok {
				check(n)
			}
		}
	}
	var cards func([]*doc.Node)
	cards = func(nodes []*doc.Node) {
		for _, n := range nodes {
			if n.Type == "card" {
				check(n)
			}
			cards(n.Children)
		}
	}
	cards(doc.MarkdownBlocks(md))
	return refused
}

// blankStrings is md with each JSON string in it that spells one of values,
// space around it or not, made empty. A string of a fence's JSON is on one
// line, a newline in it being escaped, so each quote of a line may start one.
func blankStrings(md string, values map[string]bool) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(md); i++ {
		if md[i] != '"' {
			continue
		}
		end := i + 1
		for end < len(md) && md[end] != '"' && md[end] != '\n' {
			if md[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(md) || md[end] != '"' {
			continue
		}
		var s string
		if json.Unmarshal([]byte(md[i:end+1]), &s) != nil || !values[strings.TrimSpace(s)] {
			continue
		}
		b.WriteString(md[last:i])
		b.WriteString(`""`)
		last, i = end+1, end
	}
	if last == 0 {
		return md
	}
	return b.String() + md[last:]
}
