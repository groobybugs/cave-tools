package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Ports the cave__edit assertions of test.mjs (6, 6b, 6e, 6e2, 6f),
// scripts/test-edit-reliability.mjs, scripts/test-edit-handler-v2.mjs and
// scripts/test-edit-schema.mjs.

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-edit-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type harness struct {
	t   *testing.T
	dir string
	env map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, dir: t.TempDir(), env: map[string]string{"CAVE_TOOLS_MODE": "enforce"}}
}

func (h *harness) ctx() context.Context {
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{
		Cwd:      h.dir,
		Env:      h.env,
		ClientID: "local",
	}, func(ctx context.Context) context.Context { return ctx })
}

func (h *harness) fixture(name, content string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		h.t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func (h *harness) read(path string) string {
	h.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func (h *harness) run(args map[string]any) types.ToolResult {
	h.t.Helper()
	result, err := handle(h.ctx(), args)
	if err != nil {
		h.t.Fatalf("handler returned error: %v", err)
	}
	return result
}

func (h *harness) rejectsUnchanged(path string, args map[string]any, pattern string) string {
	h.t.Helper()
	before := h.read(path)
	result := h.run(args)
	if !result.IsError {
		h.t.Fatalf("expected rejection for %s, got %q", path, resultText(result))
	}
	got := resultText(result)
	if pattern != "" && !strings.Contains(got, pattern) {
		h.t.Fatalf("message %q does not contain %q", got, pattern)
	}
	if after := h.read(path); after != before {
		h.t.Fatalf("file must stay unchanged: %s", path)
	}
	return got
}

func resultText(result types.ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(types.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (h *harness) mustSucceed(result types.ToolResult, label string) string {
	h.t.Helper()
	if result.IsError {
		h.t.Fatalf("%s failed: %s", label, resultText(result))
	}
	return resultText(result)
}

func TestEditStringMode(t *testing.T) {
	h := newHarness(t)
	filePath := h.fixture("sample.txt", "alpha beta beta\n")

	result := h.run(map[string]any{"file_path": filePath, "old_string": "alpha", "new_string": "omega"})
	h.mustSucceed(result, "unique")
	if got := h.read(filePath); got != "omega beta beta\n" {
		t.Fatalf("content = %q", got)
	}

	if result := h.run(map[string]any{"file_path": filePath, "old_string": "beta", "new_string": "gamma"}); !result.IsError {
		t.Fatalf("non-unique edit must fail")
	}

	result = h.run(map[string]any{"file_path": filePath, "old_string": "beta", "new_string": "gamma", "replace_all": true})
	h.mustSucceed(result, "replace_all")
	if got := h.read(filePath); got != "omega gamma gamma\n" {
		t.Fatalf("content = %q", got)
	}

	result = h.run(map[string]any{"file_path": filePath, "old_string": "", "new_string": "x", "replace_all": true})
	if !result.IsError || !strings.Contains(resultText(result), "old_string cannot be empty") {
		t.Fatalf("empty old_string = %q", resultText(result))
	}
}

func TestEditImprovements(t *testing.T) {
	h := newHarness(t)

	crlfFile := h.fixture("crlf.txt", "line1\r\nline2\r\nline3\r\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": crlfFile, "old_string": "line1\nline2", "new_string": "changed1\nchanged2",
	}), "crlf")
	if got := h.read(crlfFile); got != "changed1\r\nchanged2\r\nline3\r\n" {
		t.Fatalf("crlf content = %q", got)
	}

	lfFile := h.fixture("lf.txt", "line1\nline2\nline3\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": lfFile, "old_string": "line1\r\nline2", "new_string": "a\r\nb",
	}), "lf")
	if got := h.read(lfFile); got != "a\nb\nline3\n" {
		t.Fatalf("lf content = %q", got)
	}

	wsFile := h.fixture("whitespace.txt", "  let x = 1;  \n  let y = 2;\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": wsFile, "old_string": "  let x = 1;\n  let y = 2;", "new_string": "  let x = 10;\n  let y = 20;",
	}), "trailing whitespace")
	if got := h.read(wsFile); !strings.Contains(got, "let x = 10;") || !strings.Contains(got, "let y = 20;") {
		t.Fatalf("whitespace content = %q", got)
	}

	appliedFile := h.fixture("applied.txt", "new content here\n")
	result := h.run(map[string]any{"file_path": appliedFile, "old_string": "old content", "new_string": "new content here"})
	if !result.IsError || !strings.Contains(resultText(result), "already applied") {
		t.Fatalf("already applied = %q", resultText(result))
	}

	hintFile := h.fixture("hint.txt", "function myFunction() {\n  return 42;\n}\n")
	result = h.run(map[string]any{"file_path": hintFile, "old_string": "function myFunction() {", "new_string": "function myFunction() {"})
	if !result.IsError || !strings.Contains(resultText(result), "identical") {
		t.Fatalf("identical = %q", resultText(result))
	}

	hintFile2 := h.fixture("hint2.txt", "function myFunction() {\n  return 42;\n}\n")
	result = h.run(map[string]any{"file_path": hintFile2, "old_string": "function notHere() {", "new_string": "function gone() {"})
	if !result.IsError || !strings.Contains(resultText(result), "Closest match at line 1") {
		t.Fatalf("closest match = %q", resultText(result))
	}

	batchFile := h.fixture("batch.txt", "alpha beta gamma delta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchFile,
		"edits": []any{
			map[string]any{"old_string": "alpha", "new_string": "one"},
			map[string]any{"old_string": "gamma", "new_string": "three"},
		},
	}), "batch")
	if got := h.read(batchFile); got != "one beta three delta\n" {
		t.Fatalf("batch content = %q", got)
	}

	overlapFile := h.fixture("overlap.txt", "foo bar baz\n")
	h.rejectsUnchanged(overlapFile, map[string]any{
		"file_path": overlapFile,
		"edits": []any{
			map[string]any{"old_string": "foo bar", "new_string": "x"},
			map[string]any{"old_string": "bar baz", "new_string": "y"},
		},
	}, "overlap")

	largeFile := h.fixture("large.txt", strings.Repeat("pattern\n", 50))
	result = h.run(map[string]any{"file_path": largeFile, "old_string": "pattern", "new_string": "replaced"})
	if !result.IsError || !strings.Contains(resultText(result), "not unique") {
		t.Fatalf("large not unique = %q", resultText(result))
	}
}

