package doc

import "strings"

// A callout is a text block set off in a tinted box with an icon; its
// tone, data.tone, picks the tint: info, tip, warning or danger. In
// Markdown it is one of GitHub's alerts, as "> [!TIP]".

// CalloutTones are the tones a callout may take, the first the default.
var CalloutTones = []string{"info", "tip", "warning", "danger"}

var calloutAlerts = map[string]string{"info": "NOTE", "tip": "TIP", "warning": "WARNING", "danger": "CAUTION"}

// CalloutTone reads a tone, or "info" for one not known.
func CalloutTone(tone string) string {
	tone = strings.ToLower(strings.TrimSpace(tone))
	if _, ok := calloutAlerts[tone]; ok {
		return tone
	}
	return "info"
}

// CalloutAlert is the GitHub alert line that opens a callout of a tone.
func CalloutAlert(tone string) string { return "> [!" + calloutAlerts[CalloutTone(tone)] + "]" }

// AlertTone reads a GitHub alert line, as "> [!WARNING]", as the tone of
// the callout it opens, and reports whether it is one.
func AlertTone(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), ">")
	if !ok {
		return "", false
	}
	switch strings.ToUpper(strings.TrimSpace(rest)) {
	case "[!NOTE]", "[!IMPORTANT]":
		return "info", true
	case "[!TIP]":
		return "tip", true
	case "[!WARNING]":
		return "warning", true
	case "[!CAUTION]":
		return "danger", true
	}
	return "", false
}

// CalloutMarkdown writes a callout as a GitHub alert.
func CalloutMarkdown(n *Node) string {
	return CalloutAlert(n.Str("tone")) + "\n> " + strings.ReplaceAll(n.Delta().ToMarkdown(), "\n", "\n> ")
}
