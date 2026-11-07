package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"leaflet/internal/doc"
)

// CopyOptions say where a copy of a page goes, as NewPageOptions do.
type CopyOptions struct {
	ParentID string
	FolderID string
	SpaceID  string
	AfterID  string
	Title    string
	// Template makes the copy and its subpages templates; else they are
	// pages.
	Template bool
}

// CopyPage copies a page and its subpages, which keep their titles and
// order, and returns the copy. Links among them go to the copies.
func (s *Store) CopyPage(id string, o CopyOptions) (Page, error) {
	plan, err := s.copyPlan(id, doc.NewRowID())
	if err != nil {
		return Page{}, err
	}
	var p Page
	err = s.storeCopies(plan, func(tx *sql.Tx) (bool, error) {
		root := plan.tree[0]
		p, err = createPage(tx, s.Session(), NewPageOptions{ID: plan.ids[id], ParentID: o.ParentID, FolderID: o.FolderID,
			SpaceID: o.SpaceID, AfterID: o.AfterID, Title: o.Title, Icon: root.Icon, Tags: root.Tags, Template: o.Template,
			Content: plan.contents[0]})
		return o.Template, err
	})
	return p, err
}

// CopyInto copies page id's subpages under page into, and returns a copy
// of id's content for into to hold, as when a page starts from a
// template. Links among them go to the copies.
func (s *Store) CopyInto(id, into string) (*doc.Content, error) {
	plan, err := s.copyPlan(id, into)
	if err != nil {
		return nil, err
	}
	err = s.storeCopies(plan, func(tx *sql.Tx) (bool, error) {
		target, err := pageIn(tx, into)
		if err != nil {
			return false, err
		}
		// The page takes the template's tags too, after its own.
		if tags := NormalizeTags(append(append([]string{}, target.Tags...), plan.tree[0].Tags...)); len(tags) != len(target.Tags) {
			target.Tags, target.UpdatedAt = tags, time.Now()
			if err := savePage(tx, target, "tagged"); err != nil {
				return false, err
			}
		}
		return target.Template, nil
	})
	if err != nil {
		return nil, err
	}
	return plan.contents[0], nil
}

// storeCopies stores what copyPlan planned in one transaction, with the
// root the caller makes or changes in it first, which reports whether the
// copies are templates: a copy cut short would be left half made, and
// queued to go up so. What it copied here of the files and audio goes
// again when it fails.
func (s *Store) storeCopies(plan pageCopies, root func(tx *sql.Tx) (bool, error)) error {
	recs, files, made, err := s.copyLocal(plan)
	if err == nil {
		err = s.inTx(func(tx *sql.Tx) error {
			template, err := root(tx)
			if err != nil {
				return err
			}
			if err := createCopies(tx, s.Session(), plan.tree[1:], plan.ids, plan.contents[1:], template); err != nil {
				return err
			}
			if err := putRecordingCopies(tx, s.Dir, s.Session().SpaceID, recs); err != nil {
				return err
			}
			return putFileCopies(tx, s.Dir, files)
		})
	}
	if err != nil {
		for _, path := range made {
			_ = os.RemoveAll(path)
		}
		return err
	}
	s.Notify("outbox", "")
	s.Notify("pages", "")
	if len(recs) > 0 {
		s.Notify("recordings", "")
	}
	if len(files) > 0 {
		s.Notify("files", "")
	}
	return nil
}

// pageCopies is what copyPlan plans: the pages, parents before their
// subpages, the IDs of their copies, the copies' contents, and the
// recordings and files the copies get of their own.
type pageCopies struct {
	tree     []Page
	ids      map[string]string
	contents []*doc.Content
	recs     []recordingCopy
	files    []fileCopy
}

