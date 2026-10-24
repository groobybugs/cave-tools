package runtime

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/storage"
)

const (
	// port of src/runtime/jobs.ts:FORCE_KILL_GRACE_MS
	forceKillGraceMS = 3000
	// port of src/runtime/jobs.ts:DEFAULT_MAX_AGE_MS
	defaultMaxAgeMS = 24 * 60 * 60 * 1000
	// port of src/runtime/jobs.ts:ADOPTED_JOB_POLL_MS
	adoptedJobPollMS = 100
)

// JobWorkerCommand is the argv[1] this binary dispatches to RunJobWorker.
// TS runs a separate compiled job-worker.js (JOB_WORKER_PATH); Go re-executes
// itself so the worker always ships with the binary.
const JobWorkerCommand = "__job-worker"

// port of src/runtime/jobs.ts:EXIT_MARKER
var exitMarker = regexp.MustCompile(`\[cave-job exit=(-?\d+)(?: signal=(\w+))?\]\s*$`)

// closeWaiters are the close-waiters for jobs started by this server process,
// keyed by jobId.
var (
	closeWaitersMu sync.Mutex
	closeWaiters   = map[string]chan struct{}{}
)

// port of src/runtime/jobs.ts:newJobId
func newJobID() (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return "j" + strconv.FormatInt(time.Now().UnixMilli(), 36) + hex.EncodeToString(suffix), nil
}

// port of src/runtime/jobs.ts:logPathFor
func LogPathFor(jobID string) string {
	return filepath.Join(storage.JobsDir(), jobID+".log")
}

// port of src/runtime/jobs.ts:resultPathFor
func ResultPathFor(jobID string) string {
	return filepath.Join(storage.JobsDir(), jobID+".result.json")
}

// StartJobInput is startJob's argument object.
// port of src/runtime/jobs.ts:startJob (input parameter)
type StartJobInput struct {
	Command   string
	SessionID string
	// Workdir is the optional cwd; nil means the worker inherits this process's.
	Workdir *string
}

// StartJob launches a detached worker for command and records the job row.
// port of src/runtime/jobs.ts:startJob
func StartJob(input StartJobInput) (*storage.JobRecord, error) {
	if err := os.MkdirAll(storage.JobsDir(), 0o700); err != nil {
		return nil, err
	}
	jobID, err := newJobID()
	if err != nil {
		return nil, err
	}
	logPath := LogPathFor(jobID)
	resultPath := ResultPathFor(jobID)
	startedAt := time.Now().UnixMilli()
	header := "# cave-job " + jobID + "\n# command: " + input.Command + "\n# started: " +
		time.UnixMilli(startedAt).UTC().Format("2006-01-02T15:04:05.000Z") + "\n\n"
	if err := os.WriteFile(logPath, []byte(header), 0o600); err != nil {
		return nil, err
	}

	handle, err := startDurableBackgroundCommand(input.Command, durableStartOptions{
		cwd:        input.Workdir,
		shell:      defaultShell(),
		jobID:      jobID,
		logPath:    logPath,
		resultPath: resultPath,
	})
	if err != nil {
		return nil, err
	}

	record := storage.JobRecord{
		JobID:     jobID,
		Pid:       handle.pid,
		SessionID: input.SessionID,
		Command:   input.Command,
		Workdir:   input.Workdir,
		StartedAt: startedAt,
		State:     storage.JobRunning,
		LogPath:   logPath,
	}
	if err := storage.InsertJob(record); err != nil {
		return nil, err
	}

	registerCloseWaiter(jobID)

	handle.onClose(func(code *int64, signal *string) {
		completion := recoverFromResult(jobID, resultPath)
		endedAt := time.Now().UnixMilli()
		exitCode := code
		completionSignal := signal
		if completion != nil {
			endedAt = completion.EndedAt
			exitCode = completion.ExitCode
			completionSignal = completion.Signal
		}
		// stopJob() marks the row "killed" before signalling; keep that outcome.
		state := storage.JobExited
		if current, err := storage.GetJob(jobID); err == nil && current != nil && current.State == storage.JobKilled {
			state = storage.JobKilled
		}
		_ = storage.UpdateJobState(jobID, storage.JobUpdate{
			State:    state,
			EndedAt:  &endedAt,
			ExitCode: exitCode,
			Signal:   completionSignal,
		})
		notifyCloseWaiter(jobID)
	})

	return &record, nil
}

