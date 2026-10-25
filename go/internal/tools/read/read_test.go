package read

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain keeps the package off the developer's real state: stats, archives
// and the read registry all go to throwaway directories.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-read-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	// A subagent environment would disable stubs and snapshots. Scrub every
	// detection key, not just the four known signals.
	testenv.ScrubProcess()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// resetState clears the process-wide cache/snapshot/stat state, like the TS
// tests' resetCache()/resetSnapshots() calls.
func resetState(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	compression.ResetCache(ctx)
	compression.ResetStats(ctx)
	compression.ResetSnapshots()
	compression.ResetSubagentContextCache()
	return ctx
}

// fixtureRoot returns a t.TempDir() with symlinks resolved, so the directory
// listing's containsPath filter sees the same path it was given.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func callRead(t *testing.T, ctx context.Context, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(ctx, args)
	if err != nil {
		t.Fatalf("cave__read returned an error: %v", err)
	}
	return result
}

func resultText(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("expected a text block, got %T", result.Content[0])
	}
	return text.Text
}

// TestShortRIFFPrefix pins Uint8Array.slice's non-panicking behavior on short
// files that happen to start with the RIFF image-container signature.
func TestShortRIFFPrefix(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	for size := 4; size < 12; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := "RIFF" + strings.Repeat("x", size-4)
			file := filepath.Join(dir, fmt.Sprintf("riff-%d.txt", size))
			mustWrite(t, file, body)
			result := callRead(t, ctx, map[string]any{"file_path": file})
			if result.IsError || resultText(t, result) != body {
				t.Fatalf("short RIFF read = %+v, want text %q", result, body)
			}
		})
	}
}

// TestToolDefinition pins the listing metadata byte-for-byte against the TS
// tool object declared in src/tools/read.ts.
func TestToolDefinition(t *testing.T) {
	if Tool.Name != "cave__read" {
		t.Fatalf("Name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("Annotations = %s", Tool.Annotations)
	}
	wantDescription := "Use instead of Read. Returns a stub if the file is unchanged since the last read. Images return as image blocks, PDFs as resources; binary files are rejected."
	if Tool.Description != wantDescription {
		t.Fatalf("Description = %q", Tool.Description)
	}
	wantSchema := `{"type":"object","properties":{"file_path":{"type":"string","description":"Absolute path"},"offset":{"type":"number","description":"1-indexed start line","default":1},"limit":{"type":"number","default":200},"mode":{"type":"string","enum":["full","signatures","aggressive"],"description":"signatures: TS/JS/Rust signatures only; aggressive: drop comments/blank lines. Both ignore offset/limit.","default":"full"},"line_numbers":{"type":"boolean","description":"Tag lines and append a file_hash/range_checksum footer for cave__edit range mode. Disables codebook compression.","default":false},"force":{"type":"boolean","description":"Bypass dedup cache","default":false}},"required":["file_path"]}`
	if string(Tool.InputSchema) != wantSchema {
		t.Fatalf("InputSchema drifted:\n got %s\nwant %s", Tool.InputSchema, wantSchema)
	}
}

// TestSignaturesMode ports test.mjs "2e. Testing signatures read mode".
func TestSignaturesMode(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "sample.ts")
	mustWrite(t, file, "export async function loadUser(id: string): Promise<User> {\n  return db.get(id);\n}\n\nclass UserService {\n  constructor() {}\n}\n\nexport interface User {\n  id: string;\n}\n")

	text := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "mode": "signatures"}))
	if !regexp.MustCompile(`export async fn loadUser\(id: string\) → Promise<User>`).MatchString(text) {
		t.Fatalf("missing signature: %q", text)
	}
	if !strings.Contains(text, "class UserService") {
		t.Fatalf("missing class: %q", text)
	}
	if !strings.Contains(text, "export interface User") {
		t.Fatalf("missing interface: %q", text)
	}
	if strings.Contains(text, "return db.get") {
		t.Fatalf("body leaked into signatures: %q", text)
	}
}

