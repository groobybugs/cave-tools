package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// ToolSavings is one tool's savings bucket.
// port of src/storage/db.ts:ToolSavings
type ToolSavings struct {
	Calls           int64 `json:"calls"`
	RawChars        int64 `json:"rawChars"`
	CompressedChars int64 `json:"compressedChars"`
	SavedChars      int64 `json:"savedChars"`
}

// RtkStats is the rtk rewrite accounting.
// port of src/storage/db.ts:RtkStats
type RtkStats struct {
	Rewrites       int64 `json:"rewrites"`
	AlreadyWrapped int64 `json:"alreadyWrapped"`
	Passthrough    int64 `json:"passthrough"`
}

// BudgetConfig is a configured output budget for one tool.
// port of src/storage/db.ts:BudgetConfig
//
// The line fields are float64 because TS stores plain numbers there: configure
// can set 12.7 and status/applyBudget keep the fraction. JS coerces to an
// integer only at an array-slice boundary.
type BudgetConfig struct {
	MaxLines  float64 `json:"maxLines"`
	HeadLines float64 `json:"headLines"`
	TailLines float64 `json:"tailLines"`
	// MaxChars is the optional total character cap applied after the line
	// budget. TS: `maxChars?: number`.
	MaxChars *float64 `json:"maxChars,omitempty"`
}

// MarshalJSON mirrors JSON.stringify for TS numbers: NaN and ±Infinity become
// null, and an absent maxChars stays omitted (TS `undefined`). The field order
// matches TS's property insertion order in setBudget.
// port of src/storage/db.ts:JSON.stringify(stats.budgets)
func (b BudgetConfig) MarshalJSON() ([]byte, error) {
	type budgetJSON struct {
		MaxLines  any             `json:"maxLines"`
		HeadLines any             `json:"headLines"`
		TailLines any             `json:"tailLines"`
		MaxChars  json.RawMessage `json:"maxChars,omitempty"`
	}
	out := budgetJSON{
		MaxLines:  jsonBudgetNumber(b.MaxLines),
		HeadLines: jsonBudgetNumber(b.HeadLines),
		TailLines: jsonBudgetNumber(b.TailLines),
	}
	if b.MaxChars != nil {
		out.MaxChars = json.RawMessage("null")
		if finite := jsonBudgetNumber(*b.MaxChars); finite != nil {
			raw, err := json.Marshal(finite)
			if err != nil {
				return nil, err
			}
			out.MaxChars = raw
		}
	}
	return json.Marshal(out)
}

// jsonBudgetNumber returns nil for non-finite numbers so json.Marshal writes
// null, exactly like JSON.stringify.
func jsonBudgetNumber(value float64) any {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return value
}

// BounceStats is the dedup-bounce accounting for one file extension.
// port of src/storage/db.ts:BounceStats
type BounceStats struct {
	TotalReads  int64 `json:"totalReads"`
	Bounces     int64 `json:"bounces"`
	WastedChars int64 `json:"wastedChars"`
}

// CacheStats is the read-cache counters of one session.
type CacheStats struct {
	Hits         int64   `json:"hits"`
	Misses       int64   `json:"misses"`
	Total        int64   `json:"total"`
	HitRate      float64 `json:"hitRate"`
	FilesTracked int64   `json:"filesTracked"`
}

// SavingsStats is the token-savings accounting of one session.
type SavingsStats struct {
	TotalCalls            int64 `json:"totalCalls"`
	RawChars              int64 `json:"rawChars"`
	CompressedChars       int64 `json:"compressedChars"`
	CompressionSavedChars int64 `json:"compressionSavedChars"`
	// CompressionSavedCharsSet reports whether the source carried the
	// compressionSavedChars field at all (TS undefined vs an explicit 0).
	// DB rows always carry it; hand-built legacy rows may not.
	CompressionSavedCharsSet bool                   `json:"-"`
	DedupSavedChars          int64                  `json:"dedupSavedChars"`
	SavedChars               int64                  `json:"savedChars"`
	EstimatedTokensSaved     int64                  `json:"estimatedTokensSaved"`
	ByTool                   map[string]ToolSavings `json:"byTool"`
}

// BounceSummary is the dedup-bounce accounting of one session.
type BounceSummary struct {
	TotalBounces     int64                  `json:"totalBounces"`
	TotalWastedChars int64                  `json:"totalWastedChars"`
	ByExtension      map[string]BounceStats `json:"byExtension"`
}

// PersistedStats is one session's stats row.
// port of src/storage/db.ts:PersistedStats
type PersistedStats struct {
	Pid          int64                   `json:"pid"`
	SessionStart int64                   `json:"sessionStart"`
	UpdatedAt    int64                   `json:"updatedAt"`
	Cache        CacheStats              `json:"cache"`
	Savings      SavingsStats            `json:"savings"`
	Rtk          RtkStats                `json:"rtk"`
	Budgets      map[string]BudgetConfig `json:"budgets"`
	Bounces      BounceSummary           `json:"bounces"`
}

// LifetimeStats is the banked lifetime totals across ended sessions.
// port of src/storage/db.ts:LifetimeStats
type LifetimeStats struct {
	BankedSessions        int64 `json:"bankedSessions"`
	Calls                 int64 `json:"calls"`
	RawChars              int64 `json:"rawChars"`
	CompressedChars       int64 `json:"compressedChars"`
	CompressionSavedChars int64 `json:"compressionSavedChars"`
	DedupSavedChars       int64 `json:"dedupSavedChars"`
	SavedChars            int64 `json:"savedChars"`
	Hits                  int64 `json:"hits"`
	Misses                int64 `json:"misses"`
	RtkRewrites           int64 `json:"rtkRewrites"`
	RtkAlreadyWrapped     int64 `json:"rtkAlreadyWrapped"`
	RtkPassthrough        int64 `json:"rtkPassthrough"`
	UpdatedAt             int64 `json:"updatedAt"`
	TotalBounces          int64 `json:"totalBounces"`
	TotalWastedChars      int64 `json:"totalWastedChars"`
}

