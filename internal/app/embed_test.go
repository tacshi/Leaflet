package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/locales"
)

// An embed's ID goes into the wrapper's script as a string it cannot end.
func TestEmbedPageQuotesID(t *testing.T) {
	page := rendered.EmbedPage("x</script><script>window.mygo.call(1)//\u2028", "<p>hi</p>", "#ffffff", false, true)
	// The frame's HTML is in an attribute, so the wrapper's own script is
	// the only one.
	if n := strings.Count(page, "<script>"); n != 1 {
		t.Errorf("%d scripts in the wrapper: %s", n, page)
	}
	if !strings.Contains(page, `var id = "x\u003c/script\u003e\u003cscript\u003ewindow.mygo.call(1)//\u2028";`) {
		t.Errorf("id not escaped: %s", page)
	}
	if !strings.Contains(page, `; frame-src about:">`) {
		t.Errorf("the wrapper lets frames load data URLs: %s", page)
	}
}

// The frame's policy and WebRTC guard come before anything of the embed's,
// even a "<head>" the embed only mentions.
func TestEmbedFrameLeadsWithPolicy(t *testing.T) {
	csp := `<meta http-equiv="Content-Security-Policy" content="` + rendered.EmbedCSP + `">`
	got := rendered.EmbedFrame(`<!-- <head> --><script>new RTCPeerConnection()</script>`, "dark", true)
	if !strings.HasPrefix(got, csp+rendered.EmbedNoRTC) {
		t.Errorf("frame starts %q", got[:min(len(got), 120)])
	}
	if !strings.Contains(got, `dataset.theme = "dark"`) || !strings.Contains(got, "leafletWheel") || !strings.Contains(got, "leafletSize") {
		t.Error("frame lacks its theme, wheel or fit script")
	}
	if !strings.HasSuffix(got, `<!-- <head> --><script>new RTCPeerConnection()</script>`) {
		t.Errorf("embed's HTML not last: %q", got)
	}
	// A doctype stays first, keeping the embed's rendering mode.
	got = rendered.EmbedFrame("\n<!DOCTYPE html><html><head><title>t</title></head><body>b</body></html>", "light", true)
	if !strings.HasPrefix(got, "<!DOCTYPE html>"+csp+rendered.EmbedNoRTC) || !strings.HasSuffix(got, "<html><head><title>t</title></head><body>b</body></html>") {
		t.Errorf("frame with doctype: %q", got)
	}
	if !strings.Contains(rendered.EmbedCSP, "webrtc 'block'") {
		t.Error("policy does not block WebRTC")
	}
}

// A view that gives the page the wheel, as math's, has no script for it:
// the frame keeps its theme, policy and fit, and neither passes the wheel.
func TestEmbedPageWithoutWheel(t *testing.T) {
	page := rendered.EmbedPage("m", "<p>x</p>", "#000000", true, false)
	if strings.Contains(page, `addEventListener("wheel"`) || strings.Contains(page, "deltaMode") {
		t.Errorf("a wheel script in %s", page)
	}
	frame := rendered.EmbedFrame("<p>x</p>", "dark", false)
	csp := `<meta http-equiv="Content-Security-Policy" content="` + rendered.EmbedCSP + `">`
	if !strings.HasPrefix(frame, csp+rendered.EmbedNoRTC) || !strings.Contains(frame, `dataset.theme = "dark"`) ||
		!strings.Contains(frame, "leafletSize") || strings.Contains(frame, "leafletWheel") {
		t.Errorf("frame %q", frame)
	}
	// The size still reaches the app.
	if !strings.Contains(page, `"Embeds.Size"`) {
		t.Errorf("the wrapper passes no size: %s", page)
	}
}

