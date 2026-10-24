package status

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain pins one temp data dir for the package: storage keeps a process
// singleton connection, so a test must never reach the real ~/.cache database.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-status-")
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

func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__status" {
		t.Errorf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Errorf("annotations = %s", Tool.Annotations)
	}
	if Tool.Description != "Session stats: cache hit rate, tokens saved, budgets, RTK availability." {
		t.Errorf("description = %q", Tool.Description)
	}
	if string(Tool.InputSchema) != `{"type":"object","properties":{}}` {
		t.Errorf("inputSchema = %s", Tool.InputSchema)
	}
}

// TestBounceTieInsertionOrder ports TS's stable sort over Object.entries:
// equal bounce counts keep the per-extension Map insertion order (.zzz before
// .aaa), not an alphabetical approximation.
func TestBounceTieInsertionOrder(t *testing.T) {
	ctx := context.Background()
	compression.ResetStats(ctx)
	t.Cleanup(func() { compression.ResetStats(ctx) })

	for _, name := range []string{"a.zzz", "a.aaa"} {
		compression.RecordRead(ctx, name, true, 100)
		compression.RecordRead(ctx, name, false, 0)
	}

	result, err := Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	text := textOf(t, result)
	zzz := strings.Index(text, "  .zzz:")
	aaa := strings.Index(text, "  .aaa:")
	if zzz < 0 || aaa < 0 {
		t.Fatalf("bounce rows missing:\n%s", text)
	}
	if zzz > aaa {
		t.Fatalf(".zzz must keep insertion order before .aaa on a bounce tie:\n%s", text)
	}
}

// TestBudgetNumericFirstOrder ports Object.entries(budgets): integer-like keys
// enumerate first in ascending numeric order, then string keys in insertion
// order.
func TestBudgetNumericFirstOrder(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"2", "10", "1", "zzz", "aaa"} {
		compression.SetBudget(ctx, name, 3, 1, 1, nil)
	}

	result, err := Tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	text := textOf(t, result)
	order := []string{"  1:", "  2:", "  10:", "  bash:", "  read:", "  grep:", "  web:", "  find:", "  ls:", "  zzz:", "  aaa:"}
	last := -1
	for _, want := range order {
		index := strings.Index(text, want)
		if index < 0 {
			t.Fatalf("missing budget %q:\n%s", want, text)
		}
		if index <= last {
			t.Fatalf("budget order wrong at %q:\n%s", want, text)
		}
		last = index
	}
}

func textOf(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content = %d", len(result.Content))
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return text.Text
}

func TestHandlerReport(t *testing.T) {
	result, err := Tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("isError = true")
	}
	if len(result.Content) != 1 {
		t.Fatalf("content = %d", len(result.Content))
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	for _, want := range []string{
		"=== Cave Tools Status ===",
		"RTK Available: ",
		"Cache Stats:",
		"Output trimming:",
		"Budget Configuration:",
		"bash: max=50, head=15, tail=35, chars=6000",
		"read: max=200, head=100, tail=100",
		"grep: max=60, head=30, tail=30, chars=6000",
		"web: max=120, head=60, tail=60, chars=8000",
		"find: max=120, head=60, tail=60",
		"ls: max=60, head=30, tail=30",
	} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q", want)
		}
	}
	// The six known budgets stay in TS insertion order.
	budgetAt := strings.Index(text.Text, "Budget Configuration:")
	body := text.Text[budgetAt:]
	last := -1
	for _, name := range []string{"bash:", "read:", "grep:", "web:", "find:", "ls:"} {
		index := strings.Index(body, name)
		if index <= last {
			t.Errorf("budget order wrong at %q:\n%s", name, body)
		}
		last = index
	}
}

// TestJsNumberToString pins ECMAScript Number::toString for the budget text.
func TestJsNumberToString(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{12.7, "12.7"},
		{1e21, "1e+21"},
		{1e20, "100000000000000000000"},
		{1e-7, "1e-7"},
		{1e-6, "0.000001"},
		{1000000000000000128, "1000000000000000100"},
		{math.Copysign(0, -1), "0"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
	}
	for _, test := range cases {
		if got := jsNumberToString(test.value); got != test.want {
			t.Errorf("jsNumberToString(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestJsToFixed(t *testing.T) {
	cases := []struct {
		value  float64
		digits int
		want   string
	}{
		{0, 1, "0.0"},
		{12.5, 1, "12.5"},
		{12.5, 0, "13"},
		{0.15, 1, "0.1"},
		{1.005, 2, "1.00"},
		{87.525, 1, "87.5"},
		{100, 1, "100.0"},
	}
	for _, test := range cases {
		if got := jsToFixed(test.value, test.digits); got != test.want {
			t.Errorf("jsToFixed(%v, %d) = %q, want %q", test.value, test.digits, got, test.want)
		}
	}
}

// TestHandlerParity runs the TS handler and the Go handler against the same
// fixture and compares the full ToolResult JSON. The Go handler runs in a
// fresh test-binary process so a storage singleton opened by another test (or
// the real ~/.cache database) cannot leak into the comparison.
func TestHandlerParity(t *testing.T) {
	root := repoRoot(t)
	dist := filepath.Join(root, "dist", "tools", "status.js")
	if _, err := os.Stat(dist); err != nil {
		t.Skip("dist/tools/status.js is missing: run pnpm build first")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	dir := t.TempDir()
	goJSON := runGoStatus(t, dir)

	script := filepath.Join(t.TempDir(), "run-status.mjs")
	source := "import { statusTool } from " + jsonString(pathToFileURL(dist)) + ";\n" +
		"const result = await statusTool.handler({});\n" +
		"process.stdout.write(JSON.stringify(result));\n"
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

// TestStatusHelperProcess is the child side of TestHandlerParity: it runs the
// Go handler with the caller-provided temp data dir and writes the ToolResult
// JSON to CAVE_TOOLS_STATUS_RESULT.
func TestStatusHelperProcess(t *testing.T) {
	out := os.Getenv("CAVE_TOOLS_STATUS_RESULT")
	if out == "" {
		t.Skip("helper process only")
	}
	result, err := Tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGoStatus(t *testing.T, dir string) []byte {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "status.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStatusHelperProcess$")
	cmd.Env = append(os.Environ(),
		"CAVE_TOOLS_STATUS_RESULT="+resultPath,
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
