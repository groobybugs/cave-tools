package daemon

// Thin client for the shared daemon (CAVE_TOOLS_DAEMON=1). Imports no tool
// code: callers fall back to in-process dispatch only when a request was
// never sent.
//
// port of src/daemon-client.ts

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

const (
	connectTimeoutMS = 300
	helloTimeoutMS   = 2000
	spawnWaitMS      = 2000
	pollMS           = 50
	staleLockMS      = 10_000
	// After the daemon can't be reached, run in-process for a while instead of
	// paying the spawn wait on every call.
	retryBackoffMS = 30_000
)

// ConnectionLostText is reported when a call was written but the daemon went
// away before replying. The call may have run, so it must never be retried
// locally (bash/edit have side effects).
//
// port of src/daemon-client.ts:CONNECTION_LOST_TEXT
const ConnectionLostText = "daemon connection lost; command may or may not have run"

// HelloOptions is the hello payload telling the daemon who is connecting.
// port of src/daemon-client.ts:DaemonClient constructor hello argument
type HelloOptions struct {
	// Kind is "cli" for the CLI; empty for MCP clients.
	Kind string
	// ExplicitSession opts into stable cli:<sessionId> keys (CLI --session).
	ExplicitSession bool
}

// CallOutcome reports whether a request reached the daemon. Sent=false allows
// the caller to run in-process; Sent=true means the result is final.
// port of src/daemon-client.ts:CallOutcome
type CallOutcome struct {
	Sent   bool
	Result types.ToolResult
}

// DaemonEnabled reports whether CAVE_TOOLS_DAEMON opts into the shared daemon.
// The default is the process environment; tests pass an explicit env map.
//
// port of src/daemon-client.ts:daemonEnabled
func DaemonEnabled(env ...map[string]string) bool {
	var value string
	if len(env) > 0 {
		if env[0] != nil {
			value = env[0]["CAVE_TOOLS_DAEMON"]
		}
	} else {
		value = os.Getenv("CAVE_TOOLS_DAEMON")
	}
	value = strings.ToLower(jsre.Trim(value))
	return value != "" && value != "0" && value != "false" && value != "off"
}

// waiter is one in-flight request. The channel is buffered so the reader
// never blocks on a caller that already timed out.
type waiter struct {
	ch chan waiterResult
}

type waiterResult struct {
	result  json.RawMessage
	errText *string
	err     error
}

// DaemonClient is a persistent NDJSON connection to the shared daemon.
// port of src/daemon-client.ts:DaemonClient
type DaemonClient struct {
	hello    HelloOptions
	clientID string
	nextID   int

	mu         sync.Mutex
	conn       net.Conn
	pending    map[int]*waiter
	retryAfter time.Time

	// connectMu serialises connectOrSpawn so concurrent first calls share one
	// handshake, like the single `connecting` promise in TS.
	connectMu sync.Mutex
	// writeMu serialises whole request lines; Go has no single event loop.
	writeMu sync.Mutex
}

// NewDaemonClient builds a client with an explicit hello payload.
// port of src/daemon-client.ts:new DaemonClient(...)
func NewDaemonClient(hello ...HelloOptions) *DaemonClient {
	options := HelloOptions{}
	if len(hello) > 0 {
		options = hello[0]
	}
	return &DaemonClient{
		hello:    options,
		clientID: fmt.Sprintf("%d-%s", os.Getpid(), randomHex(4)),
		pending:  make(map[int]*waiter),
	}
}

// randomHex returns n random bytes as lowercase hex, matching
// Math.random().toString(16).slice(2, 10)'s eight character shape.
func randomHex(n int) string {
	buf := make([]byte, n)
	// crypto/rand.Read never returns an error on the platforms Go supports;
	// fall back to the timestamp if it ever does.
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

// GetDaemonClient returns the process-wide client. The first hello payload
// wins, exactly like the TS module-level singleton.
//
// port of src/daemon-client.ts:getDaemonClient
func GetDaemonClient(hello ...HelloOptions) *DaemonClient {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		shared = NewDaemonClient(hello...)
	}
	return shared
}

var (
	sharedMu sync.Mutex
	shared   *DaemonClient
)

