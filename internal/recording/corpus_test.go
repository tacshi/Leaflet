package recording

import (
	"encoding/json"
	"reflect"
	"testing"

	"leaflet/internal/parity"
	"leaflet/internal/store"
)

// Soniox's tokens make the transcript's lines here that they make on the
// server (supabase/functions/_shared/corpus_test.ts writes the corpus).
func TestCorpusTokensToSegments(t *testing.T) {
	var cases []struct {
		Tokens   []Token         `json:"tokens"`
		Segments []store.Segment `json:"segments"`
	}
	parity.Read(t, "segments.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		got := TokensToSegments(c.Tokens)
		if len(got) == 0 && len(c.Segments) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Segments) {
			tokens, _ := json.Marshal(c.Tokens)
			fail("%s made\n%+v\nthe server\n%+v", tokens, got, c.Segments)
		}
	}
}
