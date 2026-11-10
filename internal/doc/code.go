package doc

import "strings"

type CodeLanguage struct {
	ID, Name string
}

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

var languageAliases = map[string]string{
	"sh": "bash", "shell": "bash", "zsh": "bash", "console": "bash", "c++": "cpp", "cc": "cpp",
	"cs": "csharp", "c#": "csharp", "golang": "go", "htm": "html", "js": "javascript",
	"jsx": "javascript", "mjs": "javascript", "node": "javascript", "kt": "kotlin", "md": "markdown",
	"py": "python", "python3": "python", "rb": "ruby", "rs": "rust", "ts": "typescript",
	"tsx": "typescript", "yml": "yaml", "docker": "dockerfile", "text": "", "txt": "",
	"plain": "", "plaintext": "", "none": "",
}

func CodeLanguageID(name string) string {
	id := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(name, "`", "")), "-"))
	if a, ok := languageAliases[id]; ok {
		return a
	}
	return id
}

func CodeLanguageName(id string) string {
	for _, l := range CodeLanguages {
		if l.ID == id {
			return l.Name
		}
	}
	return id
}

func CodeNode(code, language string) *Node {
	n := &Node{ID: NewBlockID(), Type: "code"}
	n.SetDelta(Delta{{Text: code}}.Compact())
	n.Set("language", CodeLanguageID(language))
	return n
}

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

func CodeMarkdown(n *Node) string {
	code := n.Delta().Plain()
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	return fence + CodeLanguageID(n.Str("language")) + "\n" + code + "\n" + fence
}
