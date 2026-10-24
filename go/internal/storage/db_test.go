package storage

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// resetDB closes the package singleton so the next call re-resolves the data
// dir. TS has the same process-wide singleton; tests need to move it.
func resetDB(t *testing.T) {
	t.Helper()
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}
		db = nil
	}
}

// newDataDir points the storage layer at a private data dir and isolates the
// legacy read-registry path from the developer's ~/.claude.
// TestBudgetConfigJSONStringify mirrors JSON.stringify for budget numbers:
// fractions are preserved, non-finite values become null, and an absent
// maxChars is omitted.
func TestBudgetConfigJSONStringify(t *testing.T) {
	maxChars := -5.5
	cases := []struct {
		name string
		cfg  BudgetConfig
		want string
	}{
		{
			name: "fractions",
			cfg:  BudgetConfig{MaxLines: 12.7, HeadLines: 2.5, TailLines: 1.25, MaxChars: &maxChars},
			want: `{"maxLines":12.7,"headLines":2.5,"tailLines":1.25,"maxChars":-5.5}`,
		},
		{
			name: "nonfinite lines",
			cfg:  BudgetConfig{MaxLines: math.Inf(1), HeadLines: math.NaN(), TailLines: math.Inf(-1)},
			want: `{"maxLines":null,"headLines":null,"tailLines":null}`,
		},
		{
			name: "nonfinite maxChars",
			cfg:  BudgetConfig{MaxLines: 10, HeadLines: 5, TailLines: 5, MaxChars: ptrFloat(math.Inf(1))},
			want: `{"maxLines":10,"headLines":5,"tailLines":5,"maxChars":null}`,
		},
	}
	for _, test := range cases {
		raw, err := json.Marshal(test.cfg)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if string(raw) != test.want {
			t.Errorf("%s = %s, want %s", test.name, raw, test.want)
		}
	}
}

func ptrFloat(value float64) *float64 { return &value }

func newDataDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })
	return dir
}

// mustOpen opens the package database for read-only assertions.
func mustOpen(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	return conn
}

func TestOpenDBCreatesSchema(t *testing.T) {
	newDataDir(t)
	if _, err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}

	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB twice: %v", err)
	}

	wantTables := []string{
		"archive_meta", "bounce_stats", "jobs", "lifetime_baseline",
		"meta", "read_registry", "sessions", "tool_stats",
	}
	gotTables := queryStrings(t, conn, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if !equalStrings(gotTables, wantTables) {
		t.Fatalf("tables = %v, want %v", gotTables, wantTables)
	}

	wantIndexes := []string{
		"idx_archive_created_at", "idx_jobs_started_at", "idx_sessions_ended_at",
	}
	gotIndexes := queryStrings(t, conn,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if !equalStrings(gotIndexes, wantIndexes) {
		t.Fatalf("indexes = %v, want %v", gotIndexes, wantIndexes)
	}

	// Schema text must keep TS's columns and defaults.
	var sessionsSQL string
	if err := conn.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'").Scan(&sessionsSQL); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, fragment := range []string{
		"pid INTEGER PRIMARY KEY",
		"ended_at INTEGER",
		"budgets_json TEXT NOT NULL DEFAULT '{}'",
		"total_wasted_chars INTEGER NOT NULL DEFAULT 0",
	} {
		if !contains(sessionsSQL, fragment) {
			t.Errorf("sessions schema missing %q:\n%s", fragment, sessionsSQL)
		}
	}

	// The legacy migration must be marked done exactly once.
	var marker string
	if err := conn.QueryRow("SELECT value FROM meta WHERE key = 'legacy_migration_v1'").Scan(&marker); err != nil {
		t.Fatalf("legacy marker: %v", err)
	}
	if marker != "done" {
		t.Fatalf("legacy_migration_v1 = %q, want done", marker)
	}
}

