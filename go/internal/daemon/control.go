package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

// rpcTimeout mirrors the TS control client's 2000 ms timer.
const rpcTimeout = 2 * time.Second

// rpcResult is the payload of a hello/shutdown reply.
type rpcResult struct {
	PID     *int64  `json:"pid"`
	BuildID *string `json:"buildId"`
}

// rpcReply is one newline-delimited JSON message from the daemon.
type rpcReply struct {
	ID     int        `json:"id"`
	Result *rpcResult `json:"result"`
	Error  *string    `json:"error"`
}

// rpc sends `hello` (and then `method` when it is not hello itself) and waits
// for the reply with the matching id. A connection error, a malformed stream,
// or the 2 s timeout all yield a nil reply, exactly like TS resolving null.
//
// port of src/daemon/control.ts:rpc
func rpc(method string) (*rpcReply, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := dialDaemon(path)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()

	wantID := 1
	if method != "hello" {
		wantID = 2
	}

	replies := make(chan *rpcReply, 1)
	go func() {
		hello := struct {
			ID        int    `json:"id"`
			Method    string `json:"method"`
			ClientPID int    `json:"clientPid"`
		}{ID: 1, Method: "hello", ClientPID: os.Getpid()}
		if err := writeJSONLine(conn, hello); err != nil {
			replies <- nil
			return
		}
		if method != "hello" {
			request := struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}{ID: 2, Method: method}
			if err := writeJSONLine(conn, request); err != nil {
				replies <- nil
				return
			}
		}
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var msg rpcReply
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				// A malformed reply line must not crash the CLI; TS gained the
				// same try/catch in 1b3fbbc (scripts/test-port-bugs.mjs #13).
				continue
			}
			if msg.ID == wantID {
				replies <- &msg
				return
			}
		}
		replies <- nil
	}()

	select {
	case reply := <-replies:
		return reply, nil
	case <-time.After(rpcTimeout):
		// Close so the reader goroutine unblocks and exits.
		conn.Close()
		return nil, nil
	}
}

// writeJSONLine writes one compact JSON object followed by a newline.
func writeJSONLine(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

// dialDaemon connects to the daemon socket: a unix socket everywhere except
// Windows, where SocketPath returns a named pipe opened as a file.
func dialDaemon(path string) (io.ReadWriteCloser, error) {
	if pathsPlatform == "win32" {
		return os.OpenFile(path, os.O_RDWR, 0)
	}
	return net.DialTimeout("unix", path, rpcTimeout)
}

// DaemonStatus implements `cave-tools daemon status`: 0 when a daemon answered
// hello, 1 when it did not. The caller owns the error path because TS lets
// socketPath()/buildId() throw.
//
// port of src/daemon/control.ts:daemonStatus
func DaemonStatus(out io.Writer) (int, error) {
	reply, err := rpc("hello")
	if err != nil {
		return 0, err
	}
	path, err := SocketPath()
	if err != nil {
		return 0, err
	}
	if reply == nil || reply.Result == nil {
		// TS only calls buildId() on the not-running branch.
		build, err := BuildID()
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(out, "not running (socket %s, build %s)\n", path, build)
		return 1, nil
	}
	fmt.Fprintf(out, "running pid %s build %s socket %s\n",
		jsOptionalInt(reply.Result.PID), jsOptionalString(reply.Result.BuildID), path)
	return 0, nil
}

// DaemonStop implements `cave-tools daemon stop`.
//
// port of src/daemon/control.ts:daemonStop
func DaemonStop(out io.Writer) (int, error) {
	reply, err := rpc("shutdown")
	if err != nil {
		return 0, err
	}
	if reply == nil || reply.Result == nil {
		fmt.Fprintln(out, "not running")
		return 1, nil
	}
	fmt.Fprintln(out, "stopped")
	return 0, nil
}

// jsOptionalInt mirrors `${undefined}` / `${123}` in a TS template literal.
func jsOptionalInt(value *int64) string {
	if value == nil {
		return "undefined"
	}
	return strconv.FormatInt(*value, 10)
}

// jsOptionalString mirrors `${undefined}` / `${"x"}` in a TS template literal.
func jsOptionalString(value *string) string {
	if value == nil {
		return "undefined"
	}
	return *value
}
