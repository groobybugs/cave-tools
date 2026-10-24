// Package storage persists cave-tools stats, archives and background jobs.
//
// Port of src/storage/data-dir.ts and src/storage/db.ts. Both implementations
// must be able to open the same SQLite file at the same path and read each
// other's rows, so schema text, pragmas, file names and permissions are
// identical to TS.
package storage

import (
	"os"
	"path/filepath"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// CaveToolsDataDir returns the directory holding SQLite stats, archives and
// jobs.
//
// Override with CAVE_TOOLS_DATA_DIR. This directory holds SQLite stats,
// archives, and jobs — not the in-memory file-content cache. Parallel MCP
// processes share stats here but never share read-body cache (correctness).
// port of src/storage/data-dir.ts:caveToolsDataDir
func CaveToolsDataDir() string {
	var home string
	if h, err := os.UserHomeDir(); err == nil {
		home = h
	}
	return caveToolsDataDir(os.Getenv("CAVE_TOOLS_DATA_DIR"), home)
}

// caveToolsDataDir is the pure part of CaveToolsDataDir so tests can pin the
// home directory.
func caveToolsDataDir(override, home string) string {
	if trimmed := jsre.Trim(override); trimmed != "" {
		return trimmed
	}
	return filepath.Join(home, ".cache", "cave-tools")
}
