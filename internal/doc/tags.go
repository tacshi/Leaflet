package doc

import (
	"hash/fnv"
	"strings"
)

var tagColors = []string{"#ef4444", "#f97316", "#eab308", "#22c55e", "#14b8a6", "#3b82f6", "#6366f1", "#a855f7", "#ec4899"}

func TagColor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return tagColors[h.Sum32()%uint32(len(tagColors))] // #nosec G115
}
