package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func longPage(n int) *doc.Content {
	var blocks []*doc.Node
	for i := range n {
		p := &doc.Node{ID: fmt.Sprint("b", i), Type: "paragraph"}
		p.SetDelta(doc.FromMarkdown(fmt.Sprintf("Paragraph %d of the notes, a line of <plain> words & more of them to read.", i)))
		blocks = append(blocks, p)
	}
	return doc.FromBlocks(blocks)
}

func readWhole(t *testing.T, x *Executor, tool string, args map[string]any, total int) []map[string]any {
	t.Helper()
	var parts []map[string]any
	seen := map[string]bool{}
	for offset := 0; ; {
		call := map[string]any{"offset": float64(offset)}
		for k, v := range args {
			call[k] = v
		}
		out := x.Call(tool, call)
		if out["error"] != nil || len(parts) > total {
			t.Fatalf("%s from %d: %v", tool, offset, out["error"])
		}
		parts = append(parts, out)
		if units := len(utf16.Encode([]rune(resultJSON(out)))); units > toolResultLimit {
			t.Fatalf("%s from %d takes %d units, past %d", tool, offset, units, toolResultLimit)
		}
		if out["total_blocks"] != total {
			t.Fatalf("%s from %d: total_blocks %v, want %d", tool, offset, out["total_blocks"], total)
		}
		for _, b := range out["blocks"].([]map[string]any) {
			seen[b["id"].(string)] = true
		}
		next, more := out["next_offset"].(int)
		if !more {
			break
		}
		if next <= offset {
			t.Fatalf("%s from %d reads on from %d", tool, offset, next)
		}
		offset = next
	}
	if len(seen) != total {
		t.Fatalf("%s read %d of %d blocks in %d parts", tool, len(seen), total, len(parts))
	}
	return parts
}

