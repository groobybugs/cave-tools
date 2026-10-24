package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain doubles as the re-exec entry point for the daemon and client
// helper subprocesses: the Go client spawns `<test binary> daemon start`, and
// the spawn/lock test runs `<test binary>` with CAVE_TOOLS_TEST_CLIENT_MODE.
func TestMain(m *testing.M) {
	if hasDaemonStartArgs(os.Args[1:]) {
		os.Exit(runDaemonHelper())
	}
	if mode := os.Getenv(testClientModeEnv); mode != "" {
		os.Exit(runClientHelper(mode))
	}
	os.Exit(m.Run())
}

const (
	testClientModeEnv = "CAVE_TOOLS_TEST_CLIENT_MODE"
	testClientTextEnv = "CAVE_TOOLS_TEST_CLIENT_TEXT"
	testClientCwdEnv  = "CAVE_TOOLS_TEST_CLIENT_CWD"
)

func hasDaemonStartArgs(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "daemon" && args[i+1] == "start" {
			return true
		}
	}
	return false
}

func runDaemonHelper() int {
	if err := StartDaemon(context.Background(), fixtureTools(), fixtureCall); err != nil {
		fmt.Fprintln(os.Stderr, "helper daemon:", err)
		return 1
	}
	return 0
}

func runClientHelper(mode string) int {
	// The daemon this client spawns must not inherit the client helper mode.
	os.Unsetenv(testClientModeEnv)
	client := NewDaemonClient()
	defer client.Close()
	text := os.Getenv(testClientTextEnv)
	cwd := os.Getenv(testClientCwdEnv)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	outcome, err := client.Call(context.Background(), "cave__fixture_echo", map[string]any{"text": text}, &runtime.CallContext{
		SessionID: "spawn-test",
		Cwd:       cwd,
		Env:       runtime.EnvFromEnviron(os.Environ()),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client helper:", err)
		return 3
	}
	if !outcome.Sent {
		fmt.Fprintln(os.Stderr, "client helper: daemon unavailable")
		return 4
	}
	if len(outcome.Result.Content) == 0 {
		fmt.Fprintln(os.Stderr, "client helper: empty result")
		return 5
	}
	textBlock, ok := outcome.Result.Content[0].(types.TextContent)
	if !ok || !strings.Contains(textBlock.Text, "echo:"+text) {
		fmt.Fprintf(os.Stderr, "client helper: unexpected result %#v\n", outcome.Result.Content[0])
		return 6
	}
	return 0
}

// fixtureTools is the daemon tool list used by every hermetic daemon test.
func fixtureTools() []types.ToolDefinition {
	schema := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
	return []types.ToolDefinition{
		{Name: "cave__fixture_echo", Description: "fixture echo", InputSchema: schema},
		{Name: "cave__fixture_slow", Description: "fixture slow", InputSchema: schema},
		{Name: "cave__fixture_error", Description: "fixture error", InputSchema: schema},
		{Name: "cave__fixture_stats", Description: "fixture stats", InputSchema: schema},
	}
}

// fixtureCall exercises the injected callback contract: ctx carries the
// client's cwd/env/session and daemon-only client id/pid/namespace.
func fixtureCall(ctx context.Context, tool string, args map[string]any, call *runtime.CallContext) (types.ToolResult, error) {
	text, _ := args["text"].(string)
	switch tool {
	case "cave__fixture_echo":
		compression.ApplyBudget(ctx, "fixture-raw-text", "fixture")
		pid := "nil"
		if call.ClientPID != nil {
			pid = strconv.Itoa(*call.ClientPID)
		}
		line := fmt.Sprintf("echo:%s|cwd=%s|mark=%s|session=%s|ns=%s|pid=%s|client=%s",
			text, runtime.GetCwd(ctx), runtime.GetEnv(ctx)["CT_MARK"],
			call.SessionID, call.SessionNamespace, pid, call.ClientID)
		blocks := []types.Content{types.Text(line)}
		if args["media"] == true {
			blocks = append(blocks, types.Image("aGk=", "image/png"), types.ResourceBlock("file:///fixture", "application/pdf", "aGk="))
		}
		result := types.ToolResult{Content: blocks}
		if args["meta"] == true {
			result.Meta = map[string]any{"fixture": map[string]any{"ok": true}}
		}
		return result, nil
	case "cave__fixture_slow":
		ms := 0.0
		if value, ok := args["ms"].(float64); ok {
			ms = value
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return types.ToolResult{Content: []types.Content{types.Text("slow:" + text)}}, nil
	case "cave__fixture_error":
		return types.ToolResult{}, errors.New("fixture failure")
	case "cave__fixture_stats":
		compression.ApplyBudget(ctx, strings.Repeat("x", 100), "fixture")
		return types.ToolResult{Content: []types.Content{types.Text("stats")}}, nil
	}
	return types.ToolResult{}, fmt.Errorf("unknown fixture tool %q", tool)
}

// rawClient speaks the daemon protocol directly.
type rawClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialRaw(t *testing.T, path string) *rawClient {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return &rawClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *rawClient) send(t *testing.T, request map[string]any) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	c.sendLine(t, string(raw))
}

func (c *rawClient) sendLine(t *testing.T, line string) {
	t.Helper()
	if _, err := c.conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func (c *rawClient) read(t *testing.T) map[string]any {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		t.Fatalf("decode reply %q: %v", line, err)
	}
	return reply
}

func (c *rawClient) close() { _ = c.conn.Close() }

func replyErrorText(t *testing.T, reply map[string]any) string {
	t.Helper()
	text, ok := reply["error"].(string)
	if !ok {
		t.Fatalf("reply has no error: %#v", reply)
	}
	return text
}

// startFixtureDaemon runs StartDaemon in this process and returns its path.
func startFixtureDaemon(t *testing.T, idleMS int) (string, func()) {
	t.Helper()
	if pathsPlatform == "win32" {
		t.Skip("unix-socket fixture daemon is not available on Windows")
	}
	isolate(t)
	requireDist(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if idleMS > 0 {
		t.Setenv("CAVE_TOOLS_DAEMON_IDLE_MS", strconv.Itoa(idleMS))
	} else {
		t.Setenv("CAVE_TOOLS_DAEMON_IDLE_MS", "600000")
	}
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- StartDaemon(ctx, fixtureTools(), fixtureCall) }()
	waitSocket(t, path, true)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("StartDaemon returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("StartDaemon did not return after cancellation")
			}
		})
	}
	t.Cleanup(stop)
	return path, stop
}

