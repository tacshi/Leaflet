package icloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnavailable  = errors.New("icloud: iCloud Drive is not available")
	ErrNoAccess     = errors.New("icloud: no access to iCloud Drive")
	ErrOtherLibrary = errors.New("icloud: the Leaflet folder holds other pages")
	ErrNewerFormat  = errors.New("icloud: written by a newer Leaflet")
	ErrNotReady     = errors.New("icloud: not downloaded yet")
	ErrDamaged      = errors.New("icloud: does not parse")
)

const Format = 1

func Root() string {
	if dir := os.Getenv("LEAFLET_ICLOUD_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Leaflet")
	case "windows":
		return filepath.Join(home, "iCloudDrive", "Leaflet")
	}
	return ""
}

func Available() bool {
	root := Root()
	if root == "" {
		return false
	}
	info, err := os.Lstat(filepath.Dir(root))
	return err == nil && info.IsDir()
}

type Folder struct {
	Root string
	Temp string
}

var (
	idPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	seqPattern = regexp.MustCompile(`^[0-9]{16}\.json$`)
)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func Name(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", fmt.Errorf("icloud: bad file name %q", name)
	}
	return PortableName(name), nil
}

func PortableName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	if name = strings.TrimRight(name, ". "); name == "" {
		return "_"
	}
	stem, _, _ := strings.Cut(name, ".")
	if reservedName(strings.TrimRight(stem, " ")) {
		name = "_" + name
	}
	return name
}