func TestEditFuzzyReplacers(t *testing.T) {
	h := newHarness(t)

	indentFile := h.fixture("indent.txt", "function f() {\n        return 1;\n}\n")
	h.mustSucceed(h.run(map[string]any{"file_path": indentFile, "old_string": "return 1;", "new_string": "return 2;"}), "indent drift")
	if got := h.read(indentFile); !strings.Contains(got, "return 2;") {
		t.Fatalf("indent content = %q", got)
	}

	wsNormFile := h.fixture("wsnorm.txt", "const   x   =   1;\n")
	h.mustSucceed(h.run(map[string]any{"file_path": wsNormFile, "old_string": "const x = 1;", "new_string": "const x = 2;"}), "ws normalized")
	if got := h.read(wsNormFile); !strings.Contains(got, "const x = 2;") {
		t.Fatalf("ws content = %q", got)
	}

	blockFile := h.fixture("block.txt", "if (cond) {\n  doSomethingElse();\n}\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path":  blockFile,
		"old_string": "if (cond) {\n  doSomething();\n}",
		"new_string": "if (cond) {\n  done();\n}",
	}), "block anchor")
	if got := h.read(blockFile); !strings.Contains(got, "done();") {
		t.Fatalf("block content = %q", got)
	}

	escFile := h.fixture("esc.txt", "line one\nline two\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": escFile, "old_string": `line one\nline two`, "new_string": "changed",
	}), "escape normalized")
	if got := h.read(escFile); !strings.Contains(got, "changed") {
		t.Fatalf("esc content = %q", got)
	}
}

func TestEditUpstreamTiers(t *testing.T) {
	h := newHarness(t)

	uniFile := h.fixture("uni.txt", "it’s a “test” — done\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": uniFile, "old_string": `it's a "test" - done`, "new_string": `it's a "test" - changed`,
	}), "unicode tier")
	if got := h.read(uniFile); !strings.Contains(got, "changed") {
		t.Fatalf("unicode content = %q", got)
	}

	trailFile := h.fixture("trail.txt", "alpha   \nbeta\t\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": trailFile, "old_string": "alpha\nbeta", "new_string": "alpha\nGAMMA",
	}), "trimEnd tier")
	if got := h.read(trailFile); !strings.Contains(got, "GAMMA") {
		t.Fatalf("trail content = %q", got)
	}

	fuzzyOffFile := h.fixture("fuzzyoff.txt", "const   x   =   1;\n")
	h.env["CAVE_TOOLS_FUZZY_EDIT"] = "0"
	result := h.run(map[string]any{"file_path": fuzzyOffFile, "old_string": "const x = 1;", "new_string": "const x = 2;"})
	delete(h.env, "CAVE_TOOLS_FUZZY_EDIT")
	if !result.IsError || !strings.Contains(resultText(result), "Could not find oldString") {
		t.Fatalf("fuzzy off = %q", resultText(result))
	}
}

func TestEditLineRangeMode(t *testing.T) {
	h := newHarness(t)

	midFile := h.fixture("mid.txt", "one\ntwo\nthree\nfour\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": midFile, "start_line": 2, "end_line": 3, "content": "TWO\nTHREE",
	}), "middle replace")
	if got := h.read(midFile); got != "one\nTWO\nTHREE\nfour\n" {
		t.Fatalf("mid content = %q", got)
	}

	insFile := h.fixture("ins.txt", "a\nb\n")
	h.mustSucceed(h.run(map[string]any{"file_path": insFile, "start_line": 2, "end_line": 1, "content": "x"}), "insert")
	if got := h.read(insFile); got != "a\nx\nb\n" {
		t.Fatalf("insert content = %q", got)
	}

	appFile := h.fixture("app.txt", "a\nb\n")
	h.mustSucceed(h.run(map[string]any{"file_path": appFile, "start_line": 3, "end_line": 2, "content": "c"}), "append")
	if got := h.read(appFile); got != "a\nb\nc\n" {
		t.Fatalf("append content = %q", got)
	}

	result := h.run(map[string]any{"file_path": midFile, "start_line": 99, "end_line": 99, "content": "x"})
	if !result.IsError || !strings.Contains(resultText(result), "out of range") {
		t.Fatalf("bounds = %q", resultText(result))
	}

	result = h.run(map[string]any{
		"file_path": midFile, "old_string": "one", "new_string": "ONE",
		"start_line": 1, "end_line": 1, "content": "ONE",
	})
	if !result.IsError || !strings.Contains(resultText(result), "either") {
		t.Fatalf("mutual exclusion = %q", resultText(result))
	}

	hashFile := h.fixture("hash.txt", "hello\n")
	result = h.run(map[string]any{
		"file_path": hashFile, "start_line": 1, "end_line": 1, "content": "bye",
		"expected_hash": "deadbeefdeadbeef",
	})
	if !result.IsError || !strings.Contains(resultText(result), "expected_hash mismatch") {
		t.Fatalf("hash mismatch = %q", resultText(result))
	}

	realHash := compression.HashContent([]byte(h.read(hashFile)))
	h.mustSucceed(h.run(map[string]any{
		"file_path": hashFile, "start_line": 1, "end_line": 1, "content": "bye",
		"expected_hash": realHash[:12],
	}), "hash prefix")
	if got := h.read(hashFile); got != "bye\n" {
		t.Fatalf("hash content = %q", got)
	}

	batchFile := h.fixture("batch.txt", "1\n2\n3\n4\n5\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchFile,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "A"},
			map[string]any{"start_line": 4, "end_line": 5, "content": "D\nE"},
		},
	}), "batch ranges")
	if got := h.read(batchFile); got != "A\n2\n3\nD\nE\n" {
		t.Fatalf("batch ranges content = %q", got)
	}

	csFile := h.fixture("cs.txt", "a\nb\nc\n")
	csVal := tools.RangeChecksum([]string{"a", "b", "c", ""}, 2, 2)
	h.mustSucceed(h.run(map[string]any{
		"file_path": csFile, "start_line": 2, "end_line": 2, "content": "B",
		"expected_range_checksum": csVal,
	}), "checksum ok")
	if got := h.read(csFile); got != "a\nB\nc\n" {
		t.Fatalf("checksum content = %q", got)
	}

	h.fixture("cs.txt", "a\nb\nc\n")
	result = h.run(map[string]any{
		"file_path": csFile, "start_line": 2, "end_line": 2, "content": "B",
		"expected_range_checksum": "ffffffff",
	})
	if !result.IsError || !strings.Contains(resultText(result), "expected_range_checksum mismatch") {
		t.Fatalf("checksum mismatch = %q", resultText(result))
	}
	if !strings.Contains(resultText(result), "window:") {
		t.Fatalf("checksum window missing: %q", resultText(result))
	}

	delFile := h.fixture("del.txt", "1\n2\n3\n")
	h.mustSucceed(h.run(map[string]any{"file_path": delFile, "start_line": 2, "end_line": 2, "delete": true}), "delete")
	if got := h.read(delFile); got != "1\n3\n" {
		t.Fatalf("delete content = %q", got)
	}

	mvFile := h.fixture("mv.txt", "a\nb\nc\nd\n")
	h.mustSucceed(h.run(map[string]any{"file_path": mvFile, "start_line": 2, "end_line": 3, "insert_before": 5}), "move")
	if got := h.read(mvFile); got != "a\nd\nb\nc\n" {
		t.Fatalf("move content = %q", got)
	}

	mf1 := h.fixture("mf1.txt", "x\n")
	mf2 := h.fixture("mf2.txt", "y\n")
	h.mustSucceed(h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": mf1, "start_line": 1, "end_line": 1, "content": "X"},
			map[string]any{"file_path": mf2, "old_string": "y", "new_string": "Y"},
		},
	}), "multi-file batch")
	if h.read(mf1) != "X\n" || h.read(mf2) != "Y\n" {
		t.Fatalf("multi-file content = %q / %q", h.read(mf1), h.read(mf2))
	}

	h.fixture("strict.txt", "a\nb\n")
	strictFile := filepath.Join(h.dir, "strict.txt")
	h.env["CAVE_TOOLS_MODE"] = "strict"
	result = h.run(map[string]any{"file_path": strictFile, "start_line": 1, "end_line": 1, "content": "A"})
	if !result.IsError || !strings.Contains(resultText(result), "STRICT") {
		t.Fatalf("strict failure = %q", resultText(result))
	}
	h.mustSucceed(h.run(map[string]any{
		"file_path": strictFile, "start_line": 1, "end_line": 1, "content": "A",
		"expected_range_checksum": tools.RangeChecksum([]string{"a", "b", ""}, 1, 1),
	}), "strict proof")
	h.env["CAVE_TOOLS_MODE"] = "enforce"
}

