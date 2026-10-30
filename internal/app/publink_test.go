package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func TestParseTunnelURL(t *testing.T) {
	for line, want := range map[string]string{
		"2026-10-29T10:00:00Z INF |  https://quiet-river-bold-moon.trycloudflare.com                                   |": "https://quiet-river-bold-moon.trycloudflare.com",
		"2026-10-29T10:00:00Z INF Requesting new quick Tunnel on trycloudflare.com...":                                    "",
		`2026-10-29T10:00:00Z ERR failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel": EOF`:       "",
		"2026-10-29T10:00:00Z INF Registered tunnel connection connIndex=0":                                               "",
	} {
		if got := parseTunnelURL(line); got != want {
			t.Errorf("parseTunnelURL(%q) = %q, want %q", line, got, want)
		}
	}
	if v := cloudflaredVersion("cloudflared version 2026.9.3 (built 2026-09-30-1200 UTC)"); v != "2026.9.3" {
		t.Errorf("version %q", v)
	}
	if !(cloudflaredInfo{version: "2026.10.0"}).canRestrict() || (cloudflaredInfo{version: "2026.9.2"}).canRestrict() {
		t.Error("canRestrict compares versions wrong")
	}
}

func TestCloudflaredPrompt(t *testing.T) {
	prompt := cloudflaredPrompt()
	if !strings.Contains(prompt, cloudflaredDownloads) || !strings.Contains(prompt, "cloudflared --version") {
		t.Errorf("prompt %q", prompt)
	}
	if cmd := cloudflaredCommand(); cmd != "" && !strings.Contains(prompt, cmd) {
		t.Errorf("prompt %q lacks %q", prompt, cmd)
	}
}

func TestSplitEmails(t *testing.T) {
	got := splitEmails(" Ann@Example.com, *@team.dev;ann@example.com\nbo@x.io ")
	if want := []string{"ann@example.com", "*@team.dev", "bo@x.io"}; !slices.Equal(got, want) {
		t.Errorf("splitEmails = %q, want %q", got, want)
	}
	if !validEmails(got) || validEmails([]string{"ann"}) || validEmails([]string{"@x.io"}) || validEmails([]string{"a@localhost"}) {
		t.Error("validEmails")
	}
}

// pubTest is an app sharing one page through a link whose server runs
// without a tunnel.
func pubTest(t *testing.T) (*App, *pubLink, http.Handler, store.Page) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	pic := filepath.Join(st.Dir, "files", "dawn.png")
	if err := os.MkdirAll(filepath.Dir(pic), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pic, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.FromMarkdown("Hello **world**"))
	img := &doc.Node{ID: "i1", Type: "image", Data: map[string]any{"url": "file://" + pic, "alt": "dawn"}}
	away := &doc.Node{ID: "i2", Type: "image", Data: map[string]any{"url": "file:///etc/hosts.png"}}
	embed := &doc.Node{ID: "e1", Type: "embed", Data: map[string]any{"title": "Piano", "html": "<html><head></head><body><script>play()</script></body></html>", "height": 300}}
	pg, err := st.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks([]*doc.Node{para, img, away, embed})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	p := &pubLink{page: pg.ID, token: "tok", state: "live", comments: true, threads: map[string]bool{}, posts: map[string][]time.Time{}}
	a.pub = p
	p.render(a.pubJob(p))
	return a, p, a.pubHandler(p), pg
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestPubServesOnlyThePage(t *testing.T) {
	_, _, h, _ := pubTest(t)
	w := get(h, "/tok/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Hello <strong>world</strong>") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || w.Header().Get("X-Robots-Tag") == "" {
		t.Errorf("headers %v", w.Header())
	}
	if strings.Contains(w.Body.String(), "/etc/hosts") || strings.Contains(w.Body.String(), "file://") {
		t.Errorf("a file of the device's own leaked: %s", w.Body)
	}
	if w := get(h, "/tok/f/1.png"); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("picture: %d %v", w.Code, w.Header())
	}
	for _, path := range []string{"/", "/tak/", "/tokx/", "/tok/f/2.png", "/tok/f/../../etc/hosts", "/tok/other"} {
		if w := get(h, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/tok/", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	if w := get(h, "/tok/comments.js"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "leaflet-name") {
		t.Errorf("comments script: %d", w.Code)
	}
	if w := get(h, "/tok/favicon.png"); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(get(h, "/tok/").Body.String(), `<link rel="icon" type="image/png" href="/tok/favicon.png">`) {
		t.Errorf("favicon: %d %v", w.Code, w.Header())
	}

	// An embed runs in its own sandbox, off the network, never in the page.
	if page := get(h, "/tok/").Body.String(); strings.Contains(page, "play()") || !strings.Contains(page, `<iframe sandbox="allow-scripts" src="/tok/e/e1"`) {
		t.Errorf("embed in the page: %s", page)
	}
	w = get(h, "/tok/e/e1")
	if csp := w.Header().Get("Content-Security-Policy"); w.Code != http.StatusOK || !strings.Contains(csp, "sandbox allow-scripts") ||
		!strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "connect-src") {
		t.Errorf("embed: %d %q", w.Code, csp)
	}
	if body := w.Body.String(); !strings.HasPrefix(body, `<meta charset`) || !strings.Contains(body, "RTCPeerConnection") || !strings.Contains(body, "play()") || !strings.Contains(body, "leafletSize") {
		t.Errorf("embed page: %s", body)
	}
	if w := get(h, "/tok/e/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown embed: %d", w.Code)
	}
}

