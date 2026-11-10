package doc

import (
	"slices"
	"strings"
)

var CalloutTones = []string{"info", "tip", "warning", "danger"}

var calloutAlerts = map[string]string{"info": "NOTE", "tip": "TIP", "warning": "WARNING", "danger": "CAUTION"}

func CalloutTone(tone string) string {
	tone = strings.ToLower(strings.TrimSpace(tone))
	if _, ok := calloutAlerts[tone]; ok {
		return tone
	}
	return "info"
}

var calloutIcons = map[string]string{"info": "info", "tip": "lightbulb", "warning": "triangle-alert", "danger": "octagon-alert"}

func CalloutIcon(tone string) string { return calloutIcons[CalloutTone(tone)] }

func NextTone(tone string) string {
	i := slices.Index(CalloutTones, CalloutTone(tone))
	return CalloutTones[(i+1)%len(CalloutTones)]
}

func CalloutAlert(tone string) string { return "> [!" + calloutAlerts[CalloutTone(tone)] + "]" }

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

func CalloutMarkdown(n *Node) string {
	lines := markdownLines(n.Delta().ReadableMarkdown())
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+EscapeBlockStart(l), " ")
	}
	return CalloutAlert(n.Str("tone")) + "\n" + strings.Join(lines, "\n")
}