// ArchiveEntry is one archived shell output.
// port of src/storage/db.ts:ArchiveEntry
type ArchiveEntry struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	SizeChars   int64  `json:"sizeChars"`
	CreatedAt   int64  `json:"createdAt"`
	ContentPath string `json:"contentPath"`
}

// SessionStatsRow is PersistedStats plus the session's ended marker.
// TS: `PersistedStats & { endedAt: number | null }`.
type SessionStatsRow struct {
	PersistedStats
	EndedAt *int64 `json:"endedAt"`
}

// ArchiveTotals is the archive_meta aggregate.
// TS: `{ count: number; totalChars: number }`.
type ArchiveTotals struct {
	Count      int64 `json:"count"`
	TotalChars int64 `json:"totalChars"`
}

// JobState is a background job's lifecycle state.
// port of src/storage/db.ts:JobState
type JobState string

const (
	JobRunning JobState = "running"
	JobExited  JobState = "exited"
	JobKilled  JobState = "killed"
	JobLost    JobState = "lost"
)

// JobRecord is one background job row.
// port of src/storage/db.ts:JobRecord
type JobRecord struct {
	JobID     string   `json:"jobId"`
	Pid       int64    `json:"pid"`
	SessionID string   `json:"sessionId"`
	Command   string   `json:"command"`
	Workdir   *string  `json:"workdir"`
	StartedAt int64    `json:"startedAt"`
	EndedAt   *int64   `json:"endedAt"`
	ExitCode  *int64   `json:"exitCode"`
	Signal    *string  `json:"signal"`
	State     JobState `json:"state"`
	LogPath   string   `json:"logPath"`
}

// JobUpdate carries the columns updateJobState overwrites. Unset fields become
// SQL NULL, exactly like TS's `update.x ?? null`.
// port of src/storage/db.ts:updateJobState (update parameter)
type JobUpdate struct {
	State    JobState
	EndedAt  Optional[int64]
	ExitCode Optional[int64]
	Signal   Optional[string]
}

// Optional distinguishes TS's `field?: T` (omitted, keep the column) from
// `field: null` (write NULL) and `field: value` (write the value), which
// updateJobState needs.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Some marks a field as present with a value.
func Some[T any](value T) Optional[T] {
	return Optional[T]{Set: true, Value: &value}
}

// Null marks a field as present with a NULL value.
func Null[T any]() Optional[T] {
	return Optional[T]{Set: true}
}

// SessionRow mirrors the sessions table's columns.
// port of src/storage/db.ts:SessionRow
type sessionRow struct {
	pid                   int64
	sessionStart          int64
	updatedAt             int64
	endedAt               sql.NullInt64
	cacheHits             int64
	cacheMisses           int64
	filesTracked          int64
	totalCalls            int64
	rawChars              int64
	compressedChars       int64
	compressionSavedChars int64
	dedupSavedChars       int64
	savedChars            int64
	rtkRewrites           int64
	rtkAlreadyWrapped     int64
	rtkPassthrough        int64
	totalBounces          int64
	totalWastedChars      int64
	budgetsJSON           string
}

// sessionColumns is the explicit column list behind `SELECT *` in TS, in the
// same order as sessionRow's fields.
const sessionColumns = `pid, session_start, updated_at, ended_at,
	cache_hits, cache_misses, files_tracked,
	total_calls, raw_chars, compressed_chars, compression_saved_chars,
	dedup_saved_chars, saved_chars,
	rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
	total_bounces, total_wasted_chars, budgets_json`

// port of src/storage/db.ts:sessionsDir
func SessionsDir() string { return filepath.Join(CaveToolsDataDir(), "sessions") }

// LifetimeFile is the legacy JSON file folded into lifetime_baseline.
// port of src/storage/db.ts:lifetimeFile
func LifetimeFile() string { return filepath.Join(CaveToolsDataDir(), "lifetime.json") }

// port of src/storage/db.ts:archiveDir
func ArchiveDir() string { return filepath.Join(CaveToolsDataDir(), "archives") }

// port of src/storage/db.ts:jobsDir
func JobsDir() string { return filepath.Join(CaveToolsDataDir(), "jobs") }

// ReadRegistryFile is the legacy read-registry text file.
// port of src/storage/db.ts:readRegistryFile
func ReadRegistryFile() string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "cave-tools", "read-registry.txt")
}

// DBPath is the shared SQLite file.
// port of src/storage/db.ts:dbPath
func DBPath() string { return filepath.Join(CaveToolsDataDir(), "cave-tools.db") }

var (
	dbMu sync.Mutex
	db   *sql.DB
)

// queryer is what *sql.DB and *sql.Tx both provide, so the migration helpers
// can run either standalone or inside the legacy-migration transaction (TS's
// conn.transaction passes the same connection through).
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// port of src/storage/db.ts:emptyLifetime
func emptyLifetime() LifetimeStats { return LifetimeStats{} }

// tightenMode drops permission bits beyond mode (the db, job logs and archives
// hold paths and command output). Never fails; skipped on Windows.
// port of src/storage/db.ts:tightenMode
func tightenMode(target string, mode os.FileMode) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		// missing or not ours: leave it
		return
	}
	if info.Mode().Perm()&^mode == 0 {
		return
	}
	_ = os.Chmod(target, mode)
}

