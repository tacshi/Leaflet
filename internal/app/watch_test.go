package app

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/mcp"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func watchApp(t *testing.T) (*App, *mcp.Server, store.Page) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	para := func(id, text string) *doc.Node {
		n := &doc.Node{ID: id, Type: "paragraph"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: doc.FromBlocks([]*doc.Node{para("p1", "one"), para("p2", "two")})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session(), settings: st.Settings()}
	a.reloadAll()
	a.startWatch()
	s := mcp.NewServer(st, "test")
	s.SetAgent("Codex")
	return a, s, p
}

func shownIDs(a *App) []string {
	var ids []string
	a.watch.ed.Content.Walk(func(n, _ *doc.Node) { ids = append(ids, n.ID) })
	return ids
}

func TestAgentEditShowsInTheCornerBlockByBlock(t *testing.T) {
	a, s, p := watchApp(t)
	out := s.Call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"after": "p1"}, "markdown": "new one\n\nnew two"})
	added, _ := out["inserted_ids"].([]string)
	if out["error"] != nil || len(added) != 2 {
		t.Fatal(out)
	}
	if out := s.Call("replace_block", map[string]any{"page_id": p.ID, "id": "p2", "block": map[string]any{"type": "paragraph", "text": "two, rewritten"}}); out["error"] != nil {
		t.Fatal(out)
	}
	a.agentsEdited()
	w := &a.watch
	if !w.shown || w.act.PageID != p.ID || w.act.Agent != "Codex" {
		t.Fatalf("the window shows %v, for %+v", w.shown, w.act)
	}
	// The two edits came in at once: the blocks of the first show one by one,
	// and the block the last rewrote shows rewritten, lit.
	if got := shownIDs(a); !slices.Equal(got, []string{"p1", "p2"}) {
		t.Errorf("before the new blocks come in it shows %v", got)
	}
	if !slices.Equal(w.pending, added) {
		t.Errorf("to come %v, want %v", w.pending, added)
	}
	if got := w.ed.Content.Find("p2").Delta().Plain(); got != "two, rewritten" || !w.tinted["p2"] {
		t.Errorf("p2 shows %q, lit %v", got, w.tinted["p2"])
	}

	start := w.next
	w.advance(start)
	if got := shownIDs(a); !slices.Equal(got, []string{"p1", added[0], "p2"}) || !w.tinted[added[0]] || w.follow != added[0] {
		t.Errorf("after one step it shows %v, lit %v, following %q", got, w.tinted, w.follow)
	}
	if wake := w.advance(start.Add(w.step / 2)); wake <= 0 || wake > w.step {
		t.Errorf("between steps it wakes in %v, a step being %v", wake, w.step)
	}
	w.advance(start.Add(w.step))
	if got := shownIDs(a); !slices.Equal(got, []string{"p1", added[0], added[1], "p2"}) || len(w.pending) != 0 {
		t.Errorf("after two steps it shows %v, %v to come", got, w.pending)
	}
	w.advance(start.Add(w.step + watchLit))
	if len(w.tinted) != 0 || len(w.lit) != 0 {
		t.Errorf("lights still on %v", w.tinted)
	}
	if w.firstEdit() != "p2" {
		t.Errorf("Open goes to %q, the first block the last edit changed being p2", w.firstEdit())
	}

	a.openWatched()
	if a.page == nil || a.page.id != p.ID || a.page.scrollTo != "p2" || !a.page.tinted["p2"] || w.shown {
		t.Errorf("Open in Leaflet: page %v, at %q, window showing %v", a.page != nil && a.page.id == p.ID, a.page.scrollTo, w.shown)
	}
	a.watch.act.PageID = p.ID
	a.watch.shown = true
	a.watchFront()
	if w.shown {
		t.Error("the window stays as the app comes in front with its page")
	}
}

func TestActivityBeforeTheAppStartedDoesNotShow(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(st, "test")
	if out := s.Call("create_page", map[string]any{"title": "Old", "markdown": "Written before."}); out["error"] != nil {
		t.Fatal(out)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session(), settings: st.Settings()}
	a.reloadAll()
	a.startWatch()
	a.agentsEdited()
	if a.watch.shown {
		t.Error("an edit from before the app started shows")
	}

	old := mcp.Activity{Agent: "Codex", PageID: a.pages[0].ID, Tool: "write_markdown", At: time.Now().Add(-time.Hour).UnixMilli()}
	raw, _ := json.Marshal([]mcp.Activity{old})
	if err := st.SetValue(mcp.ActivityPrefix+"codex", string(raw)); err != nil {
		t.Fatal(err)
	}
	a.agentsEdited()
	if a.watch.shown {
		t.Error("an edit an hour old shows")
	}
}

