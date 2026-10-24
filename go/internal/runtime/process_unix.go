//go:build !windows

// Unix process and signal primitives for the runtime core.
//
// Mirrors the non-win32 branches of src/runtime/process.ts: detached process
// groups (src/runtime/process.ts:233-240, 150, 200-207), group kills
// (src/runtime/process.ts:108-116, 251-258) and POSIX signal names
// (src/runtime/process.ts:5-25).
package runtime

import (
	"os/exec"
	"strconv"
	"syscall"
)

// shellCommand mirrors the non-win32 spawn in src/runtime/process.ts:231-241
// (`/bin/sh -c <command>`).
func shellCommand(shell, command string) *exec.Cmd {
	return exec.Command(shell, "-c", command)
}

// configureSpawn mirrors the spawn options: `detached` starts a new process
// group on POSIX (src/runtime/process.ts:150,200,233) so a kill reaches the
// whole tree. Windows has no equivalent and is handled in process_windows.go.
func configureSpawn(cmd *exec.Cmd, detached bool) {
	if detached {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

// terminateProcess mirrors the runCommand kill closure: POSIX sends the
// signal to the detached process group (src/runtime/process.ts:251-258).
func terminateProcess(pid int, signal processSignal) {
	sig := syscall.SIGTERM
	if signal == signalKill {
		sig = syscall.SIGKILL
	}
	// Process already exited: ignore, exactly like the TS catch.
	_ = syscall.Kill(-pid, sig)
}

// port of src/runtime/process.ts:isProcessAlive
func IsProcessAlive(pid int) bool {
	// process.kill(pid, 0): EPERM throws in Node, so it is not "alive" here.
	return syscall.Kill(pid, 0) == nil
}

// port of src/runtime/process.ts:killProcessGroup
func KillProcessGroup(pid int, signal string) bool {
	sig, ok := signalFromName(signal)
	if !ok {
		return false
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		return false
	}
	return true
}

// waitResult mirrors the child_process close event on POSIX: a signalled child
// reports a null exit code and the signal name (src/runtime/process.ts:281-298).
func waitResult(cmd *exec.Cmd) (*int, *string) {
	state := cmd.ProcessState
	if state == nil {
		return nil, nil
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		name := signalName(status.Signal())
		return nil, &name
	}
	code := state.ExitCode()
	if code < 0 {
		// Unreachable on Unix (signalled processes are handled above); keep
		// TS's null code instead of a sentinel.
		return nil, nil
	}
	return &code, nil
}

// signalNames mirrors the POSIX entries of os.constants.signals that Node
// resolves in process.kill and that Go's syscall package exposes on every Unix
// port (linux, darwin).
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:    "SIGHUP",
	syscall.SIGINT:    "SIGINT",
	syscall.SIGQUIT:   "SIGQUIT",
	syscall.SIGILL:    "SIGILL",
	syscall.SIGTRAP:   "SIGTRAP",
	syscall.SIGABRT:   "SIGABRT",
	syscall.SIGBUS:    "SIGBUS",
	syscall.SIGFPE:    "SIGFPE",
	syscall.SIGKILL:   "SIGKILL",
	syscall.SIGUSR1:   "SIGUSR1",
	syscall.SIGSEGV:   "SIGSEGV",
	syscall.SIGUSR2:   "SIGUSR2",
	syscall.SIGPIPE:   "SIGPIPE",
	syscall.SIGALRM:   "SIGALRM",
	syscall.SIGTERM:   "SIGTERM",
	syscall.SIGCHLD:   "SIGCHLD",
	syscall.SIGCONT:   "SIGCONT",
	syscall.SIGSTOP:   "SIGSTOP",
	syscall.SIGTSTP:   "SIGTSTP",
	syscall.SIGTTIN:   "SIGTTIN",
	syscall.SIGTTOU:   "SIGTTOU",
	syscall.SIGURG:    "SIGURG",
	syscall.SIGXCPU:   "SIGXCPU",
	syscall.SIGXFSZ:   "SIGXFSZ",
	syscall.SIGVTALRM: "SIGVTALRM",
	syscall.SIGPROF:   "SIGPROF",
	syscall.SIGWINCH:  "SIGWINCH",
	syscall.SIGIO:     "SIGIO",
	syscall.SIGSYS:    "SIGSYS",
}

// signalsByName is the reverse table. Node resolves signal names with an exact
// property lookup on os.constants.signals, but defaults a falsy name to
// SIGTERM (`sig ||= "SIGTERM"` in process.kill).
var signalsByName = func() map[string]syscall.Signal {
	byName := make(map[string]syscall.Signal, len(signalNames))
	for value, name := range signalNames {
		byName[name] = value
	}
	return byName
}()

func signalName(sig syscall.Signal) string {
	if name, ok := signalNames[sig]; ok {
		return name
	}
	return "SIG" + strconv.Itoa(int(sig))
}

func signalFromName(name string) (syscall.Signal, bool) {
	if name == "" {
		name = "SIGTERM"
	}
	sig, ok := signalsByName[name]
	return sig, ok
}
