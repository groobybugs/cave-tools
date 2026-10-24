//go:build parity

// Black-box MCP stdio differential: the built TypeScript server and the Go
// binary get the same JSON-RPC frames and their responses must match exactly.
package parity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const mcpBootLine = "Cave Tools MCP server running on stdio"

// mcpProcess is a newline-delimited JSON-RPC client for one server process.
type mcpProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
	done   chan error
	waited bool
	err    error
}

// startMCP launches binary (with prefix) as an MCP stdio server.
func startMCP(t *testing.T, binary string, prefix []string, cwd string, env map[string]string) *mcpProcess {
	t.Helper()
	full := append(append([]string{}, prefix...), "mcp")
	cmd := exec.Command(binary, full...)
	cmd.Dir = cwd
	cmd.Env = flattenEnv(cleanEnv(env))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start MCP server: %v", err)
	}
	process := &mcpProcess{cmd: cmd, in: stdin, out: bufio.NewReader(stdout), stderr: stderr, done: make(chan error, 1)}
	go func() { process.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		process.stop(t, 5*time.Second)
	})
	return process
}

// stop closes stdin and waits once.
func (p *mcpProcess) stop(t *testing.T, timeout time.Duration) error {
	t.Helper()
	if p.waited {
		return p.err
	}
	_ = p.in.Close()
	select {
	case err := <-p.done:
		p.waited = true
		p.err = err
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		p.err = <-p.done
		p.waited = true
	}
	return p.err
}

// write sends one JSON-RPC message.
func (p *mcpProcess) write(t *testing.T, message map[string]any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.in.Write(append(data, '\n')); err != nil {
		t.Fatalf("write frame: %v (stderr %q)", err, p.stderr.String())
	}
}

// read reads one JSON-RPC response and verifies stdout stayed protocol-clean.
func (p *mcpProcess) read(t *testing.T) map[string]any {
	t.Helper()
	line, err := p.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read frame: %v (stderr %q)", err, p.stderr.String())
	}
	if trimmed := strings.TrimSpace(string(line)); trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("stdout is not clean JSON-RPC: %q", line)
	}
	var message map[string]any
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return message
}

// exchange writes a request and reads its response.
func (p *mcpProcess) exchange(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	p.write(t, message)
	return p.read(t)
}

// mcpPair is both servers with isolated data dirs but one shared fixture cwd.
type mcpPair struct {
	ts, goSide *mcpProcess
}

// startMCPPair starts both servers for a fixture cwd. The fixture is shared and
// read-only; each server gets private data/config/runtime dirs.
func startMCPPair(t *testing.T, fixture fixtureSpec, profile string) (*mcpPair, *side, *side) {
	t.Helper()
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	base := t.TempDir()
	cwd := filepath.Join(base, "fixture")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFixture(t, cwd, fixture)
	tsSide := newSide(t, "ts", tsBinary, tsPrefix, fixtureSpec{})
	goSide := newSide(t, "go", goBinary, nil, fixtureSpec{})
	env := func(s *side) map[string]string {
		return map[string]string{
			"CAVE_TOOLS_DATA_DIR":   s.data,
			"CLAUDE_CONFIG_DIR":     s.claude,
			"XDG_RUNTIME_DIR":       s.runDir,
			"HOME":                  s.home,
			"CAVE_TOOLS_DAEMON":     "0",
			"CAVE_TOOLS_PROFILE":    profile,
			"CAVE_TOOLS_ERROR_LOG":  "1",
			"CAVE_TOOLS_STATUS_RTK": "0",
		}
	}
	return &mcpPair{
		ts:     startMCP(t, tsBinary, tsPrefix, cwd, env(tsSide)),
		goSide: startMCP(t, goBinary, nil, cwd, env(goSide)),
	}, tsSide, goSide
}

// initializeFrame is the shared handshake request.
func initializeFrame() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "w5a-blackbox", "version": "0"},
		},
	}
}

// initializedNotification acknowledges the handshake.
func initializedNotification() map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
}

