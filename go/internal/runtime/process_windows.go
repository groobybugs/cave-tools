//go:build windows

// Windows process and signal primitives for the runtime core.
//
// Mirrors the win32 branches of src/runtime/process.ts: COMSPEC default shell
// (line 28), killProcessGroup's single-pid kill (lines 108-116), the
// non-detached spawn with windowsHide (lines 150, 200-207, 233-240) and the
// kill closure's `proc.kill(signal)` fallback (lines 251-258).
package runtime

import (
	"os"
	"os/exec"
	"regexp"
	"syscall"
)

// shellCommand mirrors the win32 spawn in src/runtime/process.ts:231-241.
// Node uses `/d /s /c` only for cmd.exe and `-c` for every other shell
// (normalizeSpawnArguments). Node also sets windowsVerbatimArguments; os/exec
// has no verbatim mode, so the command goes through Go's Windows quoting.
func shellCommand(shell, command string) *exec.Cmd {
	if isCmdExe(shell) {
		return exec.Command(shell, "/d", "/s", "/c", command)
	}
	return exec.Command(shell, "-c", command)
}

// cmdShellPattern is Node's cmd.exe detection: /^(?:.*\\)?cmd(?:\.exe)?$/i.
var cmdShellPattern = regexp.MustCompile(`(?i)^(?:.*\\)?cmd(?:\.exe)?$`)

func isCmdExe(shell string) bool {
	return cmdShellPattern.MatchString(shell)
}

// configureSpawn mirrors `windowsHide: true`, which every spawn in
// process.ts (plus ripgrep.ts:39 and bins.ts:160) sets. TS never sets
// `detached` on win32, so there is no process group to create.
func configureSpawn(cmd *exec.Cmd, detached bool) {
	_ = detached
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// terminateProcess is the Go form of the runCommand kill closure on win32:
// `proc.kill(signal)` terminates only the child, with no process group
// (src/runtime/process.ts:251-258).
func terminateProcess(pid int, signal processSignal) {
	_ = signal
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Kill()
}

// port of src/runtime/process.ts:isProcessAlive (win32)
//
// process.kill(pid, 0) probes the process (libuv uv__kill case 0) and throws
// when it is missing or inaccessible; os.FindProcess opens the same handle.
func IsProcessAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}

// windowsSignals is os.constants.signals on win32. Node only exposes the
// signals whose macros are defined when node_constants.cc compiles
// DefineSignalConstants: the Windows CRT's <csignal> plus the SIGHUP/SIGQUIT/
// SIGKILL/SIGWINCH that libuv's uv/win.h defines. process.kill rejects every
// other name with ERR_UNKNOWN_SIGNAL before libuv sees it.
var windowsSignals = map[string]struct{}{
	"SIGHUP":   {},
	"SIGINT":   {},
	"SIGQUIT":  {},
	"SIGILL":   {},
	"SIGFPE":   {},
	"SIGKILL":  {},
	"SIGSEGV":  {},
	"SIGTERM":  {},
	"SIGBREAK": {},
	"SIGABRT":  {},
	"SIGWINCH": {},
}

// windowsTerminatingSignals are the signals libuv's uv__kill actually turns
// into TerminateProcess on Windows; every other name in the table reaches the
// default case and fails with UV_ENOSYS, which killProcessGroup catches.
var windowsTerminatingSignals = map[string]struct{}{
	"SIGINT":  {},
	"SIGQUIT": {},
	"SIGTERM": {},
	"SIGKILL": {},
}

// port of src/runtime/process.ts:killProcessGroup (win32)
//
// process.kill(pid, signal) validates the name, ignores which signal it is
// beyond libuv's terminating set, and terminates the single pid.
func KillProcessGroup(pid int, signal string) bool {
	if signal == "" {
		// Node: `sig ||= "SIGTERM"`.
		signal = "SIGTERM"
	}
	if _, ok := windowsSignals[signal]; !ok {
		return false
	}
	if _, ok := windowsTerminatingSignals[signal]; !ok {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Kill() == nil
}

// waitResult mirrors the child_process close event on win32: Node always
// reports a null signal and only the exit code (src/runtime/process.ts:281).
func waitResult(cmd *exec.Cmd) (*int, *string) {
	state := cmd.ProcessState
	if state == nil {
		return nil, nil
	}
	code := state.ExitCode()
	if code < 0 {
		return nil, nil
	}
	return &code, nil
}
