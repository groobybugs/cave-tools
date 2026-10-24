// Compatibility writer for the flat text read registry consumed by the
// strict-mode built-in guards (OpenCode plugin, Claude hook, Hermes plugin).
//
// Port of src/runtime/read-registry.ts.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

const (
	maxKnownPaths = 20_000
	maxSeedBytes  = 1024 * 1024
	maxPathChars  = 4096
	maxStates     = 16
)

// orderedSet mirrors the insertion-ordered JS Set used for known paths.
type orderedSet struct {
	index map[string]struct{}
	order []string
}

func newOrderedSet() *orderedSet {
	return &orderedSet{index: map[string]struct{}{}}
}

func (s *orderedSet) has(key string) bool {
	_, ok := s.index[key]
	return ok
}

func (s *orderedSet) add(key string) {
	if _, ok := s.index[key]; ok {
		return
	}
	s.index[key] = struct{}{}
	s.order = append(s.order, key)
}

func (s *orderedSet) delete(key string) {
	if _, ok := s.index[key]; !ok {
		return
	}
	delete(s.index, key)
	for index, value := range s.order {
		if value == key {
			s.order = append(s.order[:index], s.order[index+1:]...)
			break
		}
	}
}

func (s *orderedSet) size() int {
	return len(s.index)
}

// evictOldest drops the first inserted entry still present.
func (s *orderedSet) evictOldest() {
	for len(s.order) > 0 {
		key := s.order[0]
		s.order = s.order[1:]
		if _, ok := s.index[key]; ok {
			delete(s.index, key)
			return
		}
	}
}

type pendingAppend struct {
	done chan struct{}
}

type seedRun struct {
	done chan struct{}
}

type registryState struct {
	mu      sync.Mutex
	known   *orderedSet
	pending map[string]*pendingAppend
	seed    *seedRun
}

var registryStates = struct {
	sync.Mutex
	byPath map[string]*registryState
	order  []string
}{byPath: map[string]*registryState{}}

// port of src/runtime/read-registry.ts:registryFile
func registryFile(ctx context.Context) string {
	base := GetEnv(ctx)["CLAUDE_CONFIG_DIR"]
	if base == "" {
		base = filepath.Join(homeDir(), ".claude")
	}
	return filepath.Join(base, "cave-tools", "read-registry.txt")
}

// port of src/runtime/read-registry.ts:stateFor
func stateFor(registry string) *registryState {
	registryStates.Lock()
	defer registryStates.Unlock()
	if state, ok := registryStates.byPath[registry]; ok {
		return state
	}
	state := &registryState{
		known:   newOrderedSet(),
		pending: map[string]*pendingAppend{},
	}
	registryStates.byPath[registry] = state
	registryStates.order = append(registryStates.order, registry)
	if len(registryStates.byPath) > maxStates {
		for index, key := range registryStates.order {
			if key == registry {
				continue
			}
			delete(registryStates.byPath, key)
			registryStates.order = append(registryStates.order[:index], registryStates.order[index+1:]...)
			break
		}
	}
	return state
}

// port of src/runtime/read-registry.ts:isRegistrablePath
func isRegistrablePath(filePath string) bool {
	if filePath == "" || utf16Length(filePath) > maxPathChars {
		return false
	}
	// A flat line registry cannot represent CR/LF (or NUL) in a path.
	if strings.ContainsAny(filePath, "\r\n\x00") {
		return false
	}
	// Consumers compare the exact tool argument; surrounding whitespace never
	// round-trips reliably.
	if jsre.Trim(filePath) != filePath {
		return false
	}
	return filepath.IsAbs(filePath)
}

// port of src/runtime/read-registry.ts:remember
func remember(state *registryState, filePath string) {
	state.known.add(filePath)
	if state.known.size() > maxKnownPaths {
		state.known.evictOldest()
	}
}

// port of src/runtime/read-registry.ts:seedKnown
func seedKnown(state *registryState, registry string) {
	// Existing entries may only be trusted after the container and file modes
	// are checked/tightened: a symlinked final directory is never traversed.
	if err := ensureRegistryDir(filepath.Dir(registry)); err != nil {
		return
	}

	info, err := os.Lstat(registry)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return
		}
		info = nil
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return
	}

	// O_NOFOLLOW equivalent: Lstat refused symlinks above, and SameFile below
	// rejects a path that was swapped for a symlink after the check.
	file, err := os.Open(registry)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return
	}
	if info != nil && !os.SameFile(info, stat) {
		return
	}
	_ = file.Chmod(0o600)

	// Read at most MAX_SEED_BYTES even if the file grows between stat and read.
	capBytes := stat.Size()
	if capBytes > maxSeedBytes {
		capBytes = maxSeedBytes
	}
	if capBytes <= 0 {
		return
	}
	buffer := make([]byte, capBytes)
	read := 0
	for read < len(buffer) {
		n, readErr := file.Read(buffer[read:])
		if n > 0 {
			read += n
		}
		if readErr != nil {
			break
		}
	}
	raw := DecodeUTF8Replace(buffer[:read])
	for _, line := range strings.Split(raw, "\n") {
		path := jsre.Trim(line)
		if path == "" {
			continue
		}
		state.mu.Lock()
		if !state.known.has(path) {
			state.known.add(path)
			stop := state.known.size() >= maxKnownPaths
			state.mu.Unlock()
			if stop {
				return
			}
			continue
		}
		state.mu.Unlock()
	}
}

