package find

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain pins one data dir for the package: storage keeps a process
// singleton connection, so per-test dirs would dangle.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-find-")
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

// fixture builds the test.mjs find directory.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"top.md":                        "x\n",
		filepath.Join("sub", "deep.ts"): "y\n",
		"ignore.txt":                    "z\n",
		".secret":                       "s\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

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
	content, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", result.Content[0])
	}
	return content.Text
}

// TestToolMetadata pins the declaration to src/tools/find.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__find" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", Tool.Annotations)
	}
}

// TestMarkdown ports test.mjs:4 basic glob.
func TestMarkdown(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"pattern": "*.md", "path": dir, "limit": float64(50)})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	got := text(t, result)
	if !strings.Contains(got, "top.md") || strings.Contains(got, ".ts") {
		t.Fatalf("text = %q", got)
	}
}

// TestRecursive ports test.mjs:4 recursive glob.
func TestRecursive(t *testing.T) {
	dir := fixture(t)
	if got := text(t, run(t, map[string]any{"pattern": "**/*.ts", "path": dir, "limit": float64(50)})); !strings.Contains(got, "sub/deep.ts") {
		t.Fatalf("text = %q", got)
	}
}

// TestHidden ports test.mjs:4 hidden default and opt-out.
func TestHidden(t *testing.T) {
	dir := fixture(t)
	hidden := text(t, run(t, map[string]any{"pattern": "*", "path": dir, "limit": float64(50)}))
	if !strings.Contains(hidden, ".secret") {
		t.Fatalf("hidden = %q", hidden)
	}
	noHidden := text(t, run(t, map[string]any{"pattern": "*", "path": dir, "limit": float64(50), "hidden": false}))
	if strings.Contains(noHidden, ".secret") {
		t.Fatalf("noHidden = %q", noHidden)
	}
}

// TestLimitNotice ports test.mjs:4 limit notice.
func TestLimitNotice(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "*", "path": dir, "limit": float64(1)}))
	if !strings.Contains(got, "1 results limit reached. Use limit=2 for more") {
		t.Fatalf("text = %q", got)
	}
}

// TestMissingDirectory ports test.mjs:4 missing directory.
func TestMissingDirectory(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"pattern": "*", "path": filepath.Join(dir, "missing"), "limit": float64(5)})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.Contains(got, "Path not found or not a directory") {
		t.Fatalf("text = %q", got)
	}
}

// TestNoFiles pins the empty result message.
func TestNoFiles(t *testing.T) {
	dir := fixture(t)
	if got := text(t, run(t, map[string]any{"pattern": "*.nope", "path": dir})); got != "No files found matching pattern" {
		t.Fatalf("text = %q", got)
	}
}

// TestLimitCoercion pins Math.max(1, Number(limit) || DEFAULT_LIMIT).
func TestLimitCoercion(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "*", "path": dir, "limit": "2"}))
	if !strings.Contains(got, "2 results limit reached. Use limit=4 for more") {
		t.Fatalf("text = %q", got)
	}
}
