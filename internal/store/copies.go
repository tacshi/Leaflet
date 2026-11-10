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

type CopyOptions struct {
	ParentID string
	FolderID string
	SpaceID  string
	AfterID  string
	Title    string
	Template bool
}

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

type pageCopies struct {
	tree     []Page
	ids      map[string]string
	contents []*doc.Content
	recs     []recordingCopy
	files    []fileCopy
}

func (s *Store) copyPlan(id, copyID string) (pageCopies, error) {
	root, err := s.Page(id)
	if err != nil {
		return pageCopies{}, err
	}
	tree := []Page{root}
	seen := map[string]bool{id: true}
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
		c, err := doc.Decode(row.Content)
		if err != nil {
			return pageCopies{}, fmt.Errorf("%s: %w", p.DisplayTitle(), err)
		}
		contents[p.ID] = c
	}
	copies := doc.CopyPages(contents, plan.ids)
	for i, p := range tree {
		files := map[string]int{}
		plan.contents[i] = copies[p.ID]
		plan.contents[i].Walk(func(n, _ *doc.Node) {
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
			n.Set("url", FileURL(fc.to, fileName(fc.Name)))
		})
	}
	return plan, nil
}

func createCopies(tx *sql.Tx, sess Session, pages []Page, ids map[string]string, contents []*doc.Content, template bool) error {
	for i, p := range pages {
		if _, err := createPage(tx, sess, NewPageOptions{ID: ids[p.ID], ParentID: ids[p.ParentID], Title: p.Title,
			Icon: p.Icon, Tags: p.Tags, Template: template, Content: contents[i]}); err != nil {
			return err
		}
	}
	return nil
}

type recordingCopy struct {
	Recording
	from string
}

type fileCopy struct {
	File
	to, pageID string
}

var ErrCopyNotHere = errors.New("store: a file or a recording the copy copies is not on this device")

type CopyNeed struct {
	Bucket, Path        string
	FileID, RecordingID string
}

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
