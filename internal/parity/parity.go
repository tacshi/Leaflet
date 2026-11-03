// Package parity reads the corpora the server's tests write
// (supabase/functions/_shared/corpus_test.ts): random cases, each with
// what the server's TypeScript copy of the app's core makes of it, for
// the Go tests to check the app against. Any way the two read, write or
// edit a page apart fails a test.
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// dir is where the corpora are, from the repository's root.
const dir = "supabase/functions/_shared/testdata/corpus"

// Read reads a corpus into v. A corpus without cases fails the test: a
// check of none would pass whatever the app does.
func Read(t testing.TB, name string, v any) {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		up := filepath.Dir(root)
		if up == root {
			t.Fatal("no go.mod above the test's directory")
		}
		root = up
	}
	raw, err := os.ReadFile(filepath.Join(root, dir, name)) // #nosec G304 -- the repository's own corpora.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var cases []json.RawMessage
	if json.Unmarshal(raw, &cases) == nil && len(cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
}

// JSON is v as JSON reads it back, so values compare whatever their Go
// types: maps, slices, strings, float64s, bools and nils.
func JSON(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Plain is a JSON value without the empty data and children of blocks,
// which one side writes and the other leaves out: both read them alike.
func Plain(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if (k == "data" || k == "children") && empty(e) {
				continue
			}
			out[k] = Plain(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Plain(e)
		}
		return out
	}
	return v
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// Steady renames the IDs of blocks made on the way, which each side draws
// at random, by the order they first come in: block IDs vs have not in
// known become "new-1", "new-2" and so on, wherever they are, in vs walked
// in order, each map by its keys in order. Values two sides made alike
// are alike after, whatever IDs each drew.
func Steady(known map[string]bool, vs ...any) []any {
	names := map[string]string{}
	var learn func(any)
	learn = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				learn(x[k])
			}
			if id, ok := x["id"].(string); ok && x["type"] != nil && !known[id] && names[id] == "" {
				names[id] = fmt.Sprintf("new-%d", len(names)+1)
			}
		case []any:
			for _, e := range x {
				learn(e)
			}
		}
	}
	var rename func(any) any
	rename = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				out[k] = rename(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = rename(e)
			}
			return out
		case string:
			if name, ok := names[x]; ok {
				return name
			}
		}
		return v
	}
	for _, v := range vs {
		learn(v)
	}
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = rename(v)
	}
	return out
}

// Check is how a corpus test reports a case that differs: the first ten
// of them, then, at the end, how many differed in all.
func Check(t testing.TB) func(format string, args ...any) {
	t.Helper()
	failed := 0
	t.Cleanup(func() {
		if failed > 10 {
			t.Errorf("%d cases differ in all", failed)
		}
	})
	return func(format string, args ...any) {
		t.Helper()
		if failed++; failed <= 10 {
			t.Errorf(format, args...)
		}
	}
}

// Show is v as JSON on one line, for a test's message.
func Show(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
