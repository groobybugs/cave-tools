package write

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Ports the cave__write assertions of test.mjs (section 7).

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-write-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func testCtx(t *testing.T, cwd string) context.Context {
	t.Helper()
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{
		Cwd:      cwd,
		Env:      map[string]string{"CAVE_TOOLS_MODE": "enforce"},
		ClientID: "local",
	}, func(ctx context.Context) context.Context { return ctx })
}

func run(t *testing.T, cwd string, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := handle(testCtx(t, cwd), args)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

func textOf(result types.ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(types.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()

	newFile := filepath.Join(dir, "new.txt")
	result := run(t, dir, map[string]any{"file_path": newFile, "content": "hello world"})
	if result.IsError {
		t.Fatalf("create failed: %s", textOf(result))
	}
	if got := textOf(result); !strings.Contains(got, "Wrote 11 chars") {
		t.Fatalf("create message = %q", got)
	}
	if got := readFile(t, newFile); got != "hello world" {
		t.Fatalf("create content = %q", got)
	}

	result = run(t, dir, map[string]any{"file_path": newFile, "content": "replaced"})
	if result.IsError {
		t.Fatalf("overwrite failed: %s", textOf(result))
	}
	if got := readFile(t, newFile); got != "replaced" {
		t.Fatalf("overwrite content = %q", got)
	}
	if got := textOf(result); !strings.Contains(got, "overwrote existing file") {
		t.Fatalf("overwrite message = %q", got)
	}

	emptyFile := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte("to be cleared"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = run(t, dir, map[string]any{"file_path": emptyFile, "content": ""})
	if result.IsError {
		t.Fatalf("empty content failed: %s", textOf(result))
	}
	if got := readFile(t, emptyFile); got != "" {
		t.Fatalf("empty content = %q", got)
	}

	truncateFile := filepath.Join(dir, "truncate.txt")
	if err := os.WriteFile(truncateFile, []byte("will be emptied"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = run(t, dir, map[string]any{"file_path": truncateFile, "truncate": true})
	if result.IsError {
		t.Fatalf("truncate failed: %s", textOf(result))
	}
	if got := textOf(result); !strings.Contains(got, "Truncated") {
		t.Fatalf("truncate message = %q", got)
	}
	if got := readFile(t, truncateFile); got != "" {
		t.Fatalf("truncate content = %q", got)
	}

	result = run(t, dir, map[string]any{"file_path": newFile})
	if !result.IsError || !strings.Contains(textOf(result), "cave__invalidate") {
		t.Fatalf("no-op result = %q", textOf(result))
	}

	nestedFile := filepath.Join(dir, "nested", "created.txt")
	result = run(t, dir, map[string]any{"file_path": nestedFile, "content": "nested"})
	if result.IsError {
		t.Fatalf("nested failed: %s", textOf(result))
	}
	if got := readFile(t, nestedFile); got != "nested" {
		t.Fatalf("nested content = %q", got)
	}

	bomFile := filepath.Join(dir, "bom.txt")
	if err := os.WriteFile(bomFile, []byte("\uFEFFbefore"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = run(t, dir, map[string]any{"file_path": bomFile, "content": "after"})
	if result.IsError {
		t.Fatalf("bom failed: %s", textOf(result))
	}
	if got := readFile(t, bomFile); got != "\uFEFFafter" {
		t.Fatalf("bom content = %q", got)
	}
}

func TestWriteRequiredArgs(t *testing.T) {
	dir := t.TempDir()

	result := run(t, dir, map[string]any{})
	if !result.IsError || !strings.Contains(textOf(result), "`file_path` is required") {
		t.Fatalf("missing file_path = %q", textOf(result))
	}

	result = run(t, dir, map[string]any{"file_path": ""})
	if !result.IsError || !strings.Contains(textOf(result), "`file_path` is required") {
		t.Fatalf("empty file_path = %q", textOf(result))
	}
}

func TestWriteCountsUTF16CodeUnits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "emoji.txt")
	result := run(t, dir, map[string]any{"file_path": path, "content": "😀"})
	if result.IsError {
		t.Fatalf("write failed: %s", textOf(result))
	}
	// String.prototype.length counts surrogate pairs as two.
	if got := textOf(result); !strings.Contains(got, "Wrote 2 chars") {
		t.Fatalf("message = %q", got)
	}
}

func TestWriteCannotWriteMessage(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Writing a file over an existing directory fails with the pinned text.
	result := run(t, dir, map[string]any{"file_path": target, "content": "x"})
	if !result.IsError {
		t.Fatalf("write over directory must fail")
	}
	if got := textOf(result); !strings.HasPrefix(got, "Error: cannot write ") {
		t.Fatalf("message = %q", got)
	}
}
