package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// jobWorker owns one durable background command. It is the Go form of the
// src/runtime/job-worker.ts script: TS spawns node with that script, Go
// re-executes this binary with JobWorkerCommand and dispatches here.
// port of src/runtime/job-worker.ts:main
type jobWorker struct {
	jobID      string
	logPath    string
	resultPath string
	finished   bool
	exitCode   int
}

// RunJobWorker runs the durable job worker and returns the process exit code.
// args is [jobId, logPath, resultPath, shell, command] — exactly TS's
// process.argv.slice(2).
//
// The Go CLI must dispatch `cave-tools-go __job-worker ...` here. Keeping the
// worker in the binary (instead of a script path) is the one necessary
// deviation: there is no separate JS file to hand to node.
// port of src/runtime/job-worker.ts (script entry point)
func RunJobWorker(args []string) int {
	if len(args) < 5 || args[0] == "" || args[1] == "" || args[2] == "" || args[3] == "" {
		_, _ = fmt.Fprint(os.Stderr, "cave job worker: invalid arguments\n")
		return 1
	}
	worker := &jobWorker{jobID: args[0], logPath: args[1], resultPath: args[2]}
	return worker.run(args[3], args[4])
}

// spawnFailed is the exit code of a worker whose shell never started: 127,
// like the shell's "command not found", so a job that never started is never
// reported as success.
// port of src/runtime/job-worker.ts:SPAWN_FAILED
const spawnFailed = 127

// run spawns the shell command with stdout and stderr appended to the log and
// records completion.
func (w *jobWorker) run(shell, command string) int {
	logFile, err := os.OpenFile(w.logPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		return w.finish(jobInt64Ptr(spawnFailed), nil, err)
	}
	// TS opens the log first, then spawn() throws on an empty command: the
	// worker-error line still lands in the log and the job never reports 0.
	if command == "" {
		_ = logFile.Close()
		return w.finish(jobInt64Ptr(spawnFailed), nil, ErrEmptyCommand)
	}
	cmd := shellCommand(shell, command)
	// TS passes windowsHide: process.platform === "win32"; no `detached`.
	configureSpawn(cmd, false)
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return w.finish(jobInt64Ptr(spawnFailed), nil, spawnError(shell, err))
	}
	_ = logFile.Close()

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return w.finish(jobInt64Ptr(spawnFailed), nil, err)
		}
	}
	code, signal := waitResult(cmd)
	return w.finish(jobInt64From(code), signal, nil)
}

// finish writes the worker-error line, the structured result sidecar and the
// legacy exit marker, then fixes the process exit code. It is idempotent like
// TS's `finished` guard.
// port of src/runtime/job-worker.ts:finish
func (w *jobWorker) finish(exitCode *int64, signal *string, workerErr error) int {
	if w.finished {
		return w.exitCode
	}
	w.finished = true

	if workerErr != nil {
		w.appendLog("\n[cave-job worker-error=" + jobJSONQuote(workerErr.Error()) + "]\n")
	}

	result := durableJobResult{
		Version:  1,
		JobID:    w.jobID,
		EndedAt:  time.Now().UnixMilli(),
		ExitCode: exitCode,
		Signal:   signal,
	}
	tempPath := w.resultPath + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tempPath, jobJSONLine(result), 0o600); err == nil {
		if err := os.Rename(tempPath, w.resultPath); err != nil {
			w.appendLog("\n[cave-job result-error=" + jobJSONQuote(err.Error()) + "]\n")
		}
	} else {
		w.appendLog("\n[cave-job result-error=" + jobJSONQuote(err.Error()) + "]\n")
	}
	_ = os.Remove(tempPath)

	marker := "\n[cave-job exit="
	if exitCode == nil {
		marker += "-1"
	} else {
		marker += strconv.FormatInt(*exitCode, 10)
	}
	if signal != nil {
		marker += " signal=" + *signal
	}
	marker += "]\n"
	w.appendLog(marker)

	if exitCode == nil {
		w.exitCode = 1
		return w.exitCode
	}
	code := *exitCode
	if code < 0 {
		code = 0
	}
	if code > 255 {
		code = 255
	}
	w.exitCode = int(code)
	return w.exitCode
}

// appendLog appends to the job log, swallowing errors like TS's try/catch.
// port of src/runtime/job-worker.ts:appendFileSync guards
func (w *jobWorker) appendLog(text string) {
	f, err := os.OpenFile(w.logPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		return
	}
	_, _ = f.WriteString(text)
	_ = f.Close()
}

// jsonLine renders v like `${JSON.stringify(v)}\n`: HTML escaping off and a
// trailing newline.
func jobJSONLine(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.Bytes()
}

// jsonQuote renders s like JSON.stringify(s) (no trailing newline).
func jobJSONQuote(s string) string {
	return string(bytes.TrimSuffix(jobJSONLine(s), []byte("\n")))
}

func jobInt64Ptr(value int64) *int64 {
	return &value
}

// jobInt64From widens the process helper's exit code for storage and the
// result sidecar.
func jobInt64From(code *int) *int64 {
	if code == nil {
		return nil
	}
	value := int64(*code)
	return &value
}