// openDB opens (once) the shared stats database, creating dirs, the file and
// the schema on first use.
// port of src/storage/db.ts:openDb
func openDB() (*sql.DB, error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		return db, nil
	}

	path := DBPath()
	dataDir := filepath.Dir(path)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// mkdir's mode only applies on creation; tighten dirs from older installs.
	for _, dir := range []string{dataDir, JobsDir(), ArchiveDir()} {
		tightenMode(dir, 0o700)
	}
	// Pre-create the db 0600 so SQLite creates -wal/-shm with the same mode.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0o600); err == nil {
				_ = f.Close()
			}
			// A real problem is reported by SQLite below.
		}
	}
	sidecars := []string{path, path + "-wal", path + "-shm"}
	for _, file := range sidecars {
		tightenMode(file, 0o600)
	}

	conn, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	// Force the first connection so the WAL pragma runs and the sidecar files
	// exist before we tighten them, like TS's db.pragma("journal_mode = WAL").
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	for _, file := range sidecars {
		tightenMode(file, 0o600)
	}
	if _, err := conn.Exec(schemaSQL); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := migrateLegacyData(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	db = conn
	return db, nil
}

// sqliteDSN applies TS's pragmas on every pooled connection. journal_mode is
// persistent in the file; synchronous and busy_timeout are per connection, so
// they live in the DSN instead of one Exec after open.
func sqliteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"busy_timeout(5000)",
	}
	query := u.Query()
	for _, pragma := range pragmas {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// schemaSQL is byte-identical to the TS schema so both implementations see the
// same tables, indexes and defaults.
const schemaSQL = `
    CREATE TABLE IF NOT EXISTS meta (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS lifetime_baseline (
      id INTEGER PRIMARY KEY CHECK (id = 1),
      banked_sessions INTEGER NOT NULL DEFAULT 0,
      calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      compression_saved_chars INTEGER NOT NULL DEFAULT 0,
      dedup_saved_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      hits INTEGER NOT NULL DEFAULT 0,
      misses INTEGER NOT NULL DEFAULT 0,
      rtk_rewrites INTEGER NOT NULL DEFAULT 0,
      rtk_already_wrapped INTEGER NOT NULL DEFAULT 0,
      rtk_passthrough INTEGER NOT NULL DEFAULT 0,
      updated_at INTEGER NOT NULL DEFAULT 0,
      total_bounces INTEGER NOT NULL DEFAULT 0,
      total_wasted_chars INTEGER NOT NULL DEFAULT 0
    );

    CREATE TABLE IF NOT EXISTS sessions (
      pid INTEGER PRIMARY KEY,
      session_start INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      ended_at INTEGER,
      cache_hits INTEGER NOT NULL DEFAULT 0,
      cache_misses INTEGER NOT NULL DEFAULT 0,
      files_tracked INTEGER NOT NULL DEFAULT 0,
      total_calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      compression_saved_chars INTEGER NOT NULL DEFAULT 0,
      dedup_saved_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      rtk_rewrites INTEGER NOT NULL DEFAULT 0,
      rtk_already_wrapped INTEGER NOT NULL DEFAULT 0,
      rtk_passthrough INTEGER NOT NULL DEFAULT 0,
      total_bounces INTEGER NOT NULL DEFAULT 0,
      total_wasted_chars INTEGER NOT NULL DEFAULT 0,
      budgets_json TEXT NOT NULL DEFAULT '{}'
    );

    CREATE TABLE IF NOT EXISTS tool_stats (
      pid INTEGER NOT NULL,
      tool_name TEXT NOT NULL,
      calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      PRIMARY KEY (pid, tool_name)
    );

    CREATE TABLE IF NOT EXISTS bounce_stats (
      pid INTEGER NOT NULL,
      extension TEXT NOT NULL,
      total_reads INTEGER NOT NULL DEFAULT 0,
      bounces INTEGER NOT NULL DEFAULT 0,
      wasted_chars INTEGER NOT NULL DEFAULT 0,
      PRIMARY KEY (pid, extension)
    );

    CREATE TABLE IF NOT EXISTS read_registry (
      path TEXT PRIMARY KEY,
      hash TEXT,
      last_read_at INTEGER NOT NULL
    );

    CREATE TABLE IF NOT EXISTS archive_meta (
      id TEXT PRIMARY KEY,
      command TEXT NOT NULL,
      size_chars INTEGER NOT NULL,
      created_at INTEGER NOT NULL,
      content_path TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_sessions_ended_at ON sessions(ended_at);
    CREATE INDEX IF NOT EXISTS idx_archive_created_at ON archive_meta(created_at);

    CREATE TABLE IF NOT EXISTS jobs (
      job_id TEXT PRIMARY KEY,
      pid INTEGER NOT NULL,
      session_id TEXT NOT NULL DEFAULT 'default',
      command TEXT NOT NULL,
      workdir TEXT,
      started_at INTEGER NOT NULL,
      ended_at INTEGER,
      exit_code INTEGER,
      signal TEXT,
      state TEXT NOT NULL DEFAULT 'running',
      log_path TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_jobs_started_at ON jobs(started_at);
  `

// metaValue reads one meta row; ok is false when the key is absent.
// port of src/storage/db.ts:metaValue
func metaValue(conn queryer, key string) (value string, ok bool, err error) {
	err = conn.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// port of src/storage/db.ts:setMeta
func setMeta(conn queryer, key, value string) error {
	_, err := conn.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value)
	return err
}

// port of src/storage/db.ts:migrateLegacyData
func migrateLegacyData(conn *sql.DB) error {
	value, ok, err := metaValue(conn, "legacy_migration_v1")
	if err != nil {
		return err
	}
	if ok && value == "done" {
		return nil
	}

	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := migrateLifetime(tx); err != nil {
		return err
	}
	if err := migrateSessions(tx); err != nil {
		return err
	}
	if err := migrateReadRegistry(tx); err != nil {
		return err
	}
	if err := migrateArchives(tx); err != nil {
		return err
	}
	if err := setMeta(tx, "legacy_migration_v1", "done"); err != nil {
		return err
	}
	return tx.Commit()
}

// port of src/storage/db.ts:migrateLifetime
func migrateLifetime(conn queryer) error {
	lifetime := emptyLifetime()
	if raw, err := os.ReadFile(LifetimeFile()); err == nil {
		// No legacy lifetime file yet: keep the zero value.
		parsed := emptyLifetime()
		if json.Unmarshal(raw, &parsed) == nil {
			lifetime = parsed
		}
	}

	_, err := conn.Exec(`
    INSERT OR IGNORE INTO lifetime_baseline (
      id, banked_sessions, calls, raw_chars, compressed_chars,
      compression_saved_chars, dedup_saved_chars, saved_chars,
      hits, misses, rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
      updated_at, total_bounces, total_wasted_chars
    ) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `,
		lifetime.BankedSessions,
		lifetime.Calls,
		lifetime.RawChars,
		lifetime.CompressedChars,
		lifetime.CompressionSavedChars,
		lifetime.DedupSavedChars,
		lifetime.SavedChars,
		lifetime.Hits,
		lifetime.Misses,
		lifetime.RtkRewrites,
		lifetime.RtkAlreadyWrapped,
		lifetime.RtkPassthrough,
		lifetime.UpdatedAt,
		lifetime.TotalBounces,
		lifetime.TotalWastedChars,
	)
	return err
}

// port of src/storage/db.ts:migrateSessions
func migrateSessions(conn queryer) error {
	entries, err := os.ReadDir(SessionsDir())
	if err != nil {
		// No legacy sessions dir. Anything else (e.g. ENOTDIR) fails openDb,
		// as TS readdirSync does.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(SessionsDir(), name))
		if err != nil {
			continue
		}
		stats, ok := normalizeLegacyStats(raw)
		if !ok {
			// Skip corrupt legacy session files.
			continue
		}
		// TS catches per file and keeps going.
		_ = upsertSessionStatsWithDB(conn, stats, nil)
	}
	return nil
}

// normalizeLegacyStats fills the sections and leaves an older session file
// may lack, the way a fresh session would, so the NOT NULL columns accept the
// row instead of the whole session being dropped.
// port of src/storage/db.ts:normalizeLegacyStats
func normalizeLegacyStats(raw []byte) (PersistedStats, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return PersistedStats{}, false
	}
	pid, ok := legacyFiniteNumber(fields["pid"])
	if !ok {
		return PersistedStats{}, false
	}

	cache := legacyObject(fields["cache"])
	savings := legacyObject(fields["savings"])
	rtk := legacyObject(fields["rtk"])
	bounces := legacyObject(fields["bounces"])

	byTool := make(map[string]ToolSavings)
	for name, entry := range legacyObject(savings["byTool"]) {
		values := legacyObject(entry)
		byTool[name] = ToolSavings{
			Calls:           legacyNumber(values["calls"]),
			RawChars:        legacyNumber(values["rawChars"]),
			CompressedChars: legacyNumber(values["compressedChars"]),
			SavedChars:      legacyNumber(values["savedChars"]),
		}
	}
	byExtension := make(map[string]BounceStats)
	for name, entry := range legacyObject(bounces["byExtension"]) {
		values := legacyObject(entry)
		byExtension[name] = BounceStats{
			TotalReads:  legacyNumber(values["totalReads"]),
			Bounces:     legacyNumber(values["bounces"]),
			WastedChars: legacyNumber(values["wastedChars"]),
		}
	}
	budgets := make(map[string]BudgetConfig)
	for name, entry := range legacyObject(fields["budgets"]) {
		var config BudgetConfig
		if json.Unmarshal(entry, &config) == nil {
			budgets[name] = config
		}
	}

	return PersistedStats{
		Pid:          int64(pid),
		SessionStart: legacyNumber(fields["sessionStart"]),
		UpdatedAt:    legacyNumber(fields["updatedAt"]),
		Cache: CacheStats{
			Hits:         legacyNumber(cache["hits"]),
			Misses:       legacyNumber(cache["misses"]),
			Total:        legacyNumber(cache["total"]),
			HitRate:      legacyFloat(cache["hitRate"]),
			FilesTracked: legacyNumber(cache["filesTracked"]),
		},
		Savings: SavingsStats{
			TotalCalls:               legacyNumber(savings["totalCalls"]),
			RawChars:                 legacyNumber(savings["rawChars"]),
			CompressedChars:          legacyNumber(savings["compressedChars"]),
			CompressionSavedChars:    legacyNumber(savings["compressionSavedChars"]),
			CompressionSavedCharsSet: savings["compressionSavedChars"] != nil,
			DedupSavedChars:          legacyNumber(savings["dedupSavedChars"]),
			SavedChars:               legacyNumber(savings["savedChars"]),
			EstimatedTokensSaved:     legacyNumber(savings["estimatedTokensSaved"]),
			ByTool:                   byTool,
		},
		Rtk: RtkStats{
			Rewrites:       legacyNumber(rtk["rewrites"]),
			AlreadyWrapped: legacyNumber(rtk["alreadyWrapped"]),
			Passthrough:    legacyNumber(rtk["passthrough"]),
		},
		Budgets: budgets,
		Bounces: BounceSummary{
			TotalBounces:     legacyNumber(bounces["totalBounces"]),
			TotalWastedChars: legacyNumber(bounces["totalWastedChars"]),
			ByExtension:      byExtension,
		},
	}, true
}

