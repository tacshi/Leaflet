//go:build !windows

package icloud

import "os"

func linkNew(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	_ = os.Remove(from)
	return nil
}