func TestOpenDBPragmas(t *testing.T) {
	newDataDir(t)
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}

	var journalMode string
	if err := conn.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var synchronous int64
	if err := conn.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if synchronous != 1 { // NORMAL
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	var busyTimeout int64
	if err := conn.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestOpenDBPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })

	// Pre-create loose dirs and db the way an older install would.
	for _, path := range []string{dir, filepath.Join(dir, "jobs"), filepath.Join(dir, "archives")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}
	dbFile := filepath.Join(dir, "cave-tools.db")
	if err := os.WriteFile(dbFile, nil, 0o644); err != nil {
		t.Fatalf("write db: %v", err)
	}
	if err := os.Chmod(dbFile, 0o644); err != nil {
		t.Fatalf("chmod db: %v", err)
	}

	if _, err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}

	for _, path := range []string{dir, filepath.Join(dir, "jobs"), filepath.Join(dir, "archives")} {
		if got := perm(t, path); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", path, got)
		}
	}
	for _, path := range []string{dbFile, dbFile + "-wal", dbFile + "-shm"} {
		if got := perm(t, path); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, got)
		}
	}
}

func TestSessionStatsRoundTrip(t *testing.T) {
	newDataDir(t)
	maxChars := float64(4000)
	stats := PersistedStats{
		Pid:          1234,
		SessionStart: 1_000,
		UpdatedAt:    2_000,
		Cache: CacheStats{
			Hits:         3,
			Misses:       1,
			FilesTracked: 7,
		},
		Savings: SavingsStats{
			TotalCalls:            10,
			RawChars:              1000,
			CompressedChars:       400,
			CompressionSavedChars: 500,
			DedupSavedChars:       100,
			SavedChars:            10, // 10/4 = 2.5 -> Math.round -> 3
			ByTool: map[string]ToolSavings{
				"cave__read": {Calls: 6, RawChars: 600, CompressedChars: 200, SavedChars: 400},
				"cave__grep": {Calls: 4, RawChars: 400, CompressedChars: 200, SavedChars: 200},
			},
		},
		Rtk: RtkStats{Rewrites: 5, AlreadyWrapped: 2, Passthrough: 1},
		Budgets: map[string]BudgetConfig{
			"cave__read": {MaxLines: 2000, HeadLines: 800, TailLines: 800, MaxChars: &maxChars},
			"cave__grep": {MaxLines: 100, HeadLines: 40, TailLines: 40},
		},
		Bounces: BounceSummary{
			TotalBounces:     2,
			TotalWastedChars: 300,
			ByExtension: map[string]BounceStats{
				".ts": {TotalReads: 4, Bounces: 2, WastedChars: 300},
			},
		},
	}

	if err := UpsertSessionStats(stats); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}

	sessions, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	got := sessions[0]
	if got.Pid != stats.Pid || got.SessionStart != stats.SessionStart || got.UpdatedAt != stats.UpdatedAt {
		t.Errorf("identity fields = %d/%d/%d", got.Pid, got.SessionStart, got.UpdatedAt)
	}
	if got.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil", *got.EndedAt)
	}
	if got.Cache.Hits != 3 || got.Cache.Misses != 1 || got.Cache.Total != 4 {
		t.Errorf("cache = %+v", got.Cache)
	}
	if got.Cache.HitRate != 0.75 {
		t.Errorf("hitRate = %v, want 0.75", got.Cache.HitRate)
	}
	if got.Cache.FilesTracked != 7 {
		t.Errorf("filesTracked = %d", got.Cache.FilesTracked)
	}
	if got.Savings.EstimatedTokensSaved != 3 {
		t.Errorf("estimatedTokensSaved = %d, want 3 (Math.round(10/4))", got.Savings.EstimatedTokensSaved)
	}
	if len(got.Savings.ByTool) != 2 || got.Savings.ByTool["cave__read"].SavedChars != 400 {
		t.Errorf("byTool = %+v", got.Savings.ByTool)
	}
	if got.Rtk != stats.Rtk {
		t.Errorf("rtk = %+v", got.Rtk)
	}
	if len(got.Budgets) != 2 {
		t.Fatalf("budgets = %+v", got.Budgets)
	}
	if got.Budgets["cave__read"].MaxChars == nil || *got.Budgets["cave__read"].MaxChars != 4000 {
		t.Errorf("budgets maxChars lost: %+v", got.Budgets["cave__read"])
	}
	if got.Budgets["cave__grep"].MaxChars != nil {
		t.Errorf("unset maxChars must stay nil: %+v", got.Budgets["cave__grep"])
	}
	if got.Bounces.ByExtension[".ts"].WastedChars != 300 {
		t.Errorf("byExtension = %+v", got.Bounces.ByExtension)
	}

	// Tool and bounce rows are rewritten, never accumulated.
	stats.Savings.ByTool = map[string]ToolSavings{"cave__read": {Calls: 1}}
	stats.Bounces.ByExtension = map[string]BounceStats{}
	if err := UpsertSessionStats(stats); err != nil {
		t.Fatalf("second UpsertSessionStats: %v", err)
	}
	sessions, err = ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	if len(sessions[0].Savings.ByTool) != 1 {
		t.Errorf("tool rows accumulated: %+v", sessions[0].Savings.ByTool)
	}
	if len(sessions[0].Bounces.ByExtension) != 0 {
		t.Errorf("bounce rows accumulated: %+v", sessions[0].Bounces.ByExtension)
	}
}