// TestAggressiveMode ports test.mjs "2f. Testing aggressive read mode".
func TestAggressiveMode(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "sample.py")
	mustWrite(t, file, "#!/usr/bin/env python3\n# This is a comment\n\ndef hello():\n    # inline comment\n    return 'world'\n\n\nclass Foo:\n    pass\n")

	text := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "mode": "aggressive"}))
	if !strings.Contains(text, "#!/usr/bin/env python3") {
		t.Fatalf("shebang dropped: %q", text)
	}
	if strings.Contains(text, "# This is a comment") {
		t.Fatalf("comment kept: %q", text)
	}
	if !strings.Contains(text, "def hello():") || !strings.Contains(text, "return 'world'") {
		t.Fatalf("code dropped: %q", text)
	}
}

// TestFastPathAndBudget ports the read fast-path/budget assertions in test.mjs
// ("filesTracked + subagent context tests").
func TestFastPathAndBudget(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)

	smallBody := "import { useState } from 'react';\nexport const FAST_PATH_UNIQUE = 'y';\n"
	smallFile := filepath.Join(dir, "small.ts")
	mustWrite(t, smallFile, smallBody)
	smallRead := resultText(t, callRead(t, ctx, map[string]any{"file_path": smallFile}))
	if smallRead != smallBody {
		t.Fatalf("small-read fast-path is not identity: %q", smallRead)
	}
	if strings.Contains(smallRead, "[§") {
		t.Fatalf("fast-path ran the codebook: %q", smallRead)
	}
	if !compression.FitsBudget(smallBody, "read") {
		t.Fatal("small body should fit the read budget")
	}

	largeLines := make([]string, 0, 400)
	for i := 1; i <= 400; i++ {
		largeLines = append(largeLines, fmt.Sprintf("ZZZ-large-read-%03d", i))
	}
	largeFile := filepath.Join(dir, "large.txt")
	mustWrite(t, largeFile, strings.Join(largeLines, "\n"))

	largeText := resultText(t, callRead(t, ctx, map[string]any{"file_path": largeFile, "limit": 400}))
	if !strings.HasPrefix(largeText, "ZZZ-large-read-001\n") {
		t.Fatalf("large read head: %q", largeText[:60])
	}
	for _, want := range []string{"ZZZ-large-read-100", "ZZZ-large-read-301", "ZZZ-large-read-400", "lines truncated"} {
		if !strings.Contains(largeText, want) {
			t.Fatalf("large read missing %q: %q", want, largeText)
		}
	}
	if strings.Contains(largeText, "ZZZ-large-read-200") {
		t.Fatalf("large read should drop the middle: %q", largeText)
	}

	paged := resultText(t, callRead(t, ctx, map[string]any{"file_path": largeFile, "force": true}))
	for _, want := range []string{"offset=201", "limit", "force=true"} {
		if !strings.Contains(paged, want) {
			t.Fatalf("truncation hint missing %q: %q", want, paged)
		}
	}
}

// TestForceParameter ports test.mjs "6c. Testing cave__read force parameter".
func TestForceParameter(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "force.txt")
	mustWrite(t, file, "initial content\n")

	first := callRead(t, ctx, map[string]any{"file_path": file})
	if first.IsError {
		t.Fatalf("first read failed: %q", resultText(t, first))
	}
	if !strings.Contains(resultText(t, first), "initial content") {
		t.Fatalf("first read body: %q", resultText(t, first))
	}

	second := resultText(t, callRead(t, ctx, map[string]any{"file_path": file}))
	if !regexp.MustCompile(`unchanged since read #\d+`).MatchString(second) {
		t.Fatalf("second read should stub: %q", second)
	}

	third := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "force": true}))
	if !strings.Contains(third, "initial content") {
		t.Fatalf("force should bypass dedup: %q", third)
	}
}

// TestPerSessionDedup ports test.mjs "6c2. Testing cave__read per-session
// dedup (multi-session isolation)".
func TestPerSessionDedup(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "shared.txt")
	mustWrite(t, file, "shared content\n")

	a1 := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": "sessionA"}))
	if !strings.Contains(a1, "shared content") {
		t.Fatalf("session A first read: %q", a1)
	}
	a2 := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": "sessionA"}))
	if !regexp.MustCompile(`unchanged since read #\d+`).MatchString(a2) {
		t.Fatalf("session A re-read should dedup: %q", a2)
	}
	b1 := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": "sessionB"}))
	if !strings.Contains(b1, "shared content") {
		t.Fatalf("session B must get fresh content, not A's stub: %q", b1)
	}
	b2 := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": "sessionB"}))
	if !regexp.MustCompile(`unchanged since read #\d+`).MatchString(b2) {
		t.Fatalf("session B re-read should dedup: %q", b2)
	}
	legacy := resultText(t, callRead(t, ctx, map[string]any{"file_path": file}))
	if !strings.Contains(legacy, "shared content") {
		t.Fatalf("legacy client should get content on first read: %q", legacy)
	}
}