// durableJobResult is the worker's structured completion sidecar.
// port of src/runtime/jobs.ts:DurableJobResult
type durableJobResult struct {
	Version  int     `json:"version"`
	JobID    string  `json:"jobId"`
	EndedAt  int64   `json:"endedAt"`
	ExitCode *int64  `json:"exitCode"`
	Signal   *string `json:"signal"`
}

// port of src/runtime/jobs.ts:recoverFromResult
func recoverFromResult(jobID, resultPath string) *durableJobResult {
	data, err := os.ReadFile(resultPath)
	if err != nil {
		return nil
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	if isJSONNull(parsed["version"]) {
		return nil
	}
	version, ok := jsonNumberField(parsed, "version")
	if !ok || version != 1 {
		return nil
	}
	var parsedJobID string
	if raw, ok := parsed["jobId"]; !ok || json.Unmarshal(raw, &parsedJobID) != nil || parsedJobID != jobID {
		return nil
	}
	endedAt, ok := jsonNumberField(parsed, "endedAt")
	if !ok {
		return nil
	}
	exitRaw, ok := parsed["exitCode"]
	if !ok {
		return nil
	}
	var exitCode *int64
	if !isJSONNull(exitRaw) {
		value, ok := jsonNumberField(parsed, "exitCode")
		if !ok {
			return nil
		}
		code := int64(value)
		exitCode = &code
	}
	signalRaw, ok := parsed["signal"]
	if !ok {
		return nil
	}
	var signal *string
	if !isJSONNull(signalRaw) {
		var value string
		if json.Unmarshal(signalRaw, &value) != nil {
			return nil
		}
		signal = &value
	}
	return &durableJobResult{
		Version:  1,
		JobID:    jobID,
		EndedAt:  int64(endedAt),
		ExitCode: exitCode,
		Signal:   signal,
	}
}

// isJSONNull reports whether raw is the JSON null literal (or absent).
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

// jsonNumberField reads a finite JSON number field; null and non-numbers fail.
func jsonNumberField(parsed map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := parsed[key]
	if !ok || isJSONNull(raw) {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// recoveredMarker is the legacy exit marker's payload.
type recoveredMarker struct {
	exitCode int64
	signal   *string
}

// recoverFromMarker reads the legacy exit marker; retained for jobs launched by
// older versions.
// port of src/runtime/jobs.ts:recoverFromMarker
func recoverFromMarker(logPath string) *recoveredMarker {
	info, err := os.Stat(logPath)
	if err != nil {
		return nil
	}
	size := info.Size()
	start := size - 256
	if start < 0 {
		start = 0
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 256)
	read, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	match := exitMarker.FindStringSubmatch(string(buf[:read]))
	if match == nil {
		return nil
	}
	exitCode, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return nil
	}
	var signal *string
	if match[2] != "" {
		value := match[2]
		signal = &value
	}
	return &recoveredMarker{exitCode: exitCode, signal: signal}
}

// GetJobInfo returns the freshest view of a job. Rows still marked "running"
// from a previous server process are reconciled against pid liveness and the
// durable result or the log's exit marker, and the db is updated as a side
// effect.
// port of src/runtime/jobs.ts:getJobInfo
func GetJobInfo(jobID string) (*storage.JobRecord, error) {
	job, err := storage.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	if job == nil || job.State != storage.JobRunning {
		return job, nil
	}
	if hasCloseWaiter(jobID) || isProcessAlive(job.Pid) {
		return job, nil
	}

	if durable := recoverFromResult(jobID, ResultPathFor(jobID)); durable != nil {
		endedAt := durable.EndedAt
		_ = storage.UpdateJobState(jobID, storage.JobUpdate{
			State:    storage.JobExited,
			EndedAt:  &endedAt,
			ExitCode: durable.ExitCode,
			Signal:   durable.Signal,
		})
		job.State = storage.JobExited
		job.ExitCode = durable.ExitCode
		job.Signal = durable.Signal
		job.EndedAt = &endedAt
		return job, nil
	}

	if recovered := recoverFromMarker(job.LogPath); recovered != nil {
		endedAt := time.Now().UnixMilli()
		_ = storage.UpdateJobState(jobID, storage.JobUpdate{
			State:    storage.JobExited,
			EndedAt:  &endedAt,
			ExitCode: &recovered.exitCode,
			Signal:   recovered.signal,
		})
		job.State = storage.JobExited
		job.ExitCode = &recovered.exitCode
		job.Signal = recovered.signal
		job.EndedAt = &endedAt
		return job, nil
	}

	endedAt := time.Now().UnixMilli()
	// TS passes only state/endedAt here, so exit_code and signal become NULL.
	_ = storage.UpdateJobState(jobID, storage.JobUpdate{State: storage.JobLost, EndedAt: &endedAt})
	job.State = storage.JobLost
	job.EndedAt = &endedAt
	return job, nil
}

// ListSessionJobs returns a session's jobs newest first, reconciled.
// port of src/runtime/jobs.ts:listSessionJobs
func ListSessionJobs(sessionID string) ([]storage.JobRecord, error) {
	jobs, err := storage.ListJobs(&sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]storage.JobRecord, 0, len(jobs))
	for _, job := range jobs {
		info, err := GetJobInfo(job.JobID)
		if err != nil {
			return nil, err
		}
		if info != nil {
			out = append(out, *info)
		} else {
			out = append(out, job)
		}
	}
	return out, nil
}

// StopJob marks the job killed, signals its process group and schedules a
// SIGKILL after forceKillGraceMS. signal defaults to SIGTERM like TS's default
// parameter.
// port of src/runtime/jobs.ts:stopJob
func StopJob(jobID string, signal ...string) (*storage.JobRecord, error) {
	requestedSignal := "SIGTERM"
	if len(signal) > 0 {
		requestedSignal = signal[0]
	}
	job, err := GetJobInfo(jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}
	if job.State != storage.JobRunning {
		return job, nil
	}

	endedAt := time.Now().UnixMilli()
	_ = storage.UpdateJobState(jobID, storage.JobUpdate{
		State:   storage.JobKilled,
		EndedAt: &endedAt,
		Signal:  &requestedSignal,
	})
	killProcessGroup(job.Pid, requestedSignal)
	go func(pid int64) {
		time.Sleep(forceKillGraceMS * time.Millisecond)
		if isProcessAlive(pid) {
			killProcessGroup(pid, "SIGKILL")
		}
	}(job.Pid)

	returnedAt := time.Now().UnixMilli()
	result := *job
	result.State = storage.JobKilled
	result.Signal = &requestedSignal
	result.EndedAt = &returnedAt
	return &result, nil
}

// waitWithTimeout waits for waiter up to waitMs; waitMs <= 0 returns at once.
// port of src/runtime/jobs.ts:waitWithTimeout
func waitWithTimeout(waiter <-chan struct{}, waitMS int64) {
	if waitMS <= 0 {
		return
	}
	timer := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-waiter:
	case <-timer.C:
	}
}

