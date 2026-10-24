//go:build !windows

package compression

import (
	"errors"
	"os/exec"
	"syscall"
)

// processAlive mirrors process.kill(pid, 0): nil or EPERM means alive.
// port of src/compression/utils.ts:pruneDeadSessions (process.kill branch)
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// configureHidden sets platform-specific child process attributes.
// TS uses execFile({ windowsHide: true }); POSIX has no equivalent.
func configureHidden(cmd *exec.Cmd) {}
