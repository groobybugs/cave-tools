//go:build parity

// Helpers shared by the black-box CLI, MCP and cross-implementation tests:
// seeding rows through the TypeScript storage layer and reading persisted
// state back through the same schema both implementations use.
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nodeModuleScript runs an inline ESM script with node and returns stdout.
func nodeModuleScript(t *testing.T, cwd string, env map[string]string, script string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Dir = cwd
	cmd.Env = flattenEnv(cleanEnv(env))
	output, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node helper failed: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("node helper: %v", err)
	}
	return string(output)
}

// moduleURLExpr resolves CT_MODULE into a file URL inside a node helper.
const moduleURLExpr = `const { pathToFileURL } = await import("node:url"); const mod = await import(pathToFileURL(process.env.CT_MODULE).href);`

// distModule points at one compiled TS module.
func distModule(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{repoRoot(t), "dist"}, parts...)...)
}

// fixedJob is the deterministic seeded job used by CLI format tests.
func fixedJob(logPath string) map[string]any {
	return map[string]any{
		"jobId":     "jobfixed",
		"pid":       4242,
		"sessionId": "seed",
		"command":   "echo   token=ghp_abcdefghijklmnopqrstuvwxyz0123456789  hi",
		"workdir":   nil,
		"startedAt": int64(1700000000000),
		"endedAt":   int64(1700000004000),
		"exitCode":  3,
		"signal":    nil,
		"state":     "exited",
		"logPath":   logPath,
	}
}

// seedJob inserts fixedJob into a side's data dir through TS storage (proving
// Go reads TS-written rows too).
func seedJob(t *testing.T, s *side) {
	t.Helper()
	job := fixedJob(filepath.Join(s.data, "jobs", "jobfixed.log"))
	if err := os.MkdirAll(filepath.Join(s.data, "jobs"), 0o700); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	if err := os.WriteFile(job["logPath"].(string), []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatalf("write job log: %v", err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	script := moduleURLExpr + `
const job = JSON.parse(process.env.CT_JOB);
mod.insertJob(job);
`
	env := map[string]string{
		"CAVE_TOOLS_DATA_DIR": s.data,
		"CT_MODULE":           distModule(t, "storage", "db.js"),
		"CT_JOB":              string(payload),
	}
	nodeModuleScript(t, s.root, env, script)
}

// sessionRow is the persisted per-client stats row this suite asserts on.
type sessionRow struct {
	Pid          int64            `json:"pid"`
	TotalCalls   int64            `json:"totalCalls"`
	RawChars     int64            `json:"rawChars"`
	CacheHits    int64            `json:"cacheHits"`
	CacheMisses  int64            `json:"cacheMisses"`
	FilesTracked int64            `json:"filesTracked"`
	Tools        map[string]int64 `json:"tools"`
	Budgets      map[string]struct {
		MaxLines *float64 `json:"maxLines"`
		MaxChars *float64 `json:"maxChars"`
	} `json:"budgets"`
}

// jobRow mirrors the persisted job columns.
type jobRow struct {
	JobID    string `json:"jobId"`
	Pid      int64  `json:"pid"`
	State    string `json:"state"`
	ExitCode *int64 `json:"exitCode"`
	LogPath  string `json:"logPath"`
}

// archiveRow mirrors the persisted archive columns.
type archiveRow struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	SizeChars   int64  `json:"sizeChars"`
	ContentPath string `json:"contentPath"`
}

// dbState is the semantic snapshot read through the shared TS storage layer.
type dbState struct {
	Sessions []sessionRow `json:"sessions"`
	Jobs     []jobRow     `json:"jobs"`
	Archives []archiveRow `json:"archives"`
}

// readDBState reads sessions/jobs/archives from a data dir through TS storage.
func readDBState(t *testing.T, dataDir string) dbState {
	t.Helper()
	script := moduleURLExpr + `
const sessions = (await mod.listSessionStats(true)).map((s) => ({
  pid: s.pid,
  totalCalls: s.savings?.totalCalls ?? 0,
  rawChars: s.savings?.rawChars ?? 0,
  cacheHits: s.cache?.hits ?? 0,
  cacheMisses: s.cache?.misses ?? 0,
  filesTracked: s.cache?.filesTracked ?? 0,
  tools: Object.fromEntries(Object.entries(s.savings?.byTool ?? {}).map(([k, v]) => [k, v.calls])),
  budgets: s.budgets ?? {},
}));
const jobs = mod.listJobs().map((j) => ({
  jobId: j.jobId, pid: j.pid, state: j.state, exitCode: j.exitCode ?? null, logPath: j.logPath,
}));
const archives = mod.listArchives().map((a) => ({
  id: a.id, command: a.command, sizeChars: a.sizeChars, contentPath: a.contentPath,
}));
console.log(JSON.stringify({ sessions, jobs, archives }));
`
	env := map[string]string{
		"CAVE_TOOLS_DATA_DIR": dataDir,
		"CT_MODULE":           distModule(t, "storage", "db.js"),
	}
	raw := nodeModuleScript(t, repoRoot(t), env, script)
	var state dbState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode db state %q: %v", raw, err)
	}
	return state
}

// toolErrorCount reads the privacy-safe tool-error row count.
func toolErrorCount(t *testing.T, dataDir string) int {
	t.Helper()
	script := moduleURLExpr + `
console.log(JSON.stringify(mod.countToolErrors()));
`
	env := map[string]string{
		"CAVE_TOOLS_DATA_DIR": dataDir,
		"CT_MODULE":           distModule(t, "runtime", "tool-errors.js"),
	}
	raw := strings.TrimSpace(nodeModuleScript(t, repoRoot(t), env, script))
	var counts struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(raw), &counts); err != nil {
		t.Fatalf("decode tool error counts %q: %v", raw, err)
	}
	return counts.Total
}

