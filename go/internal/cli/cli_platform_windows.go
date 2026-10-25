//go:build windows

// Windows raw-command execution and statusline ownership checks.
package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// isWindowsPlatform tells joinCommandArgs the process is on win32.
func isWindowsPlatform() bool { return true }

// rawCommand mirrors Node's win32 spawn: cmd.exe gets /d /s /c with verbatim
// arguments, every other shell gets -c.
func rawCommand(shell, command string) *exec.Cmd {
	base := strings.ToLower(filepath.Base(shell))
	if base == "cmd" || base == "cmd.exe" {
		cmd := exec.Command(shell)
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: shell + " /d /s /c " + command}
		return cmd
	}
	return exec.Command(shell, "-c", command)
}

// rawExitCode is the child exit event on win32: Node reports no signal, only
// the exit code.
func rawExitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 1
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