func TestSessionStatsNilBudgetsSerialiseAsEmptyObject(t *testing.T) {
	newDataDir(t)
	if err := UpsertSessionStats(PersistedStats{Pid: 1, Savings: SavingsStats{}}); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	var raw string
	if err := conn.QueryRow("SELECT budgets_json FROM sessions WHERE pid = 1").Scan(&raw); err != nil {
		t.Fatalf("budgets_json: %v", err)
	}
	if raw != "{}" {
		t.Fatalf("budgets_json = %q, want {}", raw)
	}
}

func TestMarkSessionEnded(t *testing.T) {
	newDataDir(t)
	if err := UpsertSessionStats(PersistedStats{Pid: 7}); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}

	changed, err := MarkSessionEnded(7, 123_456)
	if err != nil {
		t.Fatalf("MarkSessionEnded: %v", err)
	}
	if !changed {
		t.Fatal("first MarkSessionEnded reported no change")
	}
	changed, err = MarkSessionEnded(7, 999_999)
	if err != nil {
		t.Fatalf("second MarkSessionEnded: %v", err)
	}
	if changed {
		t.Fatal("second MarkSessionEnded must not overwrite ended_at")
	}

	all, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats(true): %v", err)
	}
	if len(all) != 1 || all[0].EndedAt == nil || *all[0].EndedAt != 123_456 {
		t.Fatalf("endedAt = %+v", all)
	}
	live, err := ListSessionStats(false)
	if err != nil {
		t.Fatalf("ListSessionStats(false): %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live sessions = %d, want 0", len(live))
	}

	// Upserting a live session keeps the ended marker (COALESCE).
	if err := UpsertSessionStats(PersistedStats{Pid: 7, UpdatedAt: 5}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all, err = ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats(true): %v", err)
	}
	if all[0].EndedAt == nil || *all[0].EndedAt != 123_456 {
		t.Fatalf("ended_at lost on upsert: %+v", all[0].EndedAt)
	}
}

