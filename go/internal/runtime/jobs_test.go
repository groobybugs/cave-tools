package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// TestMain both isolates the storage singleton in a temp data dir and serves as
// the durable job worker when this binary is re-executed with JobWorkerCommand
// (the Go replacement for node spawning dist/runtime/job-worker.js).
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == JobWorkerCommand {
		os.Exit(RunJobWorker(os.Args[2:]))
	}
	dir, err := os.MkdirTemp("", "cave-tools-go-jobs-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	_ = os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// startAndWait starts a job and waits for it to settle.
func jobsStartAndWait(t *testing.T, command, session string) *storage.JobRecord {
	t.Helper()
	record, err := StartJob(StartJobInput{Command: command, SessionID: session})
	if err != nil {
		t.Fatalf("StartJob(%q): %v", command, err)
	}
	job, err := WaitForJob(record.JobID, 30_000)
	if err != nil {
		t.Fatalf("WaitForJob(%q): %v", record.JobID, err)
	}
	if job == nil {
		t.Fatalf("WaitForJob(%q) returned nil", record.JobID)
	}
	return job
}

func TestNewJobID(t *testing.T) {
	pattern := regexp.MustCompile(`^j[0-9a-z]+[0-9a-f]{6}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newJobID()
		if err != nil {
			t.Fatalf("newJobID: %v", err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("newJobID() = %q, want j + base36 millis + 6 hex chars", id)
		}
		if seen[id] {
			t.Fatalf("newJobID() = %q twice", id)
		}
		seen[id] = true
	}
}

func TestPathHelpers(t *testing.T) {
	id := "jabc123"
	if got, want := LogPathFor(id), filepath.Join(storage.JobsDir(), id+".log"); got != want {
		t.Errorf("LogPathFor = %q, want %q", got, want)
	}
	if got, want := ResultPathFor(id), filepath.Join(storage.JobsDir(), id+".result.json"); got != want {
		t.Errorf("ResultPathFor = %q, want %q", got, want)
	}
}

// Mirrors test.mjs background-job case 1: a fast command completes and the
// status data reports exit code 0 with the log content.
func TestStartJobFastCommand(t *testing.T) {
	job := jobsStartAndWait(t, "echo hello-bg", "test-jobs-fast")
	if job.State != storage.JobExited {
		t.Fatalf("state = %q, want exited", job.State)
	}
	if job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("exitCode = %v, want 0", job.ExitCode)
	}
	if job.Signal != nil {
		t.Fatalf("signal = %v, want nil", job.Signal)
	}
	if job.LogPath != LogPathFor(job.JobID) {
		t.Fatalf("logPath = %q, want %q", job.LogPath, LogPathFor(job.JobID))
	}
	if job.EndedAt == nil {
		t.Fatal("endedAt is nil")
	}
	log := jobsReadFile(t, job.LogPath)
	if !strings.Contains(log, "hello-bg") {
		t.Fatalf("log %q does not contain hello-bg", log)
	}
	header := regexp.MustCompile(`^# cave-job ` + regexp.QuoteMeta(job.JobID) + `\n# command: echo hello-bg\n# started: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z\n\n`)
	if !header.MatchString(log) {
		t.Fatalf("log header = %q", log)
	}
	info, err := os.Stat(job.LogPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("log mode = %04o, want 0600", perm)
	}
	resultInfo, err := os.Stat(ResultPathFor(job.JobID))
	if err != nil {
		t.Fatalf("result sidecar missing: %v", err)
	}
	if perm := resultInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("result mode = %04o, want 0600", perm)
	}
}

// Mirrors test.mjs case 7: a non-zero exit surfaces the exit code.
func TestStartJobExitCode(t *testing.T) {
	job := jobsStartAndWait(t, "exit 7", "test-jobs-exit")
	if job.State != storage.JobExited {
		t.Fatalf("state = %q, want exited", job.State)
	}
	if job.ExitCode == nil || *job.ExitCode != 7 {
		t.Fatalf("exitCode = %v, want 7", job.ExitCode)
	}
	result := jobsReadDurableResult(t, ResultPathFor(job.JobID))
	if result.ExitCode == nil || *result.ExitCode != 7 || result.Signal != nil {
		t.Fatalf("durable result = %+v", result)
	}
	if !strings.Contains(jobsReadFile(t, job.LogPath), "[cave-job exit=7]") {
		t.Fatalf("log missing exit marker: %q", jobsReadFile(t, job.LogPath))
	}
	if result.Version != 1 || result.JobID != job.JobID || result.EndedAt <= 0 {
		t.Fatalf("durable result = %+v", result)
	}
}

// Mirrors test.mjs case 3 (stop): stopJob reports killed and the process dies.
func TestStopJobKillsProcess(t *testing.T) {
	record, err := StartJob(StartJobInput{Command: "sleep 30", SessionID: "test-jobs-stop"})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	stopped, err := StopJob(record.JobID, "SIGTERM")
	if err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	if stopped == nil || stopped.State != storage.JobKilled {
		t.Fatalf("stopped = %+v, want state killed", stopped)
	}
	if stopped.Signal == nil || *stopped.Signal != "SIGTERM" {
		t.Fatalf("signal = %v, want SIGTERM", stopped.Signal)
	}
	deadline := time.Now().Add(5 * time.Second)
	for isProcessAlive(record.Pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still alive after SIGTERM", record.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := GetJobInfo(record.JobID)
	if err != nil {
		t.Fatalf("GetJobInfo: %v", err)
	}
	if job == nil || job.State != storage.JobKilled {
		t.Fatalf("job after stop = %+v, want killed", job)
	}

	if job, err := StopJob("j-nope", "SIGTERM"); err != nil || job != nil {
		t.Fatalf("StopJob(unknown) = %v, %v; want nil, nil", job, err)
	}
	exited := jobsStartAndWait(t, "true", "test-jobs-stop")
	again, err := StopJob(exited.JobID, "SIGTERM")
	if err != nil || again == nil || again.State != storage.JobExited {
		t.Fatalf("StopJob(exited) = %+v, %v; want unchanged exited", again, err)
	}
}

// Mirrors test.mjs case 6: restart recovery from the legacy exit marker, and
// lost when there is no evidence of completion.
func TestGetJobInfoRecovery(t *testing.T) {
	deadPid := int64(4194303)
	mkJob := func(t *testing.T, id string, marker string) string {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), id+".log")
		content := "fake log\n"
		if marker != "" {
			content += marker + "\n"
		}
		if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
		if err := storage.InsertJob(storage.JobRecord{
			JobID:     id,
			Pid:       deadPid,
			SessionID: "test-jobs-recovery",
			Command:   "fake",
			StartedAt: time.Now().UnixMilli() - 1000,
			State:     storage.JobRunning,
			LogPath:   logPath,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
		return id
	}

	t.Run("marker recovers exit code", func(t *testing.T) {
		id := mkJob(t, "j-test-recover", "[cave-job exit=3]")
		job, err := GetJobInfo(id)
		if err != nil {
			t.Fatalf("GetJobInfo: %v", err)
		}
		if job.State != storage.JobExited || job.ExitCode == nil || *job.ExitCode != 3 {
			t.Fatalf("job = %+v, want exited code 3", job)
		}
		stored, err := storage.GetJob(id)
		if err != nil || stored == nil || stored.State != storage.JobExited {
			t.Fatalf("stored = %+v, %v; want exited", stored, err)
		}
	})

	t.Run("no marker becomes lost", func(t *testing.T) {
		id := mkJob(t, "j-test-lost", "")
		job, err := GetJobInfo(id)
		if err != nil {
			t.Fatalf("GetJobInfo: %v", err)
		}
		if job.State != storage.JobLost || job.EndedAt == nil {
			t.Fatalf("job = %+v, want lost with endedAt", job)
		}
		stored, err := storage.GetJob(id)
		if err != nil || stored == nil || stored.State != storage.JobLost {
			t.Fatalf("stored = %+v, %v; want lost", stored, err)
		}
	})

	t.Run("signal marker", func(t *testing.T) {
		id := mkJob(t, "j-test-recover-signal", "[cave-job exit=-1 signal=SIGTERM]")
		job, err := GetJobInfo(id)
		if err != nil {
			t.Fatalf("GetJobInfo: %v", err)
		}
		if job.State != storage.JobExited || job.ExitCode == nil || *job.ExitCode != -1 {
			t.Fatalf("job = %+v, want exited code -1", job)
		}
		if job.Signal == nil || *job.Signal != "SIGTERM" {
			t.Fatalf("signal = %v, want SIGTERM", job.Signal)
		}
	})

	t.Run("marker must be at the end", func(t *testing.T) {
		id := mkJob(t, "j-test-recover-mid", "[cave-job exit=3] trailing")
		job, err := GetJobInfo(id)
		if err != nil {
			t.Fatalf("GetJobInfo: %v", err)
		}
		if job.State != storage.JobLost {
			t.Fatalf("job = %+v, want lost (marker not at end)", job)
		}
	})
}

func TestGetJobInfoMissing(t *testing.T) {
	job, err := GetJobInfo("j-does-not-exist")
	if err != nil || job != nil {
		t.Fatalf("GetJobInfo(missing) = %v, %v; want nil, nil", job, err)
	}
}

// Mirrors test.mjs case 9: byte chunks never split a UTF-8 sequence and resume
// without duplicates.
func TestReadFileChunkUTF8Boundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf.log")
	content := strings.Repeat("a", 65535) + "€Z"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := ReadFileChunk(path, 0, 65536)
	if first.Text != strings.Repeat("a", 65535) {
		t.Fatalf("first chunk length = %d, want 65535", len(first.Text))
	}
	if first.NextCursor != 65535 {
		t.Fatalf("first nextCursor = %d, want 65535", first.NextCursor)
	}
	if !first.Truncated {
		t.Fatal("first chunk should be truncated")
	}
	second := ReadFileChunk(path, first.NextCursor, 65536)
	if second.Text != "€Z" {
		t.Fatalf("second chunk = %q, want €Z", second.Text)
	}
}

func TestReadFileChunkCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("whole file", func(t *testing.T) {
		chunk := ReadFileChunk(path, 0, 10)
		if chunk.Text != "hello" || chunk.StartCursor != 0 || chunk.NextCursor != 5 ||
			chunk.FileSize != 5 || chunk.Truncated || chunk.CursorReset {
			t.Fatalf("chunk = %+v", chunk)
		}
	})
	t.Run("bounded chunk", func(t *testing.T) {
		chunk := ReadFileChunk(path, 0, 2)
		if chunk.Text != "he" || chunk.NextCursor != 2 || !chunk.Truncated {
			t.Fatalf("chunk = %+v", chunk)
		}
	})
	t.Run("zero bytes", func(t *testing.T) {
		chunk := ReadFileChunk(path, 0, 0)
		if chunk.Text != "" || chunk.NextCursor != 0 || !chunk.Truncated || chunk.CursorReset {
			t.Fatalf("chunk = %+v", chunk)
		}
	})
	t.Run("cursor past end resets", func(t *testing.T) {
		chunk := ReadFileChunk(path, 99, 10)
		if !chunk.CursorReset || chunk.StartCursor != 0 || chunk.Text != "hello" {
			t.Fatalf("chunk = %+v", chunk)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		chunk := ReadFileChunk(filepath.Join(dir, "missing.txt"), 5, 10)
		if chunk.Text != "" || chunk.FileSize != 0 || chunk.Truncated || !chunk.CursorReset {
			t.Fatalf("chunk = %+v", chunk)
		}
		chunk = ReadFileChunk(filepath.Join(dir, "missing.txt"), 0, 10)
		if chunk.CursorReset {
			t.Fatalf("chunk = %+v, want cursorReset false at cursor 0", chunk)
		}
	})
	t.Run("utf8 boundaries are respected", func(t *testing.T) {
		euro := filepath.Join(dir, "euro.txt")
		if err := os.WriteFile(euro, []byte("a€Z"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Start in the middle of "€" (0xe2 0x82 0xac): the two continuation
		// bytes are skipped, so the chunk starts at "Z".
		chunk := ReadFileChunk(euro, 2, 10)
		if chunk.Text != "Z" || chunk.StartCursor != 4 || chunk.NextCursor != 5 {
			t.Fatalf("chunk = %+v, want Z at 4..5", chunk)
		}
		// A chunk that ends mid-sequence drops the partial lead byte.
		chunk = ReadFileChunk(euro, 0, 2)
		if chunk.Text != "a" || chunk.NextCursor != 1 || !chunk.Truncated {
			t.Fatalf("chunk = %+v, want a truncated at 1", chunk)
		}
	})
}

func TestTailFileBytesAndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := TailFileBytes(path, 2); got != "lo" {
		t.Errorf("TailFileBytes = %q, want lo", got)
	}
	if got := TailFileBytes(path, 99); got != "hello" {
		t.Errorf("TailFileBytes(all) = %q, want hello", got)
	}
	if got := TailFileBytes(filepath.Join(t.TempDir(), "nope"), 2); got != "" {
		t.Errorf("TailFileBytes(missing) = %q, want empty", got)
	}
	if got := FileByteSize(path); got != 5 {
		t.Errorf("FileByteSize = %d, want 5", got)
	}
	if got := FileByteSize(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Errorf("FileByteSize(missing) = %d, want 0", got)
	}
}

// Mirrors test.mjs case 10: a launcher process can die while its detached
// worker later records structured completion; adopted wait polls and returns
// before its timeout.
func TestWaitForJobAdoptedAfterRestart(t *testing.T) {
	record, err := StartJob(StartJobInput{
		Command:   "sleep 1; printf durable-restart; exit 4",
		SessionID: "test-jobs-adopted",
	})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	// Simulate a fresh server process: no local close waiter for this job.
	closeWaitersMu.Lock()
	delete(closeWaiters, record.JobID)
	closeWaitersMu.Unlock()

	started := time.Now()
	job, err := WaitForJob(record.JobID, 10_000)
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("adopted wait took %v, want < 5s", elapsed)
	}
	if job == nil || job.State != storage.JobExited || job.ExitCode == nil || *job.ExitCode != 4 {
		t.Fatalf("job = %+v, want exited code 4", job)
	}
	if log := jobsReadFile(t, record.LogPath); !strings.Contains(log, "durable-restart") {
		t.Fatalf("log = %q", log)
	}
	if _, err := os.Stat(ResultPathFor(record.JobID)); err != nil {
		t.Fatalf("durable result sidecar missing: %v", err)
	}
}

// Mirrors test.mjs case 11: pruning never removes an old log still referenced
// by a running row.
func TestPruneJobs(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)

	activeID := "j-test-active-prune"
	activeLog := filepath.Join(dir, activeID+".log")
	if err := os.WriteFile(activeLog, []byte("active\n"), 0o600); err != nil {
		t.Fatalf("write active log: %v", err)
	}
	if err := os.Chtimes(activeLog, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := storage.InsertJob(storage.JobRecord{
		JobID:     activeID,
		Pid:       int64(os.Getpid()),
		SessionID: "test-jobs-prune",
		Command:   "active",
		StartedAt: old.UnixMilli(),
		State:     storage.JobRunning,
		LogPath:   activeLog,
	}); err != nil {
		t.Fatalf("insert active job: %v", err)
	}

	endedID := "j-test-ended-prune"
	endedLog := filepath.Join(dir, endedID+".log")
	endedResult := ResultPathFor(endedID)
	if err := os.WriteFile(endedLog, []byte("done\n"), 0o600); err != nil {
		t.Fatalf("write ended log: %v", err)
	}
	if err := os.WriteFile(endedResult, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write ended result: %v", err)
	}
	if err := os.Chtimes(endedLog, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chtimes(endedResult, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := storage.InsertJob(storage.JobRecord{
		JobID:     endedID,
		Pid:       int64(os.Getpid()),
		SessionID: "test-jobs-prune",
		Command:   "done",
		StartedAt: old.UnixMilli(),
		EndedAt:   jobInt64Ptr(old.UnixMilli()),
		ExitCode:  jobInt64Ptr(0),
		State:     storage.JobExited,
		LogPath:   endedLog,
	}); err != nil {
		t.Fatalf("insert ended job: %v", err)
	}

	orphanOld := filepath.Join(storage.JobsDir(), "j-orphan-old.log")
	if err := os.WriteFile(orphanOld, []byte("orphan\n"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	if err := os.Chtimes(orphanOld, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	orphanFresh := filepath.Join(storage.JobsDir(), "j-orphan-fresh.log")
	if err := os.WriteFile(orphanFresh, []byte("orphan\n"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	removed, err := PruneJobs()
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if removed < 1 {
		t.Fatalf("removed = %d, want at least 1", removed)
	}
	if _, err := os.Stat(activeLog); err != nil {
		t.Fatalf("active job log was pruned: %v", err)
	}
	if _, err := os.Stat(endedLog); !os.IsNotExist(err) {
		t.Fatalf("ended job log still exists: %v", err)
	}
	if _, err := os.Stat(endedResult); !os.IsNotExist(err) {
		t.Fatalf("ended job result still exists: %v", err)
	}
	if job, err := storage.GetJob(endedID); err != nil || job != nil {
		t.Fatalf("ended job row = %+v, %v; want deleted", job, err)
	}
	if _, err := os.Stat(orphanOld); !os.IsNotExist(err) {
		t.Fatalf("old orphan log still exists: %v", err)
	}
	if _, err := os.Stat(orphanFresh); err != nil {
		t.Fatalf("fresh orphan log was pruned: %v", err)
	}

	_, _ = PruneJobs(-1)
}

func TestListSessionJobs(t *testing.T) {
	first := jobsStartAndWait(t, "echo one", "test-jobs-list")
	second := jobsStartAndWait(t, "echo two", "test-jobs-list")

	jobs, err := ListSessionJobs("test-jobs-list")
	if err != nil {
		t.Fatalf("ListSessionJobs: %v", err)
	}
	ids := map[string]bool{}
	for _, job := range jobs {
		ids[job.JobID] = true
	}
	if !ids[first.JobID] || !ids[second.JobID] {
		t.Fatalf("jobs = %+v, want %s and %s", jobs, first.JobID, second.JobID)
	}
	other, err := ListSessionJobs("test-jobs-list-other")
	if err != nil {
		t.Fatalf("ListSessionJobs: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("other session jobs = %+v, want none", other)
	}
}

func TestRecoverFromResultValidation(t *testing.T) {
	valid := `{"version":1,"jobId":"j1","endedAt":1700000000000,"exitCode":7,"signal":null}`
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"valid", valid, true},
		{"null signal and code", `{"version":1,"jobId":"j1","endedAt":1,"exitCode":null,"signal":"SIGTERM"}`, true},
		{"wrong version", `{"version":2,"jobId":"j1","endedAt":1,"exitCode":null,"signal":null}`, false},
		{"wrong job", `{"version":1,"jobId":"j2","endedAt":1,"exitCode":null,"signal":null}`, false},
		{"missing endedAt", `{"version":1,"jobId":"j1","exitCode":null,"signal":null}`, false},
		{"missing exitCode", `{"version":1,"jobId":"j1","endedAt":1,"signal":null}`, false},
		{"missing signal", `{"version":1,"jobId":"j1","endedAt":1,"exitCode":null}`, false},
		{"string exitCode", `{"version":1,"jobId":"j1","endedAt":1,"exitCode":"7","signal":null}`, false},
		{"string endedAt", `{"version":1,"jobId":"j1","endedAt":"1","exitCode":null,"signal":null}`, false},
		{"not json", `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := recoverFromResult("j1", path) != nil
			if got != tc.want {
				t.Fatalf("recoverFromResult = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWaitWithTimeoutZeroReturnsImmediately(t *testing.T) {
	never := make(chan struct{})
	started := time.Now()
	waitWithTimeout(never, 0)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("waitWithTimeout(0) took %v", elapsed)
	}
}

// Worker-level assertions mirroring src/runtime/job-worker.ts behaviour when it
// is invoked directly.
func TestRunJobWorker(t *testing.T) {
	t.Run("invalid arguments", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], JobWorkerCommand)
		out, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if err == nil || !ok || exitErr.ExitCode() != 1 {
			t.Fatalf("worker exit = %v, want 1 (out=%q)", err, out)
		}
		if !strings.Contains(string(out), "cave job worker: invalid arguments") {
			t.Fatalf("stderr = %q", out)
		}
	})

	t.Run("spawn error records worker-error", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "job.log")
		resultPath := filepath.Join(dir, "job.result.json")
		if err := os.WriteFile(logPath, nil, 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
		code := RunJobWorker([]string{"j-worker-error", logPath, resultPath, "/nonexistent-shell", "echo hi"})
		// exitCode -1 clamps to 0, matching Node's Math.max(0, Math.min(255, -1)).
		if code != 0 {
			t.Fatalf("worker exit = %d, want 0", code)
		}
		log := jobsReadFile(t, logPath)
		if !strings.Contains(log, `[cave-job worker-error="spawn /nonexistent-shell ENOENT"]`) {
			t.Fatalf("log = %q", log)
		}
		if !strings.Contains(log, "[cave-job exit=-1]") {
			t.Fatalf("log = %q", log)
		}
		result := jobsReadDurableResult(t, resultPath)
		if result.ExitCode == nil || *result.ExitCode != -1 || result.Signal != nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("signal termination records signal name", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "job.log")
		resultPath := filepath.Join(dir, "job.result.json")
		code := RunJobWorker([]string{"j-worker-signal", logPath, resultPath, "/bin/sh", "kill -TERM $$"})
		if code != 1 {
			t.Fatalf("worker exit = %d, want 1 (null exit code)", code)
		}
		result := jobsReadDurableResult(t, resultPath)
		if result.ExitCode != nil {
			t.Fatalf("exitCode = %v, want null", result.ExitCode)
		}
		if result.Signal == nil || *result.Signal != "SIGTERM" {
			t.Fatalf("signal = %v, want SIGTERM", result.Signal)
		}
		if log := jobsReadFile(t, logPath); !strings.Contains(log, "[cave-job exit=-1 signal=SIGTERM]") {
			t.Fatalf("log = %q", log)
		}
	})

	t.Run("exit code clamps to 255", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "job.log")
		resultPath := filepath.Join(dir, "job.result.json")
		// `exit 300` is truncated by the shell to 44; use a direct finish call
		// for the clamp itself.
		worker := &jobWorker{jobID: "j-clamp", logPath: logPath, resultPath: resultPath}
		if got := worker.finish(jobInt64Ptr(300), nil, nil); got != 255 {
			t.Fatalf("finish(300) = %d, want 255", got)
		}
		if got := worker.finish(jobInt64Ptr(300), nil, nil); got != 255 {
			t.Fatalf("second finish = %d, want 255 (idempotent)", got)
		}
	})
}

// readFile reads a file or fails the test.
func jobsReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// readDurableResult decodes a worker result sidecar.
func jobsReadDurableResult(t *testing.T, path string) durableJobResult {
	t.Helper()
	var result durableJobResult
	if err := json.Unmarshal([]byte(jobsReadFile(t, path)), &result); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return result
}
