package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// activity is an agent's last change, as kept.
func activity(t *testing.T, st *store.Store, agent string) Activity {
	t.Helper()
	raw, ok := Activities(st)[agent]
	list := ParseActivities(raw)
	if !ok || len(list) == 0 {
		t.Fatalf("no activity of %s in %v", agent, Activities(st))
	}
	return list[len(list)-1]
}

func TestEditsKeepTheirActivity(t *testing.T) {
	st := newStore(t)
	para := func(id, text string, children ...*doc.Node) *doc.Node {
		n := &doc.Node{ID: id, Type: "bulleted_list", Children: children}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: doc.FromBlocks([]*doc.Node{
		para("a", "one", para("a1", "one, nested")), para("b", "two"), para("c", "three"),
	})})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	s.SetAgent("Claude Code")

	if out := s.Call("read_page", map[string]any{"page_id": p.ID}); out["error"] != nil {
		t.Fatal(out)
	}
	if got := Activities(st); len(got) != 0 {
		t.Errorf("a read kept %v", got)
	}

	out := s.Call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"after": "b"}, "markdown": "- four\n- five"})
	if out["error"] != nil {
		t.Fatal(out)
	}
	a := activity(t, st, "claude code")
	inserted, _ := out["inserted_ids"].([]string)
	if a.Agent != "Claude Code" || a.PageID != p.ID || a.Tool != "write_markdown" || a.At == 0 ||
		!slices.Equal(a.Added, inserted) || len(a.Changed) != 0 || a.Removed != 0 {
		t.Errorf("after write_markdown %+v, inserted %v", a, inserted)
	}

	out = s.Call("replace_block", map[string]any{"page_id": p.ID, "id": "c", "block": map[string]any{"type": "bulleted_list", "text": "three, again"}})
	if out["error"] != nil {
		t.Fatal(out)
	}
	if a := activity(t, st, "claude code"); a.Tool != "replace_block" || len(a.Added) != 0 || !slices.Equal(a.Changed, []string{"c"}) {
		t.Errorf("after replace_block %+v", a)
	}

	if out := s.Call("move_block", map[string]any{"page_id": p.ID, "id": "c", "position": map[string]any{"before": "a"}}); out["error"] != nil {
		t.Fatal(out)
	}
	if a := activity(t, st, "claude code"); a.Tool != "move_block" || !slices.Equal(a.Changed, []string{"c"}) {
		t.Errorf("after move_block %+v", a)
	}

	if out := s.Call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{"a", "b"}}); out["error"] != nil {
		t.Fatal(out)
	}
	// The page reads c, a (with a1 in it), b, four, five, after the move.
	if a := activity(t, st, "claude code"); a.Tool != "delete_blocks" || a.Removed != 2 || len(a.Added)+len(a.Changed) != 0 || a.Near != inserted[0] {
		t.Errorf("after delete_blocks, of a block with one in it and another %+v, near %s", a, inserted[0])
	}
	if out := s.Call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{inserted[0], inserted[1]}}); out["error"] != nil {
		t.Fatal(out)
	}
	if a := activity(t, st, "claude code"); a.Removed != 2 || a.Near != "c" {
		t.Errorf("after deleting the last blocks %+v, near c before them", a)
	}
}

func TestEachAgentKeepsItsActivity(t *testing.T) {
	st := newStore(t)
	codex := NewServer(st, "test")
	codex.SetAgent("Codex")
	out := codex.Call("create_page", map[string]any{"title": "Notes", "markdown": "# Notes\nFirst line."})
	id, _ := out["page_id"].(string)
	if id == "" {
		t.Fatal(out)
	}
	var blocks []string
	st.Content(id).Walk(func(n, _ *doc.Node) { blocks = append(blocks, n.ID) })
	if a := activity(t, st, "codex"); a.Tool != "create_page" || a.PageID != id || !slices.Equal(a.Added, blocks) {
		t.Errorf("after create_page %+v, the page's blocks %v", a, blocks)
	}

	cli := NewServer(st, "test")
	cli.SetAgent("Command line")
	if out := cli.Call("write_markdown", map[string]any{"page_id": id, "position": map[string]any{"at": "end"}, "markdown": "More."}); out["error"] != nil {
		t.Fatal(out)
	}
	if a := activity(t, st, "codex"); a.Tool != "create_page" {
		t.Errorf("another agent's edit took Codex's activity: %+v", a)
	}
	if a := activity(t, st, "command line"); a.Tool != "write_markdown" || len(a.Added) != 1 {
		t.Errorf("the command line's activity %+v", a)
	}
}

func TestActivityKeepsAQuickRunOfChanges(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Log"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	s.SetAgent("Codex")
	old := Activity{Agent: "Codex", PageID: p.ID, Tool: "write_markdown", At: doc.NowMs() - 2*activityAge.Milliseconds()}
	raw, _ := json.Marshal([]Activity{old})
	if err := st.SetValue(ActivityPrefix+"codex", string(raw)); err != nil {
		t.Fatal(err)
	}
	for i := range keptActivity + 3 {
		if out := s.Call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "markdown": fmt.Sprint("line ", i)}); out["error"] != nil {
			t.Fatal(out)
		}
	}
	list := ParseActivities(Activities(st)["codex"])
	if len(list) != keptActivity || list[0].At == old.At {
		t.Fatalf("kept %d, the first at %d, the old one at %d", len(list), list[0].At, old.At)
	}
	var last string
	st.Content(p.ID).Walk(func(n, _ *doc.Node) { last = n.ID })
	if got := list[len(list)-1].Added; !slices.Equal(got, []string{last}) {
		t.Errorf("the last change added %v, the page ending in %s", got, last)
	}
	for i := 1; i < len(list); i++ {
		if list[i].At <= list[i-1].At {
			t.Errorf("not after the one before: %d, then %d", list[i-1].At, list[i].At)
		}
	}
}

func TestActivityCountsATableAsOneBlock(t *testing.T) {
	before, err := doc.TableNode([][]string{{"Phase", "Owner"}, {"Editor", "Ana"}}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	after := before.Clone()
	doc.TableCells(after)[1][1].Children[0].SetDelta(doc.Delta{{Text: "Bo"}})
	table, err := doc.TableNode([][]string{{"A"}}, "t2")
	if err != nil {
		t.Fatal(err)
	}
	added, changed, removed := blockChanges(doc.FromBlocks([]*doc.Node{before}), doc.FromBlocks([]*doc.Node{after, table}), nil)
	if !slices.Equal(added, []string{"t2"}) || !slices.Equal(changed, []string{"t1"}) || removed != 0 {
		t.Errorf("added %v, changed %v, removed %d", added, changed, removed)
	}
}
