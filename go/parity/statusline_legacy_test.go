//go:build parity

// Legacy stats differential: a sessions/*.json written before
// savings.compressionSavedChars existed (or holding null) must produce
// byte-identical status output and statusline badges on both implementations,
// including the MCP server's in-process badge refresh.
//
// TS migrates the file through normalizeLegacyStats, where num() maps a missing
// or null field to 0 (src/storage/db.ts:446). The badge then reads the stored
// number, so Go must never fall back to savedChars on this path.
package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacySavingsShapes is every shape savings.compressionSavedChars can take in
// a legacy session file: absent (pre-field row), explicit 0, explicit value and
// explicit null.
var legacySavingsShapes = []struct {
	name    string
	savings string
}{
	{name: "absent", savings: `"totalCalls":4,"rawChars":1000,"compressedChars":500,"dedupSavedChars":0,"savedChars":500`},
	{name: "explicit-zero", savings: `"totalCalls":4,"rawChars":1000,"compressedChars":500,"compressionSavedChars":0,"dedupSavedChars":0,"savedChars":500`},
	{name: "explicit-value", savings: `"totalCalls":4,"rawChars":1000,"compressedChars":600,"compressionSavedChars":400,"dedupSavedChars":0,"savedChars":500`},
	{name: "explicit-null", savings: `"totalCalls":4,"rawChars":1000,"compressedChars":500,"compressionSavedChars":null,"dedupSavedChars":0,"savedChars":500`},
}

// legacySessionFile renders one pre-DB session file. The pid is the test
// process, so both sides treat the row as live and keep it visible.
func legacySessionFile(pid, now int64, savings string) string {
	return fmt.Sprintf(`{
  "pid": %d,
  "sessionStart": %d,
  "updatedAt": %d,
  "cache": { "hits": 3, "misses": 1, "total": 4, "hitRate": 0.75, "filesTracked": 2 },
  "savings": { %s, "estimatedTokensSaved": 125, "byTool": { "bash": { "calls": 4, "rawChars": 1000, "compressedChars": 500, "savedChars": 500 } } },
  "rtk": { "rewrites": 0, "alreadyWrapped": 0, "passthrough": 0 },
  "budgets": {},
  "bounces": { "totalBounces": 0, "totalWastedChars": 0, "byExtension": {} }
}
`, pid, now, now, savings)
}

// writeLegacySession drops the fixture in a side's legacy sessions directory.
func writeLegacySession(t *testing.T, s *side, pid int64, body string) {
	t.Helper()
	dir := filepath.Join(s.data, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// badgePath is where both implementations write the rendered badge.
func badgePath(s *side) string {
	return filepath.Join(s.claude, ".cave-tools-statusline-suffix")
}

// readBadge returns the badge bytes, failing when the file is missing.
func readBadge(t *testing.T, s *side) string {
	t.Helper()
	content, err := os.ReadFile(badgePath(s))
	if err != nil {
		t.Fatalf("%s badge: %v", s.label, err)
	}
	return string(content)
}

// normalizeStatusOut masks the one clock-derived field; paths and counts stay
// exact.
func normalizeStatusOut(s *side, text string) string {
	return statusClock.ReplaceAllString(s.normalize(text), "updated <SECONDS>s ago")
}

// TestStatuslineLegacyStatsDifferential drives every status surface from the
// same legacy session file on both implementations.
func TestStatuslineLegacyStatsDifferential(t *testing.T) {
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)

	for _, shape := range legacySavingsShapes {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			tsSide := newSide(t, "ts", tsBinary, tsPrefix, baseFixture())
			goSide := newSide(t, "go", goBinary, nil, baseFixture())

			pid := int64(os.Getpid())
			body := legacySessionFile(pid, time.Now().UnixMilli(), shape.savings)
			writeLegacySession(t, tsSide, pid, body)
			writeLegacySession(t, goSide, pid, body)

			env := map[string]string{"CAVE_TOOLS_ASCII": "1"}
			for _, args := range [][]string{
				{"status"},
				{"status", "--verbose"},
				{"status", "--emit-statusline"},
			} {
				want := tsSide.run(t, args, "", env)
				got := goSide.run(t, args, "", env)
				if normalizeStatusOut(goSide, got.stdout) != normalizeStatusOut(tsSide, want.stdout) ||
					normalizeStatusOut(goSide, got.stderr) != normalizeStatusOut(tsSide, want.stderr) ||
					got.code != want.code {
					t.Fatalf("%v differs\n go: %+v\n ts: %+v", args, got, want)
				}
			}
			// Only --emit-statusline writes the badge.
			if badgeGo, badgeTS := readBadge(t, goSide), readBadge(t, tsSide); badgeGo != badgeTS {
				t.Fatalf("emit-statusline badge = %q, want %q", badgeGo, badgeTS)
			}

			// MCP in-process badge path: one side-effect-free tool call schedules
			// the same debounced build/write, so the badge must be rebuilt with
			// identical bytes. Start from no badge to prove the refresh ran.
			for _, s := range []*side{tsSide, goSide} {
				if err := os.Remove(badgePath(s)); err != nil {
					t.Fatalf("remove badge: %v", err)
				}
			}
			tsMCP := startMCP(t, tsBinary, tsPrefix, tsSide.root, tsSide.baseEnv(env))
			goMCP := startMCP(t, goBinary, nil, goSide.root, goSide.baseEnv(env))
			handshake(t, &mcpPair{ts: tsMCP, goSide: goMCP})
			call := map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/call",
				"params":  map[string]any{"name": "cave__status", "arguments": map[string]any{}},
			}
			if response := tsMCP.exchange(t, call); response["error"] != nil {
				t.Fatalf("ts status call: %v", response["error"])
			}
			goMCP.write(t, call)
			if response := goMCP.read(t); response["error"] != nil {
				t.Fatalf("go status call: %v", response["error"])
			}
			if err := tsMCP.stop(t, 5*time.Second); err != nil {
				t.Fatalf("stop ts MCP: %v", err)
			}
			if err := goMCP.stop(t, 5*time.Second); err != nil {
				t.Fatalf("stop go MCP: %v", err)
			}
			badgeTS, badgeGo := readBadge(t, tsSide), readBadge(t, goSide)
			if badgeGo != badgeTS {
				t.Fatalf("MCP badge = %q, want %q", badgeGo, badgeTS)
			}
			if strings.TrimSpace(badgeTS) == "" {
				t.Fatal("MCP badge is empty on both sides")
			}
		})
	}
}