// TestRecentlyEditedBypass ports test.mjs "6d. Testing cave__read
// recently-edited bypass".
func TestRecentlyEditedBypass(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "recent.txt")
	mustWrite(t, file, "original content\n")

	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": file})); !strings.Contains(got, "original content") {
		t.Fatalf("first read: %q", got)
	}
	// cave__edit records the edit; the read must then show the new content
	// instead of the unchanged stub.
	compression.RecordEdit(ctx, file)
	mustWrite(t, file, "edited content\n")

	got := resultText(t, callRead(t, ctx, map[string]any{"file_path": file}))
	if !strings.Contains(got, "edited content") {
		t.Fatalf("recently-edited file should bypass dedup: %q", got)
	}
}

// TestEdgeCases ports test.mjs "11. Testing cave__read edge cases".
func TestEdgeCases(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)

	// Binary rejection via NUL byte.
	binFile := filepath.Join(dir, "bin.dat")
	mustWrite(t, binFile, string([]byte{0x00, 0x01, 0x02, 0x03}))
	binResult := callRead(t, ctx, map[string]any{"file_path": binFile})
	if !binResult.IsError {
		t.Fatal("NUL file should be rejected")
	}
	if !regexp.MustCompile(`Cannot read binary file:.*bin\.dat`).MatchString(resultText(t, binResult)) {
		t.Fatalf("binary error text: %q", resultText(t, binResult))
	}

	// Binary rejection via known extension.
	zipFile := filepath.Join(dir, "blob.zip")
	mustWrite(t, zipFile, "PK\x03\x04")
	if !callRead(t, ctx, map[string]any{"file_path": zipFile}).IsError {
		t.Fatal("zip should be rejected without sniffing")
	}

	// Image magic bytes produce an image content block.
	pngFile := filepath.Join(dir, "pixel.png")
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52}
	if err := os.WriteFile(pngFile, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pngResult := callRead(t, ctx, map[string]any{"file_path": pngFile})
	if pngResult.IsError {
		t.Fatalf("png read failed: %q", resultText(t, pngResult))
	}
	image, ok := pngResult.Content[0].(types.ImageContent)
	if !ok {
		t.Fatalf("expected image block, got %T", pngResult.Content[0])
	}
	if image.MimeType != "image/png" {
		t.Fatalf("mimeType = %q", image.MimeType)
	}
	if image.Data != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("image data mismatch")
	}

	// offset/limit slicing.
	linesFile := filepath.Join(dir, "lines.txt")
	lines := make([]string, 0, 50)
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("L%d", i))
	}
	mustWrite(t, linesFile, strings.Join(lines, "\n")+"\n")
	slice := resultText(t, callRead(t, ctx, map[string]any{"file_path": linesFile, "offset": 10, "limit": 3}))
	if !strings.Contains(slice, "L10") || !strings.Contains(slice, "L12") || strings.Contains(slice, "L13") {
		t.Fatalf("slice = %q", slice)
	}

	// Missing file surfaces the "did you mean" hint.
	mustWrite(t, filepath.Join(dir, "linnes"), "x")
	missing := callRead(t, ctx, map[string]any{"file_path": filepath.Join(dir, "linnes.txt")})
	if !missing.IsError {
		t.Fatal("missing file should be an error")
	}
	missingText := resultText(t, missing)
	if !strings.Contains(missingText, "File not found") {
		t.Fatalf("missing text: %q", missingText)
	}
	if !strings.Contains(missingText, "Did you mean one of these?") {
		t.Fatalf("missing hint: %q", missingText)
	}

	// Signatures mode on a TS file emits only signatures.
	tsFile := filepath.Join(dir, "mod.ts")
	mustWrite(t, tsFile, "export function add(a: number, b: number): number {\n  return a + b;\n}\n\nexport class Box {\n  constructor(public v: number) {}\n}\n")
	sigs := resultText(t, callRead(t, ctx, map[string]any{"file_path": tsFile, "mode": "signatures"}))
	if !strings.Contains(sigs, "export fn add") {
		t.Fatalf("signatures: %q", sigs)
	}
	if strings.Contains(sigs, "return a + b") {
		t.Fatalf("signatures leaked body: %q", sigs)
	}

	// Aggressive mode strips comments.
	jsFile := filepath.Join(dir, "code.js")
	mustWrite(t, jsFile, "// header comment\nconst x = 1;\n\n\n\nconst y = 2;\n")
	agg := resultText(t, callRead(t, ctx, map[string]any{"file_path": jsFile, "mode": "aggressive"}))
	if strings.Contains(agg, "header comment") || !strings.Contains(agg, "const x = 1") {
		t.Fatalf("aggressive: %q", agg)
	}
}

