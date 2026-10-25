package statusline

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// TestMain pins one data dir for the package: storage caches its SQLite
// connection on first open, so per-test overrides would point later tests at a
// deleted directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-statusline-test-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestFormat(t *testing.T) {
	cases := []struct {
		name    string
		summary Summary
		env     map[string]string
		want    string
	}{
		{name: "no data", summary: Summary{}, want: ""},
		{name: "millions", summary: Summary{HasData: true, ReductionPct: 38.4, TokensSaved: 1_100_000, CacheHits: 5}, want: "↓38% • 1.1M tok"},
		{name: "thousands", summary: Summary{HasData: true, ReductionPct: 48, TokensSaved: 1500}, want: "↓48% • 2k tok"},
		{name: "small", summary: Summary{HasData: true, ReductionPct: 12, TokensSaved: 999}, want: "↓12% • 999 tok"},
		{name: "cache fallback", summary: Summary{HasData: true, ReductionPct: 5, CacheHits: 18}, want: "↓5% • 18cache"},
		{name: "ascii", summary: Summary{HasData: true, ReductionPct: 38, TokensSaved: 1_100_000}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}, want: "v38% | 1.1M tok"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CAVE_TOOLS_ASCII", "")
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if got := Format(test.summary); got != test.want {
				t.Fatalf("suffix = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	Write("↓38% • 1.1M tok")
	path := filepath.Join(claudeDir, ".cave-tools-statusline-suffix")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read suffix: %v", err)
	}
	if string(content) != "↓38% • 1.1M tok" {
		t.Fatalf("content = %q", content)
	}
	if info, err := os.Stat(path); err == nil && goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	// Atomic write: no temp files may be left behind.
	entries, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".cave-tools-statusline-suffix" {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("unexpected files in config dir: %v", names)
	}
}

func TestWriteReplacesPreviousContent(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	Write("old")
	Write("new")
	content, err := os.ReadFile(filepath.Join(claudeDir, ".cave-tools-statusline-suffix"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("content = %q", content)
	}
}

func TestWriteRefusesSymlinkTarget(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(root, "claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(claudeDir, ".cave-tools-statusline-suffix")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	Write("new")
	content, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep" {
		t.Fatalf("followed symlink: %q", content)
	}
}

func TestBuildSuffixAggregates(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CAVE_TOOLS_ASCII", "1")
	seedSession(t)
	suffix := BuildSuffix()
	if suffix == "" {
		t.Fatal("suffix is empty despite seeded stats")
	}
	if !strings.HasPrefix(suffix, "v") || !strings.Contains(suffix, "|") {
		t.Fatalf("suffix = %q, want ASCII badge", suffix)
	}
	if suffix != Format(Build()) {
		t.Fatalf("BuildSuffix = %q, Format(Build) = %q", suffix, Format(Build()))
	}
}

// counters is a race-safe recorder for emitter tests.
type counters struct {
	mu     sync.Mutex
	builds int
	writes []string
}

func (c *counters) build() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.builds++
	return "badge"
}

func (c *counters) write(suffix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, suffix)
}

func (c *counters) snapshot() (int, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.builds, append([]string{}, c.writes...)
}

func waitForWrites(t *testing.T, c *counters, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, writes := c.snapshot()
		if len(writes) >= want || time.Now().After(deadline) {
			return writes
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEmitterDebouncesBurst(t *testing.T) {
	c := &counters{}
	emitter := newEmitter(30*time.Millisecond, c.build, c.write, func() bool { return false })
	for i := 0; i < 5; i++ {
		emitter.Schedule()
		time.Sleep(2 * time.Millisecond)
	}
	writes := waitForWrites(t, c, 1)
	time.Sleep(60 * time.Millisecond) // let a wrongly-armed second timer fire
	builds, writes := c.snapshot()
	if len(writes) != 1 {
		t.Fatalf("writes = %v, want exactly one", writes)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1 (one SQLite read per burst)", builds)
	}
	if writes[0] != "badge" {
		t.Fatalf("write = %q", writes[0])
	}
}

func TestEmitterFlushWritesPending(t *testing.T) {
	c := &counters{}
	emitter := newEmitter(time.Hour, c.build, c.write, func() bool { return false })
	emitter.Schedule()
	emitter.Flush()
	if _, writes := c.snapshot(); len(writes) != 1 {
		t.Fatalf("writes after flush = %v, want one", writes)
	}
	// Nothing pending: a second flush must not rewrite.
	emitter.Flush()
	if _, writes := c.snapshot(); len(writes) != 1 {
		t.Fatalf("writes after empty flush = %v, want still one", writes)
	}
}

func TestEmitterDisabledSkipsWrite(t *testing.T) {
	c := &counters{}
	emitter := newEmitter(5*time.Millisecond, c.build, c.write, func() bool { return true })
	emitter.Schedule()
	time.Sleep(40 * time.Millisecond)
	if builds, writes := c.snapshot(); builds != 0 || len(writes) != 0 {
		t.Fatalf("disabled emitter ran: builds=%d writes=%v", builds, writes)
	}
}

func TestEmitterSwallowsWriteFailure(t *testing.T) {
	c := &counters{}
	emitter := newEmitter(5*time.Millisecond, c.build, func(string) { panic("boom") }, func() bool { return false })
	emitter.Schedule()
	time.Sleep(40 * time.Millisecond) // a panic would fail the test binary
	// The emitter must remain usable after a failed write.
	emitter.Schedule()
	time.Sleep(40 * time.Millisecond)
	if builds, _ := c.snapshot(); builds < 2 {
		t.Fatalf("builds = %d, want at least 2", builds)
	}
}

// seedSession inserts one live session row (this test process) so the
// aggregate path has data to summarize.
func seedSession(t *testing.T) {
	t.Helper()
	err := storage.UpsertSessionStats(storage.PersistedStats{
		Pid:          int64(os.Getpid()),
		SessionStart: 1,
		UpdatedAt:    time.Now().UnixMilli(),
		Cache:        storage.CacheStats{Hits: 3, Misses: 1, Total: 4, FilesTracked: 2},
		Savings: storage.SavingsStats{
			TotalCalls:            4,
			RawChars:              1000,
			CompressedChars:       500,
			CompressionSavedChars: 400,
			DedupSavedChars:       100,
			SavedChars:            500,
			ByTool: map[string]storage.ToolSavings{
				"read": {Calls: 3, RawChars: 700, SavedChars: 350},
				"bash": {Calls: 1, RawChars: 300, SavedChars: 150},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
}
