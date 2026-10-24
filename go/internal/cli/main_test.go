package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain pins one stable data dir for the whole package. storage caches its
// SQLite connection on first open, so per-test t.Setenv("CAVE_TOOLS_DATA_DIR")
// would leave later tests reading a deleted temp directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-cli-test-")
	if err != nil {
		panic(err)
	}
	old, hadOld := os.LookupEnv("CAVE_TOOLS_DATA_DIR")
	if err := os.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data")); err != nil {
		panic(err)
	}
	code := m.Run()
	if hadOld {
		_ = os.Setenv("CAVE_TOOLS_DATA_DIR", old)
	} else {
		_ = os.Unsetenv("CAVE_TOOLS_DATA_DIR")
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