func waitSocket(t *testing.T, path string, present bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Poll the socket file, never probe a live daemon: a probe is itself a
		// connection and would keep resetting the idle timer.
		_, err := os.Lstat(path)
		exists := err == nil
		if present {
			exists = isDaemonAlive(path)
		}
		if exists == present {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s present=%v not reached\nlog:\n%s", path, present, daemonLogText(t))
}

func daemonLogText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(DaemonLogPath())
	if err != nil {
		return ""
	}
	return string(raw)
}

func hello(t *testing.T, client *rawClient, extra map[string]any) map[string]any {
	t.Helper()
	request := map[string]any{"id": 1, "method": "hello"}
	for key, value := range extra {
		request[key] = value
	}
	client.send(t, request)
	return client.read(t)
}

// TestServerHelloListAndShutdown covers the protocol basics: hello build/pid,
// list order, malformed JSON, unknown methods and the shutdown reply.
func TestServerHelloListAndShutdown(t *testing.T) {
	path, stop := startFixtureDaemon(t, 0)
	client := dialRaw(t, path)
	defer client.close()

	build, err := BuildID()
	if err != nil {
		t.Fatalf("BuildID: %v", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat socket: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", got)
	}
	reply := hello(t, client, map[string]any{"clientPid": 4242})
	result, _ := reply["result"].(map[string]any)
	if result["buildId"] != build {
		t.Fatalf("hello buildId = %v, want %s", result["buildId"], build)
	}
	if result["pid"] != float64(os.Getpid()) {
		t.Fatalf("hello pid = %v, want %d", result["pid"], os.Getpid())
	}

	client.send(t, map[string]any{"id": 2, "method": "list"})
	reply = client.read(t)
	listResult, _ := reply["result"].(map[string]any)
	tools, _ := listResult["tools"].([]any)
	if len(tools) != len(fixtureTools()) {
		t.Fatalf("list tools = %d, want %d", len(tools), len(fixtureTools()))
	}
	for i, want := range fixtureTools() {
		tool, _ := tools[i].(map[string]any)
		if tool["name"] != want.Name {
			t.Fatalf("tool[%d] = %v, want %s", i, tool["name"], want.Name)
		}
	}

	client.sendLine(t, "{not json")
	reply = client.read(t)
	if replyErrorText(t, reply) != "invalid JSON" {
		t.Fatalf("malformed JSON error = %#v", reply)
	}
	if _, ok := reply["id"]; ok {
		t.Fatalf("invalid JSON reply must not carry an id: %#v", reply)
	}

	client.send(t, map[string]any{"id": 3, "method": "nope"})
	if got := replyErrorText(t, client.read(t)); got != "unknown method: nope" {
		t.Fatalf("unknown method error = %q", got)
	}
	client.send(t, map[string]any{"id": 4})
	if got := replyErrorText(t, client.read(t)); got != "unknown method: undefined" {
		t.Fatalf("missing method error = %q", got)
	}

	client.send(t, map[string]any{"id": 5, "method": "shutdown"})
	reply = client.read(t)
	shutdownResult, _ := reply["result"].(map[string]any)
	if shutdownResult["ok"] != true {
		t.Fatalf("shutdown result = %#v", reply)
	}
	waitSocket(t, path, false)
	stop()
	if !strings.Contains(daemonLogText(t), "shutting down: stop requested") {
		t.Fatalf("log missing stop reason:\n%s", daemonLogText(t))
	}
}

// TestServerAlreadyRunning covers the startup probe: a live daemon on the
// same key makes a second StartDaemon return without stealing the socket.
func TestServerAlreadyRunning(t *testing.T) {
	path, _ := startFixtureDaemon(t, 0)
	done := make(chan error, 1)
	go func() { done <- StartDaemon(context.Background(), fixtureTools(), fixtureCall) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second StartDaemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second StartDaemon did not return")
	}
	if !isDaemonAlive(path) {
		t.Fatal("second StartDaemon disturbed the running daemon")
	}
	if !strings.Contains(daemonLogText(t), "already running at") {
		t.Fatalf("log missing already-running line:\n%s", daemonLogText(t))
	}
}

// TestServerReplacesStaleSocket covers startup: a leftover socket file with no
// listener is removed and replaced.
func TestServerReplacesStaleSocket(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("unix-socket fixture daemon is not available on Windows")
	}
	isolate(t)
	requireDist(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CAVE_TOOLS_DAEMON_IDLE_MS", "600000")
	if _, err := EnsureSocketDir(); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	// A dead socket path: Go removes sockets on listener close, so stand in
	// with a plain file that no daemon is listening on.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create stale socket file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- StartDaemon(ctx, fixtureTools(), fixtureCall) }()
	waitSocket(t, path, true)
	client := dialRaw(t, path)
	hello(t, client, nil)
	client.close()
	shutdownDaemon(t, path)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartDaemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartDaemon did not return after shutdown")
	}
	waitSocket(t, path, false)
}

