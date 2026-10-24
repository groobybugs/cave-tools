package invalidate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

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

// TestToolMetadata pins the declaration to src/tools/invalidate.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__invalidate" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", Tool.Annotations)
	}
	if string(Tool.InputSchema) != `{"type":"object","properties":{"file_paths":{"type":"array","items":{"type":"string"},"description":"Absolute paths"}},"required":["file_paths"]}` {
		t.Fatalf("input schema = %s", Tool.InputSchema)
	}
}

// TestInvalidateFiles ports test.mjs:7b.
func TestInvalidateFiles(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(fileA, []byte("keep a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("keep b"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := run(t, map[string]any{"file_paths": []any{fileA, fileB}})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); got != "Invalidated cache for 2 file(s)" {
		t.Fatalf("text = %q", got)
	}
	if content, err := os.ReadFile(fileA); err != nil || string(content) != "keep a" {
		t.Fatalf("file A = %q, %v", content, err)
	}
	if content, err := os.ReadFile(fileB); err != nil || string(content) != "keep b" {
		t.Fatalf("file B = %q, %v", content, err)
	}
}

// TestEmptyArray ports test.mjs:7b empty array error.
func TestEmptyArray(t *testing.T) {
	result := run(t, map[string]any{"file_paths": []any{}})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); got != "Error: `file_paths` must be a non-empty array" {
		t.Fatalf("text = %q", got)
	}
	missing := run(t, map[string]any{})
	if !missing.IsError {
		t.Fatal("missing file_paths should error")
	}
}

// TestNonArray pins Array.isArray rejection.
func TestNonArray(t *testing.T) {
	if result := run(t, map[string]any{"file_paths": "/tmp/a"}); !result.IsError {
		t.Fatalf("string payload = %+v", result)
	}
}

// TestEmptyEntriesFiltered pins `.filter(p => p.length > 0)`.
func TestEmptyEntriesFiltered(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"file_paths": []any{"", file, ""}})
	if got := text(t, result); got != "Invalidated cache for 1 file(s)" {
		t.Fatalf("text = %q", got)
	}
}

// TestDropsCacheEntry proves the invalidate actually clears the read cache.
func TestDropsCacheEntry(t *testing.T) {
	ctx := context.Background()
	compression.ResetCache(ctx)
	file := filepath.Join(t.TempDir(), "cached.txt")
	if err := os.WriteFile(file, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	compression.UpdateFileCache(ctx, file, "s1", compression.FileCacheUpdate{Hash: "abc"})
	if tracked := compression.GetCacheStats(ctx).FilesTracked; tracked == 0 {
		t.Fatal("cache seed failed")
	}
	run(t, map[string]any{"file_paths": []any{file}})
	if tracked := compression.GetCacheStats(ctx).FilesTracked; tracked != 0 {
		t.Fatalf("FilesTracked = %d after invalidate", tracked)
	}
}