// copyPlan reads page id and the pages under it, parents before their
// subpages, and gives each the ID of its copy (copyID for id) and its
// content with links among them going to the copies, and blocks of the
// page's own recordings and files to recordings and files of their own,
// which storeCopies stores once the copies are in.
func (s *Store) copyPlan(id, copyID string) (pageCopies, error) {
	root, err := s.Page(id)
	if err != nil {
		return pageCopies{}, err
	}
	tree := []Page{root}
	seen := map[string]bool{id: true} // a loop of parents is copied once
	for i := 0; i < len(tree); i++ {
		kids, err := s.queryPages(`parent_id = ? AND deleted_at IS NULL ORDER BY sort_key, id`, tree[i].ID)
		if err != nil {
			return pageCopies{}, err
		}
		for _, k := range kids {
			if !seen[k.ID] {
				seen[k.ID] = true
				tree = append(tree, k)
			}
		}
	}
	plan := pageCopies{tree: tree, ids: map[string]string{id: copyID}, contents: make([]*doc.Content, len(tree))}
	for _, p := range tree[1:] {
		plan.ids[p.ID] = doc.NewRowID()
	}
	contents := make(map[string]*doc.Content, len(tree))
	for _, p := range tree {
		row, err := s.ContentRow(p.ID)
		if IsNotFound(err) {
			return pageCopies{}, fmt.Errorf("%s is not on this device yet", p.DisplayTitle())
		} else if err != nil {
			return pageCopies{}, err
		}
		// Content that cannot be read is not copied as an empty page.
		c, err := doc.Decode(row.Content)
		if err != nil {
			return pageCopies{}, fmt.Errorf("%s: %w", p.DisplayTitle(), err)
		}
		contents[p.ID] = c
	}
	copies := doc.CopyPages(contents, plan.ids)
	for i, p := range tree {
		files := map[string]int{} // the page's files, by where plan.files has their copies
		plan.contents[i] = copies[p.ID]
		plan.contents[i].Walk(func(n, _ *doc.Node) {
			// Only the page's own recordings and files are copied: a block
			// may name any, as one an editor of the page wrote could, and
			// another page's would go to the copy's audience. A block of
			// another's keeps naming it, as the page's does, and shows it no
			// more than the page does.
			if n.Type == "recording" {
				r, err := s.Recording(n.Str("recording_id"))
				if err != nil || r.PageID != p.ID {
					return
				}
				from := r.ID
				r.ID, r.PageID, r.BlockID = doc.NewRowID(), plan.ids[p.ID], n.ID
				n.Set("recording_id", r.ID)
				plan.recs = append(plan.recs, recordingCopy{r, from})
				return
			}
			fileID := n.Str("file_id")
			if fileID == "" {
				return
			}
			k, ok := files[fileID]
			if !ok {
				f, err := s.File(fileID)
				if err != nil || !f.DeletedAt.IsZero() || f.PageID != p.ID {
					return
				}
				k = len(plan.files)
				files[fileID] = k
				plan.files = append(plan.files, fileCopy{f, doc.NewRowID(), plan.ids[p.ID]})
			}
			fc := plan.files[k]
			n.Set("file_id", fc.to)
			n.Set("url", "file://"+filepath.Join(s.Dir, "files", fc.to, fileName(fc.Name)))
		})
	}
	return plan, nil
}

// createCopies makes the copies of subpages that copyPlan planned, each
// last under its parent's copy, so they keep their order.
func createCopies(tx *sql.Tx, sess Session, pages []Page, ids map[string]string, contents []*doc.Content, template bool) error {
	for i, p := range pages {
		if _, err := createPage(tx, sess, NewPageOptions{ID: ids[p.ID], ParentID: ids[p.ParentID], Title: p.Title,
			Icon: p.Icon, Tags: p.Tags, Template: template, Content: contents[i]}); err != nil {
			return err
		}
	}
	return nil
}

// recordingCopy is a recording of a copied page, and the ID of the one it
// copies.
type recordingCopy struct {
	Recording
	from string
}

// fileCopy is a file of a copied page, with the ID of its copy and the
// page the copy is on.
type fileCopy struct {
	File
	to, pageID string
}

// ErrCopyNotHere is a copy refused because a file's contents or a
// recording's audio it copies is not on this device (CopyNeeds).
var ErrCopyNotHere = errors.New("store: a file or a recording the copy copies is not on this device")

// CopyNeed is a file's contents or a recording's audio that copying a
// page needs on this device first: where it is stored, by its bucket and
// path, which is "" for audio still uploading from where it was made.
type CopyNeed struct {
	Bucket, Path        string
	FileID, RecordingID string
}

