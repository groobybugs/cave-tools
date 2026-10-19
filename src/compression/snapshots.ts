// Last full text content each session received for a file, so a re-read after
// a change can be answered with a diff instead of the whole file. In-memory,
// per process, LRU-bounded.

// Files larger than this are never snapshotted (diffs get diminishing returns).
export const MAX_SNAPSHOT_FILE_BYTES = 256 * 1024;
// Total bytes held across all sessions/files; least recently used evicted first.
export const MAX_SNAPSHOT_TOTAL_BYTES = 8 * 1024 * 1024;

interface Snapshot {
  hash: string;
  content: string;
  bytes: number;
}

// Map iteration order doubles as LRU order (oldest first).
const snapshots = new Map<string, Snapshot>();
let totalBytes = 0;

const keyFor = (sessionId: string, filePath: string) => `${sessionId}\0${filePath}`;

function drop(key: string): void {
  const existing = snapshots.get(key);
  if (!existing) return;
  totalBytes -= existing.bytes;
  snapshots.delete(key);
}

export function getSnapshot(sessionId: string, filePath: string): { hash: string; content: string } | undefined {
  const key = keyFor(sessionId, filePath);
  const entry = snapshots.get(key);
  if (!entry) return undefined;
  snapshots.delete(key);
  snapshots.set(key, entry);
  return entry;
}

export function rememberSnapshot(sessionId: string, filePath: string, hash: string, content: string): void {
  const key = keyFor(sessionId, filePath);
  drop(key);
  const bytes = Buffer.byteLength(content, "utf-8");
  if (bytes > MAX_SNAPSHOT_FILE_BYTES) return;
  snapshots.set(key, { hash, content, bytes });
  totalBytes += bytes;
  while (totalBytes > MAX_SNAPSHOT_TOTAL_BYTES) {
    const oldest = snapshots.keys().next().value;
    if (oldest === undefined) break;
    drop(oldest);
  }
}

export function resetSnapshots(): void {
  snapshots.clear();
  totalBytes = 0;
}

export function getSnapshotStats(): { files: number; bytes: number } {
  return { files: snapshots.size, bytes: totalBytes };
}