func TestEditHandlerV2(t *testing.T) {
	h := newHarness(t)

	// A. string vectors
	unicodeFile := h.fixture("unicode.txt", "it’s a “test” — done\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": unicodeFile, "old_string": `it's a "test" - done`, "new_string": `it's a "test" - changed`,
	}), "A1 unicode")
	if got := h.read(unicodeFile); got != "it's a \"test\" - changed\n" {
		t.Fatalf("A1 content = %q", got)
	}

	trailFile := h.fixture("trail2.txt", "alpha   \nbeta\t\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": trailFile, "old_string": "alpha\nbeta", "new_string": "alpha\nGAMMA",
	}), "A2 trailing")
	if got := h.read(trailFile); got != "alpha\nGAMMA\n" {
		t.Fatalf("A2 content = %q", got)
	}

	crlfFile := h.fixture("crlf2.txt", "line1\r\nline2\r\nline3\r\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": crlfFile, "old_string": "line1\nline2", "new_string": "changed1\nchanged2",
	}), "A3 crlf")
	if got := h.read(crlfFile); got != "changed1\r\nchanged2\r\nline3\r\n" {
		t.Fatalf("A3 content = %q", got)
	}

	lfFile := h.fixture("lf2.txt", "line1\nline2\nline3\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": lfFile, "old_string": "line1\r\nline2", "new_string": "a\r\nb",
	}), "A4 lf")
	if got := h.read(lfFile); got != "a\nb\nline3\n" {
		t.Fatalf("A4 content = %q", got)
	}

	variantsFile := h.fixture("variants.txt", "\u2018a\u2019 and \u201Aa\u201B\n")
	message := h.mustSucceed(h.run(map[string]any{
		"file_path": variantsFile, "old_string": "'a'", "new_string": "'b'", "replace_all": true,
	}), "A5 variants")
	if got := h.read(variantsFile); got != "'b' and 'b'\n" {
		t.Fatalf("A5 content = %q", got)
	}
	if !strings.Contains(message, "(2 replacements)") {
		t.Fatalf("A5 count = %q", message)
	}

	unchangedFile := h.fixture("unchanged.txt", "\u2018hi\u2019\n")
	result := h.run(map[string]any{"file_path": unchangedFile, "old_string": "'hi'", "new_string": "‘hi’"})
	if !result.IsError || !strings.Contains(resultText(result), "already applied") {
		t.Fatalf("A6 already applied = %q", resultText(result))
	}
	if got := h.read(unchangedFile); got != "\u2018hi\u2019\n" {
		t.Fatalf("A6 unchanged = %q", got)
	}

	endingsFile := h.fixture("endings.txt", "a\nb\n")
	h.mustSucceed(h.run(map[string]any{"file_path": endingsFile, "old_string": "a\nb", "new_string": "a\r\nb"}), "A7 endingsOnly")
	if got := h.read(endingsFile); got != "a\r\nb\n" {
		t.Fatalf("A7 bytes = %q", got)
	}

	fuzzyFile := h.fixture("fuzzy.txt", "const   x   =   1;\n")
	h.mustSucceed(h.run(map[string]any{"file_path": fuzzyFile, "old_string": "const x = 1;", "new_string": "const x = 2;"}), "A8 fuzzy")
	if got := h.read(fuzzyFile); !strings.Contains(got, "const x = 2;") {
		t.Fatalf("A8 content = %q", got)
	}
	h.env["CAVE_TOOLS_FUZZY_EDIT"] = "0"
	h.mustSucceed(h.run(map[string]any{"file_path": fuzzyFile, "old_string": "const x = 2;", "new_string": "const x = 3;"}), "A8 strict exact")
	result = h.run(map[string]any{"file_path": fuzzyFile, "old_string": "const   x   =   3;", "new_string": "QQQZZZ"})
	if !result.IsError || !strings.Contains(resultText(result), "Could not find oldString") {
		t.Fatalf("A8 strict miss = %q", resultText(result))
	}
	delete(h.env, "CAVE_TOOLS_FUZZY_EDIT")

	nonUniqueFile := h.fixture("nonunique.txt", "beta beta\n")
	result = h.run(map[string]any{"file_path": nonUniqueFile, "old_string": "beta", "new_string": "gamma"})
	if !result.IsError || !strings.Contains(resultText(result), "not unique") {
		t.Fatalf("A9 nonUnique = %q", resultText(result))
	}
	if got := h.read(nonUniqueFile); got != "beta beta\n" {
		t.Fatalf("A9 unchanged = %q", got)
	}

	hintFile := h.fixture("hint3.txt", "function myFunction() {\n  return 42;\n}\n")
	result = h.run(map[string]any{"file_path": hintFile, "old_string": "function notHere() {", "new_string": "x"})
	if !result.IsError || !strings.Contains(resultText(result), "Closest match") {
		t.Fatalf("A10 hint = %q", resultText(result))
	}

	crlfMultiFile := h.fixture("crlfmulti.txt", "a\r\nb\r\n")
	h.mustSucceed(h.run(map[string]any{"file_path": crlfMultiFile, "old_string": "b", "new_string": "x\ny"}), "A11 crlf multiline")
	if got := h.read(crlfMultiFile); got != "a\r\nx\r\ny\r\n" {
		t.Fatalf("A11 content = %q", got)
	}

	mixedMatchFile := h.fixture("mixedmatch.txt", "a\r\nb\nc\r\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": mixedMatchFile, "old_string": "a\r\nb\nc", "new_string": "a\nB\nc\nd",
	}), "A12 mixed region")
	if got := h.read(mixedMatchFile); got != "a\r\nB\nc\r\nd\r\n" {
		t.Fatalf("A12 content = %q", got)
	}

	// B. pure-string batches
	batchFile := h.fixture("batch2.txt", "alpha beta gamma delta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchFile,
		"edits": []any{
			map[string]any{"old_string": "alpha", "new_string": "one"},
			map[string]any{"old_string": "gamma", "new_string": "three"},
		},
	}), "B1 batch")
	if got := h.read(batchFile); got != "one beta three delta\n" {
		t.Fatalf("B1 content = %q", got)
	}

	batchRaFile := h.fixture("batchra.txt", "foo foo\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchRaFile,
		"edits":     []any{map[string]any{"old_string": "foo", "new_string": "bar", "replace_all": true}},
	}), "B2 batch replace_all")
	if got := h.read(batchRaFile); got != "bar bar\n" {
		t.Fatalf("B2 content = %q", got)
	}

	batchOverlapFile := h.fixture("batchoverlap.txt", "foo bar baz\n")
	h.rejectsUnchanged(batchOverlapFile, map[string]any{
		"file_path": batchOverlapFile,
		"edits": []any{
			map[string]any{"old_string": "foo bar", "new_string": "x"},
			map[string]any{"old_string": "bar baz", "new_string": "y"},
		},
	}, "overlap")

	batchExpandFile := h.fixture("batchexpand.txt", "foo foo\n")
	h.rejectsUnchanged(batchExpandFile, map[string]any{
		"file_path": batchExpandFile,
		"edits": []any{
			map[string]any{"old_string": "foo", "new_string": "x", "replace_all": true},
			map[string]any{"old_string": "foo", "new_string": "y"},
		},
	}, "not unique")

	batchVariantsFile := h.fixture("batchvariants.txt", "\u2018a\u2019 and \u201Aa\u201B\n")
	result = h.run(map[string]any{
		"file_path": batchVariantsFile,
		"edits": []any{
			map[string]any{"old_string": "x", "new_string": "1"},
			map[string]any{"old_string": "'a'", "new_string": "'b'", "replace_all": true},
		},
	})
	if !result.IsError {
		t.Fatalf("B5 batch miss must fail")
	}
	if got := h.read(batchVariantsFile); got != "\u2018a\u2019 and \u201Aa\u201B\n" {
		t.Fatalf("B5 unchanged = %q", got)
	}
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchVariantsFile,
		"edits":     []any{map[string]any{"old_string": "'a'", "new_string": "'b'", "replace_all": true}},
	}), "B5 variants")
	if got := h.read(batchVariantsFile); got != "'b' and 'b'\n" {
		t.Fatalf("B5 content = %q", got)
	}

	// C. mixed range+string batches
	mixedFile := h.fixture("mixed.txt", "one\ntwo\nthree\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": mixedFile,
		"edits": []any{
			map[string]any{"start_line": 2, "end_line": 2, "content": "TWO"},
			map[string]any{"old_string": "three", "new_string": "THREE"},
		},
	}), "C1 mixed")
	if got := h.read(mixedFile); got != "one\nTWO\nTHREE\n" {
		t.Fatalf("C1 content = %q", got)
	}

	mixedRevFile := h.fixture("mixedrev.txt", "one\ntwo\nthree\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": mixedRevFile,
		"edits": []any{
			map[string]any{"old_string": "three", "new_string": "THREE"},
			map[string]any{"start_line": 2, "end_line": 2, "content": "TWO"},
		},
	}), "C2 mixed reversed")
	if got := h.read(mixedRevFile); got != "one\nTWO\nTHREE\n" {
		t.Fatalf("C2 content = %q", got)
	}

	mixedNoopFile := h.fixture("mixednoop.txt", "one\ntwo\nthree\n")
	h.rejectsUnchanged(mixedNoopFile, map[string]any{
		"file_path": mixedNoopFile,
		"edits": []any{
			map[string]any{"start_line": 2, "end_line": 2, "content": "TWO"},
			map[string]any{"old_string": "missing", "new_string": "X"},
		},
	}, "Could not find")

	mixedIdenticalFile := h.fixture("mixedidentical.txt", "one\ntwo\n")
	h.rejectsUnchanged(mixedIdenticalFile, map[string]any{
		"file_path": mixedIdenticalFile,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "ONE"},
			map[string]any{"old_string": "two", "new_string": "two"},
		},
	}, "identical")

	// D. BOM preservation
	bomFile := h.fixture("bom.txt", "\uFEFFalpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{"file_path": bomFile, "old_string": "beta", "new_string": "BETA"}), "D1 bom string")
	if got := h.read(bomFile); got != "\uFEFFalpha\nBETA\n" {
		t.Fatalf("D1 content = %q", got)
	}

	bomRangeFile := h.fixture("bomrange.txt", "\uFEFFalpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{"file_path": bomRangeFile, "start_line": 1, "end_line": 1, "content": "ALPHA"}), "D2 bom range")
	if got := h.read(bomRangeFile); got != "\uFEFFALPHA\nbeta\n" {
		t.Fatalf("D2 content = %q", got)
	}

	bomCrlfFile := h.fixture("bomcrlf.txt", "\uFEFFa\r\nb\r\n")
	h.mustSucceed(h.run(map[string]any{"file_path": bomCrlfFile, "start_line": 2, "end_line": 2, "content": "B"}), "D3 bom+crlf")
	if got := h.read(bomCrlfFile); got != "\uFEFFa\r\nB\r\n" {
		t.Fatalf("D3 content = %q", got)
	}

	// E. range EOF + mixed EOL
	appendFile := h.fixture("append.txt", "a\nb\n")
	h.mustSucceed(h.run(map[string]any{"file_path": appendFile, "start_line": 4, "end_line": 3, "content": "c"}), "E1 append")
	if got := h.read(appendFile); got != "a\nb\nc\n" {
		t.Fatalf("E1 content = %q", got)
	}

	appendNoNLFile := h.fixture("appendnonl.txt", "a\nb")
	h.mustSucceed(h.run(map[string]any{"file_path": appendNoNLFile, "start_line": 3, "end_line": 2, "content": "c"}), "E2 append no trailing NL")
	if got := h.read(appendNoNLFile); got != "a\nb\nc" {
		t.Fatalf("E2 content = %q", got)
	}

	mixRangeFile := h.fixture("mixrange.txt", "1\r\n2\n3\r\n4\n")
	h.mustSucceed(h.run(map[string]any{"file_path": mixRangeFile, "start_line": 2, "end_line": 2, "content": "TWO"}), "E3 mixed range")
	if got := h.read(mixRangeFile); got != "1\r\nTWO\n3\r\n4\n" {
		t.Fatalf("E3 content = %q", got)
	}

	mixMoveFile := h.fixture("mixmove.txt", "1\r\n2\n3\r\n4\n")
	h.mustSucceed(h.run(map[string]any{"file_path": mixMoveFile, "start_line": 1, "end_line": 1, "insert_before": 5}), "E4 mixed move")
	if got := h.read(mixMoveFile); got != "2\n3\r\n4\n1\r\n" {
		t.Fatalf("E4 content = %q", got)
	}
}

func TestEditArgContract(t *testing.T) {
	h := newHarness(t)
	file := h.fixture("invalid.txt", "alpha\n")

	for _, label := range []string{"F1 undefined", "F2 null"} {
		result, err := handle(h.ctx(), nil)
		if err != nil {
			t.Fatalf("%s threw: %v", label, err)
		}
		if !result.IsError {
			t.Fatalf("%s must be an error", label)
		}
		if got := h.read(file); got != "alpha\n" {
			t.Fatalf("%s changed the file", label)
		}
	}

	h.rejectsUnchanged(file, map[string]any{"file_path": file, "old_string": 5, "new_string": "x"}, "old_string must be a string")
	h.rejectsUnchanged(file, map[string]any{"file_path": 5, "old_string": "alpha", "new_string": "x"}, "file_path must be a string")
	h.rejectsUnchanged(file, map[string]any{"file_path": file, "old_string": "alpha", "new_string": "x", "replace_all": 1}, "replace_all must be a boolean")
	h.rejectsUnchanged(file, map[string]any{"file_path": file, "start_line": 1, "end_line": 1, "content": 5}, "content must be a string")
	h.rejectsUnchanged(file, map[string]any{"file_path": file, "start_line": "1", "end_line": 1, "content": "x"}, "start_line must be a number")
	h.rejectsUnchanged(file, map[string]any{
		"file_path": file, "edits": []any{map[string]any{"old_string": "alpha", "new_string": 5}},
	}, "new_string is required")
	h.rejectsUnchanged(file, map[string]any{"file_path": file, "edits": []any{nil}}, "must be an edit object")
	h.rejectsUnchanged(file, map[string]any{
		"file_path": file, "edits": []any{map[string]any{"old_string": 5, "new_string": "x"}},
	}, "old_string must be a string")
	h.rejectsUnchanged(file, map[string]any{
		"file_path": file, "edits": []any{map[string]any{"start_line": "1", "end_line": 1, "content": "x"}},
	}, "start_line must be a number")
	h.rejectsUnchanged(file, map[string]any{"file_path": file, "edits": []any{}}, "edits array is empty")

	// test-edit-schema.mjs invalid inputs, handler half.
	other := h.fixture("other.txt", "alpha\n")
	str := map[string]any{"old_string": "alpha", "new_string": "ALPHA"}
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing everything", map[string]any{}, "file_path is required"},
		{"missing single path", str, "file_path is required"},
		{"empty single path", map[string]any{"file_path": "", "old_string": "alpha", "new_string": "ALPHA"}, "file_path is required"},
		{"missing mode", map[string]any{"file_path": file}, "need old_string/new_string"},
		{"missing new string", map[string]any{"file_path": file, "old_string": "alpha"}, "new_string is required"},
		{"null new string", map[string]any{"file_path": file, "old_string": "alpha", "new_string": nil}, "new_string is required"},
		{"empty search", map[string]any{"file_path": file, "old_string": "", "new_string": "x"}, "old_string cannot be empty"},
		{"native key names", map[string]any{"path": file, "oldString": "alpha", "newString": "ALPHA"}, "file_path is required"},
		{"range missing end", map[string]any{"file_path": file, "start_line": 1, "content": "ALPHA"}, "start_line and end_line must be integers"},
		{"range missing body", map[string]any{"file_path": file, "start_line": 1, "end_line": 1}, "need old_string/new_string"},
		{"false delete without body", map[string]any{"file_path": file, "start_line": 1, "end_line": 1, "delete": false}, "need old_string/new_string"},
		{"move missing end", map[string]any{"file_path": file, "start_line": 1, "insert_before": 2}, "start_line, end_line, and insert_before must be integers"},
		{"fractional lines", map[string]any{"file_path": file, "start_line": 1, "end_line": 1.5, "content": "ALPHA"}, "start_line and end_line must be integers"},
		{"empty batch", map[string]any{"file_path": file, "edits": []any{}}, "edits array is empty"},
		{"batch missing target", map[string]any{"edits": []any{str}}, "file_path required"},
		{"batch empty item target", map[string]any{"edits": []any{map[string]any{"file_path": "", "old_string": "alpha", "new_string": "ALPHA"}}}, "file_path required"},
		{"batch partially missing targets", map[string]any{"edits": []any{map[string]any{"file_path": other, "old_string": "alpha", "new_string": "ALPHA"}, str}}, "file_path required"},
		{"batch missing item replacement", map[string]any{"file_path": file, "edits": []any{map[string]any{"old_string": "alpha"}}}, "new_string is required"},
		{"batch missing item mode", map[string]any{"edits": []any{map[string]any{"file_path": other}}}, "need old_string/new_string"},
		{"batch item null", map[string]any{"file_path": file, "edits": []any{nil}}, "must be an edit object"},
		{"wrong batch type", map[string]any{"file_path": file, "edits": "not an array"}, "edits must be an array"},
		{"mixed single and batch", map[string]any{"file_path": file, "old_string": "alpha", "new_string": "ALPHA", "edits": []any{str}}, "batch operation fields belong inside edits[]"},
	}
	for _, key := range editOperationKeys {
		cases = append(cases, struct {
			name string
			args map[string]any
			want string
		}{
			name: "batch top-level operation " + key,
			args: map[string]any{"file_path": file, key: "x", "edits": []any{str}},
			want: "batch operation fields belong inside edits[]",
		})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			before := h.read(file)
			beforeOther := h.read(other)
			result := h.run(tt.args)
			if !result.IsError {
				t.Fatalf("expected rejection, got %q", resultText(result))
			}
			if got := resultText(result); !strings.Contains(got, tt.want) {
				t.Fatalf("message %q does not contain %q", got, tt.want)
			}
			if h.read(file) != before || h.read(other) != beforeOther {
				t.Fatalf("%s changed a file", tt.name)
			}
		})
	}
}

