package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultShell(t *testing.T) {
	ctx := w1bCtx(t, t.TempDir(), nil)
	shell := DefaultShell(ctx)
	if goruntime.GOOS == "windows" {
		if shell != "cmd.exe" && !strings.HasSuffix(strings.ToLower(shell), "cmd.exe") {
			t.Fatalf("DefaultShell = %q, want cmd.exe", shell)
		}
		return
	}
	if shell != "/bin/sh" {
		t.Fatalf("DefaultShell = %q, want /bin/sh", shell)
	}
}

func TestCompactOutput(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		// TS compactOutput branches.
		{"both", "out", "err", "out\n\nstderr:\nerr"},
		{"stderr only", "", "err", "stderr:\nerr"},
		{"stdout only", "out", "", "out"},
		{"neither", "", "", "(no output)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CompactOutput(test.stdout, test.stderr); got != test.want {
				t.Fatalf("CompactOutput(%q, %q) = %q, want %q", test.stdout, test.stderr, got, test.want)
			}
		})
	}
}

func TestCaptureNotice(t *testing.T) {
	tests := []struct {
		name           string
		stdout, stderr bool
		want           string
	}{
		{"both", true, true, "[stdout and stderr capture truncated at the in-memory safety limit]"},
		{"stdout", true, false, "[stdout capture truncated at the in-memory safety limit]"},
		{"stderr", false, true, "[stderr capture truncated at the in-memory safety limit]"},
		{"none", false, false, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CaptureNotice(test.stdout, test.stderr)
			if test.want == "" {
				if got != nil {
					t.Fatalf("CaptureNotice = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != test.want {
				t.Fatalf("CaptureNotice = %v, want %q", got, test.want)
			}
		})
	}
}

func TestTailUTF8Safe(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		maxLines int
		maxBytes int
		want     string
	}{
		{"under budget", "a\nb\n", 10, 100, "a\nb\n"},
		{"keeps last lines", "a\nb\nc\n", 2, 100, "c\n"},
		{"last lines without trailing LF", "a\nb\nc", 2, 100, "b\nc"},
		{"byte cap keeps tail", "abcdefghij", 2000, 4, "ghij"},
		{"split multibyte walked forward", "é", 2000, 1, ""},
		{"maxLines zero disables line cap", "a\nb\nc", 0, 100, "a\nb\nc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tailUTF8Safe(test.raw, test.maxLines, test.maxBytes); got != test.want {
				t.Fatalf("tailUTF8Safe(%q, %d, %d) = %q, want %q",
					test.raw, test.maxLines, test.maxBytes, got, test.want)
			}
		})
	}
}

func TestBoundedCapture(t *testing.T) {
	t.Run("ring drops the oldest chunks", func(t *testing.T) {
		capture := newBoundedCapture(5, 100)
		capture.push([]byte("aaaaa"))
		capture.push([]byte("bbbbb"))
		capture.push([]byte("ccccc"))
		text, truncated := capture.finalize()
		if !truncated {
			t.Fatal("expected truncated=true after dropping a chunk")
		}
		if text != "ccccc" {
			t.Fatalf("finalize = %q, want ccccc", text)
		}
	})

	t.Run("single oversized chunk keeps its tail", func(t *testing.T) {
		capture := newBoundedCapture(4, 100)
		capture.push([]byte("0123456789"))
		text, truncated := capture.finalize()
		if !truncated || text != "6789" {
			t.Fatalf("finalize = (%q, %v), want (6789, true)", text, truncated)
		}
	})

	t.Run("under budget is not truncated", func(t *testing.T) {
		capture := newBoundedCapture(4, 100)
		capture.push([]byte("hi"))
		text, truncated := capture.finalize()
		if truncated || text != "hi" {
			t.Fatalf("finalize = (%q, %v), want (hi, false)", text, truncated)
		}
	})
}

