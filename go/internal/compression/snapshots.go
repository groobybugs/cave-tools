// Last full text content each session received for a file, so a re-read after
// a change can be answered with a diff instead of the whole file. In-memory,
// per process, LRU-bounded.

package compression

import (
	"container/list"
	"sync"
)

// MaxSnapshotFileBytes is the largest file that is ever snapshotted.
// port of src/compression/snapshots.ts:MAX_SNAPSHOT_FILE_BYTES
const MaxSnapshotFileBytes = 256 * 1024

// MaxSnapshotTotalBytes is the total budget across all sessions/files.
// port of src/compression/snapshots.ts:MAX_SNAPSHOT_TOTAL_BYTES
const MaxSnapshotTotalBytes = 8 * 1024 * 1024

// Snapshot is one remembered file body.
// port of src/compression/snapshots.ts:Snapshot
type Snapshot struct {
	Hash    string
	Content string
	Bytes   int64
}

// snapshotEntry is one LRU list element.
type snapshotEntry struct {
	key      string
	snapshot Snapshot
}

// Map iteration order doubles as LRU order (oldest first) in TS; Go keeps an
// explicit list with the newest entry at the back.
var (
	snapshotMu    sync.Mutex
	snapshots     = make(map[string]*list.Element)
	snapshotOrder = list.New()
	totalBytes    int64
)

// keyFor builds the composite session/file key. TS joins with NUL.
// port of src/compression/snapshots.ts:keyFor
func keyFor(sessionID string, filePath string) string {
	return sessionID + "\x00" + filePath
}

// dropSnapshot forgets one key and refunds its bytes.
// port of src/compression/snapshots.ts:drop
func dropSnapshot(key string) {
	existing, ok := snapshots[key]
	if !ok {
		return
	}
	entry := existing.Value.(*snapshotEntry)
	totalBytes -= entry.snapshot.Bytes
	snapshotOrder.Remove(existing)
	delete(snapshots, key)
}

// GetSnapshot returns the last content this session saw for filePath and marks
// it most recently used.
// port of src/compression/snapshots.ts:getSnapshot
func GetSnapshot(sessionID string, filePath string) *Snapshot {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	key := keyFor(sessionID, filePath)
	entry, ok := snapshots[key]
	if !ok {
		return nil
	}
	snapshotOrder.MoveToBack(entry)
	snapshot := entry.Value.(*snapshotEntry).snapshot
	return &snapshot
}

// RememberSnapshot stores content for a session/file unless it exceeds the
// per-file cap, evicting least recently used entries over the total cap.
// port of src/compression/snapshots.ts:rememberSnapshot
func RememberSnapshot(sessionID string, filePath string, hash string, content string) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	key := keyFor(sessionID, filePath)
	dropSnapshot(key)
	bytes := int64(len(content))
	if bytes > MaxSnapshotFileBytes {
		return
	}

	entry := snapshotOrder.PushBack(&snapshotEntry{
		key:      key,
		snapshot: Snapshot{Hash: hash, Content: content, Bytes: bytes},
	})
	snapshots[key] = entry
	totalBytes += bytes
	for totalBytes > MaxSnapshotTotalBytes {
		oldest := snapshotOrder.Front()
		if oldest == nil {
			break
		}
		dropSnapshot(oldest.Value.(*snapshotEntry).key)
	}
}

// ResetSnapshots forgets every snapshot.
// port of src/compression/snapshots.ts:resetSnapshots
func ResetSnapshots() {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snapshots = make(map[string]*list.Element)
	snapshotOrder.Init()
	totalBytes = 0
}

// GetSnapshotStats reports how many snapshots are held and their total bytes.
// port of src/compression/snapshots.ts:getSnapshotStats
func GetSnapshotStats() (files int, bytes int64) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	return len(snapshots), totalBytes
}