// TestNumberedFooter ports the line_numbers footer assertions in test.mjs.
func TestNumberedFooter(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "meta.txt")
	mustWrite(t, file, "alpha\nbeta\n")

	result := callRead(t, ctx, map[string]any{"file_path": file, "line_numbers": true, "force": true})
	if result.IsError {
		t.Fatalf("numbered read failed: %q", resultText(t, result))
	}
	text := resultText(t, result)
	checks := []struct {
		name    string
		pattern string
	}{
		{"numbered line", `00001:[0-9a-f]{2}\| alpha`},
		{"footer", `--- cave_edit_meta ---`},
		{"file hash", `file_hash: [0-9a-f]{16}`},
		{"range checksum", `range_checksum: \d+-\d+:[0-9a-f]{8}`},
	}
	for _, check := range checks {
		if !regexp.MustCompile(check.pattern).MatchString(text) {
			t.Fatalf("%s missing in %q", check.name, text)
		}
	}
	if !strings.Contains(text, "lines: 1-3 of 3") {
		t.Fatalf("line window: %q", text)
	}
	// The footer hash must be the file's sha256 prefix, so cave__edit's
	// expected_hash agrees with the numbered read.
	wantHash := compression.HashContent([]byte("alpha\nbeta\n"))[:16]
	if !strings.Contains(text, "file_hash: "+wantHash) {
		t.Fatalf("file_hash mismatch in %q, want %s", text, wantHash)
	}
}

// TestRepresentationAwareDedup ports the range/signatures/numbered dedup
// assertions in test.mjs ("Dedup is representation-aware").
func TestRepresentationAwareDedup(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "ranges.ts")
	mustWrite(t, file, "export function alpha() {\n  return 1;\n}\nexport function beta() {\n  return 2;\n}\n")

	rangeArgs := map[string]any{
		"file_path":   file,
		"offset":      1,
		"limit":       2,
		"mode":        "full",
		"__sessionId": "range-session",
	}
	first := resultText(t, callRead(t, ctx, rangeArgs))
	if !strings.Contains(first, "alpha") {
		t.Fatalf("range first: %q", first)
	}
	repeat := resultText(t, callRead(t, ctx, rangeArgs))
	if !strings.Contains(repeat, "file unchanged") {
		t.Fatalf("range repeat should stub: %q", repeat)
	}
	next := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "offset": 3, "limit": 2, "mode": "full", "__sessionId": "range-session"}))
	if strings.Contains(next, "file unchanged") {
		t.Fatalf("new range must be delivered once: %q", next)
	}
	if !strings.Contains(next, "}") {
		t.Fatalf("new range body: %q", next)
	}
	signaturesFirst := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "offset": 1, "limit": 2, "mode": "signatures", "__sessionId": "range-session"}))
	if strings.Contains(signaturesFirst, "file unchanged") || !strings.Contains(signaturesFirst, "alpha") {
		t.Fatalf("signatures first: %q", signaturesFirst)
	}
	signaturesRepeat := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "offset": 1, "limit": 2, "mode": "signatures", "__sessionId": "range-session"}))
	if !strings.Contains(signaturesRepeat, "file unchanged") {
		t.Fatalf("signatures repeat should stub: %q", signaturesRepeat)
	}
	numbered := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "offset": 1, "limit": 2, "mode": "full", "line_numbers": true, "__sessionId": "range-session"}))
	if strings.Contains(numbered, "file unchanged") || !strings.Contains(numbered, "cave_edit_meta") {
		t.Fatalf("numbered read: %q", numbered)
	}
}

