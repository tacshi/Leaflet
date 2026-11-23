package mobile

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func TestThawedOpsLeaveEarlierRevsAsTheyWere(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) // #nosec G404
	ids := 0
	newID := func() string {
		ids++
		return fmt.Sprint("n", ids)
	}
	item := func(typ string, kids ...*doc.Node) *doc.Node {
		n := paragraph(newID(), "text")
		n.Type = typ
		n.Children = kids
		return n
	}
	var top []*doc.Node
	for range 12 {
		top = append(top, item("bulleted_list", item("paragraph"), item("numbered_list", item("todo_list"))))
	}
	table := &doc.Node{ID: newID(), Type: "table", Data: map[string]any{"rowsLen": 1, "colsLen": 2}}
	for col := range 2 {
		cell := &doc.Node{ID: newID(), Type: "table/cell", Data: map[string]any{"rowPosition": 0, "colPosition": col}}
		cell.Children = []*doc.Node{paragraph(newID(), "cell")}
		table.Children = append(table.Children, cell)
	}
	top = append(top, table)
	c := doc.FromBlocks(top)

	pick := func(c *doc.Content) string {
		all := c.All()
		return all[r.IntN(len(all))].ID
	}
	position := func(c *doc.Content) *doc.Position {
		switch r.IntN(6) {
		case 0:
			return &doc.Position{After: pick(c)}
		case 1:
			return &doc.Position{Before: pick(c)}
		case 2:
			return &doc.Position{Parent: pick(c)}
		case 3:
			return &doc.Position{At: "start"}
		case 4:
			return &doc.Position{After: "gone"}
		}
		return &doc.Position{At: "end"}
	}
	var revs []*doc.Content
	var coded []string
	failed, applied := 0, 0
	for range 3000 {
		var op doc.Op
		switch r.IntN(5) {
		case 0, 1:
			op = doc.InsertOp(*position(c), item("bulleted_list", item("paragraph")))
		case 2:
			id := pick(c)
			n := item("numbered_list")
			if prev := c.Find(id); prev != nil && r.IntN(2) == 0 {
				n.Children = prev.Clone().Children
				op = doc.Op{Op: "replace", ID: id, Node: n, PrevNode: prev.Clone()}
			} else {
				op = doc.Op{Op: "replace", ID: id, Node: n}
			}
		case 3:
			op = doc.Op{Op: "move", ID: pick(c), Position: position(c)}
		default:
			op = doc.Op{Op: "delete", IDs: []string{pick(c), pick(c)}}
		}
		whole := c.Copy()
		wantErr := whole.Apply(op)
		thawedC, _ := thawed(c, op)
		gotErr := thawedC.Apply(op)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("op %+v: on a copy %v, thawed %v", op, wantErr, gotErr)
		}
		if gotErr != nil {
			failed++
			continue
		}
		if got, want := thawedC.Encode(), whole.Encode(); got != want {
			t.Fatalf("op %+v made\n%s\nnot\n%s", op, got, want)
		}
		revs = append(revs, c)
		coded = append(coded, c.Encode())
		c = thawedC
		applied++
		if len(c.All()) > 400 {
			c = thawedC.Copy()
			c.Children = c.Children[:len(c.Children)/2]
		}
	}
	for i, rev := range revs {
		if rev.Encode() != coded[i] {
			t.Fatalf("rev %d changed after the ops that came after it", i)
		}
	}
	if applied < 1000 || failed == 0 {
		t.Fatalf("%d ops applied and %d failed: the ops don't cover enough", applied, failed)
	}
}

func TestPageEncodesAsItsContent(t *testing.T) {
	p := &openPage{}
	c := doc.FromBlocks([]*doc.Node{paragraph("a", "<One> & two"), paragraph("b", "Two")})
	for range 3 {
		if got, want := p.encode(c), c.Encode(); got != want {
			t.Fatalf("encoded\n%s\nnot\n%s", got, want)
		}
		next, _ := thawed(c, doc.InsertOp(doc.Position{After: "a"}, paragraph(fmt.Sprint("x", len(c.Children)), "new")))
		if err := next.Apply(doc.InsertOp(doc.Position{After: "a"}, paragraph(fmt.Sprint("x", len(c.Children)), "new"))); err != nil {
			t.Fatal(err)
		}
		c = next
	}
	stranded := doc.FromBlocks([]*doc.Node{{ID: "q", Type: "quote", Children: []*doc.Node{paragraph("in", "x")}}})
	if got, want := p.encode(stranded), stranded.Lifted().Encode(); got != want {
		t.Fatalf("encoded\n%s\nnot\n%s", got, want)
	}
	if got, want := p.encode(&doc.Content{}), (&doc.Content{}).Encode(); got != want {
		t.Fatalf("encoded\n%s\nnot\n%s", got, want)
	}
}

