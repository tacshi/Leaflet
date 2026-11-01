package app

import (
	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/store"
)

// writeExport writes every page to file, and returns how many pages it
// left out: those this device does not keep and could not download. It
// runs off the main thread, so it is given the folders and files as they
// were when it started.
func (a *App) writeExport(file string, allFolders []store.Folder, allFiles []store.File) (int, error) {
	return export.All(file, export.Source{Store: a.st, Sync: a.sync, Rec: a.rec}, allFolders, allFiles)
}

// withTranscripts is a page's content to export: a copy, each recording's
// transcript after its block as lines of text.
func (a *App) withTranscripts(c *doc.Content) *doc.Content { return export.WithTranscripts(a.st, c) }

// ownFiles returns where each of files is on this device, and a reader of
// the app's own files only, as export.OwnFiles.
func ownFiles(dir string, files []store.File) (localPath func(fileID string) string, read func(path string) ([]byte, error)) {
	return export.OwnFiles(dir, files)
}