func TestEditBatchGuards(t *testing.T) {
	h := newHarness(t)

	guardFile := h.fixture("guard.txt", "1\n2\n3\n4\n5\n6\n")
	h.rejectsUnchanged(guardFile, map[string]any{
		"file_path": guardFile,
		"edits": []any{
			map[string]any{"start_line": 3, "end_line": 6, "content": "X"},
			map[string]any{"start_line": 5, "end_line": 4, "content": "y"},
		},
	}, "lies inside")

	guardStartFile := h.fixture("guardstart.txt", "1\n2\n3\n4\n5\n")
	h.rejectsUnchanged(guardStartFile, map[string]any{
		"file_path": guardStartFile,
		"edits": []any{
			map[string]any{"start_line": 3, "end_line": 5, "content": "X"},
			map[string]any{"start_line": 3, "end_line": 2, "content": "y"},
		},
	}, "lies inside")

	guardAfterFile := h.fixture("guardafter.txt", "1\n2\n3\n4\n5\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": guardAfterFile,
		"edits": []any{
			map[string]any{"start_line": 3, "end_line": 4, "content": "X"},
			map[string]any{"start_line": 5, "end_line": 4, "content": "y"},
		},
	}), "G3 insert after range")
	if got := h.read(guardAfterFile); got != "1\n2\nX\ny\n5\n" {
		t.Fatalf("G3 content = %q", got)
	}

	guardSameFile := h.fixture("guardsame.txt", "1\n2\n3\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": guardSameFile,
		"edits": []any{
			map[string]any{"start_line": 2, "end_line": 1, "content": "A"},
			map[string]any{"start_line": 2, "end_line": 1, "content": "B"},
		},
	}), "G4 same-point inserts")
	if got := h.read(guardSameFile); got != "1\nB\nA\n2\n3\n" {
		t.Fatalf("G4 content = %q", got)
	}

	guardMoveFile := h.fixture("guardmove.txt", "1\n2\n3\n4\n")
	h.rejectsUnchanged(guardMoveFile, map[string]any{
		"file_path": guardMoveFile,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 2, "insert_before": 5},
			map[string]any{"start_line": 3, "end_line": 3, "insert_before": 1},
		},
	}, "separate calls")

	guardMixFile := h.fixture("guardmix.txt", "1\n2\n3\n4\n")
	h.rejectsUnchanged(guardMixFile, map[string]any{
		"file_path": guardMixFile,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 2, "insert_before": 5},
			map[string]any{"start_line": 3, "end_line": 3, "content": "X"},
		},
	}, "cannot mix")
}

