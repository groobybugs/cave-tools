//go:build windows

// Signal numbering for ShellExitCode (128+n), mirroring os.constants.signals
// on Windows.
package bash

// isWindows reports process.platform === "win32".
func isWindows() bool { return true }

// signalNumbers is os.constants.signals on win32. Node exposes the CRT's
// <csignal> macros plus the SIGHUP/SIGQUIT/SIGKILL/SIGWINCH that libuv's
// uv/win.h defines.
var signalNumbers = map[string]int{
	"SIGHUP":   1,
	"SIGINT":   2,
	"SIGQUIT":  3,
	"SIGILL":   4,
	"SIGFPE":   8,
	"SIGKILL":  9,
	"SIGSEGV":  11,
	"SIGTERM":  15,
	"SIGBREAK": 21,
	"SIGABRT":  22,
	"SIGWINCH": 28,
}

// signalNumber is os.constants.signals[name] ?? 0.
func signalNumber(name string) int {
	return signalNumbers[name]
}
