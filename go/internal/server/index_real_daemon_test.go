package server

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/daemon"
	callruntime "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMCPRealDaemonToolRoundTrip covers the real registry/dispatch, not an echo
// fixture, through both the MCP transport and the shared daemon's JSON stream.
func TestMCPRealDaemonToolRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local integration fixture uses a Unix socket and POSIX shell")
	}
	root := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("CAVE_TOOLS_DAEMON", "1")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	t.Setenv("CAVE_TOOLS_ERROR_LOG", "0")
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	daemon.GetDaemonClient().Close()
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon.StartDaemon(ctx, AllTools, func(ctx context.Context, name string, args map[string]any, call *callruntime.CallContext) (types.ToolResult, error) {
			calls.Add(1)
			return CallTool(ctx, name, args, call)
		})
	}()
	t.Cleanup(func() {
		daemon.GetDaemonClient().Close()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("StartDaemon: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("real daemon did not stop")
		}
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
			t.Fatal("real daemon socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pipe := startMCPPipe(t)
	pipe.initialize(t)
	list := pipe.call(t, 2, "tools/list", map[string]any{})
	if got := len(list["result"].(map[string]any)["tools"].([]any)); got != len(AllTools) {
		t.Fatalf("real daemon registry has %d tools, want %d", got, len(AllTools))
	}
	id := 2
	call := func(name string, args map[string]any) map[string]any {
		id++
		response := pipe.call(t, id, "tools/call", map[string]any{
			"name": name, "arguments": args,
			"_meta": map[string]any{"sessionID": "real-daemon-roundtrip"},
		})
		result, ok := response["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s returned protocol error: %#v", name, response)
		}
		return result
	}
	textPath := filepath.Join(root, "note.txt")
	text := "real daemon <tag> & Unicode 😀\n"
	if err := os.WriteFile(textPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := firstText(t, call("cave__read", map[string]any{"file_path": textPath})); got != text {
		t.Fatalf("real daemon text = %q", got)
	}
	png := []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	pngPath := filepath.Join(root, "image.png")
	if err := os.WriteFile(pngPath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	image := call("cave__read", map[string]any{"file_path": pngPath})["content"].([]any)[0].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("real daemon image changed: %#v", image)
	}
	pdf := []byte("%PDF-1.7\nreal daemon resource\n")
	pdfPath := filepath.Join(root, "document.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	block := call("cave__read", map[string]any{"file_path": pdfPath})["content"].([]any)[0].(map[string]any)
	resource := block["resource"].(map[string]any)
	if block["type"] != "resource" || resource["mimeType"] != "application/pdf" || resource["blob"] != base64.StdEncoding.EncodeToString(pdf) {
		t.Fatalf("real daemon PDF changed: %#v", block)
	}
	missing := call("cave__read", map[string]any{"file_path": filepath.Join(root, "missing.txt")})
	if missing["isError"] != true || !strings.Contains(firstText(t, missing), "missing.txt") {
		t.Fatalf("real daemon soft error changed: %#v", missing)
	}
	bash := call("cave__bash", map[string]any{
		"command": "printf roundtrip; printf diagnostic >&2; exit 3", "__cli": true, "redact_secrets": false,
	})
	meta := bash["_meta"].(map[string]any)["caveCli"].(map[string]any)
	if firstText(t, bash) != "roundtrip" || meta["exitCode"] != float64(3) || meta["stderr"] != "diagnostic" {
		t.Fatalf("real daemon CLI metadata changed: %#v", bash)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("daemon handled %d real calls, want 5 (local fallback must not mask failure)", got)
	}
}
