package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain pins one data dir for the whole package: storage keeps a process
// singleton connection, so switching CAVE_TOOLS_DATA_DIR per test would leave
// the database handle pointing at a deleted directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-bash-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	_ = os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// run invokes the tool with the package data dir.
func run(t *testing.T, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

func text(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	content, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", result.Content[0])
	}
	return content.Text
}

// TestToolMetadata pins the declaration to src/tools/bash.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__bash" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if Tool.Annotations != nil {
		t.Fatalf("annotations = %s, want none", Tool.Annotations)
	}
	const wantDescription = "Shell with RTK pre-rewrite, secret redaction, output budget, and failure compression. On Claude Code/opencode use native shell by default (CLI-filtered); use this for RTK pre-rewrite or guaranteed failure compression. Never prefix rtk yourself."
	if Tool.Description != wantDescription {
		t.Fatalf("description = %q, want %q", Tool.Description, wantDescription)
	}
	const wantInputSchema = `{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string","description":"5-10 word summary"},"workdir":{"type":"string","description":"Use instead of cd"},"timeout":{"type":"number","description":"ms; 0 disables, max 600000","default":120000},"allowFailure":{"type":"boolean","description":"Return non-zero exits as text, not errors","default":false},"redact_secrets":{"type":"boolean","default":true}},"required":["command","description"]}`
	if got := string(Tool.InputSchema); got != wantInputSchema {
		t.Fatalf("input schema = %s, want %s", got, wantInputSchema)
	}
}

// TestEcho ports test.mjs:1 (bash echo).
func TestEcho(t *testing.T) {
	result := run(t, map[string]any{"command": "echo 'Hello from Cave Tools!'", "description": "Test echo command"})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); !strings.Contains(got, "Hello from Cave Tools!") {
		t.Fatalf("text = %q", got)
	}
}

// TestSoftFailure ports test.mjs:1 (allowFailure keeps isError unset).
func TestSoftFailure(t *testing.T) {
	result := run(t, map[string]any{
		"command":      "sh -c 'echo out; echo err >&2; exit 7'",
		"description":  "Test soft failure command",
		"allowFailure": true,
	})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	got := text(t, result)
	for _, want := range []string{"[exit: 7]", "err", "out"} {
		if !strings.Contains(got, want) {
			t.Fatalf("text missing %q: %q", want, got)
		}
	}
}

// TestHardFailure ports test.mjs:1 (non-zero exit is an error).
func TestHardFailure(t *testing.T) {
	result := run(t, map[string]any{"command": "sh -c 'exit 7'", "description": "Test hard failure command"})
	if !result.IsError {
		t.Fatal("isError = false, want true")
	}
	if got := text(t, result); !strings.Contains(got, "[exit: 7]") {
		t.Fatalf("text = %q", got)
	}
}

// TestSecretRedaction ports test.mjs:1b.
func TestSecretRedaction(t *testing.T) {
	bearer := run(t, map[string]any{
		"command":     "echo 'Authorization: Bearer sk-live-abc123xyz789secret'",
		"description": "Test bearer token redaction",
	})
	got := text(t, bearer)
	if !strings.Contains(got, "[REDACTED:Bearer token]") || strings.Contains(got, "sk-live-abc123xyz789secret") {
		t.Fatalf("bearer text = %q", got)
	}

	apiKey := run(t, map[string]any{
		"command":     "echo 'api_key=AKIAIOSFODNN7EXAMPLE'",
		"description": "Test API key redaction",
	})
	if got := text(t, apiKey); !strings.Contains(got, "[REDACTED:AWS key]") {
		t.Fatalf("api key text = %q", got)
	}

	noRedact := run(t, map[string]any{
		"command":        "echo 'api_key=AKIAIOSFODNN7EXAMPLE'",
		"description":    "Test redaction opt-out",
		"redact_secrets": false,
	})
	if got := text(t, noRedact); !strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("opt-out text = %q", got)
	}
}

