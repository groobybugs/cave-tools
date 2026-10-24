package status

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/types"
)

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

func TestHandlerReport(t *testing.T) {
	t.Setenv("CAVE_TOOLS_DATA_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
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
// fixture and compares the full ToolResult JSON.
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
	goResult, err := Tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("go handler: %v", err)
	}
	goJSON, err := json.Marshal(goResult)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

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