// WaitForJob waits up to waitMS for the job to finish. Jobs started by this
// server resolve the moment the close event fires; jobs adopted after a
// restart fall back to polling followed by a liveness re-check.
// port of src/runtime/jobs.ts:waitForJob
func WaitForJob(jobID string, waitMS int64) (*storage.JobRecord, error) {
	if waiter := closeWaiterFor(jobID); waiter != nil {
		waitWithTimeout(waiter, waitMS)
		return GetJobInfo(jobID)
	}

	deadline := time.Now().UnixMilli() + max(0, waitMS)
	job, err := GetJobInfo(jobID)
	if err != nil {
		return nil, err
	}
	for job != nil && job.State == storage.JobRunning && time.Now().UnixMilli() < deadline {
		remaining := deadline - time.Now().UnixMilli()
		sleep := remaining
		if sleep > adoptedJobPollMS {
			sleep = adoptedJobPollMS
		}
		sleepMS(sleep)
		job, err = GetJobInfo(jobID)
		if err != nil {
			return nil, err
		}
	}
	return job, nil
}

// sleepMS waits ms milliseconds.
// port of src/runtime/jobs.ts:sleep
func sleepMS(ms int64) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// registerCloseWaiter installs the close-waiter for a job started here.
// port of src/runtime/jobs.ts (closeWaiters.set in startJob)
func registerCloseWaiter(jobID string) chan struct{} {
	waiter := make(chan struct{})
	closeWaitersMu.Lock()
	closeWaiters[jobID] = waiter
	closeWaitersMu.Unlock()
	return waiter
}

// notifyCloseWaiter releases waiters and forgets the job.
// port of src/runtime/jobs.ts (notify + closeWaiters.delete in startJob)
func notifyCloseWaiter(jobID string) {
	closeWaitersMu.Lock()
	waiter, ok := closeWaiters[jobID]
	delete(closeWaiters, jobID)
	closeWaitersMu.Unlock()
	if ok {
		close(waiter)
	}
}

