package icloud

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNew moves a file into place, as rename does, unless a file is
// there: then it fails with fs.ErrExist and leaves both. APFS and HFS+
// check that as they move it; a disk that cannot has it linked there
// instead.
func renameNew(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return linkNew(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
