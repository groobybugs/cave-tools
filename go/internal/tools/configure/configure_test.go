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
	"github.com/groobybugs/cave-tools/go/internal/tools/status"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestHandlerParity runs the TS handler and the Go handler over the same call
// sequence and fixture, then compares every ToolResult plus the final status
// report (which includes TS's insertion-ordered budgets).
func TestHandlerParity(t *testing.T) {
	root := repoRoot(t)
	distConfigure := filepath.Join(root, "dist", "tools", "configure.js")
	distStatus := filepath.Join(root, "dist", "tools", "status.js")
	if _, err := os.Stat(distConfigure); err != nil {
		t.Skip("dist/tools/configure.js is missing: run pnpm build first")
	}
	if _, err := os.Stat(distStatus); err != nil {
		t.Skip("dist/tools/status.js is missing: run pnpm build first")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}

	calls := []map[string]any{
		{"action": "set_budget", "tool_name": "grep", "max_lines": 5, "head_lines": 2, "tail_lines": 2},
		{"action": "set_budget", "tool_name": "no-such-tool", "max_lines": 10},
		{"action": "set_budget", "tool_name": "web", "max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 100},
		{"action": "set_budget", "tool_name": "web", "max_lines": 120, "head_lines": 60, "tail_lines": 60, "max_chars": 0},
		{"action": "set_budget", "tool_name": "zzz", "max_lines": 1, "head_lines": 1, "tail_lines": 1},
		{"action": "set_budget", "tool_name": "aaa", "max_lines": 2, "head_lines": 2, "tail_lines": 2},
		{"action": "reset_cache"},
		{"action": "reset_stats"},
		{"action": "frobnicate"},
		{},
		{"action": 42},
	}

	type payload struct {
		Results []types.ToolResult `json:"results"`
		Status  string             `json:"status"`
	}

	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	ctx := context.Background()
	compression.ResetStats(ctx)
	goPayload := payload{Results: make([]types.ToolResult, 0, len(calls))}
	for _, call := range calls {
		result, err := Tool.Handler(ctx, call)
		if err != nil {
			t.Fatalf("go handler %v: %v", call, err)
		}
		goPayload.Results = append(goPayload.Results, result)
	}
	statusResult, err := status.Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("go status: %v", err)
	}
	if text, ok := statusResult.Content[0].(types.TextContent); ok {
		goPayload.Status = text.Text
	}
	goJSON, err := json.Marshal(goPayload)
	if err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "run-configure.mjs")
	source := "import { configureTool } from " + jsonString(pathToFileURL(distConfigure)) + ";\n" +
		"import { statusTool } from " + jsonString(pathToFileURL(distStatus)) + ";\n" +
		"const calls = " + mustJSON(calls) + ";\n" +
		"const results = [];\n" +
		"for (const args of calls) results.push(await configureTool.handler(args));\n" +
		"const statusResult = await statusTool.handler({});\n" +
		"process.stdout.write(JSON.stringify({ results, status: statusResult.content[0].text }));\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
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
