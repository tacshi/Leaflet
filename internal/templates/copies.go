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

// A copy of pages goes up with its own files and recordings' audio,
// copied from this device as it is made, never later from the
// originals', which another device may delete meanwhile (store.CopyNeeds):
// those only stored come first (Needs, Fetch), and the audio that came
// goes again once the copy has its own (DropAudio). Each app makes one
// copy at a time, from what it fetches to the copy itself, so the audio
// one brings doesn't go from under another.

// CopyTitle is the title of a copy, as Finder names one.
func CopyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return locales.TemplateCopyTitle(title)
}

// Duplicate is how page p is duplicated: next to it, after it, and
// titled as a copy. A page shared with the user that they can't add to the
// parent of is copied into their own space.
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

// RecordingIn reports whether a recording on page id or its subpages is
// still being made or transcribed here, as rec has it: a copy of it now
// would stay as it is, without what the recording gets once it finishes.
// under reports whether a page is another or among its subpages.
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

// ErrFilesUploading is why a copy is not made: a file or a recording's
// audio it copies is not on this device, still uploading from where it
// was added, or can't be downloaded now. It says so in the language the
// app shows as it is read.
var ErrFilesUploading error = filesUploading{}

type filesUploading struct{}

func (filesUploading) Error() string { return locales.TemplateFilesUploading() }

// CopyError says why a copy was not made: ErrFilesUploading for a file or
// a recording's audio not on this device, as one still uploading or gone
// from where it was stored; else err.
func CopyError(err error) error {
	if errors.Is(err, store.ErrCopyNotHere) || remote.IsNotFound(err) || errors.Is(err, icloud.ErrNotReady) || errors.Is(err, fs.ErrNotExist) {
		return ErrFilesUploading
	}
	return err
}

// Needs are what copying page id, with its subpages, needs brought to
// this device first (store.CopyNeeds). When one is still uploading from
// where it was added, or sync can't fetch, none can come: it fails with
// ErrFilesUploading.
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

// Fetch brings to this device what needs lists, one at a time, each in
// five minutes at most: off the app's thread, as it takes a while. It
// returns the recordings whose audio came, for DropAudio once the copy is
// made, or won't be: when it fails too, with what came before.
func Fetch(st *store.Store, sync *syncsvc.Service, needs []store.CopyNeed) ([]string, error) {
	var audio []string
	for _, n := range needs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		data, err := sync.Fetch(ctx, n.Bucket, n.Path)
		cancel()
		if err != nil {
			return audio, err
		}
		if n.FileID != "" {
			_, err = st.DownloadedFile(n.FileID, data)
		} else if _, err = st.DownloadedAudio(n.RecordingID, data); err == nil {
			audio = append(audio, n.RecordingID)
		}
		if err != nil {
			return audio, err
		}
	}
	return audio, nil
}

// DropAudio removes from this device the recordings' audio Fetch brought
// for a copy, once the copy has its own or won't be made: it stays only as
// Keep the audio, in settings s, says.
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
