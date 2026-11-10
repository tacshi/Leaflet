package mobile

import "leaflet/internal/doc"

func MarkdownBlocks(markdown string) (_ string, err error) {
	defer catch(&err)
	blocks := doc.MarkdownBlocks(markdown)
	if blocks == nil {
		blocks = []*doc.Node{}
	}
	return encode(blocks)
}
