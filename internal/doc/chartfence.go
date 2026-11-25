package doc

import "encoding/json"

// AgentChartData is ChartData for a chart an agent writes, which a pie
// of several series fails: a pie draws its first series alone, so the
// rest would be lost. The chart editor keeps such a pie's other columns,
// for its table to go on holding them.
func AgentChartData(chart map[string]any) (map[string]any, error) {
	data, err := ChartData(chart)
	if err != nil {
		return nil, err
	}
	if series, _ := data["series"].([]any); data["chart_type"] == "pie" && len(series) > 1 {
		return nil, toolErr("A pie chart takes exactly one series.")
	}
	return data, nil
}

// ChartNodeFromJSON makes the chart a ```chart fence's JSON is, as
// {"chart_type", "title", "labels", "series": [{"name", "values"}],
// "caption"}, the chart of a block spec: Markdown's way to write one, for
// agents that write a page in Markdown alone, as the leaflet command's.
// It reports whether the JSON is a chart; a fence that is not stays code.
func ChartNodeFromJSON(src string) (*Node, bool) {
	var spec map[string]any
	if err := json.Unmarshal([]byte(src), &spec); err != nil {
		return nil, false
	}
	data, err := AgentChartData(spec)
	if err != nil {
		return nil, false
	}
	return &Node{ID: NewBlockID(), Type: "chart", Data: data}, true
}

// FenceError says why a ```chart, ```card or ```map fence among nodes, or
// nested in them, stayed code rather than becoming its block, for a write
// that meant it as one to fail rather than leave its JSON on the page.
// nil where there is none.
func FenceError(nodes []*Node) error {
	for _, n := range nodes {
		if err := fenceError(n); err != nil {
			return err
		}
		if err := FenceError(n.Children); err != nil {
			return err
		}
	}
	return nil
}

func fenceError(n *Node) error {
	if n.Type != "code" {
		return nil
	}
	lang := n.Str("language")
	read := map[string]func(map[string]any) (map[string]any, error){"chart": AgentChartData, "card": CardData, "map": MapData}[lang]
	if read == nil {
		return nil
	}
	var spec map[string]any
	err := json.Unmarshal([]byte(n.Delta().Plain()), &spec)
	if err != nil {
		err = toolErr("it is not a JSON object (%v).", err)
	} else {
		_, err = read(spec)
	}
	if err == nil {
		err = toolErr("it is not one.")
	}
	return toolErr("A ```%s fence holds a %s's JSON, and this one was refused: %v Nothing was written. Fix it, or name another language, as json, to show it as code.", lang, lang, err)
}
