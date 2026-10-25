package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/statusline"
)

type mcpPipe struct {
	in      *io.PipeWriter
	out     *bufio.Reader
	stderr  *bytes.Buffer
	done    chan error
	cancel  context.CancelFunc
	outPipe *io.PipeWriter
	stopped bool
	lastRaw []byte
}

func startMCPPipe(t *testing.T) *mcpPipe {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	stderr := &bytes.Buffer{}
	done := make(chan error, 1)
	go func() {
		done <- StartServer(ctx, "0.6.1", stdinReader, stdoutWriter, stderr)
	}()
	pipe := &mcpPipe{
		in:      stdinWriter,
		out:     bufio.NewReader(stdoutReader),
		stderr:  stderr,
		done:    done,
		cancel:  cancel,
		outPipe: stdoutWriter,
	}
	t.Cleanup(func() {
		_ = stdinWriter.Close()
		cancel()
		if !pipe.stopped {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
		_ = stdoutWriter.Close()
		_ = stdoutReader.Close()
	})
	return pipe
}

func (p *mcpPipe) write(t *testing.T, message map[string]any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.in.Write(append(data, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (p *mcpPipe) read(t *testing.T) map[string]any {
	t.Helper()
	line, err := p.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v (stderr %q)", err, p.stderr.String())
	}
	p.lastRaw = append(p.lastRaw[:0], line...)
	var message map[string]any
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatalf("decode line %q: %v", line, err)
	}
	return message
}

func (p *mcpPipe) initialize(t *testing.T) map[string]any {
	t.Helper()
	p.write(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "w4i", "version": "0"},
		},
	})
	response := p.read(t)
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize response = %#v", response)
	}
	p.write(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return result
}

func (p *mcpPipe) call(t *testing.T, id int, method string, params map[string]any) map[string]any {
	t.Helper()
	p.write(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return p.read(t)
}

// waitForSuffix polls for the statusline badge, returning its content and
// whether it appeared before the deadline.
func waitForSuffix(t *testing.T, path string, timeout time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if content, err := os.ReadFile(path); err == nil {
			return string(content), true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMCPCallWritesStatuslineSuffix proves a tools/call refreshes the badge
// in-process (no `status --emit-statusline` child) and that the bytes match
// what the status command would write for the same state.
func TestMCPCallWritesStatuslineSuffix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	t.Setenv("CAVE_TOOLS_ASCII", "1")

	pipe := startMCPPipe(t)
	claudeDir := filepath.Join(dir, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	pipe.initialize(t)

	source := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(source, []byte("statusline payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := pipe.call(t, 2, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": source},
	})
	if response["result"] == nil {
		t.Fatalf("tool call failed: %#v", response)
	}

	path := filepath.Join(claudeDir, ".cave-tools-statusline-suffix")
	content, ok := waitForSuffix(t, path, 3*time.Second)
	if !ok {
		t.Fatalf("statusline suffix %s was not written", path)
	}
	if content == "" {
		t.Fatal("statusline suffix is empty")
	}
	if !strings.HasPrefix(content, "v") || !strings.Contains(content, "|") {
		t.Fatalf("suffix = %q, want ASCII badge", content)
	}
	if want := statusline.BuildSuffix(); content != want {
		t.Fatalf("suffix = %q, want %q (status --emit-statusline content)", content, want)
	}
}

// TestMCPCallStatuslineWriteFailureIsSilent proves an unwritable config dir
// cannot change the tool result or crash the server.
func TestMCPCallStatuslineWriteFailureIsSilent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")

	pipe := startMCPPipe(t)
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// mkdirAll(config dir) now fails: the badge write must stay silent.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(blocker, "claude"))
	pipe.initialize(t)

	source := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(source, []byte("still works\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := pipe.call(t, 2, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": source},
	})
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("tool call returned no result: %#v", response)
	}
	if result["isError"] == true {
		t.Fatalf("tool result marked as error: %#v", result)
	}
	// Let the debounce fire; a panic or failing handler would break later calls.
	time.Sleep(statusline.EmitDebounce + 100*time.Millisecond)
	second := pipe.call(t, 3, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": source},
	})
	if second["result"] == nil {
		t.Fatalf("second tool call failed: %#v", second)
	}
}

func firstText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result content = %#v", result["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok || block["type"] != "text" {
		t.Fatalf("first block = %#v", content[0])
	}
	text, _ := block["text"].(string)
	return text
}