// TestReReadDiffs ports test.mjs "Testing cave__read re-read diffs".
func TestReReadDiffs(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "notes.txt")

	base := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		base = append(base, fmt.Sprintf("line %d of the original notes file", i))
	}
	mustWrite(t, file, strings.Join(base, "\n"))

	read := func(args map[string]any) string {
		full := map[string]any{"file_path": file, "__sessionId": "diff-a"}
		for key, value := range args {
			full[key] = value
		}
		return resultText(t, callRead(t, ctx, full))
	}
	isDiff := func(text string) bool { return strings.HasPrefix(text, diffHeader) }

	first := read(nil)
	if isDiff(first) {
		t.Fatalf("first read must be full: %q", first)
	}
	if first != strings.Join(base, "\n") {
		t.Fatalf("first read body mismatch")
	}

	// Small change: diff.
	v2 := append([]string{}, base...)
	v2[50] = "line 50 CHANGED"
	mustWrite(t, file, strings.Join(v2, "\n"))
	small := read(nil)
	if !isDiff(small) {
		t.Fatalf("small change should diff: %.200q", small)
	}
	for _, want := range []string{"-line 50 of the original notes file", "+line 50 CHANGED", "@@ -48,7 +48,7 @@"} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).MatchString(small) {
			t.Fatalf("diff missing %q:\n%s", want, small)
		}
	}
	if utf16Len(small) >= int(0.4*float64(utf16Len(strings.Join(v2, "\n")))) {
		t.Fatalf("diff should be smaller than 40%% of the file")
	}

	// Unchanged since the diff: stub.
	repeat := read(nil)
	if !strings.HasPrefix(repeat, "<file unchanged") {
		t.Fatalf("repeat should stub: %q", repeat)
	}

	// Other session, first read anywhere: full.
	other := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": "diff-b"}))
	if isDiff(other) || other != strings.Join(v2, "\n") {
		t.Fatalf("other session should get full content: %q", other)
	}

	// Small change but line_numbers / force: full.
	v3 := append([]string{}, v2...)
	v3[10] = "line 10 CHANGED"
	mustWrite(t, file, strings.Join(v3, "\n"))
	numbered := read(map[string]any{"line_numbers": true})
	if isDiff(numbered) || !strings.Contains(numbered, "cave_edit_meta") {
		t.Fatalf("numbered read must be full: %q", numbered)
	}
	forced := read(map[string]any{"force": true})
	if isDiff(forced) || forced != strings.Join(v3, "\n") {
		t.Fatalf("forced read must be full: %q", forced)
	}

	// Large change: full content.
	v4 := make([]string, 0, len(base))
	for _, line := range base {
		v4 = append(v4, strings.ToUpper(line))
	}
	mustWrite(t, file, strings.Join(v4, "\n"))
	large := read(nil)
	if isDiff(large) || large != strings.Join(v4, "\n") {
		t.Fatalf("large change must be full: %q", large)
	}

	// Re-read right after cave__edit still diffs (snapshot path first).
	compression.RecordEdit(ctx, file)
	mustWrite(t, file, strings.Replace(strings.Join(v4, "\n"), "LINE 70 OF THE ORIGINAL NOTES FILE", "edited seventy", 1))
	afterEdit := read(nil)
	if !isDiff(afterEdit) {
		t.Fatalf("post-edit read should diff: %.200q", afterEdit)
	}
	if !regexp.MustCompile(`(?m)^\+edited seventy$`).MatchString(afterEdit) {
		t.Fatalf("post-edit diff body: %q", afterEdit)
	}

	// Files over the snapshot cap are never diffed.
	big := filepath.Join(dir, "big.txt")
	bigLines := make([]string, 0, 150)
	for i := 0; i < 150; i++ {
		bigLines = append(bigLines, fmt.Sprintf("%d%s", i, strings.Repeat("x", 1900)))
	}
	mustWrite(t, big, strings.Join(bigLines, "\n"))
	info, err := os.Stat(big)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= compression.MaxSnapshotFileBytes {
		t.Fatalf("big fixture is only %d bytes", info.Size())
	}
	callRead(t, ctx, map[string]any{"file_path": big, "limit": 1000, "__sessionId": "diff-a"})
	bigLines[3] = "changed"
	mustWrite(t, big, strings.Join(bigLines, "\n"))
	bigAgain := resultText(t, callRead(t, ctx, map[string]any{"file_path": big, "limit": 1000, "__sessionId": "diff-a"}))
	if isDiff(bigAgain) {
		t.Fatal("oversized files must not be diffed")
	}
}

