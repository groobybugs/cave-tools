package configure

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
	"github.com/groobybugs/cave-tools/go/internal/tools/status"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

type configureParityPayload struct {
	Results []types.ToolResult `json:"results"`
	Status  string             `json:"status"`
	Applied []string           `json:"applied"`
}

type applyCase struct {
	Tool string `json:"tool"`
	Text string `json:"text"`
}

// configureParityCalls exercises JS number coercion, fractional budgets, the
// optional max_chars carry-over, Object.entries ordering, and the drain cases.
var configureParityCalls = []map[string]any{
	{"action": "set_budget", "tool_name": "grep", "max_lines": 5, "head_lines": 2, "tail_lines": 2},
	{"action": "set_budget", "tool_name": "no-such-tool", "max_lines": 10},
	{"action": "set_budget", "tool_name": "web", "max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 100},
	{"action": "set_budget", "tool_name": "web", "max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 0},
	{"action": "set_budget", "tool_name": "zzz", "max_lines": 1, "head_lines": 1, "tail_lines": 1},
	{"action": "set_budget", "tool_name": "aaa", "max_lines": 2, "head_lines": 2, "tail_lines": 2},
	{"action": "set_budget", "tool_name": "frac", "max_lines": 12.7, "head_lines": 2.5, "tail_lines": 1.25, "max_chars": 12.7},
	{"action": "set_budget", "tool_name": "frac", "max_lines": 6, "head_lines": 1.5, "tail_lines": 1.25},
	{"action": "set_budget", "tool_name": "neg", "max_lines": 4, "head_lines": 1.5, "tail_lines": 1.25, "max_chars": -5},
	{"action": "set_budget", "tool_name": "neglines", "max_lines": -3, "head_lines": -2.5, "tail_lines": -1},
	{"action": "set_budget", "tool_name": "inf", "max_lines": "Infinity", "head_lines": 2, "tail_lines": 1},
	{"action": "set_budget", "tool_name": "infchars", "max_lines": 10, "max_chars": "Infinity"},
	{"action": "set_budget", "tool_name": "radix", "max_lines": "0b11", "head_lines": "0o17", "tail_lines": "0x10", "max_chars": "0o17"},
	{"action": "set_budget", "tool_name": "2", "max_lines": 2, "head_lines": 2, "tail_lines": 2},
	{"action": "set_budget", "tool_name": "10", "max_lines": 10, "head_lines": 5, "tail_lines": 5},
	{"action": "set_budget", "tool_name": "1", "max_lines": 1, "head_lines": 1, "tail_lines": 1},
	{"action": "set_budget", "tool_name": "coerce1", "max_lines": []any{}, "head_lines": []any{7}, "tail_lines": nil, "max_chars": []any{12.5}},
	{"action": "set_budget", "tool_name": "coerce2", "max_lines": "1e999", "max_chars": "1e999"},
	{"action": "set_budget", "tool_name": "coerce3", "max_lines": []any{"5"}, "max_chars": "abc"},
	{"action": "set_budget", "tool_name": "coerce4", "max_lines": map[string]any{"x": 1}, "max_chars": map[string]any{}},
	{"action": "reset_cache"},
	{"action": "reset_stats"},
	{"action": "frobnicate"},
	{},
	{"action": 42},
}

var configureParityApply = []applyCase{
	{Tool: "frac", Text: "a\nb\nc\nd\ne\nf\ng\nh"},
	{Tool: "neg", Text: "one\ntwo\nthree\nfour\nfive\nsix"},
	{Tool: "neglines", Text: "one\ntwo\nthree\nfour\nfive\nsix"},
	{Tool: "inf", Text: "one\ntwo\nthree"},
	{Tool: "infchars", Text: "one\ntwo\nthree"},
	{Tool: "radix", Text: "a\nb\nc\nd\ne"},
	{Tool: "2", Text: "a\nb\nc\nd\ne"},
}

// TestHandlerParity runs the TS handler and the Go handler over the same call
// sequence in fresh processes, then compares every ToolResult, the status
// report and applyBudget output. The Go side runs in a test-binary child so
// budgets and the storage singleton cannot leak from other tests.
func TestHandlerParity(t *testing.T) {
	root := repoRoot(t)
	distConfigure := filepath.Join(root, "dist", "tools", "configure.js")
	distStatus := filepath.Join(root, "dist", "tools", "status.js")
	distUtils := filepath.Join(root, "dist", "compression", "utils.js")
	for _, dist := range []string{distConfigure, distStatus, distUtils} {
		if _, err := os.Stat(dist); err != nil {
			t.Skipf("%s is missing: run pnpm build first", dist)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}

	dir := t.TempDir()
	goJSON := runGoConfigure(t, dir)

	script := filepath.Join(t.TempDir(), "run-configure.mjs")
	source := "import { configureTool } from " + jsonString(pathToFileURL(distConfigure)) + ";\n" +
		"import { statusTool } from " + jsonString(pathToFileURL(distStatus)) + ";\n" +
		"import { applyBudget } from " + jsonString(pathToFileURL(distUtils)) + ";\n" +
		"const calls = " + mustJSON(configureParityCalls) + ";\n" +
		"const applyCases = " + mustJSON(configureParityApply) + ";\n" +
		"const results = [];\n" +
		"for (const args of calls) results.push(await configureTool.handler(args));\n" +
		"const statusResult = await statusTool.handler({});\n" +
		"const applied = applyCases.map((c) => applyBudget(c.text, c.tool));\n" +
		"process.stdout.write(JSON.stringify({ results, status: statusResult.content[0].text, applied }));\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Dir = root
	cmd.Env = append(testenv.Environ(),
		"CAVE_TOOLS_DATA_DIR="+dir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(dir, "claude"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node handler: %v (stderr %s)", err, stderr.String())
	}
	if stdout.String() != string(goJSON) {
		t.Errorf("handler mismatch\nTS: %s\nGo: %s", stdout.String(), goJSON)
	}
}

// TestConfigureHelperProcess is the child side of TestHandlerParity: a fresh
// process with the temp data dir and default budgets.
func TestConfigureHelperProcess(t *testing.T) {
	callsPath := os.Getenv("CAVE_TOOLS_CONFIGURE_CALLS")
	resultPath := os.Getenv("CAVE_TOOLS_CONFIGURE_RESULT")
	if callsPath == "" || resultPath == "" {
		t.Skip("helper process only")
	}
	var calls []map[string]any
	if err := readJSON(callsPath, &calls); err != nil {
		t.Fatal(err)
	}
	var applyCases []applyCase
	if err := readJSON(os.Getenv("CAVE_TOOLS_CONFIGURE_APPLY"), &applyCases); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	payload := configureParityPayload{
		Results: make([]types.ToolResult, 0, len(calls)),
		Applied: make([]string, 0, len(applyCases)),
	}
	for _, call := range calls {
		result, err := Tool.Handler(ctx, call)
		if err != nil {
			t.Fatalf("handler %v: %v", call, err)
		}
		payload.Results = append(payload.Results, result)
	}
	statusResult, err := status.Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	payload.Status = resultText(t, statusResult)
	for _, apply := range applyCases {
		payload.Applied = append(payload.Applied, compression.ApplyBudget(ctx, apply.Text, apply.Tool))
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGoConfigure(t *testing.T, dir string) []byte {
	t.Helper()
	scratch := t.TempDir()
	callsPath := filepath.Join(scratch, "calls.json")
	applyPath := filepath.Join(scratch, "apply.json")
	resultPath := filepath.Join(scratch, "result.json")
	if err := os.WriteFile(callsPath, []byte(mustJSON(configureParityCalls)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(applyPath, []byte(mustJSON(configureParityApply)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureHelperProcess$")
	cmd.Env = append(os.Environ(),
		"CAVE_TOOLS_CONFIGURE_CALLS="+callsPath,
		"CAVE_TOOLS_CONFIGURE_APPLY="+applyPath,
		"CAVE_TOOLS_CONFIGURE_RESULT="+resultPath,
		"CAVE_TOOLS_DATA_DIR="+dir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(dir, "claude"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go helper: %v (stderr %s)", err, stderr.String())
	}
	encoded, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read helper result: %v (stderr %s)", err, stderr.String())
	}
	return encoded
}

func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__configure" {
		t.Errorf("name = %q", Tool.Name)
	}
	if len(Tool.Annotations) != 0 {
		t.Errorf("configure must not be read-only: %s", Tool.Annotations)
	}
	if Tool.Description != "Set per-tool budgets, reset cache, or reset stats." {
		t.Errorf("description = %q", Tool.Description)
	}
	want := `{"type":"object","properties":{"action":{"type":"string","enum":["set_budget","reset_cache","reset_stats"]},"tool_name":{"type":"string","description":"set_budget target (bash, read, grep, web)"},"max_lines":{"type":"number"},"head_lines":{"type":"number"},"tail_lines":{"type":"number"},"max_chars":{"type":"number","description":"0 disables; omit to keep"}},"required":["action"]}`
	if string(Tool.InputSchema) != want {
		t.Errorf("inputSchema = %s", Tool.InputSchema)
	}
}

func TestSetBudget(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	result, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "grep",
		"max_lines": 5, "head_lines": 2, "tail_lines": 2,
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("isError")
	}
	if got := resultText(t, result); got != "Set budget for grep: max=5, head=2, tail=2, chars=6000" {
		t.Errorf("text = %q", got)
	}
	budget := compression.GetAllBudgets()["grep"]
	if budget.MaxLines != 5 || budget.HeadLines != 2 || budget.TailLines != 2 {
		t.Errorf("budget = %+v", budget)
	}
	if budget.MaxChars == nil || *budget.MaxChars != 6000 {
		t.Errorf("maxChars = %v, want preserved 6000", budget.MaxChars)
	}
	trimmed := compression.ApplyBudget(ctx, "a\nb\nc\nd\ne\nf\ng\n", "grep")
	if !strings.Contains(trimmed, "lines truncated") {
		t.Errorf("budget tightening did not truncate: %q", trimmed)
	}

	// An unknown tool name is permissive and echoed back.
	bad, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "no-such-tool", "max_lines": 10,
	})
	if err != nil || bad.IsError {
		t.Fatalf("bad tool: %v %v", err, bad.IsError)
	}
	if got := resultText(t, bad); got != "Set budget for no-such-tool: max=10, head=50, tail=50" {
		t.Errorf("text = %q", got)
	}

	// Restore the defaults so later tests are unaffected.
	if _, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "grep",
		"max_lines": 60, "head_lines": 30, "tail_lines": 30,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSetBudgetMaxChars(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	if _, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "web",
		"max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 100,
	}); err != nil {
		t.Fatal(err)
	}
	if budget := compression.GetAllBudgets()["web"]; budget.MaxChars == nil || *budget.MaxChars != 100 {
		t.Errorf("maxChars = %v, want 100", budget.MaxChars)
	}

	result, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "web",
		"max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(t, result); got != "Set budget for web: max=120, head=60, tail=60" {
		t.Errorf("text = %q", got)
	}
	if budget := compression.GetAllBudgets()["web"]; budget.MaxChars != nil {
		t.Errorf("maxChars = %v, want disabled", budget.MaxChars)
	}

	if _, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "web",
		"max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 8000,
	}); err != nil {
		t.Fatal(err)
	}
	budget := compression.GetAllBudgets()["web"]
	if budget.MaxLines != 120 || budget.HeadLines != 60 || budget.TailLines != 60 ||
		budget.MaxChars == nil || *budget.MaxChars != 8000 {
		t.Errorf("budget = %+v", budget)
	}
}

// TestSetBudgetFractionalText reproduces the W3e Muse finding: configure
// numbers must stay intact in the confirmation text (and status) instead of
// being truncated to int64 by the shared budget store.
func TestSetBudgetFractionalText(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	result, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "frac",
		"max_lines": 12.7, "head_lines": 2.5, "tail_lines": 1.25,
		"max_chars": 12.7,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Set budget for frac: max=12.7, head=2.5, tail=1.25, chars=12.7"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	budget := compression.GetAllBudgets()["frac"]
	if budget.MaxLines != 12.7 || budget.HeadLines != 2.5 || budget.TailLines != 1.25 ||
		budget.MaxChars == nil || *budget.MaxChars != 12.7 {
		t.Fatalf("stored budget = %+v, want 12.7/2.5/1.25 chars=12.7", budget)
	}
	statusResult, err := status.Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	statusText := resultText(t, statusResult)
	if !strings.Contains(statusText, "frac: max=12.7, head=2.5, tail=1.25, chars=12.7") {
		t.Fatalf("status did not match stored fraction:\n%s", statusText)
	}
}

// TestSetBudgetNumberSyntax covers Number()'s binary/octal literals and the
// String() coercion on the confirmation text.
func TestSetBudgetNumberSyntax(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	result, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": "radix",
		"max_lines": "0b11", "head_lines": "0o17", "tail_lines": "0x10",
		"max_chars": "0o17",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Set budget for radix: max=3, head=15, tail=16, chars=15"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestResetAndUnknownActions(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	cache, err := Tool.Handler(ctx, map[string]any{"action": "reset_cache"})
	if err != nil || cache.IsError {
		t.Fatalf("reset_cache: %v", err)
	}
	if got := resultText(t, cache); got != "Cache reset" {
		t.Errorf("text = %q", got)
	}

	stats, err := Tool.Handler(ctx, map[string]any{"action": "reset_stats"})
	if err != nil || stats.IsError {
		t.Fatalf("reset_stats: %v", err)
	}
	if got := resultText(t, stats); got != "Statistics reset" {
		t.Errorf("text = %q", got)
	}

	unknown, err := Tool.Handler(ctx, map[string]any{"action": "frobnicate"})
	if err != nil {
		t.Fatal(err)
	}
	if !unknown.IsError || resultText(t, unknown) != "Unknown action: frobnicate" {
		t.Errorf("unknown = %+v", unknown)
	}

	missing, err := Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.IsError || resultText(t, missing) != "Unknown action: undefined" {
		t.Errorf("missing = %+v", missing)
	}
}

func TestJsCoercions(t *testing.T) {
	ctx, cleanup := configureTestContext(t)
	defer cleanup()

	result, err := Tool.Handler(ctx, map[string]any{
		"action": "set_budget", "tool_name": 0,
		"max_lines": "7", "head_lines": float64(1), "tail_lines": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(t, result); got != "Set budget for bash: max=7, head=1, tail=1, chars=6000" {
		t.Errorf("text = %q", got)
	}
	if _, err := Tool.Handler(ctx, map[string]any{"action": 42}); err != nil {
		t.Fatal(err)
	}
}

func configureTestContext(t *testing.T) (context.Context, func()) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	ctx := context.Background()
	compression.ResetStats(ctx)
	return ctx, func() { compression.ResetStats(ctx) }
}

func resultText(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatalf("no content")
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return text.Text
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func pathToFileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
