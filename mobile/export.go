package mobile

import (
	"errors"
	"os"
	"path/filepath"

	"leaflet/internal/export"
	"leaflet/internal/store"
	"leaflet/locales"
)

// InFolder reports whether path is in folder dir, links followed, as the
// app asks of a picture a block links to on this device: a block's link can
// come from another device or person, and a file outside the app's data
// folder is not shown.
func InFolder(dir, path string) bool {
	defer catch(nil)
	return export.InFolder(dir, path)
}

// exportedAll is Export all's zip, as ExportAll returns it.
type exportedAll struct {
	// Path is the zip's, in the folder ExportAll was given, for the app
	// to move where the user picks; Name, its file name.
	Path string `json:"path"`
	Name string `json:"name"`
	// Missing counts the pages left out: those this device does not keep
	// and could not download.
	Missing int `json:"missing"`
}

// ExportAll writes every page as Markdown in a zip, with the pictures,
// files and recordings this device keeps, into the folder dir, as the
// desktop's Account › Export all does, and returns an exportedAll in
// JSON. The open pages' edits are saved first: an export without them
// would miss them.
func (c *Core) ExportAll(dir string) (_ string, err error) {
	defer catch(&err)
	var saved bool
	var folders []store.Folder
	var files []store.File
	c.q.do(func() {
		if saved = c.saveOpen(); !saved {
			return
		}
		if folders, err = c.core.Store.Folders(); err == nil {
			files, err = c.core.Store.Files()
		}
	})
	if !saved {
		return "", errNotSaved()
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	out := exportedAll{Name: locales.SettingsExportFileName()}
	out.Path = filepath.Join(dir, out.Name)
	// Off the queue, as the desktop's runs off its main thread: pages
	// this device does not keep download first.
	src := export.Source{Store: c.core.Store, Sync: c.core.Sync, Rec: c.core.Rec}
	if out.Missing, err = export.All(out.Path, src, folders, files); err != nil {
		return "", errors.New(locales.SettingsExportFailed(err.Error()))
	}
	return encode(out)
}
