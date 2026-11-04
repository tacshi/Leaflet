package app

import (
	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/store"
)

// writeExport writes every page to file, and returns how many pages it
// left out: those this device does not keep and could not download. It
// runs off the main thread, so it is given the folders and files as they
// were when it started. The file is marked as from elsewhere, as one
// openSafely opens is: it holds the pages' files, from other devices and
// people, and what is unpacked from it keeps the mark.
func (a *App) writeExport(file string, allFolders []store.Folder, allFiles []store.File) (int, error) {
	missing, err := export.All(file, export.Source{Store: a.st, Sync: a.sync, Rec: a.rec}, allFolders, allFiles)
	if err == nil {
		markDownloaded(file)
	}
	return missing, err
}

// markdownOptions are how the app writes a page as Markdown: in its
// language, its subpages by their titles.
func (a *App) markdownOptions() doc.MarkdownOptions {
	return doc.MarkdownOptions{Lang: i18n.Locale(), PageTitle: a.pageTitleOf}
}

// withTranscripts is a page's content to export: a copy, each recording's
// transcript after its block as lines of text.
func (a *App) withTranscripts(c *doc.Content) *doc.Content { return export.WithTranscripts(a.st, c) }
