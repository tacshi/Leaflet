//go:build !darwin && !windows

package app

import "errors"

func commandState(string, string) commandStatus { return commandStatus{} }

func installCommand(string, string, string) error { return errors.ErrUnsupported }

func removeCommand(string, string, string) error { return errors.ErrUnsupported }
