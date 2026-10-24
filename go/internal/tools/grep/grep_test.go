package grep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain pins one data dir for the package: storage keeps a process
// singleton connection, so per-test dirs would dangle.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-grep-")
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

// fixture builds the test.mjs grep directory.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.ts", "export const ALPHA = 1;\nexport const beta = 2;\n")
	write("b.md", "# Title\nALPHA reference here\n")
	write("lit.txt", "price is $5.00 each\n")
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

// TestToolMetadata pins the declaration to src/tools/grep.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__grep" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", Tool.Annotations)
	}
	if !strings.Contains(string(Tool.InputSchema), `"required":["pattern"]`) {
		t.Fatalf("input schema = %s", Tool.InputSchema)
	}
}

// TestMatches ports test.mjs:3 basic matching.
func TestMatches(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"pattern": "ALPHA", "path": dir, "limit": float64(10)})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	got := text(t, result)
	for _, want := range []string{"a.ts:1: export const ALPHA = 1", "b.md:2: ALPHA reference here"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

// TestGlob ports test.mjs:3 glob filtering.
func TestGlob(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "ALPHA", "path": dir, "glob": "*.ts", "limit": float64(10)}))
	if !strings.Contains(got, "a.ts:1:") || strings.Contains(got, "b.md") {
		t.Fatalf("text = %q", got)
	}
}

// TestLiteral ports test.mjs:3 literal mode.
func TestLiteral(t *testing.T) {
	dir := fixture(t)
	literal := text(t, run(t, map[string]any{
		"pattern": "$5.00", "path": dir, "glob": "lit.txt", "literal": true, "limit": float64(5),
	}))
	if !strings.Contains(literal, "lit.txt:1: price is $5.00 each") {
		t.Fatalf("literal = %q", literal)
	}
	regex := text(t, run(t, map[string]any{
		"pattern": "$5.00", "path": dir, "glob": "lit.txt", "limit": float64(5),
	}))
	if regex != "No matches found" {
		t.Fatalf("regex = %q", regex)
	}
}

// TestIgnoreCase ports test.mjs:3 ignoreCase.
func TestIgnoreCase(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{
		"pattern": "alpha", "path": dir, "glob": "a.ts", "ignoreCase": true, "limit": float64(5),
	}))
	if !strings.Contains(got, "a.ts:1: export const ALPHA = 1") {
		t.Fatalf("text = %q", got)
	}
}

// TestContextLines ports test.mjs:3 context windows.
func TestContextLines(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{
		"pattern": "beta", "path": dir, "glob": "a.ts", "context": float64(1), "limit": float64(5),
	}))
	if !strings.Contains(got, "a.ts:2: export const beta = 2") {
		t.Fatalf("missing match line: %q", got)
	}
	if !strings.Contains(got, "a.ts-1- export const ALPHA = 1") {
		t.Fatalf("missing context line: %q", got)
	}
}

// TestMissingPath ports test.mjs:3 missing path.
func TestMissingPath(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"pattern": "x", "path": filepath.Join(dir, "nope"), "limit": float64(5)})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.Contains(got, "Path not found") {
		t.Fatalf("text = %q", got)
	}
}

// TestPerFileCap ports test.mjs:3 per-file cap.
func TestPerFileCap(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("HIT_TOKEN line %d", i))
	}
	if err := os.WriteFile(filepath.Join(dir, "many.ts"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := text(t, run(t, map[string]any{"pattern": "HIT_TOKEN", "path": dir, "glob": "many.ts", "limit": float64(100)}))
	hits := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "HIT_TOKEN") {
			hits++
		}
	}
	if hits != 10 {
		t.Fatalf("hits = %d, want 10 in %q", hits, got)
	}
	if !strings.Contains(got, "+20 more in many.ts") {
		t.Fatalf("overflow marker missing: %q", got)
	}
}

