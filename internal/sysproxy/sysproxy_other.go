//go:build !darwin && !windows

package sysproxy

func read() settings { return settings{} }