func TestEditAliasesAndIndexes(t *testing.T) {
	h := newHarness(t)

	aliasFile := h.fixture("alias.txt", "one\ntwo\n")
	// Deliberately un-normalized spelling (a join would collapse the "..").
	aliasSpelling := filepath.Dir(aliasFile) + "/sub/../" + filepath.Base(aliasFile)
	result := h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": aliasSpelling, "old_string": "one", "new_string": "ONE"},
			map[string]any{"file_path": aliasFile, "old_string": "two", "new_string": "TWO"},
		},
	})
	if !result.IsError || !strings.Contains(resultText(result), "same file") {
		t.Fatalf("H2 alias = %q", resultText(result))
	}
	if got := h.read(aliasFile); got != "one\ntwo\n" {
		t.Fatalf("H2 unchanged = %q", got)
	}

	sameLitFile := h.fixture("samelit.txt", "one\ntwo\n")
	h.mustSucceed(h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": sameLitFile, "old_string": "one", "new_string": "ONE"},
			map[string]any{"file_path": sameLitFile, "old_string": "two", "new_string": "TWO"},
		},
	}), "H3 same literal")
	if got := h.read(sameLitFile); got != "ONE\nTWO\n" {
		t.Fatalf("H3 content = %q", got)
	}

	missingFile := h.fixture("rel-atomic-a.txt", "alpha\n")
	absentFile := filepath.Join(h.dir, "rel-atomic-missing.txt")
	result = h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": missingFile, "old_string": "alpha", "new_string": "ALPHA"},
			map[string]any{"file_path": absentFile, "old_string": "beta", "new_string": "BETA"},
		},
	})
	if !result.IsError || !strings.Contains(resultText(result), "Cannot read file") {
		t.Fatalf("I5 missing file = %q", resultText(result))
	}
	if got := h.read(missingFile); got != "alpha\n" {
		t.Fatalf("I5 first file unchanged = %q", got)
	}

	idxA := h.fixture("rel-idx-a.txt", "one\ntwo\n")
	idxB := h.fixture("rel-idx-b.txt", "three\n")
	result = h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": idxA, "old_string": "one", "new_string": "ONE"},
			map[string]any{"file_path": idxA, "old_string": "two", "new_string": "TWO"},
			map[string]any{"file_path": idxB, "old_string": "three", "new_string": "THREE"},
			map[string]any{"file_path": idxB, "old_string": "missing", "new_string": "X"},
		},
	})
	if !result.IsError || !strings.Contains(resultText(result), "edits[3]") {
		t.Fatalf("I6 global index = %q", resultText(result))
	}
	if h.read(idxA) != "one\ntwo\n" || h.read(idxB) != "three\n" {
		t.Fatalf("I6 files changed")
	}

	hashA := h.fixture("rel-hash-a.txt", "aaa\n")
	hashB := h.fixture("rel-hash-b.txt", "bbb\n")
	result = h.run(map[string]any{
		"expected_hash": strings.Repeat("0", 64),
		"edits": []any{
			map[string]any{"file_path": hashA, "old_string": "aaa", "new_string": "AAA"},
			map[string]any{"file_path": hashB, "old_string": "bbb", "new_string": "BBB"},
		},
	})
	if !result.IsError || !strings.Contains(resultText(result), "single-file") {
		t.Fatalf("I7 top hash = %q", resultText(result))
	}
	if h.read(hashA) != "aaa\n" || h.read(hashB) != "bbb\n" {
		t.Fatalf("I7 files changed")
	}
}

