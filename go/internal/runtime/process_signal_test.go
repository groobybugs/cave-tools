//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Node resolves process.kill signal names with an exact os.constants.signals
// lookup (`sig ||= "SIGTERM"` only for a falsy name), so the Go table must
// round-trip and reject anything else.
func TestSignalNameTable(t *testing.T) {
	for value, name := range signalNames {
		if got := signalName(value); got != name {
			t.Errorf("signalName(%d) = %q, want %q", value, got, name)
		}
		sig, ok := signalFromName(name)
		if !ok || sig != value {
			t.Errorf("signalFromName(%q) = (%d, %v), want (%d, true)", name, sig, ok, value)
		}
	}
}

func TestSignalFromName(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  syscall.Signal
		ok    bool
	}{
		{"exact name", "SIGTERM", syscall.SIGTERM, true},
		{"exact kill", "SIGKILL", syscall.SIGKILL, true},
		{"empty defaults to SIGTERM", "", syscall.SIGTERM, true},
		{"lowercase is rejected", "sigterm", 0, false},
		{"bare name is rejected", "TERM", 0, false},
		{"unknown name", "SIGNOTREAL", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := signalFromName(test.value)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("signalFromName(%q) = (%d, %v), want (%d, %v)",
					test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

// An empty signal name reaches process.kill as SIGTERM (Node `sig ||= ...`).
func TestKillProcessGroupEmptySignal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	handle := StartBackgroundCommand(w1bCtx(t, t.TempDir(), nil), "sleep 5",
		StartBackgroundOptions{LogFd: int(logFile.Fd())})
	if handle.Pid <= 0 {
		t.Fatalf("Pid = %d", handle.Pid)
	}
	closed := make(chan struct{})
	var gotSignal string
	handle.OnClose(func(code *int, signal *string) {
		if signal != nil {
			gotSignal = *signal
		}
		close(closed)
	})
	if !KillProcessGroup(handle.Pid, "") {
		t.Fatal("KillProcessGroup with an empty signal must default to SIGTERM")
	}
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("background close did not fire")
	}
	if gotSignal != "SIGTERM" {
		t.Fatalf("signal = %q, want SIGTERM", gotSignal)
	}
}
