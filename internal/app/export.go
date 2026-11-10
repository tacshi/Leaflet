package app

import (
	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/store"
)

func (a *App) writeExport(file string, kept export.Keeper, allFolders []store.Folder, allFiles []store.File) (export.Report, error) {
	report, err := export.All(file, export.Source{Store: a.st, Sync: a.sync, Rec: a.rec, Kept: kept}, allFolders, allFiles)
	if err == nil {
		markDownloaded(file)
	}
	return report, err
}

func (a *App) markdownOptions() doc.MarkdownOptions {
	return doc.MarkdownOptions{Lang: i18n.Locale(), PageTitle: a.pageTitleOf}
}

func (a *App) withTranscripts(pageID string, c *doc.Content) *doc.Content {
	return export.WithTranscripts(a.st, pageID, c)
}
