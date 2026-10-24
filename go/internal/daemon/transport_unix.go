//go:build unix

package daemon

import (
	"net"
	"os"
	"syscall"
	"time"
)

// listenDaemon starts listening on the unix socket.
func listenDaemon(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}

// dialDaemon connects to the unix socket with TS's connect timeout.
func dialDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

// isDaemonAlive probes an existing socket file.
// port of src/daemon/server.ts:isAlive
func isDaemonAlive(path string) bool {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// removeDaemonSocket unlinks the socket file.
func removeDaemonSocket(path string) {
	_ = os.Remove(path)
}

// chmodDaemonSocket tightens the socket to 0600, like TS chmodSync.
func chmodDaemonSocket(path string) error {
	return os.Chmod(path, 0o600)
}

// sysProcAttr detaches the spawned daemon (TS detached: true).
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