func TestEditStrictBatch(t *testing.T) {
	h := newHarness(t)
	file := h.fixture("strict-batch.txt", "x\ny\n")
	hash := compression.HashContent([]byte(h.read(file)))
	h.env["CAVE_TOOLS_MODE"] = "strict"
	h.mustSucceed(h.run(map[string]any{
		"file_path":     file,
		"expected_hash": hash,
		"edits":         []any{map[string]any{"start_line": 1, "end_line": 1, "content": "X"}},
	}), "strict batch top-level hash")
	if got := h.read(file); got != "X\ny\n" {
		t.Fatalf("strict batch content = %q", got)
	}
}

// TS 7c81d32 (scripts/test-port-bugs.mjs 18-21): strict proofs must be
// nonempty strings. A stringified null/number/boolean must not satisfy the
// gate, and a rejected edit must leave the file untouched.
func TestEditStrictProofValues(t *testing.T) {
	h := newHarness(t)
	h.env["CAVE_TOOLS_MODE"] = "strict"
	strictProof := "STRICT: range/move edit requires expected_hash or expected_range_checksum"

	nullHash := h.fixture("strict-null-hash.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(nullHash, map[string]any{
		"file_path": nullHash, "start_line": 1, "end_line": 1,
		"content": "ALPHA", "expected_hash": nil,
	}, strictProof)

	nullChecksum := h.fixture("strict-null-checksum.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(nullChecksum, map[string]any{
		"file_path": nullChecksum, "start_line": 1, "end_line": 1,
		"insert_before": 3, "expected_range_checksum": nil,
	}, strictProof)

	numberChecksum := h.fixture("strict-number-checksum.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(numberChecksum, map[string]any{
		"file_path": numberChecksum, "start_line": 1, "end_line": 1,
		"content": "ALPHA", "expected_range_checksum": 42,
	}, strictProof)

	boolChecksum := h.fixture("strict-bool-checksum.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(boolChecksum, map[string]any{
		"file_path": boolChecksum, "start_line": 1, "end_line": 1,
		"content": "ALPHA", "expected_range_checksum": false,
	}, strictProof)

	batchNullHash := h.fixture("strict-batch-null-hash.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(batchNullHash, map[string]any{
		"file_path":     batchNullHash,
		"expected_hash": nil,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "ALPHA", "expected_hash": nil},
		},
	}, "edits[0]: "+strictProof)

	batchNullChecksum := h.fixture("strict-batch-null-checksum.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(batchNullChecksum, map[string]any{
		"file_path": batchNullChecksum,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "insert_before": 3, "expected_range_checksum": nil},
		},
	}, "edits[0]: "+strictProof)

	batchNumberChecksum := h.fixture("strict-batch-number-checksum.txt", "alpha\nbeta\n")
	h.rejectsUnchanged(batchNumberChecksum, map[string]any{
		"file_path": batchNumberChecksum,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "ALPHA", "expected_range_checksum": 42},
		},
	}, "edits[0]: "+strictProof)

	// Valid string proofs still pass the gate and edit the file.
	validHash := h.fixture("strict-valid-hash.txt", "alpha\nbeta\n")
	hash := compression.HashContent([]byte(h.read(validHash)))
	h.mustSucceed(h.run(map[string]any{
		"file_path": validHash, "start_line": 1, "end_line": 1,
		"content": "ALPHA", "expected_hash": hash,
	}), "strict valid hash")
	if got := h.read(validHash); got != "ALPHA\nbeta\n" {
		t.Fatalf("strict valid hash content = %q", got)
	}

	validChecksum := tools.RangeChecksum([]string{"alpha", "beta", ""}, 1, 1)
	validMove := h.fixture("strict-valid-move.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": validMove, "start_line": 1, "end_line": 1,
		"insert_before": 3, "expected_range_checksum": validChecksum,
	}), "strict valid move checksum")
	if got := h.read(validMove); got != "beta\nalpha\n" {
		t.Fatalf("strict valid move content = %q", got)
	}

	validBatchRange := h.fixture("strict-valid-batch-range.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": validBatchRange,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "ALPHA", "expected_range_checksum": validChecksum},
		},
	}), "strict valid batch range checksum")
	if got := h.read(validBatchRange); got != "ALPHA\nbeta\n" {
		t.Fatalf("strict valid batch range content = %q", got)
	}

	validBatchMove := h.fixture("strict-valid-batch-move.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": validBatchMove,
		"edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "insert_before": 3, "expected_range_checksum": validChecksum},
		},
	}), "strict valid batch move checksum")
	if got := h.read(validBatchMove); got != "beta\nalpha\n" {
		t.Fatalf("strict valid batch move content = %q", got)
	}
}

