package core

import (
	"context"
	"log"
	"os"
	"slices"

	"leaflet/internal/doc"
)

// DownloadPictures downloads the pictures of page's image blocks in
// content that another device uploaded and this one has no copy of, as
// the blocks download them as they show, for the page to be written
// whole, as Export › PDF prints it. A picture that cannot come is left
// out, as a missing one is. It reports whether any came.
func (c *Core) DownloadPictures(ctx context.Context, content *doc.Content, page string) bool {
	if c.Sync == nil || !c.Sync.CanFetch() {
		return false
	}
	var ids []string
	content.Walk(func(n, _ *doc.Node) {
		if id := n.Str("file_id"); n.Type == "image" && id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	})
	came := false
	for _, id := range ids {
		f, err := c.Store.File(id)
		if err != nil || f.PageID != page || f.StoragePath == "" {
			continue
		}
		if f.LocalPath != "" {
			if _, err := os.Stat(f.LocalPath); err == nil {
				continue
			}
		}
		// As it comes: a picture may be large, and is not kept in memory.
		data, err := c.Sync.FetchReader(ctx, "files", f.StoragePath)
		if err == nil {
			_, err = c.Store.DownloadedFileFrom(id, data)
			_ = data.Close()
		}
		if err != nil {
			log.Printf("download %s: %v", f.Name, err)
			continue
		}
		came = true
	}
	return came
}