func TestLongPageIsReadInParts(t *testing.T) {
	c := longPage(900)
	x := executor(c)
	x.Ctx.Subpages = func() []map[string]any { return []map[string]any{{"id": "sub", "title": "Appendix"}} }
	x.Ctx.Comments = func() []map[string]any {
		return []map[string]any{{"block_id": "b899", "resolved": false, "thread": []map[string]any{{"author": "Ann", "body": "Cut this?"}}}}
	}
	parts := readWhole(t, x, "read_page", nil, 900)
	if len(parts) < 2 {
		t.Fatalf("900 blocks came in %d part", len(parts))
	}
	if len(parts[0]["comments"].([]map[string]any)) != 1 || len(parts[0]["subpages"].([]map[string]any)) != 1 {
		t.Errorf("the first part has comments %v, subpages %v", parts[0]["comments"], parts[0]["subpages"])
	}
	if parts[1]["comments"] != nil || parts[1]["subpages"] != nil || parts[1]["page"] == nil {
		t.Errorf("the second part has comments %v, subpages %v, page %v", parts[1]["comments"], parts[1]["subpages"], parts[1]["page"])
	}
	if out := x.Call("read_page", map[string]any{"offset": 890.0, "limit": 3.0}); len(out["blocks"].([]map[string]any)) != 3 || out["next_offset"] != 893 {
		t.Errorf("3 blocks from 890: %d, next %v", len(out["blocks"].([]map[string]any)), out["next_offset"])
	}
	for _, bad := range []map[string]any{{"offset": -1.0}, {"offset": 1.5}, {"limit": 0.0}, {"offset": "3"}} {
		if out := x.Call("read_page", bad); out["error"] == nil {
			t.Errorf("read with %v", bad)
		}
	}

	f := &fakeModel{replies: []map[string]any{{"content": "", "tool_calls": []any{call("read_page", map[string]any{})}}, {"content": "Read it."}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m", SupportsTools: true}, x.Ctx, Events{}); err != nil {
		t.Fatal(err)
	}
	if user := f.requests[0]["messages"].([]any)[1].(map[string]any)["content"].(string); !strings.Contains(user, "in parts") || strings.Contains(user, `"b0"`) {
		t.Errorf("the request says of the long page: %.200s", user)
	}
	msgs := f.requests[1]["messages"].([]any)
	var got map[string]any
	if err := json.Unmarshal([]byte(msgs[len(msgs)-1].(map[string]any)["content"].(string)), &got); err != nil {
		t.Fatalf("the model was given a result cut short: %v", err)
	}
	if got["total_blocks"] != 900.0 || got["next_offset"] == nil || len(got["comments"].([]any)) != 1 || len(got["subpages"].([]any)) != 1 {
		t.Errorf("the model was given total %v, next %v, comments %v, subpages %v", got["total_blocks"], got["next_offset"], got["comments"], got["subpages"])
	}
}

func TestBlockTooLongForAPartKeepsWhatFollowsReadable(t *testing.T) {
	c := longPage(3)
	huge := &doc.Node{ID: "huge", Type: "code"}
	huge.SetDelta(doc.Delta{{Text: strings.Repeat("x = 1\n", 20_000)}})
	c.Children = append([]*doc.Node{huge}, c.Children...)
	x := executor(c)
	x.Ctx.Comments = func() []map[string]any {
		return []map[string]any{{"block_id": "b2", "resolved": false, "thread": []map[string]any{{"author": "Ann", "body": "Still true?"}}}}
	}
	out := x.Call("read_page", map[string]any{})
	if blocks := out["blocks"].([]map[string]any); len(blocks) != 1 || out["next_offset"] != 1 {
		t.Fatalf("the part has %d blocks, next %v", len(blocks), out["next_offset"])
	}
	given := cutResult(resultJSON(out))
	head, _, cut := strings.Cut(given, `"blocks":`)
	if !cut || !strings.HasSuffix(given, "…(truncated)") {
		t.Fatalf("the result reads %.80s … %s", given, given[len(given)-40:])
	}
	for _, want := range []string{`"total_blocks":4`, `"next_offset":1`, `"Still true?"`, `"subpages":[]`, `"page":{`} {
		if !strings.Contains(head, want) {
			t.Errorf("no %s before the blocks: %s", want, head)
		}
	}
	if rest := x.Call("read_page", map[string]any{"offset": 1.0}); len(rest["blocks"].([]map[string]any)) != 3 || rest["next_offset"] != nil {
		t.Errorf("after the long block: %v", rest)
	}
}

func TestManyCommentsLeaveRoomForThePage(t *testing.T) {
	x := executor(longPage(50))
	x.Ctx.Comments = func() []map[string]any {
		var out []map[string]any
		for i := range 600 {
			out = append(out, map[string]any{"block_id": fmt.Sprint("b", i%50), "resolved": false,
				"thread": []map[string]any{{"author": "Ann", "body": strings.Repeat("Is this still what we want to say here? ", 4)}}})
		}
		return out
	}
	x.Ctx.Subpages = func() []map[string]any {
		var out []map[string]any
		for i := range 2000 {
			out = append(out, map[string]any{"id": fmt.Sprintf("%08d-0000-0000-0000-000000000000", i), "title": "Meeting notes"})
		}
		return out
	}
	parts := readWhole(t, x, "read_page", nil, 50)
	first := parts[0]
	comments, subpages := len(first["comments"].([]map[string]any)), len(first["subpages"].([]map[string]any))
	if comments == 0 || comments == 600 || first["more_comments"] != 600-comments {
		t.Errorf("%d comment threads given, more %v", comments, first["more_comments"])
	}
	if subpages == 0 || subpages == 2000 || first["more_subpages"] != 2000-subpages {
		t.Errorf("%d subpages given, more %v", subpages, first["more_subpages"])
	}
	if len(first["blocks"].([]map[string]any)) == 0 {
		t.Error("no block in the first part")
	}
}

func TestOtherPagesAreReadInParts(t *testing.T) {
	st := openStore(t)
	here, err := st.CreatePage(store.NewPageOptions{Title: "Here", Content: page()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreatePage(store.NewPageOptions{Title: "Long", Content: longPage(900), Tags: []string{"notes"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Inside", ParentID: other.ID}); err != nil {
		t.Fatal(err)
	}
	x := &Executor{Ctx: PageTools(st, here.ID, "Here", "Test", st.Content(here.ID), true)}
	parts := readWhole(t, x, "read_other_page", map[string]any{"page_id": other.ID}, 900)
	if len(parts) < 2 || len(parts[0]["subpages"].([]map[string]any)) != 1 || len(parts[0]["comments"].([]map[string]any)) != 0 {
		t.Errorf("%d parts, the first with subpages %v, comments %v", len(parts), parts[0]["subpages"], parts[0]["comments"])
	}
	if page := parts[1]["page"].(map[string]any); page["id"] != other.ID || page["title"] != "Long" {
		t.Errorf("the second part is of %v", page)
	}

	h := NewHub(st, nil, linkHost{st: st, linked: new([]string), main: &sync.Mutex{}})
	w := &Executor{Ctx: h.workspaceTools(context.Background(), "Test", store.Settings{AgentsReadOtherPages: true}, true)}
	parts = readWhole(t, w, "read_page", map[string]any{"page_id": other.ID}, 900)
	page := parts[0]["page"].(map[string]any)
	if len(parts) < 2 || len(parts[0]["subpages"].([]map[string]any)) != 1 || fmt.Sprint(page["tags"]) != "[notes]" {
		t.Errorf("%d parts, the first with subpages %v, page %v", len(parts), parts[0]["subpages"], page)
	}
	if _, has := parts[0]["comments"]; has {
		t.Errorf("the workspace read gives comments: %v", parts[0]["comments"])
	}
}
