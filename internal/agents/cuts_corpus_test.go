package agents

import (
	"strings"
	"testing"

	"leaflet/internal/parity"
)

// A tool's result is cut here where the server cuts it, half of a
// surrogate pair and all, and an answer without its thinking is the same
// here as on the server.
func TestCorpusCuts(t *testing.T) {
	var cases []struct {
		Pad    int    `json:"pad"`
		End    string `json:"end"`
		Cut    string `json:"cut"`
		Answer string `json:"answer"`
		Reply  string `json:"reply"`
	}
	parity.Read(t, "cuts.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := strings.TrimPrefix(cutResult(strings.Repeat("x", c.Pad)+c.End), strings.Repeat("x", c.Pad)); got != c.Cut {
			fail("%d and %q cut to %q, on the server %q", c.Pad, c.End, got, c.Cut)
		}
		if got := withoutThinking(c.Answer); got != c.Reply {
			fail("%q read as %q, on the server %q", c.Answer, got, c.Reply)
		}
	}
}
