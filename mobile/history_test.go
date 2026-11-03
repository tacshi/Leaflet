package mobile

import (
	"encoding/json"
	"testing"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
)

// versions returns page id's kept versions, newest first.
func versions(t *testing.T, c *Core, id string) []version {
	t.Helper()
	raw, err := c.History(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []version
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// A version of no blocks restores as one to type in, the page open or
// not, as on the desktop; the version kept before restoring brings the
// page back, for Undo.
func TestRestoringAnEmptyVersionLeavesABlock(t *testing.T) {
	c, ev := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var err error
	c.q.do(func() { err = c.core.Store.AddHistory(page.ID, &doc.Content{}, "manual", "", "Empty") })
	if err != nil {
		t.Fatal(err)
	}
	empty := versions(t, c, page.ID)[0]
	c.q.do(func() {
		err = c.editContent(page.ID, func(content *doc.Content) bool {
			content.Children = []*doc.Node{paragraph("p", "Milk")}
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	kept, err := c.RestoreVersion(page.ID, empty.ID)
	if err != nil || kept == "" {
		t.Fatalf("restore: kept %q, %v", kept, err)
	}
	if got := c.core.Store.Content(page.ID); len(got.Children) != 1 || got.Children[0].Type != "paragraph" || texts(got) != "" {
		t.Fatalf("restored to %s", got.Encode())
	}
	if _, err := c.RestoreVersion(page.ID, kept); err != nil {
		t.Fatal(err)
	}
	if got := texts(c.core.Store.Content(page.ID)); got != "Milk" {
		t.Fatalf("undone to %q", got)
	}

	// Open, the editor is given the block.
	openEditor(t, c, page.ID)
	if _, err := c.RestoreVersion(page.ID, empty.ID); err != nil {
		t.Fatal(err)
	}
	u := wait(t, ev, "external")
	if got := doc.MustDecode(u.content); len(got.Children) != 1 || texts(got) != "" {
		t.Fatalf("the editor shows %s", u.content)
	}
	if _, err := c.RestoreVersion(page.ID, "gone"); err == nil {
		t.Error("restored a version not kept")
	}
}

// Restoring a version settles the page's last agent run: its Undo shows
// no more outside the chat, as the desktop's run bar hides, until a run
// changes the page again.
func TestRestoringAVersionSettlesTheLastRun(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var err error
	c.q.do(func() {
		s := c.core.Hub.Session(page.ID)
		s.Runs = append(s.Runs, &agents.Run{ID: "r1", Status: "done", Ops: []doc.Op{{}}})
		err = c.core.Store.AddHistory(page.ID, c.content(page.ID), "agent_run", "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestoreVersion(page.ID, versions(t, c, page.ID)[0].ID); err != nil {
		t.Fatal(err)
	}
	settled := func() bool {
		raw, err := c.AgentState(page.ID)
		if err != nil {
			t.Fatal(err)
		}
		var st agentState
		_ = json.Unmarshal([]byte(raw), &st)
		return len(st.Runs) == 1 && st.Runs[0].Settled
	}
	if !settled() {
		t.Fatal("the run still offers Undo")
	}
	host{c}.Tint(page.ID, nil)
	if settled() {
		t.Error("a run that changed the page again stays settled")
	}
}
