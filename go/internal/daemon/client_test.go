package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// fakeDaemon is a minimal NDJSON daemon used to drive the Go client without
// the real server.
type fakeDaemon struct {
	listener net.Listener
	mu       sync.Mutex
	conns    int
	requests []map[string]any
	handler  func(conn net.Conn, req map[string]any) bool
}

func startNDJSONFakeDaemon(t *testing.T, handler func(conn net.Conn, req map[string]any) bool) (*fakeDaemon, string) {
	t.Helper()
	isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return serveNDJSON(t, handler)
}

// serveNDJSON starts the fake at the current environment's socket path.
func serveNDJSON(t *testing.T, handler func(conn net.Conn, req map[string]any) bool) (*fakeDaemon, string) {
	t.Helper()
	if _, err := EnsureSocketDir(); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fake := &fakeDaemon{listener: listener, handler: handler}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fake.mu.Lock()
			fake.conns++
			fake.mu.Unlock()
			go fake.serve(conn)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return fake, path
}

func (f *fakeDaemon) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal([]byte(line), &request) != nil {
			continue
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.mu.Unlock()
		if !f.handler(conn, request) {
			return
		}
	}
}

func (f *fakeDaemon) connectionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeDaemon) requestCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if request["method"] == method {
			count++
		}
	}
	return count
}

func writeFakeReply(conn net.Conn, request map[string]any, payload string) {
	id, _ := json.Marshal(request["id"])
	fmt.Fprintf(conn, `{"id":%s,%s}`+"\n", id, payload)
}

