package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// startFakeDaemon listens on the real control socket path and answers one
// newline-delimited JSON request at a time. The handler owns the reply so
// tests can cover hello, shutdown and malformed replies.
func startFakeDaemon(t *testing.T, reply func(conn net.Conn, id int, method string) bool) string {
	t.Helper()
	if pathsPlatform == "win32" {
		t.Skip("unix sockets are not available on Windows")
	}
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
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
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					var request struct {
						ID     int    `json:"id"`
						Method string `json:"method"`
					}
					if json.Unmarshal(scanner.Bytes(), &request) != nil {
						continue
					}
					if !reply(conn, request.ID, request.Method) {
						return
					}
				}
			}(conn)
		}
	}()
	return path
}

func controlEnv(t *testing.T) {
	t.Helper()
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
}

func rpcReplyLine(id int, result string) string {
	return fmt.Sprintf(`{"id":%d,"result":%s}`+"\n", id, result)
}

// port of test.mjs daemon status ("running pid N build B socket S").
func TestDaemonStatusRunning(t *testing.T) {
	startFakeDaemon(t, func(conn net.Conn, id int, method string) bool {
		if method == "hello" {
			fmt.Fprint(conn, rpcReplyLine(1, `{"pid":4321,"buildId":"feedface"}`))
		}
		return true
	})
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	var out bytes.Buffer
	code, err := DaemonStatus(&out)
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "running pid 4321 build feedface socket " + path + "\n"; out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestDaemonStatusNotRunning(t *testing.T) {
	controlEnv(t)
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	build, err := BuildID()
	if err != nil {
		t.Fatalf("BuildID: %v", err)
	}
	var out bytes.Buffer
	code, err := DaemonStatus(&out)
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if want := "not running (socket " + path + ", build " + build + ")\n"; out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestDaemonStatusErrorReplyIsNotRunning(t *testing.T) {
	startFakeDaemon(t, func(conn net.Conn, id int, method string) bool {
		if method == "hello" {
			fmt.Fprint(conn, `{"id":1,"error":"nope"}`+"\n")
		}
		return true
	})
	var out bytes.Buffer
	code, err := DaemonStatus(&out)
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("not running")) {
		t.Fatalf("stdout = %q, want not running", out.String())
	}
}

// port of test.mjs daemon stop.
func TestDaemonStop(t *testing.T) {
	startFakeDaemon(t, func(conn net.Conn, id int, method string) bool {
		switch method {
		case "hello":
			fmt.Fprint(conn, rpcReplyLine(1, `{"pid":1,"buildId":"b"}`))
		case "shutdown":
			fmt.Fprint(conn, rpcReplyLine(2, `{"ok":true}`))
			return false
		}
		return true
	})
	var out bytes.Buffer
	code, err := DaemonStop(&out)
	if err != nil {
		t.Fatalf("DaemonStop: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out.String() != "stopped\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "stopped\n")
	}
}

func TestDaemonStopNotRunning(t *testing.T) {
	controlEnv(t)
	var out bytes.Buffer
	code, err := DaemonStop(&out)
	if err != nil {
		t.Fatalf("DaemonStop: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out.String() != "not running\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "not running\n")
	}
}

// port of src/daemon/control.ts:rpc 2000 ms timeout.
func TestDaemonStatusTimesOut(t *testing.T) {
	startFakeDaemon(t, func(net.Conn, int, string) bool {
		return true // accept and stay silent
	})
	var out bytes.Buffer
	started := time.Now()
	code, err := DaemonStatus(&out)
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if elapsed := time.Since(started); elapsed < 1900*time.Millisecond {
		t.Fatalf("returned after %v, want >= 2s", elapsed)
	}
}

func TestSocketPathIsUnderSocketDir(t *testing.T) {
	controlEnv(t)
	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("socket %q not under %q", path, dir)
	}
}