func TestCodeHTML(t *testing.T) {
	got := codeHTML("func f() { return \"<b>\" } // x", "go")
	if !strings.Contains(got, `<span class="k1">func</span>`) || !strings.Contains(got, "&lt;b&gt;") || strings.Contains(got, "<b>") {
		t.Errorf("codeHTML = %s", got)
	}
	if got := codeHTML("a < b", "nope"); got != "a &lt; b" {
		t.Errorf("unknown language: %s", got)
	}
	if !strings.Contains(pubLook(), ".k1{color:") || !strings.Contains(pubLook(), "--tip-icon:") {
		t.Error("pubLook lacks token or callout colors")
	}
}

func TestPubFollowsSaves(t *testing.T) {
	a, p, h, pg := pubTest(t)
	c := a.st.Content(pg.ID)
	c.Find("p1").SetDelta(doc.FromMarkdown("Changed"))
	if _, _, err := a.st.SaveContent(pg.ID, c); err != nil {
		t.Fatal(err)
	}
	p.render(a.pubJob(p))
	if w := get(h, "/tok/"); !strings.Contains(w.Body.String(), "Changed") {
		t.Errorf("the page did not follow the save: %s", w.Body)
	}
}

func TestPubCommentBody(t *testing.T) {
	got := pubCommentBody(store.Comment{Body: "**LWT** sets a retained `offline` <b>will</b>\nsee [docs](javascript:x)"})
	want := "<strong>LWT</strong> sets a retained <code>offline</code> &lt;b&gt;will&lt;/b&gt;<br>see docs"
	if got != want {
		t.Errorf("pubCommentBody = %s, want %s", got, want)
	}
}

