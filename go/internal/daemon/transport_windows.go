//go:build windows

package daemon

// Real Windows named-pipe transport for the shared daemon. Both sides use
// \\.\pipe\cave-tools-<key>, the same name TS derives in src/daemon/paths.ts.
// No TCP substitute: the server owns the pipe instances and the client opens
// the pipe path with CreateFile (via os.OpenFile).

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const pipeBufferSize = 64 * 1024

// listenDaemon creates the first named-pipe instance.
func listenDaemon(path string) (net.Listener, error) {
	listener := &pipeListener{name: path, closeCh: make(chan struct{})}
	handle, err := createPipeInstance(path)
	if err != nil {
		return nil, err
	}
	listener.pending = handle
	return listener, nil
}

func createPipeInstance(name string) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateNamedPipe(
		path,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES,
		pipeBufferSize,
		pipeBufferSize,
		0,
		nil,
	)
}

// pipeListener accepts named-pipe clients one at a time while keeping a spare
// instance open so the next client can connect immediately.
type pipeListener struct {
	name      string
	mu        sync.Mutex
	closed    bool
	pending   windows.Handle
	closeCh   chan struct{}
	closeOnce sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	handle := l.pending
	l.pending = 0
	// Prepare the next instance before waiting on this one.
	if next, err := createPipeInstance(l.name); err == nil {
		l.pending = next
	}
	l.mu.Unlock()

	if handle == 0 {
		created, err := createPipeInstance(l.name)
		if err != nil {
			return nil, err
		}
		handle = created
	}

	connected := make(chan error, 1)
	go func() {
		connected <- windows.ConnectNamedPipe(handle, nil)
	}()
	select {
	case <-l.closeCh:
		windows.CloseHandle(handle)
		return nil, net.ErrClosed
	case err := <-connected:
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			windows.CloseHandle(handle)
			return nil, err
		}
	}

	file := os.NewFile(uintptr(handle), l.name)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, errors.New("daemon: invalid named pipe handle")
	}
	return &pipeConn{file: file, name: l.name}, nil
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		handle := l.pending
		l.pending = 0
		l.mu.Unlock()
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		close(l.closeCh)
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.name) }

// pipeConn adapts a pipe handle to net.Conn. Deadlines are unsupported on
// synchronous pipes; callers rely on request timers instead.
type pipeConn struct {
	file *os.File
	name string
}

func (c *pipeConn) Read(buf []byte) (int, error)  { return c.file.Read(buf) }
func (c *pipeConn) Write(buf []byte) (int, error) { return c.file.Write(buf) }
func (c *pipeConn) Close() error                  { return c.file.Close() }

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr(c.name) }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr(c.name) }

func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// dialDaemon opens the named pipe, retrying while every instance is busy.
func dialDaemon(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			return &pipeConn{file: file, name: path}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// isDaemonAlive probes the pipe.
// port of src/daemon/server.ts:isAlive
func isDaemonAlive(path string) bool {
	conn, err := dialDaemon(path, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// removeDaemonSocket is a no-op: a named pipe disappears when its last
// instance closes.
func removeDaemonSocket(string) {}

// chmodDaemonSocket is a no-op: Windows pipes have no unix mode bits.
func chmodDaemonSocket(string) error { return nil }

// sysProcAttr detaches the spawned daemon, mirroring Node's detached: true
// (new process group) on Windows.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
