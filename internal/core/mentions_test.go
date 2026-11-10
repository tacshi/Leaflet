package core

import "testing"

// A comment's mentions of agents are marked, and only them: not an email
// address, a longer word or code.
func TestMarkMentions(t *testing.T) {
	names := []string{"agent", "Chat", "ChatGPT"}
	mark := func(m string) string { return "<mark><b>" + m + "</b></mark>" }
	for _, tc := range []struct{ in, want string }{
		{"@ChatGPT What is LWT?", "<mark><b>@ChatGPT</b></mark> What is LWT?"},
		{"ask @chatgpt, then @agent", "ask <mark><b>@chatgpt</b></mark>, then <mark><b>@agent</b></mark>"},
		{"me@ChatGPT.com @ChatGPTs @Bob", "me@ChatGPT.com @ChatGPTs @Bob"},
		{"`@ChatGPT` @Chat", "`@ChatGPT` <mark><b>@Chat</b></mark>"},
		// Code spans of longer runs, and over lines of a paragraph.
		{"``a ` @agent`` @agent", "``a ` @agent`` <mark><b>@agent</b></mark>"},
		{"see `x\n@agent` @agent", "see `x\n@agent` <mark><b>@agent</b></mark>"},
		// A backtick escaped, or never closed, opens no span.
		{"\\` @agent \\`", "\\` <mark><b>@agent</b></mark> \\`"},
		{"a ` @agent", "a ` <mark><b>@agent</b></mark>"},
		// Fenced and indented code blocks.
		{"```\n@agent\n```\n@agent", "```\n@agent\n```\n<mark><b>@agent</b></mark>"},
		{"~~~~go\n@agent\n~~~\n@agent\n~~~~\n@agent", "~~~~go\n@agent\n~~~\n@agent\n~~~~\n<mark><b>@agent</b></mark>"},
		{"code:\n\n    @agent\n\t@agent\n\n@agent", "code:\n\n    @agent\n\t@agent\n\n<mark><b>@agent</b></mark>"},
		// An indented line going on with a paragraph is not code.
		{"ask\n    @agent", "ask\n    <mark><b>@agent</b></mark>"},
		// Backticks with a backtick after them on their line are a code
		// span, opening no block.
		{"```x``` is wrong\n@agent", "```x``` is wrong\n<mark><b>@agent</b></mark>"},
		{"``` a`b\n@agent", "``` a`b\n<mark><b>@agent</b></mark>"},
		{"~~~ a`b\n@agent\n~~~", "~~~ a`b\n@agent\n~~~"},
		// What is indented under a list item is its text, after a blank
		// line too; indented as code from there, or under an item that
		// ended, it is code.
		{"- one\n\n    @agent", "- one\n\n    <mark><b>@agent</b></mark>"},
		{"1. one\n\n   two\n\n    @agent", "1. one\n\n   two\n\n    <mark><b>@agent</b></mark>"},
		{"- one\n  - two\n\n      @agent", "- one\n  - two\n\n      <mark><b>@agent</b></mark>"},
		{"- one\n\n      @agent", "- one\n\n      @agent"},
		{"- one\n\ntwo\n\n    @agent", "- one\n\ntwo\n\n    @agent"},
		{"-one\n\n    @agent", "-one\n\n    @agent"},
		// A fence under an item opens and closes there.
		{"1. one\n\n    - two\n\n      ```\n      @agent\n      ```\n      @agent", "1. one\n\n    - two\n\n      ```\n      @agent\n      ```\n      <mark><b>@agent</b></mark>"},
	} {
		if got := MarkMentions(tc.in, names, mark); got != tc.want {
			t.Errorf("MarkMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An agent is mentioned where the comment shows it marked: not in code, an
// address, a longer word or after a backslash; the longest name first.
func TestMentioned(t *testing.T) {
	names := []string{"agent", "Ollama", "Ollama Cloud"}
	for in, want := range map[string]string{
		"@agent sum it up": "agent", "ask @Ollama": "ollama", "ask @ollama cloud": "ollama cloud",
		"`@agent`": "", "```\n@agent\n```": "", `\@agent`: "",
		"me@ollama.com": "", "@agentic": "", "@Ollamas": "",
	} {
		got := Mentioned(in, names)
		if want == "" && len(got) > 0 || want != "" && (len(got) != 1 || !got[want]) {
			t.Errorf("Mentioned(%q) = %v, want %q", in, got, want)
		}
	}
}
