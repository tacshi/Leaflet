// Package skills holds Leaflet's skills: guides that tell an agent, in the
// app or outside it, how to do something well in a page, such as choosing
// the block that shows content best. Each is a Markdown file in the Agent
// Skills format, a name and a description in its front matter, read whole
// only when an agent asks for it, so a prompt carries just the index.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

//go:embed *.md
var files embed.FS

// Skill is a guide an agent reads when its task calls for it.
type Skill struct {
	// Name is how an agent asks for it, as expressive-pages.
	Name string
	// Description says what it guides and when to read it.
	Description string
	// Body is the guide, in Markdown.
	Body string
	// External is a skill for agents outside the app only, as one on
	// saving what a chat in another app settles into Leaflet.
	External bool
}

// Doc is the skill as its file has it: front matter, then the guide.
func (s Skill) Doc() string {
	head := "---\nname: " + yamlScalar(s.Name) + "\ndescription: " + yamlScalar(s.Description) + "\n"
	if s.External {
		head += "audience: external\n"
	}
	return head + "---\n\n" + s.Body
}

func yamlScalar(s string) string {
	if needsYAMLQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsYAMLQuote(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.HasSuffix(s, ":") ||
		strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.ContainsAny(s, "\r\n") {
		return true
	}
	return strings.ContainsRune(`"'[{}]&*!|>%@`+"`?-#:,", rune(s[0]))
}

func unquoteYAML(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strconv.Unquote(s)
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	}
	return s, nil
}

var all = load()

func load() []Skill {
	names, err := fs.Glob(files, "*.md")
	if err != nil {
		panic(err)
	}
	var out []Skill
	for _, name := range names {
		raw, err := files.ReadFile(name)
		if err != nil {
			panic(err)
		}
		s, err := parse(string(raw))
		if err != nil {
			panic(fmt.Sprintf("skills: %s: %v", name, err))
		}
		if s.Name+".md" != name {
			panic(fmt.Sprintf("skills: %s is named %q", name, s.Name))
		}
		out = append(out, s)
	}
	// The guide every writing task starts from comes first.
	slices.SortStableFunc(out, func(a, b Skill) int {
		return order(a.Name) - order(b.Name)
	})
	return out
}

func order(name string) int {
	if name == Expressive {
		return 0
	}
	return 1
}

// Expressive is the skill an agent reads before it writes a page.
const Expressive = "expressive-pages"

func parse(raw string) (Skill, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	rest, ok := strings.CutPrefix(raw, "---\n")
	if !ok {
		return Skill{}, fmt.Errorf("no front matter")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Skill{}, fmt.Errorf("front matter not closed")
	}
	var s Skill
	for line := range strings.SplitSeq(head, "\n") {
		key, value, _ := strings.Cut(line, ":")
		val, err := unquoteYAML(value)
		if err != nil {
			return Skill{}, fmt.Errorf("%s: %w", strings.TrimSpace(key), err)
		}
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		case "audience":
			switch val {
			case "external":
				s.External = true
			case "all":
			default:
				return Skill{}, fmt.Errorf("audience %q is neither all nor external", val)
			}
		}
	}
	s.Body = strings.TrimSpace(body) + "\n"
	if s.Name == "" || s.Description == "" || strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("needs a name, a description and a guide")
	}
	return s, nil
}

// All are the skills, expressive-pages first.
func All() []Skill { return slices.Clone(all) }

// For are the skills for agents in the app, or with external those for
// agents outside it too, in the order All gives them.
func For(external bool) []Skill {
	return slices.DeleteFunc(All(), func(s Skill) bool { return s.External && !external })
}

// Names are the names of the skills For gives.
func Names(external bool) []string {
	var out []string
	for _, s := range For(external) {
		out = append(out, s.Name)
	}
	return out
}

// Get finds a skill by its name.
func Get(name string) (Skill, bool) {
	i := slices.IndexFunc(all, func(s Skill) bool { return s.Name == strings.TrimSpace(name) })
	if i < 0 {
		return Skill{}, false
	}
	return all[i], true
}

// Index lists the skills For gives a line each, "- name: description",
// for a prompt to offer them.
func Index(external bool) string {
	var lines []string
	for _, s := range For(external) {
		lines = append(lines, "- "+s.Name+": "+s.Description)
	}
	return strings.Join(lines, "\n")
}