// TS Number(null) == 0: an explicit null end_line (or a null start_line on
// the batch delete:true path) parses as 0, while an absent key stays NaN.
func TestEditNullLineNumbers(t *testing.T) {
	h := newHarness(t)

	insertFile := h.fixture("null-end-range.txt", "line1\nline2\n")
	message := h.mustSucceed(h.run(map[string]any{
		"file_path": insertFile, "start_line": 1, "end_line": nil, "content": "INSERTED",
	}), "single range null end_line")
	if !strings.Contains(message, "(insert lines 1-0)") {
		t.Fatalf("single range message = %q", message)
	}
	if got := h.read(insertFile); got != "INSERTED\nline1\nline2\n" {
		t.Fatalf("single range content = %q", got)
	}

	batchFile := h.fixture("null-end-range-batch.txt", "line1\nline2\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchFile,
		"edits":     []any{map[string]any{"start_line": 1, "end_line": nil, "content": "INSERTED"}},
	}), "batch range null end_line")
	if got := h.read(batchFile); got != "INSERTED\nline1\nline2\n" {
		t.Fatalf("batch range content = %q", got)
	}

	moveFile := h.fixture("null-end-move.txt", "line1\nline2\n")
	h.rejectsUnchanged(moveFile, map[string]any{
		"file_path": moveFile, "start_line": 1, "end_line": nil, "insert_before": 3,
	}, "move requires end_line >= start_line >= 1")

	batchMoveFile := h.fixture("null-end-move-batch.txt", "line1\nline2\n")
	h.rejectsUnchanged(batchMoveFile, map[string]any{
		"file_path": batchMoveFile,
		"edits":     []any{map[string]any{"start_line": 1, "end_line": nil, "insert_before": 3}},
	}, "edits[0]: move requires end_line >= start_line >= 1")

	// Absent keys stay NaN in Number(): the integer gate still fires.
	missingEnd := h.fixture("missing-end.txt", "line1\nline2\n")
	h.rejectsUnchanged(missingEnd, map[string]any{
		"file_path": missingEnd, "start_line": 1, "content": "X",
	}, "start_line and end_line must be integers for range mode")
	missingEndBatch := h.fixture("missing-end-batch.txt", "line1\nline2\n")
	h.rejectsUnchanged(missingEndBatch, map[string]any{
		"file_path": missingEndBatch,
		"edits":     []any{map[string]any{"start_line": 1, "content": "X"}},
	}, "edits[0]: start_line and end_line must be integers")

	// Batch delete:true reaches parsing with a null start_line; Number(null)
	// is 0, so the range bounds check (not the integer gate) rejects it.
	deleteFile := h.fixture("null-start-delete.txt", "line1\nline2\n")
	h.rejectsUnchanged(deleteFile, map[string]any{
		"file_path": deleteFile,
		"edits":     []any{map[string]any{"start_line": nil, "end_line": 2, "delete": true}},
	}, "edits[0]: start_line must be >= 1 (got 0)")

	// A non-number end_line is still rejected by the field-type gate.
	h.rejectsUnchanged(insertFile, map[string]any{
		"file_path": insertFile, "start_line": 1, "end_line": "1", "content": "X",
	}, "end_line must be a number")
}

