package doc

import (
	"hash/fnv"
	"strings"
)

// tagColors are the colors tags take, as hex: the text colors but gray
// and brown, which would read as no color.
var tagColors = []string{"#ef4444", "#f97316", "#eab308", "#22c55e", "#14b8a6", "#3b82f6", "#6366f1", "#a855f7", "#ec4899"}

// TagColor is a tag's color, as hex: one of tagColors, chosen by the
// FNV-1a hash of its name in any case, so a tag has the same one on every
// device, on the desktop and the phone alike.
func TagColor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return tagColors[h.Sum32()%uint32(len(tagColors))] // #nosec G115 -- a handful of colors.
}
