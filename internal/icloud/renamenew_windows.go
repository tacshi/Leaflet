package icloud

import (
	"os"

	"golang.org/x/sys/windows"
)

// renameNew moves a file into place, as rename does, unless a file is
// there: then it fails with fs.ErrExist and leaves both, as a move that
// may not replace one does.
func renameNew(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(f, t, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
