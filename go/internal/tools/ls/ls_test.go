package ls

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
	dir, err := os.MkdirTemp("", "cave-tools-go-ls-")
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

// fixture builds the test.mjs ls directory.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"file-a.txt": "a\n",
		"file-b.md":  "b\n",
		".hidden":    "h\n",
	} {
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

// TestToolMetadata pins the declaration to src/tools/ls.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__ls" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", Tool.Annotations)
	}
	if !strings.Contains(string(Tool.InputSchema), `"offset"`) {
		t.Fatalf("input schema = %s", Tool.InputSchema)
	}
}

// TestListing ports test.mjs:5 directory ordering and dotfiles.
func TestListing(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"path": dir, "limit": float64(50)})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	got := text(t, result)
	lines := strings.Split(got, "\n")
	if len(lines) == 0 || lines[0] != "dir/" {
		t.Fatalf("first line = %q", got)
	}
	for _, want := range []string{".hidden", "file-a.txt", "file-b.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Index(got, "dir/") > strings.Index(got, "file-a.txt") {
		t.Fatalf("directories must list first: %q", got)
	}
}

// TestPagination ports test.mjs:5 offset pagination.
func TestPagination(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"path": dir, "limit": float64(2), "offset": float64(2)}))
	if !strings.Contains(got, "Directory listing truncated. Use offset=4 to continue") {
		t.Fatalf("text = %q", got)
	}
}

// TestSymlinkEscape ports test.mjs:5 symlink escape guard.
func TestSymlinkEscape(t *testing.T) {
	dir := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got := text(t, run(t, map[string]any{"path": dir, "limit": float64(50)}))
	if strings.Contains(got, "escape-link") {
		t.Fatalf("symlink escaping the dir should be filtered: %q", got)
	}
}

// TestMissingDirectory ports test.mjs:5 missing directory.
func TestMissingDirectory(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"path": filepath.Join(dir, "nope"), "limit": float64(5)})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.Contains(got, "Path not found or not a directory") {
		t.Fatalf("text = %q", got)
	}
}

// TestEmptyAndOffsetBeyond pins the empty-directory message.
func TestEmptyAndOffsetBeyond(t *testing.T) {
	empty := t.TempDir()
	if got := text(t, run(t, map[string]any{"path": empty})); got != "(empty directory)" {
		t.Fatalf("empty = %q", got)
	}
	dir := fixture(t)
	if got := text(t, run(t, map[string]any{"path": dir, "offset": float64(50)})); got != "(empty directory)" {
		t.Fatalf("offset beyond = %q", got)
	}
}

// TestLocaleOrder pins the ICU-ish collation used by ls.
func TestLocaleOrder(t *testing.T) {
	names := []string{"Zebra", "apple", "a1", "A1", "b", "B", "_x", "-y", ".hidden", "10", "2", "file-a.txt", "dir/", "Dir2", "zz", "ZZ"}
	entries := make([]entry, len(names))
	for index, name := range names {
		entries[index] = entry{name: name}
	}
	sortEntries(entries)
	got := make([]string, len(entries))
	for index, item := range entries {
		got[index] = item.name
	}
	want := []string{"_x", "-y", ".hidden", "10", "2", "a1", "A1", "apple", "b", "B", "dir/", "Dir2", "file-a.txt", "Zebra", "zz", "ZZ"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestDirectoriesSortFirst pins type-first ordering regardless of collation.
func TestDirectoriesSortFirst(t *testing.T) {
	entries := []entry{{name: "aaa.txt"}, {name: "zzz/", isDirectory: true}}
	sortEntries(entries)
	if !entries[0].isDirectory {
		t.Fatalf("order = %#v", entries)
	}
}

// TestOffsetCoercion pins Math.max(1, Number(offset) || 1).
func TestOffsetCoercion(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"path": dir, "offset": "2", "limit": "2"}))
	if !strings.Contains(got, "Directory listing truncated. Use offset=4 to continue") {
		t.Fatalf("text = %q", got)
	}
}