// legacyFiniteNumber mirrors Number.isFinite: only JSON numbers qualify, so
// strings, nulls, objects and out-of-range literals are rejected.
func legacyFiniteNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// legacyNumber is TS's num(v): a finite number keeps its value, anything else
// becomes 0.
func legacyNumber(raw json.RawMessage) int64 {
	value, _ := legacyFiniteNumber(raw)
	return int64(value)
}

// legacyFloat is legacyNumber for the one rate field (cache.hitRate).
func legacyFloat(raw json.RawMessage) float64 {
	value, _ := legacyFiniteNumber(raw)
	return value
}

// legacyObject is TS's obj(v): anything that is not a JSON object becomes {}.
func legacyObject(raw json.RawMessage) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return map[string]json.RawMessage{}
	}
	return fields
}

// port of src/storage/db.ts:migrateReadRegistry
func migrateReadRegistry(conn queryer) error {
	raw, err := os.ReadFile(ReadRegistryFile())
	if err != nil {
		// No legacy registry.
		return nil
	}
	now := time.Now().UnixMilli()
	for _, line := range strings.Split(string(raw), "\n") {
		path := jsre.Trim(line)
		if path == "" {
			continue
		}
		if _, err := conn.Exec(
			"INSERT OR IGNORE INTO read_registry (path, hash, last_read_at) VALUES (?, NULL, ?)",
			path, now,
		); err != nil {
			return err
		}
	}
	return nil
}

