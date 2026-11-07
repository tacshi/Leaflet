package app

import (
	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/store"
)

// writeExport writes every page to file, and reports the pages it left
// out, those this device does not have and could not download, and those
// it could not bring up to date. It runs off the main thread, so it is
// given the folders and files, and which pages this device keeps up to
// date (keeper), as they were when it started. The file is marked as from
// elsewhere, as one openSafely opens is: it holds the pages' files, from
// other devices and people, and what is unpacked from it keeps the mark.
func (a *App) writeExport(file string, kept export.Keeper, allFolders []store.Folder, allFiles []store.File) (export.Report, error) {
	report, err := export.All(file, export.Source{Store: a.st, Sync: a.sync, Rec: a.rec, Kept: kept}, allFolders, allFiles)
	if err == nil {
		markDownloaded(file)
	}
	return report, err
}

// markdownOptions are how the app writes a page as Markdown: in its
// language, its subpages by their titles.
func (a *App) markdownOptions() doc.MarkdownOptions {
	return doc.MarkdownOptions{Lang: i18n.Locale(), PageTitle: a.pageTitleOf}
}

// withTranscripts is page pageID's content c to export: a copy, each of
// its recordings' transcript after its block as lines of text.
func (a *App) withTranscripts(pageID string, c *doc.Content) *doc.Content {
	return export.WithTranscripts(a.st, pageID, c)
}
