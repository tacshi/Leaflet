package doc

import "strings"

// A code block is a text block of type "code", as appflowy_editor's is:
// its delta holds the code as plain text, lines and all, and data.language
// names its language, or is "" for plain text.

// CodeLanguage is a language a code block may name.
type CodeLanguage struct {
	ID, Name string
}

// CodeLanguages are the languages offered for code blocks. A block may
// name others, as a fence does, and keeps the name.
var CodeLanguages = []CodeLanguage{
	{"", "Plain text"}, {"bash", "Bash"}, {"c", "C"}, {"cpp", "C++"}, {"csharp", "C#"},
	{"css", "CSS"}, {"dart", "Dart"}, {"diff", "Diff"}, {"dockerfile", "Dockerfile"},
	{"go", "Go"}, {"graphql", "GraphQL"}, {"html", "HTML"}, {"java", "Java"},
	{"javascript", "JavaScript"}, {"json", "JSON"}, {"kotlin", "Kotlin"}, {"lua", "Lua"},
	{"markdown", "Markdown"}, {"php", "PHP"}, {"python", "Python"}, {"r", "R"},
	{"ruby", "Ruby"}, {"rust", "Rust"}, {"scala", "Scala"}, {"sql", "SQL"},
	{"swift", "Swift"}, {"toml", "TOML"}, {"typescript", "TypeScript"}, {"xml", "XML"},
	{"yaml", "YAML"}, {"zig", "Zig"},
}

// languageAliases are other names fences and agents give languages.
var languageAliases = map[string]string{
	"sh": "bash", "shell": "bash", "zsh": "bash", "console": "bash", "c++": "cpp", "cc": "cpp",
	"cs": "csharp", "c#": "csharp", "golang": "go", "htm": "html", "js": "javascript",
	"jsx": "javascript", "mjs": "javascript", "node": "javascript", "kt": "kotlin", "md": "markdown",
	"py": "python", "python3": "python", "rb": "ruby", "rs": "rust", "ts": "typescript",
	"tsx": "typescript", "yml": "yaml", "docker": "dockerfile", "text": "", "txt": "",
	"plain": "", "plaintext": "", "none": "",
}

// CodeLanguageID reads a language's name, as a fence or an agent gives it,
// as the ID a code block keeps: "JS" is "javascript", and a language not
// listed keeps its name in lower case, its spaces dashes, as a fence,
// which ends a language at a space, can write it, and without its
// backticks, which a fence's line can't hold after the ones it opens with.
func CodeLanguageID(name string) string {
	id := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(name, "`", "")), "-"))
	if a, ok := languageAliases[id]; ok {
		return a
	}
	return id
}

// CodeLanguageName is how a language ID shows.
func CodeLanguageName(id string) string {
	for _, l := range CodeLanguages {
		if l.ID == id {
			return l.Name
		}
	}
	return id
}

// CodeNode is a code block holding code, in a language.
func CodeNode(code, language string) *Node {
	n := &Node{ID: NewBlockID(), Type: "code"}
	n.SetDelta(Delta{{Text: code}}.Compact())
	n.Set("language", CodeLanguageID(language))
	return n
}

// CodeFence reads a line that opens a Markdown code fence, three or more
// backticks and a language, and reports whether it is one. As in
// CommonMark, only spaces and tabs may be around it: a line that starts
// with another space is text.
func CodeFence(line string) (language string, ok bool) {
	line = strings.Trim(line, " \t")
	rest := strings.TrimLeft(line, "`")
	if len(line)-len(rest) < 3 || strings.Contains(rest, "`") {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) > 0 {
		language = fields[0]
	}
	return CodeLanguageID(language), true
}

// CodeMarkdown writes a code block as a fenced block, with a fence longer
// than any run of backticks in the code, and its language as CodeFence
// reads it back.
func CodeMarkdown(n *Node) string {
	code := n.Delta().Plain()
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	return fence + CodeLanguageID(n.Str("language")) + "\n" + code + "\n" + fence
}
