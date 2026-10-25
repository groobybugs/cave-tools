// Process execution, capture and signalling helpers.
//
// Port of src/runtime/process.ts.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// RunCommandOptions mirrors src/runtime/process.ts:RunCommandOptions.
// Optional TS fields keep their zero value as "use the default" (the TS
// defaults are 100KB / 2000 lines / 3000ms).
type RunCommandOptions struct {
	Cwd              string `json:"cwd,omitempty"`
	Timeout          int64  `json:"timeout"`
	Shell            string `json:"shell,omitempty"`
	MaxBytes         int64  `json:"maxBytes,omitempty"`
	MaxLines         int64  `json:"maxLines,omitempty"`
	ForceKillAfterMs int64  `json:"forceKillAfterMs,omitempty"`
}

// RunCommandResult mirrors src/runtime/process.ts:RunCommandResult.
// ExitCode and Signal are pointers because TS reports null when the process
// died from a signal (or was never reaped).
type RunCommandResult struct {
	Stdout          string  `json:"stdout"`
	Stderr          string  `json:"stderr"`
	Output          string  `json:"output"`
	ExitCode        *int    `json:"exitCode"`
	Signal          *string `json:"signal"`
	TimedOut        bool    `json:"timedOut"`
	StdoutTruncated bool    `json:"stdoutTruncated"`
	StderrTruncated bool    `json:"stderrTruncated"`
}

// port of src/runtime/process.ts:defaultShell
func DefaultShell(ctx context.Context) string {
	if goruntime.GOOS == "windows" {
		if comspec, ok := GetEnv(ctx)["COMSPEC"]; ok {
			return comspec
		}
		return "cmd.exe"
	}
	return "/bin/sh"
}

// port of src/runtime/process.ts:compactOutput
func CompactOutput(stdout, stderr string) string {
	if stdout != "" && stderr != "" {
		return stdout + "\n\nstderr:\n" + stderr
	}
	if stderr != "" {
		return "stderr:\n" + stderr
	}
	if stdout != "" {
		return stdout
	}
	return "(no output)"
}

// port of src/runtime/process.ts:captureNotice
func CaptureNotice(stdoutTruncated, stderrTruncated bool) *string {
	var notice string
	switch {
	case stdoutTruncated && stderrTruncated:
		notice = "[stdout and stderr capture truncated at the in-memory safety limit]"
	case stdoutTruncated:
		notice = "[stdout capture truncated at the in-memory safety limit]"
	case stderrTruncated:
		notice = "[stderr capture truncated at the in-memory safety limit]"
	default:
		return nil
	}
	return &notice
}

// port of src/runtime/process.ts:BoundedCapture
//
// Chunks are kept up to keep = maxBytes*2, the oldest ones are dropped over
// budget, and the final string is UTF-8-boundary safe via tailUTF8Safe.
type boundedCapture struct {
	maxBytes int
	maxLines int
	keep     int
	chunks   [][]byte
	used     int
	cut      bool
}

// port of src/runtime/process.ts:new BoundedCapture
func newBoundedCapture(maxBytes, maxLines int) *boundedCapture {
	return &boundedCapture{maxBytes: maxBytes, maxLines: maxLines, keep: maxBytes * 2}
}

// port of src/runtime/process.ts:BoundedCapture.push
// A single chunk larger than the keep budget keeps only its tail, so one huge
// write cannot blow memory.
func (c *boundedCapture) push(chunk []byte) {
	if len(chunk) > c.keep {
		chunk = chunk[len(chunk)-c.keep:]
		c.cut = true
	}
	c.chunks = append(c.chunks, append([]byte(nil), chunk...))
	c.used += len(chunk)
	for c.used > c.keep && len(c.chunks) > 0 {
		dropped := c.chunks[0]
		c.chunks = c.chunks[1:]
		c.used -= len(dropped)
		c.cut = true
	}
}

// port of src/runtime/process.ts:BoundedCapture.finalize
func (c *boundedCapture) finalize() (string, bool) {
	raw := make([]byte, 0, c.used)
	for _, chunk := range c.chunks {
		raw = append(raw, chunk...)
	}
	return tailUTF8Safe(DecodeUTF8Replace(raw), c.maxLines, c.maxBytes), c.cut
}

