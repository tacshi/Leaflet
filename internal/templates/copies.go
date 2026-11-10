package templates

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"leaflet/internal/icloud"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func CopyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return locales.TemplateCopyTitle(title)
}

func Duplicate(st *store.Store, p store.Page) store.CopyOptions {
	o := store.CopyOptions{ParentID: p.ParentID, FolderID: p.FolderID, AfterID: p.ID, Title: CopyTitle(p.Title)}
	if p.SpaceID != st.Session().SpaceID && (p.ParentID == "" || !canEdit(st, p.ParentID)) {
		return store.CopyOptions{Title: o.Title}
	}
	return o
}

func canEdit(st *store.Store, id string) bool {
	p, err := st.Page(id)
	return err == nil && st.CanEdit(p)
}

func RecordingIn(st *store.Store, rec *recording.Controller, id string, under func(page, other string) bool) bool {
	if rec.Active() && under(rec.State.PageID, id) {
		return true
	}
	for rid := range rec.Transcribing {
		if r, err := st.Recording(rid); err == nil && under(r.PageID, id) {
			return true
		}
	}
	return false
}

var ErrFilesUploading error = filesUploading{}

type filesUploading struct{}

func (filesUploading) Error() string { return locales.TemplateFilesUploading() }

func CopyError(err error) error {
	if errors.Is(err, store.ErrCopyNotHere) || remote.IsNotFound(err) || errors.Is(err, icloud.ErrNotReady) || errors.Is(err, fs.ErrNotExist) {
		return ErrFilesUploading
	}
	return err
}

func Needs(st *store.Store, sync *syncsvc.Service, id string) ([]store.CopyNeed, error) {
	needs, err := st.CopyNeeds(id)
	if err != nil {
		return nil, err
	}
	for _, n := range needs {
		if n.Path == "" || !sync.CanFetch() {
			return nil, ErrFilesUploading
		}
	}
	return needs, nil
}

func Fetch(st *store.Store, sync *syncsvc.Service, needs []store.CopyNeed) ([]string, error) {
	var audio []string
	for _, n := range needs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		r, err := sync.FetchReader(ctx, n.Bucket, n.Path)
		if err != nil {
			cancel()
			return audio, err
		}
		if n.FileID != "" {
			_, err = st.DownloadedFileFrom(n.FileID, r)
		} else if _, err = st.DownloadedAudioFrom(n.RecordingID, r); err == nil {
			audio = append(audio, n.RecordingID)
		}
		_ = r.Close()
		cancel()
		if err != nil {
			return audio, err
		}
	}
	return audio, nil
}

func DropAudio(st *store.Store, s store.Settings, audio []string) {
	if s.KeepAudioOnDevice != "until_uploaded" {
		return
	}
	for _, id := range audio {
		if r, err := st.Recording(id); err == nil && r.AudioPath != "" && r.LocalPath != "" {
			_ = os.Remove(r.LocalPath)
			_ = st.ClearLocalPath(id)
		}
	}
}
