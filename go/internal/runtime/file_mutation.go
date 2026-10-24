// Per-process file mutation queue and BOM-preserving writers.
//
// Port of src/runtime/file-mutation.ts.
package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// locks is the per-process write queue. TS promises become channels: the stored
// channel is closed when the current holder releases. A lock held by the
// current call context is tracked in heldLocks instead, so nested mutations on
// an already-acquired path do not deadlock.
var locks = struct {
	sync.Mutex
	gates map[string]*lockGate
}{gates: map[string]*lockGate{}}

type lockGate struct {
	released chan struct{}
}

// heldLockToken mirrors HeldLockToken. active is atomic because Go callbacks
// may run on their own goroutines, where TS's single-threaded store could not.
type heldLockToken struct {
	key    string
	active atomic.Bool
}

// heldLockStore mirrors HeldLockStore, keeping insertion order for the error
// message (a Go map would randomize it).
type heldLockStore struct {
	order  []string
	tokens map[string]*heldLockToken
}

func (s *heldLockStore) get(key string) *heldLockToken {
	return s.tokens[key]
}

func (s *heldLockStore) keys() []string {
	return s.order
}

func (s *heldLockStore) anyActive() bool {
	for _, key := range s.order {
		if token := s.tokens[key]; token != nil && token.active.Load() {
			return true
		}
	}
	return false
}

type heldLocksKey struct{}

type lockHandle struct {
	token *heldLockToken
	gate  *lockGate
	key   string

	mu       sync.Mutex
	released bool
}

// port of src/runtime/file-mutation.ts:acquireLock
func acquireLock(key string) *lockHandle {
	locks.Lock()
	previous := locks.gates[key]
	gate := &lockGate{released: make(chan struct{})}
	locks.gates[key] = gate
	locks.Unlock()

	if previous != nil {
		<-previous.released
	}
	token := &heldLockToken{key: key}
	token.active.Store(true)
	return &lockHandle{token: token, gate: gate, key: key}
}

// port of src/runtime/file-mutation.ts:LockHandle.release
func (h *lockHandle) release() {
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return
	}
	h.released = true
	h.mu.Unlock()

	h.token.active.Store(false)
	close(h.gate.released)
	locks.Lock()
	if locks.gates[h.key] == h.gate {
		delete(locks.gates, h.key)
	}
	locks.Unlock()
}

// port of src/runtime/file-mutation.ts:liveHeldLocks
func liveHeldLocks(ctx context.Context) *heldLockStore {
	if ctx == nil {
		return nil
	}
	store, _ := ctx.Value(heldLocksKey{}).(*heldLockStore)
	if store == nil || !store.anyActive() {
		return nil
	}
	return store
}

// port of src/runtime/file-mutation.ts:reentryError
func reentryError(requested string, held *heldLockStore) error {
	return fmt.Errorf(
		"Cannot acquire file lock for %s while holding %s. "+
			"Acquire every path up front with withFileLocks([...]) before performing mutations.",
		requested, strings.Join(held.keys(), ", "),
	)
}

type bomSplit struct {
	bom  bool
	text string
}

// port of src/runtime/file-mutation.ts:splitBom
func splitBom(text string) bomSplit {
	stripped := strings.TrimLeft(text, "\uFEFF")
	return bomSplit{bom: len(stripped) != len(text), text: stripped}
}

// port of src/runtime/file-mutation.ts:joinBom
func JoinBom(text string, bom bool) string {
	stripped := splitBom(text).text
	if bom {
		return "\uFEFF" + stripped
	}
	return stripped
}

// port of src/runtime/file-mutation.ts:hasUtf8Bom
func hasUtf8Bom(content []byte) bool {
	return len(content) >= 3 && content[0] == 0xef && content[1] == 0xbb && content[2] == 0xbf
}

// port of src/runtime/file-mutation.ts:sameBytes
func SameBytes(left, right []byte) bool {
	return bytes.Equal(left, right)
}

// DecodeUtf8PreserveBom decodes UTF-8 like a fatal TextDecoder and reports
// whether the bytes started with a BOM.
// port of src/runtime/file-mutation.ts:decodeUtf8PreserveBom
func DecodeUtf8PreserveBom(content []byte) (text string, bom bool, err error) {
	bom = hasUtf8Bom(content)
	data := content
	if bom {
		data = content[3:]
	}
	if !utf8.Valid(data) {
		return "", false, errors.New("The encoded data was not valid for encoding utf-8")
	}
	return string(data), bom, nil
}

