//go:build windows

package compression

import (
	"os/exec"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	errorAccessDenied              = 5
)

// processAlive mirrors process.kill(pid, 0) on Windows: an access-denied open
// means the process exists (Node reports EPERM), a successful open with a
// non-STILL_ACTIVE exit code means it is gone.
// port of src/compression/utils.ts:pruneDeadSessions (process.kill branch)
func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == errorAccessDenied {
			return true
		}
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// configureHidden mirrors execFile({ windowsHide: true }).
func configureHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