// TestDirectoryListing covers the isDirectory branch.
func TestDirectoryListing(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "zeta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "b.txt"), "x")
	mustWrite(t, filepath.Join(dir, "a.txt"), "y")
	mustWrite(t, filepath.Join(dir, ".hidden"), "z")
	mustWrite(t, filepath.Join(dir, "B.md"), "w")

	// Directories first, then files; ICU ordering (lowercase before uppercase
	// for the same letter, case-folded primaries).
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": dir})); got != "Alpha/\nzeta/\n.hidden\na.txt\nB.md\nb.txt" {
		t.Fatalf("listing = %q", got)
	}
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": dir, "limit": 2})); got != "Alpha/\nzeta/\n\n(Directory has more entries. Use 'offset'=3)" {
		t.Fatalf("paged listing = %q", got)
	}
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": dir, "offset": 3, "limit": 2})); got != ".hidden\na.txt\n\n(Directory has more entries. Use 'offset'=5)" {
		t.Fatalf("offset listing = %q", got)
	}
	outOfRange := callRead(t, ctx, map[string]any{"file_path": dir, "offset": 99})
	if !outOfRange.IsError || resultText(t, outOfRange) != "Offset 99 is out of range" {
		t.Fatalf("out of range = %q", resultText(t, outOfRange))
	}

	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": empty})); got != "(empty directory)" {
		t.Fatalf("empty listing = %q", got)
	}
}

// TestDirectoryListingUnicodeCollation pins the collation the listing sort
// relies on: accents sort next to their base letter, Turkish dotless i sorts
// before i, and canonically equivalent names (NFC vs NFD) tie, so the readdir
// order libuv produces decides between them.
func TestDirectoryListingUnicodeCollation(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	for _, name := range []string{
		"apple.txt", "Apple.txt", "APPLE.txt",
		"arger.txt", "\u00c4rger.txt",
		"cafe\u0301.txt", "caf\u00e9.txt",
		"istanbul.txt", "\u0131stanbul.txt", "\u0130stanbul.txt",
		"zebra.txt", "Zebra.txt",
	} {
		mustWrite(t, filepath.Join(dir, name), "x")
	}

	want := strings.Join([]string{
		"apple.txt", "Apple.txt", "APPLE.txt",
		"arger.txt", "\u00c4rger.txt",
		"cafe\u0301.txt", "caf\u00e9.txt", // NFD then NFC: libuv readdir is bytewise, and the collator ties
		"istanbul.txt", "\u0130stanbul.txt", "\u0131stanbul.txt",
		"zebra.txt", "Zebra.txt",
	}, "\n")
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": dir})); got != want {
		t.Fatalf("listing = %q, want %q", got, want)
	}
}

