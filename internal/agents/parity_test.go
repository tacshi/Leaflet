package agents

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"leaflet/internal/doc"
)

// Agents find URLs and title subpages here as the server's agent function
// does (supabase/functions/_shared/testdata/agent.json, which its tests
// write).
func TestAgentsAsTheServerHasThem(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		URLs []struct {
			Text string   `json:"text"`
			URLs []string `json:"urls"`
		} `json:"urls"`
		Titles []struct {
			Title string  `json:"title"`
			Made  *string `json:"made"`
		} `json:"titles"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.URLs) == 0 || len(f.Titles) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, tc := range f.URLs {
		if got := urlRe.FindAllString(tc.Text, -1); !slices.Equal(got, tc.URLs) {
			t.Errorf("%q: URLs %q, want %q", tc.Text, got, tc.URLs)
		}
	}
	for _, tc := range f.Titles {
		var made *string
		x := executor(page())
		x.Ctx.CreateSubpage = func(title string, _ []*doc.Node) (string, error) {
			made = &title
			return "page-2", nil
		}
		x.Call("create_subpage", map[string]any{"title": tc.Title})
		if (made == nil) != (tc.Made == nil) || made != nil && *made != *tc.Made {
			t.Errorf("%q made %v, want %v", tc.Title, made, tc.Made)
		}
	}
}
