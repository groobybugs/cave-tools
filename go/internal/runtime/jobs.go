package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
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
//
// TODO(jsre): compile through go/internal/jsre once W1a lands it, so `\s`
// keeps JS whitespace semantics.
var exitMarker = jsre.MustCompile(`\[cave-job exit=(-?\d+)(?: signal=(\w+))?\]\s*$`, "")

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
//
// ctx carries the call environment the worker inherits, like TS's ambient
// getEnv() in startDurableBackgroundCommand.
func StartJob(ctx context.Context, input StartJobInput) (*storage.JobRecord, error) {
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

	handle := StartDurableBackgroundCommand(ctx, input.Command, StartDurableBackgroundOptions{
		Cwd:        jobWorkdir(input.Workdir),
		Shell:      DefaultShell(ctx),
		WorkerPath: JobWorkerCommand,
		JobID:      jobID,
		LogPath:    logPath,
		ResultPath: resultPath,
	})

	record := storage.JobRecord{
		JobID:     jobID,
		Pid:       int64(handle.Pid),
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

	handle.OnClose(func(code *int, signal *string) {
		completion := recoverFromResult(jobID, resultPath)
		endedAt := time.Now().UnixMilli()
		exitCode := jobInt64From(code)
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
			EndedAt:  storage.Some(endedAt),
			ExitCode: jobOptionalInt64(exitCode),
			Signal:   jobOptionalString(completionSignal),
		})
		notifyCloseWaiter(jobID)
	})

	return &record, nil
}

// jobWorkdir maps the optional workdir onto the process helper's empty-string
// "inherit" convention.
func jobWorkdir(workdir *string) string {
	if workdir == nil {
		return ""
	}
	return *workdir
}

// jobOptionalInt64 maps a `number | null` onto updateJobState's three-state
// field: TS passes these explicitly, so nil is an explicit NULL.
func jobOptionalInt64(value *int64) storage.Optional[int64] {
	if value == nil {
		return storage.Null[int64]()
	}
	return storage.Some(*value)
}

// jobOptionalString is jobOptionalInt64 for signals.
func jobOptionalString(value *string) storage.Optional[string] {
	if value == nil {
		return storage.Null[string]()
	}
	return storage.Some(*value)
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
	if hasCloseWaiter(jobID) || (job.Pid > 0 && IsProcessAlive(int(job.Pid))) {
		return job, nil
	}

	if durable := recoverFromResult(jobID, ResultPathFor(jobID)); durable != nil {
		endedAt := durable.EndedAt
		_ = storage.UpdateJobState(jobID, storage.JobUpdate{
			State:    storage.JobExited,
			EndedAt:  storage.Some(endedAt),
			ExitCode: jobOptionalInt64(durable.ExitCode),
			Signal:   jobOptionalString(durable.Signal),
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
			EndedAt:  storage.Some(endedAt),
			ExitCode: storage.Some(recovered.exitCode),
			Signal:   jobOptionalString(recovered.signal),
		})
		job.State = storage.JobExited
		job.ExitCode = &recovered.exitCode
		job.Signal = recovered.signal
		job.EndedAt = &endedAt
		return job, nil
	}

	endedAt := time.Now().UnixMilli()
	// Only state/endedAt are written; a recorded exit code and signal survive.
	_ = storage.UpdateJobState(jobID, storage.JobUpdate{State: storage.JobLost, EndedAt: storage.Some(endedAt)})
	// Return the stored row so the result matches the db exactly.
	if stored, err := storage.GetJob(jobID); err == nil && stored != nil {
		return stored, nil
	}
	job.State = storage.JobLost
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
		EndedAt: storage.Some(endedAt),
		Signal:  storage.Some(requestedSignal),
	})
	// pid 0 means the worker never started (a Go-only state: TS fails the
	// insert before storing it). Signalling 0 would hit this process group.
	if job.Pid > 0 {
		KillProcessGroup(int(job.Pid), requestedSignal)
		go func(pid int64) {
			time.Sleep(forceKillGraceMS * time.Millisecond)
			if IsProcessAlive(int(pid)) {
				KillProcessGroup(int(pid), "SIGKILL")
			}
		}(job.Pid)
	}

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
	Text            string `json:"text"`
	RequestedCursor int64  `json:"requestedCursor"`
	StartCursor     int64  `json:"startCursor"`
	NextCursor      int64  `json:"nextCursor"`
	FileSize        int64  `json:"fileSize"`
	Truncated       bool   `json:"truncated"`
	CursorReset     bool   `json:"cursorReset"`
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
