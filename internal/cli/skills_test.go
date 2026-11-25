package cli

import (
	"encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"

	"leaflet/internal/skills"
)

// Skills need no pages: an agent reads them before Leaflet ever opened.
func TestSkills(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "none")
	list := ok(t, dir, "", "skills")
	for _, s := range skills.All() {
		if !strings.Contains(list, s.Name+"\n  "+s.Description+"\n") {
			t.Errorf("skills lists %q, without %s", list, s.Name)
		}
		if got := ok(t, dir, "", "skills", s.Name); got != s.Doc() {
			t.Errorf("skills %s prints %q", s.Name, got[:min(80, len(got))])
		}
		var one map[string]string
		if err := json.Unmarshal([]byte(ok(t, dir, "", "skills", s.Name, "--json")), &one); err != nil || one["guide"] != s.Body {
			t.Errorf("skills %s --json: %v", s.Name, err)
		}
	}
	var all struct {
		Skills []map[string]string `json:"skills"`
	}
	if err := json.Unmarshal([]byte(ok(t, dir, "", "--json", "skills")), &all); err != nil || len(all.Skills) != len(skills.All()) || all.Skills[0]["name"] != skills.Expressive {
		t.Errorf("--json skills: %v, %v", all, err)
	}
	if _, errOut, code := run(t, dir, "", "skills", "nope"); code != 1 || !strings.Contains(errOut, `no skill "nope"`) {
		t.Errorf("skills nope: exit %d, %q", code, errOut)
	}
	if _, errOut, code := run(t, dir, "", "skills", "nope", "--json"); code != 1 || !strings.Contains(errOut, `no skill "nope"`) {
		t.Errorf("skills nope --json: exit %d, %q", code, errOut)
	}
	if help := ok(t, dir, "", "--help"); !strings.Contains(help, "leaflet skills expressive-pages") {
		t.Errorf("help does not point agents at the skills: %q", help)
	}
	if !Invoked([]string{"/Applications/Leaflet.app/Contents/MacOS/Leaflet", "skills"}, "leaflet") {
		t.Error("the app's executable does not take skills as a command")
	}
}

// What the guide tells an agent with only this command, Markdown makes:
// a chart from its fence, as a card and a callout from theirs.
func TestMarkdownMakesExpressiveBlocks(t *testing.T) {
	dir := pages(t)
	ok(t, dir, "", "new", "Trip")
	md := "> [!TIP]\n> Book early.\n\n```chart\n{\"title\": \"Spend\", \"labels\": [\"Mon\", \"Tue\"], \"series\": [{\"name\": \"¥\", \"values\": [42, 31]}]}\n```\n\n```card\n{\"layout\": \"figures\", \"title\": \"At a glance\", \"facts\": [{\"label\": \"Days\", \"value\": \"5\"}]}\n```\n"
	ok(t, dir, md, "append", "Trip", "--stdin")
	var page struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(ok(t, dir, "", "cat", "Trip", "--json")), &page); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, b := range page.Blocks {
		types = append(types, b["type"].(string))
	}
	if strings.Join(types, " ") != "callout chart card" {
		t.Errorf("append made %v, the new page's empty line not taken", types)
	}
	if _, errOut, code := run(t, dir, "```chart\n{\"labels\": [\"a\"]}\n```\n", "append", "Trip", "--stdin"); code != 1 || !strings.Contains(errOut, "```chart fence") {
		t.Errorf("append a broken chart: exit %d, %q", code, errOut)
	}
	if md := ok(t, dir, "", "cat", "Trip"); strings.Contains(md, "```chart") {
		t.Errorf("a broken chart was written:\n%s", md)
	}
	ok(t, dir, "", "new", "Note", "--stdin")
	ok(t, dir, "", "append", "Note", "one")
	ok(t, dir, "", "append", "Note", "two")
	if md := ok(t, dir, "", "cat", "Note"); !strings.HasSuffix(md, "one\n\ntwo\n") {
		t.Errorf("appending twice to a new page:\n%q", md)
	}
	longBroken := strings.Repeat("line\n\n", 2100) + "```chart\n{\"labels\": [\"a\"]}\n```\n"
	before := ok(t, dir, "", "cat", "Note")
	if _, errOut, code := run(t, dir, longBroken, "append", "Note", "--stdin"); code != 1 || !strings.Contains(errOut, "```chart fence") {
		t.Errorf("append long broken chart: exit %d, %q", code, errOut)
	}
	if after := ok(t, dir, "", "cat", "Note"); after != before {
		t.Errorf("a refused multi-part append changed the page")
	}
	beforeList := ok(t, dir, "", "ls")
	if _, errOut, code := run(t, dir, longBroken, "new", "Broken", "--stdin"); code != 1 || !strings.Contains(errOut, "```chart fence") {
		t.Errorf("new long broken chart: exit %d, %q", code, errOut)
	}
	if afterList := ok(t, dir, "", "ls"); afterList != beforeList {
		t.Errorf("a refused multi-part new created a page:\n%s", afterList)
	}
}