// TestServerCallValidation covers the bad call request branches.
func TestServerCallValidation(t *testing.T) {
	path, _ := startFixtureDaemon(t, 0)
	client := dialRaw(t, path)
	defer client.close()
	hello(t, client, nil)

	cases := []map[string]any{
		{"id": 2, "method": "call", "tool": "cave__fixture_echo", "args": map[string]any{}},
		{"id": 3, "method": "call", "ctx": map[string]any{"cwd": "/tmp"}, "args": map[string]any{}},
		{"id": 4, "method": "call", "tool": 7, "ctx": map[string]any{"cwd": "/tmp"}, "args": map[string]any{}},
		{"id": 5, "method": "call", "tool": "cave__fixture_echo", "ctx": map[string]any{"cwd": 7}, "args": map[string]any{}},
	}
	for _, request := range cases {
		client.send(t, request)
		if got := replyErrorText(t, client.read(t)); got != "bad call request" {
			t.Fatalf("%v error = %q", request, got)
		}
	}
}

// TestServerCallContext covers ctx propagation, session namespace, per-client
// ids, media/_meta preservation and thrown callback errors.
func TestServerCallContext(t *testing.T) {
	path, _ := startFixtureDaemon(t, 0)
	client := dialRaw(t, path)
	defer client.close()
	hello(t, client, map[string]any{"clientPid": 777, "kind": "cli", "explicitSession": true})

	request := map[string]any{
		"id":     2,
		"method": "call",
		"tool":   "cave__fixture_echo",
		"args":   map[string]any{"text": "ctx", "media": true, "meta": true},
		"ctx": map[string]any{
			"sessionId": "sess-1",
			"cwd":       "/tmp/fixture-cwd",
			"env":       map[string]any{"CT_MARK": "hello"},
		},
	}
	client.send(t, request)
	reply := client.read(t)
	result, _ := reply["result"].(map[string]any)
	blocks, _ := result["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want 3: %#v", len(blocks), result)
	}
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	for _, want := range []string{
		"echo:ctx", "cwd=/tmp/fixture-cwd", "mark=hello", "session=sess-1",
		"ns=cli", "pid=777",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("echo text %q missing %q", text, want)
		}
	}
	if !strings.Contains(text, "client=c") {
		t.Fatalf("echo text %q missing per-connection client id", text)
	}
	image, _ := blocks[1].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != "aGk=" {
		t.Fatalf("image block = %#v", image)
	}
	resource, _ := blocks[2].(map[string]any)
	payload, _ := resource["resource"].(map[string]any)
	if resource["type"] != "resource" || payload["uri"] != "file:///fixture" {
		t.Fatalf("resource block = %#v", resource)
	}
	meta, _ := result["_meta"].(map[string]any)
	fixture, _ := meta["fixture"].(map[string]any)
	if fixture["ok"] != true {
		t.Fatalf("_meta = %#v", result["_meta"])
	}

	// Without --session opt-in the namespace stays empty.
	other := dialRaw(t, path)
	defer other.close()
	hello(t, other, map[string]any{"kind": "cli"})
	other.send(t, map[string]any{
		"id": 2, "method": "call", "tool": "cave__fixture_echo",
		"args": map[string]any{"text": "nons"},
		"ctx":  map[string]any{"sessionId": "sess-2", "cwd": "/tmp", "env": map[string]any{}},
	})
	reply = other.read(t)
	result, _ = reply["result"].(map[string]any)
	blocks, _ = result["content"].([]any)
	first, _ = blocks[0].(map[string]any)
	text, _ = first["text"].(string)
	if !strings.Contains(text, "ns=|") {
		t.Fatalf("namespace without explicit session = %q", text)
	}
	if !strings.Contains(text, "cwd=/tmp") || !strings.Contains(text, "mark=|") {
		t.Fatalf("second client did not carry its own cwd/env: %q", text)
	}

	// Callback errors become error replies, not connection failures.
	client.send(t, map[string]any{
		"id": 3, "method": "call", "tool": "cave__fixture_error",
		"args": map[string]any{}, "ctx": map[string]any{"cwd": "/tmp"},
	})
	if got := replyErrorText(t, client.read(t)); got != "fixture failure" {
		t.Fatalf("callback error = %q", got)
	}
}

