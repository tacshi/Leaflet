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

// fenceReaders check the JSON a fence of their language holds, as a block
// spec's chart, card or map: Markdown's way to write those blocks, for
// agents that write a page in Markdown alone, as the leaflet command's,
// and for an agent's answer to show a card or a map.
var fenceReaders = map[string]func(map[string]any) (map[string]any, error){
	"chart": AgentChartData,
	"card":  CardData,
	"map":   MapData,
}

// fenceNode is the block a fence of lang holds, of that type, or why the
// fence holds none, as a ```chart fence's JSON that is no chart; neither
// for a language that makes no block, whose fence is code.
func fenceNode(lang, text string) (*Node, error) {
	read := fenceReaders[lang]
	if read == nil {
		return nil, nil
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(text), &spec); err != nil {
		return nil, toolErr("it is not a JSON object (%v).", err)
	}
	data, err := read(spec)
	if err != nil {
		return nil, err
	}
	return &Node{ID: NewBlockID(), Type: lang, Data: data}, nil
}

// CardNodeFromJSON is the card block a ```card fence's JSON is, or false
// when it is none.
func CardNodeFromJSON(src string) (*Node, bool) {
	n, err := fenceNode("card", src)
	return n, err == nil
}

// MapNodeFromJSON is the map block a ```map fence's JSON is, its route
// resolved already, or false when it is none.
func MapNodeFromJSON(src string) (*Node, bool) {
	n, err := fenceNode("map", src)
	return n, err == nil
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
	if fenceReaders[lang] == nil {
		return nil
	}
	_, err := fenceNode(lang, n.Delta().Plain())
	if err == nil {
		err = toolErr("it is not one.")
	}
	return toolErr("A ```%s fence holds a %s's JSON, and this one was refused: %v Nothing was written. Fix it, or name another language, as json, to show it as code.", lang, lang, err)
}
