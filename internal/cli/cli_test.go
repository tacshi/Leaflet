package cli

import (
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"leaflet/internal/doc"
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
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPages(t *testing.T) {
	dir := pages(t)
	id := strings.TrimSpace(ok(t, dir, "The plan.\n\n- [ ] ship it\n", "new", "Roadmap", "--stdin"))
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
	if err := json.Unmarshal([]byte(ok(t, dir, "Hello.\n", "new", "Notes", "--stdin", "--json")), &made); err != nil || made.PageID == "" {
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
		// What a shell's unset variable gives is no place, nor a tag.
		{[]string{"mv", "Notes", "--in", ""}, "", "--in names no page"},
		{[]string{"mv", "Notes", "--folder", " "}, "", "--folder names no folder"},
		{[]string{"new", "X", "--in", ""}, "", "--in names no page"},
		{[]string{"new", "X", "--folder", ""}, "", "--folder names no folder"},
		{[]string{"tag", "Notes", ""}, "", "no tag given"},
		{[]string{"tag", "Notes", "#"}, "", "no tag given"},
		// A subcommand mistyped is no help asked for.
		{[]string{"folder", "nwe", "X"}, "", `unknown command "nwe" for "leaflet folder"`},
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
	// --in completes after the page or title it follows.
	for _, args := range [][]string{{"mv", "Work", "--in", "r"}, {"new", "X", "--in", "r"}, {"new", "--in", "r"}} {
		if out := ok(t, dir, "", append([]string{"__complete"}, args...)...); !strings.Contains(out, "Roadmap\t") {
			t.Errorf("completions of %v:\n%s", args, out)
		}
	}
	// A page's title goes only in the first argument.
	if out := ok(t, dir, "", "__complete", "rename", "Roadmap", "r"); strings.Contains(out, "Roadmap") {
		t.Errorf("completions of a new title:\n%s", out)
	}
}

// A value left empty changes nothing before it is refused.
func TestEmptyValuesChangeNothing(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "", "new", "Parent")
	ok(t, dir, "", "new", "Child", "--in", "Parent")
	ok(t, dir, "", "tag", "Child", "work")
	before, parent := ok(t, dir, "", "ls"), ok(t, dir, "", "cat", "Parent")
	for _, args := range [][]string{{"mv", "Child", "--in", ""}, {"mv", "Child", "--folder", ""}, {"tag", "Child", ""}, {"tag", "Child", "#"}} {
		if _, _, code := run(t, dir, "", args...); code != 1 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	if after := ok(t, dir, "", "ls"); after != before {
		t.Errorf("ls before:\n%s\nafter:\n%s", before, after)
	}
	if after := ok(t, dir, "", "cat", "Parent"); after != parent {
		t.Errorf("cat Parent before:\n%s\nafter:\n%s", parent, after)
	}
}

// utf16Text is s as Windows' tools write it: a byte order mark, then
// UTF-16 in the order given.
func utf16Text(s string, order binary.AppendByteOrder, mark ...byte) string {
	out := mark
	for _, u := range utf16.Encode([]rune(s)) {
		out = order.AppendUint16(out, u)
	}
	return string(out)
}

func TestPipedEncodings(t *testing.T) {
	want := "# Café 日本語 🍃\n\nAll well.\n"
	for name, in := range map[string]string{
		"UTF-8":        want,
		"UTF-8 marked": "\xEF\xBB\xBF" + want,
		"UTF-16 LE":    utf16Text(want, binary.LittleEndian, 0xFF, 0xFE),
		"UTF-16 BE":    utf16Text(want, binary.BigEndian, 0xFE, 0xFF),
	} {
		dir := pages(t)
		ok(t, dir, in, "new")
		if md := ok(t, dir, "", "cat", "Café 日本語 🍃"); md != "# Café 日本語 🍃\n\nAll well.\n" {
			t.Errorf("%s: cat %q", name, md)
		}
	}
}

// Text no page can sync, or JSON carry, is cleaned on its way in, for what
// is piped in and what is given alike; and Markdown lines with it stay.
func TestTextIsCleaned(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "before\x00after\ncaf\xe9\n", "new", "Bytes", "--stdin")
	ok(t, dir, "", "append", "Bytes", "given\x00text")
	ok(t, dir, "# Caf\xe9\x00 notes\n\nhello\n", "new")
	ok(t, dir, "", "rename", "Bytes", "Odd\xff title")
	ok(t, dir, "", "folder", "new", "Fold\xffer")
	ok(t, dir, "", "tag", "Odd\ufffd title", "ta\xffg")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.DB().Query(`SELECT title || tags FROM pages UNION ALL SELECT name FROM folders UNION ALL SELECT content || plain_text FROM page_contents`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.Contains(text, `\u0000`) {
			t.Errorf("kept %q", text)
		}
	}
	for _, want := range []string{"before\ufffdafter", "caf\ufffd", "given\ufffdtext"} {
		if md := ok(t, dir, "", "cat", "Odd\ufffd title"); !strings.Contains(md, want) {
			t.Errorf("cat lacks %q:\n%s", want, md)
		}
	}
	if md := ok(t, dir, "", "cat", "Caf\ufffd\ufffd notes"); !strings.HasSuffix(md, "hello\n") {
		t.Errorf("cat: %q", md)
	}
}

// A title kept before text was cleaned stops no command: it is listed,
// and its page reached by ID, to rename it.
func TestBadTitleKept(t *testing.T) {
	dir := pages(t)
	id := strings.TrimSpace(ok(t, dir, "", "new", "Good"))
	bad := strings.TrimSpace(ok(t, dir, "Hello.\n", "new", "Bad", "--stdin"))
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(bad, "Caf\xe9 notes"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if ls := ok(t, dir, "", "ls"); !strings.Contains(ls, "Caf\ufffd notes") || !strings.Contains(ls, id[:8]) {
		t.Errorf("ls:\n%s", ls)
	}
	if out := ok(t, dir, "", "ls", "--json"); !strings.Contains(out, "Caf\ufffd notes") {
		t.Errorf("ls --json:\n%s", out)
	}
	ok(t, dir, "", "append", "Good", "more")
	if md := ok(t, dir, "", "cat", bad); !strings.Contains(md, "Hello.") {
		t.Errorf("cat by ID: %q", md)
	}
	if out := ok(t, dir, "", "search", "Hello"); !strings.Contains(out, bad[:8]) {
		t.Errorf("search:\n%s", out)
	}
	if out := ok(t, dir, "", "search", "Hello", "--json"); !strings.Contains(out, "Caf� notes") {
		t.Errorf("search --json:\n%s", out)
	}
	ok(t, dir, "", "rename", bad, "Café notes")
	if ls := ok(t, dir, "", "ls"); !strings.Contains(ls, "Café notes") {
		t.Errorf("ls after rename:\n%s", ls)
	}
}

// A title alone reads nothing: not what a loop's next turns read, nor a
// pipe that stays open.
func TestNewReadsOnlyWhenAsked(t *testing.T) {
	dir := pages(t)
	main := func(in *strings.Reader, args ...string) int {
		var o, e strings.Builder
		return Main(args, Options{Name: "leaflet", Dir: dir, Version: "1.0.0", In: in, Out: &o, Err: &e})
	}
	in := strings.NewReader("Second\nThird\n")
	if code := main(in, "new", "First"); code != 0 || in.Len() != len("Second\nThird\n") {
		t.Errorf("exit %d, %d bytes left", code, in.Len())
	}
	if md := ok(t, dir, "", "cat", "First"); strings.Contains(md, "Second") {
		t.Errorf("cat First: %q", md)
	}
	if code := main(in, "new", "Asked", "--stdin"); code != 0 || in.Len() != 0 {
		t.Errorf("--stdin: exit %d, %d bytes left", code, in.Len())
	}
	if md := ok(t, dir, "", "cat", "Asked"); !strings.Contains(md, "Second") || !strings.Contains(md, "Third") {
		t.Errorf("cat Asked: %q", md)
	}
}

// A title alone says when it leaves input unread, reading none of it and
// waiting for none: a pipe left open with nothing in it is no reason to.
func TestNewSaysInputWaits(t *testing.T) {
	dir := pages(t)
	const note = "leaflet: what is piped in was not read: --stdin reads it\n"
	main := func(in *os.File, args ...string) (string, int) {
		var o, e strings.Builder
		code := Main(args, Options{Name: "leaflet", Dir: dir, Version: "1.0.0", In: in, Out: &o, Err: &e})
		return e.String(), code
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if errOut, code := main(r, "new", "Open pipe"); code != 0 || errOut != "" {
		t.Errorf("empty pipe: exit %d, %q", code, errOut)
	}
	if _, err := w.WriteString("the notes\n"); err != nil {
		t.Fatal(err)
	}
	if errOut, code := main(r, "new", "Full pipe"); code != 0 || errOut != note {
		t.Errorf("pipe with input: exit %d, %q", code, errOut)
	}
	if got := make([]byte, 64); !func() bool { n, err := r.Read(got); return err == nil && string(got[:n]) == "the notes\n" }() {
		t.Error("the pipe's input was read")
	}
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("the notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) // #nosec G304 -- the test's own file.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if errOut, code := main(f, "new", "From a file"); code != 0 || errOut != note {
		t.Errorf("file: exit %d, %q", code, errOut)
	}
	if errOut, code := main(f, "new", "With the file", "--stdin"); code != 0 || errOut != "" {
		t.Errorf("--stdin: exit %d, %q", code, errOut)
	}
	if errOut, code := main(f, "new", "After the file"); code != 0 || errOut != "" {
		t.Errorf("file read to its end: exit %d, %q", code, errOut)
	}
}

// Markdown that starts with a dash is text, as append's help has it, and
// the command's flags still count after the page.
func TestAppendText(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "", "new", "Inbox")
	ok(t, dir, "", "append", "Inbox", "- [ ] call the dentist")
	ok(t, dir, "", "append", "Inbox", "- milk")
	ok(t, dir, "", "append", "Inbox", "---")
	ok(t, dir, "", "append", "Inbox", "-", "split", "words")
	ok(t, dir, "", "append", "Inbox", "--", "- after dashes")
	ok(t, dir, "", "append", "Inbox", "--", "--json")
	md := ok(t, dir, "", "cat", "Inbox")
	for _, want := range []string{"- [ ] call the dentist", "- milk", "---", "- split words", "- after dashes", "--json"} {
		if !strings.Contains(md, want) {
			t.Errorf("cat lacks %q:\n%s", want, md)
		}
	}
	for _, args := range [][]string{{"append", "Inbox", "plain", "--json"}, {"append", "--json", "Inbox", "plain"}, {"--json", "append", "Inbox", "plain"}} {
		if out := ok(t, dir, "", args...); !strings.Contains(out, "inserted_ids") {
			t.Errorf("%v: %q", args, out)
		}
	}
	// --json after the page is no text, so what is piped in is read.
	if out := ok(t, dir, "piped\n", "append", "Inbox", "--json"); !strings.Contains(out, "inserted_ids") {
		t.Errorf("piped --json: %q", out)
	}
	if md := ok(t, dir, "", "cat", "Inbox"); !strings.Contains(md, "piped") {
		t.Errorf("cat lacks what was piped:\n%s", md)
	}
	// --stdin asks for what is piped in, wherever it stands; before a "--"
	// it is no text, and after one it is.
	ok(t, dir, "one\n", "append", "Inbox", "--stdin")
	ok(t, dir, "two\n", "append", "--stdin", "Inbox")
	ok(t, dir, "three\n", "append", "Inbox")
	ok(t, dir, "ignored\n", "append", "Inbox", "--", "--stdin")
	md = ok(t, dir, "", "cat", "Inbox")
	for _, want := range []string{"one\n", "two\n", "three\n", "--stdin\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("cat lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "ignored") {
		t.Errorf("text given and what is piped in:\n%s", md)
	}
	if _, errOut, code := run(t, dir, "piped\n", "append", "Inbox", "words", "--stdin"); code != 1 || !strings.Contains(errOut, "give the text or --stdin, not both") {
		t.Errorf("both: exit %d, %q", code, errOut)
	}
	if _, errOut, code := run(t, dir, " \n", "append", "Inbox", "--stdin"); code != 1 || !strings.Contains(errOut, "nothing to add") {
		t.Errorf("--stdin with nothing: exit %d, %q", code, errOut)
	}
	before := ok(t, dir, "", "cat", "Inbox")
	for _, flag := range []string{"--help", "-h"} {
		if out := ok(t, dir, "", "append", "Inbox", flag); !strings.Contains(out, "Add Markdown to the end of a page") {
			t.Errorf("%s: %q", flag, out)
		}
	}
	if after := ok(t, dir, "", "cat", "Inbox"); after != before {
		t.Errorf("help changed the page:\n%s", after)
	}
}

// numbered is count lines of Markdown, each a block.
func numbered(count int) string {
	var b strings.Builder
	for i := range count {
		fmt.Fprintf(&b, "line %d of the log\n", i)
	}
	return b.String()
}

// A log longer than a call takes all goes in, in order, with one version
// from before it in the page's history.
func TestLongInput(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "Before.\n", "new", "CI log", "--stdin")
	var added struct {
		IDs []string `json:"inserted_ids"`
	}
	if err := json.Unmarshal([]byte(ok(t, dir, numbered(5000), "append", "CI log", "--json")), &added); err != nil || len(added.IDs) != 5000 {
		t.Errorf("--json: %d IDs, %v", len(added.IDs), err)
	}
	md := ok(t, dir, "", "cat", "CI log")
	if !strings.Contains(md, "Before.\n") || strings.Count(md, " of the log") != 5000 || strings.Index(md, "line 2499 of") > strings.Index(md, "line 2500 of") {
		t.Errorf("cat: %d lines", strings.Count(md, " of the log"))
	}
	ok(t, dir, "# Long\n\n"+numbered(5000), "new")
	if md := ok(t, dir, "", "cat", "Long"); strings.Count(md, " of the log") != 5000 {
		t.Errorf("new: %d lines", strings.Count(md, " of the log"))
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	pages, err := st.Pages()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pages {
		if h, err := st.History(p.ID); p.Title == "CI log" && (err != nil || len(h) != 1) {
			t.Errorf("history %d, %v", len(h), err)
		}
	}
}

// A block too long to cut is refused before anything is written, so
// trying again adds nothing twice.
func TestTooLongWritesNothing(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "Before.\n", "new", "Log", "--stdin")
	before := ok(t, dir, "", "cat", "Log")
	long := numbered(1500) + "\n| a | b |\n| --- | --- |\n" + strings.Repeat("| 1 | 2 |\n", 3000) + "\n" + numbered(1500)
	const want = "a block too long to cut, so nothing was written: At most 2000 blocks"
	if _, errOut, code := run(t, dir, long, "append", "Log"); code != 1 || !strings.Contains(errOut, want) {
		t.Errorf("append: exit %d, %q", code, errOut)
	}
	if after := ok(t, dir, "", "cat", "Log"); after != before {
		t.Errorf("append wrote %d bytes", len(after)-len(before))
	}
	ls := ok(t, dir, "", "ls")
	if _, errOut, code := run(t, dir, long, "new", "Other", "--stdin"); code != 1 || !strings.Contains(errOut, want) {
		t.Errorf("new: exit %d, %q", code, errOut)
	}
	if after := ok(t, dir, "", "ls"); after != ls {
		t.Errorf("new made a page:\n%s", after)
	}
}

// A numbered list goes in whole however long, its items as a list, with
// the numbers of the whole list.
func TestLongNumberedList(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "", "new", "List")
	var b strings.Builder
	for i := 1; i <= 7000; i++ {
		fmt.Fprintf(&b, "%d. item %d\n", i, i)
	}
	ok(t, dir, b.String(), "append", "List")
	md := ok(t, dir, "", "cat", "List")
	for i := 1; i <= 7000; i++ {
		if want := fmt.Sprintf("%d. item %d\n", i, i); !strings.Contains(md, want) {
			t.Fatalf("cat lacks %q", want)
		}
	}
}

