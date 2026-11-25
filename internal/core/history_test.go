package core

import (
	"testing"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

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

// The snapshots from before an agent's edits are named in the app's language
// as they show, the agent and its request kept as they are, as are those
// earlier versions kept in English.
func TestAgentHistoryLabelsInTheAppsLanguage(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	for label, want := range map[string]string{
		agents.RunLabel("Claude Code", ""):           "Claude Code 编辑前",
		agents.RunLabel(agents.CommandLine, ""):      "命令行 编辑前",
		agents.RunLabel("", ""):                      "智能体 编辑前",
		agents.RunLabel("Claude", "Tidy: the notes"): "Claude 运行前：Tidy: the notes",
		"Before Codex":                               "Codex 编辑前",
		"Before Command line":                        "命令行 编辑前",
		"Before Agent":                               "智能体 编辑前",
		"Before Claude: tidy the notes":              "Claude 运行前：tidy the notes",
		"":                                           "智能体运行前",
	} {
		if got := HistoryLabel(store.HistoryEntry{Reason: "agent_run", Label: label}); got != want {
			t.Errorf("%q: %q, want %q", label, got, want)
		}
	}
	if got := HistoryLabel(store.HistoryEntry{Reason: "autosave", Label: "Autosave"}); got != "自动保存" {
		t.Errorf("an autosave: %q", got)
	}
	if got := HistoryLabel(store.HistoryEntry{Reason: "manual", Label: "Before launch"}); got != "Before launch" {
		t.Errorf("a version the user named: %q", got)
	}
}

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

func TestHistoryWithEditsNotSaved(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "PRD", "", block("p1", "Goals"))
	if err := f.st.AddHistory(p.ID, &doc.Content{}, "manual", "", "Empty"); err != nil {
		t.Fatal(err)
	}
	h, _ := f.st.History(p.ID)
	ed := f.openPage(p.ID)
	ed.c.Find("p1").SetDelta(doc.FromMarkdown("Goals, typed"))
	f.open[p.ID].Changed()
	f.fail(t)
	if err := f.NameVersion(p.ID, "Sent to legal"); err == nil || err.Error() != locales.AppErrorNotSaved() {
		t.Errorf("named with %v", err)
	}
	if _, _, err := f.RestoreVersion(p.ID, h[0].ID); err == nil || err.Error() != locales.AppErrorNotSaved() {
		t.Errorf("restored with %v", err)
	}
	if got := len(versions(t, f, p.ID, "manual")) + len(versions(t, f, p.ID, "restore")); got != 1 {
		t.Errorf("kept %d versions more", got-1)
	}
	if ed.c.PlainText() != "Goals, typed" || ed.replaced != 0 {
		t.Errorf("the editor shows %q, %d outside changes", ed.c.PlainText(), ed.replaced)
	}
}

func TestRestoreVersionNotSaved(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "PRD", "", block("p1", "Goals"))
	if err := f.st.AddHistory(p.ID, doc.FromBlocks([]*doc.Node{block("p1", "Old goals")}), "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	h, _ := f.st.History(p.ID)
	ed := f.openPage(p.ID)
	f.fail(t)
	if _, _, err := f.RestoreVersion(p.ID, h[0].ID); err == nil || err.Error() != locales.AppErrorNotSaved() {
		t.Errorf("restored with %v", err)
	}
	if ed.c.PlainText() != "Old goals" || f.st.Content(p.ID).PlainText() != "Goals" {
		t.Errorf("the editor shows %q, stored %q", ed.c.PlainText(), f.st.Content(p.ID).PlainText())
	}
}