func TestMCPPipeInitializeListCallError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)

	init := pipe.initialize(t)
	if init["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", init["protocolVersion"])
	}
	serverInfo := init["serverInfo"].(map[string]any)
	if serverInfo["name"] != "cave-tools" || serverInfo["version"] != "0.6.1" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}
	capabilities := init["capabilities"].(map[string]any)
	toolsCap, ok := capabilities["tools"].(map[string]any)
	if !ok || len(toolsCap) != 0 {
		t.Fatalf("capabilities.tools = %#v", capabilities["tools"])
	}

	list := pipe.call(t, 2, "tools/list", map[string]any{})
	tools := list["result"].(map[string]any)["tools"].([]any)
	names := make([]string, len(tools))
	for i, raw := range tools {
		names[i] = raw.(map[string]any)["name"].(string)
	}
	if !reflect.DeepEqual(names, ToolNames) {
		t.Fatalf("MCP tool order = %v", names)
	}

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello wire\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := pipe.call(t, 3, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": path},
		"_meta":     map[string]any{"sessionID": "s1"},
	})
	if got := firstText(t, call["result"].(map[string]any)); got != "hello wire\n" {
		t.Fatalf("read text = %q", got)
	}

	unknown := pipe.call(t, 4, "tools/call", map[string]any{
		"name":      "cave__nope",
		"arguments": map[string]any{},
	})
	errObj, ok := unknown["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown tool response = %#v", unknown)
	}
	if errObj["code"] != float64(-32603) || errObj["message"] != "Unknown tool: cave__nope" {
		t.Fatalf("unknown tool error = %#v", errObj)
	}

	missing := pipe.call(t, 5, "tools/call", map[string]any{"name": "cave__ls", "_meta": map[string]any{"sessionID": nil}})
	errObj, ok = missing["error"].(map[string]any)
	if !ok || errObj["message"] != "Cannot read properties of undefined (reading 'path')" {
		t.Fatalf("missing args error = %#v", missing)
	}

	pipe.write(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_ = pipe.in.Close()
	select {
	case err := <-pipe.done:
		pipe.stopped = true
		if err != nil {
			t.Fatalf("StartServer returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartServer did not stop on stdin EOF")
	}
	if got := strings.TrimSpace(pipe.stderr.String()); got != bootLine {
		t.Fatalf("stderr = %q, want %q", got, bootLine)
	}
}

func TestMCPPipeMediaPreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)
	pipe.initialize(t)

	imageBytes := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("w4i-image")...)
	imagePath := filepath.Join(dir, "pixel.png")
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	imageCall := pipe.call(t, 2, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": imagePath},
		"_meta":     map[string]any{"sessionID": "image"},
	})
	imageContent := imageCall["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if imageContent["type"] != "image" || imageContent["mimeType"] != "image/png" {
		t.Fatalf("image block = %#v", imageContent)
	}
	if imageContent["data"] != base64.StdEncoding.EncodeToString(imageBytes) {
		t.Fatalf("image data was re-encoded: %#v", imageContent["data"])
	}

	pdfBytes := []byte("%PDF-1.4\nw4i pdf\n")
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pdfCall := pipe.call(t, 3, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": pdfPath},
		"_meta":     map[string]any{"sessionID": "pdf"},
	})
	resource := pdfCall["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	payload := resource["resource"].(map[string]any)
	if resource["type"] != "resource" || payload["mimeType"] != "application/pdf" {
		t.Fatalf("resource block = %#v", resource)
	}
	if payload["blob"] != base64.StdEncoding.EncodeToString(pdfBytes) {
		t.Fatalf("pdf blob was re-encoded: %#v", payload["blob"])
	}
}

func TestMCPPipeProfileAndSessionMeta(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)
	pipe.initialize(t)

	path := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := pipe.call(t, 2, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": path},
		"_meta":     map[string]any{"sessionID": float64(123)},
	})
	if got := firstText(t, first["result"].(map[string]any)); got != "one\n" {
		t.Fatalf("first read = %q", got)
	}
	second := pipe.call(t, 3, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": path},
		"_meta":     map[string]any{"sessionID": "123"},
	})
	if got := firstText(t, second["result"].(map[string]any)); !strings.Contains(got, "session 123") {
		t.Fatalf("numeric sessionID not coerced to 123: %q", got)
	}
	nullish := pipe.call(t, 4, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": path},
		"_meta":     map[string]any{"sessionID": nil},
	})
	if got := firstText(t, nullish["result"].(map[string]any)); got != "one\n" {
		t.Fatalf("null sessionID should be a fresh default session: %q", got)
	}

	t.Setenv("CAVE_TOOLS_PROFILE", "standard")
	standard := pipe.call(t, 5, "tools/list", map[string]any{})
	standardTools := standard["result"].(map[string]any)["tools"].([]any)
	if len(standardTools) != 14 {
		t.Fatalf("standard profile lists %d tools", len(standardTools))
	}
	for _, raw := range standardTools {
		name := raw.(map[string]any)["name"]
		if name == "cave__configure" || name == "cave__compress" || name == "cave__status" {
			t.Fatalf("standard profile lists hidden %v", name)
		}
	}
}

