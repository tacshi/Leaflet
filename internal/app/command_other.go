//go:build !darwin && !windows

package app

import "errors"

// The command line has nothing to install here: Leaflet runs on macOS and
// Windows.

func commandState(string, string) commandStatus { return commandStatus{} }

func installCommand(string, string, string) error { return errors.ErrUnsupported }

func removeCommand(string, string, string) error { return errors.ErrUnsupported }
