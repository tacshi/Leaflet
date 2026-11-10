package core

import "testing"

func TestMarkMentions(t *testing.T) {
	names := []string{"agent", "Chat", "ChatGPT"}
	mark := func(m string) string { return "<mark><b>" + m + "</b></mark>" }
	for _, tc := range []struct{ in, want string }{
		{"@ChatGPT What is LWT?", "<mark><b>@ChatGPT</b></mark> What is LWT?"},
		{"ask @chatgpt, then @agent", "ask <mark><b>@chatgpt</b></mark>, then <mark><b>@agent</b></mark>"},
		{"me@ChatGPT.com @ChatGPTs @Bob", "me@ChatGPT.com @ChatGPTs @Bob"},
		{"`@ChatGPT` @Chat", "`@ChatGPT` <mark><b>@Chat</b></mark>"},
		{"``a ` @agent`` @agent", "``a ` @agent`` <mark><b>@agent</b></mark>"},
		{"see `x\n@agent` @agent", "see `x\n@agent` <mark><b>@agent</b></mark>"},
		{"\\` @agent \\`", "\\` <mark><b>@agent</b></mark> \\`"},
		{"a ` @agent", "a ` <mark><b>@agent</b></mark>"},
		{"```\n@agent\n```\n@agent", "```\n@agent\n```\n<mark><b>@agent</b></mark>"},
		{"~~~~go\n@agent\n~~~\n@agent\n~~~~\n@agent", "~~~~go\n@agent\n~~~\n@agent\n~~~~\n<mark><b>@agent</b></mark>"},
		{"code:\n\n    @agent\n\t@agent\n\n@agent", "code:\n\n    @agent\n\t@agent\n\n<mark><b>@agent</b></mark>"},
		{"ask\n    @agent", "ask\n    <mark><b>@agent</b></mark>"},
		{"```x``` is wrong\n@agent", "```x``` is wrong\n<mark><b>@agent</b></mark>"},
		{"``` a`b\n@agent", "``` a`b\n<mark><b>@agent</b></mark>"},
		{"~~~ a`b\n@agent\n~~~", "~~~ a`b\n@agent\n~~~"},
		{"- one\n\n    @agent", "- one\n\n    <mark><b>@agent</b></mark>"},
		{"1. one\n\n   two\n\n    @agent", "1. one\n\n   two\n\n    <mark><b>@agent</b></mark>"},
		{"- one\n  - two\n\n      @agent", "- one\n  - two\n\n      <mark><b>@agent</b></mark>"},
		{"- one\n\n      @agent", "- one\n\n      @agent"},
		{"- one\n\ntwo\n\n    @agent", "- one\n\ntwo\n\n    @agent"},
		{"-one\n\n    @agent", "-one\n\n    @agent"},
		{"1. one\n\n    - two\n\n      ```\n      @agent\n      ```\n      @agent", "1. one\n\n    - two\n\n      ```\n      @agent\n      ```\n      <mark><b>@agent</b></mark>"},
	} {
		if got := MarkMentions(tc.in, names, mark); got != tc.want {
			t.Errorf("MarkMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

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