func TestMCPPipeNoHTMLEscaping(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)
	pipe.initialize(t)

	path := filepath.Join(dir, "html.txt")
	const body = "a <tag> & b\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	response := pipe.call(t, 2, "tools/call", map[string]any{
		"name":      "cave__read",
		"arguments": map[string]any{"file_path": path},
		"_meta":     map[string]any{"sessionID": "html"},
	})
	result := response["result"].(map[string]any)
	if got := firstText(t, result); got != body {
		t.Fatalf("text = %q", got)
	}
	if bytes.Contains(pipe.lastRaw, []byte(`\u003c`)) || bytes.Contains(pipe.lastRaw, []byte(`\u0026`)) {
		t.Fatalf("wire escaped HTML: %s", pipe.lastRaw)
	}
	if !bytes.Contains(pipe.lastRaw, []byte("a <tag> & b")) {
		t.Fatalf("wire lost literal HTML: %s", pipe.lastRaw)
	}
}

// TestMCPCallArgumentsNumberOverflow pins tools/call arguments containing a
// JSON number outside float64 range to the TypeScript handler: JSON.parse
// yields Infinity there, so the tool runs with Infinity instead of Go's empty
// arguments object. Node-recorded expectations: read offset Infinity reports
// "Offset Infinity is out of range"; read limit Infinity returns the file.
func TestMCPCallArgumentsNumberOverflow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)
	pipe.initialize(t)

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	quoted := strconv.Quote(path)

	pipe.in.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cave__read","arguments":{"file_path":` + quoted + `,"offset":1e400}}}` + "\n"))
	response := pipe.read(t)
	result := response["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("offset overflow result = %#v", result)
	}
	if got := firstText(t, result); got != "Offset Infinity is out of range" {
		t.Fatalf("offset overflow text = %q", got)
	}

	pipe.in.Write([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"cave__read","arguments":{"file_path":` + quoted + `,"force":true,"limit":1e400}}}` + "\n"))
	response = pipe.read(t)
	result = response["result"].(map[string]any)
	if got := firstText(t, result); got != "a\nb\n" {
		t.Fatalf("limit overflow text = %q", got)
	}
}

// TestDecodeCallArgumentsNumberOverflow checks the decode step directly: the
// raw JSON is decoded with JSON.parse's number semantics, and the value is not
// dropped for the rest of the argument object.
func TestDecodeCallArgumentsNumberOverflow(t *testing.T) {
	args, state, err := decodeCallArguments(json.RawMessage(`{"file_path":"/x","limit":1e400,"nested":{"n":-1e400},"list":[1e400]}`))
	if err != nil {
		t.Fatalf("decodeCallArguments: %v", err)
	}
	if state != argsPresent {
		t.Fatalf("state = %v", state)
	}
	if args["file_path"] != "/x" {
		t.Fatalf("file_path = %#v", args["file_path"])
	}
	if limit, _ := args["limit"].(float64); !math.IsInf(limit, 1) {
		t.Fatalf("limit = %#v, want +Inf", args["limit"])
	}
	nested, _ := args["nested"].(map[string]any)
	if value, _ := nested["n"].(float64); !math.IsInf(value, -1) {
		t.Fatalf("nested = %#v", args["nested"])
	}
	list, _ := args["list"].([]any)
	if value, _ := list[0].(float64); !math.IsInf(value, 1) {
		t.Fatalf("list = %#v", args["list"])
	}

	// Normal numbers keep their exact value.
	normal, _, err := decodeCallArguments(json.RawMessage(`{"limit":2,"offset":1.5}`))
	if err != nil {
		t.Fatalf("decodeCallArguments normal: %v", err)
	}
	if normal["limit"] != float64(2) || normal["offset"] != 1.5 {
		t.Fatalf("normal args = %#v", normal)
	}
}