// Write makes boundedCapture an io.Writer for os/exec.
func (c *boundedCapture) Write(p []byte) (int, error) {
	c.push(p)
	return len(p), nil
}

// tailUTF8Safe keeps the last maxLines lines, capped at maxBytes, walking back
// to a char boundary when the byte slice would split a multibyte sequence.
// port of src/runtime/process.ts:tailUtf8Safe
func tailUTF8Safe(raw string, maxLines, maxBytes int) string {
	lines := raw
	if maxLines > 0 {
		all := strings.Split(raw, "\n")
		if len(all) > maxLines {
			lines = strings.Join(all[len(all)-maxLines:], "\n")
		}
	}
	if len(lines) <= maxBytes {
		return lines
	}
	end := len(lines)
	start := end - maxBytes
	if start < 0 {
		start = 0
	}
	for start < end && lines[start]&0xc0 == 0x80 {
		start++
	}
	return lines[start:end]
}

// DecodeUTF8Replace decodes bytes exactly like Node's Buffer.toString("utf-8")
// and a stdin stream with setEncoding("utf8"): invalid input becomes U+FFFD,
// one replacement per maximal invalid subpart (a truncated multibyte sequence
// is a single replacement, bad continuation bytes are one each).
// strings.ToValidUTF8 collapses runs instead.
func DecodeUTF8Replace(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		lead := b[i]
		if lead < 0x80 {
			sb.WriteByte(lead)
			i++
			continue
		}
		need := 0
		switch {
		case lead >= 0xc2 && lead <= 0xdf:
			need = 2
		case lead >= 0xe0 && lead <= 0xef:
			need = 3
		case lead >= 0xf0 && lead <= 0xf4:
			need = 4
		default:
			sb.WriteRune(0xfffd)
			i++
			continue
		}
		j := i + 1
		ok := true
		for k := 1; k < need; k++ {
			if j >= len(b) {
				ok = false
				break
			}
			next := b[j]
			if k == 1 {
				lo, hi := byte(0x80), byte(0xbf)
				switch lead {
				case 0xe0:
					lo = 0xa0
				case 0xed:
					hi = 0x9f
				case 0xf0:
					lo = 0x90
				case 0xf4:
					hi = 0x8f
				}
				if next < lo || next > hi {
					ok = false
					break
				}
			} else if next < 0x80 || next > 0xbf {
				ok = false
				break
			}
			j++
		}
		if !ok {
			sb.WriteRune(0xfffd)
			i = j
			continue
		}
		sb.WriteRune(decodeRuneBytes(b[i:j]))
		i = j
	}
	return sb.String()
}

// decodeRuneBytes decodes an already validated UTF-8 sequence.
func decodeRuneBytes(b []byte) rune {
	switch len(b) {
	case 1:
		return rune(b[0])
	case 2:
		return rune(b[0]&0x1f)<<6 | rune(b[1]&0x3f)
	case 3:
		return rune(b[0]&0x0f)<<12 | rune(b[1]&0x3f)<<6 | rune(b[2]&0x3f)
	default:
		return rune(b[0]&0x07)<<18 | rune(b[1]&0x3f)<<12 | rune(b[2]&0x3f)<<6 | rune(b[3]&0x3f)
	}
}

// utf16Length is JS string length (UTF-16 code units), not rune count.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xffff {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// truncateUTF16 mirrors JS String.prototype.slice(0, n): it counts UTF-16 code
// units and an unpaired surrogate decodes to U+FFFD when written as UTF-8.
func truncateUTF16(s string, n int) string {
	if utf16Length(s) <= n {
		return s
	}
	units := utf16.Encode([]rune(s))
	return string(utf16.Decode(units[:n]))
}

// StartBackgroundOptions mirrors src/runtime/process.ts:StartBackgroundOptions.
type StartBackgroundOptions struct {
	Cwd   string `json:"cwd,omitempty"`
	Shell string `json:"shell,omitempty"`
	// LogFd is an open fd the child's stdout and stderr are both redirected to.
	LogFd int `json:"logFd"`
}

// CloseListener mirrors src/runtime/process.ts:CloseListener.
type CloseListener func(code *int, signal *string)

type backgroundResult struct {
	code   *int
	signal *string
}

// BackgroundHandle mirrors src/runtime/process.ts:BackgroundHandle.
type BackgroundHandle struct {
	Pid int

	mu        sync.Mutex
	listeners []CloseListener
	result    *backgroundResult
}

