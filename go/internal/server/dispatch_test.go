package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	callruntime "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

func resultText(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatalf("result has no content: %#v", result)
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("first content is %T, want text", result.Content[0])
	}
	return text.Text
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Cleanup(func() { callruntime.FlushToolErrorQueue() })
	return dir
}

func TestLoadToolUnknown(t *testing.T) {
	_, err := LoadTool("cave__nope")
	if err == nil || err.Error() != "Unknown tool: cave__nope" {
		t.Fatalf("LoadTool error = %v, want Unknown tool: cave__nope", err)
	}
	tool, err := LoadTool("cave__read")
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name != "cave__read" || tool.Handler == nil {
		t.Fatalf("LoadTool(read) = %#v", tool)
	}
}

func TestSchemaPropertyNames(t *testing.T) {
	tool, err := LoadTool("cave__read")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file_path", "offset", "limit", "mode", "line_numbers", "force"}
	if got := schemaPropertyNames(tool.InputSchema); !reflect.DeepEqual(got, want) {
		t.Fatalf("schema props = %v, want %v", got, want)
	}
	if got := schemaPropertyNames([]byte(`{"type":"object"}`)); got != nil {
		t.Fatalf("schema without properties = %v", got)
	}
	if got := schemaPropertyNames([]byte(`{"type":"object","properties":null}`)); got != nil {
		t.Fatalf("null properties = %v", got)
	}
}

func TestNormalizePaths(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "tmp", "w4i-cwd")
	args := map[string]any{
		"file_path": "one.txt",
		"edits": []any{
			map[string]any{"file_path": "two.txt", "old_string": "a"},
			map[string]any{"old_string": "b"},
			"not-an-object",
			nil,
		},
	}
	got := normalizePaths("cave__edit", args, cwd)
	if got["file_path"] != filepath.Join(cwd, "one.txt") {
		t.Fatalf("top-level file_path = %v", got["file_path"])
	}
	edits := got["edits"].([]any)
	if edits[0].(map[string]any)["file_path"] != filepath.Join(cwd, "two.txt") {
		t.Fatalf("edit file_path = %v", edits[0])
	}
	if _, has := edits[1].(map[string]any)["file_path"]; has {
		t.Fatalf("edit without file_path gained one: %v", edits[1])
	}
	if edits[2] != "not-an-object" || edits[3] != nil {
		t.Fatalf("non-object edits changed: %v", edits)
	}
	if args["file_path"] != "one.txt" {
		t.Fatalf("caller args mutated: %v", args)
	}
	original := args["edits"].([]any)[0].(map[string]any)
	if original["file_path"] != "two.txt" {
		t.Fatalf("caller nested edit mutated: %v", original)
	}

	invalidate := map[string]any{"file_paths": []any{"a.txt", "", nil, "/abs/b.txt"}}
	gotInv := normalizePaths("cave__invalidate", invalidate, cwd)
	paths := gotInv["file_paths"].([]any)
	if paths[0] != filepath.Join(cwd, "a.txt") || paths[1] != "" || paths[2] != nil || paths[3] != "/abs/b.txt" {
		t.Fatalf("invalidate paths = %v", paths)
	}
	if invalidate["file_paths"].([]any)[0] != "a.txt" {
		t.Fatal("caller invalidate args mutated")
	}
}

func TestCallToolUnknownObservedWithoutRawName(t *testing.T) {
	tempDir(t)
	callruntime.ResetToolErrorStateForTests()
	secret := "cave__sk-ant-fake0000111122223333"
	_, err := CallTool(context.Background(), secret, map[string]any{}, &callruntime.CallContext{
		SessionID: "session",
		Env:       map[string]string{"CAVE_TOOLS_MODE": "hint"},
	})
	if err == nil || err.Error() != "Unknown tool: "+secret {
		t.Fatalf("CallTool error = %v", err)
	}
	callruntime.FlushToolErrorQueue()
	rows := callruntime.ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %#v", len(rows), rows)
	}
	if rows[0].Tool != "unknown_tool" || rows[0].Outcome != callruntime.ToolErrorThrown {
		t.Fatalf("row = %#v", rows[0])
	}
	dump := ""
	for _, row := range rows {
		dump += fmt.Sprintf("%v", row)
	}
	if strings.Contains(dump, secret) {
		t.Fatalf("raw unknown tool name persisted: %s", dump)
	}
}

func TestCallToolUnknownHonorsCallerDisable(t *testing.T) {
	tempDir(t)
	callruntime.ResetToolErrorStateForTests()
	_, err := CallTool(context.Background(), "cave__nope", nil, &callruntime.CallContext{
		SessionID: "session",
		Env:       map[string]string{"CAVE_TOOLS_ERROR_LOG": "0"},
	})
	if err == nil {
		t.Fatal("expected unknown-tool error")
	}
	callruntime.FlushToolErrorQueue()
	if rows := callruntime.ListToolErrors(10); len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}

