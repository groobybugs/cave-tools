package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools/applypatch"
	"github.com/groobybugs/cave-tools/go/internal/tools/bash"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstart"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstatus"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstop"
	"github.com/groobybugs/cave-tools/go/internal/tools/compress"
	"github.com/groobybugs/cave-tools/go/internal/tools/configure"
	"github.com/groobybugs/cave-tools/go/internal/tools/edit"
	"github.com/groobybugs/cave-tools/go/internal/tools/find"
	"github.com/groobybugs/cave-tools/go/internal/tools/grep"
	"github.com/groobybugs/cave-tools/go/internal/tools/invalidate"
	"github.com/groobybugs/cave-tools/go/internal/tools/ls"
	"github.com/groobybugs/cave-tools/go/internal/tools/read"
	"github.com/groobybugs/cave-tools/go/internal/tools/status"
	"github.com/groobybugs/cave-tools/go/internal/tools/webfetch"
	"github.com/groobybugs/cave-tools/go/internal/tools/websearch"
	"github.com/groobybugs/cave-tools/go/internal/tools/write"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// minimalEnv is a placeholder: TestMain pins one stable CAVE_TOOLS_DATA_DIR
// for the whole package (storage caches its SQLite connection).
func minimalEnv(t *testing.T) {
	t.Helper()
}

func TestJoinCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "safe words", argv: []string{"git", "log", "--oneline", "-n", "3"}, want: "git log --oneline -n 3"},
		{name: "quotes and empty", argv: []string{"echo", "it's", ""}, want: `echo 'it'\''s' ''`},
		{name: "shell syntax stays literal", argv: []string{"printf", "%s|", "a b"}, want: `printf '%s|' 'a b'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := joinCommandArgs(test.argv, "linux")
			if err != nil {
				t.Fatalf("joinCommandArgs: %v", err)
			}
			if got != test.want {
				t.Fatalf("joinCommandArgs = %q, want %q", got, test.want)
			}
		})
	}
}

func TestJoinCommandArgsWindows(t *testing.T) {
	_, err := joinCommandArgs([]string{"dir", "x"}, "win32")
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("err = %v, want UsageError", err)
	}
	want := `on Windows pass the command as one quoted string, e.g. "dir | findstr x"`
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestParseToolArgs(t *testing.T) {
	noStdin := func() (string, error) { return "", errors.New("stdin not expected") }
	parse := func(t *testing.T, tool types.ToolDefinition, argv []string, stdin ...func() (string, error)) parsedToolArgs {
		t.Helper()
		reader := noStdin
		if len(stdin) > 0 {
			reader = stdin[0]
		}
		parsed, err := parseToolArgs(tool, argv, reader)
		if err != nil {
			t.Fatalf("parseToolArgs(%v): %v", argv, err)
		}
		return parsed
	}

	t.Run("positionals and coercion", func(t *testing.T) {
		parsed := parse(t, read.Tool, []string{"/x/file.ts", "--line-numbers", "--limit", "20", "--force=false"})
		want := map[string]any{"file_path": "/x/file.ts", "line_numbers": true, "limit": float64(20), "force": false}
		if !reflect.DeepEqual(parsed.Args, want) {
			t.Fatalf("args = %#v, want %#v", parsed.Args, want)
		}
	})

	t.Run("grep", func(t *testing.T) {
		parsed := parse(t, grep.Tool, []string{"needle", "src", "--ignoreCase", "--context=2"})
		want := map[string]any{"pattern": "needle", "path": "src", "ignoreCase": true, "context": float64(2)}
		if !reflect.DeepEqual(parsed.Args, want) {
			t.Fatalf("args = %#v, want %#v", parsed.Args, want)
		}
	})

	t.Run("ls", func(t *testing.T) {
		parsed := parse(t, ls.Tool, []string{"."})
		if !reflect.DeepEqual(parsed.Args, map[string]any{"path": "."}) {
			t.Fatalf("args = %#v", parsed.Args)
		}
	})

	t.Run("edit", func(t *testing.T) {
		parsed := parse(t, edit.Tool, []string{"/f", "old", "new", "--replace_all", "true"})
		want := map[string]any{"file_path": "/f", "old_string": "old", "new_string": "new", "replace_all": true}
		if !reflect.DeepEqual(parsed.Args, want) {
			t.Fatalf("args = %#v, want %#v", parsed.Args, want)
		}
	})

	t.Run("array positionals", func(t *testing.T) {
		parsed := parse(t, invalidate.Tool, []string{"/a", "/b"})
		if !reflect.DeepEqual(parsed.Args, map[string]any{"file_paths": []any{"/a", "/b"}}) {
			t.Fatalf("args = %#v", parsed.Args)
		}
	})

	t.Run("array flag", func(t *testing.T) {
		parsed := parse(t, invalidate.Tool, []string{"--file_paths", `["/a"]`})
		values, ok := parsed.Args["file_paths"].([]any)
		if !ok || len(values) != 1 || values[0] != "/a" {
			t.Fatalf("args = %#v", parsed.Args)
		}
	})

	t.Run("object flag", func(t *testing.T) {
		parsed := parse(t, edit.Tool, []string{"--edits", `[{"start_line":1}]`, "--file-path", "/f"})
		edits, ok := parsed.Args["edits"].([]any)
		if !ok || len(edits) != 1 {
			t.Fatalf("args = %#v", parsed.Args)
		}
		if parsed.Args["file_path"] != "/f" {
			t.Fatalf("file_path = %#v", parsed.Args["file_path"])
		}
	})

	t.Run("bash command remainder", func(t *testing.T) {
		parsed := parse(t, bash.Tool, []string{"--timeout", "5000", "git", "log", "--oneline", "-n", "3"})
		want := map[string]any{"timeout": float64(5000), "command": "git log --oneline -n 3", "description": "cave-tools CLI"}
		if !reflect.DeepEqual(parsed.Args, want) {
			t.Fatalf("args = %#v, want %#v", parsed.Args, want)
		}
	})

	t.Run("bash separator", func(t *testing.T) {
		parsed := parse(t, bash.Tool, []string{"--session", "s1", "--", "echo", "--timeout"})
		if parsed.Args["command"] != "echo --timeout" || !parsed.HasSession || parsed.SessionID != "s1" {
			t.Fatalf("parsed = %#v", parsed)
		}
	})

	t.Run("session", func(t *testing.T) {
		parsed := parse(t, read.Tool, []string{"/f", "--session", "abc"})
		if !parsed.HasSession || parsed.SessionID != "abc" {
			t.Fatalf("session = %q/%v", parsed.SessionID, parsed.HasSession)
		}
	})

	t.Run("stdin value", func(t *testing.T) {
		parsed := parse(t, write.Tool, []string{"/f", "-"}, func() (string, error) { return "from stdin", nil })
		if parsed.Args["content"] != "from stdin" {
			t.Fatalf("content = %#v", parsed.Args["content"])
		}
	})

	t.Run("hex and binary numbers", func(t *testing.T) {
		parsed := parse(t, read.Tool, []string{"/f", "--limit", "0x10", "--offset", "0b11"})
		if parsed.Args["limit"] != float64(16) || parsed.Args["offset"] != float64(3) {
			t.Fatalf("args = %#v", parsed.Args)
		}
	})

	t.Run("duplicate flag last wins", func(t *testing.T) {
		parsed := parse(t, read.Tool, []string{"/f", "--limit", "10", "--limit", "20"})
		if parsed.Args["limit"] != float64(20) {
			t.Fatalf("limit = %#v", parsed.Args["limit"])
		}
	})

	t.Run("help skips validation", func(t *testing.T) {
		parsed := parse(t, read.Tool, []string{"--help"})
		if !parsed.Help {
			t.Fatal("help = false")
		}
	})
}

func TestParseToolArgsUsageErrors(t *testing.T) {
	noStdin := func() (string, error) { return "x", nil }
	tests := []struct {
		name string
		tool types.ToolDefinition
		argv []string
		want string
	}{
		{name: "missing required", tool: read.Tool, argv: nil, want: "missing required: file_path"},
		{name: "unknown flag", tool: read.Tool, argv: []string{"/f", "--bogus", "1"}, want: "unknown flag --bogus"},
		{name: "number", tool: read.Tool, argv: []string{"/f", "--limit", "abc"}, want: `--limit expects a number, got "abc"`},
		{name: "boolean", tool: read.Tool, argv: []string{"/f", "--force=maybe"}, want: `--force expects true or false, got "maybe"`},
		{name: "missing value", tool: read.Tool, argv: []string{"/f", "--limit"}, want: "--limit needs a value"},
		{name: "unexpected positional", tool: status.Tool, argv: []string{"extra"}, want: `unexpected argument "extra"`},
		{name: "array json", tool: edit.Tool, argv: []string{"--edits", "nope"}, want: "--edits expects a JSON array"},
		{name: "object json", tool: edit.Tool, argv: []string{"--edits", `{"a":1}`}, want: "--edits expects a JSON array"},
		{name: "second stdin", tool: compress.Tool, argv: []string{"-", "-"}, want: "only one value can be read from stdin (-)"},
		{name: "session value", tool: read.Tool, argv: []string{"/f", "--session"}, want: "--session needs a value"},
		{name: "command twice", tool: bash.Tool, argv: []string{"--command", "x", "--", "y"}, want: "give the command either positionally or with --command"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseToolArgs(test.tool, test.argv, noStdin)
			var usageErr *UsageError
			if !errors.As(err, &usageErr) {
				t.Fatalf("err = %v, want UsageError", err)
			}
			if usageErr.Message != test.want {
				t.Fatalf("message = %q, want %q", usageErr.Message, test.want)
			}
		})
	}
}

const wantGeneral = "cave-tools <tool> [args...] [--param value...]   (run `cave-tools help <tool>` for params)\n\n  read         Use instead of Read.\n  bash         Shell with RTK pre-rewrite, secret redaction, output budget, and failure compression.\n  bash_start   Run a long command (builds, tests, installs) detached when its final output should be compressed; returns a jobId, output goes to a log file.\n  bash_status  Check a cave__bash_start job: state, exit code, duration, log output.\n  bash_stop    Stop a cave__bash_start job by signalling its process group; escalates to SIGKILL after 3s.\n  grep         Use instead of Grep (ripgrep).\n  find         Use instead of Glob/find (fd or Node fallback).\n  ls           Use instead of shell ls.\n  write        Create or overwrite one file and refresh the read cache.\n  invalidate   Drop cave__read cache entries (no disk write).\n  edit         Edit a file; always supply file_path explicitly (never inferred from prior reads).\n  apply_patch  Apply a multi-file add/update/delete/move patch.\n  websearch    Use instead of WebSearch (Exa/Parallel); results redacted and budget-compressed.\n  webfetch     Use instead of WebFetch.\n  compress     Compress text to fewer tokens (ANSI strip, line budget, JSON/XML extraction).\n  status       Session stats: cache hit rate, tokens saved, budgets, RTK availability.\n  configure    Set per-tool budgets, reset cache, or reset stats.\n\nThe status tool runs as `cave-tools tool status` (`cave-tools status` is the savings report).\nOther commands: mcp, status, jobs, bins, init, raw <command...> (no rewrite or compression), filter --json (compress given output)."

const wantReadHelp = "read: Use instead of Read. Returns a stub if the file is unchanged since the last read. Images return as image blocks, PDFs as resources; binary files are rejected.\n\nusage: cave-tools read <file_path> [--offset <number>] [--limit <number>] [--mode <string>] [--line_numbers] [--force]\n\n  --file_path                string, required — Absolute path\n  --offset                   number, default 1 — 1-indexed start line\n  --limit                    number, default 200\n  --mode                     string, default \"full\", one of full|signatures|aggressive — signatures: TS/JS/Rust signatures only; aggressive: drop comments/blank lines. Both ignore offset/limit.\n  --line_numbers             boolean, default false — Tag lines and append a file_hash/range_checksum footer for cave__edit range mode. Disables codebook compression.\n  --force                    boolean, default false — Bypass dedup cache\n\nPositionals fill required params, then optional ones, in the order above.\nA value of - reads that param from stdin. --session <id> reuses read state across calls (needs CAVE_TOOLS_DAEMON=1)."

const wantBashStartHelp = "bash_start: Run a long command (builds, tests, installs) detached when its final output should be compressed; returns a jobId, output goes to a log file. Poll with cave__bash_status, stop with cave__bash_stop. Never sleep-poll. Servers/watchers: native background shell.\n\nusage: cave-tools bash_start <command...> [--description <string>] [--workdir <string>]\n\n  --command                  string, required\n  --description              string — 5-10 word summary\n  --workdir                  string — Use instead of cd\n\nOne argument runs verbatim as shell (e.g. \"ls | wc -l\"); several arguments are each quoted, so shell syntax inside them stays literal.\nA value of - reads that param from stdin. --session <id> reuses read state across calls (needs CAVE_TOOLS_DAEMON=1)."

const wantBashHelp = "bash: Shell with RTK pre-rewrite, secret redaction, output budget, and failure compression. On Claude Code/opencode use native shell by default (CLI-filtered); use this for RTK pre-rewrite or guaranteed failure compression. Never prefix rtk yourself.\n\nusage: cave-tools bash <command...> [--description <string>] [--workdir <string>] [--timeout <number>] [--allowFailure] [--redact_secrets]\n\n  --command                  string, required\n  --description              string — 5-10 word summary\n  --workdir                  string — Use instead of cd\n  --timeout                  number, default 120000 — ms; 0 disables, max 600000\n  --allowFailure             boolean, default false — Return non-zero exits as text, not errors\n  --redact_secrets           boolean, default true\n\nOne argument runs verbatim as shell (e.g. \"ls | wc -l\"); several arguments are each quoted, so shell syntax inside them stays literal.\nA value of - reads that param from stdin. --session <id> reuses read state across calls (needs CAVE_TOOLS_DAEMON=1)."

func allToolDefinitions() []types.ToolDefinition {
	return []types.ToolDefinition{
		read.Tool,
		bash.Tool,
		bashstart.Tool,
		bashstatus.Tool,
		bashstop.Tool,
		grep.Tool,
		find.Tool,
		ls.Tool,
		write.Tool,
		invalidate.Tool,
		edit.Tool,
		applypatch.Tool,
		websearch.Tool,
		webfetch.Tool,
		compress.Tool,
		status.Tool,
		configure.Tool,
	}
}

// installToolFixture makes the CLI see a fixed tool registry.
func installToolFixture(t *testing.T, tools []types.ToolDefinition, call func(context.Context, string, map[string]any, *rt.CallContext) (types.ToolResult, error)) {
	t.Helper()
	old := hooks
	byName := make(map[string]types.ToolDefinition, len(tools))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
		names = append(names, tool.Name)
	}
	hooks = integration{
		ToolNames:  func() []string { return names },
		IsToolName: func(name string) bool { _, ok := byName[name]; return ok },
		LoadTool: func(name string) (types.ToolDefinition, error) {
			tool, ok := byName[name]
			if !ok {
				return types.ToolDefinition{}, errors.New("unknown tool")
			}
			return tool, nil
		},
		CallTool: func(ctx context.Context, name string, args map[string]any, callCtx *rt.CallContext) (types.ToolResult, error) {
			return call(ctx, name, args, callCtx)
		},
	}
	t.Cleanup(func() { hooks = old })
}

func TestToolHelpMatchesTypeScript(t *testing.T) {
	tests := []struct {
		name string
		tool types.ToolDefinition
		want string
	}{
		{name: "read", tool: read.Tool, want: wantReadHelp},
		{name: "bash_start", tool: bashstart.Tool, want: wantBashStartHelp},
		{name: "bash", tool: bash.Tool, want: wantBashHelp},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := toolHelp(test.tool); got != test.want {
				t.Fatalf("toolHelp mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, test.want)
			}
		})
	}
}

func TestGeneralHelpMatchesTypeScript(t *testing.T) {
	installToolFixture(t, allToolDefinitions(), nil)
	got, err := generalHelp()
	if err != nil {
		t.Fatalf("generalHelp: %v", err)
	}
	if got != wantGeneral {
		t.Fatalf("generalHelp mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantGeneral)
	}
}

func TestFormatResult(t *testing.T) {
	result := types.ToolResult{Content: []types.Content{
		types.Text("hello"),
		types.Text("world"),
		types.Image("YWJjZA==", "image/png"),
		types.ResourceBlock("file:///x", "", "YWJjZA=="),
	}}
	want := "hello\nworld\n[image image/png 4 B]\n[resource unknown 4 B]"
	if got := formatResult(result); got != want {
		t.Fatalf("formatResult = %q, want %q", got, want)
	}
}

func TestBase64ByteLength(t *testing.T) {
	tests := map[string]int{"": 0, "YQ==": 1, "YWI=": 2, "YWJj": 3, "Y W J j": 3}
	for input, want := range tests {
		if got := base64ByteLength(input); got != want {
			t.Errorf("base64ByteLength(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestFirstSentence(t *testing.T) {
	tests := map[string]string{
		"Use instead of Read. Returns more.": "Use instead of Read.",
		"no sentence end":                    "no sentence end",
		"a.b c":                              "a.b c",
		"dot at end.":                        "dot at end.",
		"a. b. c.":                           "a.",
	}
	for input, want := range tests {
		if got := firstSentence(input); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRunRawCommand(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("raw shell exit/signal semantics are POSIX")
	}
	minimalEnv(t)

	run := func(argv []string, stdin string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), argv, strings.NewReader(stdin), &stdout, &stderr, "test")
		return stdout.String(), stderr.String(), code
	}

	stdout, stderr, code := run([]string{"raw", "echo x; echo y >&2; exit 4"}, "")
	if code != 4 || stdout != "x\n" || stderr != "y\n" {
		t.Fatalf("raw exit = %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	stdout, stderr, code = run([]string{"raw", "--", "printf", "%s|", "a b"}, "")
	if code != 0 || stdout != "a b|" || stderr != "" {
		t.Fatalf("raw multi = %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	stdout, _, code = run([]string{"raw", "cat"}, "piped")
	if code != 0 || stdout != "piped" {
		t.Fatalf("raw stdin = %d stdout=%q", code, stdout)
	}

	if goruntime.GOOS != "windows" {
		_, _, code = run([]string{"raw", "kill -TERM $$"}, "")
		if code != 143 {
			t.Fatalf("raw signal exit = %d, want 143", code)
		}
	}

	_, stderr, code = run([]string{"raw"}, "")
	if code != 2 || stderr != "usage: cave-tools raw [--] <command...>\n" {
		t.Fatalf("raw usage = %d stderr=%q", code, stderr)
	}
}

func TestRunFilterCommand(t *testing.T) {
	minimalEnv(t)
	root := t.TempDir()
	big := strings.Repeat("same noisy line\n", 400)
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	input, _ := json.Marshal(map[string]any{
		"command":  "touch SHOULD_NOT_EXIST; make",
		"stdout":   big,
		"stderr":   "token=" + token,
		"exitCode": 0,
	})

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"filter", "--json", "--session", "s1"}, bytes.NewReader(input), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("filter exit = %d stderr=%q", code, stderr.String())
	}
	var output struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("filter JSON: %v (%q)", err, stdout.String())
	}
	if len(output.Stdout) >= len(big)/4 {
		t.Fatalf("stdout not compressed: %d >= %d", len(output.Stdout), len(big)/4)
	}
	if strings.Contains(output.Stderr, token) {
		t.Fatalf("stderr not redacted: %q", output.Stderr)
	}
	if !strings.HasPrefix(stdout.String(), `{"stdout":`) {
		t.Fatalf("JSON key order lost: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NOT_EXIST")); err == nil {
		t.Fatal("filter executed the command")
	}

	failures := []struct {
		name  string
		argv  []string
		input string
		code  int
		part  string
	}{
		{name: "invalid json", argv: []string{"filter", "--json"}, input: "not json", code: 1, part: "cave-tools filter:"},
		{name: "missing fields", argv: []string{"filter", "--json"}, input: `{"stdout":"x"}`, code: 1, part: "stdin must be JSON {command, stdout, stderr?, exitCode?}"},
		{name: "not an object", argv: []string{"filter", "--json"}, input: `[]`, code: 1, part: "stdin must be JSON"},
		{name: "json flag required", argv: []string{"filter"}, input: `{}`, code: 2, part: "usage: cave-tools filter --json"},
		{name: "unexpected arg", argv: []string{"filter", "--json", "x"}, input: `{}`, code: 2, part: `unexpected argument "x"`},
		{name: "session without value", argv: []string{"filter", "--json", "--session"}, input: `{}`, code: 2, part: `unexpected argument "--session"`},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Run(context.Background(), failure.argv, strings.NewReader(failure.input), &out, &errOut, "test")
			if code != failure.code {
				t.Fatalf("exit = %d, want %d", code, failure.code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on failure", out.String())
			}
			if !strings.Contains(errOut.String(), failure.part) {
				t.Fatalf("stderr = %q, want substring %q", errOut.String(), failure.part)
			}
		})
	}
}

func TestBinsDisplay(t *testing.T) {
	if got := binsDisplay(""); got != "(missing)" {
		t.Fatalf("binsDisplay(\"\") = %q", got)
	}
	if got := binsDisplay("/usr/bin/rg"); got != "/usr/bin/rg" {
		t.Fatalf("binsDisplay(path) = %q", got)
	}
}