// port of src/runtime/read-registry.ts:ensureSeeded
func ensureSeeded(state *registryState, registry string) {
	state.mu.Lock()
	run := state.seed
	if run != nil {
		state.mu.Unlock()
		<-run.done
		return
	}
	run = &seedRun{done: make(chan struct{})}
	state.seed = run
	state.mu.Unlock()

	defer close(run.done)
	seedKnown(state, registry)
}

// port of src/runtime/read-registry.ts:ensureRegistryDir
func ensureRegistryDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if goruntime.GOOS == "windows" {
		// Directory handles behave differently on Windows; verify by Lstat.
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("read registry path is not a directory: %s", dir)
		}
		return nil
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("read registry directory is not usable: %s", dir)
	}
	file, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("read registry directory is not usable: %s", dir)
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("read registry directory is not usable: %s", dir)
	}
	if !stat.IsDir() {
		return fmt.Errorf("read registry path is not a directory: %s", dir)
	}
	if !os.SameFile(info, stat) {
		return fmt.Errorf("read registry directory is not usable: %s", dir)
	}
	// mkdir is subject to umask; enforce owner-only explicitly. Failure is
	// advisory but a non-directory/symlink is refused above.
	_ = file.Chmod(0o700)
	return nil
}

// port of src/runtime/read-registry.ts:appendRegistryLine
func appendRegistryLine(registry, filePath string) error {
	if err := ensureRegistryDir(filepath.Dir(registry)); err != nil {
		return err
	}

	var previous fs.FileInfo
	info, err := os.Lstat(registry)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("read registry is not a regular file: %s", registry)
		}
		previous = info
	}

	// O_NOFOLLOW / O_APPEND equivalent: a symlink at the registry path must
	// refuse instead of writing to its target; O_APPEND lands each line at EOF.
	file, err := os.OpenFile(registry, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("read registry is not a regular file: %s", registry)
	}
	if previous != nil && !os.SameFile(previous, stat) {
		return fmt.Errorf("read registry is not a regular file: %s", registry)
	}
	_ = file.Chmod(0o600)

	// Preserve a pre-existing entry when the file has no trailing newline.
	prefix := ""
	if stat.Size() > 0 {
		last := make([]byte, 1)
		if n, readErr := file.ReadAt(last, stat.Size()-1); readErr == nil && n == 1 && last[0] != 0x0a {
			prefix = "\n"
		}
	}

	payload := []byte(prefix + filePath + "\n")
	written, err := file.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		// Never mark a partial append as complete: the caller drops the dedup
		// entry so a later read retries. The retry's missing-LF prefix logic
		// repairs the partial tail.
		return fmt.Errorf("short read registry append: %d/%d", written, len(payload))
	}
	return nil
}

// RegisterReadPathCompat appends one absolute path to the legacy flat read
// registry. It resolves only after the append attempt is complete, and never
// fails: the registry is advisory and must not fail a read.
// port of src/runtime/read-registry.ts:registerReadPathCompat
func RegisterReadPathCompat(ctx context.Context, filePath string) {
	if !isRegistrablePath(filePath) {
		return
	}
	registry := registryFile(ctx)
	state := stateFor(registry)

	state.mu.Lock()
	if pending := state.pending[filePath]; pending != nil {
		state.mu.Unlock()
		<-pending.done
		return
	}
	if state.known.has(filePath) {
		state.mu.Unlock()
		return
	}
	job := &pendingAppend{done: make(chan struct{})}
	state.pending[filePath] = job
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		delete(state.pending, filePath)
		state.mu.Unlock()
		close(job.done)
	}()

	ensureSeeded(state, registry)

	state.mu.Lock()
	if state.known.has(filePath) {
		state.mu.Unlock()
		return
	}
	remember(state, filePath)
	state.mu.Unlock()

	if err := appendRegistryLine(registry, filePath); err != nil {
		// Allow a later read to retry; never fail the read.
		state.mu.Lock()
		state.known.delete(filePath)
		state.mu.Unlock()
	}
}
