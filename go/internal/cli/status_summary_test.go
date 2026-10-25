package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/statusline"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

func TestFormatStatuslineSuffix(t *testing.T) {
	cases := []struct {
		name    string
		summary statusSummary
		env     map[string]string
		want    string
	}{
		{name: "no data", summary: statusSummary{}, want: ""},
		{name: "millions", summary: statusSummary{HasData: true, ReductionPct: 38.4, TokensSaved: 1_100_000, CacheHits: 5}, want: "↓38% • 1.1M tok"},
		{name: "thousands", summary: statusSummary{HasData: true, ReductionPct: 48, TokensSaved: 1500}, want: "↓48% • 2k tok"},
		{name: "small", summary: statusSummary{HasData: true, ReductionPct: 12, TokensSaved: 999}, want: "↓12% • 999 tok"},
		{name: "cache fallback", summary: statusSummary{HasData: true, ReductionPct: 5, CacheHits: 18}, want: "↓5% • 18cache"},
		{name: "ascii", summary: statusSummary{HasData: true, ReductionPct: 38, TokensSaved: 1_100_000}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}, want: "v38% | 1.1M tok"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if test.env == nil {
				t.Setenv("CAVE_TOOLS_ASCII", "")
			}
			if got := formatStatuslineSuffix(test.summary); got != test.want {
				t.Fatalf("suffix = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteStatuslineSuffix(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	writeStatuslineSuffix("↓38% • 1.1M tok")
	path := filepath.Join(claudeDir, ".cave-tools-statusline-suffix")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read suffix: %v", err)
	}
	if string(content) != "↓38% • 1.1M tok" {
		t.Fatalf("content = %q", content)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteStatuslineSuffixRefusesSymlinkTarget(t *testing.T) {
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
	writeStatuslineSuffix("new")
	content, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep" {
		t.Fatalf("followed symlink: %q", content)
	}
}

func TestWriteStatuslineSuffixThroughOwnedSymlinkedDir(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinked config-dir ownership check is POSIX")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "claude")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", link)
	writeStatuslineSuffix("badge")
	content, err := os.ReadFile(filepath.Join(realDir, ".cave-tools-statusline-suffix"))
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(content) != "badge" {
		t.Fatalf("content = %q", content)
	}
}

func TestStatusEmitStatusline(t *testing.T) {
	minimalEnv(t)
	claudeDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CAVE_TOOLS_STATUS_RTK", "0")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"status", "--emit-statusline"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(claudeDir, ".cave-tools-statusline-suffix")); err != nil {
		t.Fatalf("suffix missing: %v", err)
	}
}

const (
	statuslineHelperEnv  = "CAVE_TOOLS_TEST_STATUSLINE_HELPER"
	statuslineHelperData = "CAVE_TOOLS_TEST_STATUSLINE_DATA"
)

// TestStatuslineHelperProcess is the subprocess body for
// TestEmitStatuslineMatchesMCPBadge. Each mode gets a fresh process (and a
// fresh SQLite connection), so background stats writes from other tests cannot
// race the comparison.
func TestStatuslineHelperProcess(t *testing.T) {
	mode := os.Getenv(statuslineHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	if dataDir := os.Getenv(statuslineHelperData); dataDir != "" {
		_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dataDir)
	}
	switch mode {
	case "seed":
		err := storage.UpsertSessionStats(storage.PersistedStats{
			Pid:          int64(os.Getpid()),
			SessionStart: 1,
			UpdatedAt:    5,
			Cache:        storage.CacheStats{Hits: 3, Misses: 1, Total: 4, FilesTracked: 2},
			Savings: storage.SavingsStats{
				TotalCalls:            4,
				RawChars:              1000,
				CompressedChars:       500,
				CompressionSavedChars: 400,
				DedupSavedChars:       100,
				SavedChars:            500,
			},
			Budgets: map[string]storage.BudgetConfig{
				"bash": {MaxLines: 50, HeadLines: 15, TailLines: 35},
			},
		})
		if err != nil {
			t.Fatalf("UpsertSessionStats: %v", err)
		}
		// Exactly what the MCP server's in-process refresh writes.
		statusline.Write(statusline.BuildSuffix())
	case "cli":
		if code := Run(context.Background(), []string{"status", "--emit-statusline"}, strings.NewReader(""), io.Discard, io.Discard, "test"); code != 0 {
			t.Fatalf("status --emit-statusline exit = %d", code)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

// TestEmitStatuslineMatchesMCPBadge pins the invariant that the badge written
// by the MCP server's in-process refresh is byte-identical to what
// `status --emit-statusline` writes for the same DB state. The seed process
// writes while its session is live; the CLI process then sees the same stats
// banked from the ended session, so the totals (and bytes) must agree.
func TestEmitStatuslineMatchesMCPBadge(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	directDir := filepath.Join(root, "direct")
	cliDir := filepath.Join(root, "cli")

	run := func(mode, claudeDir string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestStatuslineHelperProcess$")
		cmd.Env = append(os.Environ(),
			statuslineHelperEnv+"="+mode,
			statuslineHelperData+"="+dataDir,
			"CLAUDE_CONFIG_DIR="+claudeDir,
			"CAVE_TOOLS_ASCII=1",
			"CAVE_TOOLS_STATUS_RTK=0",
			"CAVE_TOOLS_DAEMON=0",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s helper: %v\n%s", mode, err, output)
		}
	}
	run("seed", directDir)
	run("cli", cliDir)

	want, err := os.ReadFile(filepath.Join(directDir, ".cave-tools-statusline-suffix"))
	if err != nil {
		t.Fatalf("read direct badge: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("direct badge is empty")
	}
	got, err := os.ReadFile(filepath.Join(cliDir, ".cave-tools-statusline-suffix"))
	if err != nil {
		t.Fatalf("read CLI badge: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("badge mismatch\n direct: %q\n    cli: %q", want, got)
	}
}

func TestStatusCommandRenders(t *testing.T) {
	minimalEnv(t)
	t.Setenv("CAVE_TOOLS_STATUS_RTK", "0")
	t.Setenv("CAVE_TOOLS_ASCII", "1")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"status", "--verbose"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "cave-tools") {
		t.Fatalf("missing status header: %q", output)
	}
	if !strings.Contains(output, "budgets") {
		t.Fatalf("verbose status missing budgets: %q", output)
	}
}

func TestBudgetEntriesOrder(t *testing.T) {
	entries := budgetEntries(map[string]storage.BudgetConfig{
		"ls":     {MaxLines: 60},
		"custom": {MaxLines: 1},
		"bash":   {MaxLines: 50},
		"read":   {MaxLines: 200},
	})
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	want := []string{"bash", "read", "ls", "custom"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

func TestBuildStatusSummaryAggregates(t *testing.T) {
	minimalEnv(t)
	pid := int64(os.Getpid())
	err := storage.UpsertSessionStats(storage.PersistedStats{
		Pid:          pid,
		SessionStart: 1,
		UpdatedAt:    5,
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
		Rtk: storage.RtkStats{Rewrites: 2, AlreadyWrapped: 1, Passthrough: 4},
		Budgets: map[string]storage.BudgetConfig{
			"bash": {MaxLines: 50, HeadLines: 15, TailLines: 35},
		},
	})
	if err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	summary := buildStatusSummary()
	if !summary.HasData || summary.LiveSessions != 1 || summary.VisibleSessions != 1 {
		t.Fatalf("session counts: %#v", summary)
	}
	if summary.CacheHits != 3 || summary.CacheMisses != 1 || summary.FilesTracked != 2 {
		t.Fatalf("cache totals: %#v", summary)
	}
	if summary.TotalCalls != 4 || summary.RawChars != 1000 || summary.CompressionSavedChars != 400 || summary.DedupSavedChars != 100 {
		t.Fatalf("savings totals: %#v", summary)
	}
	if summary.RtkRewrites != 2 || summary.RtkAlreadyWrapped != 1 || summary.RtkPassthrough != 4 {
		t.Fatalf("rtk totals: %#v", summary)
	}
	if summary.LastUpdate < 5 {
		t.Fatalf("lastUpdate = %d", summary.LastUpdate)
	}
	if len(summary.Budgets) != 1 || summary.Budgets[0].Name != "bash" || summary.Budgets[0].Value.MaxLines != 50 {
		t.Fatalf("budgets = %#v", summary.Budgets)
	}
	names := make([]string, len(summary.ByTool))
	for i, tool := range summary.ByTool {
		names[i] = tool.Name
	}
	// Go visits each session's map alphabetically; the renderer re-sorts.
	if !reflect.DeepEqual(names, []string{"bash", "read"}) {
		t.Fatalf("byTool order = %v", names)
	}
}

func TestJsMathRound(t *testing.T) {
	tests := map[float64]int64{2.5: 3, 2.4: 2, -2.5: -2, -2.6: -3, 0: 0}
	for input, want := range tests {
		if got := jsMathRound(input); got != want {
			t.Errorf("jsMathRound(%v) = %d, want %d", input, got, want)
		}
	}
}
