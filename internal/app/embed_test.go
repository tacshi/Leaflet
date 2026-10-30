package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/theme"
)

// An embed's ID goes into the wrapper's script as a string it cannot end.
func TestEmbedPageQuotesID(t *testing.T) {
	page := embedPage("x</script><script>window.mygo.call(1)//\u2028", "<p>hi</p>", theme.For(false))
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
	csp := `<meta http-equiv="Content-Security-Policy" content="` + embedCSP + `">`
	got := embedFrame(`<!-- <head> --><script>new RTCPeerConnection()</script>`, "dark")
	if !strings.HasPrefix(got, csp+embedNoRTC) {
		t.Errorf("frame starts %q", got[:min(len(got), 120)])
	}
	if !strings.Contains(got, `dataset.theme = "dark"`) || !strings.Contains(got, "leafletWheel") || !strings.Contains(got, "leafletSize") {
		t.Error("frame lacks its theme, wheel or fit script")
	}
	if !strings.HasSuffix(got, `<!-- <head> --><script>new RTCPeerConnection()</script>`) {
		t.Errorf("embed's HTML not last: %q", got)
	}
	// A doctype stays first, keeping the embed's rendering mode.
	got = embedFrame("\n<!DOCTYPE html><html><head><title>t</title></head><body>b</body></html>", "light")
	if !strings.HasPrefix(got, "<!DOCTYPE html>"+csp+embedNoRTC) || !strings.HasSuffix(got, "<html><head><title>t</title></head><body>b</body></html>") {
		t.Errorf("frame with doctype: %q", got)
	}
	if !strings.Contains(embedCSP, "webrtc 'block'") {
		t.Error("policy does not block WebRTC")
	}
}

// What an embed said about itself goes with its block, while a block still
// in the page keeps it.
func TestDropEmbedsForgetsDeletedBlocks(t *testing.T) {
	ed := editor.New(doc.Empty(), nil)
	ed.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{{ID: "kept", Type: "embed", Data: map[string]any{"html": "<p>a</p>"}}}
	})
	a := &App{page: &pageState{editor: ed}, embeds: map[string]*mygo.WebView{},
		embedSrc:    map[string]string{"kept": "a", "gone": "b", "full:gone": "c"},
		embedSizes:  map[string]embedSize{"kept": {h: 1}, "gone": {h: 2}},
		embedZoomed: map[string]bool{"full:kept": true, "full:gone": true}}
	a.dropEmbeds()
	if len(a.embedSrc) != 1 || a.embedSrc["kept"] != "a" || len(a.embedSizes) != 1 || a.embedSizes["kept"].h != 1 ||
		len(a.embedZoomed) != 1 || !a.embedZoomed["full:kept"] {
		t.Errorf("after dropping: %v %v %v", a.embedSrc, a.embedSizes, a.embedZoomed)
	}
	a.page = nil
	a.dropEmbeds()
	if len(a.embedSrc)+len(a.embedSizes)+len(a.embedZoomed) != 0 {
		t.Errorf("with no page: %v %v %v", a.embedSrc, a.embedSizes, a.embedZoomed)
	}
}