// TestInvalidTimeout ports test.mjs:10 (negative/oversize rejected, 0 disables).
func TestInvalidTimeout(t *testing.T) {
	negative := run(t, map[string]any{"command": "echo hi", "description": "negative timeout", "timeout": float64(-1)})
	if !negative.IsError || !strings.Contains(text(t, negative), "Invalid timeout value: -1") {
		t.Fatalf("negative = %+v", negative)
	}

	oversize := run(t, map[string]any{"command": "echo hi", "description": "oversize timeout", "timeout": float64(600001)})
	got := text(t, oversize)
	if !oversize.IsError || !strings.Contains(got, "must be <= 600000ms") {
		t.Fatalf("oversize = %q", got)
	}

	notANumber := run(t, map[string]any{"command": "echo hi", "description": "string timeout", "timeout": "abc"})
	if !notANumber.IsError || !strings.Contains(text(t, notANumber), "Invalid timeout value: abc") {
		t.Fatalf("not-a-number = %+v", notANumber)
	}

	zero := run(t, map[string]any{"command": "echo no-timeout-ok", "description": "zero timeout disables timer", "timeout": float64(0)})
	if zero.IsError || !strings.Contains(text(t, zero), "no-timeout-ok") {
		t.Fatalf("zero = %+v", zero)
	}
}

// TestVerbatimTruncation ports test.mjs:10 (cat keeps head+tail over 500 lines).
func TestVerbatimTruncation(t *testing.T) {
	dir := t.TempDir()
	longFile := filepath.Join(dir, "long.txt")
	var lines []string
	for i := 1; i <= 600; i++ {
		lines = append(lines, "row "+strconv.Itoa(i))
	}
	if err := os.WriteFile(longFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"command": "cat " + longFile, "description": "verbatim policy long output", "allowFailure": true})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); !strings.Contains(got, "lines truncated") {
		t.Fatalf("text = %q", got)
	}
}

// TestCompressibleBudget ports test.mjs:10 (seq gets budgeted).
func TestCompressibleBudget(t *testing.T) {
	result := run(t, map[string]any{"command": "seq 1 200", "description": "compressible long output", "allowFailure": true})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); !strings.Contains(got, "lines truncated") {
		t.Fatalf("text = %q", got)
	}
}

