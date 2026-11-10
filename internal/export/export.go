package export

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Source struct {
	Store *store.Store
	Sync  Syncer
	Rec   *recording.Controller
	Kept  Keeper
}

type Syncer interface {
	Hold(fn func())
	CanSync() bool
	RefreshContent(ctx context.Context, pageID string) (*doc.Content, error)
}

type Keeper struct {
	SyncOn     bool
	Pulled     []string
	AllOffline bool
}

func (k Keeper) Keeps(p store.Page) bool {
	return !k.SyncOn || slices.Contains(k.Pulled, p.ID) || p.KeepOffline || k.AllOffline
}

var errCantSync = errors.New("export: nowhere to bring the page from")

type Report struct {
	Missing int
	Stale   int
}

func All(file string, src Source, allFolders []store.Folder, allFiles []store.File) (Report, error) {
	var report Report
	st := src.Store
	pages, err := st.Pages()
	if err != nil {
		return report, err
	}
	templates, err := st.Templates()
	if err != nil {
		return report, err
	}
	const templatesFolder = "\x00templates"
	folders := append(slices.Clip(allFolders), store.Folder{ID: templatesFolder, Name: locales.ExportTemplatesFolder()})
	for _, t := range templates {
		if t.ParentID == "" {
			t.FolderID = templatesFolder
		}
		pages = append(pages, t)
	}
	fresh := map[string]*doc.Content{}
	for _, p := range pages {
		cached := st.Content(p.ID)
		if cached != nil && src.Kept.Keeps(p) {
			continue
		}
		var c *doc.Content
		err := errCantSync
		if src.Sync.CanSync() {
			src.Sync.Hold(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				c, err = src.Sync.RefreshContent(ctx, p.ID)
			})
		}
		switch {
		case err == nil && c != nil:
			fresh[p.ID] = c
		case cached != nil:
			report.Stale++
		default:
			report.Missing++
		}
	}
	localPath, open := OwnFiles(allFiles)
	audio, err := os.MkdirTemp(st.Dir, "export-")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(audio) }()
	content := func(id string) *doc.Content {
		c, ok := fresh[id]
		if !ok {
			c = st.Content(id)
		}
		return withAudio(src, id, WithTranscripts(st, id, c), audio)
	}
	openFile := func(path string) (io.ReadCloser, error) {
		if InFolder(audio, path) {
			return openRegular(path)
		}
		return open(path)
	}
	if err := Write(file, func(w io.Writer) error { return Zip(w, pages, folders, content, localPath, openFile) }); err != nil {
		return Report{}, err
	}
	return report, nil
}

func WithTranscripts(st *store.Store, pageID string, c *doc.Content) *doc.Content {
	if c == nil {
		return nil
	}
	c = c.Copy()
	for _, n := range recordings(c) {
		rec, err := st.Recording(n.Str("recording_id"))
		if err != nil || rec.PageID != pageID {
			continue
		}
		if lines := recording.TranscriptBlocks(rec); len(lines) > 0 {
			_ = c.Insert(doc.Position{After: n.ID}, lines...)
		}
	}
	return c
}

func recordings(c *doc.Content) []*doc.Node {
	var recs []*doc.Node
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "recording" {
			recs = append(recs, n)
		}
	})
	return recs
}

func Moments(c *doc.Content) func(string) string {
	return func(href string) string {
		at, ok := doc.LinkMoment(href)
		if !ok || c == nil {
			return href
		}
		n := c.Find(doc.LinkBlock(href))
		if n == nil {
			return href
		}
		switch n.Type {
		case "video":
			if v, ok := doc.ParseVideoURL(n.Str("url")); ok {
				return v.At(at).WatchURL()
			}
		case "recording", "file":
			return ""
		}
		return href
	}
}

