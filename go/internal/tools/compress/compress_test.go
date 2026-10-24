package compress

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain isolates archives and stats in a temp data dir, like the TS test
// runner does with CAVE_TOOLS_DATA_DIR.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-compress-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testCtx() context.Context {
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{Env: map[string]string{}}, func(ctx context.Context) context.Context {
		return ctx
	})
}

func run(t *testing.T, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(testCtx(), args)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return result
}

func text(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d", len(result.Content))
	}
	block, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return block.Text
}

// Ports the cave__compress assertions in test.mjs (section 2) plus the
// expand/structured branches the CLI exercises.
func TestCompressToolDefinition(t *testing.T) {
	if Tool.Name != "cave__compress" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if got := string(Tool.Annotations); got != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", got)
	}
	const wantDescription = "Compress text to fewer tokens (ANSI strip, line budget, JSON/XML extraction). action='expand' + archive_id retrieves archived output."
	if Tool.Description != wantDescription {
		t.Fatalf("description = %q", Tool.Description)
	}
	assertJSONEqual(t, string(Tool.InputSchema), `{"type":"object","properties":{"text":{"type":"string"},"command_hint":{"type":"string","description":"Command that produced the text"},"structured":{"type":"boolean","description":"JSON/XML extraction","default":true},"tool_name":{"type":"string","description":"Budget to apply (bash, read, grep)","default":"bash"},"action":{"type":"string","enum":["compress","expand"],"default":"compress"},"archive_id":{"type":"string","description":"For action='expand'"}},"required":["text"]}`)
}

// test.mjs asserts every read-only tool advertises readOnlyHint.
func TestReadOnlyHint(t *testing.T) {
	var annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	}
	if err := json.Unmarshal(Tool.Annotations, &annotations); err != nil {
		t.Fatalf("annotations: %v", err)
	}
	if !annotations.ReadOnlyHint {
		t.Fatal("readOnlyHint must be true")
	}
}

func TestCompressText(t *testing.T) {
	const input = "Line 1\n\n\n\nLine 2\n\n\nLine 3"
	result := run(t, map[string]any{"text": input, "tool_name": "bash"})
	if result.IsError {
		t.Fatalf("isError: %s", text(t, result))
	}
	if got := text(t, result); got != input {
		t.Fatalf("text = %q, want %q", got, input)
	}
}

func TestCompressStructuredJSON(t *testing.T) {
	result := run(t, map[string]any{"text": "{\n  \"a\": 1,\n  \"b\": [2, 3]\n}"})
	if got, want := text(t, result), `{"a":1,"b":[2,3]}`; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}

	plain := run(t, map[string]any{"text": "hello"})
	if got := text(t, plain); got != "hello" {
		t.Fatalf("text = %q", got)
	}
}

// structured:false must skip JSON/XML extraction and only apply the budget.
func TestCompressStructuredDisabled(t *testing.T) {
	const input = "{\n  \"a\": 1\n}"
	result := run(t, map[string]any{"text": input, "structured": false})
	if got := text(t, result); got != input {
		t.Fatalf("text = %q, want %q", got, input)
	}
}

// structured is only false when it is exactly the boolean false; any other
// JSON value (including an object) keeps extraction on and must not panic.
func TestCompressStructuredNonBoolean(t *testing.T) {
	result := run(t, map[string]any{"text": "{\n  \"a\": 1\n}", "structured": map[string]any{"x": 1}})
	if got, want := text(t, result), `{"a":1}`; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// A missing text argument follows String(undefined).
func TestCompressMissingText(t *testing.T) {
	result := run(t, map[string]any{})
	if result.IsError {
		t.Fatalf("isError: %s", text(t, result))
	}
	if got := text(t, result); got != "undefined" {
		t.Fatalf("text = %q", got)
	}
}

func TestExpandMissingArchiveID(t *testing.T) {
	result := run(t, map[string]any{"action": "expand"})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	if got := text(t, result); got != "Error: archive_id required" {
		t.Fatalf("text = %q", got)
	}
}

func TestExpandUnknownArchive(t *testing.T) {
	result := run(t, map[string]any{"action": "expand", "archive_id": "nope"})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	if got := text(t, result); got != "Archive not found: nope" {
		t.Fatalf("text = %q", got)
	}
}

func TestExpandRoundTrip(t *testing.T) {
	content := "line one\nline two\n"
	id, _, err := compression.StoreArchive(content, "test")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	result := run(t, map[string]any{"action": "expand", "archive_id": id})
	if result.IsError {
		t.Fatalf("isError: %s", text(t, result))
	}
	if got := text(t, result); got != content {
		t.Fatalf("text = %q, want %q", got, content)
	}

	// An empty action falls back to "compress".
	fallback := run(t, map[string]any{"action": "", "text": "hello"})
	if got := text(t, fallback); got != "hello" {
		t.Fatalf("text = %q", got)
	}
}

// jsString covers the JSON value shapes a handler can receive.
func TestJSString(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"undefined", nil, "undefined"},
		{"string", "x", "x"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"integer", float64(42), "42"},
		{"fraction", 0.5, "0.5"},
		{"small", 1e-7, "1e-7"},
		{"large", 1e21, "1e+21"},
		{"array", []any{float64(1), nil, "x"}, "1,,x"},
		{"object", map[string]any{"a": 1}, "[object Object]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jsString(test.value); got != test.want {
				t.Fatalf("jsString(%v) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("got is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", gotCanonical, wantCanonical)
	}
}
