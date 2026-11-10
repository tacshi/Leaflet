package recording

import (
	"reflect"
	"testing"

	"leaflet/internal/golden"
	"leaflet/internal/store"
)

func TestTokensMakeSegments(t *testing.T) {
	var cases []struct {
		Tokens   []Token         `json:"tokens"`
		Segments []store.Segment `json:"segments"`
	}
	f := golden.Open(t, "segments_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		got := TokensToSegments(c.Tokens)
		if len(got) == 0 && len(c.Segments) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Segments) {
			if got == nil {
				got = []store.Segment{}
			}
			f.Mismatch(got, i, "segments")
		}
	}
}
