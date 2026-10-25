//go:build !windows

package statusline

import (
	"os"
	"syscall"
)

// dirOwnerOK ports the getuid() ownership check for a symlinked Claude config
// dir.
func dirOwnerOK(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}
