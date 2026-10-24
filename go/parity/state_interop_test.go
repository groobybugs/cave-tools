//go:build parity

// Cross-implementation persistence: jobs, archives, session stats and budgets
// produced by one implementation and consumed by the other through the shared
// data dir / SQLite schema.
package parity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sharedStateHarness is one data/runtime/config tree used by both binaries.
type sharedStateHarness struct {
	root     string
	data     string
	run      string
	home     string
	claude   string
	work     string
	tsBinary string
	tsPrefix []string
	goBinary string
}

// newSharedStateHarness creates the shared tree.
func newSharedStateHarness(t *testing.T) *sharedStateHarness {
	t.Helper()
	requireMode(t)
	tsBinary, tsPrefix := typescriptCLI(t)
	h := &sharedStateHarness{
		root:     repoRoot(t),
		data:     filepath.Join(t.TempDir(), "data"),
		run:      filepath.Join(t.TempDir(), "run"),
		home:     filepath.Join(t.TempDir(), "home"),
		claude:   filepath.Join(t.TempDir(), "claude"),
		work:     filepath.Join(t.TempDir(), "work"),
		tsBinary: tsBinary,
		tsPrefix: tsPrefix,
		goBinary: sharedGoBinary(t),
	}
	for _, dir := range []string{h.data, h.run, h.home, h.claude, h.work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return h
}

// env is the shared non-daemon environment.
func (h *sharedStateHarness) env(extra map[string]string) map[string]string {
	env := map[string]string{
		"CAVE_TOOLS_DATA_DIR":    h.data,
		"CLAUDE_CONFIG_DIR":      h.claude,
		"XDG_RUNTIME_DIR":        h.run,
		"HOME":                   h.home,
		"CAVE_TOOLS_DAEMON":      "0",
		"CAVE_TOOLS_GO_DIST_DIR": filepath.Join(h.root, "dist"),
		"CAVE_TOOLS_STATUS_RTK":  "0",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// ts runs the TypeScript CLI.
func (h *sharedStateHarness) tsCLI(t *testing.T, args ...string) outcome {
	t.Helper()
	return runCommandOutcome(t, h.tsBinary, h.tsPrefix, h.work, h.env(nil), "", args...)
}

// go runs the Go CLI.
func (h *sharedStateHarness) goCLI(t *testing.T, args ...string) outcome {
	t.Helper()
	return runCommandOutcome(t, h.goBinary, nil, h.work, h.env(nil), "", args...)
}

// writeFile creates a file in the shared work dir.
func (h *sharedStateHarness) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.work, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// waitJob polls the persisted table until the job is no longer running.
func waitJob(t *testing.T, dataDir, jobID string) jobRow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		state := readDBState(t, dataDir)
		for _, job := range state.Jobs {
			if job.JobID == jobID && job.State != "running" {
				return job
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %s", jobID, formatDBState(state))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readResultSidecar decodes data/jobs/<id>.result.json.
func readResultSidecar(t *testing.T, dataDir, jobID string) map[string]any {
	t.Helper()
	path := filepath.Join(dataDir, "jobs", jobID+".result.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result sidecar: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode result sidecar %q: %v", raw, err)
	}
	return decoded
}

// TestJobTSProducerGoConsumer starts a durable job with the TypeScript CLI and
// adopts it through the Go CLI (list/log/status/kill + result sidecar).
func TestJobTSProducerGoConsumer(t *testing.T) {
	requireMode(t)
	h := newSharedStateHarness(t)
	started := h.tsCLI(t, "bash_start", "printf 'job-out\\n'; printf 'job-err\\n' >&2; exit 7")
	if started.code != 0 {
		t.Fatalf("TS bash_start = code %d stdout %q stderr %q", started.code, started.stdout, started.stderr)
	}
	jobID := jobIDFromStart(t, started)
	waited := h.goCLI(t, "bash_status", jobID, "--wait", "60", "--tail_lines", "0")
	if !strings.Contains(waited.stdout+waited.stderr, jobID) {
		t.Fatalf("Go bash_status while adopting TS job = code %d stdout %q stderr %q", waited.code, waited.stdout, waited.stderr)
	}
	job := waitJob(t, h.data, jobID)
	if job.State != "exited" || job.ExitCode == nil || *job.ExitCode != 7 {
		t.Fatalf("persisted job after TS start = %+v", job)
	}

	list := h.goCLI(t, "jobs")
	if list.code != 0 || !strings.Contains(list.stdout, jobID) || !strings.Contains(list.stdout, "exited(7)") {
		t.Fatalf("Go jobs list = code %d stdout %q stderr %q", list.code, list.stdout, list.stderr)
	}
	log := h.goCLI(t, "jobs", "log", jobID, "20")
	if log.code != 0 || !strings.Contains(log.stdout, "job-out") || !strings.Contains(log.stdout, "job-err") || !strings.Contains(log.stdout, "[cave-job exit=7]") {
		t.Fatalf("Go jobs log = code %d stdout %q stderr %q", log.code, log.stdout, log.stderr)
	}
	goStatus := h.goCLI(t, "bash_status", jobID, "--tail_lines", "5")
	tsStatus := h.tsCLI(t, "bash_status", jobID, "--tail_lines", "5")
	if goStatus != tsStatus {
		t.Fatalf("bash_status differs across implementations\n go: %#v\n ts: %#v", goStatus, tsStatus)
	}
	if goStatus.code != 1 || !strings.Contains(goStatus.stderr, "exited (code 7)") || !strings.Contains(goStatus.stderr, "job-out") {
		t.Fatalf("bash_status = code %d stdout %q stderr %q", goStatus.code, goStatus.stdout, goStatus.stderr)
	}
	sidecar := readResultSidecar(t, h.data, jobID)
	if sidecar["jobId"] != jobID || sidecar["exitCode"] != float64(7) {
		t.Fatalf("result sidecar = %#v", sidecar)
	}
	killed := h.goCLI(t, "jobs", "kill", jobID)
	if killed.code != 0 || !strings.Contains(killed.stdout, "already finished (state: exited).") {
		t.Fatalf("Go jobs kill = code %d stdout %q stderr %q", killed.code, killed.stdout, killed.stderr)
	}
}

// TestJobGoProducerTSConsumer starts a durable job with the Go CLI and adopts
// it through the TypeScript CLI.
func TestJobGoProducerTSConsumer(t *testing.T) {
	requireMode(t)
	h := newSharedStateHarness(t)
	started := h.goCLI(t, "bash_start", "printf 'go-job-out\\n'; printf 'go-job-err\\n' >&2; exit 5")
	if started.code != 0 {
		t.Fatalf("Go bash_start = code %d stdout %q stderr %q", started.code, started.stdout, started.stderr)
	}
	jobID := jobIDFromStart(t, started)
	waited := h.tsCLI(t, "bash_status", jobID, "--wait", "60", "--tail_lines", "0")
	if !strings.Contains(waited.stdout+waited.stderr, jobID) {
		t.Fatalf("TS bash_status while adopting Go job = code %d stdout %q stderr %q", waited.code, waited.stdout, waited.stderr)
	}
	job := waitJob(t, h.data, jobID)
	if job.State != "exited" || job.ExitCode == nil || *job.ExitCode != 5 {
		t.Fatalf("persisted job after Go start = %+v", job)
	}

	list := h.tsCLI(t, "jobs")
	if list.code != 0 || !strings.Contains(list.stdout, jobID) || !strings.Contains(list.stdout, "exited(5)") {
		t.Fatalf("TS jobs list = code %d stdout %q stderr %q", list.code, list.stdout, list.stderr)
	}
	log := h.tsCLI(t, "jobs", "log", jobID, "20")
	if log.code != 0 || !strings.Contains(log.stdout, "go-job-out") || !strings.Contains(log.stdout, "go-job-err") || !strings.Contains(log.stdout, "[cave-job exit=5]") {
		t.Fatalf("TS jobs log = code %d stdout %q stderr %q", log.code, log.stdout, log.stderr)
	}
	tsStatus := h.tsCLI(t, "bash_status", jobID, "--tail_lines", "5")
	goStatus := h.goCLI(t, "bash_status", jobID, "--tail_lines", "5")
	if tsStatus != goStatus {
		t.Fatalf("bash_status differs across implementations\n ts: %#v\n go: %#v", tsStatus, goStatus)
	}
	sidecar := readResultSidecar(t, h.data, jobID)
	if sidecar["jobId"] != jobID || sidecar["exitCode"] != float64(5) {
		t.Fatalf("result sidecar = %#v", sidecar)
	}
	killed := h.tsCLI(t, "jobs", "kill", jobID)
	if killed.code != 0 || !strings.Contains(killed.stdout, "already finished (state: exited).") {
		t.Fatalf("TS jobs kill = code %d stdout %q stderr %q", killed.code, killed.stdout, killed.stderr)
	}
}

// TestArchiveCrossImplementation archives through one implementation and
// expands through the other, using the shared archive DB/file store.
func TestArchiveCrossImplementation(t *testing.T) {
	requireMode(t)
	h := newSharedStateHarness(t)
	if result := h.tsCLI(t, "bash", "seq 1 200"); result.code != 0 {
		t.Fatalf("TS bash archive = %#v", result)
	}
	first := readDBState(t, h.data).Archives
	if len(first) != 1 {
		t.Fatalf("TS archive rows = %+v", first)
	}
	tsArchive := first[0]
	if tsArchive.ID == "" || !strings.Contains(tsArchive.Command, "seq 1 200") {
		t.Fatalf("TS archive row = %+v", tsArchive)
	}
	if info, err := os.Stat(tsArchive.ContentPath); err != nil || info.Size() == 0 {
		t.Fatalf("TS archive content file %s: %v", tsArchive.ContentPath, err)
	}
	expanded := h.goCLI(t, "compress", "x", "--action", "expand", "--archive_id", tsArchive.ID)
	if expanded.code != 0 || expanded.stdout != numberLines(1, 200) {
		t.Fatalf("Go expand of TS archive = code %d stdout %q stderr %q", expanded.code, expanded.stdout, expanded.stderr)
	}

	if result := h.goCLI(t, "bash", "seq 1000 1200"); result.code != 0 {
		t.Fatalf("Go bash archive = %#v", result)
	}
	var goArchive archiveRow
	for _, entry := range readDBState(t, h.data).Archives {
		if entry.ID != tsArchive.ID {
			goArchive = entry
		}
	}
	if goArchive.ID == "" || !strings.Contains(goArchive.Command, "seq 1000 1200") {
		t.Fatalf("Go archive row = %+v", goArchive)
	}
	expandedTS := h.tsCLI(t, "compress", "x", "--action", "expand", "--archive_id", goArchive.ID)
	if expandedTS.code != 0 || expandedTS.stdout != numberLines(1000, 1200) {
		t.Fatalf("TS expand of Go archive = code %d stdout %q stderr %q", expandedTS.code, expandedTS.stdout, expandedTS.stderr)
	}
}

// TestStatsAndBudgetsCrossImplementation proves both implementations persist
// and read the same session stats and budgets.
func TestStatsAndBudgetsCrossImplementation(t *testing.T) {
	requireMode(t)
	h := newSharedStateHarness(t)
	h.writeFile(t, "note.txt", "stats content\n")
	if result := h.tsCLI(t, "bash", "echo ts-stats"); result.code != 0 {
		t.Fatalf("TS bash = %#v", result)
	}
	if result := h.tsCLI(t, "read", "note.txt"); result.code != 0 {
		t.Fatalf("TS read = %#v", result)
	}
	if result := h.goCLI(t, "bash", "echo go-stats"); result.code != 0 {
		t.Fatalf("Go bash = %#v", result)
	}

	state := readDBState(t, h.data)
	var bashCalls, readMisses int64
	for _, row := range state.Sessions {
		bashCalls += row.Tools["bash"]
		readMisses += row.CacheMisses
	}
	if bashCalls < 2 {
		t.Fatalf("shared DB accounted %d bash calls, want >=2: %s", bashCalls, formatDBState(state))
	}
	if readMisses < 1 {
		t.Fatalf("shared DB lost the read cache event: %s", formatDBState(state))
	}

	// Both CLIs render the same persisted state, modulo wall-clock age.
	tsStatus := h.tsCLI(t, "status", "--verbose")
	goStatus := h.goCLI(t, "status", "--verbose")
	if tsStatus.code != 0 || goStatus.code != 0 {
		t.Fatalf("status codes ts=%d go=%d", tsStatus.code, goStatus.code)
	}
	if normalizeStatusClock(tsStatus.stdout) != normalizeStatusClock(goStatus.stdout) {
		t.Fatalf("status output differs across implementations\n--- ts ---\n%s\n--- go ---\n%s", tsStatus.stdout, goStatus.stdout)
	}

	// Budget written by TS is visible to Go, and vice versa.
	if result := h.tsCLI(t, "configure", "--action", "set_budget", "--tool_name", "read", "--max_lines", "33", "--max_chars", "77"); result.code != 0 {
		t.Fatalf("TS configure = %#v", result)
	}
	if state := readDBState(t, h.data); !hasBudget(state, "read", 33, 77) {
		t.Fatalf("TS budget was not persisted for Go to read: %s", formatDBState(state))
	}
	if result := h.goCLI(t, "configure", "--action", "set_budget", "--tool_name", "bash", "--max_lines", "44", "--max_chars", "88"); result.code != 0 {
		t.Fatalf("Go configure = %#v", result)
	}
	if state := readDBState(t, h.data); !hasBudget(state, "bash", 44, 88) {
		t.Fatalf("Go budget was not persisted for TS to read: %s", formatDBState(state))
	}
}
