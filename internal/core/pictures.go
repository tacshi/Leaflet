package core

import (
	"context"
	"log"
	"os"
	"slices"

	"leaflet/internal/doc"
)

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
