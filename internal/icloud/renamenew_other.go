//go:build !darwin && !windows

package icloud

// renameNew moves a file into place, as rename does, unless a file is
// there: then it fails with fs.ErrExist and leaves both.
func renameNew(from, to string) error { return linkNew(from, to) }