// port of src/runtime/file-mutation.ts:withLock
func withLock(ctx context.Context, key string, fn func() error) error {
	// In-process mutex only. Does not lock the file on disk.
	held := liveHeldLocks(ctx)
	if held != nil {
		if token := held.get(key); token == nil || !token.active.Load() {
			return reentryError(key, held)
		}
		return fn()
	}

	handle := acquireLock(key)
	defer handle.release()
	return fn()
}

// WithFileLocks acquires every canonical path once, sorted so opposite input
// orders share the same acquisition order. Nested calls may only re-enter for
// paths already held up front.
//
// TS is generic over the callback result; callers that only need the error use
// a struct{} result.
//
// port of src/runtime/file-mutation.ts:withFileLocks
func WithFileLocks[T any](ctx context.Context, paths []string, operation func(context.Context) (T, error)) (T, error) {
	keys := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	var zero T
	for _, filePath := range paths {
		target, err := ResolveMutationTarget(ctx, filePath)
		if err != nil {
			return zero, err
		}
		if _, ok := seen[target.Canonical]; ok {
			continue
		}
		seen[target.Canonical] = struct{}{}
		keys = append(keys, target.Canonical)
	}
	sort.Strings(keys)

	held := liveHeldLocks(ctx)
	if held != nil {
		var missing []string
		for _, key := range keys {
			if token := held.get(key); token == nil || !token.active.Load() {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return zero, reentryError(strings.Join(missing, ", "), held)
		}
		return operation(ctx)
	}

	handles := make([]*lockHandle, 0, len(keys))
	defer func() {
		for index := len(handles) - 1; index >= 0; index-- {
			handles[index].release()
		}
	}()
	for _, key := range keys {
		handles = append(handles, acquireLock(key))
	}
	store := &heldLockStore{order: keys, tokens: make(map[string]*heldLockToken, len(handles))}
	for _, handle := range handles {
		store.tokens[handle.token.key] = handle.token
	}
	return operation(context.WithValue(ctx, heldLocksKey{}, store))
}

// port of src/runtime/file-mutation.ts:fileLockQueueSize
func FileLockQueueSize() int {
	locks.Lock()
	defer locks.Unlock()
	return len(locks.gates)
}

// port of src/runtime/file-mutation.ts:writeTextPreservingBom
func WriteTextPreservingBom(ctx context.Context, filePath, content string) (MutationTarget, error) {
	target, err := ResolveMutationTarget(ctx, filePath)
	if err != nil {
		return MutationTarget{}, err
	}
	existed := false
	err = withLock(ctx, target.Canonical, func() error {
		current, readErr := os.ReadFile(target.Canonical)
		if readErr != nil {
			if !errors.Is(readErr, fs.ErrNotExist) {
				return readErr
			}
			current = nil
		}
		existed = current != nil
		next := splitBom(content)
		if err := EnsureParentDirectory(target.Canonical); err != nil {
			return err
		}
		keepBom := (current != nil && hasUtf8Bom(current)) || next.bom
		return os.WriteFile(target.Canonical, []byte(JoinBom(next.text, keepBom)), 0o666)
	})
	if err != nil {
		return MutationTarget{}, err
	}
	return MutationTarget{Canonical: target.Canonical, Existed: existed}, nil
}

// port of src/runtime/file-mutation.ts:writeIfUnchanged
func WriteIfUnchanged(ctx context.Context, filePath string, expected []byte, content string) (MutationTarget, error) {
	// Optimistic concurrency: expected must be the exact on-disk bytes from the
	// last read. The lock only orders writers in this process.
	target, err := ResolveMutationTarget(ctx, filePath)
	if err != nil {
		return MutationTarget{}, err
	}
	err = withLock(ctx, target.Canonical, func() error {
		current, readErr := os.ReadFile(target.Canonical)
		if readErr != nil {
			return readErr
		}
		if !SameBytes(current, expected) {
			return errors.New("File changed after it was read. Re-run cave__read (or cave__invalidate) and retry the edit.")
		}
		return os.WriteFile(target.Canonical, []byte(content), 0o666)
	})
	if err != nil {
		return MutationTarget{}, err
	}
	return MutationTarget{Canonical: target.Canonical, Existed: true}, nil
}

// port of src/runtime/file-mutation.ts:lockedWrite
func LockedWrite(ctx context.Context, filePath, content string) (MutationTarget, error) {
	target, err := ResolveMutationTarget(ctx, filePath)
	if err != nil {
		return MutationTarget{}, err
	}
	err = withLock(ctx, target.Canonical, func() error {
		if err := EnsureParentDirectory(target.Canonical); err != nil {
			return err
		}
		return os.WriteFile(target.Canonical, []byte(content), 0o666)
	})
	if err != nil {
		return MutationTarget{}, err
	}
	return target, nil
}
