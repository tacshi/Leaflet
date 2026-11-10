package doc

import (
	"encoding/json"
	"reflect"
	"testing"

	"leaflet/internal/golden"
)

func asTree(t *testing.T, nodes []*Node) any {
	t.Helper()
	if len(nodes) == 0 {
		return []any{}
	}
	return golden.Plain(golden.JSON(t, nodes))
}

func blocks(t *testing.T, nodes []*Node) any {
	t.Helper()
	return golden.WithoutIDs(asTree(t, nodes))
}

type bare struct {
	Type     string         `json:"type"`
	Data     map[string]any `json:"data,omitempty"`
	Children []bare         `json:"children,omitempty"`
}

func bared(nodes []*Node) []bare {
	out := []bare{}
	for _, n := range nodes {
		b := bare{Type: n.Type, Data: n.Data}
		if len(n.Children) > 0 {
			b.Children = bared(n.Children)
		}
		out = append(out, b)
	}
	return out
}

func TestMarkdownReadsAndWrites(t *testing.T) {
	var cases struct {
		Blocks []struct {
			Markdown string `json:"markdown"`
			Nodes    any    `json:"nodes"`
		} `json:"blocks"`
		Inline []struct {
			Markdown string `json:"markdown"`
		} `json:"inline"`
		Deltas []struct {
			Delta json.RawMessage `json:"delta"`
		} `json:"deltas"`
		Languages map[string]string `json:"languages"`
	}
	f := golden.Open(t, "markdown.json")
	f.Decode(&cases)
	if len(cases.Blocks) == 0 || len(cases.Inline) == 0 || len(cases.Deltas) == 0 || len(cases.Languages) == 0 {
		t.Fatal("the golden file has no cases")
	}
	for i, c := range cases.Blocks {
		if got := bared(MarkdownBlocks(c.Markdown)); !reflect.DeepEqual(golden.JSON(t, got), c.Nodes) {
			f.Mismatch(got, "blocks", i, "nodes")
		}
	}
	for i, c := range cases.Inline {
		f.Check(FromMarkdown(c.Markdown).JSON(), "inline", i, "delta")
	}
	for i, c := range cases.Deltas {
		var n Node
		if err := json.Unmarshal([]byte(`{"id":"x","type":"paragraph","data":{"delta":`+string(c.Delta)+`}}`), &n); err != nil {
			t.Fatal(err)
		}
		f.Check(n.Delta().ToMarkdown(), "deltas", i, "markdown")
	}
	for name := range cases.Languages {
		f.Check(CodeLanguageID(name), "languages", name)
	}
}

func TestInlineMarkdownReads(t *testing.T) {
	var cases []struct {
		Markdown string `json:"markdown"`
	}
	f := golden.Open(t, "inline_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(FromMarkdown(c.Markdown).JSON(), i, "delta")
	}
}

func TestDeltasWriteMarkdown(t *testing.T) {
	var cases []struct {
		Delta any `json:"delta"`
	}
	f := golden.Open(t, "deltas_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		n := &Node{Type: "paragraph", Data: map[string]any{"delta": c.Delta}}
		f.Check(n.Delta().ToMarkdown(), i, "markdown")
	}
}

func TestMarkdownMakesBlocks(t *testing.T) {
	var cases []struct {
		Markdown string `json:"markdown"`
		Nodes    any    `json:"nodes"`
	}
	f := golden.Open(t, "markdown_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		nodes := MarkdownBlocks(c.Markdown)
		if got, want := blocks(t, nodes), golden.Plain(golden.WithoutIDs(c.Nodes)); !reflect.DeepEqual(got, want) {
			f.Mismatch(bared(nodes), i, "nodes")
		}
	}
}

type specCase struct {
	Spec  json.RawMessage `json:"spec"`
	Node  any             `json:"node,omitempty"`
	Error bool            `json:"error,omitempty"`
}

func TestSpecsMakeBlocks(t *testing.T) {
	var cases []struct {
		Spec  map[string]any `json:"spec"`
		Node  any            `json:"node"`
		Error bool           `json:"error"`
	}
	f := golden.Open(t, "specs.json")
	f.Decode(&cases)
	for i, c := range cases {
		n, err := SpecToNode(c.Spec, "", "")
		if err != nil {
			if !c.Error {
				f.Mismatch(specCase{Spec: f.Raw(i, "spec"), Error: true}, i)
			}
			continue
		}
		if got := bared([]*Node{n})[0]; c.Error || !reflect.DeepEqual(golden.JSON(t, got), c.Node) {
			f.Mismatch(specCase{Spec: f.Raw(i, "spec"), Node: got}, i)
		}
	}
}

