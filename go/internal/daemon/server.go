package daemon

// Shared daemon: newline-delimited JSON over a unix socket (named pipe on
// Windows). Each call runs with the client's cwd/env; sessions and stats are
// namespaced per connection / client process.
//
// port of src/daemon/server.ts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// defaultIdleMS is the daemon's idle lifetime when CAVE_TOOLS_DAEMON_IDLE_MS
// is unset or invalid.
const defaultIdleMS = 10 * 60 * 1000

// logLine writes one best-effort daemon log line.
// port of src/daemon/server.ts:log
func logLine(message string) {
	path := DaemonLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s [%d] %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), os.Getpid(), message)
}

// idleMs reads CAVE_TOOLS_DAEMON_IDLE_MS the way TS Number() does: anything
// non-finite or <= 0 falls back to the default.
// port of src/daemon/server.ts:idleMs
func idleMs() time.Duration {
	raw := jsre.Trim(os.Getenv("CAVE_TOOLS_DAEMON_IDLE_MS"))
	value, err := strconv.ParseFloat(raw, 64)
	if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 {
		return time.Duration(value * float64(time.Millisecond))
	}
	return defaultIdleMS * time.Millisecond
}

// callRequest is one decoded `call` method payload.
type callRequest struct {
	SessionID json.RawMessage `json:"sessionId"`
	Cwd       json.RawMessage `json:"cwd"`
	Env       json.RawMessage `json:"env"`
}

// daemonRequest is one NDJSON request. Raw messages preserve the id exactly
// and defer per-method type validation to the handler, like TS.
type daemonRequest struct {
	ID              json.RawMessage `json:"id"`
	Method          json.RawMessage `json:"method"`
	ClientPID       json.RawMessage `json:"clientPid"`
	Kind            json.RawMessage `json:"kind"`
	ExplicitSession json.RawMessage `json:"explicitSession"`
	Tool            json.RawMessage `json:"tool"`
	Args            json.RawMessage `json:"args"`
	Ctx             *callRequest    `json:"ctx"`
}

// daemonReply is one NDJSON reply. Error is a pointer so an empty error string
// still encodes (TS always includes error when reply() gets one).
type daemonReply struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  *string         `json:"error,omitempty"`
}

// daemonServer is the listening state of StartDaemon.
type daemonServer struct {
	build    string
	path     string
	isPipe   bool
	idle     time.Duration
	tools    []types.ToolDefinition
	callTool func(context.Context, string, map[string]any, *runtime.CallContext) (types.ToolResult, error)

	listener net.Listener

	mu             sync.Mutex
	connections    int
	inFlight       int
	idleTimer      *time.Timer
	pidConnections map[int]int
	conns          map[net.Conn]struct{}

	closing      bool
	done         chan struct{}
	shutdownOnce sync.Once
}

// StartDaemon serves the shared daemon until shutdown or cancellation.
// Callback injection keeps the daemon package free of a server import cycle;
// CLI passes server.AllTools and server.CallTool.
//
// port of src/daemon/server.ts:startDaemon
func StartDaemon(ctx context.Context, tools []types.ToolDefinition, callTool func(context.Context, string, map[string]any, *runtime.CallContext) (types.ToolResult, error)) error {
	if callTool == nil {
		return fmt.Errorf("daemon: callTool is nil")
	}
	// Key and build fixed at startup: a later rebuild gets its own daemon.
	key, err := DaemonKey()
	if err != nil {
		return err
	}
	build, err := BuildID()
	if err != nil {
		return err
	}
	path, err := SocketPath(key)
	if err != nil {
		return err
	}
	isPipe := pathsPlatform == "win32"
	if !isPipe {
		if _, err := EnsureSocketDir(key); err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			if isDaemonAlive(path) {
				logLine("already running at " + path)
				return nil
			}
			removeDaemonSocket(path)
		}
	}

	go compression.PruneDeadSessions()
	_, _ = runtime.PruneJobs()
	go compression.CleanupArchives()

	if tools == nil {
		tools = []types.ToolDefinition{}
	}
	server := &daemonServer{
		build:          build,
		path:           path,
		isPipe:         isPipe,
		idle:           idleMs(),
		tools:          tools,
		callTool:       callTool,
		pidConnections: make(map[int]int),
		conns:          make(map[net.Conn]struct{}),
		done:           make(chan struct{}),
	}

	listener, err := listenDaemon(path)
	if err != nil {
		logLine("server error: " + err.Error())
		return err
	}
	server.listener = listener
	if !isPipe {
		if err := chmodDaemonSocket(path); err != nil {
			listener.Close()
			removeDaemonSocket(path)
			return err
		}
	}
	logLine(fmt.Sprintf("listening on %s (build %s, idle %dms)", path, build, server.idle.Milliseconds()))
	server.touchIdle()

	acceptErr := make(chan error, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			server.addConn(conn)
			go server.handleConn(conn)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-acceptErr:
		if server.isClosing() {
			return nil
		}
		logLine("server error: " + err.Error())
		return err
	case <-ctx.Done():
		server.shutdown("context canceled")
	case sig := <-sigCh:
		server.shutdown(signalReason(sig))
	case <-server.done:
	}
	return nil
}

