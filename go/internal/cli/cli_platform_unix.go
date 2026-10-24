//go:build !windows

// Unix raw-command execution and statusline ownership checks.
package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isWindowsPlatform tells joinCommandArgs whether it is running on win32.
func isWindowsPlatform() bool { return false }

// statuslineDirOwnerOK ports the getuid() ownership check for a symlinked
// Claude config dir.
func statuslineDirOwnerOK(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}

// rawCommand mirrors the POSIX spawn in src/cli-tools.ts:runRaw.
func rawCommand(shell, command string) *exec.Cmd {
	return exec.Command(shell, "-c", command)
}

// rawExitCode is the child exit event: 128+n on signal, else the exit code.
func rawExitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 1
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return 128 + int(status.Signal())
		}
	}
	code := exitErr.ExitCode()
	if code < 0 {
		return 1
	}
	return code
}

// nodeSpawnMessage mirrors Node's child_process error message for a spawn
// failure.
func nodeSpawnMessage(shell string, err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "spawn " + shell + " ENOENT"
	}
	return err.Error()
}