// TestClientConcurrentEnsureHandshake proves concurrent first calls share one
// connection and one hello, like TS's single `connecting` promise.
func TestClientConcurrentEnsureHandshake(t *testing.T) {
	fake, _ := startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			time.Sleep(50 * time.Millisecond)
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "list":
			writeFakeReply(conn, request, `"result":{"tools":[{"name":"cave__fixture_echo","inputSchema":{"type":"object"}}]}`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools, err := client.List(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if len(tools) != 1 || tools[0].Name != "cave__fixture_echo" {
				errs <- fmt.Errorf("tools = %#v", tools)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("List: %v", err)
	}
	if got := fake.connectionCount(); got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
	if got := fake.requestCount("hello"); got != 1 {
		t.Fatalf("hello requests = %d, want 1", got)
	}
	if got := fake.requestCount("list"); got != 2 {
		t.Fatalf("list requests = %d, want 2", got)
	}
}

// TestClientDecodesMediaAndMeta covers text/image/resource + _meta survival.
func TestClientDecodesMediaAndMeta(t *testing.T) {
	startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "call":
			writeFakeReply(conn, request, `"result":{"content":[{"type":"text","text":"hello"},{"type":"image","data":"aGk=","mimeType":"image/png"},{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf","blob":"aGk="}}],"_meta":{"caveCli":{"exitCode":0}}}`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()
	outcome, err := client.Call(context.Background(), "cave__fixture_echo", nil, &runtime.CallContext{
		SessionID: "s",
		Cwd:       "/tmp",
		Env:       map[string]string{},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !outcome.Sent || outcome.Result.IsError {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(outcome.Result.Content) != 3 {
		t.Fatalf("content = %#v", outcome.Result.Content)
	}
	if text, ok := outcome.Result.Content[0].(types.TextContent); !ok || text.Text != "hello" {
		t.Fatalf("content[0] = %#v", outcome.Result.Content[0])
	}
	if image, ok := outcome.Result.Content[1].(types.ImageContent); !ok || image.MimeType != "image/png" {
		t.Fatalf("content[1] = %#v", outcome.Result.Content[1])
	}
	if resource, ok := outcome.Result.Content[2].(types.ResourceContent); !ok || resource.Resource.URI != "file:///x" {
		t.Fatalf("content[2] = %#v", outcome.Result.Content[2])
	}
	if outcome.Result.Meta["caveCli"] == nil {
		t.Fatalf("_meta lost: %#v", outcome.Result.Meta)
	}
}

// TestClientErrorReply covers daemon error replies becoming tool errors.
func TestClientErrorReply(t *testing.T) {
	startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "call":
			writeFakeReply(conn, request, `"error":"boom"`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()
	outcome, err := client.Call(context.Background(), "cave__fixture_error", nil, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !outcome.Sent || !outcome.Result.IsError {
		t.Fatalf("outcome = %#v", outcome)
	}
	text, ok := outcome.Result.Content[0].(types.TextContent)
	if !ok || text.Text != "boom" {
		t.Fatalf("error text = %#v", outcome.Result.Content)
	}
}

// TestClientNoResendAfterDisconnect covers the side-effect rule: a request
// written before the connection dropped is never retried automatically.
func TestClientNoResendAfterDisconnect(t *testing.T) {
	var calls atomic.Int32
	fake, _ := startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "call":
			if calls.Add(1) == 1 {
				// Drop the connection after receiving the call.
				return false
			}
			writeFakeReply(conn, request, `"result":{"content":[{"type":"text","text":"ok-after-reconnect"}]}`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()
	first, err := client.Call(context.Background(), "cave__bash", map[string]any{"command": "echo once"}, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if !first.Sent || !first.Result.IsError {
		t.Fatalf("first outcome = %#v", first)
	}
	text, _ := first.Result.Content[0].(types.TextContent)
	if text.Text != ConnectionLostText {
		t.Fatalf("lost text = %q", text.Text)
	}
	if got := fake.requestCount("call"); got != 1 {
		t.Fatalf("call requests after disconnect = %d, want 1 (no resend)", got)
	}

	second, err := client.Call(context.Background(), "cave__bash", map[string]any{"command": "echo twice"}, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if !second.Sent || second.Result.IsError {
		t.Fatalf("second outcome = %#v", second)
	}
	secondText, _ := second.Result.Content[0].(types.TextContent)
	if secondText.Text != "ok-after-reconnect" {
		t.Fatalf("second text = %q", secondText.Text)
	}
	if got := fake.requestCount("call"); got != 2 {
		t.Fatalf("call requests = %d, want 2", got)
	}
}

// TestClientUnsupportedContentStaysSent covers the post-send invariant: a
// reply the Go types cannot model is still Sent=true, so callers never re-run
// a side-effecting tool.
func TestClientUnsupportedContentStaysSent(t *testing.T) {
	startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "call":
			writeFakeReply(conn, request, `"result":{"content":[{"type":"audio","data":"aGk="}]}`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()
	outcome, err := client.Call(context.Background(), "cave__fixture_echo", nil, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !outcome.Sent || !outcome.Result.IsError {
		t.Fatalf("outcome = %#v, want sent tool error", outcome)
	}
	text, _ := outcome.Result.Content[0].(types.TextContent)
	if text.Text != ConnectionLostText {
		t.Fatalf("text = %q, want %q", text.Text, ConnectionLostText)
	}
}

// TestClientListFallback covers nil when list cannot be decoded or errors.
func TestClientListFallback(t *testing.T) {
	startNDJSONFakeDaemon(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "list":
			writeFakeReply(conn, request, `"result":{"tools":"nope"}`)
		}
		return true
	})
	client := NewDaemonClient()
	defer client.Close()
	tools, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if tools != nil {
		t.Fatalf("List = %#v, want nil", tools)
	}
}

// TestClientListTimeout covers the hello/list 2s timeout: a daemon that
// accepts but never answers is treated as unavailable, not an error.
func TestClientListTimeout(t *testing.T) {
	fake, _ := startNDJSONFakeDaemon(t, func(net.Conn, map[string]any) bool { return true })
	client := NewDaemonClient()
	defer client.Close()
	started := time.Now()
	tools, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if tools != nil {
		t.Fatalf("List = %#v, want nil", tools)
	}
	if elapsed := time.Since(started); elapsed < helloTimeoutMS*time.Millisecond {
		t.Fatalf("List returned after %s, want >= %dms", elapsed, helloTimeoutMS)
	}
	if got := fake.connectionCount(); got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
}

// TestClientRetryBackoff covers the 30s in-process fallback: after a failed
// connect the client does not retry immediately, even once the socket exists.
func TestClientRetryBackoff(t *testing.T) {
	isolate(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocked: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", blocked)
	client := NewDaemonClient()
	defer client.Close()
	first, err := client.Call(context.Background(), "cave__fixture_echo", nil, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil || first.Sent {
		t.Fatalf("first Call = %#v, %v; want unsent", first, err)
	}

	// A working daemon appears, but the backoff window still applies.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	fake, _ := serveNDJSON(t, func(conn net.Conn, request map[string]any) bool {
		switch request["method"] {
		case "hello":
			writeFakeReply(conn, request, `"result":{"buildId":"test","pid":1}`)
		case "call":
			writeFakeReply(conn, request, `"result":{"content":[{"type":"text","text":"after-backoff"}]}`)
		}
		return true
	})
	second, err := client.Call(context.Background(), "cave__fixture_echo", nil, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil || second.Sent {
		t.Fatalf("backoff Call = %#v, %v; want unsent", second, err)
	}
	if got := fake.connectionCount(); got != 0 {
		t.Fatalf("backoff dialed %d times, want 0", got)
	}

	// A fresh client sees the daemon immediately.
	fresh := NewDaemonClient()
	defer fresh.Close()
	third, err := fresh.Call(context.Background(), "cave__fixture_echo", nil, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil || !third.Sent {
		t.Fatalf("fresh Call = %#v, %v; want sent", third, err)
	}
}

// TestClientUnavailableSocketDir covers the pre-connect safety check: an
// unsafe socket dir means nothing is sent and nothing is spawned.
func TestClientUnavailableSocketDir(t *testing.T) {
	isolate(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocked: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", blocked)
	client := NewDaemonClient()
	defer client.Close()
	started := time.Now()
	outcome, err := client.Call(context.Background(), "cave__bash", map[string]any{"command": "echo x"}, &runtime.CallContext{SessionID: "s", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if outcome.Sent {
		t.Fatalf("Call sent with an unsafe socket dir: %#v", outcome)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("unsafe socket dir took %s, want an immediate fallback", elapsed)
	}
	tools, err := client.List(context.Background())
	if err != nil || tools != nil {
		t.Fatalf("List = %#v, %v; want nil, nil", tools, err)
	}
}

// TestClientToTSDaemon is the Go-client -> TypeScript-daemon differential:
// same dist-derived key, socket path and NDJSON protocol, real TS dispatch.
func TestClientToTSDaemon(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("unix-socket interop test is not available on Windows")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dist, err := tsDistDir()
	if err != nil {
		t.Skipf("dist/ not built: %v (run `pnpm build` first)", err)
	}
	isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CAVE_TOOLS_DAEMON_IDLE_MS", "60000")
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}

	var output strings.Builder
	var outputMu sync.Mutex
	daemon := exec.Command(node, filepath.Join(dist, "cli.js"), "daemon", "start")
	daemon.Dir = t.TempDir()
	daemon.Env = testenv.Environ()
	daemon.Stdout = &lockedWriter{mu: &outputMu, out: &output}
	daemon.Stderr = &lockedWriter{mu: &outputMu, out: &output}
	if err := daemon.Start(); err != nil {
		t.Fatalf("start TS daemon: %v", err)
	}
	t.Cleanup(func() {
		stop := exec.Command(node, filepath.Join(dist, "cli.js"), "daemon", "stop")
		stop.Env = testenv.Environ()
		_ = stop.Run()
		waitCommand(t, daemon)
		if t.Failed() {
			outputMu.Lock()
			t.Logf("TS daemon output:\n%s", output.String())
			outputMu.Unlock()
		}
	})
	waitSocket(t, path, true)

	client := NewDaemonClient()
	defer client.Close()
	workDir := t.TempDir()
	outcome, err := client.Call(context.Background(), "cave__bash", map[string]any{
		"command":     "echo go-client-to-ts",
		"description": "interop",
	}, &runtime.CallContext{
		SessionID: "go-client",
		Cwd:       workDir,
		Env:       testenv.ScrubMap(runtime.EnvFromEnviron(os.Environ())),
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !outcome.Sent {
		t.Fatalf("call not sent; daemon output:\n%s", output.String())
	}
	if outcome.Result.IsError {
		t.Fatalf("TS daemon error: %#v", outcome.Result.Content)
	}
	text, ok := outcome.Result.Content[0].(types.TextContent)
	if !ok || !strings.Contains(text.Text, "go-client-to-ts") {
		t.Fatalf("bash output = %#v", outcome.Result.Content)
	}

	tools, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tools) < 10 {
		t.Fatalf("TS daemon listed %d tools", len(tools))
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"cave__bash", "cave__read", "cave__edit"} {
		if !names[want] {
			t.Fatalf("TS daemon list missing %s", want)
		}
	}
}

// TestTSClientToGoDaemon is the TypeScript-client -> Go-daemon differential:
// TS computes the key, connects and drives the injected Go callback.
func TestTSClientToGoDaemon(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("unix-socket interop test is not available on Windows")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dist, err := tsDistDir()
	if err != nil {
		t.Skipf("dist/ not built: %v (run `pnpm build` first)", err)
	}
	isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CAVE_TOOLS_DAEMON_IDLE_MS", "60000")
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}

	var output strings.Builder
	var outputMu sync.Mutex
	daemon := exec.Command(os.Args[0], "daemon", "start")
	daemon.Stdout = &lockedWriter{mu: &outputMu, out: &output}
	daemon.Stderr = &lockedWriter{mu: &outputMu, out: &output}
	if err := daemon.Start(); err != nil {
		t.Fatalf("start Go daemon: %v", err)
	}
	t.Cleanup(func() {
		shutdownDaemon(t, path)
		waitCommand(t, daemon)
		if t.Failed() {
			outputMu.Lock()
			t.Logf("Go daemon output:\n%s", output.String())
			outputMu.Unlock()
		}
	})
	waitSocket(t, path, true)

	workDir := t.TempDir()
	script := `
const { pathToFileURL } = await import("node:url");
const { DaemonClient } = await import(pathToFileURL(process.env.CT_DAEMON_CLIENT).href);
const client = new DaemonClient();
const outcome = await client.call("cave__fixture_echo", { text: "ts-to-go", media: true, meta: true }, {
  sessionId: "ts-client",
  cwd: process.env.CT_CWD,
  env: { CT_MARK: "ts" },
});
const tools = await client.list();
console.log(JSON.stringify({ outcome, tools: tools.map((tool) => tool.name) }));
client.close();
`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Env = append(testenv.Environ(),
		"CT_DAEMON_CLIENT="+filepath.Join(dist, "daemon-client.js"),
		"CT_CWD="+workDir,
	)
	raw, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("TS client failed: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("TS client: %v", err)
	}
	var decoded struct {
		Outcome struct {
			Sent   bool `json:"sent"`
			Result struct {
				Content []map[string]any `json:"content"`
				IsError bool             `json:"isError"`
				Meta    map[string]any   `json:"_meta"`
			} `json:"result"`
		} `json:"outcome"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode TS client output %q: %v", raw, err)
	}
	if !decoded.Outcome.Sent {
		t.Fatalf("TS client outcome not sent: %s", raw)
	}
	if decoded.Outcome.Result.IsError {
		t.Fatalf("TS client saw a tool error: %s", raw)
	}
	content := decoded.Outcome.Result.Content
	if len(content) != 3 {
		t.Fatalf("content blocks = %d: %s", len(content), raw)
	}
	text, _ := content[0]["text"].(string)
	for _, want := range []string{"echo:ts-to-go", "cwd=" + workDir, "mark=ts", "session=ts-client"} {
		if !strings.Contains(text, want) {
			t.Fatalf("echo text %q missing %q", text, want)
		}
	}
	if content[1]["type"] != "image" || content[1]["mimeType"] != "image/png" {
		t.Fatalf("image block = %#v", content[1])
	}
	if content[2]["type"] != "resource" {
		t.Fatalf("resource block = %#v", content[2])
	}
	fixture, _ := decoded.Outcome.Result.Meta["fixture"].(map[string]any)
	if fixture["ok"] != true {
		t.Fatalf("_meta = %#v", decoded.Outcome.Result.Meta)
	}
	wantNames := make([]string, 0, len(fixtureTools()))
	for _, tool := range fixtureTools() {
		wantNames = append(wantNames, tool.Name)
	}
	if strings.Join(decoded.Tools, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("tools = %v, want %v", decoded.Tools, wantNames)
	}
}

// lockedWriter serialises subprocess diagnostics.
type lockedWriter struct {
	mu  *sync.Mutex
	out *strings.Builder
}

func (w *lockedWriter) Write(buf []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(buf)
}

// waitCommand waits for a subprocess, killing it rather than hanging a test.
func waitCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		t.Errorf("subprocess %v did not exit", cmd.Args)
	}
}