func TestCallToolMissingAndNullArgs(t *testing.T) {
	dir := tempDir(t)
	tool, err := LoadTool("cave__read")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{nil, {"file_path": nil}} {
		result, callErr := CallTool(context.Background(), "cave__read", args, &callruntime.CallContext{SessionID: t.Name()})
		if callErr != nil {
			t.Fatalf("CallTool(%v): %v", args, callErr)
		}
		direct, directErr := tool.Handler(context.Background(), map[string]any{"__sessionId": t.Name(), "file_path": nil})
		if directErr != nil {
			t.Fatal(directErr)
		}
		if !result.IsError || resultText(t, result) != resultText(t, direct) {
			t.Fatalf("dispatch %q != direct %q", resultText(t, result), resultText(t, direct))
		}
	}

	// cave__ls has no session injection: TS reads args.path on undefined and
	// throws, so dispatch reproduces the TypeError instead of listing the cwd.
	os.WriteFile(filepath.Join(dir, "entry.txt"), []byte("x"), 0o600)
	_, err = CallTool(context.Background(), "cave__ls", nil, &callruntime.CallContext{
		SessionID: "s",
		Cwd:       dir,
		Env:       map[string]string{},
	})
	if err == nil || err.Error() != "Cannot read properties of undefined (reading 'path')" {
		t.Fatalf("ls nil args error = %v", err)
	}

	// cave__edit validates nil itself, so it still returns a soft error.
	editResult, err := CallTool(context.Background(), "cave__edit", nil, &callruntime.CallContext{
		SessionID: "s",
		Cwd:       dir,
		Env:       map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !editResult.IsError || resultText(t, editResult) != "Error: edit arguments must be an object" {
		t.Fatalf("edit nil args = %q", resultText(t, editResult))
	}
}

func TestCallToolCwdAndEmptyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "cwd-marker.txt"), []byte("x"), 0o600)
	result, err := CallTool(context.Background(), "cave__ls", map[string]any{"path": "."}, &callruntime.CallContext{
		SessionID: "cwd",
		Cwd:       dir,
		Env:       map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(resultText(t, result), "cwd-marker.txt") {
		t.Fatalf("relative path not resolved against call cwd: %q", resultText(t, result))
	}

	t.Setenv("CAVE_TOOLS_TEST_ENV", "process-value")
	args := map[string]any{
		"command":     `printf '%s' "$CAVE_TOOLS_TEST_ENV"`,
		"description": "env probe",
	}
	empty, err := CallTool(context.Background(), "cave__bash", args, &callruntime.CallContext{
		SessionID: "env-empty",
		Cwd:       dir,
		Env:       map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.IsError || strings.TrimSpace(resultText(t, empty)) != "(no output)" {
		t.Fatalf("empty caller env fell back to process env: %q", resultText(t, empty))
	}
	custom, err := CallTool(context.Background(), "cave__bash", args, &callruntime.CallContext{
		SessionID: "env-custom",
		Cwd:       dir,
		Env:       map[string]string{"CAVE_TOOLS_TEST_ENV": "call-value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if custom.IsError || strings.TrimSpace(resultText(t, custom)) != "call-value" {
		t.Fatalf("caller env not honored: %q", resultText(t, custom))
	}
}

func TestCallToolSessionNamespacing(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := &callruntime.CallContext{Cwd: dir, Env: map[string]string{}}
	first := *base
	first.SessionID = "s"
	first.ClientID = "daemon-conn"

	one, err := CallTool(context.Background(), "cave__read", map[string]any{"file_path": path}, &first)
	if err != nil {
		t.Fatal(err)
	}
	if one.IsError || resultText(t, one) != "one\ntwo\n" {
		t.Fatalf("first read = %#v", one)
	}
	two, err := CallTool(context.Background(), "cave__read", map[string]any{"file_path": path}, &first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, two), "session daemon-conn:s") {
		t.Fatalf("stub missing namespaced session: %q", resultText(t, two))
	}

	other := first
	other.ClientID = "other-client"
	full, err := CallTool(context.Background(), "cave__read", map[string]any{"file_path": path}, &other)
	if err != nil {
		t.Fatal(err)
	}
	if full.IsError || resultText(t, full) != "one\ntwo\n" {
		t.Fatalf("distinct client shared read state: %q", resultText(t, full))
	}

	namespace := first
	namespace.SessionNamespace = "cli-shared"
	nsRead, err := CallTool(context.Background(), "cave__read", map[string]any{"file_path": path}, &namespace)
	if err != nil {
		t.Fatal(err)
	}
	if nsRead.IsError || resultText(t, nsRead) != "one\ntwo\n" {
		t.Fatalf("namespace read = %q", resultText(t, nsRead))
	}
	nsStub, err := CallTool(context.Background(), "cave__read", map[string]any{"file_path": path}, &namespace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, nsStub), "session cli-shared:s") {
		t.Fatalf("namespace stub = %q", resultText(t, nsStub))
	}
}

func TestCallToolDoesNotMutateArgs(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("alpha\n"), 0o600)
	args := map[string]any{
		"file_path": "a.txt",
		"edits": []any{
			map[string]any{"file_path": "a.txt", "start_line": 1, "end_line": 1, "content": "beta"},
			map[string]any{"file_path": "missing.txt", "old_string": "x", "new_string": "y"},
		},
	}
	before := map[string]any{
		"file_path": "a.txt",
		"edits": []any{
			map[string]any{"file_path": "a.txt", "start_line": 1, "end_line": 1, "content": "beta"},
			map[string]any{"file_path": "missing.txt", "old_string": "x", "new_string": "y"},
		},
	}
	_, err := CallTool(context.Background(), "cave__edit", args, &callruntime.CallContext{
		SessionID: "nomutate",
		Cwd:       dir,
		Env:       map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, before) {
		t.Fatalf("caller args mutated:\n got %#v\nwant %#v", args, before)
	}
}

func TestCallToolUnknownDoesNotMutateArgs(t *testing.T) {
	tempDir(t)
	args := map[string]any{"path": "relative"}
	before := map[string]any{"path": "relative"}
	_, _ = CallTool(context.Background(), "cave__nope", args, &callruntime.CallContext{SessionID: "s"})
	if !reflect.DeepEqual(args, before) {
		t.Fatalf("unknown call mutated args: %#v", args)
	}
}
