//go:build !darwin

package app

func translocated(string) bool { return false }