// isClosing reports whether shutdown has begun.
func (s *daemonServer) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// signalReason names a signal the way the TS handlers do.
func signalReason(sig os.Signal) string {
	switch sig {
	case os.Interrupt:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	default:
		return sig.String()
	}
}

// shutdown closes the listener, removes the socket and drops every connection,
// matching process.exit(0) after server.close().
// port of src/daemon/server.ts:shutdown
func (s *daemonServer) shutdown(reason string) {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		logLine("shutting down: " + reason)
		s.listener.Close()
		if !s.isPipe {
			removeDaemonSocket(s.path)
		}
		s.mu.Lock()
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		s.mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
		close(s.done)
	})
}

// touchIdle restarts the idle countdown when nothing is connected or running.
// port of src/daemon/server.ts:touchIdle
func (s *daemonServer) touchIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchIdleLocked()
}

func (s *daemonServer) touchIdleLocked() {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	if s.connections == 0 && s.inFlight == 0 {
		s.idleTimer = time.AfterFunc(s.idle, s.checkIdle)
	}
}

func (s *daemonServer) checkIdle() {
	s.mu.Lock()
	idle := s.connections == 0 && s.inFlight == 0
	s.mu.Unlock()
	if idle {
		s.shutdown("idle")
	}
}

// addConn registers an accepted connection before its reader starts.
func (s *daemonServer) addConn(conn net.Conn) {
	s.mu.Lock()
	s.connections++
	s.conns[conn] = struct{}{}
	s.touchIdleLocked()
	s.mu.Unlock()
}

