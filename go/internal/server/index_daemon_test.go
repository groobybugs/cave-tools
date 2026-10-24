package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/daemon"
	callruntime "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMCPDaemonFirst proves the MCP server lists and calls through the daemon
// when CAVE_TOOLS_DAEMON is enabled, using the real daemon package.
func TestMCPDaemonFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemon socket fixture is unix-only")
	}
	root := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("CAVE_TOOLS_DAEMON", "1")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}

	fixture := []types.ToolDefinition{{
		Name:        "cave__fixture_echo",
		Description: "daemon fixture",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	call := func(_ context.Context, name string, args map[string]any, _ *callruntime.CallContext) (types.ToolResult, error) {
		text := ""
		if value, ok := args["text"].(string); ok {
			text = value
		}
		return types.ToolResult{Content: []types.Content{types.Text(name + ":" + text)}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.StartDaemon(ctx, fixture, call) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("StartDaemon returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("StartDaemon did not stop")
		}
		daemon.GetDaemonClient().Close()
	})

	path, err := daemon.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon socket %s did not appear", path)
		}
		time.Sleep(10 * time.Millisecond)
	}

	pipe := startMCPPipe(t)
	pipe.initialize(t)
	list := pipe.call(t, 2, "tools/list", map[string]any{})
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "cave__fixture_echo" {
		t.Fatalf("daemon tools/list = %#v", tools)
	}
	result := pipe.call(t, 3, "tools/call", map[string]any{
		"name":      "cave__fixture_echo",
		"arguments": map[string]any{"text": "hi"},
		"_meta":     map[string]any{"sessionID": "daemon-test"},
	})
	if got := firstText(t, result["result"].(map[string]any)); got != "cave__fixture_echo:hi" {
		t.Fatalf("daemon call = %q", got)
	}
}