// TestServerConcurrentCalls proves calls run concurrently and replies match ids.
func TestServerConcurrentCalls(t *testing.T) {
	path, _ := startFixtureDaemon(t, 0)
	client := dialRaw(t, path)
	defer client.close()
	hello(t, client, nil)

	slow := map[string]any{
		"id": 10, "method": "call", "tool": "cave__fixture_slow",
		"args": map[string]any{"text": "slow", "ms": 300},
		"ctx":  map[string]any{"cwd": "/tmp"},
	}
	fast := map[string]any{
		"id": 11, "method": "call", "tool": "cave__fixture_slow",
		"args": map[string]any{"text": "fast", "ms": 10},
		"ctx":  map[string]any{"cwd": "/tmp"},
	}
	client.send(t, slow)
	client.send(t, fast)

	first := client.read(t)
	second := client.read(t)
	if first["id"] != float64(11) {
		t.Fatalf("first reply id = %v, want 11 (fast call overtakes slow)", first["id"])
	}
	if second["id"] != float64(10) {
		t.Fatalf("second reply id = %v, want 10", second["id"])
	}
}

// TestServerIdleShutdown covers the idle timer removing the socket.
func TestServerIdleShutdown(t *testing.T) {
	path, _ := startFixtureDaemon(t, 150)
	client := dialRaw(t, path)
	hello(t, client, nil)
	client.close()
	waitSocket(t, path, false)
	if !strings.Contains(daemonLogText(t), "shutting down: idle") {
		t.Fatalf("log missing idle shutdown:\n%s", daemonLogText(t))
	}
}

