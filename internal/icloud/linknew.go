//go:build !windows

package icloud

import "os"

// linkNew is renameNew by a link to the file, which fails where a file is,
// and then the file's removal.
func linkNew(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	_ = os.Remove(from)
	return nil
}
