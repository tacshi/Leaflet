// Package icloud reads and writes the Leaflet folder in iCloud Drive,
// which iCloud sync goes through (docs/protocol.md §8). Every device
// writes only its own files there, so iCloud never has two versions of
// one to keep; the folder knows nothing of pages, which internal/sync
// reads out of what it holds.
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
	// ErrUnavailable is the folder's when iCloud Drive is not on this
	// device, or the Leaflet folder in it has gone.
	ErrUnavailable = errors.New("icloud: iCloud Drive is not available")
	// ErrNoAccess is the folder's when the system keeps Leaflet out of
	// iCloud Drive.
	ErrNoAccess = errors.New("icloud: no access to iCloud Drive")
	// ErrOtherLibrary is the folder's when it holds another set of pages
	// than the ones this device syncs.
	ErrOtherLibrary = errors.New("icloud: the Leaflet folder holds other pages")
	// ErrNewerFormat is a file's that a newer Leaflet wrote.
	ErrNewerFormat = errors.New("icloud: written by a newer Leaflet")
	// ErrNotReady is a file's that iCloud has not downloaded yet; it has
	// been asked to.
	ErrNotReady = errors.New("icloud: not downloaded yet")
	// ErrDamaged is, with ErrNotReady, a file's that is here but does not
	// parse: one iCloud is still writing, or one a crash cut short before
	// it reached the disk, which stays so.
	ErrDamaged = errors.New("icloud: does not parse")
)

// Format is the folder's format, in every file it writes.
const Format = 1

// Root is the Leaflet folder in iCloud Drive, or "" where there is none.
// LEAFLET_ICLOUD_DIR names another folder, for tests and trying sync out
// on one machine.
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

// Available reports whether iCloud Drive is on this device. It looks only
// at the folder the Leaflet folder goes in, not into it, so it asks the
// system for no access.
func Available() bool {
	root := Root()
	if root == "" {
		return false
	}
	info, err := os.Lstat(filepath.Dir(root))
	return err == nil && info.IsDir()
}

// Folder is the Leaflet folder in iCloud Drive.
type Folder struct {
	Root string
	// Temp is where files are written before they move into the folder:
	// a folder of the app's own on the same disk.
	Temp string
}

var (
	idPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	seqPattern = regexp.MustCompile(`^[0-9]{16}\.json$`)
)

// ValidID reports whether id is one of the folder's: a row or device ID.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Name checks a file's name for the folder, one path element, and returns
// it as PortableName makes it.
func Name(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", fmt.Errorf("icloud: bad file name %q", name)
	}
	return PortableName(name), nil
}

// PortableName is a file's name as every system with iCloud Drive can hold
// it, so a file named on a Mac syncs to Windows too: the characters
// Windows takes in no name (<>:"/\|?* and control characters) become "_",
// the dots and spaces it drops from the end go, and a name it keeps for a
// device (CON, NUL, COM1…, with an extension or not) gets "_" before it.
// A name that is so already is left as it is, and so is one made so.
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

// reservedName reports whether Windows keeps a name for a device.
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

// Paths in the folder, separated by slashes.

func devicePath(device string) string { return "devices/" + device }

// DevicePath is a device's device.json.
func DevicePath(device string) (string, error) {
	return devicePath(device) + "/device.json", checkID(device)
}

// BatchPath is a batch of a device's log.
func BatchPath(device string, seq int64) (string, error) {
	return fmt.Sprintf("%s/log/%016d.json", devicePath(device), seq), checkID(device)
}

// PagePath is a device's version of a page.
func PagePath(device, pageID string) (string, error) {
	if err := checkID(pageID); err != nil {
		return "", err
	}
	return devicePath(device) + "/pages/" + pageID + ".json", checkID(device)
}

// FileBlobPath is a file's copy.
func FileBlobPath(fileID, name string) (string, error) {
	if err := checkID(fileID); err != nil {
		return "", err
	}
	n, err := Name(name)
	return "blobs/files/" + fileID + "/" + n, err
}

// RecordingBlobPath is a recording's audio.
func RecordingBlobPath(recordingID string) (string, error) {
	return "blobs/recordings/" + recordingID + ".wav", checkID(recordingID)
}

func (f Folder) abs(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

// Check reports whether the folder can be used: ErrUnavailable when iCloud
// Drive is not there, ErrNoAccess when the system keeps Leaflet out.
// create makes the Leaflet folder when iCloud Drive is there without it.
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

// access is ErrNoAccess for an error that says so, and otherwise.
func access(err, otherwise error) error {
	if errors.Is(err, fs.ErrPermission) {
		return ErrNoAccess
	}
	return otherwise
}

// placeholder is the name iCloud gives a file of name it has not
// downloaded, on macOS before 14.
func placeholder(name string) string { return "." + name + ".icloud" }

// fromPlaceholder is the name of the file a placeholder stands for.
func fromPlaceholder(name string) (string, bool) {
	if len(name) <= len("..icloud") || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".icloud") {
		return "", false
	}
	return name[1 : len(name)-len(".icloud")], true
}

// names lists a directory of the folder: the files and directories in it,
// with those iCloud has not downloaded under their own names, each once,
// as a file and its placeholder may be there both while iCloud brings it.
// It leaves out what starts with a dot, as files being written do, and
// placeholders of those. A directory that is not there has nothing in it.
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

// Devices lists the devices with a folder in it. Names that are no
// device's, such as the copies iCloud makes of a folder two devices
// made at once, are left out.
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

