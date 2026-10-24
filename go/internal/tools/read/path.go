// Node path semantics (path.dirname/basename/extname).
//
// read.ts calls path.extname on every path and dirname/basename when it builds
// the "did you mean" hint. Go's filepath.Dir/Base/Ext disagree with Node on
// dotfiles, trailing dots and trailing separators, so the algorithms are ported
// directly. Separator handling is platform-specific (path_unix.go /
// path_windows.go); device roots ("C:", `\\host\share`) come from
// filepath.VolumeName, which is empty on unix.

package read

import (
	"path/filepath"
	"strings"
)

// nodeExtname mirrors path.extname: the extension of the last path component.
// A dotfile (single leading dot, no other dot) has no extension, "." and ".."
// have none, and a trailing dot yields ".".
func nodeExtname(path string) string {
	start := len(filepath.VolumeName(path))
	if start > len(path) {
		start = len(path)
	}

	end := len(path)
	for end > start && pathIsSeparator(path[end-1]) {
		end--
	}
	if end <= start {
		return ""
	}

	segmentStart := start
	for i := end - 1; i >= start; i-- {
		if pathIsSeparator(path[i]) {
			segmentStart = i + 1
			break
		}
	}
	segment := path[segmentStart:end]
	dot := strings.LastIndexByte(segment, '.')
	if dot < 0 {
		return ""
	}
	if dot == 0 && !strings.ContainsRune(segment[1:], '.') {
		// Dotfile: ".bashrc" has no extension.
		return ""
	}
	if allDots(segment) {
		// "." and ".." have no extension; longer dot runs keep the last dot.
		if len(segment) <= 2 {
			return ""
		}
		return "."
	}
	return segment[dot:]
}

// allDots reports whether every byte of segment is '.'.
func allDots(segment string) bool {
	for i := 0; i < len(segment); i++ {
		if segment[i] != '.' {
			return false
		}
	}
	return true
}

// nodeDirname mirrors path.dirname: trailing separators are ignored, a
// separator-only path is the root, and a leading "//" is preserved (posix).
func nodeDirname(path string) string {
	if path == "" {
		return "."
	}
	volume := filepath.VolumeName(path)
	start := len(volume)
	if start > len(path) {
		start = len(path)
	}
	hasRoot := start < len(path) && pathIsSeparator(path[start])

	end := -1
	matchedSlash := true
	for i := len(path) - 1; i >= start && i >= 1; i-- {
		if pathIsSeparator(path[i]) {
			if !matchedSlash {
				end = i
				break
			}
			continue
		}
		matchedSlash = false
	}

	if end == -1 {
		if volume != "" {
			if hasRoot {
				return path[:start+1]
			}
			return volume
		}
		if hasRoot {
			return "/"
		}
		return "."
	}
	if volume != "" {
		if hasRoot && end == start {
			return path[:start+1]
		}
		return path[:end]
	}
	if hasRoot && end == 1 {
		return "//"
	}
	return path[:end]
}

// nodeBasename mirrors path.basename: the last path component without its
// trailing separators.
func nodeBasename(path string) string {
	if path == "" {
		return ""
	}
	start := len(filepath.VolumeName(path))
	if start > len(path) {
		start = len(path)
	}

	segmentStart := start
	end := -1
	matchedSlash := true
	for i := len(path) - 1; i >= start; i-- {
		if pathIsSeparator(path[i]) {
			if !matchedSlash {
				segmentStart = i + 1
				break
			}
			continue
		}
		if end == -1 {
			matchedSlash = false
			end = i + 1
		}
	}
	if end == -1 {
		return ""
	}
	return path[segmentStart:end]
}