// TestMediaBlocks covers the svg/pdf branches.
func TestMediaBlocks(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)

	svg := "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>\n"
	svgFile := filepath.Join(dir, "icon.svg")
	mustWrite(t, svgFile, svg)
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": svgFile})); got != svg {
		t.Fatalf("svg should be text: %q", got)
	}

	pdfBytes := []byte("%PDF-1.4\n")
	pdfFile := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfFile, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pdfResult := callRead(t, ctx, map[string]any{"file_path": pdfFile})
	resource, ok := pdfResult.Content[0].(types.ResourceContent)
	if !ok {
		t.Fatalf("expected resource block, got %T", pdfResult.Content[0])
	}
	if resource.Resource.URI != "file://"+pdfFile || resource.Resource.MimeType != "application/pdf" {
		t.Fatalf("resource = %+v", resource.Resource)
	}
	if resource.Resource.Blob != base64.StdEncoding.EncodeToString(pdfBytes) {
		t.Fatal("pdf blob mismatch")
	}

	// Oversized media is refused before reading the body.
	bigImage := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(bigImage, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(bigImage, maxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	imageError := callRead(t, ctx, map[string]any{"file_path": bigImage})
	if !imageError.IsError || resultText(t, imageError) != "Image too large (20971521 bytes, max 20971520). Resize or crop before reading." {
		t.Fatalf("image cap = %q", resultText(t, imageError))
	}

	bigPDF := filepath.Join(dir, "huge.pdf")
	mustWrite(t, bigPDF, "%PDF-1.4\n")
	if err := os.Truncate(bigPDF, maxPDFBytes+1); err != nil {
		t.Fatal(err)
	}
	pdfError := callRead(t, ctx, map[string]any{"file_path": bigPDF})
	if !pdfError.IsError || resultText(t, pdfError) != "PDF too large (20971521 bytes, max 20971520)." {
		t.Fatalf("pdf cap = %q", resultText(t, pdfError))
	}
}

// TestEncoding covers BOM stripping and strict UTF-8 rejection.
func TestEncoding(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)

	bomFile := filepath.Join(dir, "bom.txt")
	if err := os.WriteFile(bomFile, []byte{0xef, 0xbb, 0xbf, 'a', '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": bomFile})); got != "a\n" {
		t.Fatalf("BOM should be stripped: %q", got)
	}

	badFile := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(badFile, []byte{0xc3, 0x28}, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := callRead(t, ctx, map[string]any{"file_path": badFile})
	if !bad.IsError || resultText(t, bad) != "Error reading file: File is not valid UTF-8: "+badFile {
		t.Fatalf("invalid utf-8 = %q", resultText(t, bad))
	}
}

// TestFractionalOffset pins the TS quirk: a fractional offset indexes
// lines[1.5], and reading .length of undefined throws inside the handler.
func TestFractionalOffset(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "frac.txt")
	mustWrite(t, file, "a\nb\nc\n")

	result := callRead(t, ctx, map[string]any{"file_path": file, "offset": 2.5})
	if !result.IsError {
		t.Fatal("fractional offset should fail like TS")
	}
	if got := resultText(t, result); got != "Error reading file: Cannot read properties of undefined (reading 'length')" {
		t.Fatalf("fractional offset = %q", got)
	}

	// A fractional limit only shortens the window (TS loops i < 2.5).
	limited := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "limit": 2.5, "force": true}))
	if !strings.Contains(limited, "a\nb\nc") || !strings.Contains(limited, "offset=4") {
		t.Fatalf("fractional limit = %q", limited)
	}
}

// TestSelectLinesUnits covers the per-line and total byte caps.
func TestSelectLinesUnits(t *testing.T) {
	longLine := strings.Repeat("a", maxLineLength+500)
	sel, err := selectLines([]string{longLine, "next"}, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	cappedLine := strings.Split(sel.body, "\n")[0]
	if !strings.HasSuffix(cappedLine, "...") {
		t.Fatalf("long line should be capped: %q", cappedLine)
	}
	if utf16Len(cappedLine) != maxLineLength+3 {
		t.Fatalf("capped line length = %d", utf16Len(cappedLine))
	}
	if sel.truncated {
		t.Fatal("caps alone do not mark the read truncated")
	}

	manyLines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		manyLines = append(manyLines, strings.Repeat("b", 300))
	}
	sel, err = selectLines(manyLines, 1, 300, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sel.truncatedByBytes || len(sel.body) > maxBytes {
		t.Fatalf("byte cap not applied: bytes=%d truncatedByBytes=%v", len(sel.body), sel.truncatedByBytes)
	}
	if !strings.Contains(truncationHint(sel), fmt.Sprintf("Output truncated at %d bytes", maxBytes)) {
		t.Fatalf("byte hint = %q", truncationHint(sel))
	}

	// Out-of-range window: empty body, offset echoed by the handler.
	sel, err = selectLines(manyLines, 999, 200, false)
	if err != nil {
		t.Fatal(err)
	}
	if sel.body != "" || sel.lastReadLine != 998 {
		t.Fatalf("out-of-range select = %+v", sel)
	}
}

// TestUtf16LineNumbering proves the numbered line width uses padStart(5).
func TestUtf16LineNumbering(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "wide.txt")
	mustWrite(t, file, "x\n")
	text := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "line_numbers": true, "force": true}))
	if !regexp.MustCompile(`00001:[0-9a-f]{2}\| x\n`).MatchString(text) {
		t.Fatalf("numbered line = %q", text)
	}
}

// TestEmptyFile pins the empty-content read (single empty line, no hint).
func TestEmptyFile(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "empty.txt")
	mustWrite(t, file, "")
	if got := resultText(t, callRead(t, ctx, map[string]any{"file_path": file})); got != "" {
		t.Fatalf("empty file = %q", got)
	}
}

