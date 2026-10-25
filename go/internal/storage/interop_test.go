package storage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// interopDeps locates the built TS tree and node. Both the Go and TS
// implementations must read each other's rows in the same data dir, so these
// tests skip (loudly) when the reference build is missing.
func interopDeps(t *testing.T) (nodePath, distDB string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; skipping TS/Go SQLite interop test")
	}
	distDB = filepath.Join(repoRoot(t), "dist", "storage", "db.js")
	if _, err := os.Stat(distDB); err != nil {
		t.Skip("dist/storage/db.js not built: run `pnpm build` first, then re-run")
	}
	return nodePath, distDB
}

// repoRoot is go/internal/storage -> repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// runNode executes an ES module snippet with the TS storage layer importable
// through CAVE_DIST_DB and returns its stdout.
func runNode(t *testing.T, nodePath, distDB, dataDir, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, nodePath, "--input-type=module", "-e", script)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(testenv.Environ(),
		"CAVE_TOOLS_DATA_DIR="+dataDir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(filepath.Dir(dataDir), "claude"),
		"CAVE_DIST_DB="+distDB,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node script failed: %v\n%s", err, output)
	}
	if ctx.Err() != nil {
		t.Fatalf("node script timed out: %v", ctx.Err())
	}
	return string(output)
}

// interopSession returns a session row that exercises every nested map.
func interopSession(pid int64) PersistedStats {
	maxChars := float64(4000)
	return PersistedStats{
		Pid:          pid,
		SessionStart: 111,
		UpdatedAt:    222,
		Cache:        CacheStats{Hits: 3, Misses: 1, FilesTracked: 2},
		Savings: SavingsStats{
			TotalCalls:            9,
			RawChars:              900,
			CompressedChars:       400,
			CompressionSavedChars: 300,
			DedupSavedChars:       200,
			SavedChars:            500,
			ByTool: map[string]ToolSavings{
				"cave__read": {Calls: 5, RawChars: 500, CompressedChars: 200, SavedChars: 300},
				"cave__grep": {Calls: 4, RawChars: 400, CompressedChars: 200, SavedChars: 200},
			},
		},
		Rtk: RtkStats{Rewrites: 2, AlreadyWrapped: 1, Passthrough: 3},
		Budgets: map[string]BudgetConfig{
			"cave__read": {MaxLines: 2000, HeadLines: 800, TailLines: 800, MaxChars: &maxChars},
		},
		Bounces: BounceSummary{
			TotalBounces:     1,
			TotalWastedChars: 25,
			ByExtension:      map[string]BounceStats{".ts": {TotalReads: 2, Bounces: 1, WastedChars: 25}},
		},
	}
}

