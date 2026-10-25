// Package testenv builds hermetic child environments for tests that spawn the
// TypeScript CLI, its MCP server, or Node tool modules.
//
// The TypeScript reference implementation inspects the environment for
// subagent signals (src/compression/utils.ts:isSubagentContext). A leaked
// signal changes read freshness, and an unknown-but-similar key makes the
// child log "cave-tools: unknown subagent-like env keys (ignored; not forcing
// fresh): ..." to stderr, which breaks CLI/MCP differentials that compare
// stderr byte-for-byte. Tests therefore scrub those keys before spawning.
package testenv

import (
	"os"
	"strings"
)

// SubagentLike reports whether key participates in the subagent detection
// documented above. It mirrors the union of the three TS predicates:
//
//   - KNOWN_SUBAGENT_KEYS (CLINE_TASK_ID, CLAUDE_CODE_SUBAGENT,
//     ANTHROPIC_SUBAGENT, CURSOR_TASK_ID),
//   - TASK_ID_STYLE, /(?:^|_)TASK_ID$/,
//   - UNKNOWN_SUBAGENT_CANDIDATE, /(?:SUBAGENT|^CLAUDE_CODE_)/, which covers
//     every key the child would warn about,
//
// plus the CAVE_TOOLS_FORCE_FRESH override that forces fresh reads.
func SubagentLike(key string) bool {
	if key == "CAVE_TOOLS_FORCE_FRESH" {
		return true
	}
	if strings.HasPrefix(key, "CLAUDE_CODE_") || strings.Contains(key, "SUBAGENT") {
		return true
	}
	return key == "TASK_ID" || strings.HasSuffix(key, "_TASK_ID")
}

// Scrub returns environ without subagent-like KEY=VALUE entries.
func Scrub(environ []string) []string {
	scrubbed := make([]string, 0, len(environ))
	for _, entry := range environ {
		if key, _, ok := strings.Cut(entry, "="); ok && SubagentLike(key) {
			continue
		}
		scrubbed = append(scrubbed, entry)
	}
	return scrubbed
}

// Environ is os.Environ with subagent-like keys removed.
func Environ() []string { return Scrub(os.Environ()) }

// ScrubMap deletes subagent-like keys from a key/value environment map and
// returns it for chaining.
func ScrubMap(env map[string]string) map[string]string {
	for key := range env {
		if SubagentLike(key) {
			delete(env, key)
		}
	}
	return env
}

// ScrubProcess removes subagent-like keys from the test process itself. Call
// it from TestMain before m.Run() in packages that compare in-process Go
// behaviour with a scrubbed TS child: removing a known trigger from only one
// side would make both sides see different read-freshness state.
func ScrubProcess() {
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && SubagentLike(key) {
			_ = os.Unsetenv(key)
		}
	}
}
