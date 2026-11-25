package skills

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

func TestSkills(t *testing.T) {
	all := All()
	if len(all) < 2 || all[0].Name != Expressive {
		t.Fatalf("skills %v, want %s first", Names(true), Expressive)
	}
	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.Name] {
			t.Errorf("two skills named %s", s.Name)
		}
		seen[s.Name] = true
		if strings.Contains(s.Description, "\n") || !strings.HasPrefix(s.Body, "# ") {
			t.Errorf("%s: description %q, body starts %q", s.Name, s.Description, s.Body[:min(20, len(s.Body))])
		}
		if raw, err := files.ReadFile(s.Name + ".md"); err != nil || string(raw) != s.Doc() {
			t.Errorf("%s: file differs from Doc() (%v)", s.Name, err)
		}
		if needsYAMLQuote(s.Name) || needsYAMLQuote(s.Description) {
			t.Errorf("%s: front matter value needs YAML quoting", s.Name)
		}
		got, ok := Get(" " + s.Name + " ")
		if !ok || got.Body != s.Body {
			t.Errorf("Get(%s) = %v, %v", s.Name, got.Name, ok)
		}
		again, err := parse(s.Doc())
		if err != nil || again != s {
			t.Errorf("%s: its Doc reads back as %+v, %v", s.Name, again.Name, err)
		}
		if !strings.Contains(Index(true), "- "+s.Name+": "+s.Description) {
			t.Errorf("index %q leaves out %s", Index(true), s.Name)
		}
		if strings.Contains(Index(false), "- "+s.Name+": ") == s.External {
			t.Errorf("the app's index %q, with %s external %v", Index(false), s.Name, s.External)
		}
	}
	if saving, _ := Get("saving-to-leaflet"); !saving.External || slices.Contains(Names(false), saving.Name) || !slices.Contains(Names(true), saving.Name) {
		t.Errorf("saving-to-leaflet is for agents outside the app alone: external %v, the app's %v", saving.External, Names(false))
	}
	if _, ok := Get("nope"); ok {
		t.Error("Get(nope) found one")
	}
	quoted := Skill{Name: "quoted", Description: `Clear: with "quotes" # note`, Body: "# Title\n"}
	if again, err := parse(quoted.Doc()); err != nil || again != quoted {
		t.Errorf("quoted skill round-trip: %+v, %v", again, err)
	}
	if got, err := parse("---\nname: 'single'\ndescription: 'It''s: clear'\n---\n# Title\n"); err != nil || got.Name != "single" || got.Description != "It's: clear" {
		t.Errorf("single-quoted front matter: %+v, %v", got, err)
	}
	for _, bad := range []string{"no front matter", "---\nname: x\n", "---\nname: x\n---\nbody", "---\ndescription: y\n---\nbody", "---\nname: x\ndescription: y\n---\n  \n", "---\nname: x\ndescription: y\naudience: everyone\n---\nbody"} {
		if _, err := parse(bad); err == nil {
			t.Errorf("parse(%q) took it", bad)
		}
	}
}

// The guide names every block an agent can write, so a new block type
// gets its line in it.
func TestExpressiveNamesEveryBlock(t *testing.T) {
	s, _ := Get(Expressive)
	for _, typ := range doc.AgentTypes {
		if !strings.Contains(s.Body, "`"+typ+"`") {
			t.Errorf("%s does not name the %s block", Expressive, typ)
		}
	}
}

// What the guide says Markdown writes, Markdown writes: its example makes
// the blocks it shows.
func TestExpressiveMarkdownMakesItsBlocks(t *testing.T) {
	s, _ := Get(Expressive)
	example := regexp.MustCompile("(?s)````markdown\n(.*?)\n````").FindStringSubmatch(s.Body)
	if example == nil {
		t.Fatal("no example")
	}
	var types []string
	for _, n := range doc.MarkdownBlocks(example[1]) {
		types = append(types, n.Type)
	}
	want := []string{"heading", "paragraph", "card", "callout", "table", "chart", "todo_list", "todo_list"}
	if !slices.Equal(types, want) {
		t.Errorf("example makes %v, want %v", types, want)
	}
	syntax := "> [!WARNING]\n> Mind the gap.\n\n<details><summary>The data</summary>\n\nRaw rows.\n\n</details>\n\n$$\ne = mc^2\n$$\n\n```mermaid\nflowchart LR\n  a --> b\n```\n\n![A bridge](https://example.com/bridge.jpg)\n\n```map\n{\"title\": \"Kyoto\", \"places\": [{\"name\": \"Kyoto Station\", \"lat\": 34.9858, \"lng\": 135.7588}]}\n```\n"
	types = nil
	for _, n := range doc.MarkdownBlocks(syntax) {
		types = append(types, n.Type)
	}
	if want := []string{"callout", "toggle_list", "math_equation", "diagram", "image", "map"}; !slices.Equal(types, want) {
		t.Errorf("the guide's Markdown makes %v, want %v", types, want)
	}
}

// Each color a skill names is a hex the app takes, and a highlight is
// see-through, so text stays readable in the dark look.
func TestSkillColors(t *testing.T) {
	for _, s := range All() {
		for _, m := range regexp.MustCompile(`background:\s*(#[0-9a-fA-F]+)`).FindAllStringSubmatch(s.Body, -1) {
			if !doc.ValidColor(m[1]) || len(m[1]) != 9 && !strings.Contains(s.Body, "Never highlight with an opaque color, such as `"+m[1]+"`") {
				t.Errorf("%s highlights with %s", s.Name, m[1])
			}
		}
	}
}
