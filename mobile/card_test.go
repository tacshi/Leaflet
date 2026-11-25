package mobile

import (
	"encoding/json"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

func TestCardViewAndData(t *testing.T) {
	data, err := CardData(`{"layout":"details","icon":"hospital","title":"CUHK Medical Centre","image":"","note":"A **teaching** hospital.",
		"facts":[{"label":"总机","value":"**3946 6888**"},{"label":"","value":""}],"sources":[{"url":"https://www.cuhkmc.hk","title":""},{"url":"","title":""}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	node, _ := json.Marshal(&doc.Node{ID: "c", Type: "card", Data: d})
	out, err := CardView(string(node))
	if err != nil {
		t.Fatal(err)
	}
	var v cardView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Icon != "hospital" || len(v.Facts) != 1 || v.Facts[0].Separator != "：" || len(v.Sources) != 1 || v.Sources[0].Name != "cuhkmc.hk" {
		t.Errorf("view %+v", v)
	}
	if b, _ := json.Marshal(v.Facts[0].Value); !strings.Contains(string(b), `"bold":true`) {
		t.Errorf("value delta %s", b)
	}
	if _, err := CardData(`{"title":"x","sources":[{"url":"not a link"}]}`); err == nil || !strings.Contains(err.Error(), "1") {
		t.Errorf("a bad source: %v", err)
	}
	para, _ := json.Marshal(&doc.Node{ID: "p", Type: "paragraph"})
	if _, err := CardView(string(para)); err == nil {
		t.Error("a paragraph read as a card")
	}
}
