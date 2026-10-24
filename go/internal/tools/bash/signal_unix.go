//go:build !windows

// Signal numbering for ShellExitCode (128+n), mirroring os.constants.signals.
package bash

import "syscall"

// isWindows reports process.platform === "win32".
func isWindows() bool { return false }

// signalNumbers is os.constants.signals on Linux and macOS: Node reads the
// platform's signal macros, so the numbers match syscall's constants.
var signalNumbers = map[string]int{
	"SIGHUP":    int(syscall.SIGHUP),
	"SIGINT":    int(syscall.SIGINT),
	"SIGQUIT":   int(syscall.SIGQUIT),
	"SIGILL":    int(syscall.SIGILL),
	"SIGTRAP":   int(syscall.SIGTRAP),
	"SIGABRT":   int(syscall.SIGABRT),
	"SIGBUS":    int(syscall.SIGBUS),
	"SIGFPE":    int(syscall.SIGFPE),
	"SIGKILL":   int(syscall.SIGKILL),
	"SIGUSR1":   int(syscall.SIGUSR1),
	"SIGSEGV":   int(syscall.SIGSEGV),
	"SIGUSR2":   int(syscall.SIGUSR2),
	"SIGPIPE":   int(syscall.SIGPIPE),
	"SIGALRM":   int(syscall.SIGALRM),
	"SIGTERM":   int(syscall.SIGTERM),
	"SIGCHLD":   int(syscall.SIGCHLD),
	"SIGCONT":   int(syscall.SIGCONT),
	"SIGSTOP":   int(syscall.SIGSTOP),
	"SIGTSTP":   int(syscall.SIGTSTP),
	"SIGTTIN":   int(syscall.SIGTTIN),
	"SIGTTOU":   int(syscall.SIGTTOU),
	"SIGURG":    int(syscall.SIGURG),
	"SIGXCPU":   int(syscall.SIGXCPU),
	"SIGXFSZ":   int(syscall.SIGXFSZ),
	"SIGVTALRM": int(syscall.SIGVTALRM),
	"SIGPROF":   int(syscall.SIGPROF),
	"SIGWINCH":  int(syscall.SIGWINCH),
	"SIGIO":     int(syscall.SIGIO),
	"SIGSYS":    int(syscall.SIGSYS),
}

// signalNumber is os.constants.signals[name] ?? 0.
func signalNumber(name string) int {
	return signalNumbers[name]
}