// TestArchiveHandle ports test.mjs:10 (trimmed output archived behind a handle).
func TestArchiveHandle(t *testing.T) {
	result, err := Tool.Handler(context.Background(), map[string]any{
		"command":      "yes 'abcdefghijklmnopqrstuvwxyz0123456789-abcdefghij' | head -n 2000",
		"description":  "archive-sized output",
		"allowFailure": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, result)
	handleRE := regexp.MustCompile(`\[cave: full output \((\d+) lines\) at (\S+) — cave__read offset/limit or cave__grep\]`)
	handle := handleRE.FindStringSubmatch(got)
	if handle == nil {
		t.Fatalf("no handle in %q", got[len(got)-400:])
	}
	archived, err := os.ReadFile(handle[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(string(archived), "\n")) != atoi(handle[1]) {
		t.Fatalf("archive line count = %d, handle says %s", len(strings.Split(string(archived), "\n")), handle[1])
	}
	lines := strings.Split(strings.TrimRight(string(archived), "\n"), "\n")
	if len(lines) < 1999 {
		t.Fatalf("archive has %d lines", len(lines))
	}
	for _, line := range lines {
		if line != "abcdefghijklmnopqrstuvwxyz0123456789-abcdefghij" {
			t.Fatalf("unexpected archived line %q", line)
		}
	}
	if len(archived) <= len(got) {
		t.Fatal("archive should hold more than was shown")
	}
}

// TestCliMode ports the escape of stderr/exit code into _meta.caveCli.
func TestCliMode(t *testing.T) {
	result := run(t, map[string]any{
		"command":     "echo o; echo e >&2; exit 3",
		"description": "cli",
		"__cli":       true,
	})
	if !result.IsError {
		t.Fatal("isError = false, want true")
	}
	if got := text(t, result); strings.Contains(got, "stderr:") || strings.Contains(got, "[exit: 3]") {
		t.Fatalf("stdout carries folded stderr/notes: %q", got)
	}
	meta, ok := result.Meta["caveCli"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.caveCli = %#v", result.Meta)
	}
	if meta["exitCode"] != 3 {
		t.Fatalf("exitCode = %#v", meta["exitCode"])
	}
	if stderr, _ := meta["stderr"].(string); !strings.Contains(stderr, "e") {
		t.Fatalf("meta stderr = %q", stderr)
	}
}

// TestMcpFoldsStderr ports test.mjs:CLI (MCP output unchanged).
func TestMcpFoldsStderr(t *testing.T) {
	result := run(t, map[string]any{"command": "echo o; echo e >&2; exit 3", "description": "t"})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	got := text(t, result)
	if !strings.Contains(got, "stderr:\ne") || !strings.Contains(got, "[exit: 3]") {
		t.Fatalf("text = %q", got)
	}
	if result.Meta != nil {
		t.Fatalf("_meta = %#v, want nil", result.Meta)
	}
}

// TestShellExitCode pins the shell exit convention.
func TestShellExitCode(t *testing.T) {
	code := 0
	signal := "SIGTERM"
	seven := 7
	tests := []struct {
		name   string
		result rt.RunCommandResult
		want   int
	}{
		{name: "timeout", result: rt.RunCommandResult{TimedOut: true, ExitCode: &code}, want: 124},
		{name: "signal", result: rt.RunCommandResult{Signal: &signal}, want: 128 + signalNumber("SIGTERM")},
		{name: "exit", result: rt.RunCommandResult{ExitCode: &seven}, want: 7},
		{name: "null", result: rt.RunCommandResult{}, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ShellExitCode(test.result); got != test.want {
				t.Fatalf("ShellExitCode = %d, want %d", got, test.want)
			}
		})
	}
	if got := ShellExitCode(rt.RunCommandResult{Signal: &signal}); got != 143 {
		t.Fatalf("SIGTERM exit = %d, want 143", got)
	}
}

// TestCliShell pins the $SHELL bash/zsh preference.
func TestCliShell(t *testing.T) {
	env := rt.EnvFromEnviron(os.Environ())
	env["SHELL"] = "/bin/zsh"
	ctx := rt.RunWithContext(context.Background(), &rt.CallContext{Cwd: "/", Env: env, ClientID: "local"}, func(ctx context.Context) struct{} {
		if got := CliShell(ctx); got != "/bin/zsh" {
			t.Fatalf("CliShell = %q, want /bin/zsh", got)
		}
		env["SHELL"] = "/usr/bin/fish"
		if got := CliShell(ctx); got != rt.DefaultShell(ctx) {
			t.Fatalf("CliShell = %q, want default shell", got)
		}
		return struct{}{}
	})
	_ = ctx
}

// TestFilterShellOutputPassthrough pins the passthrough branch.
func TestFilterShellOutputPassthrough(t *testing.T) {
	filtered := FilterShellOutput(context.Background(), ShellOutput{
		Command: "git commit -m msg",
		Stdout:  "line1\nline2   \nline3\n",
	})
	if filtered.Stdout != "line1\nline2   \nline3\n" {
		t.Fatalf("stdout = %q", filtered.Stdout)
	}
	if filtered.Stderr != "" {
		t.Fatalf("stderr = %q", filtered.Stderr)
	}
}

// TestFilterShellOutputRedaction pins redaction on/off in the pipeline.
func TestFilterShellOutputRedaction(t *testing.T) {
	on := true
	off := false
	input := ShellOutput{Command: "echo x", Stdout: "token=abcdef0123456789abcdef0123456789\n"}
	input.Redact = &on
	if got := FilterShellOutput(context.Background(), input).Stdout; !strings.Contains(got, "REDACTED") {
		t.Fatalf("redacted stdout = %q", got)
	}
	input.Redact = &off
	if got := FilterShellOutput(context.Background(), input).Stdout; !strings.Contains(got, "abcdef0123456789abcdef0123456789") {
		t.Fatalf("raw stdout = %q", got)
	}
}

// TestRtkDoubleWrap ports test.mjs "strip one rtk wrap then run".
func TestRtkDoubleWrap(t *testing.T) {
	result := run(t, map[string]any{"command": "rtk echo CAVE_RTK_DOUBLEWRAP", "description": "strip one rtk wrap then run"})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); !strings.Contains(got, "CAVE_RTK_DOUBLEWRAP") {
		t.Fatalf("text = %q", got)
	}
}

// TestJSCoercions pins the ECMAScript coercion helpers on JSON values.
func TestJSCoercions(t *testing.T) {
	stringCases := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "true"},
		{false, "false"},
		{float64(600000), "600000"},
		{float64(1.5), "1.5"},
		{float64(1e21), "1e+21"},
		{float64(0.000001), "0.000001"},
		{float64(0.0000001), "1e-7"},
		{[]any{float64(1), "a", nil}, "1,a,"},
		{map[string]any{}, "[object Object]"},
		{json.Number("12"), "12"},
	}
	for _, test := range stringCases {
		if got := jsString(test.value); got != test.want {
			t.Fatalf("jsString(%#v) = %q, want %q", test.value, got, test.want)
		}
	}

	numberCases := []struct {
		value any
		want  float64
	}{
		{nil, 0},
		{" 12 ", 12},
		{"", 0},
		{"0x10", 16},
		{"0b101", 5},
		{"1e3", 1000},
		{"Infinity", math.Inf(1)},
		{"-Infinity", math.Inf(-1)},
		{"inf", math.NaN()},
		{"12px", math.NaN()},
		{"+0x10", math.NaN()},
		{true, 1},
		{[]any{}, 0},
	}
	for _, test := range numberCases {
		got := jsNumber(test.value)
		if math.IsNaN(test.want) {
			if !math.IsNaN(got) {
				t.Fatalf("jsNumber(%#v) = %v, want NaN", test.value, got)
			}
			continue
		}
		if got != test.want {
			t.Fatalf("jsNumber(%#v) = %v, want %v", test.value, got, test.want)
		}
	}

	if !jsTruthy([]any{}) || jsTruthy("") || jsTruthy(float64(0)) || jsTruthy(nil) || !jsTruthy("x") {
		t.Fatal("jsTruthy mismatch")
	}
}