// handleConn reads newline-delimited requests and runs calls concurrently.
// port of src/daemon/server.ts:net.createServer connection handler
func (s *daemonServer) handleConn(conn net.Conn) {
	// Assigned here, never taken from the client, so namespacing can't be spoofed.
	clientID := "c" + randomHex(6)
	var stateMu sync.Mutex
	var clientPid *int
	// Explicit CLI --session opt-in (same-uid trust): stable cli:<id> keys.
	sessionNamespace := ""
	var writeMu sync.Mutex

	reply := func(msg daemonReply) {
		raw, err := json.Marshal(msg)
		if err != nil {
			return
		}
		raw = append(raw, '\n')
		writeMu.Lock()
		_ = writeFull(conn, raw)
		writeMu.Unlock()
	}
	replyError := func(id json.RawMessage, text string) {
		reply(daemonReply{ID: id, Error: &text})
	}

	handle := func(req daemonRequest) {
		switch requestMethod(req.Method) {
		case "hello":
			stateMu.Lock()
			if requestString(req.Kind) == "cli" && string(req.ExplicitSession) == "true" {
				sessionNamespace = "cli"
			}
			if pid, ok := requestInteger(req.ClientPID); ok && clientPid == nil {
				clientPid = &pid
				s.mu.Lock()
				s.pidConnections[pid]++
				s.mu.Unlock()
			}
			stateMu.Unlock()
			reply(daemonReply{ID: req.ID, Result: struct {
				BuildID string `json:"buildId"`
				PID     int    `json:"pid"`
			}{BuildID: s.build, PID: os.Getpid()}})
		case "list":
			reply(daemonReply{ID: req.ID, Result: struct {
				Tools []types.ToolDefinition `json:"tools"`
			}{Tools: s.tools}})
		case "shutdown":
			reply(daemonReply{ID: req.ID, Result: struct {
				OK bool `json:"ok"`
			}{OK: true}})
			// setImmediate: answer first, then stop serving.
			go s.shutdown("stop requested")
		case "call":
			tool, ok := requestStringOK(req.Tool)
			if !ok || req.Ctx == nil {
				replyError(req.ID, "bad call request")
				return
			}
			cwd, ok := requestStringOK(req.Ctx.Cwd)
			if !ok {
				replyError(req.ID, "bad call request")
				return
			}
			stateMu.Lock()
			var pidCopy *int
			if clientPid != nil {
				value := *clientPid
				pidCopy = &value
			}
			namespace := sessionNamespace
			stateMu.Unlock()
			call := &runtime.CallContext{
				SessionID:        requestSessionID(req.Ctx.SessionID),
				Cwd:              cwd,
				Env:              decodeEnv(req.Ctx.Env),
				ClientID:         clientID,
				ClientPID:        pidCopy,
				SessionNamespace: namespace,
			}
			args := decodeArgs(req.Args)

			s.mu.Lock()
			s.inFlight++
			if s.idleTimer != nil {
				s.idleTimer.Stop()
				s.idleTimer = nil
			}
			s.mu.Unlock()
			go func() {
				defer func() {
					s.mu.Lock()
					s.inFlight--
					s.touchIdleLocked()
					s.mu.Unlock()
				}()
				var result types.ToolResult
				var callErr error
				runtime.RunWithContext(context.Background(), call, func(ctx context.Context) struct{} {
					result, callErr = s.callTool(ctx, tool, args, call)
					return struct{}{}
				})
				if callErr != nil {
					replyError(req.ID, callErr.Error())
					return
				}
				reply(daemonReply{ID: req.ID, Result: result})
			}()
		default:
			replyError(req.ID, "unknown method: "+requestValueString(req.Method))
		}
	}

	reader := bufio.NewReaderSize(conn, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if jsre.Trim(line) == "" {
			continue
		}
		var req daemonRequest
		if json.Unmarshal([]byte(line), &req) != nil {
			replyError(nil, "invalid JSON")
			continue
		}
		// The switch runs synchronously (like TS's event loop): in-flight calls
		// are counted before the handler yields, and only tool callbacks run
		// concurrently.
		handle(req)
	}

	conn.Close()
	s.mu.Lock()
	s.connections--
	s.mu.Unlock()
	stateMu.Lock()
	releasePid := 0
	shouldRelease := false
	if clientPid != nil {
		s.mu.Lock()
		left := s.pidConnections[*clientPid] - 1
		if left <= 0 {
			delete(s.pidConnections, *clientPid)
			releasePid = *clientPid
			shouldRelease = true
		} else {
			s.pidConnections[*clientPid] = left
		}
		s.mu.Unlock()
	}
	stateMu.Unlock()
	s.mu.Lock()
	delete(s.conns, conn)
	s.touchIdleLocked()
	s.mu.Unlock()
	if shouldRelease {
		compression.ReleaseClientStats(context.Background(), releasePid)
	}
}

// requestMethod decodes the method field; a missing/non-string method is
// reported as the JS string "undefined" (or its JSON scalar text).
func requestMethod(raw json.RawMessage) string {
	value, ok := requestStringOK(raw)
	if !ok {
		return "\x00" // cannot collide with a real method name
	}
	return value
}

// requestString mirrors `typeof value === "string"` coercion for the kind.
func requestString(raw json.RawMessage) string {
	value, _ := requestStringOK(raw)
	return value
}

// requestStringOK reports whether raw is a JSON string.
func requestStringOK(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// requestInteger mirrors Number.isInteger.
func requestInteger(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

// requestValueString mirrors String(value) for template literals.
func requestValueString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "undefined"
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "undefined"
	}
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	default:
		return string(raw)
	}
}

// requestSessionID mirrors String(ctx.sessionId ?? "default").
func requestSessionID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "default"
	}
	return requestValueString(raw)
}

// decodeArgs decodes the call arguments object.
func decodeArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return nil
	}
	return args
}

// decodeEnv mirrors `ctx.env ?? {}` for an object of strings; a non-object
// degrades to an empty env instead of failing the call.
func decodeEnv(raw json.RawMessage) map[string]string {
	env := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return env
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) == nil && values != nil {
		return values
	}
	return env
}