func TestLifetimeStatsBaselineAndEndedSessions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	baseline := LifetimeStats{
		BankedSessions:   2,
		Calls:            10,
		RawChars:         100,
		CompressedChars:  40,
		SavedChars:       60,
		Hits:             5,
		Misses:           1,
		RtkRewrites:      3,
		RtkPassthrough:   2,
		UpdatedAt:        1_000,
		TotalBounces:     1,
		TotalWastedChars: 25,
	}
	writeJSON(t, filepath.Join(dir, "lifetime.json"), baseline)

	if _, err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}

	ended := PersistedStats{
		Pid:       42,
		UpdatedAt: 2_000,
		Cache:     CacheStats{Hits: 2, Misses: 2},
		Savings: SavingsStats{
			TotalCalls:            4,
			RawChars:              200,
			CompressedChars:       80,
			CompressionSavedChars: 90,
			DedupSavedChars:       30,
			SavedChars:            120,
		},
		Rtk:     RtkStats{Rewrites: 1, AlreadyWrapped: 1},
		Bounces: BounceSummary{TotalBounces: 3, TotalWastedChars: 9},
	}
	if err := UpsertSessionStats(ended); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	if _, err := MarkSessionEnded(42, 2_500); err != nil {
		t.Fatalf("MarkSessionEnded: %v", err)
	}

	got, err := GetLifetimeStats()
	if err != nil {
		t.Fatalf("GetLifetimeStats: %v", err)
	}
	want := LifetimeStats{
		BankedSessions:        3,
		Calls:                 14,
		RawChars:              300,
		CompressedChars:       120,
		CompressionSavedChars: 90,
		DedupSavedChars:       30,
		SavedChars:            180,
		Hits:                  7,
		Misses:                3,
		RtkRewrites:           4,
		RtkAlreadyWrapped:     1,
		RtkPassthrough:        2,
		UpdatedAt:             2_000,
		TotalBounces:          4,
		TotalWastedChars:      34,
	}
	if got != want {
		t.Fatalf("GetLifetimeStats = %+v\nwant %+v", got, want)
	}
}

func TestRegisterReadPath(t *testing.T) {
	newDataDir(t)
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}

	RegisterReadPath("/tmp/a.ts", nil)
	var (
		hash       *string
		lastReadAt int64
	)
	if err := conn.QueryRow("SELECT hash, last_read_at FROM read_registry WHERE path = ?", "/tmp/a.ts").
		Scan(&hash, &lastReadAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if hash != nil {
		t.Fatalf("hash = %q, want NULL", *hash)
	}
	if lastReadAt == 0 {
		t.Fatal("last_read_at was not stamped")
	}

	digest := "abc123"
	RegisterReadPath("/tmp/a.ts", &digest)
	var stored *string
	var updated int64
	if err := conn.QueryRow("SELECT hash, last_read_at FROM read_registry WHERE path = ?", "/tmp/a.ts").
		Scan(&stored, &updated); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if stored == nil || *stored != digest {
		t.Fatalf("hash = %v, want abc123", stored)
	}

	// A later hash-less read keeps the known hash (COALESCE).
	RegisterReadPath("/tmp/a.ts", nil)
	if err := conn.QueryRow("SELECT hash FROM read_registry WHERE path = ?", "/tmp/a.ts").Scan(&stored); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if stored == nil || *stored != digest {
		t.Fatalf("hash = %v, want it preserved", stored)
	}

	var count int64
	if err := conn.QueryRow("SELECT COUNT(*) FROM read_registry").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("read_registry rows = %d, want 1", count)
	}
}

func TestArchives(t *testing.T) {
	newDataDir(t)
	entries := []ArchiveEntry{
		{ID: "aa1", Command: "seq 1 300", SizeChars: 100, CreatedAt: 1_000, ContentPath: "/tmp/aa/aa1.txt"},
		{ID: "bb2", Command: "ls", SizeChars: 250, CreatedAt: 2_000, ContentPath: "/tmp/bb/bb2.txt"},
	}
	for _, entry := range entries {
		if err := RecordArchive(entry); err != nil {
			t.Fatalf("RecordArchive: %v", err)
		}
	}

	// INSERT OR REPLACE on the same id.
	if err := RecordArchive(ArchiveEntry{ID: "aa1", Command: "seq 1 400", SizeChars: 200, CreatedAt: 3_000, ContentPath: "/tmp/aa/aa1.txt"}); err != nil {
		t.Fatalf("RecordArchive replace: %v", err)
	}

	archives, err := ListArchives()
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	if len(archives) != 2 {
		t.Fatalf("archives = %d, want 2", len(archives))
	}
	if archives[0].ID != "aa1" || archives[0].SizeChars != 200 {
		t.Fatalf("newest first with replace: %+v", archives)
	}

	totals, err := ArchiveStats()
	if err != nil {
		t.Fatalf("ArchiveStats: %v", err)
	}
	if totals.Count != 2 || totals.TotalChars != 450 {
		t.Fatalf("ArchiveStats = %+v, want {2 450}", totals)
	}

	if err := DeleteArchive("aa1"); err != nil {
		t.Fatalf("DeleteArchive: %v", err)
	}
	totals, err = ArchiveStats()
	if err != nil {
		t.Fatalf("ArchiveStats: %v", err)
	}
	if totals.Count != 1 || totals.TotalChars != 250 {
		t.Fatalf("ArchiveStats after delete = %+v", totals)
	}
}

