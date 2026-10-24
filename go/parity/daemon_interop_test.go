//go:build parity

// Cross-implementation daemon integration: a Go CLI (and MCP server) driving
// the TypeScript daemon and a TypeScript CLI (and MCP server) driving the Go
// daemon, with the real tool registry on the server side.
package parity

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// daemonHarness owns one hermetic data/runtime/config tree.
type daemonHarness struct {
	root     string
	data     string
	run      string
	home     string
	claude   string
	work     string
	tsBinary string
	tsPrefix []string
	goBinary string
}

// newDaemonHarness creates the shared directories and both binary handles.
func newDaemonHarness(t *testing.T) *daemonHarness {
	t.Helper()
	requireMode(t)
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	root := repoRoot(t)
	base := t.TempDir()
	h := &daemonHarness{
		root:     root,
		data:     filepath.Join(base, "data"),
		run:      filepath.Join(base, "run"),
		home:     filepath.Join(base, "home"),
		claude:   filepath.Join(base, "claude"),
		work:     filepath.Join(base, "work"),
		tsBinary: tsBinary,
		tsPrefix: tsPrefix,
		goBinary: goBinary,
	}
	for _, dir := range []string{h.data, h.run, h.home, h.claude, h.work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return h
}

// baseEnv is the daemon process environment (never a daemon client).
func (h *daemonHarness) baseEnv() map[string]string {
	return map[string]string{
		"CAVE_TOOLS_DATA_DIR":       h.data,
		"CLAUDE_CONFIG_DIR":         h.claude,
		"XDG_RUNTIME_DIR":           h.run,
		"HOME":                      h.home,
		"CAVE_TOOLS_DAEMON":         "0",
		"CAVE_TOOLS_DAEMON_IDLE_MS": "60000",
		"CAVE_TOOLS_GO_DIST_DIR":    filepath.Join(h.root, "dist"),
		"CAVE_TOOLS_STATUS_RTK":     "0",
	}
}

// clientEnv opts one CLI/MCP process into the shared daemon.
func (h *daemonHarness) clientEnv(extra map[string]string) map[string]string {
	env := h.baseEnv()
	env["CAVE_TOOLS_DAEMON"] = "1"
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// daemonProcess is a started daemon child.
type daemonProcess struct {
	cmd    *exec.Cmd
	done   chan error
	out    *bytes.Buffer
	exited bool
}

// startTSDaemon starts node dist/cli.js daemon start.
func (h *daemonHarness) startTSDaemon(t *testing.T) *daemonProcess {
	t.Helper()
	args := append(append([]string{}, h.tsPrefix...), "daemon", "start")
	return h.startDaemon(t, h.tsBinary, args)
}

// startGoDaemon starts the Go binary's daemon.
func (h *daemonHarness) startGoDaemon(t *testing.T) *daemonProcess {
	t.Helper()
	return h.startDaemon(t, h.goBinary, []string{"daemon", "start"})
}

// startDaemon starts a daemon, waits for its socket, and registers cleanup.
func (h *daemonHarness) startDaemon(t *testing.T, binary string, args []string) *daemonProcess {
	t.Helper()
	process := &daemonProcess{out: &bytes.Buffer{}}
	process.cmd = exec.Command(binary, args...)
	process.cmd.Dir = h.root
	process.cmd.Env = flattenEnv(cleanEnv(h.baseEnv()))
	process.cmd.Stdout = process.out
	process.cmd.Stderr = process.out
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	process.done = make(chan error, 1)
	go func() { process.done <- process.cmd.Wait() }()
	t.Cleanup(func() {
		h.stop(t, process)
	})
	h.waitReady(t, process)
	return process
}

// waitReady polls daemon status through the Go binary.
func (h *daemonHarness) waitReady(t *testing.T, process *daemonProcess) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		result := runCommandOutcome(t, h.goBinary, nil, h.work, h.baseEnv(), "", "daemon", "status")
		if result.code == 0 {
			return
		}
		select {
		case err := <-process.done:
			process.exited = true
			t.Fatalf("daemon exited before ready: %v\n%s", err, process.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never became ready: status=%q stderr=%q\n%s", result.stdout, result.stderr, process.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop asks the daemon to shut down and reaps it. Safe to call repeatedly.
func (h *daemonHarness) stop(t *testing.T, process *daemonProcess) {
	t.Helper()
	if process == nil || process.done == nil {
		return
	}
	if process.exited {
		return
	}
	select {
	case <-process.done:
		process.exited = true
		return
	default:
	}
	_ = runCommandOutcome(t, h.goBinary, nil, h.work, h.baseEnv(), "", "daemon", "stop")
	select {
	case <-process.done:
		process.exited = true
	case <-time.After(10 * time.Second):
		_ = process.cmd.Process.Kill()
		<-process.done
		process.exited = true
	}
}

// runGo runs the Go CLI with the daemon client environment.
func (h *daemonHarness) runGo(t *testing.T, cwd string, env map[string]string, args ...string) outcome {
	t.Helper()
	return runCommandOutcome(t, h.goBinary, nil, cwd, h.clientEnv(env), "", args...)
}

// runTS runs the TypeScript CLI with the daemon client environment.
func (h *daemonHarness) runTS(t *testing.T, cwd string, env map[string]string, args ...string) outcome {
	t.Helper()
	return runCommandOutcome(t, h.tsBinary, h.tsPrefix, cwd, h.clientEnv(env), "", args...)
}

// writeWorkFile creates a fixture file inside the shared work dir.
func (h *daemonHarness) writeWorkFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(h.work, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// waitSessionRows polls persisted stats until at least want client rows exist.
func (h *daemonHarness) waitSessionRows(t *testing.T, want int) dbState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := readDBState(t, h.data)
		if len(state.Sessions) >= want || time.Now().After(deadline) {
			return state
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertDistinctClientRows proves daemon-client stats did not merge across
// client processes: every persisted row has a unique pid, the bash call is
// accounted once, and the explicit-session reads show their cache hit/miss
// pattern (read calls are cache events, not total_calls).
func assertDistinctClientRows(t *testing.T, state dbState, wantRows int) {
	t.Helper()
	if len(state.Sessions) < wantRows {
		t.Fatalf("got %d session rows, want at least %d: %s", len(state.Sessions), wantRows, formatDBState(state))
	}
	seen := map[int64]bool{}
	var bashCalls int64
	var hits, misses int64
	for _, row := range state.Sessions {
		if seen[row.Pid] {
			t.Fatalf("duplicate stats row for pid %d: %s", row.Pid, formatDBState(state))
		}
		seen[row.Pid] = true
		bashCalls += row.Tools["bash"]
		hits += row.CacheHits
		misses += row.CacheMisses
	}
	if bashCalls != 1 {
		t.Fatalf("accounted %d bash calls, want 1: %s", bashCalls, formatDBState(state))
	}
	if misses < 2 || hits < 1 {
		t.Fatalf("explicit-session cache stats = %d hits / %d misses, want >=1/>=2: %s", hits, misses, formatDBState(state))
	}
}

// TestDaemonGoClientToTSDaemon drives the real TypeScript daemon from Go CLI
// processes: cwd/env, explicit-session persistence across processes, stats
// isolation and persisted budgets.
func TestDaemonGoClientToTSDaemon(t *testing.T) {
	requireMode(t)
	h := newDaemonHarness(t)
	h.startTSDaemon(t)
	h.writeWorkFile(t, "note.txt", "session content\n")

	bash := h.runGo(t, h.work, map[string]string{"CT_MARK": "go-env"}, "bash", `printf '%s|%s' "$PWD" "$CT_MARK"`)
	if bash.code != 0 || bash.stdout != h.work+"|go-env\n" || bash.stderr != "" {
		t.Fatalf("daemon bash cwd/env = code %d stdout %q stderr %q", bash.code, bash.stdout, bash.stderr)
	}

	first := h.runGo(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if first.code != 0 || !strings.HasPrefix(first.stdout, "session content") {
		t.Fatalf("first explicit-session read = code %d stdout %q stderr %q", first.code, first.stdout, first.stderr)
	}
	second := h.runGo(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if second.code != 0 || !strings.Contains(second.stdout, "<file unchanged since read #1 (session cli:shared)") {
		t.Fatalf("second explicit-session read = code %d stdout %q stderr %q", second.code, second.stdout, second.stderr)
	}
	other := h.runGo(t, h.work, nil, "read", "note.txt", "--session", "other")
	if other.code != 0 || !strings.HasPrefix(other.stdout, "session content") {
		t.Fatalf("other explicit-session read = code %d stdout %q stderr %q", other.code, other.stdout, other.stderr)
	}

	configured := h.runGo(t, h.work, nil, "configure", "--action", "set_budget", "--tool_name", "read", "--max_lines", "17", "--max_chars", "123")
	if configured.code != 0 {
		t.Fatalf("daemon configure = code %d stdout %q stderr %q", configured.code, configured.stdout, configured.stderr)
	}

	state := h.waitSessionRows(t, 5)
	assertDistinctClientRows(t, state, 5)

	// The daemon owns stats; the CLI process that stopped it must not add its
	// own local bucket for the daemon-served calls.
	h.stopDaemonProcess(t)

	status := runCommandOutcome(t, h.goBinary, nil, h.work, cleanEnv(h.baseEnv()), "", "status", "--verbose")
	if status.code != 0 {
		t.Fatalf("status after daemon stop = code %d stdout %q stderr %q", status.code, status.stdout, status.stderr)
	}
	if !strings.Contains(status.stdout, "calls") {
		t.Fatalf("status lost persisted calls: %q", status.stdout)
	}
	persisted := readDBState(t, h.data)
	if !hasBudget(persisted, "read", 17, 123) {
		t.Fatalf("daemon-configured read budget was not persisted: %s", formatDBState(persisted))
	}

	// The read cache is in-memory per daemon: after a restart the same
	// explicit session reads the file again instead of returning a stub.
	h.startTSDaemon(t)
	fresh := h.runGo(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if fresh.code != 0 || !strings.HasPrefix(fresh.stdout, "session content") {
		t.Fatalf("post-restart read = code %d stdout %q stderr %q", fresh.code, fresh.stdout, fresh.stderr)
	}
}

// TestDaemonTSClientToGoDaemon mirrors the previous test with the TypeScript
// CLI driving the Go daemon.
func TestDaemonTSClientToGoDaemon(t *testing.T) {
	requireMode(t)
	h := newDaemonHarness(t)
	h.startGoDaemon(t)
	h.writeWorkFile(t, "note.txt", "session content\n")

	bash := h.runTS(t, h.work, map[string]string{"CT_MARK": "ts-env"}, "bash", `printf '%s|%s' "$PWD" "$CT_MARK"`)
	if bash.code != 0 || bash.stdout != h.work+"|ts-env\n" || bash.stderr != "" {
		t.Fatalf("daemon bash cwd/env = code %d stdout %q stderr %q", bash.code, bash.stdout, bash.stderr)
	}

	first := h.runTS(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if first.code != 0 || !strings.HasPrefix(first.stdout, "session content") {
		t.Fatalf("first explicit-session read = code %d stdout %q stderr %q", first.code, first.stdout, first.stderr)
	}
	second := h.runTS(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if second.code != 0 || !strings.Contains(second.stdout, "<file unchanged since read #1 (session cli:shared)") {
		t.Fatalf("second explicit-session read = code %d stdout %q stderr %q", second.code, second.stdout, second.stderr)
	}
	other := h.runTS(t, h.work, nil, "read", "note.txt", "--session", "other")
	if other.code != 0 || !strings.HasPrefix(other.stdout, "session content") {
		t.Fatalf("other explicit-session read = code %d stdout %q stderr %q", other.code, other.stdout, other.stderr)
	}

	configured := h.runTS(t, h.work, nil, "configure", "--action", "set_budget", "--tool_name", "bash", "--max_lines", "19", "--max_chars", "321")
	if configured.code != 0 {
		t.Fatalf("daemon configure = code %d stdout %q stderr %q", configured.code, configured.stdout, configured.stderr)
	}

	state := h.waitSessionRows(t, 5)
	assertDistinctClientRows(t, state, 5)

	h.stopDaemonProcess(t)

	status := runCommandOutcome(t, h.tsBinary, h.tsPrefix, h.work, cleanEnv(h.baseEnv()), "", "status", "--verbose")
	if status.code != 0 {
		t.Fatalf("status after daemon stop = code %d stdout %q stderr %q", status.code, status.stdout, status.stderr)
	}
	persisted := readDBState(t, h.data)
	if !hasBudget(persisted, "bash", 19, 321) {
		t.Fatalf("daemon-configured bash budget was not persisted: %s", formatDBState(persisted))
	}

	h.startGoDaemon(t)
	fresh := h.runTS(t, h.work, nil, "read", "note.txt", "--session", "shared")
	if fresh.code != 0 || !strings.HasPrefix(fresh.stdout, "session content") {
		t.Fatalf("post-restart read = code %d stdout %q stderr %q", fresh.code, fresh.stdout, fresh.stderr)
	}
}

// TestMCPDaemonCross runs a TS MCP server against the Go daemon and a Go MCP
// server against the TS daemon, exercising list/call/session over both hops.
func TestMCPDaemonCross(t *testing.T) {
	requireMode(t)
	t.Run("ts-mcp-to-go-daemon", func(t *testing.T) {
		h := newDaemonHarness(t)
		h.startGoDaemon(t)
		h.writeWorkFile(t, "note.txt", "cross mcp content\n")
		process := startMCP(t, h.tsBinary, h.tsPrefix, h.work, h.clientEnv(map[string]string{"CAVE_TOOLS_PROFILE": "full"}))
		exerciseDaemonMCP(t, h, process, "cross")
	})
	t.Run("go-mcp-to-ts-daemon", func(t *testing.T) {
		h := newDaemonHarness(t)
		h.startTSDaemon(t)
		h.writeWorkFile(t, "note.txt", "cross mcp content\n")
		process := startMCP(t, h.goBinary, nil, h.work, h.clientEnv(map[string]string{"CAVE_TOOLS_PROFILE": "full"}))
		exerciseDaemonMCP(t, h, process, "cross")
	})
}

// exerciseDaemonMCP performs the shared daemon-MCP exchange.
func exerciseDaemonMCP(t *testing.T, h *daemonHarness, process *mcpProcess, session string) {
	t.Helper()
	init := process.exchange(t, initializeFrame())
	if init["result"] == nil {
		t.Fatalf("initialize through daemon failed: %s", mustJSON(t, init))
	}
	process.write(t, initializedNotification())

	list := process.exchange(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 17 {
		t.Fatalf("daemon tools/list returned %d tools: %s", len(tools), mustJSON(t, list))
	}

	bash := process.exchange(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "cave__bash", "arguments": map[string]any{"command": "printf daemon-mcp-ok"},
	}})
	text := mcpResultText(t, bash)
	if text != "daemon-mcp-ok" {
		t.Fatalf("daemon MCP bash = %q (%s)", text, mustJSON(t, bash))
	}

	read := func(id int) map[string]any {
		return process.exchange(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
			"name":      "cave__read",
			"arguments": map[string]any{"file_path": "note.txt"},
			"_meta":     map[string]any{"sessionID": session},
		}})
	}
	first := read(4)
	if got := mcpResultText(t, first); !strings.HasPrefix(got, "cross mcp content") {
		t.Fatalf("daemon MCP read = %q", got)
	}
	second := read(5)
	if got := mcpResultText(t, second); !strings.Contains(got, "<file unchanged since read #1") {
		t.Fatalf("daemon MCP session did not dedup: %q", got)
	}
	// The TS MCP server keeps the daemon socket open, so closing stdin alone
	// does not end the process. Stop the daemon first: the client sees EOF on
	// its idle socket, then the transport closes.
	h.stopDaemonProcess(t)
	if err := process.stop(t, 5*time.Second); err != nil {
		t.Fatalf("MCP server exited with %v", err)
	}
	if got := process.stderr.String(); got != mcpBootLine+"\n" {
		t.Fatalf("MCP stderr = %q", got)
	}
}

// mcpResultText extracts the first text block from a tools/call result.
func mcpResultText(t *testing.T, message map[string]any) string {
	t.Helper()
	result, _ := message["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %s", mustJSON(t, message))
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

// stopDaemonProcess is a helper for tests that need to stop the registered
// daemon before their deferred cleanup, then start another one.
func (h *daemonHarness) stopDaemonProcess(t *testing.T) {
	t.Helper()
	_ = runCommandOutcome(t, h.goBinary, nil, h.work, h.baseEnv(), "", "daemon", "stop")
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := runCommandOutcome(t, h.goBinary, nil, h.work, h.baseEnv(), "", "daemon", "status")
		if status.code != 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