func TestAgentRevsShareTheBlocksTheyHaveAlike(t *testing.T) {
	c, ev := open(t)
	row := create(t, c, `{"title":"Long"}`)
	pg := openEditor(t, c, row.ID)
	var blocks []*doc.Node
	for i := range 2000 {
		blocks = append(blocks, paragraph(fmt.Sprint("p", i), "A paragraph of a long page."))
	}
	if _, err := c.EditPage(row.ID, pg.Editor, doc.FromBlocks(blocks).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		c.q.do(func() {
			p := c.pages[row.ID]
			if err := p.ApplyOp(doc.InsertOp(doc.Position{After: "p10"}, paragraph(fmt.Sprint("w", i), "written")), false); err != nil {
				t.Error(err)
			}
			p.show()
		})
		u := wait(t, ev, "agent")
		if got := doc.MustDecode(u.content); len(got.Children) != 2001+i || got.Children[11].ID != fmt.Sprint("w", i) {
			t.Fatalf("the app got %d blocks", len(got.Children))
		}
	}
	c.q.do(func() {
		e := c.pages[row.ID].editors[pg.Editor]
		if len(e.seen) != 6 {
			t.Fatalf("kept %d revs", len(e.seen))
		}
		var first *doc.Content
		total := 0
		for r, kept := range e.seen {
			if first == nil {
				first = kept
			}
			if kept.Find("p1999") != first.Find("p1999") {
				t.Errorf("rev %d has a copy of a block no op touched", r)
			}
			total += e.sizes[r]
		}
		if edit := len(doc.FromBlocks(blocks).Encode()); total > edit+5*(8*2001+4096) {
			t.Errorf("the revs hold about %d bytes, for a page of %d", total, edit)
		}
	})
}

func TestEditorKeepsRevsBoundedByBytes(t *testing.T) {
	e := &pageEditor{seen: map[int64]*doc.Content{}, last: 1}
	e.saw(1, doc.Empty(), 1000)
	for r := int64(2); r <= 40; r++ {
		e.saw(r, doc.Empty(), 1000)
	}
	if len(e.seen) != keepRevs+1 || e.seen[1] == nil || e.seen[40] == nil || e.seen[8] != nil || e.seen[9] == nil {
		t.Fatalf("kept %d revs of small ones", len(e.seen))
	}
	for r := int64(41); r <= 45; r++ {
		e.saw(r, doc.Empty(), keepBytes/4)
	}
	total := 0
	for r := range e.seen {
		total += e.sizes[r]
	}
	if total > keepBytes || e.seen[1] == nil || e.seen[45] == nil || e.seen[41] != nil {
		t.Fatalf("kept %d bytes in %d revs", total, len(e.seen))
	}
	e.saw(46, doc.Empty(), 2*keepBytes)
	if len(e.seen) != 2 || e.seen[1] == nil || e.seen[46] == nil {
		t.Fatalf("kept revs %v", e.sizes)
	}
}

func TestEditorKeepsTheRevItShows(t *testing.T) {
	e := &pageEditor{seen: map[int64]*doc.Content{}, last: 1}
	for r := int64(1); r <= 5; r++ {
		e.saw(r, doc.Empty(), 1000)
	}
	if err := e.showed(3, nil, 0); err != nil {
		t.Fatal(err)
	}
	if e.last != 3 || e.seen[2] != nil || e.seen[3] == nil || e.seen[5] == nil {
		t.Fatalf("showing rev 3 kept revs %v from %d", e.sizes, e.last)
	}
	for r := int64(6); r <= 60; r++ {
		e.saw(r, doc.Empty(), 1000)
	}
	if len(e.seen) != keepRevs+1 || e.seen[3] == nil || e.seen[60] == nil {
		t.Fatalf("kept revs %v", e.sizes)
	}
	if err := e.showed(2, nil, 0); err != nil || e.last != 3 {
		t.Fatalf("a rev before the one shown gave %v and moved it to %d", err, e.last)
	}
	if err := e.showed(10, nil, 0); !errors.Is(err, errRevGone) || e.last != 3 || e.seen[3] == nil {
		t.Fatalf("a rev no longer kept gave %v, the editor at %d", err, e.last)
	}
	shown := doc.Empty()
	if err := e.showed(10, shown, 100); err != nil || e.last != 10 || e.seen[10] != shown || e.seen[3] != nil {
		t.Fatalf("what the editor shows at a rev no longer kept gave %v, the editor at %d", err, e.last)
	}
}

func writer(t *testing.T, c *Core, ev *fakeEvents, page, block string) func(text string) update {
	return func(text string) update {
		t.Helper()
		c.q.do(func() {
			p := c.pages[page]
			if err := p.ApplyOp(doc.Op{Op: "replace", ID: block, Node: paragraph(block, text)}, false); err != nil {
				t.Error(err)
			}
			p.show()
		})
		return wait(t, ev, "agent")
	}
}

