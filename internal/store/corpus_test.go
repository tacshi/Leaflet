package store

import (
	"testing"

	"leaflet/internal/parity"
)

// A key the server's agent puts after another is the key the app makes
// (supabase/functions/_shared/corpus_test.ts writes the corpus), whatever
// characters the key before holds.
func TestCorpusKeysPutLast(t *testing.T) {
	var cases []struct{ After, Key string }
	parity.Read(t, "sort_keys.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := KeyBetween(c.After, ""); got != c.Key {
			fail("after %q: the app makes %q, the server %q", c.After, got, c.Key)
		}
	}
}