func Save(path string, r io.Reader) error {
	return Write(path, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

func Write(path string, write func(w io.Writer) error) (err error) {
	perm := fs.FileMode(0o666)
	old, statErr := os.Stat(path)
	if statErr == nil {
		perm = old.Mode().Perm()
	}
	f, err := createBeside(path, perm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if statErr == nil {
		if err = os.Chmod(f.Name(), perm); err != nil {
			return err
		}
	}
	return os.Rename(f.Name(), path)
}

func createBeside(path string, perm fs.FileMode) (*os.File, error) {
	for {
		name := filepath.Join(filepath.Dir(path), ".leaflet-"+rand.Text())
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm) // #nosec G302 G304
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
}

const audioOf = "audio_of"

func withAudio(src Source, pageID string, c *doc.Content, dir string) *doc.Content {
	if c == nil || src.Rec == nil {
		return c
	}
	for _, n := range recordings(c) {
		rec, err := src.Store.Recording(n.Str("recording_id"))
		if err != nil || rec.PageID != pageID {
			continue
		}
		title := n.Str("title")
		if strings.TrimSpace(title) == "" {
			title = locales.ExportRecording()
		}
		name := SafeName(title) + ".wav"
		path := filepath.Join(dir, rec.ID, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			continue
		}
		if putAudio(src, rec, path) != nil {
			continue
		}
		file := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file://" + path, "name": name, audioOf: n.ID}}
		_ = c.Insert(doc.Position{After: n.ID}, file)
	}
	return c
}

func putAudio(src Source, rec store.Recording, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if _, err := os.Stat(rec.LocalPath); rec.LocalPath != "" && err == nil {
		if os.Link(rec.LocalPath, path) == nil {
			return nil
		}
		return copyAudio(rec.LocalPath, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	audio, err := src.Rec.OpenAudio(ctx, rec)
	if err != nil {
		return err
	}
	defer func() { _ = audio.Close() }()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	_, err = io.Copy(out, audio)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func copyAudio(from, path string) error {
	in, err := os.Open(from) // #nosec G304
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

var ErrNotOwnFile = errors.New("not a file of the app's")

func OwnFiles(files []store.File) (localPath func(fileID string) string, open func(path string) (io.ReadCloser, error)) {
	known := map[string]string{}
	owned := map[string]bool{}
	for _, f := range files {
		known[f.ID] = f.LocalPath
		owned[f.LocalPath] = f.LocalPath != ""
	}
	localPath = func(fileID string) string { return known[fileID] }
	open = func(path string) (io.ReadCloser, error) {
		if !owned[path] {
			return nil, ErrNotOwnFile
		}
		return openRegular(path)
	}
	return localPath, open
}

func openRegular(path string) (*os.File, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrNotOwnFile
	}
	return f, nil
}

func InFolder(dir, path string) bool {
	if dir == "" || !filepath.IsAbs(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return locales.CommonUntitled()
	}
	if strings.Trim(s, ".") == "" {
		return strings.ReplaceAll(s, ".", "-")
	}
	const maxName = 200
	if len(s) > maxName {
		ext := path.Ext(s)
		if len(ext) > 16 || strings.ContainsFunc(ext[min(len(ext), 1):], func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
		}) {
			ext = ""
		}
		cut := maxName - len(ext)
		for !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + ext
	}
	if s = strings.TrimRight(s, ". "); s == "" {
		return locales.CommonUntitled()
	}
	if stem, rest, dot := strings.Cut(s, "."); windowsDevice.MatchString(strings.TrimRight(stem, " ")) {
		s = stem + "-"
		if dot {
			s += "." + rest
		}
	}
	return s
}

var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])$`)

func Zip(w io.Writer, pages []store.Page, folders []store.Folder, content func(id string) *doc.Content,
	localPath func(fileID string) string, open func(string) (io.ReadCloser, error)) error {
	byID := map[string]store.Page{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	used := map[string]bool{}
	free := func(name string) bool { return !used[strings.ToLower(name)] && !used[strings.ToLower(name)+".md"] }
	claim := func(base string) string {
		name := base
		for n := 2; !free(name); n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		used[strings.ToLower(name)], used[strings.ToLower(name)+".md"] = true, true
		return name
	}
	folderByID := map[string]store.Folder{}
	for _, fo := range folders {
		folderByID[fo.ID] = fo
	}
	folderPaths := map[string]string{}
	var folderPath func(id string, seen map[string]bool) string
	folderPath = func(id string, seen map[string]bool) string {
		if p, ok := folderPaths[id]; ok {
			return p
		}
		fo := folderByID[id]
		seen[id] = true
		dir := ""
		if _, ok := folderByID[fo.ParentFolderID]; ok && !seen[fo.ParentFolderID] {
			dir = folderPath(fo.ParentFolderID, seen) + "/"
		}
		folderPaths[id] = claim(dir + SafeName(fo.Name))
		return folderPaths[id]
	}
	for _, fo := range folders {
		folderPath(fo.ID, map[string]bool{})
	}
	paths := map[string]string{}
	var pathOf func(id string, seen map[string]bool) string
	pathOf = func(id string, seen map[string]bool) string {
		if p, ok := paths[id]; ok {
			return p
		}
		p := byID[id]
		seen[id] = true
		dir := ""
		if _, ok := byID[p.ParentID]; ok && !seen[p.ParentID] {
			dir = pathOf(p.ParentID, seen) + "/"
		} else if dirOf, ok := folderPaths[p.FolderID]; ok {
			dir = dirOf + "/"
		}
		paths[id] = claim(dir + SafeName(p.DisplayTitle()))
		return paths[id]
	}
	for _, p := range pages {
		pathOf(p.ID, map[string]bool{})
	}

	z := zip.NewWriter(w)
	files := map[string]string{}
	videos := map[string]map[string]doc.Video{}
	media := map[string]map[string]string{}
	for _, p := range pages {
		c := content(p.ID)
		if c == nil {
			continue
		}
		videos[p.ID], media[p.ID] = map[string]doc.Video{}, map[string]string{}
		var fail error
		c.Walk(func(n, _ *doc.Node) {
			if v, ok := doc.ParseVideoURL(n.Str("url")); ok && n.Type == "video" {
				videos[p.ID][n.ID] = v
			}
			if n.Type != "image" && n.Type != "file" || fail != nil {
				return
			}
			src := n.Str("url")
			if n.Type == "file" {
				block := n.Str(audioOf)
				if block == "" {
					block = n.ID
				}
				media[p.ID][block] = src
			}
			local, ok := strings.CutPrefix(src, "file://")
			if _, done := files[src]; done || !ok {
				return
			}
			if p := localPath(n.Str("file_id")); p != "" {
				local = p
			}
			name, err := putFile(z, open, local, used, p.UpdatedAt)
			switch {
			case errors.Is(err, errNotRead):
			case err != nil:
				fail = err
			default:
				files[src] = name
			}
		})
		if fail != nil {
			return fail
		}
	}
	for _, p := range pages {
		c := content(p.ID)
		if c == nil {
			continue
		}
		own := paths[p.ID]
		rel := func(to string) string { return (&url.URL{Path: relPath(path.Dir(own), to)}).String() }
		md := doc.PageMarkdown(p.DisplayTitle(), c, doc.MarkdownOptions{
			Lang: i18n.Locale(),
			PageTitle: func(id string) string {
				if q, ok := byID[id]; ok {
					return q.DisplayTitle()
				}
				return locales.CommonUntitled()
			},
			Link: func(href string) string {
				if name, ok := files[href]; ok {
					return rel(name)
				}
				if id, ok := doc.PageLinkID(href); ok && paths[id] != "" {
					if at, ok := doc.LinkMoment(href); ok {
						block := doc.LinkBlock(href)
						if v, ok := videos[id][block]; ok {
							return v.At(at).WatchURL()
						}
						if name, ok := files[media[id][block]]; ok {
							return rel(name) + "#t=" + strconv.Itoa(at)
						}
					}
					if block := doc.LinkBlock(href); block != "" {
						return rel(paths[id]+".md") + "#" + block
					}
					return rel(paths[id] + ".md")
				}
				return href
			},
		})
		fw, err := create(z, own+".md", p.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, md); err != nil {
			return err
		}
	}
	return z.Close()
}

var errNotRead = errors.New("export: file not read")

func putFile(z *zip.Writer, open func(string) (io.ReadCloser, error), local string, used map[string]bool, at time.Time) (string, error) {
	in, err := open(local)
	if err != nil {
		return "", errors.Join(errNotRead, err)
	}
	defer func() { _ = in.Close() }()
	base := SafeName(filepath.Base(local))
	name := "files/" + base
	for n := 2; used[strings.ToLower(name)]; n++ {
		ext := path.Ext(base)
		name = fmt.Sprintf("files/%s (%d)%s", strings.TrimSuffix(base, ext), n, ext)
	}
	used[strings.ToLower(name)] = true
	fw, err := create(z, name, at)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, in); err != nil {
		return "", err
	}
	return name, nil
}

func create(z *zip.Writer, name string, at time.Time) (io.Writer, error) {
	if name != path.Clean(name) || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return nil, fmt.Errorf("export: unsafe name %q", name)
	}
	if at.IsZero() {
		at = time.Now()
	}
	return z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
}

func relPath(dir, to string) string {
	var from []string
	if dir != "." {
		from = strings.Split(dir, "/")
	}
	parts := strings.Split(to, "/")
	for len(from) > 0 && len(parts) > 1 && from[0] == parts[0] {
		from, parts = from[1:], parts[1:]
	}
	return strings.Repeat("../", len(from)) + strings.Join(parts, "/")
}
