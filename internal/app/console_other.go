//go:build !windows

package app

import "os/exec"

func hideConsole(*exec.Cmd) {}
