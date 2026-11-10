package golden

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "golden"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "golden", name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func updated(t *testing.T, content string, check func(f *File)) string {
	t.Helper()
	dir := write(t, "cases.json", content)
	*update = true
	defer func() { *update = false }()
	t.Run("update", func(t *testing.T) { check(OpenIn(t, dir, "cases.json")) })
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "golden", "cases.json")) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRawFindsAValueByItsPath(t *testing.T) {
	dir := write(t, "cases.json", `[{"in": "a]}\"", "out": [1, {"k": "v,"}]}, {"in":"b","out":null}]`)
	f := OpenIn(t, dir, "cases.json")
	for want, path := range map[string][]any{
		`"a]}\""`:     {0, "in"},
		`{"k": "v,"}`: {0, "out", 1},
		`"v,"`:        {0, "out", 1, "k"},
		`null`:        {1, "out"},
	} {
		if got := string(f.Raw(path...)); got != want {
			t.Errorf("%v is %s, want %s", path, got, want)
		}
	}
	if _, _, ok := span(f.raw, []any{2}); ok {
		t.Error("a case past the last found")
	}
	if _, _, ok := span(f.raw, []any{0, "none"}); ok {
		t.Error("a key the case has not found")
	}
}

func TestUpdateWritesOnlyWhatDiffers(t *testing.T) {
	const before = "[\n{\"in\":\"<a>\",  \"out\":\"x\"},\n{\"in\":\"b\",\"out\":[1 , 2]}\n]\n"
	got := updated(t, before, func(f *File) {
		f.Check("<b> & é", 0, "out")
		f.Check([]int{1, 2}, 1, "out")
		f.Check(true, 1, "ok")
	})
	if want := "[\n{\"in\":\"<a>\",  \"out\":\"<b> & é\"},\n{\"in\":\"b\",\"out\":[1 , 2],\"ok\":true}\n]\n"; got != want {
		t.Errorf("updated to\n%s\nwant\n%s", got, want)
	}
}

func TestUpdateIndentsAsTheFile(t *testing.T) {
	const before = "{\n  \"cases\": [\n    {\n      \"in\": 1,\n      \"out\": []\n    }\n  ]\n}\n"
	got := updated(t, before, func(f *File) {
		f.Check(map[string]any{"a": []int{1}}, "cases", 0, "out")
		f.Check("new", "cases", 0, "more")
	})
	const want = "{\n  \"cases\": [\n    {\n      \"in\": 1,\n      \"out\": {\n        \"a\": [\n          1\n        ]\n      },\n      \"more\": \"new\"\n    }\n  ]\n}\n"
	if got != want {
		t.Errorf("updated to\n%s\nwant\n%s", got, want)
	}
}

func TestUpdateLeavesAFileThatHolds(t *testing.T) {
	const before = "[\n{\"in\":1,\"out\":{\"b\":1.0,\"a\":\"\\u00e9\"}}\n]\n"
	if got := updated(t, before, func(f *File) { f.Check(map[string]any{"a": "é", "b": 1}, 0, "out") }); got != before {
		t.Errorf("updated to\n%s", got)
	}
}