func atoi(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

// TestTimeoutNote pins the timeout note and exitFailed handling.
func TestTimeoutNote(t *testing.T) {
	result := run(t, map[string]any{"command": "sleep 5", "description": "timeout", "timeout": float64(30)})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.Contains(got, "[timed out after 30ms - process group killed]") {
		t.Fatalf("text = %q", got)
	}
}

// TestSignalNote pins the signal note.
func TestSignalNote(t *testing.T) {
	result := run(t, map[string]any{"command": "kill -TERM $$", "description": "signal"})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.Contains(got, "[killed by signal SIGTERM]") {
		t.Fatalf("text = %q", got)
	}
	if got := ShellExitCode(rt.RunCommandResult{Signal: ptrString("SIGTERM")}); got != 143 {
		t.Fatalf("ShellExitCode = %d, want 143", got)
	}
}

func ptrString(value string) *string { return &value }

// TestEmptyCommandMatchesNodeSpawnRejection pins the exactly-empty command
// case: Node's normalizeSpawnArguments rejects an empty `file` before shell
// handling, so the tool reports "Command failed to launch" instead of running
// the shell. Whitespace-only commands still reach the shell.
func TestEmptyCommandMatchesNodeSpawnRejection(t *testing.T) {
	const wantMessage = "Command failed to launch\n" +
		"Original command: \n" +
		"Executed command: \n" +
		"Message: The argument 'file' cannot be empty. Received ''"

	t.Run("cli error", func(t *testing.T) {
		result := run(t, map[string]any{"command": "", "description": "empty", "__cli": true})
		if !result.IsError {
			t.Fatalf("isError = false, want true")
		}
		if got := text(t, result); got != wantMessage {
			t.Fatalf("text = %q, want %q", got, wantMessage)
		}
		// TS's catch block returns no _meta, so the CLI prints the text to
		// stderr and exits 1 through the generic isError path.
		if result.Meta != nil {
			t.Fatalf("meta = %v, want nil", result.Meta)
		}
	})

	t.Run("cli allowFailure", func(t *testing.T) {
		result := run(t, map[string]any{"command": "", "description": "empty", "__cli": true, "allowFailure": true})
		if result.IsError {
			t.Fatalf("isError = true, want false with allowFailure")
		}
		if got := text(t, result); got != wantMessage {
			t.Fatalf("text = %q, want %q", got, wantMessage)
		}
	})

	t.Run("mcp result folds message into text", func(t *testing.T) {
		result := run(t, map[string]any{"command": "", "description": "empty"})
		if !result.IsError {
			t.Fatalf("isError = false, want true")
		}
		if got := text(t, result); got != wantMessage {
			t.Fatalf("text = %q, want %q", got, wantMessage)
		}
	})

	t.Run("whitespace-only still runs the shell", func(t *testing.T) {
		result := run(t, map[string]any{"command": " ", "description": "space", "__cli": true})
		if result.IsError {
			t.Fatalf("isError = true: %s", text(t, result))
		}
		if got := text(t, result); got != "" {
			t.Fatalf("text = %q, want empty", got)
		}
		meta, ok := result.Meta["caveCli"].(map[string]any)
		if !ok {
			t.Fatalf("meta = %v", result.Meta)
		}
		if meta["exitCode"] != 0 {
			t.Fatalf("exitCode = %v, want 0", meta["exitCode"])
		}
	})

	t.Run("workdir error wins over the empty command", func(t *testing.T) {
		result := run(t, map[string]any{"command": "", "description": "empty", "__cli": true, "workdir": filepath.Join(t.TempDir(), "missing")})
		if !result.IsError {
			t.Fatalf("isError = false, want true")
		}
		got := text(t, result)
		if !strings.Contains(got, "ENOENT") || strings.Contains(got, "cannot be empty") {
			t.Fatalf("text = %q, want the cwd ENOENT launch failure", got)
		}
	})
}