// port of src/storage/db.ts:migrateArchives
func migrateArchives(conn queryer) error {
	prefixes, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return nil
	}
	for _, prefix := range prefixes {
		prefixDir := filepath.Join(ArchiveDir(), prefix.Name())
		info, err := os.Stat(prefixDir)
		if err != nil || !info.IsDir() {
			continue
		}
		files, err := os.ReadDir(prefixDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".meta.json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(prefixDir, file.Name()))
			if err != nil {
				continue
			}
			var meta struct {
				ID        string `json:"id"`
				Command   string `json:"command"`
				SizeChars int64  `json:"sizeChars"`
				CreatedAt int64  `json:"createdAt"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				// Skip corrupt archive metadata.
				continue
			}
			if _, err := conn.Exec(`
    INSERT OR IGNORE INTO archive_meta (id, command, size_chars, created_at, content_path)
    VALUES (?, ?, ?, ?, ?)
  `, meta.ID, meta.Command, meta.SizeChars, meta.CreatedAt, filepath.Join(prefixDir, meta.ID+".txt")); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseBudgets mirrors TS's JSON.parse returning a plain object.
// port of src/storage/db.ts:parseBudgets
func parseBudgets(raw string) map[string]BudgetConfig {
	budgets := map[string]BudgetConfig{}
	if raw == "" {
		return budgets
	}
	if err := json.Unmarshal([]byte(raw), &budgets); err != nil {
		return map[string]BudgetConfig{}
	}
	if budgets == nil {
		return map[string]BudgetConfig{}
	}
	return budgets
}

// port of src/storage/db.ts:rowToStats
func rowToStats(conn queryer, row sessionRow) (SessionStatsRow, error) {
	tools, err := conn.Query("SELECT tool_name, calls, raw_chars, compressed_chars, saved_chars FROM tool_stats WHERE pid = ?", row.pid)
	if err != nil {
		return SessionStatsRow{}, err
	}
	defer func() { _ = tools.Close() }()
	byTool := map[string]ToolSavings{}
	for tools.Next() {
		var name string
		var tool ToolSavings
		if err := tools.Scan(&name, &tool.Calls, &tool.RawChars, &tool.CompressedChars, &tool.SavedChars); err != nil {
			return SessionStatsRow{}, err
		}
		byTool[name] = tool
	}
	if err := tools.Err(); err != nil {
		return SessionStatsRow{}, err
	}

	bounces, err := conn.Query("SELECT extension, total_reads, bounces, wasted_chars FROM bounce_stats WHERE pid = ?", row.pid)
	if err != nil {
		return SessionStatsRow{}, err
	}
	defer func() { _ = bounces.Close() }()
	byExtension := map[string]BounceStats{}
	for bounces.Next() {
		var ext string
		var bounce BounceStats
		if err := bounces.Scan(&ext, &bounce.TotalReads, &bounce.Bounces, &bounce.WastedChars); err != nil {
			return SessionStatsRow{}, err
		}
		byExtension[ext] = bounce
	}
	if err := bounces.Err(); err != nil {
		return SessionStatsRow{}, err
	}

	hitRate := 0.0
	if row.cacheHits+row.cacheMisses > 0 {
		hitRate = float64(row.cacheHits) / float64(row.cacheHits+row.cacheMisses)
	}

	return SessionStatsRow{
		PersistedStats: PersistedStats{
			Pid:          row.pid,
			SessionStart: row.sessionStart,
			UpdatedAt:    row.updatedAt,
			Cache: CacheStats{
				Hits:         row.cacheHits,
				Misses:       row.cacheMisses,
				Total:        row.cacheHits + row.cacheMisses,
				HitRate:      hitRate,
				FilesTracked: row.filesTracked,
			},
			Savings: SavingsStats{
				TotalCalls:               row.totalCalls,
				RawChars:                 row.rawChars,
				CompressedChars:          row.compressedChars,
				CompressionSavedChars:    row.compressionSavedChars,
				CompressionSavedCharsSet: true,
				DedupSavedChars:          row.dedupSavedChars,
				SavedChars:               row.savedChars,
				// TS: Math.round(saved_chars / 4).
				EstimatedTokensSaved: int64(math.Round(float64(row.savedChars) / 4)),
				ByTool:               byTool,
			},
			Rtk: RtkStats{
				Rewrites:       row.rtkRewrites,
				AlreadyWrapped: row.rtkAlreadyWrapped,
				Passthrough:    row.rtkPassthrough,
			},
			Budgets: parseBudgets(row.budgetsJSON),
			Bounces: BounceSummary{
				TotalBounces:     row.totalBounces,
				TotalWastedChars: row.totalWastedChars,
				ByExtension:      byExtension,
			},
		},
		EndedAt: nullInt64Ptr(row.endedAt),
	}, nil
}

// port of src/storage/db.ts:upsertSessionStatsWithDb
func upsertSessionStatsWithDB(conn queryer, stats PersistedStats, endedAt *int64) error {
	if _, err := conn.Exec(`
    INSERT INTO sessions (
      pid, session_start, updated_at, ended_at,
      cache_hits, cache_misses, files_tracked,
      total_calls, raw_chars, compressed_chars, compression_saved_chars,
      dedup_saved_chars, saved_chars,
      rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
      total_bounces, total_wasted_chars, budgets_json
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(pid) DO UPDATE SET
      session_start = excluded.session_start,
      updated_at = excluded.updated_at,
      ended_at = COALESCE(sessions.ended_at, excluded.ended_at),
      cache_hits = excluded.cache_hits,
      cache_misses = excluded.cache_misses,
      files_tracked = excluded.files_tracked,
      total_calls = excluded.total_calls,
      raw_chars = excluded.raw_chars,
      compressed_chars = excluded.compressed_chars,
      compression_saved_chars = excluded.compression_saved_chars,
      dedup_saved_chars = excluded.dedup_saved_chars,
      saved_chars = excluded.saved_chars,
      rtk_rewrites = excluded.rtk_rewrites,
      rtk_already_wrapped = excluded.rtk_already_wrapped,
      rtk_passthrough = excluded.rtk_passthrough,
      total_bounces = excluded.total_bounces,
      total_wasted_chars = excluded.total_wasted_chars,
      budgets_json = excluded.budgets_json
  `,
		stats.Pid,
		stats.SessionStart,
		stats.UpdatedAt,
		endedAt,
		stats.Cache.Hits,
		stats.Cache.Misses,
		stats.Cache.FilesTracked,
		stats.Savings.TotalCalls,
		stats.Savings.RawChars,
		stats.Savings.CompressedChars,
		stats.Savings.CompressionSavedChars,
		stats.Savings.DedupSavedChars,
		stats.Savings.SavedChars,
		stats.Rtk.Rewrites,
		stats.Rtk.AlreadyWrapped,
		stats.Rtk.Passthrough,
		stats.Bounces.TotalBounces,
		stats.Bounces.TotalWastedChars,
		marshalBudgets(stats.Budgets),
	); err != nil {
		return err
	}

	if _, err := conn.Exec("DELETE FROM tool_stats WHERE pid = ?", stats.Pid); err != nil {
		return err
	}
	for name, tool := range stats.Savings.ByTool {
		if _, err := conn.Exec(`
    INSERT INTO tool_stats (pid, tool_name, calls, raw_chars, compressed_chars, saved_chars)
    VALUES (?, ?, ?, ?, ?, ?)
  `, stats.Pid, name, tool.Calls, tool.RawChars, tool.CompressedChars, tool.SavedChars); err != nil {
			return err
		}
	}

	if _, err := conn.Exec("DELETE FROM bounce_stats WHERE pid = ?", stats.Pid); err != nil {
		return err
	}
	for ext, bounce := range stats.Bounces.ByExtension {
		if _, err := conn.Exec(`
    INSERT INTO bounce_stats (pid, extension, total_reads, bounces, wasted_chars)
    VALUES (?, ?, ?, ?, ?)
  `, stats.Pid, ext, bounce.TotalReads, bounce.Bounces, bounce.WastedChars); err != nil {
			return err
		}
	}
	return nil
}

// marshalBudgets is JSON.stringify(stats.budgets): a nil map serialises as {}
// instead of null so TS's JSON.parse still yields an object.
func marshalBudgets(budgets map[string]BudgetConfig) string {
	if budgets == nil {
		return "{}"
	}
	raw, err := json.Marshal(budgets)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// port of src/storage/db.ts:upsertSessionStats
func UpsertSessionStats(stats PersistedStats) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertSessionStatsWithDB(tx, stats, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// ListSessionStats returns one row per session.
// port of src/storage/db.ts:listSessionStats
func ListSessionStats(includeEnded bool) ([]SessionStatsRow, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + sessionColumns + " FROM sessions WHERE ended_at IS NULL ORDER BY updated_at DESC"
	if includeEnded {
		query = "SELECT " + sessionColumns + " FROM sessions ORDER BY updated_at DESC"
	}
	rows, err := conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	stats := []SessionStatsRow{}
	for rows.Next() {
		row, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		converted, err := rowToStats(conn, row)
		if err != nil {
			return nil, err
		}
		stats = append(stats, converted)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// MarkSessionEnded stamps ended_at once; reports whether a row changed.
// port of src/storage/db.ts:markSessionEnded
func MarkSessionEnded(pid int64, endedAtMillis ...int64) (bool, error) {
	endedAt := time.Now().UnixMilli()
	if len(endedAtMillis) > 0 {
		endedAt = endedAtMillis[0]
	}
	conn, err := openDB()
	if err != nil {
		return false, err
	}
	result, err := conn.Exec("UPDATE sessions SET ended_at = ? WHERE pid = ? AND ended_at IS NULL", endedAt, pid)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed > 0, nil
}

// port of src/storage/db.ts:lifetimeFromRow
func lifetimeFromRow(row *LifetimeStats) LifetimeStats {
	if row == nil {
		return emptyLifetime()
	}
	return *row
}

// GetLifetimeStats returns the baseline plus every ended session.
// port of src/storage/db.ts:getLifetimeStats
func GetLifetimeStats() (LifetimeStats, error) {
	conn, err := openDB()
	if err != nil {
		return LifetimeStats{}, err
	}

	baselineRow := LifetimeStats{}
	found := true
	err = conn.QueryRow("SELECT "+lifetimeColumns+" FROM lifetime_baseline WHERE id = 1").Scan(
		&baselineRow.BankedSessions,
		&baselineRow.Calls,
		&baselineRow.RawChars,
		&baselineRow.CompressedChars,
		&baselineRow.CompressionSavedChars,
		&baselineRow.DedupSavedChars,
		&baselineRow.SavedChars,
		&baselineRow.Hits,
		&baselineRow.Misses,
		&baselineRow.RtkRewrites,
		&baselineRow.RtkAlreadyWrapped,
		&baselineRow.RtkPassthrough,
		&baselineRow.UpdatedAt,
		&baselineRow.TotalBounces,
		&baselineRow.TotalWastedChars,
	)
	if errors.Is(err, sql.ErrNoRows) {
		found = false
	} else if err != nil {
		return LifetimeStats{}, err
	}
	var baseline LifetimeStats
	if found {
		baseline = lifetimeFromRow(&baselineRow)
	} else {
		baseline = lifetimeFromRow(nil)
	}

	rows, err := conn.Query("SELECT " + sessionColumns + " FROM sessions WHERE ended_at IS NOT NULL")
	if err != nil {
		return LifetimeStats{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		row, err := scanSessionRow(rows)
		if err != nil {
			return LifetimeStats{}, err
		}
		baseline.BankedSessions++
		baseline.Calls += row.totalCalls
		baseline.RawChars += row.rawChars
		baseline.CompressedChars += row.compressedChars
		baseline.CompressionSavedChars += row.compressionSavedChars
		baseline.DedupSavedChars += row.dedupSavedChars
		baseline.SavedChars += row.savedChars
		baseline.Hits += row.cacheHits
		baseline.Misses += row.cacheMisses
		baseline.RtkRewrites += row.rtkRewrites
		baseline.RtkAlreadyWrapped += row.rtkAlreadyWrapped
		baseline.RtkPassthrough += row.rtkPassthrough
		baseline.TotalBounces += row.totalBounces
		baseline.TotalWastedChars += row.totalWastedChars
		if row.updatedAt > baseline.UpdatedAt {
			baseline.UpdatedAt = row.updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return LifetimeStats{}, err
	}
	return baseline, nil
}

// lifetimeColumns matches the lifetime_baseline column order.
const lifetimeColumns = `banked_sessions, calls, raw_chars, compressed_chars,
	compression_saved_chars, dedup_saved_chars, saved_chars,
	hits, misses, rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
	updated_at, total_bounces, total_wasted_chars`

// RegisterReadPath records that path was read. Advisory: a missing or locked
// DB must not fail reads, so errors are swallowed like TS's try/catch.
// port of src/storage/db.ts:registerReadPath
func RegisterReadPath(path string, hash *string) {
	conn, err := openDB()
	if err != nil {
		return
	}
	_, _ = conn.Exec(
		"INSERT INTO read_registry (path, hash, last_read_at) VALUES (?, ?, ?) ON CONFLICT(path) DO UPDATE SET hash = COALESCE(excluded.hash, read_registry.hash), last_read_at = excluded.last_read_at",
		path, anyString(hash), time.Now().UnixMilli(),
	)
}

// port of src/storage/db.ts:recordArchive
func RecordArchive(entry ArchiveEntry) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	_, err = conn.Exec(
		"INSERT OR REPLACE INTO archive_meta (id, command, size_chars, created_at, content_path) VALUES (?, ?, ?, ?, ?)",
		entry.ID, entry.Command, entry.SizeChars, entry.CreatedAt, entry.ContentPath,
	)
	return err
}

// port of src/storage/db.ts:listArchives
func ListArchives() ([]ArchiveEntry, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query("SELECT id, command, size_chars, created_at, content_path FROM archive_meta ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	archives := []ArchiveEntry{}
	for rows.Next() {
		var entry ArchiveEntry
		if err := rows.Scan(&entry.ID, &entry.Command, &entry.SizeChars, &entry.CreatedAt, &entry.ContentPath); err != nil {
			return nil, err
		}
		archives = append(archives, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return archives, nil
}

// port of src/storage/db.ts:archiveStats
func ArchiveStats() (ArchiveTotals, error) {
	conn, err := openDB()
	if err != nil {
		return ArchiveTotals{}, err
	}
	var totals ArchiveTotals
	err = conn.QueryRow("SELECT COUNT(*) AS count, COALESCE(SUM(size_chars), 0) AS totalChars FROM archive_meta").
		Scan(&totals.Count, &totals.TotalChars)
	if err != nil {
		return ArchiveTotals{}, err
	}
	return totals, nil
}

// port of src/storage/db.ts:deleteArchive
func DeleteArchive(id string) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	_, err = conn.Exec("DELETE FROM archive_meta WHERE id = ?", id)
	return err
}

// jobColumns matches the jobs table column order.
const jobColumns = `job_id, pid, session_id, command, workdir, started_at, ended_at, exit_code, signal, state, log_path`

// port of src/storage/db.ts:insertJob
func InsertJob(job JobRecord) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	_, err = conn.Exec(`
       INSERT INTO jobs (job_id, pid, session_id, command, workdir, started_at, ended_at, exit_code, signal, state, log_path)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.JobID,
		job.Pid,
		job.SessionID,
		job.Command,
		anyString(job.Workdir),
		job.StartedAt,
		anyInt64(job.EndedAt),
		anyInt64(job.ExitCode),
		anyString(job.Signal),
		string(job.State),
		job.LogPath,
	)
	return err
}

// UpdateJobState overwrites state, ended_at, exit_code and signal; unset fields
// land as NULL, matching TS's `update.x ?? null`.
// port of src/storage/db.ts:updateJobState
func UpdateJobState(jobID string, update JobUpdate) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	// Only the fields the caller provides are written; omitted ones keep their
	// value, so a state-only update no longer erases a recorded exit code.
	sets := []string{"state = ?"}
	values := []any{string(update.State)}
	if update.EndedAt.Set {
		sets = append(sets, "ended_at = ?")
		values = append(values, optionalValue(update.EndedAt))
	}
	if update.ExitCode.Set {
		sets = append(sets, "exit_code = ?")
		values = append(values, optionalValue(update.ExitCode))
	}
	if update.Signal.Set {
		sets = append(sets, "signal = ?")
		values = append(values, optionalValue(update.Signal))
	}
	values = append(values, jobID)
	_, err = conn.Exec("UPDATE jobs SET "+strings.Join(sets, ", ")+" WHERE job_id = ?", values...)
	return err
}

// optionalValue maps a three-state field onto a driver argument.
func optionalValue[T any](field Optional[T]) any {
	if field.Value == nil {
		return nil
	}
	return *field.Value
}

// port of src/storage/db.ts:getJob
func GetJob(jobID string) (*JobRecord, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	job, err := scanJob(conn.QueryRow("SELECT "+jobColumns+" FROM jobs WHERE job_id = ?", jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ListJobs returns jobs newest first, optionally filtered by session.
// port of src/storage/db.ts:listJobs
func ListJobs(sessionID *string) ([]JobRecord, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if sessionID == nil {
		rows, err = conn.Query("SELECT " + jobColumns + " FROM jobs ORDER BY started_at DESC")
	} else {
		rows, err = conn.Query("SELECT "+jobColumns+" FROM jobs WHERE session_id = ? ORDER BY started_at DESC", *sessionID)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectJobs(rows)
}

// port of src/storage/db.ts:listRunningJobs
func ListRunningJobs() ([]JobRecord, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query("SELECT " + jobColumns + " FROM jobs WHERE state = 'running' ORDER BY started_at DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectJobs(rows)
}

// port of src/storage/db.ts:deleteJob
func DeleteJob(jobID string) error {
	conn, err := openDB()
	if err != nil {
		return err
	}
	_, err = conn.Exec("DELETE FROM jobs WHERE job_id = ?", jobID)
	return err
}

// port of src/storage/db.ts:listJobsEndedBefore
func ListJobsEndedBefore(cutoffMs int64) ([]JobRecord, error) {
	conn, err := openDB()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(
		"SELECT "+jobColumns+" FROM jobs WHERE state != 'running' AND started_at < ? ORDER BY started_at DESC",
		cutoffMs,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectJobs(rows)
}

// collectJobs scans a jobs result set in start-time order.
func collectJobs(rows *sql.Rows) ([]JobRecord, error) {
	jobs := []JobRecord{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// scanner is the shared shape of *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanJob reads one job row in jobColumns order.
// port of src/storage/db.ts:jobFromRow
func scanJob(row scanner) (JobRecord, error) {
	var (
		job      JobRecord
		workdir  sql.NullString
		endedAt  sql.NullInt64
		exitCode sql.NullInt64
		signal   sql.NullString
		state    string
	)
	if err := row.Scan(
		&job.JobID, &job.Pid, &job.SessionID, &job.Command, &workdir, &job.StartedAt,
		&endedAt, &exitCode, &signal, &state, &job.LogPath,
	); err != nil {
		return JobRecord{}, err
	}
	if workdir.Valid {
		job.Workdir = &workdir.String
	}
	job.EndedAt = nullInt64Ptr(endedAt)
	job.ExitCode = nullInt64Ptr(exitCode)
	if signal.Valid {
		job.Signal = &signal.String
	}
	job.State = JobState(state)
	return job, nil
}

// scanSessionRow reads one sessions row in sessionColumns order.
func scanSessionRow(rows *sql.Rows) (sessionRow, error) {
	var row sessionRow
	err := rows.Scan(
		&row.pid,
		&row.sessionStart,
		&row.updatedAt,
		&row.endedAt,
		&row.cacheHits,
		&row.cacheMisses,
		&row.filesTracked,
		&row.totalCalls,
		&row.rawChars,
		&row.compressedChars,
		&row.compressionSavedChars,
		&row.dedupSavedChars,
		&row.savedChars,
		&row.rtkRewrites,
		&row.rtkAlreadyWrapped,
		&row.rtkPassthrough,
		&row.totalBounces,
		&row.totalWastedChars,
		&row.budgetsJSON,
	)
	return row, err
}

// nullInt64Ptr converts a nullable SQL integer to TS's `number | null`.
func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	converted := value.Int64
	return &converted
}

// anyInt64 maps a `number | null` pointer to a driver argument.
func anyInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// anyString maps a `string | null` pointer to a driver argument.
func anyString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
