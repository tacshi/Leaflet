package app

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole keeps a command-line program the app runs from opening a
// console window of its own, which Windows gives one started by an app
// that has none.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
