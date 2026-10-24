package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// tsMCPProcess is a raw JSON-RPC client over the built TS MCP stdio server.
type tsMCPProcess struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	stderr  *bytes.Buffer
	waited  bool
	waitErr error
}

func startTSMCP(t *testing.T, root, cwd, dataDir, profile string) *tsMCPProcess {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	cli := filepath.Join(root, "dist", "cli.js")
	if _, err := os.Stat(cli); err != nil {
		t.Skip("dist/cli.js missing; run pnpm build")
	}
	cmd := exec.Command(node, cli, "mcp")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"CAVE_TOOLS_DATA_DIR="+dataDir,
		"CAVE_TOOLS_DAEMON=0",
		"CAVE_TOOLS_PROFILE="+profile,
	)
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
		t.Fatal(err)
	}
	process := &tsMCPProcess{cmd: cmd, in: stdin, out: bufio.NewReader(stdout), stderr: stderr}
	t.Cleanup(func() {
		_ = stdin.Close()
		if !process.waited {
			process.wait(t, 5*time.Second)
		}
	})
	return process
}

// wait reaps the process once. Reading the stderr buffer is only safe after it
// returns, because exec copies stderr from its own goroutine.
func (p *tsMCPProcess) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	if p.waited {
		return p.waitErr
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		p.waited = true
		p.waitErr = err
		return err
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		err := <-done
		p.waited = true
		p.waitErr = err
		return err
	}
}

func (p *tsMCPProcess) write(t *testing.T, message map[string]any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.in.Write(append(data, '\n')); err != nil {
		t.Fatalf("write: %v (stderr %q)", err, p.stderr.String())
	}
}

func (p *tsMCPProcess) exchange(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	p.write(t, message)
	line, err := p.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v (stderr %q)", err, p.stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return response
}

// TestMCPWireDifferential sends the same JSON-RPC frames to the Go and TS MCP
// servers and compares every result, including media blocks and _meta session
// coercion.
func TestMCPWireDifferential(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	goData := filepath.Join(cwd, "go-data")
	tsData := filepath.Join(cwd, "ts-data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", goData)
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")

	textPath := filepath.Join(cwd, "note.txt")
	if err := os.WriteFile(textPath, []byte("wire text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	imageBytes := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("wire-image")...)
	imagePath := filepath.Join(cwd, "pixel.png")
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pdfBytes := []byte("%PDF-1.4\nwire pdf\n")
	pdfPath := filepath.Join(cwd, "doc.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ts := startTSMCP(t, root, cwd, tsData, "full")
	goPipe := startMCPPipe(t)

	type frame struct {
		name    string
		message map[string]any
	}
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "w4i-diff", "version": "0"},
		},
	}
	frames := []frame{
		{name: "initialize", message: initialize},
		{name: "initialized", message: map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}},
		{name: "tools/list", message: map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}},
		{name: "read text", message: map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": textPath}, "_meta": map[string]any{"sessionID": "wire"},
		}}},
		{name: "read image", message: map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": imagePath}, "_meta": map[string]any{"sessionID": "wire-img"},
		}}},
		{name: "read pdf", message: map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": pdfPath}, "_meta": map[string]any{"sessionID": "wire-pdf"},
		}}},
		{name: "unknown tool", message: map[string]any{"jsonrpc": "2.0", "id": 6, "method": "tools/call", "params": map[string]any{
			"name": "cave__nope", "arguments": map[string]any{},
		}}},
		{name: "missing args", message: map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{
			"name": "cave__ls",
		}}},
		{name: "null args", message: map[string]any{"jsonrpc": "2.0", "id": 8, "method": "tools/call", "params": map[string]any{
			"name": "cave__ls", "arguments": nil,
		}}},
		{name: "null session first", message: map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": textPath}, "_meta": map[string]any{"sessionID": nil},
		}}},
		{name: "null session second", message: map[string]any{"jsonrpc": "2.0", "id": 10, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": textPath}, "_meta": map[string]any{"sessionID": nil},
		}}},
		{name: "object session", message: map[string]any{"jsonrpc": "2.0", "id": 11, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{"file_path": textPath}, "_meta": map[string]any{"sessionID": map[string]any{"a": 1}},
		}}},
		{name: "empty object args", message: map[string]any{"jsonrpc": "2.0", "id": 12, "method": "tools/call", "params": map[string]any{
			"name": "cave__read", "arguments": map[string]any{}, "_meta": map[string]any{"sessionID": "empty"},
		}}},
		{name: "bash cli meta", message: map[string]any{"jsonrpc": "2.0", "id": 13, "method": "tools/call", "params": map[string]any{
			"name": "cave__bash", "arguments": map[string]any{"command": "printf hi", "description": "wire meta", "__cli": true},
		}}},
	}

	for _, item := range frames {
		t.Logf("frame %s", item.name)
		if item.name == "initialized" {
			ts.write(t, item.message)
			goPipe.write(t, item.message)
			continue
		}
		want := ts.exchange(t, item.message)
		goPipe.write(t, item.message)
		got := goPipe.read(t)
		if !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			t.Fatalf("%s differs\n go: %s\n ts: %s", item.name, gotJSON, wantJSON)
		}
	}

	_ = ts.in.Close()
	_ = ts.wait(t, 5*time.Second)
	if got := ts.stderr.String(); got != bootLine+"\n" {
		t.Fatalf("TS stderr = %q", got)
	}
	if err := goPipe.in.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-goPipe.done:
		goPipe.stopped = true
		if err != nil {
			t.Fatalf("StartServer returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Go server did not stop on EOF")
	}
	if got := goPipe.stderr.String(); got != bootLine+"\n" {
		t.Fatalf("Go stderr = %q", got)
	}
}
