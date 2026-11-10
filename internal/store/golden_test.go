package store

import (
	"testing"

	"leaflet/internal/golden"
)

func TestKeysPutLast(t *testing.T) {
	for _, name := range []string{"sort_keys.json", "sort_keys_random.json"} {
		var cases []struct {
			After string `json:"after"`
		}
		f := golden.Open(t, name)
		f.Decode(&cases)
		for i, c := range cases {
			f.Check(KeyBetween(c.After, ""), i, "key")
		}
	}
}

func TestTagsAreTidied(t *testing.T) {
	var cases []struct {
		Tags []string `json:"tags"`
	}
	f := golden.Open(t, "tags_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		kept := NormalizeTags(c.Tags)
		if kept == nil {
			kept = []string{}
		}
		f.Check(kept, i, "kept")
	}
}