// Batches lists the seqs of a device's log in order, and reports whether
// its folder holds names a device never writes there: copies iCloud made
// of a file two devices wrote, which a second device with this one's ID
// leaves.
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

// Exists reports whether a file is in the folder, downloaded or not.
func (f Folder) Exists(rel string) bool { return f.exists(f.blob(rel)) }

// Stamp is what a file's metadata says of it: its time and size, which
// change as another device writes it again, read without reading the file.
type Stamp struct {
	ModTime time.Time
	Size    int64
}

// Stamp returns a file's Stamp, as it is downloaded: ok is false for one
// not there, or not downloaded yet.
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

// blob is where the folder has a file's copy, by its path rel: there, or,
// while only that is there, under the name a Leaflet before PortableName
// gave it, which PortableName makes rel's, as a Mac's "Q3: report.pdf".
// Other paths are as they are.
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

// Read reads a file, or returns fs.ErrNotExist, or ErrNotReady after
// asking iCloud to download it.
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

// Open opens a file to read as Read reads it, a little at a time, as a
// recording's audio is too large to read whole: fs.ErrNotExist, or
// ErrNotReady after asking iCloud to download it. Read to its end, it is
// as long as it was when it opened, or fails with io.ErrUnexpectedEOF.
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
	file, err := os.Open(path) // #nosec G304 -- a file of the Leaflet folder, by a path checked above.
	if err != nil {
		return nil, access(err, err)
	}
	return &sized{file: file, left: info.Size()}, nil
}

// sized is a file read to the size it had as it opened: one iCloud cuts
// short meanwhile ends in io.ErrUnexpectedEOF, and what it gains after is
// not read. It reads only through Read, so io.Copy goes through the check.
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

// Download asks iCloud to download a file it has not, without waiting for
// it, so a later Read finds it here. iCloud on iOS downloads only what it
// is asked for, and asked one file at a time, many come one by one. A
// file downloaded already, or not in the folder, is left as it is.
func (f Folder) Download(rel string) {
	rel = f.blob(rel)
	path := f.abs(rel)
	info, err := os.Lstat(path)
	if err == nil && notReady(info) || errors.Is(err, fs.ErrNotExist) && f.exists(rel) {
		_ = f.download(path)
	}
}

// header is what every file of the folder starts with.
type header struct {
	Format int `json:"format"`
}

// ReadJSON reads a file into v. A file that does not parse is one iCloud
// is still writing, or one cut short, and both ErrNotReady and ErrDamaged;
// one from a newer Leaflet is ErrNewerFormat.
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

// WriteJSON writes v to a file, whole or not at all.
func (f Folder) WriteJSON(rel string, v any) error {
	return f.writeJSON(rel, v, rename)
}

// CreateJSON writes v to a file that is not there yet, whole or not at
// all, as WriteJSON does, and fails with fs.ErrExist, leaving it, when one
// is, downloaded or not: a file that another device made meanwhile is
// never written over. Checked as the file moves into place, two devices
// writing at once cannot both make it.
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

// CopyIn copies a file of this device into the folder, whole or not at
// all. A file already there stays: the folder's copies never change. One
// of another size than the file is a copy cut short, and goes again.
func (f Folder) CopyIn(rel, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if f.exists(rel) && f.whole(rel, info.Size()) {
		return nil
	}
	return f.write(rel, rename, func(w io.Writer) error {
		// Opened for each try: the one before read it to the end.
		in, err := os.Open(source) // #nosec G304 -- a file in the app's own data folder.
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

// whole reports whether the file at rel has size bytes, or is one iCloud
// has not downloaded, which counts as whole.
func (f Folder) whole(rel string, size int64) bool {
	have, err := os.Lstat(f.abs(rel))
	return err != nil || have.Size() == size
}

// write writes a file to Temp and moves it into place with move, so a
// device reading the folder never sees half of it. Where Temp is on
// another disk it writes beside the file instead, under a name that starts
// with a dot, which readers pass over. The file reaches the disk before it
// moves, and the move before write returns: a crash after it, with the
// database saying the file was written, must not leave it cut short or
// gone. A move that finds a file there (renameNew) leaves both.
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
			// On macOS Sync is F_FULLFSYNC, past the disk's own cache.
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

// tempPattern names the file write writes before it moves into place:
// the file's name with a dot before it, cut short, as the disk takes no
// name of more than 255 bytes and a file's own name may be that long.
func tempPattern(name string) string {
	if len(name) > 64 {
		name = strings.ToValidUTF8(name[:64], "")
	}
	return "." + name + ".*.tmp"
}

// syncDir has a move into dir reach the disk, where the system allows it:
// Windows opens no directory to sync.
func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304 -- a directory of the Leaflet folder.
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// rename moves a file into place. Windows refuses while another program,
// such as iCloud uploading the file, has it open: try again a few times.
// Tests stand in for it.
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

// RemoveBlob removes a file's or a recording's copy. A file's goes with
// the folder it is in, and so with the copies iCloud made of it there
// ("notes 2.pdf") when two devices copied it in at once.
func (f Folder) RemoveBlob(rel string) error {
	dir := path.Dir(rel)
	if path.Dir(dir) != "blobs/files" || !ValidID(path.Base(dir)) {
		return f.Remove(rel)
	}
	err := os.RemoveAll(f.abs(dir))
	return access(err, err)
}

// Remove removes a file of this device's.
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