func reservedName(stem string) bool {
	up := strings.ToUpper(stem)
	switch up {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(up) < 4 || up[:3] != "COM" && up[:3] != "LPT" {
		return false
	}
	switch up[3:] {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

func checkID(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("icloud: bad id %q", id)
	}
	return nil
}

func devicePath(device string) string { return "devices/" + device }

func DevicePath(device string) (string, error) {
	return devicePath(device) + "/device.json", checkID(device)
}

func BatchPath(device string, seq int64) (string, error) {
	return fmt.Sprintf("%s/log/%016d.json", devicePath(device), seq), checkID(device)
}

func PagePath(device, pageID string) (string, error) {
	if err := checkID(pageID); err != nil {
		return "", err
	}
	return devicePath(device) + "/pages/" + pageID + ".json", checkID(device)
}

func FileBlobPath(fileID, name string) (string, error) {
	if err := checkID(fileID); err != nil {
		return "", err
	}
	n, err := Name(name)
	return "blobs/files/" + fileID + "/" + n, err
}

func RecordingBlobPath(recordingID string) (string, error) {
	return "blobs/recordings/" + recordingID + ".wav", checkID(recordingID)
}

func (f Folder) abs(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

func (f Folder) Check(create bool) error {
	if info, err := os.Lstat(filepath.Dir(f.Root)); err != nil || !info.IsDir() {
		return access(err, ErrUnavailable)
	}
	if _, err := os.ReadDir(f.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) && create {
			if err := os.Mkdir(f.Root, 0o750); err != nil {
				return access(err, ErrUnavailable)
			}
			return nil
		}
		return access(err, ErrUnavailable)
	}
	return nil
}

func access(err, otherwise error) error {
	if errors.Is(err, fs.ErrPermission) {
		return ErrNoAccess
	}
	return otherwise
}

func placeholder(name string) string { return "." + name + ".icloud" }

func fromPlaceholder(name string) (string, bool) {
	if len(name) <= len("..icloud") || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".icloud") {
		return "", false
	}
	return name[1 : len(name)-len(".icloud")], true
}

func (f Folder) names(rel string) ([]string, error) {
	entries, err := os.ReadDir(f.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, access(err, err)
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if real, ok := fromPlaceholder(name); ok {
			name = real
		}
		if strings.HasPrefix(name, ".") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func (f Folder) Devices() ([]string, error) {
	names, err := f.names("devices")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if ValidID(n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f Folder) Batches(device string) (seqs []int64, copies bool, err error) {
	if err := checkID(device); err != nil {
		return nil, false, err
	}
	names, err := f.names(devicePath(device) + "/log")
	if err != nil {
		return nil, false, err
	}
	for _, n := range names {
		if !seqPattern.MatchString(n) {
			copies = true
			continue
		}
		seq, err := strconv.ParseInt(strings.TrimSuffix(n, ".json"), 10, 64)
		if err == nil {
			seqs = append(seqs, seq)
		}
	}
	slices.Sort(seqs)
	return seqs, copies, nil
}

func (f Folder) Exists(rel string) bool { return f.exists(f.blob(rel)) }

type Stamp struct {
	ModTime time.Time
	Size    int64
}

func (f Folder) Stamp(rel string) (stamp Stamp, ok bool) {
	info, err := os.Lstat(f.abs(rel))
	if err != nil || notReady(info) {
		return Stamp{}, false
	}
	return Stamp{info.ModTime(), info.Size()}, true
}

func (f Folder) exists(rel string) bool {
	path := f.abs(rel)
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	_, err := os.Lstat(filepath.Join(filepath.Dir(path), placeholder(filepath.Base(path))))
	return err == nil
}

func (f Folder) blob(rel string) string {
	if !strings.HasPrefix(rel, "blobs/files/") || f.exists(rel) {
		return rel
	}
	dir, base := path.Split(rel)
	names, err := f.names(strings.TrimSuffix(dir, "/"))
	if err != nil {
		return rel
	}
	for _, n := range names {
		if n != base && PortableName(n) == base {
			return dir + n
		}
	}
	return rel
}

func (f Folder) Read(rel string) ([]byte, error) {
	r, err := f.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, access(err, err)
	}
	return data, nil
}

func (f Folder) Open(rel string) (io.ReadCloser, error) {
	path := f.abs(f.blob(rel))
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, perr := os.Lstat(filepath.Join(filepath.Dir(path), placeholder(filepath.Base(path)))); perr == nil {
			return nil, f.download(path)
		}
		return nil, err
	}
	if err != nil {
		return nil, access(err, err)
	}
	if notReady(info) {
		return nil, f.download(path)
	}
	file, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, access(err, err)
	}
	return &sized{file: file, left: info.Size()}, nil
}

type sized struct {
	file *os.File
	left int64
}

func (s *sized) Read(p []byte) (int, error) {
	if s.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.file.Read(p)
	s.left -= int64(n)
	if errors.Is(err, io.EOF) && s.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (s *sized) Close() error { return s.file.Close() }

func (f Folder) download(path string) error {
	if StartDownload == nil {
		return fmt.Errorf("%w: the app can't ask iCloud for the file", ErrNotReady)
	}
	if err := StartDownload(path); err != nil {
		return fmt.Errorf("%w: %w", ErrNotReady, err)
	}
	return ErrNotReady
}

func (f Folder) Download(rel string) {
	rel = f.blob(rel)
	path := f.abs(rel)
	info, err := os.Lstat(path)
	if err == nil && notReady(info) || errors.Is(err, fs.ErrNotExist) && f.exists(rel) {
		_ = f.download(path)
	}
}

type header struct {
	Format int `json:"format"`
}

func (f Folder) ReadJSON(rel string, v any) error {
	data, err := f.Read(rel)
	if err != nil {
		return err
	}
	var h header
	if err := json.Unmarshal(data, &h); err != nil {
		return fmt.Errorf("%w: %w: %s: %w", ErrNotReady, ErrDamaged, rel, err)
	}
	if h.Format > Format {
		return ErrNewerFormat
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %w: %s: %w", ErrNotReady, ErrDamaged, rel, err)
	}
	return nil
}

func (f Folder) WriteJSON(rel string, v any) error {
	return f.writeJSON(rel, v, rename)
}

func (f Folder) CreateJSON(rel string, v any) error {
	if f.exists(rel) {
		return fmt.Errorf("icloud: %s: %w", rel, fs.ErrExist)
	}
	return f.writeJSON(rel, v, renameNew)
}

func (f Folder) writeJSON(rel string, v any, move func(from, to string) error) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.write(rel, move, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func (f Folder) CopyIn(rel, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if f.exists(rel) && f.whole(rel, info.Size()) {
		return nil
	}
	return f.write(rel, rename, func(w io.Writer) error {
		in, err := os.Open(source) // #nosec G304
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		size := info.Size()
		if st, err := in.Stat(); err == nil {
			size = st.Size()
		}
		n, err := io.Copy(w, in)
		if err == nil && n != size {
			err = fmt.Errorf("icloud: copied %d of %d bytes of %s", n, size, filepath.Base(source))
		}
		return err
	})
}

func (f Folder) whole(rel string, size int64) bool {
	have, err := os.Lstat(f.abs(rel))
	return err != nil || have.Size() == size
}

func (f Folder) write(rel string, move func(from, to string) error, fill func(io.Writer) error) error {
	path := f.abs(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return access(err, err)
	}
	dirs := []string{f.Temp, filepath.Dir(path)}
	if f.Temp == "" {
		dirs = dirs[1:]
	}
	var last error
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			last = err
			continue
		}
		tmp, err := os.CreateTemp(dir, tempPattern(filepath.Base(path)))
		if err != nil {
			last = err
			continue
		}
		err = fill(tmp)
		if err == nil {
			err = tmp.Sync()
		}
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = move(tmp.Name(), path)
		}
		if err == nil {
			syncDir(filepath.Dir(path))
			return nil
		}
		_ = os.Remove(tmp.Name())
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		last = err
	}
	return access(last, last)
}

func tempPattern(name string) string {
	if len(name) > 64 {
		name = strings.ToValidUTF8(name[:64], "")
	}
	return "." + name + ".*.tmp"
}

func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

var rename = func(from, to string) error {
	var err error
	for try := 0; try < 5; try++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(time.Duration(50*(try+1)) * time.Millisecond)
	}
	return err
}

func (f Folder) RemoveBlob(rel string) error {
	dir := path.Dir(rel)
	if path.Dir(dir) != "blobs/files" || !ValidID(path.Base(dir)) {
		return f.Remove(rel)
	}
	err := os.RemoveAll(f.abs(dir))
	return access(err, err)
}

func (f Folder) Remove(rel string) error {
	path := f.abs(rel)
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = os.Remove(filepath.Join(filepath.Dir(path), placeholder(filepath.Base(path))))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