func TestArchiveStatsEmpty(t *testing.T) {
	newDataDir(t)
	totals, err := ArchiveStats()
	if err != nil {
		t.Fatalf("ArchiveStats: %v", err)
	}
	if totals.Count != 0 || totals.TotalChars != 0 {
		t.Fatalf("empty ArchiveStats = %+v", totals)
	}
}

func TestJobs(t *testing.T) {
	newDataDir(t)
	workdir := "/tmp/work"
	signal := "SIGTERM"
	exitCode := int64(0)
	endedAt := int64(2_500)

	jobs := []JobRecord{
		{JobID: "j1", Pid: 11, SessionID: "s1", Command: "sleep 5", Workdir: &workdir, StartedAt: 1_000, State: JobRunning, LogPath: "/tmp/j1.log"},
		{JobID: "j2", Pid: 12, SessionID: "s2", Command: "true", StartedAt: 2_000, EndedAt: &endedAt, ExitCode: &exitCode, Signal: &signal, State: JobExited, LogPath: "/tmp/j2.log"},
	}
	for _, job := range jobs {
		if err := InsertJob(job); err != nil {
			t.Fatalf("InsertJob %s: %v", job.JobID, err)
		}
	}

	got, err := GetJob("j1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got == nil || got.JobID != "j1" || got.Workdir == nil || *got.Workdir != workdir {
		t.Fatalf("GetJob = %+v", got)
	}
	if got.EndedAt != nil || got.ExitCode != nil || got.Signal != nil {
		t.Fatalf("running job must have nil completion fields: %+v", got)
	}
	if got.State != JobRunning {
		t.Fatalf("state = %q", got.State)
	}

	missing, err := GetJob("nope")
	if err != nil {
		t.Fatalf("GetJob missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("GetJob missing = %+v, want nil", missing)
	}

	all, err := ListJobs(nil)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 2 || all[0].JobID != "j2" || all[1].JobID != "j1" {
		t.Fatalf("ListJobs order = %+v", all)
	}

	session := "s2"
	bySession, err := ListJobs(&session)
	if err != nil {
		t.Fatalf("ListJobs(session): %v", err)
	}
	if len(bySession) != 1 || bySession[0].JobID != "j2" {
		t.Fatalf("ListJobs(s2) = %+v", bySession)
	}

	running, err := ListRunningJobs()
	if err != nil {
		t.Fatalf("ListRunningJobs: %v", err)
	}
	if len(running) != 1 || running[0].JobID != "j1" {
		t.Fatalf("ListRunningJobs = %+v", running)
	}

	// Only the fields the caller provides are written.
	killedAt := int64(3_000)
	if err := UpdateJobState("j1", JobUpdate{State: JobKilled, EndedAt: Some(killedAt), Signal: Some(signal)}); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}
	got, err = GetJob("j1")
	if err != nil {
		t.Fatalf("GetJob after update: %v", err)
	}
	if got.State != JobKilled || got.EndedAt == nil || *got.EndedAt != killedAt {
		t.Fatalf("updated job = %+v", got)
	}
	if got.ExitCode != nil {
		t.Fatalf("exitCode = %v, want untouched NULL", *got.ExitCode)
	}

	// A state-only update keeps the recorded completion fields (the TS bug).
	if err := UpdateJobState("j2", JobUpdate{State: JobLost}); err != nil {
		t.Fatalf("UpdateJobState state-only: %v", err)
	}
	got, err = GetJob("j2")
	if err != nil {
		t.Fatalf("GetJob j2: %v", err)
	}
	if got.State != JobLost {
		t.Fatalf("state-only update state = %q, want lost", got.State)
	}
	if got.EndedAt == nil || *got.EndedAt != endedAt || got.ExitCode == nil || *got.ExitCode != exitCode ||
		got.Signal == nil || *got.Signal != signal {
		t.Fatalf("state-only update erased completion fields: %+v", got)
	}

	// An explicit NULL still clears a column.
	if err := UpdateJobState("j2", JobUpdate{State: JobExited, ExitCode: Null[int64]()}); err != nil {
		t.Fatalf("UpdateJobState null exit code: %v", err)
	}
	got, err = GetJob("j2")
	if err != nil {
		t.Fatalf("GetJob j2 after null: %v", err)
	}
	if got.ExitCode != nil || got.Signal == nil {
		t.Fatalf("explicit NULL did not clear only exit_code: %+v", got)
	}

	endedBefore, err := ListJobsEndedBefore(10_000)
	if err != nil {
		t.Fatalf("ListJobsEndedBefore: %v", err)
	}
	if len(endedBefore) != 2 {
		t.Fatalf("ListJobsEndedBefore = %+v", endedBefore)
	}
	endedBefore, err = ListJobsEndedBefore(1_500)
	if err != nil {
		t.Fatalf("ListJobsEndedBefore: %v", err)
	}
	if len(endedBefore) != 1 || endedBefore[0].JobID != "j1" {
		t.Fatalf("ListJobsEndedBefore(1500) = %+v", endedBefore)
	}

	if err := DeleteJob("j1"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	all, err = ListJobs(nil)
	if err != nil {
		t.Fatalf("ListJobs after delete: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("jobs after delete = %+v", all)
	}
}

func TestLegacyMigration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })

	// Legacy lifetime.json.
	writeJSON(t, filepath.Join(dir, "lifetime.json"), LifetimeStats{
		BankedSessions: 1,
		Calls:          3,
		SavedChars:     30,
		UpdatedAt:      500,
	})

	// Legacy sessions/*.json, one valid and one corrupt.
	session := PersistedStats{
		Pid:          99,
		SessionStart: 10,
		UpdatedAt:    20,
		Cache:        CacheStats{Hits: 1, Misses: 1, FilesTracked: 2},
		Savings: SavingsStats{
			TotalCalls: 2,
			RawChars:   80,
			SavedChars: 40,
			ByTool:     map[string]ToolSavings{"cave__ls": {Calls: 2, RawChars: 80, SavedChars: 40}},
		},
		Budgets: map[string]BudgetConfig{"cave__ls": {MaxLines: 10, HeadLines: 4, TailLines: 4}},
		Bounces: BounceSummary{ByExtension: map[string]BounceStats{".go": {TotalReads: 1, Bounces: 1, WastedChars: 9}}},
	}
	writeJSON(t, filepath.Join(dir, "sessions", "99.json"), session)
	if err := os.WriteFile(filepath.Join(dir, "sessions", "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write non-json: %v", err)
	}

	// Legacy read-registry.txt with blank lines and padding.
	registryFile := filepath.Join(claudeDir, "cave-tools", "read-registry.txt")
	if err := os.MkdirAll(filepath.Dir(registryFile), 0o700); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	// The BOM/NEL lines are the Muse regression for migrateReadRegistry's
	// line.trim(): JS trim strips U+FEFF and keeps U+0085.
	if err := os.WriteFile(registryFile, []byte("  /tmp/one.ts  \n\n/tmp/two.ts\n\ufeff/tmp/three.ts\n/tmp/four.ts\u0085\n"), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	// Legacy archives/<prefix>/<id>.meta.json plus one corrupt and one file.
	metaDir := filepath.Join(dir, "archives", "ab")
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	writeJSON(t, filepath.Join(metaDir, "abc123.meta.json"), map[string]any{
		"id": "abc123", "command": "seq 1 300", "sizeChars": 700, "createdAt": 1_700,
	})
	if err := os.WriteFile(filepath.Join(metaDir, "corrupt.meta.json"), []byte("nope"), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "archives", "loose.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write loose file: %v", err)
	}

	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}

	lifetime, err := GetLifetimeStats()
	if err != nil {
		t.Fatalf("GetLifetimeStats: %v", err)
	}
	// Baseline (1 session, 3 calls) plus the migrated session, which is not
	// ended so it does not add to the lifetime totals here.
	if lifetime.BankedSessions != 1 || lifetime.Calls != 3 || lifetime.SavedChars != 30 {
		t.Fatalf("lifetime baseline = %+v", lifetime)
	}

	sessions, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Pid != 99 {
		t.Fatalf("migrated sessions = %+v", sessions)
	}
	if sessions[0].Savings.ByTool["cave__ls"].SavedChars != 40 {
		t.Fatalf("migrated byTool = %+v", sessions[0].Savings.ByTool)
	}
	if sessions[0].Budgets["cave__ls"].MaxLines != 10 {
		t.Fatalf("migrated budgets = %+v", sessions[0].Budgets)
	}
	if sessions[0].Bounces.ByExtension[".go"].WastedChars != 9 {
		t.Fatalf("migrated bounces = %+v", sessions[0].Bounces.ByExtension)
	}

	paths := queryStrings(t, conn, "SELECT path FROM read_registry ORDER BY path")
	if !equalStrings(paths, []string{"/tmp/four.ts\u0085", "/tmp/one.ts", "/tmp/three.ts", "/tmp/two.ts"}) {
		t.Fatalf("migrated read_registry = %v", paths)
	}

	archives, err := ListArchives()
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	wantArchive := ArchiveEntry{
		ID:          "abc123",
		Command:     "seq 1 300",
		SizeChars:   700,
		CreatedAt:   1_700,
		ContentPath: filepath.Join(metaDir, "abc123.txt"),
	}
	if len(archives) != 1 || archives[0] != wantArchive {
		t.Fatalf("migrated archives = %+v, want %+v", archives, wantArchive)
	}

	// Re-opening must not duplicate rows: the meta marker short-circuits it.
	resetDB(t)
	if _, err := openDB(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	sessions, err = ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats after reopen: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("migration re-ran: %d sessions", len(sessions))
	}
	archives, err = ListArchives()
	if err != nil {
		t.Fatalf("ListArchives after reopen: %v", err)
	}
	if len(archives) != 1 {
		t.Fatalf("archive migration re-ran: %+v", archives)
	}
}

