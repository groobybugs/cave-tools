//go:build parity

// Black-box CLI differential tests. Every case runs the built TypeScript CLI
// and the Go binary against identical fixture trees, compares stdout/stderr/
// exit codes byte-for-byte after replacing implementation-private roots, and
// compares the resulting files. These cover the full tool-command surface and
// the side effects the argv-only table cannot see.
package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// allToolNames is the tools/list order shared by both implementations.
var allToolNames = []string{
	"cave__read",
	"cave__bash",
	"cave__bash_start",
	"cave__bash_status",
	"cave__bash_stop",
	"cave__grep",
	"cave__find",
	"cave__ls",
	"cave__write",
	"cave__invalidate",
	"cave__edit",
	"cave__apply_patch",
	"cave__websearch",
	"cave__webfetch",
	"cave__compress",
	"cave__status",
	"cave__configure",
}

var (
	toolErrorTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)
	toolErrorID   = regexp.MustCompile(`id=[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	toolErrorMS   = regexp.MustCompile(`elapsed=\d+ms`)
	toolErrorFile = regexp.MustCompile(`file=[0-9a-f]{16}`)
	statusClock   = regexp.MustCompile(`updated \d+s ago`)
	daemonKey     = regexp.MustCompile(`daemon-[0-9a-f]{12}`)
)

// normalizeToolErrors smooths only the generated UUID and timestamp fields.
// Counts, outcomes, codes, session hashes, file hashes and argument lists stay
// exact, and errors.flow asserts the record count independently.
func normalizeToolErrors(text string) string {
	text = toolErrorTime.ReplaceAllString(text, "<TIME>")
	text = toolErrorID.ReplaceAllString(text, "id=<ID>")
	// file= hashes the absolute fixture path, and this harness gives each side
	// its own private root. The row count, session hash and argument list below
	// still prove the record itself is identical.
	text = toolErrorFile.ReplaceAllString(text, "file=<PATHHASH>")
	return toolErrorMS.ReplaceAllString(text, "elapsed=<MS>")
}

// normalizeStatusClock removes the wall-clock age, which can cross a second
// boundary between the two sequential runs. status.fresh asserts the line is
// still present.
func normalizeStatusClock(text string) string {
	return statusClock.ReplaceAllString(text, "updated <AGE>")
}

// normalizeDaemonKey removes the build/data/config-derived socket suffix; the
// path shape is asserted by the daemon tests.
func normalizeDaemonKey(text string) string {
	return daemonKey.ReplaceAllString(text, "daemon-<KEY>")
}

// fixedToolErrorRows is a deterministic three-row error log: one not_found
// read, one soft edit failure and one soft grep failure.
func fixedToolErrorRows() []seededToolError {
	operation := func(value string) *string { return &value }
	fileHash := func(value string) *string { return &value }
	return []seededToolError{
		{
			ID: "11111111-1111-4111-8111-111111111111", CreatedAt: 1700000000000,
			Tool: "cave__read", Outcome: "soft", ReasonCode: "not_found", ElapsedMS: 3,
			Mode: "default", Operation: operation("full"), Agent: "unknown",
			SessionHash: "0123456789abcdef", FileHash: fileHash("1111111111111111"),
			ArgKeys: `["file_path"]`, ArgTypes: `{"file_path":"string"}`, ArgCount: 1,
		},
		{
			ID: "22222222-2222-4222-8222-222222222222", CreatedAt: 1700000001000,
			Tool: "cave__edit", Outcome: "soft", ReasonCode: "unknown", ElapsedMS: 4,
			Mode: "default", Operation: operation("string"), Agent: "unknown",
			SessionHash: "0123456789abcdef", FileHash: fileHash("2222222222222222"),
			ArgKeys: `["file_path","new_string","old_string"]`, ArgTypes: `{"file_path":"string","new_string":"string","old_string":"string"}`,
			ArgCount: 3,
		},
		{
			ID: "33333333-3333-4333-8333-333333333333", CreatedAt: 1700000002000,
			Tool: "cave__grep", Outcome: "soft", ReasonCode: "unknown", ElapsedMS: 5,
			Mode: "default", Agent: "unknown", SessionHash: "0123456789abcdef",
			FileHash: fileHash("3333333333333333"), ArgKeys: `["path","pattern"]`,
			ArgTypes: `{"path":"string","pattern":"string"}`, ArgCount: 2,
		},
	}
}

// TestCLIBlackBoxDifferential runs the twin-sandbox CLI table.
func TestCLIBlackBoxDifferential(t *testing.T) {
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	for _, testCase := range cliBlackBoxCases(t) {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			runCLICase(t, testCase, tsBinary, tsPrefix, goBinary)
		})
	}
}

// TestCLIEveryToolHelp exercises the "help <tool>" mapping for all 17 tools.
func TestCLIEveryToolHelp(t *testing.T) {
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	for _, name := range allToolNames {
		name := name
		t.Run(strings.TrimPrefix(name, "cave__"), func(t *testing.T) {
			runCLICase(t, cliCase{
				name:  "help-" + name,
				steps: []cliStep{{args: []string{"help", strings.TrimPrefix(name, "cave__")}}},
			}, tsBinary, tsPrefix, goBinary)
		})
	}
}

// TestCLIEveryToolExplicitHelp proves the `tool <name>` mapping reaches all 17
// tools, not only the implicit `<name>` form.
func TestCLIEveryToolExplicitHelp(t *testing.T) {
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	for _, name := range allToolNames {
		name := name
		t.Run(strings.TrimPrefix(name, "cave__"), func(t *testing.T) {
			runCLICase(t, cliCase{
				name:  "explicit-help-" + name,
				steps: []cliStep{{args: []string{"tool", strings.TrimPrefix(name, "cave__"), "--help"}}},
			}, tsBinary, tsPrefix, goBinary)
		})
	}
}

// numberLines renders lines start..end inclusive, one per line.
func numberLines(start, end int) string {
	var builder strings.Builder
	for value := start; value <= end; value++ {
		fmt.Fprintf(&builder, "%d\n", value)
	}
	return builder.String()
}

// cliBlackBoxCases is the durable CLI surface table.
func cliBlackBoxCases(t *testing.T) []cliCase {
	root := repoRoot(t)
	distDir := filepath.Join(root, "dist")
	fakeBins := fixtureWith(map[string]fixtureFile{
		"bin/rg": {Content: "#!/bin/sh\nexit 0\n", Mode: 0o755},
		"bin/fd": {Content: "#!/bin/sh\nexit 0\n", Mode: 0o755},
	})
	withExistingClaude := fixtureWith(map[string]fixtureFile{
		"CLAUDE.md": {Content: "keep me\n"},
	})
	return []cliCase{
		// read
		{name: "read.text", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", "2"}}}},
		{name: "read.offset-numbered", steps: []cliStep{{args: []string{"read", "lines.txt", "--offset", "2", "--limit", "2", "--line_numbers"}}}},
		{name: "read.signatures", steps: []cliStep{{args: []string{"read", "code.ts", "--mode", "signatures"}}}},
		{name: "read.aggressive", steps: []cliStep{{args: []string{"read", "code.ts", "--mode", "aggressive"}}}},
		{name: "read.directory", steps: []cliStep{{args: []string{"read", "sub", "--limit", "2"}}}},
		{name: "read.missing-empty-dir", steps: []cliStep{{args: []string{"read", "emptydir/nope.txt"}}}},
		{name: "read.missing-single-suggestion", steps: []cliStep{{args: []string{"read", "nope-note.txt"}}}},
		{name: "read.missing-multi-suggestion-order", steps: []cliStep{{args: []string{"read", ".txt"}}}},
		{name: "read.image", steps: []cliStep{{args: []string{"read", "img.png"}}}},
		{name: "read.pdf", steps: []cliStep{{args: []string{"read", "doc.pdf"}}}},
		{name: "read.binary", steps: []cliStep{{args: []string{"read", "binary.bin"}}}},
		{name: "read.stdin-path", steps: []cliStep{{args: []string{"read", "-", "--limit", "1"}, stdin: "note.txt"}}},
		{name: "read.only-one-stdin-value", steps: []cliStep{{args: []string{"read", "-", "--offset", "-"}, stdin: "note.txt"}}},
		{name: "read.dashdash", steps: []cliStep{{args: []string{"read", "--", "note.txt"}}}},
		{name: "read.bad-flag", steps: []cliStep{{args: []string{"read", "note.txt", "--bogus"}}}},
		{name: "read.bad-number", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", "abc"}}}},
		{name: "read.hex-number", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", "0x2"}}}},
		{name: "read.exponent-number", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", "1e1"}}}},
		{name: "read.infinity-number", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", "Infinity"}}}},
		{name: "read.empty-number", steps: []cliStep{{args: []string{"read", "note.txt", "--limit", ""}}}},
		{name: "read.force-eq", steps: []cliStep{{args: []string{"read", "note.txt", "--force=true"}}}},
		{name: "read.force-space-false", steps: []cliStep{{args: []string{"read", "note.txt", "--force", "false"}}}},

		// bash
		{name: "bash.basic", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "echo hi"}}}},
		{name: "bash.exit-meta", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "printf out; printf err >&2; exit 3"}}}},
		{name: "bash.allow-failure", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "printf out; exit 3", "--allowFailure"}}}},
		{name: "bash.workdir", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "pwd", "--workdir", "sub"}}}},
		{name: "bash.redaction", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "echo token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}}},
		{name: "bash.large-archive", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "seq 1 300"}}}},
		{name: "bash.signal", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "kill -TERM $$"}}}},
		{name: "bash.timeout", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "sleep 0.2; echo late", "--timeout", "50"}}}},
		{name: "bash.multi-args", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "echo", "a'b", "c d", "e$f"}}}},
		{name: "bash.empty-command", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", ""}}}},
		{name: "bash.raw-env", requirePOSIX: true, steps: []cliStep{{args: []string{"bash", "seq 1 50"}, env: map[string]string{"CAVE_RAW": "1"}}}},

		// grep
		{name: "grep.single-file", steps: []cliStep{{args: []string{"grep", "needle", "sub/a.txt", "--limit", "3"}}}},
		{name: "grep.glob", steps: []cliStep{{args: []string{"grep", "needle", ".", "--glob", "*.md"}}}},
		{name: "grep.ignore-case", steps: []cliStep{{args: []string{"grep", "NEEDLE", "note.txt", "--ignoreCase"}}}},
		{name: "grep.literal", steps: []cliStep{{args: []string{"grep", "beta token", "note.txt", "--literal"}}}},
		{name: "grep.context", steps: []cliStep{{args: []string{"grep", "needle", "sub/a.txt", "--context", "1"}}}},
		{name: "grep.bad-regex", steps: []cliStep{{args: []string{"grep", "[", "note.txt"}}}},

		// find
		{name: "find.single", steps: []cliStep{{args: []string{"find", "*.md", "sub"}}}},
		{name: "find.missing-dir", steps: []cliStep{{args: []string{"find", "*.txt", "nope"}}}},

		// ls
		{name: "ls.dir", steps: []cliStep{{args: []string{"ls", "sub"}}}},
		{name: "ls.offset", steps: []cliStep{{args: []string{"ls", ".", "--offset", "2", "--limit", "2"}}}},
		{name: "ls.file", steps: []cliStep{{args: []string{"ls", "note.txt"}}}},

		// write / invalidate
		{name: "write.new", steps: []cliStep{{args: []string{"write", "new.txt", "--content", "hello\nworld"}}}},
		{name: "write.unicode", steps: []cliStep{{args: []string{"write", "unicodé-新.txt", "--content", "café 日本"}}}},
		{name: "write.truncate", steps: []cliStep{{args: []string{"write", "note.txt", "--truncate"}}}},
		{name: "write.missing-content", steps: []cliStep{{args: []string{"write", "new.txt"}}}},
		{name: "write.nested", steps: []cliStep{{args: []string{"write", "sub/deep/new.txt", "--content", "x"}}}},
		{name: "invalidate.array", steps: []cliStep{{args: []string{"invalidate", `["note.txt","sub/a.txt","missing.txt"]`}}}},
		{name: "invalidate.string", steps: []cliStep{{args: []string{"invalidate", "note.txt"}}}},
		{name: "invalidate.mixed-array", steps: []cliStep{{args: []string{"invalidate", `["note.txt",2,null]`}}}},
		{name: "invalidate.bad-json", steps: []cliStep{{args: []string{"invalidate", "{}"}}}},

		// edit
		{name: "edit.replace", steps: []cliStep{{args: []string{"edit", "note.txt", "--old_string", "beta", "--new_string", "BETA"}}}},
		{name: "edit.range", steps: []cliStep{{args: []string{"edit", "lines.txt", "--start_line", "2", "--end_line", "2", "--content", "line two"}}}},
		{name: "edit.delete", steps: []cliStep{{args: []string{"edit", "lines.txt", "--start_line", "2", "--end_line", "3", "--delete"}}}},
		{name: "edit.move", steps: []cliStep{{args: []string{"edit", "lines.txt", "--start_line", "1", "--end_line", "2", "--insert_before", "5"}}}},
		{name: "edit.batch", steps: []cliStep{{args: []string{"edit", "--edits", `[{"file_path":"note.txt","old_string":"alpha","new_string":"ALPHA"},{"file_path":"sub/a.txt","old_string":"needle","new_string":"NEEDLE","replace_all":true}]`}}}},
		{name: "edit.hash-fail", steps: []cliStep{{args: []string{"edit", "note.txt", "--old_string", "beta", "--new_string", "B", "--expected_hash", "deadbeefdeadbeef"}}}},
		{name: "edit.null-line", steps: []cliStep{{args: []string{"edit", "note.txt", "--start_line", "null", "--end_line", "1", "--content", "x"}}}},
		{name: "edit.empty-old", steps: []cliStep{{args: []string{"edit", "note.txt", "--old_string", "", "--new_string", "x"}}}},
		{name: "edit.batch-object", steps: []cliStep{{args: []string{"edit", "--edits", "{}"}}}},

		// apply_patch
		{name: "patch.add", steps: []cliStep{{args: []string{"apply_patch", "--patchText", "*** Begin Patch\n*** Add File: added.txt\n+hello\n*** End Patch"}}}},
		{name: "patch.update-delete", steps: []cliStep{{args: []string{"apply_patch", "--patchText", "*** Begin Patch\n*** Update File: note.txt\n@@\n-alpha\n+ALPHA\n*** Delete File: sub/b.md\n*** End Patch"}}}},
		{name: "patch.move", steps: []cliStep{{args: []string{"apply_patch", "--patchText", "*** Begin Patch\n*** Update File: note.txt\n*** Move to: moved.txt\n@@\n-alpha\n+ALPHA\n*** End Patch"}}}},
		{name: "patch.invalid", steps: []cliStep{{args: []string{"apply_patch", "--patchText", "not a patch"}}}},

		// compress
		{name: "compress.text", steps: []cliStep{{args: []string{"compress", "a a a a b b c", "--tool_name", "bash"}}}},
		{name: "compress.json", steps: []cliStep{{args: []string{"compress", `{"a":1,"b":[1,2,3]}`, "--structured"}}}},
		{name: "compress.xml", steps: []cliStep{{args: []string{"compress", "<root><a>1</a></root>", "--structured"}}}},
		{name: "compress.budget-read", steps: []cliStep{{args: []string{"compress", numberLines(1, 120), "--tool_name", "read"}}}},
		{name: "compress.empty", steps: []cliStep{{args: []string{"compress", ""}}}},
		{name: "compress.bool-inline", steps: []cliStep{{args: []string{"compress", `{"a":1}`, "--structured=false"}}}},
		{name: "compress.expand-missing", steps: []cliStep{{args: []string{"compress", "x", "--action", "expand", "--archive_id", "deadbeef"}}}},

		// status / configure
		{name: "status.fresh", normalize: normalizeStatusClock, steps: []cliStep{{args: []string{"status"}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}}}},
		{name: "status.verbose", normalize: normalizeStatusClock, steps: []cliStep{{args: []string{"status", "--verbose"}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}}}},
		{name: "status.emit-statusline", steps: []cliStep{{args: []string{"status", "--emit-statusline"}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}}}},
		{name: "status.tool-command", normalize: normalizeStatusClock, steps: []cliStep{{args: []string{"tool", "status"}}}},
		{
			name:      "configure.set-budget-then-status",
			normalize: normalizeStatusClock,
			steps: []cliStep{
				{args: []string{"configure", "--action", "set_budget", "--tool_name", "read", "--max_lines", "17.5", "--max_chars", "0b11"}},
				{args: []string{"status", "--verbose"}, env: map[string]string{"CAVE_TOOLS_ASCII": "1"}},
			},
		},
		{name: "configure.reset-cache", steps: []cliStep{{args: []string{"configure", "--action", "reset_cache"}}}},
		{name: "configure.reset-stats", steps: []cliStep{{args: []string{"configure", "--action", "reset_stats"}}}},
		{name: "configure.bad-action", steps: []cliStep{{args: []string{"configure", "--action", "nope"}}}},

		// seeded jobs / durable-job command mappings
		{name: "jobs.seeded", steps: []cliStep{{setup: seedJob, args: []string{"jobs"}}}},
		{name: "jobs.log", steps: []cliStep{{setup: seedJob, args: []string{"jobs", "log", "jobfixed", "2"}}}},
		{name: "jobs.kill-finished", steps: []cliStep{{setup: seedJob, args: []string{"jobs", "kill", "jobfixed"}}}},
		{name: "jobs.kill-unknown", steps: []cliStep{{args: []string{"jobs", "kill", "nope"}}}},
		{name: "bash-status.seeded", steps: []cliStep{{setup: seedJob, args: []string{"bash_status", "jobfixed", "--tail_lines", "2"}}}},
		{name: "bash-stop.finished", steps: []cliStep{{setup: seedJob, args: []string{"bash_stop", "jobfixed"}}}},

		// error log
		{
			name: "errors.seeded-aggregate",
			verify: func(t *testing.T, ts, goSide *side) {
				if got := toolErrorCount(t, ts.data); got != 3 {
					t.Fatalf("TS persisted tool errors = %d, want 3", got)
				}
				if got := toolErrorCount(t, goSide.data); got != 3 {
					t.Fatalf("Go persisted tool errors = %d, want 3", got)
				}
			},
			steps: []cliStep{{setup: func(t *testing.T, s *side) {
				seedToolErrors(t, s, fixedToolErrorRows())
			}, args: []string{"errors"}}},
		},
		{
			name:      "errors.live-single",
			normalize: normalizeToolErrors,
			verify: func(t *testing.T, ts, goSide *side) {
				if got := toolErrorCount(t, ts.data); got != 1 {
					t.Fatalf("TS persisted tool errors = %d, want 1", got)
				}
				if got := toolErrorCount(t, goSide.data); got != 1 {
					t.Fatalf("Go persisted tool errors = %d, want 1", got)
				}
			},
			steps: []cliStep{
				{args: []string{"read", "nope.txt", "--session", "fixed"}},
				{args: []string{"errors"}},
			},
		},
		{name: "errors.empty", steps: []cliStep{{args: []string{"errors"}}}},
		{name: "errors.bad-limit", steps: []cliStep{{args: []string{"errors", "--limit", "0"}}}},
		{name: "errors.float-limit", steps: []cliStep{{args: []string{"errors", "--limit", "1.5"}}}},

		// init / bins
		{name: "init.fresh-then-existing", steps: []cliStep{{args: []string{"init"}}, {args: []string{"init"}}}},
		{name: "init.existing-file", fixture: withExistingClaude, steps: []cliStep{{args: []string{"init"}}}},
		{
			name:    "bins.fake-binaries",
			fixture: fakeBins,
			steps: []cliStep{{
				args: []string{"bins"},
				env:  map[string]string{"PATH": "bin" + string(os.PathListSeparator) + os.Getenv("PATH")},
			}},
		},

		// raw / filter
		{name: "raw.exit", requirePOSIX: true, steps: []cliStep{{args: []string{"raw", "printf out; printf err >&2; exit 7"}}}},
		{name: "raw.signal", requirePOSIX: true, steps: []cliStep{{args: []string{"raw", "kill -TERM $$"}}}},
		{name: "raw.stdin", requirePOSIX: true, steps: []cliStep{{args: []string{"raw", "cat"}, stdin: "piped\n"}}},
		{name: "raw.multi-args", requirePOSIX: true, steps: []cliStep{{args: []string{"raw", "echo", "a b"}}}},
		{name: "raw.dashdash", requirePOSIX: true, steps: []cliStep{{args: []string{"raw", "--", "echo", "hi"}}}},
		{name: "filter.ok", steps: []cliStep{{args: []string{"filter", "--json"}, stdin: `{"command":"make","stdout":"ok\n","stderr":"token=ghp_abcdefghijklmnopqrstuvwxyz0123456789","exitCode":0}`}}},
		{name: "filter.invalid-json", steps: []cliStep{{args: []string{"filter", "--json"}, stdin: "not json"}}},
		{name: "filter.missing-fields", steps: []cliStep{{args: []string{"filter", "--json"}, stdin: `{"stdout":"x"}`}}},
		{name: "filter.no-json", steps: []cliStep{{args: []string{"filter"}, stdin: `{}`}}},
		{name: "filter.extra-arg", steps: []cliStep{{args: []string{"filter", "--json", "extra"}, stdin: `{}`}}},

		// daemon control (not running)
		{
			name:      "daemon.status-not-running",
			normalize: normalizeDaemonKey,
			steps: []cliStep{{
				args: []string{"daemon", "status"},
				env:  map[string]string{"CAVE_TOOLS_GO_DIST_DIR": distDir},
			}},
		},
		{
			name:      "daemon.stop-not-running",
			normalize: normalizeDaemonKey,
			steps: []cliStep{{
				args: []string{"daemon", "stop"},
				env:  map[string]string{"CAVE_TOOLS_GO_DIST_DIR": distDir},
			}},
		},
		{name: "daemon.usage", steps: []cliStep{{args: []string{"daemon", "bogus"}}}},

		// top-level help / web validation (no network)
		{name: "help.top-level", steps: []cliStep{{args: []string{"help"}}}},
		{name: "help.unknown", steps: []cliStep{{args: []string{"help", "bogus"}}}},

		// explicit `tool <name>` routing
		{name: "explicit.read", steps: []cliStep{{args: []string{"tool", "read", "note.txt", "--limit", "1"}}}},
		{name: "explicit.bash", requirePOSIX: true, steps: []cliStep{{args: []string{"tool", "bash", "echo explicit"}}}},
		{name: "explicit.write", steps: []cliStep{{args: []string{"tool", "write", "explicit.txt", "--content", "x"}}}},
		{name: "explicit.invalidate", steps: []cliStep{{args: []string{"tool", "invalidate", `["note.txt"]`}}}},
		{name: "explicit.edit", steps: []cliStep{{args: []string{"tool", "edit", "note.txt", "--old_string", "alpha", "--new_string", "A"}}}},
		{name: "explicit.compress", steps: []cliStep{{args: []string{"tool", "compress", "a a b"}}}},
		{name: "explicit.configure", steps: []cliStep{{args: []string{"tool", "configure", "--action", "reset_cache"}}}},
		{name: "explicit.status", normalize: normalizeStatusClock, steps: []cliStep{{args: []string{"tool", "status"}}}},
		{name: "websearch.missing-query", steps: []cliStep{{args: []string{"websearch"}}}},
		{name: "webfetch.missing-url", steps: []cliStep{{args: []string{"webfetch"}}}},
		{name: "webfetch.reject-file-scheme", steps: []cliStep{{args: []string{"webfetch", "file:///etc/hostname"}}}},
	}
}

// TestRawAndFilterNoCompression proves raw passthrough neither rewrites nor
// compresses, and that `filter` never executes the command it is given and
// preserves its caller's output on failure.
func TestRawAndFilterNoCompression(t *testing.T) {
	requireMode(t)
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	fixture := baseFixture()
	tsSide := newSide(t, "ts", tsBinary, tsPrefix, fixture)
	goSide := newSide(t, "go", goBinary, nil, fixture)

	large := numberLines(1, 300)
	for _, binary := range []struct {
		label  string
		side   *side
		binary string
		prefix []string
	}{
		{label: "ts", side: tsSide, binary: tsBinary, prefix: tsPrefix},
		{label: "go", side: goSide, binary: goBinary, prefix: nil},
	} {
		raw := runCommandOutcome(t, binary.binary, binary.prefix, binary.side.root, binary.side.baseEnv(nil), "", "raw", "seq 1 300")
		if raw.code != 0 || raw.stdout != large || raw.stderr != "" {
			t.Fatalf("%s raw output = code %d stdout %q stderr %q", binary.label, raw.code, raw.stdout, raw.stderr)
		}
		if strings.Contains(raw.stdout, "[cave:") {
			t.Fatalf("%s raw output was compressed: %q", binary.label, raw.stdout)
		}
		direct := runCommandOutcome(t, binary.binary, binary.prefix, binary.side.root, binary.side.baseEnv(map[string]string{"CAVE_RAW": "1"}), "", "bash", "seq 1 300")
		if direct.code != 0 || direct.stdout != large || direct.stderr != "" {
			t.Fatalf("%s CAVE_RAW bash = code %d stdout %q stderr %q", binary.label, direct.code, direct.stdout, direct.stderr)
		}

		filter := runCommandOutcome(t, binary.binary, binary.prefix, binary.side.root, binary.side.baseEnv(nil), `{"command":"touch filter-created.txt","stdout":"original\n","stderr":"","exitCode":0}`, "filter", "--json")
		if filter.code != 0 || filter.stdout != `{"stdout":"original\n","stderr":""}`+"\n" {
			t.Fatalf("%s filter = code %d stdout %q stderr %q", binary.label, filter.code, filter.stdout, filter.stderr)
		}
		if _, err := os.Stat(filepath.Join(binary.side.root, "filter-created.txt")); !os.IsNotExist(err) {
			t.Fatalf("%s filter executed the command (stat err %v)", binary.label, err)
		}
		failure := runCommandOutcome(t, binary.binary, binary.prefix, binary.side.root, binary.side.baseEnv(nil), "not json", "filter", "--json")
		if failure.code != 1 || failure.stdout != "" || failure.stderr == "" {
			t.Fatalf("%s filter failure = code %d stdout %q stderr %q", binary.label, failure.code, failure.stdout, failure.stderr)
		}
	}
}