func TestDecodeUTF8Replace(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		// Node Buffer.toString("utf-8") behavior, verified against node v22.
		{"ascii", []byte("hello"), "hello"},
		{"two byte", []byte("caf\xc3\xa9"), "café"},
		{"three byte", []byte("€"), "€"},
		{"four byte", []byte("😀"), "😀"},
		{"truncated three byte is one replacement", []byte{0xe2, 0x82}, "\uFFFD"},
		{"stray continuations are one each", []byte{0x80, 0x80}, "\uFFFD\uFFFD"},
		{"invalid lead byte", []byte{0xff, 'a'}, "\uFFFD" + "a"},
		{"overlong lead byte", []byte{0xc0, 0x80}, "\uFFFD\uFFFD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decodeUTF8Replace(test.input); got != test.want {
				t.Fatalf("decodeUTF8Replace(% x) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	cwd := t.TempDir()
	ctx := w1bCtx(t, cwd, nil)

	run := func(t *testing.T, command string, options RunCommandOptions) RunCommandResult {
		t.Helper()
		if options.Cwd == "" {
			options.Cwd = cwd
		}
		result, err := RunCommand(ctx, command, options)
		if err != nil {
			t.Fatalf("RunCommand(%q): %v", command, err)
		}
		return result
	}

	t.Run("stdout", func(t *testing.T) {
		result := run(t, "echo hi", RunCommandOptions{Timeout: 5000})
		if result.Stdout != "hi\n" || result.Stderr != "" || result.Output != "hi\n" {
			t.Fatalf("result = %#v", result)
		}
		if result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != nil {
			t.Fatalf("exit = %v signal = %v", result.ExitCode, result.Signal)
		}
		if result.TimedOut || result.StdoutTruncated || result.StderrTruncated {
			t.Fatalf("unexpected flags: %#v", result)
		}
	})

	t.Run("stderr and combined output", func(t *testing.T) {
		result := run(t, "echo out; echo err >&2", RunCommandOptions{Timeout: 5000})
		if result.Stdout != "out\n" || result.Stderr != "err\n" {
			t.Fatalf("streams = %#v", result)
		}
		if result.Output != "out\n\n\nstderr:\nerr\n" {
			t.Fatalf("Output = %q", result.Output)
		}
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		result := run(t, "exit 7", RunCommandOptions{Timeout: 5000})
		if result.ExitCode == nil || *result.ExitCode != 7 {
			t.Fatalf("ExitCode = %v, want 7", result.ExitCode)
		}
		if result.Signal != nil || result.TimedOut {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("signal termination clears the exit code", func(t *testing.T) {
		result := run(t, "kill -9 $$", RunCommandOptions{Timeout: 5000})
		if result.Signal == nil || *result.Signal != "SIGKILL" {
			t.Fatalf("Signal = %v, want SIGKILL", result.Signal)
		}
		if result.ExitCode != nil {
			t.Fatalf("ExitCode = %d, want nil", *result.ExitCode)
		}
	})

	t.Run("missing command exits 127", func(t *testing.T) {
		result := run(t, "cave-not-a-real-command-xyz", RunCommandOptions{Timeout: 5000})
		if result.ExitCode == nil || *result.ExitCode != 127 {
			t.Fatalf("ExitCode = %v, want 127", result.ExitCode)
		}
		if !strings.Contains(result.Stderr, "not found") {
			t.Fatalf("Stderr = %q", result.Stderr)
		}
	})

	t.Run("timeout sends SIGTERM", func(t *testing.T) {
		start := time.Now()
		result := run(t, "sleep 5", RunCommandOptions{Timeout: 100})
		if !result.TimedOut {
			t.Fatal("TimedOut = false")
		}
		if result.Signal == nil || *result.Signal != "SIGTERM" {
			t.Fatalf("Signal = %v, want SIGTERM", result.Signal)
		}
		if result.ExitCode != nil {
			t.Fatalf("ExitCode = %d, want nil", *result.ExitCode)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("timeout took %s", elapsed)
		}
	})

	t.Run("timeout zero disables the kill timer", func(t *testing.T) {
		result := run(t, "sleep 0.05", RunCommandOptions{Timeout: 0})
		if result.TimedOut {
			t.Fatal("TimedOut = true with timeout disabled")
		}
		if result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("ExitCode = %v, want 0", result.ExitCode)
		}
	})

	t.Run("maxLines keeps the tail", func(t *testing.T) {
		result := run(t, "printf 'a\\nb\\nc\\n'", RunCommandOptions{Timeout: 5000, MaxLines: 2})
		if result.Stdout != "c\n" {
			t.Fatalf("Stdout = %q, want c\\n", result.Stdout)
		}
	})

	t.Run("maxBytes truncates and reports", func(t *testing.T) {
		result := run(t, "printf 'abcdefghij'", RunCommandOptions{Timeout: 5000, MaxBytes: 4})
		if result.Stdout != "ghij" || !result.StdoutTruncated {
			t.Fatalf("Stdout = %q truncated = %v", result.Stdout, result.StdoutTruncated)
		}
		if notice := CaptureNotice(result.StdoutTruncated, result.StderrTruncated); notice == nil {
			t.Fatal("CaptureNotice = nil")
		}
	})

	t.Run("cwd is honored", func(t *testing.T) {
		result := run(t, "pwd", RunCommandOptions{Timeout: 5000})
		want, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.Stdout) != want {
			t.Fatalf("pwd = %q, want %q", result.Stdout, want)
		}
	})

	t.Run("env comes from the call context", func(t *testing.T) {
		envCtx := w1bCtx(t, cwd, map[string]string{"CAVE_PROBE": "hello"})
		result, err := RunCommand(envCtx, `printf '%s' "$CAVE_PROBE"`, RunCommandOptions{Timeout: 5000, Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		if result.Stdout != "hello" {
			t.Fatalf("Stdout = %q, want hello", result.Stdout)
		}
	})

	t.Run("spawn failure is an error", func(t *testing.T) {
		_, err := RunCommand(ctx, "echo hi", RunCommandOptions{Timeout: 5000, Shell: "/nonexistent/sh"})
		if err == nil || !strings.Contains(err.Error(), "spawn /nonexistent/sh ENOENT") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestIsProcessAlive(t *testing.T) {
	if !IsProcessAlive(os.Getpid()) {
		t.Fatal("current process must be alive")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if IsProcessAlive(cmd.Process.Pid) {
		t.Fatalf("reaped pid %d reported alive", cmd.Process.Pid)
	}
}

func TestKillProcessGroup(t *testing.T) {
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
	if KillProcessGroup(handle.Pid, "SIGNOTREAL") {
		t.Fatal("unknown signal must report false")
	}
	closed := make(chan struct{})
	var gotSignal string
	handle.OnClose(func(code *int, signal *string) {
		if signal != nil {
			gotSignal = *signal
		}
		close(closed)
	})
	if !KillProcessGroup(handle.Pid, "SIGKILL") {
		t.Fatal("KillProcessGroup returned false")
	}
	w1bWait(t, closed, "background close")
	if gotSignal != "SIGKILL" {
		t.Fatalf("signal = %q, want SIGKILL", gotSignal)
	}
	if IsProcessAlive(handle.Pid) {
		t.Fatal("process still alive after SIGKILL")
	}
	if KillProcessGroup(1<<30, "SIGTERM") {
		t.Fatal("killing an unused pid must report false")
	}
}

func TestStartBackgroundCommand(t *testing.T) {
	cwd := t.TempDir()
	ctx := w1bCtx(t, cwd, nil)
	newLog := func(t *testing.T) (string, *os.File) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "job.log")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		return path, file
	}

	t.Run("stdout and stderr go to the log fd", func(t *testing.T) {
		path, file := newLog(t)
		defer file.Close()
		handle := StartBackgroundCommand(ctx, "printf 'logged\\n'; printf 'err\\n' >&2",
			StartBackgroundOptions{LogFd: int(file.Fd())})
		closed := make(chan struct{})
		var code *int
		handle.OnClose(func(exitCode *int, _ *string) {
			code = exitCode
			close(closed)
		})
		w1bWait(t, closed, "background close")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "logged\nerr\n" {
			t.Fatalf("log = %q", body)
		}
		if code == nil || *code != 0 {
			t.Fatalf("code = %v, want 0", code)
		}
	})

	t.Run("exit code is reported", func(t *testing.T) {
		_, file := newLog(t)
		defer file.Close()
		handle := StartBackgroundCommand(ctx, "exit 3", StartBackgroundOptions{LogFd: int(file.Fd())})
		closed := make(chan struct{})
		var code *int
		handle.OnClose(func(exitCode *int, _ *string) {
			code = exitCode
			close(closed)
		})
		w1bWait(t, closed, "background close")
		if code == nil || *code != 3 {
			t.Fatalf("code = %v, want 3", code)
		}
	})

	t.Run("late listener replays the exit", func(t *testing.T) {
		_, file := newLog(t)
		defer file.Close()
		handle := StartBackgroundCommand(ctx, "exit 0", StartBackgroundOptions{LogFd: int(file.Fd())})
		settled := make(chan struct{})
		handle.OnClose(func(*int, *string) { close(settled) })
		w1bWait(t, settled, "first close")
		late := make(chan struct{})
		handle.OnClose(func(code *int, _ *string) {
			if code == nil || *code != 0 {
				t.Errorf("replayed code = %v, want 0", code)
			}
			close(late)
		})
		w1bWait(t, late, "late replay")
	})

	t.Run("spawn failure fires a null close", func(t *testing.T) {
		_, file := newLog(t)
		defer file.Close()
		handle := StartBackgroundCommand(ctx, "echo hi",
			StartBackgroundOptions{LogFd: int(file.Fd()), Shell: "/nonexistent/sh"})
		closed := make(chan struct{})
		var code *int
		var signal *string
		handle.OnClose(func(exitCode *int, closeSignal *string) {
			code, signal = exitCode, closeSignal
			close(closed)
		})
		w1bWait(t, closed, "spawn failure close")
		if code != nil || signal != nil {
			t.Fatalf("code = %v signal = %v, want nil/nil", code, signal)
		}
	})
}

func TestStartDurableBackgroundCommand(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "worker.sh")
	// argv: $0 script, $1 jobId, $2 logPath, $3 resultPath, $4 shell, $5 command
	body := `#!/bin/sh
printf 'job=%s log=%s shell=%s command=%s' "$1" "$2" "$4" "$5" > "$3"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	previous := durableExecutable
	durableExecutable = func() (string, error) { return "/bin/sh", nil }
	defer func() { durableExecutable = previous }()

	resultPath := filepath.Join(dir, "result.txt")
	logPath := filepath.Join(dir, "job.log")
	handle := StartDurableBackgroundCommand(w1bCtx(t, dir, nil), "echo hi", StartDurableBackgroundOptions{
		Cwd:        dir,
		Shell:      "/bin/sh",
		WorkerPath: script,
		JobID:      "job-1",
		LogPath:    logPath,
		ResultPath: resultPath,
	})
	if handle.Pid <= 0 {
		t.Fatalf("Pid = %d", handle.Pid)
	}
	closed := make(chan struct{})
	var code *int
	handle.OnClose(func(exitCode *int, _ *string) {
		code = exitCode
		close(closed)
	})
	w1bWait(t, closed, "durable close")
	got, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "job=job-1 log=" + logPath + " shell=/bin/sh command=echo hi"
	if string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	if code == nil || *code != 0 {
		t.Fatalf("code = %v, want 0", code)
	}

	t.Run("executable lookup failure fires a null close", func(t *testing.T) {
		durableExecutable = func() (string, error) { return "", errors.New("no executable") }
		defer func() { durableExecutable = func() (string, error) { return "/bin/sh", nil } }()
		broken := StartDurableBackgroundCommand(context.Background(), "echo hi", StartDurableBackgroundOptions{})
		if broken.Pid != 0 {
			t.Fatalf("Pid = %d, want 0", broken.Pid)
		}
		settled := make(chan struct{})
		broken.OnClose(func(code *int, signal *string) {
			if code != nil || signal != nil {
				t.Errorf("code = %v signal = %v, want nil/nil", code, signal)
			}
			close(settled)
		})
		w1bWait(t, settled, "executable failure close")
	})
}

// TestRunCommandRejectsEmptyCommand pins Node's normalizeSpawnArguments
// behavior: an exactly-empty file argument fails before shell handling instead
// of running the shell and exiting 0.
func TestRunCommandRejectsEmptyCommand(t *testing.T) {
	ctx := w1bCtx(t, t.TempDir(), nil)

	_, err := RunCommand(ctx, "", RunCommandOptions{Timeout: 5000})
	if !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("err = %v, want ErrEmptyCommand", err)
	}
	const want = "The argument 'file' cannot be empty. Received ''"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}

	// Whitespace-only commands are not empty to Node and still reach the shell.
	result, err := RunCommand(ctx, " ", RunCommandOptions{Timeout: 5000})
	if err != nil {
		t.Fatalf("RunCommand(\" \"): %v", err)
	}
	if result.Stdout != "" || result.Stderr != "" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
}

// TestStartBackgroundCommandEmptyCommand checks the handle path reports the
// same spawn failure it reports for every other start error.
func TestStartBackgroundCommandEmptyCommand(t *testing.T) {
	handle := StartBackgroundCommand(w1bCtx(t, t.TempDir(), nil), "", StartBackgroundOptions{})
	if handle.Pid != 0 {
		t.Fatalf("pid = %d, want 0 (never started)", handle.Pid)
	}
	done := make(chan struct{}, 1)
	handle.OnClose(func(code *int, signal *string) {
		if code != nil || signal != nil {
			t.Errorf("close = %v/%v, want nil/nil", code, signal)
		}
		done <- struct{}{}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close listener never fired")
	}
}
