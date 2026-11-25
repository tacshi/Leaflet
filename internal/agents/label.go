package agents

import (
	"encoding/json"
	"strings"

	"leaflet/locales"
)

// CommandLine is the agent name the leaflet command's own changes go by,
// which the app shows in its own words, as ShownName gives it.
const CommandLine = "leaflet-command-line"

// ShownName is how an agent's name shows in the app: the app's words for the
// leaflet command and for an agent with no name of its own, else the name.
// The names earlier versions kept for those two, in English, show so too.
func ShownName(agent string) string {
	switch strings.TrimSpace(agent) {
	case "", "Agent":
		return locales.CommonAgent()
	case CommandLine, "Command line":
		return locales.ExternalLiveEditsCommandLine()
	}
	return agent
}

// runLabel is what a history snapshot from before an agent's edits keeps as
// its label: the agent, and for a run in the app, what it was asked. The app
// words it in its own language as it shows the snapshot.
type runLabel struct {
	Agent   string `json:"agent"`
	Request string `json:"request,omitempty"`
}

// RunLabel is the label of a snapshot from before agent's edits, as asked
// in request, or with no request for an agent outside the app.
func RunLabel(agent, request string) string {
	raw, _ := json.Marshal(runLabel{Agent: agent, Request: request})
	return string(raw)
}

// ParseRunLabel reads the label of a snapshot from before an agent's edits:
// as RunLabel keeps it, or as earlier versions did, in English, "Before"
// and the agent, then ": " and the request for a run in the app.
func ParseRunLabel(label string) (agent, request string, ok bool) {
	if strings.HasPrefix(label, "{") {
		var l runLabel
		if json.Unmarshal([]byte(label), &l) == nil {
			return l.Agent, l.Request, true
		}
		return "", "", false
	}
	rest, ok := strings.CutPrefix(label, "Before ")
	if !ok || strings.TrimSpace(rest) == "" {
		return "", "", false
	}
	agent, request, _ = strings.Cut(rest, ": ")
	return agent, request, true
}

// RunLabelText is how a snapshot's label from before an agent's edits reads
// in the app's language, or false for a label that is not one.
func RunLabelText(label string) (string, bool) {
	agent, request, ok := ParseRunLabel(label)
	switch {
	case !ok:
		return "", false
	case request != "":
		return locales.DialogHistoryBeforeAgentRun(ShownName(agent), request), true
	}
	return locales.DialogHistoryBeforeAgent(ShownName(agent)), true
}
