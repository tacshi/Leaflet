// Package icons is Lucide's icons as Leaflet bundles them: for the app's
// own interface, through package theme, and for web pages. It holds
// nothing of MyGo's interface, so the iOS core has them too.
package icons

import (
	"embed"
	"strings"
)

//go:embed *.svg
var files embed.FS

// Read returns an icon's SVG source by name, as "file-text", or false
// when there is none of that name.
func Read(name string) ([]byte, bool) {
	data, err := files.ReadFile(name + ".svg")
	return data, err == nil
}

// SVG returns an icon's SVG source by name, for a web page; its strokes
// are currentColor. A name with no icon is circle-alert.
func SVG(name string) string {
	data, ok := Read(name)
	if !ok {
		data, _ = Read("circle-alert")
	}
	return string(data)
}

// Names lists the bundled icons.
func Names() []string {
	entries, _ := files.ReadDir(".")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".svg"))
	}
	return out
}