// ClientID is the identity this client reports at hello.
func (c *DaemonClient) ClientID() string { return c.clientID }

// attach starts the reader for one connection.
// port of src/daemon-client.ts:DaemonClient.attach
func (c *DaemonClient) attach(conn net.Conn) {
	go func() {
		reader := bufio.NewReaderSize(conn, 64*1024)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				// A partial line is never parsed: TS only handles complete
				// newline-delimited messages.
				c.lost(conn)
				return
			}
			if jsre.Trim(line) == "" {
				continue
			}
			var msg struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *string         `json:"error"`
			}
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if msg.ID == nil {
				continue
			}
			c.mu.Lock()
			waiter, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
			if !ok {
				continue
			}
			waiter.ch <- waiterResult{result: msg.Result, errText: msg.Error}
		}
	}()
}

// lost fails every pending request and forgets the connection.
// port of src/daemon-client.ts:DaemonClient.attach `lost`
func (c *DaemonClient) lost(conn net.Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	waiters := make([]*waiter, 0, len(c.pending))
	for _, waiter := range c.pending {
		waiters = append(waiters, waiter)
	}
	c.pending = make(map[int]*waiter)
	c.mu.Unlock()

	for _, waiter := range waiters {
		waiter.ch <- waiterResult{err: errors.New(ConnectionLostText)}
	}
}

// request writes one NDJSON request and waits for the reply with its id.
// port of src/daemon-client.ts:DaemonClient.request
func (c *DaemonClient) request(conn net.Conn, method string, payload map[string]any, timeout time.Duration) (waiterResult, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	w := &waiter{ch: make(chan waiterResult, 1)}
	c.pending[id] = w
	c.mu.Unlock()

	message := make(map[string]any, len(payload)+2)
	message["id"] = id
	message["method"] = method
	for key, value := range payload {
		message[key] = value
	}
	line, err := json.Marshal(message)
	if err != nil {
		c.removeWaiter(id)
		return waiterResult{}, err
	}
	line = append(line, '\n')

	c.writeMu.Lock()
	err = writeFull(conn, line)
	c.writeMu.Unlock()
	if err != nil {
		c.removeWaiter(id)
		return waiterResult{}, err
	}

	if timeout <= 0 {
		res := <-w.ch
		return res, res.err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-w.ch:
		return res, res.err
	case <-timer.C:
		c.removeWaiter(id)
		return waiterResult{}, fmt.Errorf("daemon %s timed out", method)
	}
}

