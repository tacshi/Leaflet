package doc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"leaflet/internal/parity"
)

// The corpora the server's tests write (supabase/functions/_shared/
// corpus_test.ts) hold random cases with what the server makes of them:
// this side must make the same of each.

// asTree is nodes as JSON reads them back, without empty data and
// children, and none as an empty list.
func asTree(t *testing.T, nodes []*Node) any {
	t.Helper()
	if len(nodes) == 0 {
		return []any{}
	}
	return parity.Plain(parity.JSON(t, nodes))
}

// blocks is asTree without IDs, which each side draws at random. The
// server's lose theirs alike (withoutIDs), a mention's among them.
func blocks(t *testing.T, nodes []*Node) any {
	t.Helper()
	return withoutIDs(asTree(t, nodes))
}

// Inline Markdown reads as the same runs of text here as on the server.
func TestCorpusInlineMarkdown(t *testing.T) {
	var cases []struct {
		Markdown string `json:"markdown"`
		Delta    any    `json:"delta"`
	}
	parity.Read(t, "inline.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := parity.JSON(t, FromMarkdown(c.Markdown).JSON()); !reflect.DeepEqual(got, c.Delta) {
			fail("%q read as\n%s\nthe server's\n%s", c.Markdown, parity.Show(got), parity.Show(c.Delta))
		}
	}
}

// Runs of text write as the same inline Markdown here as on the server.
func TestCorpusDeltaMarkdown(t *testing.T) {
	var cases []struct {
		Delta    any    `json:"delta"`
		Markdown string `json:"markdown"`
	}
	parity.Read(t, "deltas.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		n := &Node{Type: "paragraph", Data: map[string]any{"delta": c.Delta}}
		if got := n.Delta().ToMarkdown(); got != c.Markdown {
			fail("%s wrote %q, the server %q", parity.Show(c.Delta), got, c.Markdown)
		}
	}
}

// Markdown of many lines makes the blocks here it makes on the server.
func TestCorpusMarkdownBlocks(t *testing.T) {
	var cases []struct {
		Markdown string `json:"markdown"`
		Nodes    any    `json:"nodes"`
	}
	parity.Read(t, "markdown.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got, want := blocks(t, MarkdownBlocks(c.Markdown)), parity.Plain(withoutIDs(c.Nodes)); !reflect.DeepEqual(got, want) {
			fail("%q made\n%s\nthe server\n%s", c.Markdown, parity.Show(got), parity.Show(want))
		}
	}
}

// A spec makes the block here it makes on the server, or is refused on
// both.
func TestCorpusSpecs(t *testing.T) {
	var cases []struct {
		Spec  map[string]any `json:"spec"`
		Node  any            `json:"node"`
		Error bool           `json:"error"`
	}
	parity.Read(t, "specs.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		n, err := SpecToNode(c.Spec, "", "Claude")
		switch {
		case c.Error && err == nil:
			fail("%s made %s; the server refuses it", parity.Show(c.Spec), parity.Show(n))
		case c.Error:
		case err != nil:
			fail("%s: %v; the server made %s", parity.Show(c.Spec), err, parity.Show(c.Node))
		default:
			if got, want := blocks(t, []*Node{n}), []any{parity.Plain(withoutIDs(c.Node))}; !reflect.DeepEqual(got, want) {
				fail("%s made\n%s\nthe server\n%s", parity.Show(c.Spec), parity.Show(got), parity.Show(want))
			}
		}
	}
}

// A link is a web link, one a block may load, and a video's, here as on
// the server.
func TestCorpusLinks(t *testing.T) {
	var cases []struct {
		URL     string `json:"url"`
		Web     bool   `json:"web"`
		Agent   bool   `json:"agent"`
		Video   bool   `json:"video"`
		VideoOf *struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
			Hash     string `json:"hash"`
		} `json:"video_of"`
	}
	parity.Read(t, "urls.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		v, video := ParseVideoURL(c.URL)
		if web, agent := WebURL(c.URL), agentURL(c.URL); web != c.Web || agent != c.Agent || video != c.Video {
			fail("%q: web %v, agent %v, video %v; the server's %v, %v, %v", c.URL, web, agent, video, c.Web, c.Agent, c.Video)
		}
		// The server knows the ID of a video whose captions agents read.
		if !video || c.VideoOf == nil {
			continue
		}
		o := *c.VideoOf
		captioned := v.Provider == "youtube" || v.Provider == "vimeo"
		if v.Provider != o.Provider || captioned && (v.ID != o.ID || v.Hash != o.Hash) {
			fail("%q: %+v; the server's %+v", c.URL, v, o)
		}
	}
}

// A page's blocks read here as the server reads them for agents, and its
// plain text is the server's, whatever its blocks hold.
func TestCorpusViews(t *testing.T) {
	var cases []struct {
		Content *Content `json:"content"`
		Views   any      `json:"views"`
		Plain   string   `json:"plain"`
	}
	parity.Read(t, "views.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := c.Content.PlainText(); got != c.Plain {
			fail("plain text\n%q\nthe server's\n%q", got, c.Plain)
		}
		got := parity.JSON(t, BlockViews(c.Content))
		if got == nil {
			got = []any{}
		}
		if !reflect.DeepEqual(got, c.Views) {
			fail("%s read as\n%s\nthe server's\n%s", parity.Show(c.Content.Children), parity.Show(got), parity.Show(c.Views))
		}
	}
}

// Blocks stored without an ID, or with one another has, get the IDs here
// the server gives them.
func TestCorpusRepairedIDs(t *testing.T) {
	var cases []struct {
		Content json.RawMessage `json:"content"`
		IDs     []string        `json:"ids"`
	}
	parity.Read(t, "ids.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		var content Content
		if err := json.Unmarshal(c.Content, &content); err != nil {
			fail("%s: %v", c.Content, err)
			continue
		}
		var ids []string
		for _, n := range content.All() {
			ids = append(ids, n.ID)
		}
		if strings.Join(ids, ",") != strings.Join(c.IDs, ",") {
			fail("%s: IDs %q, the server's %q", c.Content, ids, c.IDs)
		}
	}
}

// Ops land here where they land on the server, on a page that may have
// changed since they were made.
func TestCorpusOps(t *testing.T) {
	var cases []struct {
		Before *Content `json:"before"`
		Ops    []Op     `json:"ops"`
		After  *Content `json:"after"`
	}
	parity.Read(t, "ops.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		page := c.Before.Copy()
		for _, o := range c.Ops {
			// An op the server passes over is refused here.
			_ = page.Apply(o)
		}
		if got, want := asTree(t, page.Children), asTree(t, c.After.Children); !reflect.DeepEqual(got, want) {
			fail("%s\non %s\nmade\n%s\nthe server\n%s", parity.Show(c.Ops), parity.Show(c.Before.Children), parity.Show(got), parity.Show(want))
		}
	}
}