// port of src/runtime/process.ts:BackgroundHandle.onClose
// It replays immediately (on another goroutine, like queueMicrotask) if the
// process already exited.
func (h *BackgroundHandle) OnClose(cb CloseListener) {
	h.mu.Lock()
	if h.result != nil {
		settled := *h.result
		h.mu.Unlock()
		go cb(settled.code, settled.signal)
		return
	}
	h.listeners = append(h.listeners, cb)
	h.mu.Unlock()
}

func (h *BackgroundHandle) fire(code *int, signal *string) {
	h.mu.Lock()
	if h.result != nil {
		h.mu.Unlock()
		return
	}
	h.result = &backgroundResult{code: code, signal: signal}
	listeners := h.listeners
	h.listeners = nil
	h.mu.Unlock()
	for _, cb := range listeners {
		cb(code, signal)
	}
}

// StartDurableBackgroundOptions mirrors
// src/runtime/process.ts:StartDurableBackgroundOptions.
type StartDurableBackgroundOptions struct {
	Cwd        string `json:"cwd,omitempty"`
	Shell      string `json:"shell"`
	WorkerPath string `json:"workerPath"`
	JobID      string `json:"jobId"`
	LogPath    string `json:"logPath"`
	ResultPath string `json:"resultPath"`
}

// durableExecutable is the Go equivalent of process.execPath: the durable
// worker is this same binary invoked with WorkerPath as its first argument,
// exactly as Node receives the worker script path as argv[1].
var durableExecutable = os.Executable

// port of src/runtime/process.ts:startDurableBackgroundCommand
func StartDurableBackgroundCommand(ctx context.Context, command string, options StartDurableBackgroundOptions) *BackgroundHandle {
	handle := &BackgroundHandle{}
	executable, err := durableExecutable()
	if err != nil {
		go handle.fire(nil, nil)
		return handle
	}
	cmd := exec.Command(executable,
		options.WorkerPath,
		options.JobID,
		options.LogPath,
		options.ResultPath,
		options.Shell,
		command,
	)
	// stdio: "ignore" leaves Stdout/Stderr nil, which os/exec sends to devnull.
	startBackgroundProcess(ctx, cmd, options.Cwd, handle)
	return handle
}

// port of src/runtime/process.ts:startBackgroundCommand
func StartBackgroundCommand(ctx context.Context, command string, options StartBackgroundOptions) *BackgroundHandle {
	handle := &BackgroundHandle{}
	// Shape differs from TS: src/runtime/process.ts:startBackgroundCommand lets
	// Node's spawn throw synchronously for an empty command, while this
	// signature has no error slot. Instead the handle fires (nil, nil), the same
	// way every other spawn failure is reported here. The difference is
	// unobservable in practice: there are no production callers.
	if command == "" {
		go handle.fire(nil, nil)
		return handle
	}
	shell := options.Shell
	if shell == "" {
		shell = DefaultShell(ctx)
	}
	cmd := shellCommand(shell, command)
	logFile := backgroundLogFile(options.LogFd)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	startBackgroundProcess(ctx, cmd, options.Cwd, handle)
	return handle
}

// startBackgroundProcess starts a detached process group whose completion
// fires the handle listeners, then leaves it running on its own goroutine.
func startBackgroundProcess(ctx context.Context, cmd *exec.Cmd, cwd string, handle *BackgroundHandle) {
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = envSlice(GetEnv(ctx))
	configureSpawn(cmd, true)
	if err := cmd.Start(); err != nil {
		go handle.fire(nil, nil)
		return
	}
	handle.Pid = cmd.Process.Pid
	go func() {
		_ = cmd.Wait()
		code, signal := waitResult(cmd)
		handle.fire(code, signal)
	}()
}

var backgroundLogFiles = struct {
	sync.Mutex
	files map[int]*os.File
}{files: map[int]*os.File{}}

// backgroundLogFile wraps the caller-owned fd without taking ownership. The
// wrapper is cached so its finalizer cannot close a fd the caller still owns
// (Node treats logFd as a bare number too).
func backgroundLogFile(fd int) *os.File {
	backgroundLogFiles.Lock()
	defer backgroundLogFiles.Unlock()
	if file, ok := backgroundLogFiles.files[fd]; ok {
		return file
	}
	file := os.NewFile(uintptr(fd), fmt.Sprintf("cave-tools-log-%d", fd))
	backgroundLogFiles.files[fd] = file
	return file
}