func (c *DaemonClient) removeWaiter(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// tryConnect dials with TS's 300 ms connect timeout; nil means unreachable.
// port of src/daemon-client.ts:tryConnect
func tryConnect(path string) net.Conn {
	conn, err := dialDaemon(path, connectTimeoutMS*time.Millisecond)
	if err != nil {
		return nil
	}
	return conn
}

// handshake attaches and sends hello.
// port of src/daemon-client.ts:DaemonClient.handshake
func (c *DaemonClient) handshake(conn net.Conn) net.Conn {
	c.attach(conn)
	payload := map[string]any{
		"clientPid": os.Getpid(),
		"clientId":  c.clientID,
	}
	if c.hello.Kind != "" {
		payload["kind"] = c.hello.Kind
	}
	if c.hello.ExplicitSession {
		payload["explicitSession"] = true
	}
	reply, err := c.request(conn, "hello", payload, helloTimeoutMS*time.Millisecond)
	if err != nil || reply.errText != nil {
		conn.Close()
		return nil
	}
	return conn
}

// connectOrSpawn connects to the current-build daemon, spawning one under the
// O_EXCL lock when nobody is listening.
// port of src/daemon-client.ts:DaemonClient.connectOrSpawn
func (c *DaemonClient) connectOrSpawn() net.Conn {
	// Recomputed per attempt so a rebuild while this client lives is seen.
	key, err := DaemonKey()
	if err != nil {
		return nil
	}
	// Verify the socket dir (ours, 0700, not a symlink) BEFORE connecting: each
	// call carries the full env, and the /tmp fallback dir could be pre-created
	// by another user with their own socket in it.
	if pathsPlatform != "win32" {
		if _, err := EnsureSocketDir(key); err != nil {
			return nil
		}
	}
	path, err := SocketPath(key)
	if err != nil {
		return nil
	}
	conn := tryConnect(path)
	if conn != nil {
		return c.handshake(conn)
	}

	owner := acquireLock(key)
	defer func() {
		if owner {
			releaseLock(key)
		}
	}()
	if owner {
		spawnDaemon()
	}
	deadline := time.Now().Add(spawnWaitMS * time.Millisecond)
	for conn == nil && time.Now().Before(deadline) {
		time.Sleep(pollMS * time.Millisecond)
		conn = tryConnect(path)
	}
	if conn == nil {
		return nil
	}
	return c.handshake(conn)
}

// ensure returns the live connection, connecting (and spawning) if needed.
// nil means the daemon is unavailable and nothing was sent.
// port of src/daemon-client.ts:DaemonClient.ensure
func (c *DaemonClient) ensure() net.Conn {
	c.mu.Lock()
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn
	}
	if time.Now().Before(c.retryAfter) {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	// Another caller may have connected while we waited.
	c.mu.Lock()
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn
	}
	if time.Now().Before(c.retryAfter) {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	conn := c.connectOrSpawn()
	c.mu.Lock()
	c.conn = conn
	if conn == nil {
		c.retryAfter = time.Now().Add(retryBackoffMS * time.Millisecond)
	}
	c.mu.Unlock()
	return conn
}

// callRequestContext is the ctx object of a call request.
type callRequestContext struct {
	SessionID string            `json:"sessionId"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
}

// Call runs one tool through the daemon.
// port of src/daemon-client.ts:DaemonClient.call
func (c *DaemonClient) Call(ctx context.Context, tool string, args map[string]any, call *runtime.CallContext) (CallOutcome, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CallOutcome{}, err
		}
	}
	conn := c.ensure()
	if conn == nil {
		return CallOutcome{}, nil
	}
	if args == nil {
		args = map[string]any{}
	}
	requestCtx := callRequestContext{}
	if call != nil {
		requestCtx.SessionID = call.SessionID
		requestCtx.Cwd = call.Cwd
		requestCtx.Env = call.Env
	}
	// A nil CallContext means every field was omitted: use the same defaults
	// the in-process dispatcher would.
	if call == nil {
		wd, _ := os.Getwd()
		requestCtx.SessionID = "default"
		requestCtx.Cwd = wd
		requestCtx.Env = runtime.EnvFromEnviron(os.Environ())
	}
	if requestCtx.Cwd == "" {
		requestCtx.Cwd, _ = os.Getwd()
	}

	payload := map[string]any{
		"tool": tool,
		"args": args,
		"ctx":  requestCtx,
	}
	reply, err := c.request(conn, "call", payload, 0)
	if err != nil {
		// The request was written: never re-run it (bash/edit have side effects).
		return CallOutcome{Sent: true, Result: errorResult(ConnectionLostText)}, nil
	}
	if reply.errText != nil {
		return CallOutcome{Sent: true, Result: errorResult(*reply.errText)}, nil
	}
	result, err := decodeToolResult(reply.result)
	if err != nil {
		// The call reached the daemon; never report Sent=false here or the
		// caller could re-run a side-effecting tool.
		return CallOutcome{Sent: true, Result: errorResult(ConnectionLostText)}, nil
	}
	return CallOutcome{Sent: true, Result: result}, nil
}

// List fetches the daemon's tool definitions. nil means "fall back".
// port of src/daemon-client.ts:DaemonClient.list
func (c *DaemonClient) List(ctx context.Context) ([]types.ToolDefinition, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	conn := c.ensure()
	if conn == nil {
		return nil, nil
	}
	reply, err := c.request(conn, "list", map[string]any{}, helloTimeoutMS*time.Millisecond)
	if err != nil || reply.errText != nil {
		return nil, nil
	}
	var envelope struct {
		Tools *[]types.ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(reply.result, &envelope); err != nil {
		// TS casts the parsed JSON; a malformed result falls back to local tools.
		return nil, nil
	}
	if envelope.Tools == nil {
		return nil, nil
	}
	return *envelope.Tools, nil
}

// Close drops the connection. A later call reconnects, like TS.
// port of src/daemon-client.ts:DaemonClient.close
func (c *DaemonClient) Close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// errorResult builds the TS error-shaped CallToolResult.
func errorResult(text string) types.ToolResult {
	return types.ToolResult{
		Content: []types.Content{types.Text(text)},
		IsError: true,
	}
}

// decodeToolResult rebuilds a types.ToolResult from the daemon's JSON without
// losing _meta or media blocks.
func decodeToolResult(raw json.RawMessage) (types.ToolResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return types.ToolResult{}, nil
	}
	var envelope struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
		Meta    map[string]any    `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return types.ToolResult{}, err
	}
	result := types.ToolResult{IsError: envelope.IsError, Meta: envelope.Meta}
	for _, block := range envelope.Content {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &head); err != nil {
			return types.ToolResult{}, err
		}
		switch head.Type {
		case "text":
			var text types.TextContent
			if err := json.Unmarshal(block, &text); err != nil {
				return types.ToolResult{}, err
			}
			result.Content = append(result.Content, text)
		case "image":
			var image types.ImageContent
			if err := json.Unmarshal(block, &image); err != nil {
				return types.ToolResult{}, err
			}
			result.Content = append(result.Content, image)
		case "resource":
			var resource types.ResourceContent
			if err := json.Unmarshal(block, &resource); err != nil {
				return types.ToolResult{}, err
			}
			result.Content = append(result.Content, resource)
		default:
			// types.Content only models the blocks src/types.ts produces;
			// refuse anything else instead of silently dropping it.
			return types.ToolResult{}, fmt.Errorf("daemon: unsupported content type %q", head.Type)
		}
	}
	return result, nil
}

