package mobile

import "leaflet/internal/doc"

// MarkdownBlocks returns Markdown read as blocks, a JSON array of nodes, as
// pasting it reads it (doc.MarkdownBlocks): an agent's answer, or a
// version of a page, shows as the page's blocks. A fence left open, as an
// answer streaming in has, runs to the end.
func MarkdownBlocks(markdown string) (_ string, err error) {
	defer catch(&err)
	blocks := doc.MarkdownBlocks(markdown)
	if blocks == nil {
		blocks = []*doc.Node{}
	}
	return encode(blocks)
}