// closeWaiterFor returns the waiter for jobID, or nil.
// port of src/runtime/jobs.ts (closeWaiters.get in waitForJob)
func closeWaiterFor(jobID string) chan struct{} {
	closeWaitersMu.Lock()
	defer closeWaitersMu.Unlock()
	return closeWaiters[jobID]
}

// hasCloseWaiter reports whether this process owns a waiter for jobID.
// port of src/runtime/jobs.ts (closeWaiters.has in getJobInfo)
func hasCloseWaiter(jobID string) bool {
	closeWaitersMu.Lock()
	defer closeWaitersMu.Unlock()
	_, ok := closeWaiters[jobID]
	return ok
}

// FileByteChunk is one bounded, UTF-8-boundary-safe byte range of a file.
// port of src/runtime/jobs.ts:FileByteChunk
type FileByteChunk struct {
	Text            string
	RequestedCursor int64
	StartCursor     int64
	NextCursor      int64
	FileSize        int64
	Truncated       bool
	CursorReset     bool
}

// port of src/runtime/jobs.ts:utf8SequenceLength
func utf8SequenceLength(firstByte byte) int {
	if firstByte&0x80 == 0 {
		return 1
	}
	if firstByte&0xe0 == 0xc0 {
		return 2
	}
	if firstByte&0xf0 == 0xe0 {
		return 3
	}
	if firstByte&0xf8 == 0xf0 {
		return 4
	}
	return 1
}

// port of src/runtime/jobs.ts:readFileChunkAtSize
func readFileChunkAtSize(path string, requestedCursor, maxBytes, fileSize int64) (FileByteChunk, error) {
	cursorReset := requestedCursor > fileSize
	cursor := requestedCursor
	if cursorReset || cursor < 0 {
		cursor = 0
	}
	length := maxBytes
	if length < 0 {
		length = 0
	}
	if length > fileSize-cursor {
		length = fileSize - cursor
	}
	if length == 0 {
		return FileByteChunk{
			Text:            "",
			RequestedCursor: requestedCursor,
			StartCursor:     cursor,
			NextCursor:      cursor,
			FileSize:        fileSize,
			Truncated:       cursor < fileSize,
			CursorReset:     cursorReset,
		}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return FileByteChunk{}, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, length)
	bytesRead, err := f.ReadAt(buf, cursor)
	if err != nil && !errors.Is(err, io.EOF) {
		return FileByteChunk{}, err
	}
	buf = buf[:bytesRead]

	begin := 0
	for begin < bytesRead && buf[begin]&0xc0 == 0x80 {
		begin++
	}

	end := bytesRead
	if end > begin {
		lead := end - 1
		for lead >= begin && buf[lead]&0xc0 == 0x80 {
			lead--
		}
		if lead >= begin && end-lead < utf8SequenceLength(buf[lead]) {
			end = lead
		}
	}

	startCursor := cursor + int64(begin)
	nextCursor := cursor + int64(end)
	return FileByteChunk{
		Text:            string(buf[begin:end]),
		RequestedCursor: requestedCursor,
		StartCursor:     startCursor,
		NextCursor:      nextCursor,
		FileSize:        fileSize,
		Truncated:       nextCursor < fileSize,
		CursorReset:     cursorReset,
	}, nil
}

// ReadFileChunk reads a bounded, UTF-8-boundary-safe byte range starting at
// cursor. A missing or unreadable file yields an empty chunk.
// port of src/runtime/jobs.ts:readFileChunk
func ReadFileChunk(path string, cursor, maxBytes int64) FileByteChunk {
	info, err := os.Stat(path)
	if err != nil {
		return FileByteChunk{
			Text:            "",
			RequestedCursor: cursor,
			StartCursor:     0,
			NextCursor:      0,
			FileSize:        0,
			Truncated:       false,
			CursorReset:     cursor > 0,
		}
	}
	chunk, err := readFileChunkAtSize(path, cursor, maxBytes, info.Size())
	if err != nil {
		return FileByteChunk{
			Text:            "",
			RequestedCursor: cursor,
			StartCursor:     0,
			NextCursor:      0,
			FileSize:        0,
			Truncated:       false,
			CursorReset:     cursor > 0,
		}
	}
	return chunk
}

