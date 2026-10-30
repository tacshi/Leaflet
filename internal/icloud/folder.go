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

// Name checks a file's name for the folder: one path element.
func Name(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return "", fmt.Errorf("icloud: bad file name %q", name)
	}
	return name, nil
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
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".icloud") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "."), ".icloud"), true
}

// names lists a directory of the folder: the files and directories in it,
// with those iCloud has not downloaded under their own names. It leaves
// out what starts with a dot otherwise, as files being written do. A
// directory that is not there has nothing in it.
func (f Folder) names(rel string) ([]string, error) {
	entries, err := os.ReadDir(f.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, access(err, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if real, ok := fromPlaceholder(name); ok {
			out = append(out, real)
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
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
func (f Folder) Exists(rel string) bool {
	path := f.abs(rel)
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	_, err := os.Lstat(filepath.Join(filepath.Dir(path), placeholder(filepath.Base(path))))
	return err == nil
}

// Read reads a file, or returns fs.ErrNotExist, or ErrNotReady after
// asking iCloud to download it.
func (f Folder) Read(rel string) ([]byte, error) {
	path := f.abs(rel)
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
	data, err := os.ReadFile(path) // #nosec G304 -- a file of the Leaflet folder, by a path checked above.
	if err != nil {
		return nil, access(err, err)
	}
	return data, nil
}

func (f Folder) download(path string) error {
	if err := startDownload(path); err != nil {
		return fmt.Errorf("%w: %v", ErrNotReady, err)
	}
	return ErrNotReady
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
		return fmt.Errorf("%w: %w: %s: %v", ErrNotReady, ErrDamaged, rel, err)
	}
	if h.Format > Format {
		return ErrNewerFormat
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %w: %s: %v", ErrNotReady, ErrDamaged, rel, err)
	}
	return nil
}

// WriteJSON writes v to a file, whole or not at all.
func (f Folder) WriteJSON(rel string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.write(rel, func(w io.Writer) error {
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
	if f.Exists(rel) {
		// One iCloud has not downloaded counts as whole.
		if have, err := os.Lstat(f.abs(rel)); err != nil || have.Size() == info.Size() {
			return nil
		}
	}
	return f.write(rel, func(w io.Writer) error {
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

// write writes a file to Temp and moves it into place, so a device
// reading the folder never sees half of it. Where Temp is on another disk
// it writes beside the file instead, under a name that starts with a dot,
// which readers pass over. The file reaches the disk before it moves, and
// the move before write returns: a crash after it, with the database
// saying the file was written, must not leave it cut short or gone.
func (f Folder) write(rel string, fill func(io.Writer) error) error {
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
		tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
			err = rename(tmp.Name(), path)
		}
		if err == nil {
			syncDir(filepath.Dir(path))
			return nil
		}
		_ = os.Remove(tmp.Name())
		last = err
	}
	return access(last, last)
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
