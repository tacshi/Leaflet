package doc

import (
	"strings"
	"testing"
)

func TestCodeLanguages(t *testing.T) {
	for in, want := range map[string]string{"Go": "go", " JS ": "javascript", "py": "python", "txt": "", "Elixir": "elixir", "": ""} {
		if got := CodeLanguageID(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if CodeLanguageName("cpp") != "C++" || CodeLanguageName("") != "Plain text" || CodeLanguageName("elixir") != "elixir" {
		t.Error("names")
	}
	for line, want := range map[string]string{"```": "", "```ts": "typescript", "  ```go title": "go", "````": ""} {
		if got, ok := CodeFence(line); !ok || got != want {
			t.Errorf("%q: got %q, %v", line, got, ok)
		}
	}
	for _, line := range []string{"``", "``` `x`", "text ```"} {
		if _, ok := CodeFence(line); ok {
			t.Errorf("%q is not a fence", line)
		}
	}
}

func TestCodeBlocks(t *testing.T) {
	code := "fmt.Println(\"**not bold**\")\n// ```"
	n, err := SpecToNode(map[string]any{"type": "code", "text": code + "\n", "language": "golang"}, "c1", "Agent")
	if err != nil || n.ID != "c1" || n.Type != "code" || n.Delta().Plain() != code || n.Str("language") != "go" {
		t.Fatalf("got %+v, %v", n, err)
	}
	if len(n.Delta()) != 1 || !n.Delta()[0].Attrs.Zero() {
		t.Errorf("code has formats: %+v", n.Delta())
	}
	c := FromBlocks([]*Node{n})
	if v := BlockViews(c)[0]; v["text"] != code || v["language"] != "go" {
		t.Errorf("view %v", v)
	}
	// A fence longer than the backticks in the code.
	if md := PageMarkdown("", c, nil); md != "````go\n"+code+"\n````\n" {
		t.Errorf("markdown %q", md)
	}
	if !strings.Contains(c.PlainText(), "Println") {
		t.Errorf("plain %q", c.PlainText())
	}
}