// TailFileBytes reads at most the last maxBytes of a file as UTF-8.
// port of src/runtime/jobs.ts:tailFileBytes
func TailFileBytes(path string, maxBytes int64) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	chunk, err := readFileChunkAtSize(path, start, maxBytes, info.Size())
	if err != nil {
		return ""
	}
	return chunk.Text
}

// FileByteSize returns a file's size, or 0 when it is unreadable.
// port of src/runtime/jobs.ts:fileByteSize
func FileByteSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// PruneJobs deletes job rows and logs older than maxAgeMS (default 24h).
// Running jobs are never pruned; orphan log files with no row are removed by
// mtime.
// port of src/runtime/jobs.ts:pruneJobs
func PruneJobs(maxAgeMS ...int64) (int, error) {
	maxAge := int64(defaultMaxAgeMS)
	if len(maxAgeMS) > 0 {
		maxAge = maxAgeMS[0]
	}
	cutoff := time.Now().UnixMilli() - maxAge
	removed := 0
	ended, err := storage.ListJobsEndedBefore(cutoff)
	if err != nil {
		return 0, err
	}
	for _, job := range ended {
		if _, err := os.Stat(job.LogPath); err == nil {
			_ = os.Remove(job.LogPath)
		}
		resultPath := ResultPathFor(job.JobID)
		if _, err := os.Stat(resultPath); err == nil {
			_ = os.Remove(resultPath)
		}
		if err := storage.DeleteJob(job.JobID); err != nil {
			return removed, err
		}
		removed++
	}

	referenced := map[string]bool{}
	jobs, err := storage.ListJobs(nil)
	if err != nil {
		return removed, err
	}
	for _, job := range jobs {
		referenced[job.LogPath] = true
		referenced[ResultPathFor(job.JobID)] = true
	}
	entries, err := os.ReadDir(storage.JobsDir())
	if err != nil {
		return removed, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".result.json") {
			continue
		}
		path := filepath.Join(storage.JobsDir(), name)
		if referenced[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().UnixMilli() < cutoff {
			_ = os.Remove(path)
		}
	}
	return removed, nil
}

// ── Process helpers ────────────────────────────────────────────────────────
// W1b owns runtime/process.go. Until it lands these are the minimal unexported
// equivalents of the four src/runtime/process.ts helpers jobs.ts uses. POSIX
// only (the port targets Linux); W1b's package will replace them.

// defaultShell mirrors process.ts:defaultShell for POSIX platforms.
// port of src/runtime/process.ts:defaultShell
func defaultShell() string {
	return "/bin/sh"
}

// isProcessAlive mirrors process.ts:isProcessAlive.
// port of src/runtime/process.ts:isProcessAlive
func isProcessAlive(pid int64) bool {
	return syscall.Kill(int(pid), 0) == nil
}

// killProcessGroup mirrors process.ts:killProcessGroup (negative pid).
// port of src/runtime/process.ts:killProcessGroup
func killProcessGroup(pid int64, signal string) bool {
	sig, ok := nodeSignalToSyscall(signal)
	if !ok {
		return false
	}
	return syscall.Kill(-int(pid), sig) == nil
}

// durableStartOptions is startDurableBackgroundCommand's option object.
// port of src/runtime/process.ts:StartDurableBackgroundOptions
type durableStartOptions struct {
	cwd        *string
	shell      string
	jobID      string
	logPath    string
	resultPath string
}

// closeListener receives the worker's exit code (nil when signalled) and the
// Node-style signal name.
// port of src/runtime/process.ts:CloseListener
type closeListener func(code *int64, signal *string)

// backgroundHandle is the started worker.
// port of src/runtime/process.ts:BackgroundHandle
type backgroundHandle struct {
	pid     int64
	onClose func(cb closeListener)
}