// TestServerInFlightKeepsAlive mirrors TS: in-flight calls defer idle exit
// even after the client connection is gone.
func TestServerInFlightKeepsAlive(t *testing.T) {
	path, _ := startFixtureDaemon(t, 150)
	client := dialRaw(t, path)
	hello(t, client, nil)
	client.send(t, map[string]any{
		"id": 2, "method": "call", "tool": "cave__fixture_slow",
		"args": map[string]any{"text": "inflight", "ms": 700},
		"ctx":  map[string]any{"cwd": "/tmp"},
	})
	client.close()
	time.Sleep(400 * time.Millisecond)
	if !isDaemonAlive(path) {
		t.Fatal("daemon exited while a call was in flight")
	}
	waitSocket(t, path, false)
}

// TestServerClientStatsRelease covers per-PID connection counting: stats are
// released only when the last connection reporting that PID closes.
func TestServerClientStatsRelease(t *testing.T) {
	path, _ := startFixtureDaemon(t, 0)
	const pid = 918273
	statsCtx := runtime.RunWithContext(context.Background(), &runtime.CallContext{
		SessionID: "stats",
		Cwd:       "/tmp",
		Env:       map[string]string{},
		ClientPID: intPointer(pid),
	}, func(ctx context.Context) context.Context { return ctx })
	total := func() int64 {
		return compression.BuildStats(statsCtx, nil).Savings.TotalCalls
	}

	first := dialRaw(t, path)
	hello(t, first, map[string]any{"clientPid": pid})
	first.send(t, map[string]any{
		"id": 2, "method": "call", "tool": "cave__fixture_stats",
		"args": map[string]any{}, "ctx": map[string]any{"cwd": "/tmp"},
	})
	first.read(t)
	if total() < 1 {
		t.Fatalf("fixture stats calls = %d, want >= 1", total())
	}

	second := dialRaw(t, path)
	hello(t, second, map[string]any{"clientPid": pid})
	second.close()
	time.Sleep(100 * time.Millisecond)
	if total() < 1 {
		t.Fatal("stats released while another connection still reports the pid")
	}

	first.close()
	deadline := time.Now().Add(2 * time.Second)
	for total() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if total() != 0 {
		t.Fatalf("stats not released after last pid connection: %d", total())
	}
}

func intPointer(value int) *int { return &value }