func TestEditReliability(t *testing.T) {
	h := newHarness(t)

	// 1. string-mode new_string validation
	missingNew := h.fixture("missing-new.txt", "alpha\n")
	h.rejectsUnchanged(missingNew, map[string]any{"file_path": missingNew, "old_string": "alpha"}, "new_string is required")

	nullNew := h.fixture("null-new.txt", "alpha\n")
	h.rejectsUnchanged(nullNew, map[string]any{"file_path": nullNew, "old_string": "alpha", "new_string": nil}, "new_string is required")

	numberNew := h.fixture("number-new.txt", "alpha\n")
	h.rejectsUnchanged(numberNew, map[string]any{"file_path": numberNew, "old_string": "alpha", "new_string": 5}, "new_string is required")

	emptyDelete := h.fixture("empty-delete.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{"file_path": emptyDelete, "old_string": "alpha\n", "new_string": ""}), "empty delete")
	if got := h.read(emptyDelete); got != "beta\n" {
		t.Fatalf("empty delete content = %q", got)
	}

	batchMissingNew := h.fixture("batch-missing-new.txt", "alpha\n")
	h.rejectsUnchanged(batchMissingNew, map[string]any{"file_path": batchMissingNew, "edits": []any{map[string]any{"old_string": "alpha"}}}, "new_string is required")

	batchNumberNew := h.fixture("batch-number-new.txt", "alpha\n")
	h.rejectsUnchanged(batchNumberNew, map[string]any{"file_path": batchNumberNew, "edits": []any{map[string]any{"old_string": "alpha", "new_string": 5}}}, "new_string is required")

	nullItem := h.fixture("null-item.txt", "alpha\n")
	h.rejectsUnchanged(nullItem, map[string]any{"file_path": nullItem, "edits": []any{nil}}, "edits[0]: must be an edit object")

	editsWrongType := h.fixture("edits-wrong-type.txt", "alpha\n")
	h.rejectsUnchanged(editsWrongType, map[string]any{"file_path": editsWrongType, "edits": "not-an-array"}, "edits must be an array")

	// 2. LF / BOM / CRLF hash agreement
	variants := []struct {
		label string
		eol   string
		bom   bool
	}{
		{"lf", "\n", false},
		{"lf-bom", "\n", true},
		{"crlf", "\r\n", false},
		{"crlf-bom", "\r\n", true},
	}
	for _, variant := range variants {
		t.Run("matrix "+variant.label, func(t *testing.T) {
			prefix := ""
			if variant.bom {
				prefix = "\uFEFF"
			}
			filePath := h.fixture("matrix-"+variant.label+".txt", prefix+"alpha"+variant.eol+"beta"+variant.eol)
			expectedHash := compression.HashContent([]byte(h.read(filePath)))
			h.mustSucceed(h.run(map[string]any{
				"file_path": filePath, "start_line": 1, "end_line": 3, "content": "ALPHA\nBETA\n",
				"expected_hash": expectedHash,
			}), variant.label+" hash edit")
			if got, want := h.read(filePath), prefix+"ALPHA"+variant.eol+"BETA"+variant.eol; got != want {
				t.Fatalf("content = %q, want %q", got, want)
			}
		})
	}

	// 3. pure-string batch replace_all and overlap
	replaceAllFile := h.fixture("replace-all.txt", "foo foo\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": replaceAllFile,
		"edits":     []any{map[string]any{"old_string": "foo", "new_string": "bar", "replace_all": true}},
	}), "replace_all")
	if got := h.read(replaceAllFile); got != "bar bar\n" {
		t.Fatalf("replace_all content = %q", got)
	}

	overlapPureFile := h.fixture("overlap-pure.txt", "foo bar baz\n")
	h.rejectsUnchanged(overlapPureFile, map[string]any{
		"file_path": overlapPureFile,
		"edits": []any{
			map[string]any{"old_string": "foo bar", "new_string": "x"},
			map[string]any{"old_string": "bar baz", "new_string": "y"},
		},
	}, "overlap")

	// 4. multi-file batch validates/loads/computes before writes
	atomicA := h.fixture("atomic-a.txt", "alpha\n")
	atomicB := filepath.Join(h.dir, "atomic-b-missing.txt")
	result := h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": atomicA, "old_string": "alpha", "new_string": "ALPHA"},
			map[string]any{"file_path": atomicB, "old_string": "beta", "new_string": "BETA"},
		},
	})
	if !result.IsError || !strings.Contains(resultText(result), "Cannot read file") {
		t.Fatalf("missing second file = %q", resultText(result))
	}
	if got := h.read(atomicA); got != "alpha\n" {
		t.Fatalf("atomic A changed = %q", got)
	}

	// 5. single-edit success/delete and batch empty-string delete
	singleFile := h.fixture("single-success.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{"file_path": singleFile, "old_string": "beta", "new_string": "BETA"}), "single success")
	if got := h.read(singleFile); got != "alpha\nBETA\n" {
		t.Fatalf("single content = %q", got)
	}

	singleDeleteFile := h.fixture("single-range-delete.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{"file_path": singleDeleteFile, "start_line": 1, "end_line": 1, "delete": true}), "single delete")
	if got := h.read(singleDeleteFile); got != "beta\n" {
		t.Fatalf("single delete content = %q", got)
	}

	batchDeleteFile := h.fixture("batch-empty-delete.txt", "alpha\nbeta\n")
	h.mustSucceed(h.run(map[string]any{
		"file_path": batchDeleteFile,
		"edits": []any{
			map[string]any{"old_string": "alpha\n", "new_string": ""},
			map[string]any{"old_string": "beta\n", "new_string": ""},
		},
	}), "batch empty delete")
	if got := h.read(batchDeleteFile); got != "" {
		t.Fatalf("batch delete content = %q", got)
	}
}

func TestEditBatchWriteFailureLabels(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root user for a read-only write failure")
	}
	h := newHarness(t)
	first := h.fixture("rel-wf-first.txt", "first\n")
	blocked := h.fixture("rel-wf-blocked.txt", "second\n")
	if err := os.Chmod(blocked, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(blocked, 0o644)

	result := h.run(map[string]any{
		"edits": []any{
			map[string]any{"file_path": first, "old_string": "first", "new_string": "FIRST"},
			map[string]any{"file_path": blocked, "old_string": "second", "new_string": "SECOND"},
		},
	})
	if !result.IsError {
		t.Fatalf("write failure must be an error")
	}
	message := resultText(result)
	for _, want := range []string{
		"Failed file (may be partially modified): " + blocked,
		"Files written before failure: " + first,
		"Files not attempted: (none)",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
	if got := h.read(first); got != "FIRST\n" {
		t.Fatalf("first content = %q", got)
	}
	if got := h.read(blocked); got != "second\n" {
		t.Fatalf("blocked content = %q", got)
	}
}

func TestEditSchemaJSON(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(Tool.InputSchema, &schema); err != nil {
		t.Fatalf("inputSchema is not JSON: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, key := range []string{
		"file_path", "old_string", "new_string", "replace_all", "start_line", "end_line",
		"content", "delete", "insert_before", "expected_hash", "expected_range_checksum", "edits",
	} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("properties.%s missing", key)
		}
	}
	anyOf, _ := schema["anyOf"].([]any)
	if len(anyOf) != 2 {
		t.Fatalf("anyOf length = %d", len(anyOf))
	}
	edits, _ := properties["edits"].(map[string]any)
	if edits["type"] != "array" || edits["minItems"] != float64(1) {
		t.Fatalf("edits schema = %#v", edits)
	}
	items, _ := edits["items"].(map[string]any)
	itemAnyOf, _ := items["anyOf"].([]any)
	if len(itemAnyOf) != 4 {
		t.Fatalf("edits.items.anyOf length = %d", len(itemAnyOf))
	}
	second, _ := anyOf[1].(map[string]any)
	notBlock, _ := second["not"].(map[string]any)
	notAnyOf, _ := notBlock["anyOf"].([]any)
	if len(notAnyOf) != len(editOperationKeys) {
		t.Fatalf("top-level not.anyOf length = %d", len(notAnyOf))
	}
	if name := Tool.Name; name != "cave__edit" {
		t.Fatalf("name = %q", name)
	}
}

func TestFindClosestLineHintUTF16(t *testing.T) {
	// The preview keeps TS's UTF-16 100/97 boundaries.
	line := strings.Repeat("a", 98) + "😀" + "zzzz"
	content := "prefix\n" + line + "\n"
	got := findClosestLineHint(content, "aaaa")
	if !strings.Contains(got, "Closest match at line 2") {
		t.Fatalf("hint = %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("hint preview missing ellipsis: %q", got)
	}
	if strings.Contains(got, "😀") {
		t.Fatalf("preview must stop before the emoji: %q", got)
	}
}