// startDurableBackgroundCommand starts a detached worker that owns the command
// and writes its structured completion result before exiting.
// port of src/runtime/process.ts:startDurableBackgroundCommand
func startDurableBackgroundCommand(command string, options durableStartOptions) (*backgroundHandle, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{
		JobWorkerCommand,
		options.jobID,
		options.logPath,
		options.resultPath,
		options.shell,
		command,
	}
	cmd := exec.Command(executable, args...)
	if options.cwd != nil {
		cmd.Dir = *options.cwd
	}
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Node's `detached: true` on POSIX: new session/process group, so
	// killProcessGroup can signal the worker and the shell it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var (
		mu        sync.Mutex
		settled   bool
		finalCode *int64
		finalSig  *string
		listeners []closeListener
	)
	fire := func(code *int64, signal *string) {
		mu.Lock()
		if settled {
			mu.Unlock()
			return
		}
		settled = true
		finalCode = code
		finalSig = signal
		pending := listeners
		listeners = nil
		mu.Unlock()
		for _, cb := range pending {
			cb(code, signal)
		}
	}

	go func() {
		err := cmd.Wait()
		if err == nil {
			code := int64(0)
			fire(&code, nil)
			return
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			fire(nil, nil)
			return
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			code := int64(-1)
			fire(&code, nil)
			return
		}
		if status.Signaled() {
			var signal *string
			if name := nodeSignalName(status.Signal()); name != "" {
				signal = &name
			}
			fire(nil, signal)
			return
		}
		code := int64(status.ExitStatus())
		fire(&code, nil)
	}()

	return &backgroundHandle{
		pid: int64(cmd.Process.Pid),
		onClose: func(cb closeListener) {
			mu.Lock()
			if settled {
				code, signal := finalCode, finalSig
				mu.Unlock()
				cb(code, signal)
				return
			}
			listeners = append(listeners, cb)
			mu.Unlock()
		},
	}, nil
}

// nodeSignalToSyscall maps a Node signal name to its POSIX signal.
func nodeSignalToSyscall(name string) (syscall.Signal, bool) {
	switch name {
	case "SIGHUP":
		return syscall.SIGHUP, true
	case "SIGINT":
		return syscall.SIGINT, true
	case "SIGQUIT":
		return syscall.SIGQUIT, true
	case "SIGILL":
		return syscall.SIGILL, true
	case "SIGTRAP":
		return syscall.SIGTRAP, true
	case "SIGABRT":
		return syscall.SIGABRT, true
	case "SIGBUS":
		return syscall.SIGBUS, true
	case "SIGFPE":
		return syscall.SIGFPE, true
	case "SIGKILL":
		return syscall.SIGKILL, true
	case "SIGUSR1":
		return syscall.SIGUSR1, true
	case "SIGSEGV":
		return syscall.SIGSEGV, true
	case "SIGUSR2":
		return syscall.SIGUSR2, true
	case "SIGPIPE":
		return syscall.SIGPIPE, true
	case "SIGALRM":
		return syscall.SIGALRM, true
	case "SIGTERM":
		return syscall.SIGTERM, true
	case "SIGCHLD":
		return syscall.SIGCHLD, true
	case "SIGCONT":
		return syscall.SIGCONT, true
	case "SIGSTOP":
		return syscall.SIGSTOP, true
	case "SIGTSTP":
		return syscall.SIGTSTP, true
	case "SIGTTIN":
		return syscall.SIGTTIN, true
	case "SIGTTOU":
		return syscall.SIGTTOU, true
	case "SIGURG":
		return syscall.SIGURG, true
	case "SIGXCPU":
		return syscall.SIGXCPU, true
	case "SIGXFSZ":
		return syscall.SIGXFSZ, true
	case "SIGVTALRM":
		return syscall.SIGVTALRM, true
	case "SIGPROF":
		return syscall.SIGPROF, true
	case "SIGWINCH":
		return syscall.SIGWINCH, true
	case "SIGIO":
		return syscall.SIGIO, true
	case "SIGPWR":
		return syscall.SIGPWR, true
	case "SIGSYS":
		return syscall.SIGSYS, true
	}
	return 0, false
}

// nodeSignalName maps a POSIX signal to the name Node reports in close events.
func nodeSignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGTRAP:
		return "SIGTRAP"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGCHLD:
		return "SIGCHLD"
	case syscall.SIGCONT:
		return "SIGCONT"
	case syscall.SIGSTOP:
		return "SIGSTOP"
	case syscall.SIGTSTP:
		return "SIGTSTP"
	case syscall.SIGTTIN:
		return "SIGTTIN"
	case syscall.SIGTTOU:
		return "SIGTTOU"
	case syscall.SIGURG:
		return "SIGURG"
	case syscall.SIGXCPU:
		return "SIGXCPU"
	case syscall.SIGXFSZ:
		return "SIGXFSZ"
	case syscall.SIGVTALRM:
		return "SIGVTALRM"
	case syscall.SIGPROF:
		return "SIGPROF"
	case syscall.SIGWINCH:
		return "SIGWINCH"
	case syscall.SIGIO:
		return "SIGIO"
	case syscall.SIGPWR:
		return "SIGPWR"
	case syscall.SIGSYS:
		return "SIGSYS"
	}
	return ""
}