// handshake sends initialize (compared) and the initialized notification.
func handshake(t *testing.T, pair *mcpPair) map[string]any {
	t.Helper()
	want := pair.ts.exchange(t, initializeFrame())
	pair.goSide.write(t, initializeFrame())
	got := pair.goSide.read(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initialize differs\n go: %s\n ts: %s", mustJSON(t, got), mustJSON(t, want))
	}
	pair.ts.write(t, initializedNotification())
	pair.goSide.write(t, initializedNotification())
	return want
}

// closePair closes both and asserts the boot stderr line and clean stdout.
func closePair(t *testing.T, pair *mcpPair) {
	t.Helper()
	if err := pair.ts.stop(t, 5*time.Second); err != nil {
		t.Fatalf("TS MCP exited with %v", err)
	}
	if err := pair.goSide.stop(t, 5*time.Second); err != nil {
		t.Fatalf("Go MCP exited with %v", err)
	}
	for label, process := range map[string]*mcpProcess{"ts": pair.ts, "go": pair.goSide} {
		if got := process.stderr.String(); got != mcpBootLine+"\n" {
			t.Fatalf("%s stderr = %q, want %q", label, got, mcpBootLine+"\n")
		}
	}
}

// TestMCPToolsListDifferential compares tools/list for every profile.
func TestMCPToolsListDifferential(t *testing.T) {
	profiles := []struct {
		name  string
		value string
		want  int
	}{
		{name: "full", value: "full", want: 17},
		{name: "standard", value: "standard", want: 14},
		{name: "weird", value: "weird", want: 14},
		{name: "empty", value: "", want: 14},
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			pair, _, _ := startMCPPair(t, baseFixture(), profile.value)
			handshake(t, pair)
			frame := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}
			want := pair.ts.exchange(t, frame)
			pair.goSide.write(t, frame)
			got := pair.goSide.read(t)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("tools/list differs\n go: %s\n ts: %s", mustJSON(t, got), mustJSON(t, want))
			}
			tools := want["result"].(map[string]any)["tools"].([]any)
			if len(tools) != profile.want {
				t.Fatalf("profile %q listed %d tools, want %d", profile.name, len(tools), profile.want)
			}
			if profile.want == 17 {
				var names []string
				for _, raw := range tools {
					names = append(names, raw.(map[string]any)["name"].(string))
				}
				if !reflect.DeepEqual(names, allToolNames) {
					t.Fatalf("full tool order = %v, want %v", names, allToolNames)
				}
			} else {
				for _, hidden := range []string{"cave__configure", "cave__compress", "cave__status"} {
					for _, raw := range tools {
						if raw.(map[string]any)["name"] == hidden {
							t.Fatalf("profile %q unexpectedly listed %s", profile.name, hidden)
						}
					}
				}
			}
			closePair(t, pair)
		})
	}
}

// TestMCPCallDifferential compares tool calls, errors, media and caller
// session metadata over the wire.
func TestMCPCallDifferential(t *testing.T) {
	pair, _, _ := startMCPPair(t, baseFixture(), "full")
	handshake(t, pair)

	call := func(id int, name string, arguments any, meta map[string]any) map[string]any {
		params := map[string]any{"name": name}
		if arguments != nil {
			params["arguments"] = arguments
		}
		if meta != nil {
			params["_meta"] = meta
		}
		return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params}
	}
	frames := map[string]map[string]any{
		"read text":         call(10, "cave__read", map[string]any{"file_path": "note.txt"}, map[string]any{"sessionID": "wire"}),
		"read stub":         call(11, "cave__read", map[string]any{"file_path": "note.txt"}, map[string]any{"sessionID": "wire"}),
		"read image":        call(12, "cave__read", map[string]any{"file_path": "img.png"}, map[string]any{"sessionID": "wire-img"}),
		"read pdf":          call(13, "cave__read", map[string]any{"file_path": "doc.pdf"}, map[string]any{"sessionID": "wire-pdf"}),
		"read missing":      call(14, "cave__read", map[string]any{"file_path": "emptydir/nope.txt"}, nil),
		"read line numbers": call(15, "cave__read", map[string]any{"file_path": "lines.txt", "offset": 2, "limit": 2, "line_numbers": true}, nil),
		"bash combined":     call(16, "cave__bash", map[string]any{"command": "printf 'mcp-out'; printf 'mcp-err' >&2; exit 3"}, nil),
		"bash cli meta":     call(17, "cave__bash", map[string]any{"command": "printf 'cli-out'; printf 'cli-err' >&2; exit 3", "__cli": true}, nil),
		"compress":          call(18, "cave__compress", map[string]any{"text": "a a a a b b c", "tool_name": "bash"}, nil),
		"unknown tool":      call(19, "cave__nope", map[string]any{}, nil),
		"missing args":      call(20, "cave__ls", nil, nil),
		"null args":         call(21, "cave__ls", nil, nil),
		"number session":    call(22, "cave__read", map[string]any{"file_path": "code.ts", "limit": 1}, map[string]any{"sessionID": 42}),
		"object session":    call(23, "cave__read", map[string]any{"file_path": "sub/b.md"}, map[string]any{"sessionID": map[string]any{"a": 1}}),
		"null session":      call(24, "cave__read", map[string]any{"file_path": "nonl.txt"}, map[string]any{"sessionID": nil}),
		"array arguments":   call(25, "cave__read", []any{"note.txt"}, nil),
	}
	order := []string{"read text", "read stub", "read image", "read pdf", "read missing", "read line numbers", "bash combined", "bash cli meta", "compress", "unknown tool", "missing args", "null args", "number session", "object session", "null session", "array arguments"}
	for _, name := range order {
		want := pair.ts.exchange(t, frames[name])
		pair.goSide.write(t, frames[name])
		got := pair.goSide.read(t)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s differs\n go: %s\n ts: %s", name, mustJSON(t, got), mustJSON(t, want))
		}
	}
	closePair(t, pair)
}