// What an embed said about itself goes with its block, while a block still
// in the page keeps it.
func TestDropEmbedsForgetsDeletedBlocks(t *testing.T) {
	ed := editor.New(doc.Empty(), nil)
	ed.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{{ID: "kept", Type: "embed", Data: map[string]any{"html": "<p>a</p>"}}}
	})
	kept, gone := blockView("kept"), blockView("gone")
	a := &App{page: &pageState{editor: ed}, embeds: map[string]*mygo.WebView{},
		embedIn:     map[string]embedInput{kept: {src: "a"}, gone: {src: "b"}, fullView("gone"): {src: "c"}},
		embedSizes:  map[string]embedSize{kept: {h: 1}, gone: {h: 2}},
		embedZoomed: map[string]bool{fullView("kept"): true, fullView("gone"): true},
		embedToPage: map[string]bool{kept: true, gone: true},
		embedFailed: map[string]string{kept: "no view", gone: "no view"}}
	a.dropEmbeds()
	if len(a.embedIn) != 1 || a.embedIn[kept].src != "a" || len(a.embedSizes) != 1 || a.embedSizes[kept].h != 1 ||
		len(a.embedZoomed) != 1 || !a.embedZoomed[fullView("kept")] || len(a.embedToPage) != 1 || !a.embedToPage[kept] ||
		len(a.embedFailed) != 1 || a.embedFailed[kept] == "" {
		t.Errorf("after dropping: %v %v %v %v %v", a.embedIn, a.embedSizes, a.embedZoomed, a.embedToPage, a.embedFailed)
	}
	a.page = nil
	a.dropEmbeds()
	if len(a.embedIn)+len(a.embedSizes)+len(a.embedZoomed)+len(a.embedToPage)+len(a.embedFailed) != 0 {
		t.Errorf("with no page: %v %v %v %v", a.embedIn, a.embedSizes, a.embedZoomed, a.embedFailed)
	}
}

// A block's web view and a diagram's full screen one have keys no block's
// ID can make alike: a block whose ID starts as a full screen key does
// keeps its view in the page, whether or not a block has the ID after
// it, apart from that block's full screen view.
func TestEmbedViewKeysNameOneBlockEach(t *testing.T) {
	if blockView("full:x") == fullView("x") {
		t.Fatal("a block's view and another's full screen view have one key")
	}
	for key, want := range map[string]struct {
		id   string
		full bool
	}{blockView("full:x"): {"full:x", false}, fullView("x"): {"x", true}, blockView("block:y"): {"block:y", false}} {
		if id, full := viewBlock(key); id != want.id || full != want.full {
			t.Errorf("viewBlock(%q) = %q, %v; want %q, %v", key, id, full, want.id, want.full)
		}
	}
	ed := editor.New(doc.Empty(), nil)
	ed.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{
			{ID: "full:x", Type: "embed", Data: map[string]any{"html": "<p>a</p>"}},
			{ID: "x", Type: "diagram", Data: map[string]any{"source": "graph TD; a-->b"}},
			{ID: "full:y", Type: "embed", Data: map[string]any{"html": "<p>b</p>"}},
		}
	})
	a := &App{page: &pageState{editor: ed, fullDiagram: "x"},
		embeds:     map[string]*mygo.WebView{blockView("full:x"): {}, fullView("x"): {}, blockView("full:y"): {}},
		embedBuilt: map[string]bool{blockView("full:x"): true, blockView("full:y"): true},
		embedSizes: map[string]embedSize{blockView("full:x"): {h: 1}, blockView("full:y"): {h: 2}}}
	a.dropEmbeds()
	if len(a.embeds) != 3 || a.embedSizes[blockView("full:x")].h != 1 || a.embedSizes[blockView("full:y")].h != 2 {
		t.Errorf("after dropping: views %v, sizes %v", a.embeds, a.embedSizes)
	}
}

// A block whose web view could not be made says why, from what was kept
// of the failure, rather than trying to make one at every frame.
func TestEmbedViewNotMadeSaysWhy(t *testing.T) {
	a := &App{embeds: map[string]*mygo.WebView{}, embedFailed: map[string]string{"e1": "no WebView2"}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			a.sandbox(c, "e1", embedInput{src: "<p>hi</p>", kind: "embed"}, func() string { return "<p>hi</p>" }, 400, nil, 200)
		})
	}, 500, 300)
	tt.Frame()
	if !tt.HasText(locales.HostEmbedError("no WebView2")) {
		t.Errorf("shows %q", tt.Texts())
	}
}

