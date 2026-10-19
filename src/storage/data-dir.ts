import { homedir } from "os";
import { join } from "path";

// Kept free of SQLite so thin clients (daemon client, socket paths) can use it.
export function caveToolsDataDir(): string {
  // Override with CAVE_TOOLS_DATA_DIR. This directory holds SQLite stats,
  // archives, and jobs — not the in-memory file-content cache. Parallel MCP
  // processes share stats here but never share read-body cache (correctness).
  const override = process.env.CAVE_TOOLS_DATA_DIR?.trim();
  return override || join(homedir(), ".cache", "cave-tools");
}