// TestSpawnLock covers O_EXCL acquisition, stale-lock breaking and ownership
// on release.
func TestSpawnLock(t *testing.T) {
	isolate(t)
	if _, err := EnsureSocketDir(); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	key, err := DaemonKey()
	if err != nil {
		t.Fatalf("DaemonKey: %v", err)
	}
	lock, err := LockPath(key)
	if err != nil {
		t.Fatalf("LockPath: %v", err)
	}

	if !acquireLock(key) {
		t.Fatal("first acquireLock failed")
	}
	if acquireLock(key) {
		t.Fatal("second acquireLock succeeded while fresh lock exists")
	}
	if err := os.WriteFile(lock, []byte("999999 0"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	releaseLock(key)
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("releaseLock removed a lock owned by another pid")
	}
	if acquireLock(key) {
		t.Fatal("acquireLock broke a fresh lock owned by another pid")
	}

	stale := time.Now().Add(-(staleLockMS + 5000) * time.Millisecond)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatalf("age lock: %v", err)
	}
	if !acquireLock(key) {
		t.Fatal("acquireLock did not break a stale lock")
	}
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+" 0"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	releaseLock(key)
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("releaseLock did not remove own lock: %v", err)
	}
}

// TestDaemonDisabled covers the env opt-in semantics.
func TestDaemonDisabled(t *testing.T) {
	for _, value := range []string{"", "0", "false", "OFF", " off ", "\t"} {
		if DaemonEnabled(map[string]string{"CAVE_TOOLS_DAEMON": value}) {
			t.Fatalf("DaemonEnabled(%q) = true", value)
		}
	}
	for _, value := range []string{"1", "true", "on", "yes", " 2 "} {
		if !DaemonEnabled(map[string]string{"CAVE_TOOLS_DAEMON": value}) {
			t.Fatalf("DaemonEnabled(%q) = false", value)
		}
	}
	if DaemonEnabled(map[string]string{}) {
		t.Fatal("DaemonEnabled(empty env) = true")
	}
}

// TestSpawnedDaemonAndConcurrentSpawn covers cross-process semantics: four
// clients racing to spawn produce exactly one daemon and all calls succeed.
func TestSpawnedDaemonAndConcurrentSpawn(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("unix-socket fixture daemon is not available on Windows")
	}
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	dataDir := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("CAVE_TOOLS_DATA_DIR", dataDir)
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	waitSocket(t, path, false)
	t.Cleanup(func() {
		shutdownDaemon(t, path)
		waitSocket(t, path, false)
	})

	type childResult struct {
		out  string
		err  error
		code int
	}
	results := make(chan childResult, 4)
	for i := 1; i <= 4; i++ {
		text := fmt.Sprintf("child-%d", i)
		go func() {
			cmd := exec.Command(os.Args[0])
			// Stay inside the repository so the client can resolve dist/ for
			// the daemon it spawns; the call's cwd travels separately.
			cmd.Env = append(os.Environ(),
				"CAVE_TOOLS_DAEMON=1",
				"CAVE_TOOLS_DAEMON_IDLE_MS=60000",
				testClientModeEnv+"=call",
				testClientTextEnv+"="+text,
				testClientCwdEnv+"="+workDir,
			)
			out, err := cmd.CombinedOutput()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
			results <- childResult{out: string(out), err: err, code: code}
		}()
	}
	for i := 0; i < 4; i++ {
		result := <-results
		if result.code != 0 {
			t.Fatalf("client child exit %d: %s (err %v)", result.code, result.out, result.err)
		}
	}
	if got := strings.Count(daemonLogText(t), "listening on"); got != 1 {
		t.Fatalf("daemon start count = %d, want 1:\n%s", got, daemonLogText(t))
	}
	if !isDaemonAlive(path) {
		t.Fatal("daemon not running after concurrent spawn")
	}
}

// shutdownDaemon sends the shutdown request the way a client would.
func shutdownDaemon(t *testing.T, path string) {
	t.Helper()
	if !isDaemonAlive(path) {
		return
	}
	client := dialRaw(t, path)
	defer client.close()
	client.send(t, map[string]any{"id": 1, "method": "shutdown"})
	_ = client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = client.reader.ReadString('\n')
}