// TestInteropGoWritesNodeReads creates the DB with the Go port and proves the
// TypeScript storage layer reads the same rows.
func TestInteropGoWritesNodeReads(t *testing.T) {
	nodePath, distDB := interopDeps(t)
	dataDir := newDataDir(t)

	stats := interopSession(4242)
	if err := UpsertSessionStats(stats); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	if err := RecordArchive(ArchiveEntry{
		ID: "go1", Command: "seq 1 300", SizeChars: 700, CreatedAt: 1_700,
		ContentPath: filepath.Join(dataDir, "archives", "go", "go1.txt"),
	}); err != nil {
		t.Fatalf("RecordArchive: %v", err)
	}
	workdir := "/tmp/from-go"
	if err := InsertJob(JobRecord{
		JobID: "gj1", Pid: 77, SessionID: "s-go", Command: "sleep 5",
		Workdir: &workdir, StartedAt: 10, State: JobRunning, LogPath: "/tmp/gj1.log",
	}); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
	if _, err := MarkSessionEnded(4242, 333); err != nil {
		t.Fatalf("MarkSessionEnded: %v", err)
	}

	script := `
const { pathToFileURL } = await import("node:url");
const db = await import(pathToFileURL(process.env.CAVE_DIST_DB).href);
const out = {
  sessions: db.listSessionStats(true).map((s) => ({
    pid: s.pid,
    sessionStart: s.sessionStart,
    updatedAt: s.updatedAt,
    endedAt: s.endedAt,
    hitRate: s.cache.hitRate,
    filesTracked: s.cache.filesTracked,
    totalCalls: s.savings.totalCalls,
    savedChars: s.savings.savedChars,
    estimatedTokensSaved: s.savings.estimatedTokensSaved,
    byTool: s.savings.byTool,
    rtk: s.rtk,
    budgets: s.budgets,
    bounces: s.bounces,
  })),
  lifetime: db.getLifetimeStats(),
  archives: db.listArchives(),
  jobs: db.listJobs(),
};
console.log(JSON.stringify(out));
`
	stdout := runNode(t, nodePath, distDB, dataDir, script)

	var got struct {
		Sessions []struct {
			Pid                  int64                   `json:"pid"`
			SessionStart         int64                   `json:"sessionStart"`
			UpdatedAt            int64                   `json:"updatedAt"`
			EndedAt              *int64                  `json:"endedAt"`
			HitRate              float64                 `json:"hitRate"`
			FilesTracked         int64                   `json:"filesTracked"`
			TotalCalls           int64                   `json:"totalCalls"`
			SavedChars           int64                   `json:"savedChars"`
			EstimatedTokensSaved int64                   `json:"estimatedTokensSaved"`
			ByTool               map[string]ToolSavings  `json:"byTool"`
			Rtk                  RtkStats                `json:"rtk"`
			Budgets              map[string]BudgetConfig `json:"budgets"`
			Bounces              BounceSummary           `json:"bounces"`
		} `json:"sessions"`
		Lifetime LifetimeStats  `json:"lifetime"`
		Archives []ArchiveEntry `json:"archives"`
		Jobs     []JobRecord    `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode node output: %v\n%s", err, stdout)
	}

	if len(got.Sessions) != 1 {
		t.Fatalf("TS saw %d sessions, want 1\n%s", len(got.Sessions), stdout)
	}
	session := got.Sessions[0]
	if session.Pid != 4242 || session.SessionStart != 111 || session.UpdatedAt != 222 {
		t.Errorf("TS read identity fields %+v", session)
	}
	if session.EndedAt == nil || *session.EndedAt != 333 {
		t.Errorf("TS read endedAt = %v, want 333", session.EndedAt)
	}
	if session.HitRate != 0.75 || session.FilesTracked != 2 {
		t.Errorf("TS read cache = hitRate %v filesTracked %d", session.HitRate, session.FilesTracked)
	}
	if session.TotalCalls != 9 || session.SavedChars != 500 || session.EstimatedTokensSaved != 125 {
		t.Errorf("TS read savings %+v", session)
	}
	if session.ByTool["cave__read"].SavedChars != 300 || session.ByTool["cave__grep"].Calls != 4 {
		t.Errorf("TS read byTool %+v", session.ByTool)
	}
	if session.Rtk != stats.Rtk {
		t.Errorf("TS read rtk %+v, want %+v", session.Rtk, stats.Rtk)
	}
	if session.Budgets["cave__read"].MaxChars == nil || *session.Budgets["cave__read"].MaxChars != 4000 {
		t.Errorf("TS read budgets %+v", session.Budgets)
	}
	if session.Bounces.ByExtension[".ts"].WastedChars != 25 {
		t.Errorf("TS read bounces %+v", session.Bounces)
	}

	if got.Lifetime.BankedSessions != 1 || got.Lifetime.Calls != 9 || got.Lifetime.SavedChars != 500 {
		t.Errorf("TS read lifetime %+v", got.Lifetime)
	}
	if len(got.Archives) != 1 || got.Archives[0].ID != "go1" || got.Archives[0].SizeChars != 700 {
		t.Errorf("TS read archives %+v", got.Archives)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].JobID != "gj1" || got.Jobs[0].State != JobRunning {
		t.Errorf("TS read jobs %+v", got.Jobs)
	}
	if got.Jobs[0].Workdir == nil || *got.Jobs[0].Workdir != workdir {
		t.Errorf("TS read job workdir %+v", got.Jobs[0].Workdir)
	}
}

// TestInteropNodeWritesGoReads is the reverse direction: the TypeScript
// storage layer creates the DB, the Go port opens it and reads every table.
func TestInteropNodeWritesGoReads(t *testing.T) {
	nodePath, distDB := interopDeps(t)

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dataDir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })

	script := `
const { pathToFileURL } = await import("node:url");
const db = await import(pathToFileURL(process.env.CAVE_DIST_DB).href);
db.upsertSessionStats({
  pid: 5151,
  sessionStart: 10,
  updatedAt: 20,
  cache: { hits: 2, misses: 2, total: 4, hitRate: 0.5, filesTracked: 3 },
  savings: {
    totalCalls: 6,
    rawChars: 600,
    compressedChars: 300,
    compressionSavedChars: 100,
    dedupSavedChars: 200,
    savedChars: 300,
    estimatedTokensSaved: 75,
    byTool: { cave__bash: { calls: 6, rawChars: 600, compressedChars: 300, savedChars: 300 } },
  },
  rtk: { rewrites: 4, alreadyWrapped: 1, passthrough: 0 },
  budgets: { cave__bash: { maxLines: 100, headLines: 40, tailLines: 40, maxChars: 2000 } },
  bounces: {
    totalBounces: 1,
    totalWastedChars: 12,
    byExtension: { ".go": { totalReads: 2, bounces: 1, wastedChars: 12 } },
  },
});
db.recordArchive({ id: "ts1", command: "pnpm test", sizeChars: 999, createdAt: 42, contentPath: "/tmp/ts1.txt" });
db.insertJob({
  jobId: "tj1", pid: 88, sessionId: "s-ts", command: "pnpm build",
  workdir: null, startedAt: 30, endedAt: null, exitCode: null, signal: null,
  state: "running", logPath: "/tmp/tj1.log",
});
db.registerReadPath("/tmp/ts-file.ts", "deadbeef");
console.log("ok");
`
	if out := runNode(t, nodePath, distDB, dataDir, script); out == "" {
		t.Fatal("node produced no output")
	}

	sessions, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Go saw %d sessions, want 1", len(sessions))
	}
	session := sessions[0].PersistedStats
	if session.Pid != 5151 || session.SessionStart != 10 || session.UpdatedAt != 20 {
		t.Errorf("Go read identity fields %+v", session)
	}
	if session.Cache.HitRate != 0.5 || session.Cache.Total != 4 || session.Cache.FilesTracked != 3 {
		t.Errorf("Go read cache %+v", session.Cache)
	}
	if session.Savings.SavedChars != 300 || session.Savings.EstimatedTokensSaved != 75 {
		t.Errorf("Go read savings %+v", session.Savings)
	}
	if session.Savings.ByTool["cave__bash"].Calls != 6 {
		t.Errorf("Go read byTool %+v", session.Savings.ByTool)
	}
	if session.Rtk.Rewrites != 4 || session.Bounces.ByExtension[".go"].WastedChars != 12 {
		t.Errorf("Go read rtk/bounces %+v %+v", session.Rtk, session.Bounces)
	}
	if session.Budgets["cave__bash"].MaxChars == nil || *session.Budgets["cave__bash"].MaxChars != 2000 {
		t.Errorf("Go read budgets %+v", session.Budgets)
	}

	archives, err := ListArchives()
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	if len(archives) != 1 || archives[0] != (ArchiveEntry{
		ID: "ts1", Command: "pnpm test", SizeChars: 999, CreatedAt: 42, ContentPath: "/tmp/ts1.txt",
	}) {
		t.Errorf("Go read archives %+v", archives)
	}

	jobs, err := ListJobs(nil)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].JobID != "tj1" || jobs[0].State != JobRunning {
		t.Errorf("Go read jobs %+v", jobs)
	}
	if jobs[0].Workdir != nil || jobs[0].EndedAt != nil || jobs[0].ExitCode != nil || jobs[0].Signal != nil {
		t.Errorf("Go read null job columns %+v", jobs[0])
	}

	conn := mustOpen(t)
	var hash string
	if err := conn.QueryRow("SELECT hash FROM read_registry WHERE path = '/tmp/ts-file.ts'").Scan(&hash); err != nil {
		t.Fatalf("read_registry from TS: %v", err)
	}
	if hash != "deadbeef" {
		t.Errorf("Go read read_registry hash %q", hash)
	}
}
