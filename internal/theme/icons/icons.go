package icons

import (
	"embed"
	"strings"
)

//go:embed *.svg
var files embed.FS

func Read(name string) ([]byte, bool) {
	data, err := files.ReadFile(name + ".svg")
	return data, err == nil
}

func SVG(name string) string {
	data, ok := Read(name)
	if !ok {
		data, _ = Read("circle-alert")
	}
	return string(data)
}

func Names() []string {
	entries, _ := files.ReadDir(".")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".svg"))
	}
	return out
}