// TestNoMatches pins the empty result message.
func TestNoMatches(t *testing.T) {
	dir := fixture(t)
	if got := text(t, run(t, map[string]any{"pattern": "NO_SUCH_TOKEN", "path": dir})); got != "No matches found" {
		t.Fatalf("text = %q", got)
	}
}

// TestSingleFileSearch covers path pointing at one file.
func TestSingleFileSearch(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "ALPHA", "path": filepath.Join(dir, "a.ts")}))
	if !strings.Contains(got, "a.ts:1: export const ALPHA = 1") {
		t.Fatalf("text = %q", got)
	}
}

// TestInvalidPattern pins the escaped ripgrep error.
func TestInvalidPattern(t *testing.T) {
	dir := fixture(t)
	result := run(t, map[string]any{"pattern": "(", "path": dir})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); !strings.HasPrefix(got, "Error: ") {
		t.Fatalf("text = %q", got)
	}
}

// TestLimitNoticeAndArchive ports the grep archive case in test.mjs:handle.
func TestLimitNoticeAndArchive(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("needle number %d with some padding text", i))
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := text(t, run(t, map[string]any{"pattern": "needle", "path": dir}))
	handleRE := regexp.MustCompile(`\[cave: full output \((\d+) lines\) at (\S+) — cave__read offset/limit or cave__grep\]`)
	handle := handleRE.FindStringSubmatch(got)
	if handle == nil {
		t.Fatalf("no handle: %q", got[len(got)-300:])
	}
	archived, err := os.ReadFile(handle[2])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if !strings.Contains(string(archived), fmt.Sprintf("needle number %d ", i)) {
			t.Fatalf("archive missing needle %d", i)
		}
	}
}

// TestFormatMatchesNotices pins the doubled-limit notice and line-truncation
// notice.
func TestFormatMatchesNotices(t *testing.T) {
	output, full := formatMatches([]rt.GrepMatch{
		{Path: "a.ts", Line: 1, Text: "short"},
		{Path: "a.ts", Line: 2, Text: "long ... middle"},
	}, t.TempDir(), t.TempDir(), 0, true, 100)
	for _, want := range []string{
		"100 matches limit. Use limit=200 for more, or refine pattern",
		"Some lines truncated. Use cave__read to see full lines",
	} {
		if !strings.Contains(output, want) || !strings.Contains(full, want) {
			t.Fatalf("output = %q, full = %q", output, full)
		}
	}
}

// TestFormatMatchesPerFileOverflow pins the "+N more" reset across files.
func TestFormatMatchesPerFileOverflow(t *testing.T) {
	dir := t.TempDir()
	matches := make([]rt.GrepMatch, 0, 15)
	for i := 1; i <= 12; i++ {
		matches = append(matches, rt.GrepMatch{Path: "a.ts", Line: int64(i), Text: "a"})
	}
	for i := 1; i <= 3; i++ {
		matches = append(matches, rt.GrepMatch{Path: "b.ts", Line: int64(i), Text: "b"})
	}
	output, _ := formatMatches(matches, dir, dir, 0, false, 100)
	if !strings.Contains(output, "+2 more in a.ts") {
		t.Fatalf("output = %q", output)
	}
	if strings.Contains(output, "more in b.ts") {
		t.Fatalf("b.ts should not overflow: %q", output)
	}
}

// TestLimitZeroClampsToOne pins Math.max(1, limit).
func TestLimitZeroClampsToOne(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "ALPHA", "path": dir, "limit": float64(0)}))
	if !strings.Contains(got, "1 matches limit. Use limit=2 for more") {
		t.Fatalf("text = %q", got)
	}
}

// TestStringLimitCoercion pins Math.max over a numeric string.
func TestStringLimitCoercion(t *testing.T) {
	dir := fixture(t)
	got := text(t, run(t, map[string]any{"pattern": "ALPHA", "path": dir, "limit": "10"}))
	if !strings.Contains(got, "a.ts:1:") || !strings.Contains(got, "b.md:2:") {
		t.Fatalf("text = %q", got)
	}
}