// acquireLock takes the O_EXCL spawn lock. A lock older than STALE_LOCK_MS
// (crashed spawner) is broken.
// port of src/daemon-client.ts:acquireLock
func acquireLock(key string) bool {
	file, err := LockPath(key)
	if err != nil {
		return false
	}
	for attempt := 0; attempt < 2; attempt++ {
		fd, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(fd, "%d %d", os.Getpid(), time.Now().UnixMilli())
			fd.Close()
			return true
		}
		if !errors.Is(err, fs.ErrExist) {
			return false
		}
		info, statErr := os.Stat(file)
		if statErr != nil {
			// Raced with another client removing it; retry once.
			continue
		}
		if time.Since(info.ModTime()) > staleLockMS*time.Millisecond {
			os.Remove(file)
			continue
		}
		return false
	}
	return false
}

// releaseLock removes the lock only when this process owns it.
// port of src/daemon-client.ts:releaseLock
func releaseLock(key string) {
	file, err := LockPath(key)
	if err != nil {
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}
	owner := strings.SplitN(string(raw), " ", 2)[0]
	if owner == strconv.Itoa(os.Getpid()) {
		os.Remove(file)
	}
}

// spawnDaemon starts a detached Go daemon. TS spawns `node dist/cli.js daemon
// start`; Go re-executes itself so an explicit Go build never starts the TS
// daemon.
// port of src/daemon-client.ts:spawnDaemon
func spawnDaemon() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	home, _ := os.UserHomeDir()
	cmd := exec.Command(executable, "daemon", "start")
	cmd.Dir = home
	cmd.Env = os.Environ()
	// The daemon runs in the user's home, where it cannot walk up to the
	// repository dist/. Hand it the dist the client resolved so buildId() and
	// the socket key stay identical to TS.
	if dist, err := tsDistDir(); err == nil {
		cmd.Env = setEnv(cmd.Env, distDirEnv, dist)
	}
	if logFile, err := os.OpenFile(DaemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		return
	}
	// Detached: the client must not wait for (or kill) the daemon on exit.
	_ = cmd.Process.Release()
}

// setEnv replaces key in an environment slice (exec does not deduplicate).
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

// distDirEnv lets a re-executed Go daemon find the TS dist when its cwd is the
// user's home. TS needs no equivalent: it launches dist/cli.js by path.
const distDirEnv = "CAVE_TOOLS_GO_DIST_DIR"

// writeFull writes the whole buffer, retrying short writes.
func writeFull(w net.Conn, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("daemon: short write")
		}
		buf = buf[n:]
	}
	return nil
}