// The embed dialog inserts only with HTML to embed, and says in the app's
// words what else keeps it from inserting it, as HTML too long.
func TestEmbedDialogSaysWhatIsWrong(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	blocks := len(a.page.editor.Content.Children)
	a.dialogs.openEmbed()
	a.dialogs.embedHTML = "  "
	tt := ui.NewTester(a.embedDialog, 800, 600)
	tt.Frame()
	_ = tt.Click(locales.DialogInsert())
	tt.Frame()
	if !a.dialogs.embedOpen || len(a.page.editor.Content.Children) != blocks {
		t.Fatal("an embed with no HTML was inserted")
	}
	a.dialogs.embedHTML = strings.Repeat("<p>long</p>", 20_000)
	tt.Frame()
	if err := tt.Click(locales.DialogInsert()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := strings.TrimSuffix(locales.DialogEmbedFailed(""), " ")
	if !a.dialogs.embedOpen || !strings.HasPrefix(a.dialogs.embedError, want) || !tt.HasText(a.dialogs.embedError) || len(a.toasts) != 0 {
		t.Errorf("open %v, error %q, toasts %+v", a.dialogs.embedOpen, a.dialogs.embedError, a.toasts)
	}
	a.dialogs.openEmbed()
	if a.dialogs.embedError != "" {
		t.Errorf("the dialog opens again with %q", a.dialogs.embedError)
	}
}

// An embed scrolls the page by the wheel it leaves only while the pointer
// is over its view, and once a frame, however often it asks: one could
// otherwise scroll the page as it likes.
func TestEmbedScrollsOnlyUnderThePointer(t *testing.T) {
	ps := &pageState{}
	ps.scroll.MaxY = 10_000
	a := &App{page: ps, embeds: map[string]*mygo.WebView{"e1": {}, "e2": {}}}
	s := &embedScroll{a: a}
	s.Scroll("e1", 300)
	a.scrollByEmbeds(ps)
	if ps.scroll.Y != 0 {
		t.Errorf("an embed not under the pointer scrolled the page to %v", ps.scroll.Y)
	}
	a.embedOver = "e1"
	s.Scroll("e2", 300)
	for range 100 {
		s.Scroll("e1", 1e6)
	}
	a.scrollByEmbeds(ps)
	if ps.scroll.Y != maxEmbedWheel {
		t.Errorf("a frame scrolled the page to %v, want %v", ps.scroll.Y, float32(maxEmbedWheel))
	}
	a.scrollByEmbeds(ps)
	if ps.scroll.Y != maxEmbedWheel {
		t.Errorf("the wheel passed on scrolled the page again, to %v", ps.scroll.Y)
	}
	s.Scroll("e1", -50)
	a.scrollByEmbeds(ps)
	if ps.scroll.Y != maxEmbedWheel-50 {
		t.Errorf("the page at %v, want %v", ps.scroll.Y, float32(maxEmbedWheel-50))
	}
}

// A block's web view is made only within a window's height of the window,
// and let go three away; one not laid out yet waits for the next frame.
func TestEmbedViewsNearTheWindow(t *testing.T) {
	for _, tc := range []struct {
		r         ui.Rect
		near, far bool
	}{
		{ui.Rect{Y: 100, W: 500, H: 200}, true, false},
		{ui.Rect{Y: -500, W: 500, H: 200}, true, false},
		{ui.Rect{Y: 700, W: 500, H: 200}, true, false},
		{ui.Rect{Y: 900, W: 500, H: 200}, false, false},
		{ui.Rect{Y: -1000, W: 500, H: 200}, false, false},
		{ui.Rect{Y: 1700, W: 500, H: 200}, false, true},
		{ui.Rect{Y: -1500, W: 500, H: 200}, false, true},
		{ui.Rect{}, false, false},
	} {
		var near, far bool
		ui.NewTester(func(c *ui.Context) { near, far = embedNear(c, tc.r) }, 500, 400).Frame()
		if near != tc.near || far != tc.far {
			t.Errorf("%v: near %v, far %v; want %v, %v", tc.r, near, far, tc.near, tc.far)
		}
	}
}

// An embed's web view runs NoRTC first in every frame of its page, those
// the embed makes too.
func TestEmbedViewsTakeWebRTCFromEveryFrame(t *testing.T) {
	o := embedViewOptions(ui.RGBA(1, 2, 3, 1), true)
	if o.Page.PreloadScript != rendered.NoRTC || !o.Page.PreloadAllFrames || !o.ScrollToContainer || o.BackgroundColor != "#010203" {
		t.Errorf("options %+v", o)
	}
}
