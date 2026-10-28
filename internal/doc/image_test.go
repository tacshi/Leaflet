package doc

import "testing"

func TestImageLink(t *testing.T) {
	for s, want := range map[string]bool{
		"https://example.com/a.png":           true,
		"https://example.com/logo.svg":        true,
		" https://example.com/b/c.JPEG?x=1 ":  true,
		"http://example.com/d.webp#frag":      true,
		"https://example.com/page":            false,
		"https://example.com/a.png/next":      false,
		"https://example.com/?file=a.png":     false,
		"file:///Users/x/a.png":               false,
		"https://example.com/a picture.png":   false,
		"see https://example.com/a.png today": false,
	} {
		if got := ImageLink(s); got != want {
			t.Errorf("ImageLink(%q) = %v", s, got)
		}
	}
	n, err := ImageNode(" https://example.com/a.png ")
	if err != nil || n.Type != "image" || n.Str("url") != "https://example.com/a.png" || n.Str("align") != "center" {
		t.Errorf("ImageNode = %v, %v", n, err)
	}
	if _, err := ImageNode("/Users/x/a.png"); err == nil {
		t.Error("an image of a path on the device")
	}
}