// port of src/runtime/process.ts:runCommand
func RunCommand(ctx context.Context, command string, options RunCommandOptions) (RunCommandResult, error) {
	// Node rejects an empty `file` before shell handling, so the shell never
	// runs (and never exits 0) for an exactly-empty command.
	if command == "" {
		return RunCommandResult{}, ErrEmptyCommand
	}
	shell := options.Shell
	if shell == "" {
		shell = DefaultShell(ctx)
	}
	maxBytes := int(options.MaxBytes)
	if maxBytes == 0 {
		maxBytes = 100 * 1024
	}
	maxLines := int(options.MaxLines)
	if maxLines == 0 {
		maxLines = 2000
	}
	forceKillAfterMs := options.ForceKillAfterMs
	if forceKillAfterMs == 0 {
		forceKillAfterMs = 3000
	}

	stdoutCap := newBoundedCapture(maxBytes, maxLines)
	stderrCap := newBoundedCapture(maxBytes, maxLines)

	cmd := shellCommand(shell, command)
	if options.Cwd != "" {
		cmd.Dir = options.Cwd
	}
	cmd.Env = envSlice(GetEnv(ctx))
	cmd.Stdout = stdoutCap
	cmd.Stderr = stderrCap
	configureSpawn(cmd, true)

	if err := cmd.Start(); err != nil {
		return RunCommandResult{}, spawnError(shell, err)
	}

	var timedOut atomicFlag
	var finished atomicFlag
	var timersMu sync.Mutex
	var timers []*time.Timer
	stopTimers := func() {
		timersMu.Lock()
		for _, timer := range timers {
			timer.Stop()
		}
		timers = nil
		timersMu.Unlock()
	}
	addTimer := func(timer *time.Timer) {
		timersMu.Lock()
		timers = append(timers, timer)
		timersMu.Unlock()
	}

	pid := cmd.Process.Pid
	if options.Timeout > 0 {
		addTimer(time.AfterFunc(time.Duration(options.Timeout)*time.Millisecond, func() {
			if finished.isSet() {
				return
			}
			timedOut.set()
			terminateProcess(pid, signalTerm)
			addTimer(time.AfterFunc(time.Duration(forceKillAfterMs)*time.Millisecond, func() {
				if finished.isSet() {
					return
				}
				terminateProcess(pid, signalKill)
			}))
		}))
	}

	waitErr := cmd.Wait()
	finished.set()
	stopTimers()
	if cmd.ProcessState == nil {
		return RunCommandResult{}, waitErr
	}

	exitCode, signal := waitResult(cmd)
	outText, outCut := stdoutCap.finalize()
	errText, errCut := stderrCap.finalize()
	return RunCommandResult{
		Stdout:          outText,
		Stderr:          errText,
		Output:          CompactOutput(outText, errText),
		ExitCode:        exitCode,
		Signal:          signal,
		TimedOut:        timedOut.isSet(),
		StdoutTruncated: outCut,
		StderrTruncated: errCut,
	}, nil
}

// atomicFlag is the Go sibling of the TS closure's mutable timedOut boolean.
type atomicFlag struct {
	mu sync.Mutex
	on bool
}

func (f *atomicFlag) set() {
	f.mu.Lock()
	f.on = true
	f.mu.Unlock()
}

func (f *atomicFlag) isSet() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on
}

// processSignal is the platform-neutral subset the kill timeout sends:
// SIGTERM first, then SIGKILL after the grace period.
type processSignal int

const (
	signalTerm processSignal = iota
	signalKill
)

// ErrEmptyCommand is Node's normalizeSpawnArguments rejection of an empty
// `file` argument. Node validates the file before it applies `shell` options,
// so an exactly-empty command never reaches the shell and fails to launch;
// whitespace-only commands still run.
// port of Node's child_process.normalizeSpawnArguments (ERR_INVALID_ARG_VALUE)
var ErrEmptyCommand = errors.New("The argument 'file' cannot be empty. Received ''")

func spawnError(command string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("spawn %s ENOENT", command)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("spawn %s EACCES", command)
	}
	return err
}

func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values
}
