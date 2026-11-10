//go:build !darwin && !windows

package icloud

func renameNew(from, to string) error { return linkNew(from, to) }