func TestRandomSpecsMakeBlocks(t *testing.T) {
	var cases []struct {
		Spec  map[string]any `json:"spec"`
		Node  any            `json:"node"`
		Error bool           `json:"error"`
	}
	f := golden.Open(t, "specs_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		n, err := SpecToNode(c.Spec, "", "Claude")
		if err != nil {
			if !c.Error {
				f.Mismatch(specCase{Spec: f.Raw(i, "spec"), Error: true}, i)
			}
			continue
		}
		if got, want := blocks(t, []*Node{n}), []any{golden.Plain(golden.WithoutIDs(c.Node))}; c.Error || !reflect.DeepEqual(got, want) {
			f.Mismatch(specCase{Spec: f.Raw(i, "spec"), Node: bared([]*Node{n})[0]}, i)
		}
	}
}

func TestLinkKinds(t *testing.T) {
	type videoOf struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Hash     string `json:"hash"`
	}
	var cases []struct {
		URL     string   `json:"url"`
		Video   bool     `json:"video"`
		VideoOf *videoOf `json:"video_of"`
	}
	f := golden.Open(t, "urls_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		v, video := ParseVideoURL(c.URL)
		f.Check(WebURL(c.URL), i, "web")
		f.Check(agentURL(c.URL), i, "agent")
		f.Check(video, i, "video")
		if !video || !c.Video || c.VideoOf == nil {
			continue
		}
		o := *c.VideoOf
		captioned := v.Provider == "youtube" || v.Provider == "vimeo"
		if v.Provider != o.Provider || captioned && (v.ID != o.ID || v.Hash != o.Hash) {
			made := videoOf{Provider: v.Provider}
			if captioned {
				made.ID, made.Hash = v.ID, v.Hash
			}
			f.Mismatch(made, i, "video_of")
		}
	}
}

func views(c *Content) []map[string]any {
	if got := BlockViews(c); got != nil {
		return got
	}
	return []map[string]any{}
}

func TestBlockViews(t *testing.T) {
	var page struct {
		Content *Content `json:"content"`
		Views   []any    `json:"views"`
	}
	f := golden.Open(t, "views.json")
	f.Decode(&page)
	f.Check(page.Content.PlainText(), "plain")
	got := views(page.Content)
	if len(got) != len(page.Views) {
		f.Mismatch(got, "views")
		return
	}
	for i := range got {
		f.Check(got[i], "views", i)
	}
}

func TestRandomBlockViews(t *testing.T) {
	var cases []struct {
		Content *Content `json:"content"`
	}
	f := golden.Open(t, "views_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(c.Content.PlainText(), i, "plain")
		f.Check(views(c.Content), i, "views")
	}
}

func TestRepairedIDs(t *testing.T) {
	var cases []struct {
		Content json.RawMessage `json:"content"`
	}
	f := golden.Open(t, "ids_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		var content Content
		if err := json.Unmarshal(c.Content, &content); err != nil {
			t.Errorf("case %d, %s: %v", i, c.Content, err)
			continue
		}
		ids := []string{}
		for _, n := range content.All() {
			ids = append(ids, n.ID)
		}
		f.Check(ids, i, "ids")
	}
}

func TestRunOpsApplyAndUndo(t *testing.T) {
	var run struct {
		Before *Content `json:"before"`
		Ops    []Op     `json:"ops"`
		After  *Content `json:"after"`
	}
	f := golden.Open(t, "run_ops.json")
	f.Decode(&run)
	c := run.Before.Copy()
	for _, o := range run.Ops {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := golden.JSON(t, c.Children), golden.JSON(t, run.After.Children); !reflect.DeepEqual(got, want) {
		f.Mismatch(c, "after")
	}
	for _, o := range InvertRun(run.Ops) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := golden.JSON(t, c.Children), golden.JSON(t, run.Before.Children); !reflect.DeepEqual(got, want) {
		t.Errorf("undone\n%s\nwant\n%s", golden.Show(got), golden.Show(want))
	}
}

func TestOpsLandOnAChangedPage(t *testing.T) {
	var run struct {
		Before *Content `json:"before"`
		Ops    []Op     `json:"ops"`
		After  *Content `json:"after"`
	}
	f := golden.Open(t, "edge_ops.json")
	f.Decode(&run)
	c := run.Before.Copy()
	for _, o := range run.Ops {
		_ = c.Apply(o)
	}
	if got, want := golden.JSON(t, c.Children), golden.JSON(t, run.After.Children); !reflect.DeepEqual(got, want) {
		f.Mismatch(c, "after")
	}
}

func TestRandomOpsLand(t *testing.T) {
	var cases []struct {
		Before *Content `json:"before"`
		Ops    []Op     `json:"ops"`
		After  *Content `json:"after"`
	}
	f := golden.Open(t, "ops_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		page := c.Before.Copy()
		for _, o := range c.Ops {
			_ = page.Apply(o)
		}
		if got, want := asTree(t, page.Children), asTree(t, c.After.Children); !reflect.DeepEqual(got, want) {
			f.Mismatch(page, i, "after")
		}
	}
}