// TestMCPPrivacyDisable proves CAVE_TOOLS_ERROR_LOG=0 suppresses persisted
// unknown-tool records on both implementations, while the default records them.
func TestMCPPrivacyDisable(t *testing.T) {
	for _, enabled := range []string{"1", "0"} {
		enabled := enabled
		t.Run("log="+enabled, func(t *testing.T) {
			tsBinary, tsPrefix := typescriptCLI(t)
			goBinary := sharedGoBinary(t)
			base := t.TempDir()
			cwd := filepath.Join(base, "fixture")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			seedFixture(t, cwd, baseFixture())
			tsSide := newSide(t, "ts", tsBinary, tsPrefix, fixtureSpec{})
			goSide := newSide(t, "go", goBinary, nil, fixtureSpec{})
			env := func(s *side) map[string]string {
				return map[string]string{
					"CAVE_TOOLS_DATA_DIR":  s.data,
					"CLAUDE_CONFIG_DIR":    s.claude,
					"XDG_RUNTIME_DIR":      s.runDir,
					"HOME":                 s.home,
					"CAVE_TOOLS_DAEMON":    "0",
					"CAVE_TOOLS_PROFILE":   "full",
					"CAVE_TOOLS_ERROR_LOG": enabled,
				}
			}
			pair := &mcpPair{
				ts:     startMCP(t, tsBinary, tsPrefix, cwd, env(tsSide)),
				goSide: startMCP(t, goBinary, nil, cwd, env(goSide)),
			}
			handshake(t, pair)
			frame := map[string]any{"jsonrpc": "2.0", "id": 30, "method": "tools/call", "params": map[string]any{"name": "cave__nope", "arguments": map[string]any{}}}
			want := pair.ts.exchange(t, frame)
			pair.goSide.write(t, frame)
			got := pair.goSide.read(t)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("unknown tool differs\n go: %s\n ts: %s", mustJSON(t, got), mustJSON(t, want))
			}
			closePair(t, pair)

			wantCount := 1
			if enabled == "0" {
				wantCount = 0
			}
			for label, dataDir := range map[string]string{"ts": tsSide.data, "go": goSide.data} {
				count := waitToolErrorCount(t, dataDir, wantCount)
				if count != wantCount {
					t.Fatalf("%s recorded %d tool errors, want %d", label, count, wantCount)
				}
			}
		})
	}
}

// waitToolErrorCount polls the async error log until it reaches want (or the
// deadline), then returns the last count.
func waitToolErrorCount(t *testing.T, dataDir string, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	count := 0
	for {
		count = toolErrorCount(t, dataDir)
		if count >= want || time.Now().After(deadline) {
			return count
		}
		time.Sleep(50 * time.Millisecond)
	}
}