// CopyNeeds lists what copying page id, with its subpages, needs brought
// to this device first (DownloadedFile, DownloadedAudio): a copy goes up
// with its own contents and audio, from this device, as it is made, and
// never comes later from the original's, which another device may delete
// meanwhile.
func (s *Store) CopyNeeds(id string) ([]CopyNeed, error) {
	plan, err := s.copyPlan(id, doc.NewRowID())
	if err != nil {
		return nil, err
	}
	var out []CopyNeed
	for _, c := range plan.recs {
		if !OnDevice(c.LocalPath) {
			out = append(out, CopyNeed{Bucket: "recordings", Path: c.AudioPath, RecordingID: c.from})
		}
	}
	for _, c := range plan.files {
		if !OnDevice(c.LocalPath) {
			out = append(out, CopyNeed{Bucket: "files", Path: c.StoragePath, FileID: c.ID})
		}
	}
	return out, nil
}

// copyLocal copies, for the recordings and files of copied pages, their
// audio and contents on this device, before storeCopies stores them: only
// those who can view the page of the recording a stored audio is named
// for, or the page a file is on, can read it, so each copy goes up with
// its own. One not on this device refuses the copy (ErrCopyNotHere). It
// returns the recordings and files to store, and the paths it made, which
// go if they are not stored.
func (s *Store) copyLocal(plan pageCopies) ([]Recording, []File, []string, error) {
	var made []string
	var recs []Recording
	for _, c := range plan.recs {
		if !OnDevice(c.LocalPath) {
			return nil, nil, made, ErrCopyNotHere
		}
		r := c.Recording
		r.AudioPath, r.UploadedAt = "", time.Time{}
		r.LocalPath = filepath.Join(s.Dir, "recordings", r.ID+filepath.Ext(c.LocalPath))
		if _, err := copyFile(c.LocalPath, r.LocalPath); err != nil {
			return nil, nil, made, err
		}
		made = append(made, r.LocalPath)
		r.UpdatedAt = time.Now()
		recs = append(recs, r)
	}
	var files []File
	for _, c := range plan.files {
		if !OnDevice(c.LocalPath) {
			return nil, nil, made, ErrCopyNotHere
		}
		now := time.Now()
		f := File{ID: c.to, SpaceID: s.Session().SpaceID, PageID: c.pageID, Name: fileName(c.Name), MimeType: c.MimeType,
			SizeBytes: c.SizeBytes, CreatedAt: now, UpdatedAt: now}
		f.StoragePath = StorageKey(f.SpaceID, f.ID, f.Name)
		dir := filepath.Join(s.Dir, "files", f.ID)
		made = append(made, dir)
		f.LocalPath = filepath.Join(dir, f.Name)
		if _, err := copyFile(c.LocalPath, f.LocalPath); err != nil {
			return nil, nil, made, err
		}
		files = append(files, f)
	}
	return recs, files, made, nil
}

// putRecordingCopies stores and queues the recordings of copied pages,
// each with the upload of its audio, in storeCopies' transaction.
func putRecordingCopies(tx *sql.Tx, dir, space string, recs []Recording) error {
	if len(recs) > 0 && space != "" && !hasSpace(tx, space) {
		return ErrNoSpace
	}
	for _, r := range recs {
		if err := putRecording(tx, dir, r); err != nil {
			return err
		}
		if err := queue(tx, "recordings", r.ID, RecordingToRemote(r), "", r.PageID); err != nil {
			return err
		}
		if err := queue(tx, "storage", "recordings/"+r.ID, audioUpload(dir, r), "audio waiting to upload", r.PageID); err != nil {
			return err
		}
	}
	return nil
}

// putFileCopies stores and queues the files of copied pages, as AddFile
// adds one, in storeCopies' transaction.
func putFileCopies(tx *sql.Tx, dir string, files []File) error {
	for _, f := range files {
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		if err := putFile(tx, dir, f); err != nil {
			return err
		}
		if err := queue(tx, "files", f.ID, FileToRemote(f), "new file", f.PageID); err != nil {
			return err
		}
		if err := queue(tx, "storage", "files/"+f.ID, fileUpload(dir, f), "file waiting to upload", f.PageID); err != nil {
			return err
		}
	}
	return nil
}