// TestSessionIDCoercion covers String(args.__sessionId ?? "default").
func TestSessionIDCoercion(t *testing.T) {
	ctx := resetState(t)
	dir := fixtureRoot(t)
	file := filepath.Join(dir, "session.txt")
	mustWrite(t, file, "hello\n")

	first := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": 42}))
	if !strings.Contains(first, "hello") {
		t.Fatalf("numeric session first read = %q", first)
	}
	second := resultText(t, callRead(t, ctx, map[string]any{"file_path": file, "__sessionId": 42}))
	if !strings.Contains(second, "session 42") {
		t.Fatalf("numeric session stub = %q", second)
	}
}

// TestJSONShape pins the marshaled result shape (field order and omission)
// the MCP layer sends.
func TestJSONShape(t *testing.T) {
	result := types.ToolResult{Content: []types.Content{types.Text("hi")}, IsError: true}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"content":[{"type":"text","text":"hi"}],"isError":true}` {
		t.Fatalf("json = %s", encoded)
	}
}

// TestSuggestPathsSortedBytewise pins the readdir ordering TS gets from
// Node/libuv: entries come back sorted by strcmp on POSIX, so the first three
// matches are chosen bytewise rather than in filesystem order.
func TestSuggestPathsSortedBytewise(t *testing.T) {
	dir := t.TempDir()
	// Created out of order; uppercase sorts before lowercase bytewise, so the
	// expected prefix is Bmatch.txt, Zmatch.txt, amatch.txt.
	for _, name := range []string{"cmatch.txt", "Zmatch.txt", "amatch.txt", "Bmatch.txt", "nomatch.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got := suggestPaths(filepath.Join(dir, "MATCH"))
	want := "\n\nDid you mean one of these?\n" +
		filepath.Join(dir, "Bmatch.txt") + "\n" +
		filepath.Join(dir, "Zmatch.txt") + "\n" +
		filepath.Join(dir, "amatch.txt")
	if got != want {
		t.Fatalf("suggestPaths = %q, want %q", got, want)
	}
}

// TestSuggestPathsCases pins the unchanged single-match, no-match and
// fewer-than-three-match paths around the ordering fix.
func TestSuggestPathsCases(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if got, want := suggestPaths(filepath.Join(dir, "one")), "\n\nDid you mean one of these?\n"+filepath.Join(dir, "one.txt"); got != want {
		t.Fatalf("single match = %q, want %q", got, want)
	}
	if got := suggestPaths(filepath.Join(dir, "zzz")); got != "" {
		t.Fatalf("no match = %q, want empty", got)
	}
	if got := suggestPaths(filepath.Join(dir, "nomatchdir", "x")); got != "" {
		t.Fatalf("missing dir = %q, want empty", got)
	}
}

// TestSuggestPathsJSUnicodeCase pins String.prototype.toLowerCase parity for
// non-ASCII names. Values measured with node 24.19.0:
//
//	"imatch".toLowerCase()   = "imatch"
//	"\u0130match".toLowerCase() = "i\u0307match"   (U+0130 -> U+0069 U+0307)
//	"\u0391\u03a3".toLowerCase() = "\u03b1\u03c2" (final sigma -> U+03C2)
func TestSuggestPathsJSUnicodeCase(t *testing.T) {
	dir := t.TempDir()
	dottedI := "\u0130match.txt"
	for _, name := range []string{dottedI, "imatch.txt", "\u0391\u03a3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if got, want := suggestPaths(filepath.Join(dir, "imatch")),
		"\n\nDid you mean one of these?\n"+filepath.Join(dir, "imatch.txt"); got != want {
		t.Fatalf("ascii needle = %q, want %q", got, want)
	}
	if got, want := suggestPaths(filepath.Join(dir, dottedI)),
		"\n\nDid you mean one of these?\n"+filepath.Join(dir, dottedI); got != want {
		t.Fatalf("dotted-I needle = %q, want %q", got, want)
	}
	// Word-final sigma lowercases to U+03C2, so the ASCII "σ" needle misses.
	if got := suggestPaths(filepath.Join(dir, "\u03b1\u03c3")); got != "" {
		t.Fatalf("sigma needle = %q, want empty", got)
	}
	if got, want := suggestPaths(filepath.Join(dir, "\u03b1\u03c2")),
		"\n\nDid you mean one of these?\n"+filepath.Join(dir, "\u0391\u03a3"); got != want {
		t.Fatalf("final-sigma needle = %q, want %q", got, want)
	}
}
