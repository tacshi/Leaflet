package mobile

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"leaflet/internal/export"
	"leaflet/internal/store"
	"leaflet/locales"
)

func InFolder(dir, path string) bool {
	defer catch(nil)
	return export.InFolder(dir, path)
}

type exportedAll struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Missing int    `json:"missing"`
	Stale   int    `json:"stale"`
}

func (c *Core) ExportAll(dir string) (_ string, err error) {
	defer catch(&err)
	var saved bool
	var folders []store.Folder
	var files []store.File
	var kept export.Keeper
	c.q.do(func() {
		if saved = c.saveOpen(); !saved {
			return
		}
		kept = export.Keeper{
			SyncOn:     c.core.Remote.SignedIn() || c.core.Sync.ICloudOn(),
			Pulled:     slices.Collect(maps.Keys(c.pages)),
			AllOffline: c.core.Store.Settings().AllPagesOffline,
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
	src := export.Source{Store: c.core.Store, Sync: c.core.Sync, Rec: c.core.Rec, Kept: kept}
	report, err := export.All(out.Path, src, folders, files)
	if err != nil {
		return "", errors.New(locales.SettingsExportFailed(err.Error()))
	}
	out.Missing, out.Stale = report.Missing, report.Stale
	return encode(out)
}