func TestBudgetsJSONFromTSParses(t *testing.T) {
	newDataDir(t)
	if err := UpsertSessionStats(PersistedStats{Pid: 5}); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	// Exactly what TS JSON.stringify writes for a parsed budget map.
	tsJSON := `{"cave__read":{"maxLines":2000,"headLines":800,"tailLines":800,"maxChars":4000},"cave__bash":{"maxLines":100,"headLines":40,"tailLines":40}}`
	if _, err := conn.Exec("UPDATE sessions SET budgets_json = ? WHERE pid = 5", tsJSON); err != nil {
		t.Fatalf("update budgets: %v", err)
	}

	sessions, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	budgets := sessions[0].Budgets
	if budgets["cave__read"].MaxChars == nil || *budgets["cave__read"].MaxChars != 4000 {
		t.Fatalf("maxChars = %+v", budgets["cave__read"])
	}
	if budgets["cave__bash"].TailLines != 40 {
		t.Fatalf("tailLines = %+v", budgets["cave__bash"])
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func queryStrings(t *testing.T, conn *sql.DB, query string) []string {
	t.Helper()
	rows, err := conn.Query(query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	slices.Sort(values)
	return values
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func equalStrings(a, b []string) bool { return slices.Equal(a, b) }

// contains is strings.Contains with a readable failure message in the caller.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// TestLegacyMigrationNormalizesStats ports the TS fix: older session files may
// lack sections (budgets, rtk, bounces, ...) or hold nulls, and they now
// migrate with {} / 0 instead of being dropped. A missing or non-numeric pid
// still skips the file, exactly like Number.isFinite(raw.pid).
func TestLegacyMigrationNormalizesStats(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })

	writeRaw := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, "sessions", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// The TS port-bugs fixture: no budgets/rtk/bounces/byTool, null leaf.
	writeRaw("4242.json", `{"pid":4242,"sessionStart":1,"updatedAt":2,"cache":{"hits":1,"misses":null},"savings":{"totalCalls":3}}`)
	// A numeric pid with every section missing migrates with zeros.
	writeRaw("4243.json", `{"pid":4243}`)
	// String-typed leaves become 0 like TS's num(v); they do not skip the file.
	writeRaw("4245.json", `{"pid":4245,"cache":{"hits":"5"},"rtk":"nope"}`)
	// Files without a finite numeric pid are skipped.
	writeRaw("no-pid.json", `{"sessionStart":1}`)
	writeRaw("string-pid.json", `{"pid":"4244"}`)
	writeRaw("null-pid.json", `{"pid":null}`)
	writeRaw("array.json", `[1,2]`)
	writeRaw("broken.json", `{`)

	rows, err := ListSessionStats(true)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	byPid := map[int64]SessionStatsRow{}
	for _, row := range rows {
		byPid[row.Pid] = row
	}
	if len(byPid) != 3 {
		pids := make([]int64, 0, len(byPid))
		for pid := range byPid {
			pids = append(pids, pid)
		}
		slices.Sort(pids)
		t.Fatalf("migrated pids = %v, want [4242 4243 4245]", pids)
	}

	full := byPid[4242]
	if full.Budgets == nil || len(full.Budgets) != 0 {
		t.Errorf("4242 budgets = %#v, want {}", full.Budgets)
	}
	if full.Cache.Hits != 1 || full.Cache.Misses != 0 {
		t.Errorf("4242 cache = %+v", full.Cache)
	}
	if full.Savings.TotalCalls != 3 || full.Rtk.Rewrites != 0 || full.Bounces.TotalBounces != 0 {
		t.Errorf("4242 = %+v", full)
	}
	if full.SessionStart != 1 || full.UpdatedAt != 2 {
		t.Errorf("4242 timestamps = %d/%d", full.SessionStart, full.UpdatedAt)
	}
	if got := byPid[4242].Savings.ByTool; got == nil || len(got) != 0 {
		t.Errorf("4242 byTool = %#v, want {}", got)
	}

	empty := byPid[4243]
	if empty.Cache.Hits != 0 || empty.Savings.TotalCalls != 0 || empty.UpdatedAt != 0 {
		t.Errorf("4243 = %+v", empty)
	}
	if stringStats := byPid[4245]; stringStats.Cache.Hits != 0 || stringStats.Rtk.Rewrites != 0 {
		t.Errorf("4245 string leaves = %+v", stringStats)
	}

	// budgets_json is an object, never NULL.
	conn, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	budgets := queryStrings(t, conn, "SELECT budgets_json FROM sessions WHERE pid = 4242")
	if len(budgets) != 1 || budgets[0] != "{}" {
		t.Fatalf("budgets_json = %v, want [{}]", budgets)
	}
}

// TS readdirSync throws ENOTDIR when sessions exists as a regular file, which
// fails openDb; Go must surface the error too instead of skipping it.
func TestLegacyMigrationSessionsPathNotDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetDB(t)
	t.Cleanup(func() { resetDB(t) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDB(); err == nil {
		t.Fatal("openDB succeeded with sessions as a regular file; TS fails with ENOTDIR")
	}
}