func TestCornerWindowStaysAway(t *testing.T) {
	front := false
	was := appInFront
	appInFront = func(*App) bool { return front }
	defer func() { appInFront = was }()
	write := func(s *mcp.Server, pageID string) {
		t.Helper()
		if out := s.Call("write_markdown", map[string]any{"page_id": pageID, "position": map[string]any{"at": "end"}, "markdown": "more"}); out["error"] != nil {
			t.Fatal(out)
		}
	}

	t.Run("turned off", func(t *testing.T) {
		a, s, p := watchApp(t)
		a.settings.LiveAgentEdits = false
		write(s, p.ID)
		a.agentsEdited()
		if a.watch.shown {
			t.Error("shows turned off")
		}
	})

	t.Run("page in front", func(t *testing.T) {
		a, s, p := watchApp(t)
		a.openPage(p.ID)
		front = true
		defer func() { front = false }()
		write(s, p.ID)
		a.outsideChange()
		if a.watch.shown {
			t.Error("shows the page in front")
		}
		list := mcp.ParseActivities(mcp.Activities(a.st)["codex"])
		act := list[len(list)-1]
		if len(act.Added) != 1 || !a.page.tinted[act.Added[0]] || a.page.editor.Content.Find(act.Added[0]) == nil {
			t.Errorf("the page in front doesn't light %v up: %v", act.Added, a.page.tinted)
		}
	})

	t.Run("closed", func(t *testing.T) {
		a, s, p := watchApp(t)
		write(s, p.ID)
		a.agentsEdited()
		a.hushWatch()
		write(s, p.ID)
		a.agentsEdited()
		if a.watch.shown {
			t.Error("closed, it shows again while the agent goes on")
		}
		a.watch.at = time.Now().Add(-watchHush)
		write(s, p.ID)
		a.agentsEdited()
		if !a.watch.shown || a.watch.hushed {
			t.Error("closed, it doesn't show once the agent paused and edits again")
		}
	})
}

func TestEditSummary(t *testing.T) {
	for _, tt := range []struct {
		act  mcp.Activity
		want string
	}{
		{mcp.Activity{Tool: "write_markdown", Added: []string{"a", "b"}, Changed: []string{"c"}}, locales.AgentEditWrote(locales.AgentEditBlocks(3))},
		{mcp.Activity{Tool: "insert_blocks", Added: []string{"a"}}, locales.AgentEditInserted(locales.AgentEditBlocks(1))},
		{mcp.Activity{Tool: "upload_files", Added: []string{"a", "b"}}, locales.AgentEditInserted(locales.AgentEditBlocks(2))},
		{mcp.Activity{Tool: "replace_block", Changed: []string{"a"}}, locales.AgentEditRewrote(locales.AgentEditBlocks(1))},
		{mcp.Activity{Tool: "move_block", Changed: []string{"a"}}, locales.AgentEditMoved()},
		{mcp.Activity{Tool: "delete_blocks", Removed: 2}, locales.AgentEditDeleted(2)},
		{mcp.Activity{Tool: "write_markdown", Removed: 1}, locales.AgentEditDeleted(1)},
		{mcp.Activity{Tool: "create_page", Added: []string{"a"}}, locales.AgentEditCreatedPage("Plan")},
	} {
		if got := editSummary(tt.act, "Plan"); got != tt.want {
			t.Errorf("%+v: %q, want %q", tt.act, got, tt.want)
		}
	}
}

func TestCornerWindowGoesWhereBlocksWereTakenOut(t *testing.T) {
	a, s, p := watchApp(t)
	if out := s.Call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{"p1"}}); out["error"] != nil {
		t.Fatal(out)
	}
	a.agentsEdited()
	w := &a.watch
	if !w.shown || w.follow != "p2" || !w.jump {
		t.Errorf("after p1 went, it shows %v, going at once %v to %q, p2 being where p1 was", w.shown, w.jump, w.follow)
	}
	if got := editSummary(w.act, "Plan"); got != locales.AgentEditDeleted(1) {
		t.Errorf("it says %q", got)
	}
}
