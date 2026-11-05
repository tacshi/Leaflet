package cli

import (
	"encoding/json/v2"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"leaflet/internal/store"
)

func TestCommandName(t *testing.T) {
	for app, want := range map[string]string{"Leaflet": "leaflet", "Leaflet Debug": "leaflet-debug", "leaflet": "leaflet"} {
		if got := CommandName(app); got != want {
			t.Errorf("CommandName(%q) = %q, want %q", app, got, want)
		}
	}
}

func TestInvoked(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet"}, false},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "-psn_0_12345"}, false},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "mcp"}, true},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "ls"}, true},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "--json"}, true},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "__complete", "cat", ""}, true},
		{[]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "nope"}, false},
		// A link named leaflet, to an executable named otherwise (here
		// the test's own): the command line, with no command too.
		{[]string{"/usr/local/bin/leaflet"}, true},
		{[]string{"leaflet"}, true},
		{[]string{"/usr/local/bin/leaflet-debug"}, false},
	} {
		if got := Invoked(c.args, "leaflet"); got != c.want {
			t.Errorf("Invoked(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// pages is a data folder the app has run with.
func pages(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// run runs the command line on the pages in dir, with in piped in, and
// returns what it printed and its exit status.
func run(t *testing.T, dir, in string, args ...string) (out, errOut string, code int) {
	t.Helper()
	var o, e strings.Builder
	code = Main(args, Options{Name: "leaflet", Dir: dir, Version: "1.0.0", In: strings.NewReader(in), Out: &o, Err: &e})
	return o.String(), e.String(), code
}

// ok runs the command line, failing the test unless it works.
func ok(t *testing.T, dir, in string, args ...string) string {
	t.Helper()
	out, errOut, code := run(t, dir, in, args...)
	if code != 0 {
		t.Fatalf("leaflet %s: exit %d: %s", strings.Join(args, " "), code, errOut)
	}
	return out
}

func TestNoPages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Leaflet")
	_, errOut, code := run(t, dir, "", "ls")
	if code != 1 || !strings.Contains(errOut, "open Leaflet once first") {
		t.Errorf("ls without pages: exit %d, %q", code, errOut)
	}
	// It leaves the folder for the app to set up.
	if _, err := store.Open(dir); err != nil {
		t.Fatal(err)
	}
}

func TestPages(t *testing.T) {
	dir := pages(t)
	id := strings.TrimSpace(ok(t, dir, "The plan.\n\n- [ ] ship it\n", "new", "Roadmap"))
	if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(id) {
		t.Fatalf("new printed %q, not an ID", id)
	}
	// Its title from the heading piped in, which the page then leaves out.
	ok(t, dir, "# Standup\r\n\r\nAll well.\r\n", "new")
	ok(t, dir, "", "append", "roadmap", "Dates", "next", "week.")
	ok(t, dir, "## Risks\n\nNone yet.\n", "append", id[:6])
	md := ok(t, dir, "", "cat", "Roadmap")
	for _, want := range []string{"# Roadmap\n", "The plan.", "- [ ] ship it", "Dates next week.", "## Risks", "None yet."} {
		if !strings.Contains(md, want) {
			t.Errorf("cat lacks %q:\n%s", want, md)
		}
	}
	if md := ok(t, dir, "", "cat", "Standup"); !strings.HasPrefix(md, "# Standup\n\nAll well.") || strings.Count(md, "Standup") != 1 {
		t.Errorf("cat Standup:\n%q", md)
	}

	ok(t, dir, "", "tag", "Roadmap", "#work", "q4")
	if tags := ok(t, dir, "", "tag", "Roadmap"); tags != "work\nq4\n" {
		t.Errorf("tags %q", tags)
	}
	folder := strings.TrimSpace(ok(t, dir, "", "folder", "new", "Work"))
	ok(t, dir, "", "mv", "Roadmap", "--folder", "work")
	ok(t, dir, "", "new", "Launch", "--in", "Roadmap")
	ok(t, dir, "", "folder", "rename", folder[:8], "Projects")
	ok(t, dir, "", "rename", "Standup", "Daily standup")
	ls := ok(t, dir, "", "ls")
	want := regexp.MustCompile(`(?m)^` + folder[:8] + `  Projects/\n` + id[:8] + `    Roadmap  #work  #q4\n[0-9a-f]{8}      Launch\n[0-9a-f]{8}  Daily standup\n$`)
	if !want.MatchString(ls) {
		t.Errorf("ls:\n%s", ls)
	}
	if out := ok(t, dir, "", "search", "standup"); !strings.Contains(out, "Daily standup") {
		t.Errorf("search:\n%s", out)
	}
	ok(t, dir, "", "mv", "Roadmap", "--top")
	ok(t, dir, "", "tag", "Roadmap", "--clear")
	if ls := ok(t, dir, "", "ls"); !strings.Contains(ls, id[:8]+"  Roadmap\n") {
		t.Errorf("ls after mv --top and tag --clear:\n%s", ls)
	}

	// Two edits close together keep one snapshot, from before the first,
	// though each is a run of its own.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	h, err := st.History(id)
	if err != nil || len(h) != 1 || h[0].Label != "Before Command line" || strings.Contains(h[0].Content, "Dates") {
		t.Errorf("history %v, %v", h, err)
	}
}

func TestJSON(t *testing.T) {
	dir := pages(t)
	var made struct {
		PageID string `json:"page_id"`
	}
	if err := json.Unmarshal([]byte(ok(t, dir, "Hello.\n", "new", "Notes", "--json")), &made); err != nil || made.PageID == "" {
		t.Fatalf("new --json: %v, %+v", err, made)
	}
	var l listing
	if err := json.Unmarshal([]byte(ok(t, dir, "", "ls", "--json")), &l); err != nil || len(l.Pages) != 1 || l.Pages[0].ID != made.PageID {
		t.Errorf("ls --json: %v, %+v", err, l)
	}
	var read struct {
		Page        map[string]any   `json:"page"`
		Blocks      []map[string]any `json:"blocks"`
		TotalBlocks int              `json:"total_blocks"`
	}
	if err := json.Unmarshal([]byte(ok(t, dir, "", "--json", "cat", "Notes")), &read); err != nil || read.Page["title"] != "Notes" || len(read.Blocks) != read.TotalBlocks || read.TotalBlocks == 0 {
		t.Errorf("cat --json: %v, %+v", err, read)
	}
	if tags := ok(t, dir, "", "tag", "Notes", "--json"); !strings.Contains(tags, `"tags": []`) {
		t.Errorf("tag --json: %s", tags)
	}
}

func TestRefusals(t *testing.T) {
	dir := pages(t)
	a := strings.TrimSpace(ok(t, dir, "", "new", "Notes"))
	b := strings.TrimSpace(ok(t, dir, "", "new", "notes"))
	for _, c := range []struct {
		args []string
		in   string
		want string
	}{
		// Two titled so but for case, neither as given.
		{[]string{"tag", "NOTES"}, "", `2 pages are titled "NOTES" (` + a[:8] + ` "Notes" at the top level, ` + b[:8] + ` "notes" at the top level); name one by its ID`},
		{[]string{"cat", "Nope"}, "", `no page "Nope": leaflet ls lists them, and leaflet search finds them`},
		{[]string{"append", "Notes"}, "", "nothing to add"},
		{[]string{"new"}, "no heading\n", "give the page a title"},
		{[]string{"mv", "Notes"}, "", "at least one of the flags in the group [in folder top] is required"},
		{[]string{"mv", "Notes", "--in", "notes", "--top"}, "", "none of the others can be"},
		{[]string{"mv", "Notes", "--in", "Notes"}, "", "A page cannot move inside itself"},
		{[]string{"new", "X", "--folder", "Nowhere"}, "", `no folder "Nowhere"`},
		{[]string{"tag", "Notes", "a", "--clear"}, "", "give tags or --clear, not both"},
		{[]string{"rename", "Notes"}, "", "accepts 2 arg(s), received 1"},
	} {
		_, errOut, code := run(t, dir, c.in, c.args...)
		if code != 1 || !strings.Contains(errOut, c.want) || !strings.HasPrefix(errOut, "leaflet: ") {
			t.Errorf("%v: exit %d, %q, want %q", c.args, code, errOut, c.want)
		}
	}
	// The one titled as given.
	if out := ok(t, dir, "", "cat", "notes"); out != "# notes\n" {
		t.Errorf("cat notes: %q", out)
	}
}

func TestComplete(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "", "new", "Roadmap")
	ok(t, dir, "", "new", "Reading list")
	ok(t, dir, "", "folder", "new", "Work")
	out := ok(t, dir, "", "__complete", "cat", "r")
	if !strings.Contains(out, "Roadmap\tat the top level") || !strings.Contains(out, "Reading list\t") {
		t.Errorf("completions of cat r:\n%s", out)
	}
	if out := ok(t, dir, "", "__complete", "new", "X", "--folder", ""); !strings.Contains(out, "Work\n") {
		t.Errorf("completions of --folder:\n%s", out)
	}
}
