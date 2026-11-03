package core

import (
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A version says its name, or why it was kept.
func TestHistoryLabel(t *testing.T) {
	for h, want := range map[store.HistoryEntry]string{
		{Reason: "manual", Label: "Sent to legal"}: "Sent to legal",
		{Reason: "autosave"}:                       locales.DialogHistoryAutosave(),
		{Reason: "restore"}:                        locales.DialogHistoryBeforeRestore(),
		{Reason: "conflict"}:                       locales.DialogHistoryConflict(),
		{Reason: "agent_run"}:                      locales.DialogHistoryAgentRun(),
	} {
		if got := HistoryLabel(h); got != want {
			t.Errorf("%+v: %q, want %q", h, got, want)
		}
	}
}

// versions are the versions kept of page id for reason, newest first.
func versions(t *testing.T, f *pagesFixture, id, reason string) []store.HistoryEntry {
	t.Helper()
	all, err := f.st.History(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.HistoryEntry
	for _, h := range all {
		if h.Reason == reason {
			out = append(out, h)
		}
	}
	return out
}

// A version named keeps the page with the edits typed in it, under the
// name trimmed; a name of spaces keeps none.
func TestNameVersion(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "PRD", "", block("p1", "Goals"))
	ed := f.openPage(p.ID)
	ed.c.Find("p1").SetDelta(doc.FromMarkdown("Goals, typed"))
	f.open[p.ID].Changed()
	if err := f.NameVersion(p.ID, "  "); err != nil {
		t.Fatal(err)
	}
	if err := f.NameVersion(p.ID, " Sent to legal "); err != nil {
		t.Fatal(err)
	}
	h := versions(t, f, p.ID, "manual")
	if len(h) != 1 || h[0].Label != "Sent to legal" || doc.MustDecode(h[0].Content).PlainText() != "Goals, typed" {
		t.Errorf("named %+v", h)
	}
}

// Restoring a version keeps the page as it was first, returned for Undo
// to put back; a version of no blocks restores as one to type in, the
// page open or not.
func TestRestoreVersion(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := newPagesFixture(t)
		p := f.page(t, "PRD", "", block("p1", "Goals"))
		if err := f.st.AddHistory(p.ID, &doc.Content{}, "manual", "", "Empty"); err != nil {
			t.Fatal(err)
		}
		h, _ := f.st.History(p.ID)
		var ed *textEditor
		if open {
			ed = f.openPage(p.ID)
		}
		kept, run, err := f.RestoreVersion(p.ID, h[0].ID)
		if err != nil || kept == "" || run != "" {
			t.Fatalf("open %v: kept %q, run %q, %v", open, kept, run, err)
		}
		c := f.st.Content(p.ID)
		if len(c.Children) != 1 || c.Children[0].Type != "paragraph" || c.PlainText() != "" {
			t.Errorf("open %v: restored to %s", open, c.Encode())
		}
		if open && ed.c.Encode() != c.Encode() {
			t.Errorf("the editor shows %s", ed.c.Encode())
		}
		if _, _, err := f.RestoreVersion(p.ID, kept); err != nil {
			t.Fatal(err)
		}
		if got := f.st.Content(p.ID).PlainText(); got != "Goals" {
			t.Errorf("open %v: undone to %q", open, got)
		}
	}
}

// A version gone, or one that can't be read, puts nothing back, nor does
// a page the user can't change.
func TestRestoreVersionRefused(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "PRD", "", block("p1", "Goals"))
	if _, _, err := f.RestoreVersion(p.ID, "gone"); err == nil {
		t.Error("restored a version not kept")
	}
	if err := f.st.AddHistory(p.ID, &doc.Content{}, "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE page_history SET content = '{"document": [' WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	h, _ := f.st.History(p.ID)
	if _, _, err := f.RestoreVersion(p.ID, h[0].ID); err == nil {
		t.Error("restored a version that can't be read")
	}
	if _, err := f.st.DB().Exec(`UPDATE pages SET space_id = 'someone-elses' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.RestoreVersion(p.ID, h[0].ID); err == nil {
		t.Error("restored a page only viewed")
	}
	if got := f.st.Content(p.ID).PlainText(); got != "Goals" {
		t.Errorf("the page is %q", got)
	}
	if h := versions(t, f, p.ID, "restore"); len(h) != 0 {
		t.Errorf("kept %d versions", len(h))
	}
}