// shape is blocks as a page reads them: what kind each is, at what depth,
// and what it says.
func shape(nodes []*doc.Node, depth int) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, fmt.Sprintf("%d %s %s %q", depth, n.Type, n.Str("tone"), n.Delta().Plain()))
		out = append(out, shape(n.Children, depth+1)...)
	}
	return out
}

// Text is cut only where each part reads as it does in the whole, however
// near the middle that falls.
func TestParts(t *testing.T) {
	lines, around := numbered(1500), numbered(1200)
	fence := "```go\n" + strings.Repeat("code\n", 3000) + "```\n"
	table := "| a | b |\n| --- | --- |\n" + strings.Repeat("| 1 | 2 |\n", 600)
	details := "<details>\n<summary>More</summary>\n\n" + numbered(10) + "</details>\n"
	nested := strings.Repeat("- item\n  - under it\n", 1201)
	var items, spaced, years strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&items, "%d. item %d\n", i, i)
		fmt.Fprintf(&spaced, "%d. item %d\n  more of it\n\n", i, i)
		fmt.Fprintf(&years, "%d. was a year\n\n", i+1000)
	}
	for name, md := range map[string]string{
		"lines":            numbered(10000),
		"a fence":          lines + fence + lines,
		"a fence up":       fence + lines + lines,
		"math":             lines + "$$\n" + strings.Repeat("x\n", 3000) + "$$\n" + lines,
		"a table":          lines + table + lines,
		"a toggle":         lines + details + lines,
		"a list":           nested,
		"a quote":          around + "> one\n> two\n" + around,
		"a long quote":     around + strings.Repeat("> a line\n", 301) + around,
		"a callout":        around + "> [!NOTE]\n> body\n" + around,
		"under an item":    around + "- item\n\n  under it\n" + around,
		"a hard break":     around + "first\\\nsecond\n" + around,
		"numbered":         items.String(),
		"numbered apart":   spaced.String(),
		"numbered, more":   items.String() + "After.\n",
		"paragraphs":       around + "Before.\n" + strings.Repeat("1999. a year\n\n", 100) + around,
		"numbers, no list": years.String(),
	} {
		got, err := parts(md)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) < 2 {
			t.Errorf("%s: %d parts", name, len(got))
		}
		var read []string
		for _, p := range got {
			blocks := doc.MarkdownBlocks(p)
			if err := doc.CallNodes(blocks); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			read = append(read, shape(blocks, 0)...)
		}
		if whole := shape(doc.MarkdownBlocks(md), 0); !slices.Equal(read, whole) {
			for i := range min(len(read), len(whole)) {
				if read[i] != whole[i] {
					t.Errorf("%s: block %d reads %s, in the whole %s", name, i, read[i], whole[i])
					break
				}
			}
			t.Errorf("%s: parts read as %d blocks, the whole as %d", name, len(read), len(whole))
		}
		// Only a list's numbers change.
		if !strings.HasPrefix(name, "numbered") && strings.Join(got, "") != md {
			t.Errorf("%s: parts lose text", name)
		}
	}
	for _, md := range []string{" \n\n", "$$\n$$\n"} {
		if got, err := parts(md); got != nil || err != nil {
			t.Errorf("parts(%q) = %q, %v", md, got, err)
		}
	}
	// Past what any line can cut, for nothing to be written.
	one := "| a | b |\n| --- | --- |\n" + strings.Repeat("| 1 | 2 |\n", 3000)
	if got, err := parts(one); err == nil {
		t.Errorf("one long table: %d parts", len(got))
	}
}

func TestCurrent(t *testing.T) {
	dir := pages(t)
	if _, errOut, code := run(t, dir, "", "current"); code == 0 || !strings.Contains(errOut, "no page is open") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	id := strings.TrimSpace(ok(t, dir, "", "new", "Roadmap"))
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue(store.LastPageKey, id); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if out := ok(t, dir, "", "current"); out != id[:8]+"  Roadmap  (opened last, not open now)\n" {
		t.Fatalf("closed: %q", out)
	}

	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	view := &store.OpenView{PageID: id, Selection: store.ViewSelection{Text: "ship it", Color: "#ef4444ff", Highlight: "#eab30840"}}
	if err := st.SetOpenView(view); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if out, want := ok(t, dir, "", "current"), id[:8]+"  Roadmap\nColor: #ef4444ff\nHighlight: #eab30840\nSelected:\nship it\n"; out != want {
		t.Fatalf("open: %q, want %q", out, want)
	}
	if out := ok(t, dir, "", "--json", "current"); !strings.Contains(out, `"text": "ship it"`) {
		t.Fatalf("json: %s", out)
	}
}