func postComment(h http.Handler, origin, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/tok/comments", strings.NewReader(body))
	r.Host = "quiet-river.trycloudflare.com"
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPubComments(t *testing.T) {
	a, p, h, pg := pubTest(t)
	const origin = "https://quiet-river.trycloudflare.com"
	internal, err := a.st.AddComment(pg.ID, "p1", "Private note", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	w := postComment(h, origin, `{"block":"p1","name":"Ann","body":"Looks good"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("comment: %d %s", w.Code, w.Body)
	}
	threads, _ := a.st.Threads(pg.ID)
	var guest store.Comment
	for _, th := range threads {
		if th.Root.AuthorKind == "guest" {
			guest = th.Root
		}
	}
	if guest.AuthorName != "Ann" || guest.Body != "Looks good" || guest.BlockID != "p1" {
		t.Fatalf("stored %+v", guest)
	}
	if w := postComment(h, origin, `{"block":"p1","name":"Bo","body":"Which world?","quote":"Hello   world"}`); w.Code != http.StatusNoContent {
		t.Fatalf("selection comment: %d", w.Code)
	}
	if w := postComment(h, origin, `{"block":"p1","name":"Bo","body":"Made up","quote":"not in the block"}`); w.Code != http.StatusNoContent {
		t.Fatalf("comment with a stray quote: %d", w.Code)
	}
	threads, _ = a.st.Threads(pg.ID)
	quotes := map[string]string{}
	for _, th := range threads {
		quotes[th.Root.Body] = th.Root.Quote
	}
	if quotes["Which world?"] != "Hello world" || quotes["Made up"] != "" {
		t.Errorf("quotes %q", quotes)
	}
	if _, err := a.st.AddComment(pg.ID, "p1", "Thanks Ann", guest.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	p.render(a.pubJob(p))
	page := get(h, "/tok/").Body.String()
	if !strings.Contains(page, `class="cbadge" data-for="p1"`) || !strings.Contains(page, `id="cpanel"`) || !strings.Contains(page, `<p class="tquote">Hello world</p>`) {
		t.Errorf("no badge, panel or quote:\n%s", page)
	}
	if !strings.Contains(page, "Looks good") || !strings.Contains(page, "Thanks Ann") || strings.Contains(page, "Private note") {
		t.Errorf("visitors should see this link's threads only:\n%s", page)
	}

	// cloudflared may hand the request on under another Host: the
	// tunnel's own hostname is the page's origin all the same.
	p.host = "quiet-river.trycloudflare.com"
	r := httptest.NewRequest(http.MethodPost, "/tok/comments", strings.NewReader(`{"block":"p1","name":"Cy","body":"Via the tunnel"}`))
	r.Host = "127.0.0.1:50331"
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.8")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() != http.StatusNoContent {
		t.Errorf("a post under the origin's Host was refused")
	}

	if w := postComment(h, origin, `{"parent":"`+guest.ID+`","name":"Ann","body":"Welcome"}`); w.Code != http.StatusNoContent {
		t.Errorf("reply: %d", w.Code)
	}

	// A thread the owner resolved takes no more replies, and the refusal
	// counts against no one's limit.
	done, err := a.st.AddComment(pg.ID, "p1", "Fixed?", "", "", "guest", "Ann")
	if err != nil {
		t.Fatal(err)
	}
	p.threads[done.ID] = true
	if err := a.st.SetResolved(done.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := postComment(h, origin, `{"parent":"`+done.ID+`","name":"Ann","body":"Late"}`); w.Code != http.StatusBadRequest {
		t.Errorf("reply to a resolved thread: %d", w.Code)
	}

	// Only a link kept to emails is behind Access: on any other, the
	// header a visitor sends names no one.
	byEmail := func() string {
		r := httptest.NewRequest(http.MethodPost, "/tok/comments", strings.NewReader(`{"block":"p1","name":"Dee","body":"Signed?"}`))
		r.Host = "quiet-river.trycloudflare.com"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
		r.Header.Set(accessEmailHeader, "boss@example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("signed comment: %d", w.Code)
		}
		ts, _ := a.st.Threads(pg.ID)
		return ts[len(ts)-1].Root.AuthorName
	}
	if name := byEmail(); name != "Dee" {
		t.Errorf("an open link took the visitor's header: %q", name)
	}
	p.emails = []string{"boss@example.com"}
	if name := byEmail(); name != "boss@example.com" {
		t.Errorf("a link kept to emails named %q", name)
	}
	p.emails = nil
	for name, tc := range map[string]struct {
		origin, body string
		want         int
	}{
		"other site":     {"https://evil.example", `{"block":"p1","name":"E","body":"x"}`, http.StatusForbidden},
		"no origin":      {"", `{"block":"p1","name":"E","body":"x"}`, http.StatusForbidden},
		"unknown block":  {origin, `{"block":"nope","name":"E","body":"x"}`, http.StatusBadRequest},
		"private thread": {origin, `{"parent":"` + internal.ID + `","name":"E","body":"x"}`, http.StatusBadRequest},
		"no name":        {origin, `{"block":"p1","name":" ","body":"x"}`, http.StatusBadRequest},
		"too long":       {origin, `{"block":"p1","name":"E","body":"` + strings.Repeat("x", pubMaxComment+1) + `"}`, http.StatusBadRequest},
	} {
		if w := postComment(h, tc.origin, tc.body); w.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, w.Code, tc.want)
		}
	}

	// Four posted; the visitor may post one more this minute.
	for i := range 1 {
		if w := postComment(h, origin, `{"block":"p1","name":"Ann","body":"more"}`); w.Code != http.StatusNoContent {
			t.Fatalf("post %d: %d", i, w.Code)
		}
	}
	if w := postComment(h, origin, `{"block":"p1","name":"Ann","body":"too many"}`); w.Code != http.StatusTooManyRequests {
		t.Errorf("past the limit: %d", w.Code)
	}

	a.setPubComments(false)
	p.posts = map[string][]time.Time{}
	if w := postComment(h, origin, `{"block":"p1","name":"Ann","body":"x"}`); w.Code != http.StatusForbidden {
		t.Errorf("comments off: %d", w.Code)
	}
}

// A fake cloudflared prints its address and waits: the link goes live,
// and stopping it ends the process.
func TestPubTunnelLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake cloudflared is a shell script")
	}
	a, _, _, pg := pubTest(t)
	a.stopPub()
	tool := filepath.Join(t.TempDir(), "cloudflared")
	script := "#!/bin/sh\necho 'INF |  https://quiet-river.trycloudflare.com  |' >&2\nexec sleep 30\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { // #nosec G306 -- a test's script, run by the test.
		t.Fatal(err)
	}
	posts := make(chan func(), 16)
	a.post = func(fn func()) { posts <- fn }
	a.cloudflared = cloudflaredInfo{checked: true, path: tool, version: "2026.10.0"}

	a.startPub(pg.ID, nil, false)
	p := a.pub
	deadline := time.After(10 * time.Second)
	for p.state != "live" {
		select {
		case fn := <-posts:
			fn()
		case <-deadline:
			t.Fatalf("still %s: %s", p.state, p.err)
		}
	}
	if !strings.HasPrefix(p.url, "https://quiet-river.trycloudflare.com/") || !strings.HasSuffix(p.url, "/"+p.token+"/") {
		t.Errorf("url %q", p.url)
	}
	// Other emails take a new link, which keeps the visitors' threads.
	p.threads["t1"] = true
	proc := p.cmd.Process
	a.startPub(pg.ID, []string{"ann@example.com"}, false)
	if a.pub == p || !a.pub.threads["t1"] {
		t.Error("the new link lost the visitors' threads")
	}
	a.stopPub()
	// The link's own wait reaps the process once it is killed.
	for start := time.Now(); proc.Signal(syscall.Signal(0)) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("cloudflared still runs after the link stopped")
		}
	}
	if a.pub != nil {
		t.Error("the link is still set")
	}
}

// A page shared through a public link keeps agents to itself, from the
// moment the link starts until it stops.
func TestPublicLinkKeepsAgentsToThePage(t *testing.T) {
	a := &App{}
	a.setPub(&pubLink{page: "p1", state: "starting"})
	if !a.publiclyLinked("p1") || a.publiclyLinked("p2") {
		t.Error("the linked page is not the one shared")
	}
	a.setPub(nil)
	if a.publiclyLinked("p1") {
		t.Error("a stopped link still counts")
	}
}

// The page tells the diagram script its full-window button's words and
// icons, whether or not visitors may comment.
func TestPubHeadDiagramWords(t *testing.T) {
	head := pubHead("/tok/", false)
	for _, name := range []string{"full", "full-icon", "exit", "exit-icon", "fit", "fit-icon", "out", "out-icon", "in", "in-icon"} {
		if !strings.Contains(head, `<meta name="leaflet-`+name+`" content="`) {
			t.Errorf("no %s in %s", name, head)
		}
	}
	if !strings.Contains(head, ".dfull{") {
		t.Errorf("no full-window look in %s", head)
	}
}

// A shared page shows each block as the editor does: charts drawn, due
// dates, link cards with their pictures, files to download, recordings
// to play with their transcripts, and folded headings folded.
func TestPubBlocksAsInTheEditor(t *testing.T) {
	a, p, h, pg := pubTest(t)
	dir := a.st.Dir
	pdf := filepath.Join(dir, "files", "notes.pdf")
	wav := filepath.Join(dir, "recordings", "r1.wav")
	for _, f := range []string{pdf, wav} {
		if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.st.PutFile(store.File{ID: "f1", PageID: pg.ID, Name: "Notes.pdf", LocalPath: pdf, MimeType: "application/pdf", SizeBytes: 2048}); err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutRecording(store.Recording{ID: "r1", PageID: pg.ID, BlockID: "rec", LocalPath: wav, DurationMs: 65000, Status: "ready",
		Transcript: []store.Segment{{StartMs: 1500, EndMs: 4000, Text: "Hello there.", Speaker: "1"}}}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()

	text := func(id, typ, s string) *doc.Node {
		n := &doc.Node{ID: id, Type: typ}
		n.SetDelta(doc.FromMarkdown(s))
		return n
	}
	todo := text("td", "todo_list", "Pack")
	todo.SetDue(time.Date(2031, 3, 4, 9, 30, 0, 0, time.Local), true)
	folded := text("h1", "heading", "Later")
	folded.Set("level", 2)
	folded.Set("collapsed", true)
	next := text("h2", "heading", "Next")
	next.Set("level", 2)
	bars, _ := doc.ChartData(map[string]any{"chart_type": "bar", "title": "Pages", "labels": []any{"A", "B"},
		"series": []any{map[string]any{"name": "n", "values": []any{3.0, 5.0}}}})
	pie, _ := doc.ChartData(map[string]any{"chart_type": "pie", "title": "Week", "labels": []any{"Work", "Rest"},
		"series": []any{map[string]any{"name": "h", "values": []any{3.0, 1.0}}}})
	c := doc.FromBlocks([]*doc.Node{
		todo,
		{ID: "c1", Type: "chart", Data: bars},
		{ID: "c2", Type: "chart", Data: pie},
		{ID: "lk", Type: "link_preview", Data: map[string]any{"url": "https://www.example.com/a", "title": "Example", "description": "A page",
			"image": "https://example.com/a.png"}},
		{ID: "fl", Type: "file", Data: map[string]any{"file_id": "f1", "name": "Notes.pdf"}},
		{ID: "gone", Type: "file", Data: map[string]any{"file_id": "nope", "name": "Old.zip"}},
		{ID: "rec", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": ""}},
		folded, text("p2", "paragraph", "Hidden"), next,
	})
	if _, _, err := a.st.SaveContent(pg.ID, c); err != nil {
		t.Fatal(err)
	}
	p.render(a.pubJob(p))
	page := get(h, "/tok/").Body.String()
	for _, want := range []string{
		`<span class="due">`, locales.EditorDueAt(locales.EditorDueDate(time.Date(2031, 3, 4, 9, 30, 0, 0, time.Local)), time.Date(2031, 3, 4, 9, 30, 0, 0, time.Local)),
		`<div class="plot"><div class="area">`, `background:var(--accent)`, `<span class="x" style="left:`,
		`<svg class="pie"`, `<div class="legend">`, locales.EditorPieShare(0.75),
		`<a id="b-lk" data-block="lk" class="card link" href="https://www.example.com/a"`, `<span class="desc">A page</span>`, `example.com</span>`, `<img src="https://example.com/a.png"`,
		`class="card file" href="/tok/f/`, `download="Notes.pdf"`, locales.CardFileDetail("PDF", humanSize(2048)),
		`<div id="b-gone" data-block="gone" class="card file">`, locales.CardNotOnDevice(),
		`<strong>` + locales.CommonRecording() + `</strong>`, `<span class="muted">1:05</span>`, `<audio controls preload="metadata" src="/tok/f/`,
		`<p data-start="1.5"><span class="ts">0:01</span><span class="line"><span class="speaker">` + locales.RecSpeaker("1") + `</span>Hello there.</span></p>`,
		`<script src="/tok/recording.js" defer></script>`,
		"<details class=\"fold\"><summary><h2 id=\"b-h1\" data-block=\"h1\">Later</h2>\n</summary>\n<p id=\"b-p2\" data-block=\"p2\">Hidden</p>\n</details>\n<h2 id=\"b-h2\"",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q in\n%s", want, page)
		}
	}
	if strings.Contains(page, "<table>") || strings.Contains(page, dir) {
		t.Errorf("a chart as a table, or a path of the device's: %s", page)
	}

	src := func(after string) string {
		_, rest, _ := strings.Cut(page, after)
		s, _, _ := strings.Cut(rest, `"`)
		return s
	}
	w := get(h, src(`class="card file" href="`))
	if w.Code != http.StatusOK || w.Body.String() != "0123456789" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("file: %d %v %q", w.Code, w.Header(), w.Body)
	}
	r := httptest.NewRequest(http.MethodGet, src(`<audio controls preload="metadata" src="`), nil)
	r.Header.Set("Range", "bytes=2-4")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" || w.Header().Get("Content-Type") != "audio/wav" || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("audio: %d %v %q", w.Code, w.Header(), w.Body)
	}
	if w := get(h, "/tok/recording.js"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "currentTime") {
		t.Errorf("recording script: %d", w.Code)
	}
}

// A recording of the page that finishes shows its transcript to visitors.
func TestPubFollowsRecordings(t *testing.T) {
	a, p, h, pg := pubTest(t)
	r, err := a.st.CreateRecording(pg.ID, "rec", "local", "model", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c := a.st.Content(pg.ID)
	c.Children = append(c.Children, &doc.Node{ID: "rec", Type: "recording", Data: map[string]any{"recording_id": r.ID}})
	if _, _, err := a.st.SaveContent(pg.ID, c); err != nil {
		t.Fatal(err)
	}
	p.render(a.pubJob(p))
	if _, err := a.st.UpdateRecording(r.ID, func(r *store.Recording) {
		r.Status, r.Transcript = "ready", []store.Segment{{Text: "new transcript"}}
	}); err != nil {
		t.Fatal(err)
	}
	a.pubChanged(store.Change{Table: "recordings", PageID: pg.ID})
	if p.timer == nil {
		t.Fatal("no render after the recording changed")
	}
	p.timer.Stop()
	p.render(a.pubJob(p))
	if w := get(h, "/tok/"); !strings.Contains(w.Body.String(), "new transcript") {
		t.Errorf("the page did not follow the recording: %s", w.Body)
	}
}

// A comment the app took too long to take is not stored after the
// visitor was told so, nor counted against them.
func TestPubCommentTimedOut(t *testing.T) {
	a, p, h, pg := pubTest(t)
	wait := pubAnswerWait
	pubAnswerWait = 10 * time.Millisecond
	t.Cleanup(func() { pubAnswerWait = wait })
	var held []func()
	a.post = func(fn func()) { held = append(held, fn) }
	if w := postComment(h, "https://quiet-river.trycloudflare.com", `{"block":"p1","name":"Ann","body":"Late"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("comment: %d %s", w.Code, w.Body)
	}
	for _, fn := range held {
		fn()
	}
	if threads, _ := a.st.Threads(pg.ID); len(threads) != 0 {
		t.Errorf("stored after it timed out: %+v", threads)
	}
	if p.total != 0 || len(p.posts["203.0.113.7"]) != 0 {
		t.Errorf("counted: %d, %v", p.total, p.posts)
	}
}