// startShown opens a page of two paragraphs in an editor, and has an agent
// write into the second once: the rev of that write is the one the editor
// goes on to show.
func startShown(t *testing.T) (*Core, *fakeEvents, string, openedPage, update) {
	t.Helper()
	c, ev := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	pg := openEditor(t, c, row.ID)
	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One"), paragraph("b", "Two")})
	if _, err := c.EditPage(row.ID, pg.Editor, start.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	return c, ev, row.ID, pg, writer(t, c, ev, row.ID, "b")("Two v1")
}

func editOver(shown update) string {
	edit := doc.MustDecode(shown.content)
	edit.Find("a").SetDelta(doc.Delta{{Text: "One!"}})
	return edit.Encode()
}

func mergedOnce(t *testing.T, c *Core, ev *fakeEvents, want string) {
	t.Helper()
	if got := texts(doc.MustDecode(wait(t, ev, "merge").content)); got != want {
		t.Fatalf("merged %q, want %q", got, want)
	}
	if raw, err := c.Conflicts(); err != nil || raw != "[]" {
		t.Fatalf("conflicts %s, %v", raw, err)
	}
}

// While the app composes text, the agent's revs wait for it, and the next
// edit is over the rev the editor showed before: however many revs come in
// the meantime, the edit merges with all of them.
func TestTheRevAnEditorShowsIsKeptForItsEdit(t *testing.T) {
	c, ev, id, pg, shown := startShown(t)
	if err := c.ShowedRev(id, pg.Editor, shown.rev, ""); err != nil {
		t.Fatal(err)
	}
	write := writer(t, c, ev, id, "b")
	for i := 2; i <= keepRevs+8; i++ {
		write(fmt.Sprint("Two v", i))
	}
	if _, err := c.EditPage(id, pg.Editor, editOver(shown), shown.rev); err != nil {
		t.Fatal(err)
	}
	mergedOnce(t, c, ev, fmt.Sprint("One!|Two v", keepRevs+8))
}

// Without the rev an edit is over, the page can't tell the editor's
// changes from the ones it was shown: merging against an older rev would
// undo every write in between, so the edit is refused.
func TestAnEditOverARevNoLongerKeptIsRefused(t *testing.T) {
	c, ev, id, pg, shown := startShown(t)
	write := writer(t, c, ev, id, "b")
	for i := 2; i <= keepRevs+8; i++ {
		write(fmt.Sprint("Two v", i))
	}
	if _, err := c.EditPage(id, pg.Editor, editOver(shown), shown.rev); !errors.Is(err, errRevGone) {
		t.Fatalf("the edit over a rev no longer kept gave %v", err)
	}
	c.q.do(func() {
		if got := texts(c.pages[id].cur); got != fmt.Sprint("One|Two v", keepRevs+8) {
			t.Errorf("the page holds %q", got)
		}
	})
}

// Revs may go before the editor says it shows one, when many come in the
// while: the editor gives what it shows there, and its edit merges as if
// the rev had been kept.
func TestARevGoneBeforeTheEditorShowedItIsKeptFromWhatItShows(t *testing.T) {
	c, ev, id, pg, shown := startShown(t)
	write := writer(t, c, ev, id, "b")
	for i := 2; i <= keepRevs+8; i++ {
		write(fmt.Sprint("Two v", i))
	}
	if err := c.ShowedRev(id, pg.Editor, shown.rev, ""); !errors.Is(err, errRevGone) {
		t.Fatalf("showing a rev no longer kept gave %v", err)
	}
	if err := c.ShowedRev(id, pg.Editor, shown.rev, shown.content); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EditPage(id, pg.Editor, editOver(shown), shown.rev); err != nil {
		t.Fatal(err)
	}
	mergedOnce(t, c, ev, fmt.Sprint("One!|Two v", keepRevs+8))
	if err := c.ShowedRev(id, pg.Editor+1, shown.rev, ""); !errors.Is(err, errNotOpen) {
		t.Fatalf("another editor's rev gave %v", err)
	}
}

func TestOpeningReadsTheMembersOfSharedPages(t *testing.T) {
	c, _ := open(t)
	asked := make(chan string, 8)
	signInRockyTo(t, c, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/v1/rpc/page_members" {
			body, _ := io.ReadAll(r.Body)
			asked <- string(body)
			_, _ = io.WriteString(w, `[]`)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	own := create(t, c, `{"title":"Mine"}`)
	openEditor(t, c, own.ID)
	const shared = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := c.core.Store.PutRemotePage(map[string]any{"id": shared, "space_id": "00000000-0000-0000-0000-0000000000cc",
		"title": "Theirs", "sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	openEditor(t, c, shared)
	select {
	case body := <-asked:
		if !strings.Contains(body, shared) {
			t.Fatalf("the members read first were of %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shared page's members were not read")
	}
	select {
	case body := <-asked:
		t.Fatalf("members read again, of %s", body)
	case <-time.After(300 * time.Millisecond):
	}
}