// seededToolError is one deterministic tool_errors row.
type seededToolError struct {
	ID              string  `json:"id"`
	CreatedAt       int64   `json:"createdAt"`
	Tool            string  `json:"tool"`
	Outcome         string  `json:"outcome"`
	ReasonCode      string  `json:"reasonCode"`
	ElapsedMS       int64   `json:"elapsedMs"`
	Mode            string  `json:"mode"`
	Operation       *string `json:"operation"`
	BatchIndex      *int64  `json:"batchIndex"`
	Agent           string  `json:"agent"`
	SessionHash     string  `json:"sessionHash"`
	FileHash        *string `json:"fileHash"`
	ArgKeys         string  `json:"argKeys"`
	ArgTypes        string  `json:"argTypes"`
	ArgCount        int64   `json:"argCount"`
	UnknownArgCount int64   `json:"unknownArgCount"`
	ErrorKind       *string `json:"errorKind"`
}

// seedToolErrors writes fixed rows through node:sqlite after the TS runtime
// module creates the table. This makes the `errors` report deterministic
// without depending on the documented best-effort async logger.
func seedToolErrors(t *testing.T, s *side, rows []seededToolError) {
	t.Helper()
	_ = toolErrorCount(t, s.data) // create schema
	payload := mustJSON(t, rows)
	script := `
const { DatabaseSync } = await import("node:sqlite");
const db = new DatabaseSync(process.env.CT_DB);
db.exec(` + "`" + `
CREATE TABLE IF NOT EXISTS tool_errors (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  tool TEXT NOT NULL,
  outcome TEXT NOT NULL,
  reason_code TEXT NOT NULL,
  elapsed_ms INTEGER NOT NULL,
  mode TEXT NOT NULL DEFAULT 'unknown',
  operation TEXT,
  batch_index INTEGER,
  agent TEXT NOT NULL DEFAULT 'unknown',
  session_hash TEXT NOT NULL,
  file_hash TEXT,
  arg_keys TEXT NOT NULL DEFAULT '[]',
  arg_types TEXT NOT NULL DEFAULT '{}',
  arg_count INTEGER NOT NULL DEFAULT 0,
  unknown_arg_count INTEGER NOT NULL DEFAULT 0,
  error_kind TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_errors_created_at ON tool_errors(created_at);
CREATE INDEX IF NOT EXISTS idx_tool_errors_tool ON tool_errors(tool);
` + "`" + `);
const insert = db.prepare(` + "`" + `INSERT OR IGNORE INTO tool_errors (
  id, created_at, tool, outcome, reason_code, elapsed_ms, mode, operation,
  batch_index, agent, session_hash, file_hash, arg_keys, arg_types, arg_count,
  unknown_arg_count, error_kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)` + "`" + `);
for (const r of JSON.parse(process.env.CT_ROWS)) {
  insert.run(
    r.id, r.createdAt, r.tool, r.outcome, r.reasonCode, r.elapsedMs, r.mode,
    r.operation, r.batchIndex, r.agent, r.sessionHash, r.fileHash, r.argKeys,
    r.argTypes, r.argCount, r.unknownArgCount, r.errorKind,
  );
}
db.close();
`
	env := map[string]string{
		"CAVE_TOOLS_DATA_DIR": s.data,
		"CT_DB":               filepath.Join(s.data, "cave-tools.db"),
		"CT_ROWS":             payload,
	}
	nodeModuleScript(t, s.root, env, script)
}

// requireMode skips a case whose fixture depends on POSIX shells or sockets.
func requireMode(t *testing.T) {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("POSIX fixture")
	}
}

// fixtureWith adds files to the base fixture.
func fixtureWith(extra map[string]fixtureFile) fixtureSpec {
	fixture := baseFixture()
	for name, spec := range extra {
		fixture.Files[name] = spec
	}
	return fixture
}

// jobIDFromStart extracts "jobId: ..." from bash_start output.
func jobIDFromStart(t *testing.T, result outcome) string {
	t.Helper()
	for _, line := range strings.Split(result.stdout, "\n") {
		if value, ok := strings.CutPrefix(line, "jobId: "); ok && value != "" {
			return value
		}
	}
	t.Fatalf("bash_start output has no jobId: stdout=%q stderr=%q", result.stdout, result.stderr)
	return ""
}

// waitJobFinished polls the jobs table until the job reaches a terminal state.
func waitJobFinished(t *testing.T, binary string, prefix []string, cwd string, env map[string]string, jobID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		result := runCommandOutcome(t, binary, prefix, cwd, env, "", "jobs")
		if strings.Contains(result.stdout, jobID) && !strings.Contains(result.stdout, " running ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %q", jobID, result.stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// jsonQuote renders one string as a JSON literal.
func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// mustJSON is a compact helper for seeding payloads.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// formatDBState renders a compact failure message.
func formatDBState(state dbState) string {
	return fmt.Sprintf("sessions=%+v jobs=%+v archives=%+v", state.Sessions, state.Jobs, state.Archives)
}

// hasBudget reports whether any persisted session row carries the given
// maxLines/maxChars budget for tool (the CLI configure tool writes the budget
// into the calling process's session row).
func hasBudget(state dbState, tool string, maxLines, maxChars float64) bool {
	for _, row := range state.Sessions {
		budget, ok := row.Budgets[tool]
		if !ok || budget.MaxLines == nil || budget.MaxChars == nil {
			continue
		}
		if *budget.MaxLines == maxLines && *budget.MaxChars == maxChars {
			return true
		}
	}
	return false
}
