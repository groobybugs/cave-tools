package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// TestMain keeps the in-process MCP server on the same neutral environment as
// the TS child it is compared with: a subagent-like key in only one side would
// change read freshness or add a stderr warning.
//
// It also pins one stable data dir for the package. storage caches its SQLite
// connection on first open, so a per-test t.Setenv("CAVE_TOOLS_DATA_DIR")
// points later tests at a directory t.TempDir already deleted.
func TestMain(m *testing.M) {
	testenv.ScrubProcess()
	dir, err := os.MkdirTemp("", "cave-server-test-")
	if err != nil {
		panic(err)
	}
	old, hadOld := os.LookupEnv("CAVE_TOOLS_DATA_DIR")
	if err := os.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data")); err != nil {
		panic(err)
	}
	// Keep the badge side effect (and the daemon socket key, which hashes this
	// dir) inside the sandbox for every test in the package.
	if err := os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude")); err != nil {
		panic(err)
	}
	// Open the shared connection here, before any test overrides the data dir
	// with a t.TempDir it later deletes. The cached connection then stays valid.
	_, _ = storage.ListSessionStats(false)
	code := m.Run()
	if hadOld {
		_ = os.Setenv("CAVE_TOOLS_DATA_DIR", old)
	} else {
		_ = os.Unsetenv("CAVE_TOOLS_DATA_DIR")
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
