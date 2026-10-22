import { readFile, writeFile } from "fs/promises";
import { AsyncLocalStorage } from "async_hooks";
import { ensureParentDirectory, resolveMutationTarget } from "./path.js";

// Per-process write queue only. Cross-process MCP writers are not serialized
// (no flock/fcntl — too easy to deadlock or leave stale locks). Concurrent
// processes rely on writeIfUnchanged's expected-bytes check; a mismatch throws
// so the caller re-reads and cave__invalidate's the stale cache.
const locks = new Map<string, Promise<void>>();

// Locks held by the current async context. Tokens are marked inactive when
// released, so a callback scheduled inside withFileLocks that runs after the
// outer operation finishes cannot keep bypassing the queue: an all-inactive
// store is treated as stale instead of granted.
interface HeldLockToken {
  readonly key: string;
  active: boolean;
}

type HeldLockStore = Map<string, HeldLockToken>;

const heldLocks = new AsyncLocalStorage<HeldLockStore>();

interface LockHandle {
  token: HeldLockToken;
  release(): void;
}

async function acquireLock(key: string): Promise<LockHandle> {
  const previous = locks.get(key) ?? Promise.resolve();
  let open!: () => void;
  const gate = new Promise<void>((resolve) => {
    open = resolve;
  });
  // `locks` stores the chained promise, not `gate`. Cleanup must compare
  // against this exact promise or the entry is never removed.
  const queued = previous.then(() => gate, () => gate);
  locks.set(key, queued);
  await previous.catch(() => undefined);

  const token: HeldLockToken = { key, active: true };
  let released = false;
  return {
    token,
    release() {
      if (released) return;
      released = true;
      token.active = false;
      open();
      if (locks.get(key) === queued) locks.delete(key);
    },
  };
}

function liveHeldLocks(): HeldLockStore | undefined {
  const store = heldLocks.getStore();
  if (!store) return undefined;
  for (const token of store.values()) {
    if (token.active) return store;
  }
  return undefined;
}

function reentryError(requested: string, held: HeldLockStore): Error {
  return new Error(
    `Cannot acquire file lock for ${requested} while holding ${[...held.keys()].join(", ")}. ` +
      "Acquire every path up front with withFileLocks([...]) before performing mutations.",
  );
}

function splitBom(text: string): { bom: boolean; text: string } {
  const stripped = text.replace(/^\uFEFF+/, "");
  return { bom: stripped.length !== text.length, text: stripped };
}

function joinBom(text: string, bom: boolean): string {
  const stripped = splitBom(text).text;
  return bom ? `\uFEFF${stripped}` : stripped;
}

export { joinBom };

function hasUtf8Bom(content: Uint8Array): boolean {
  return content[0] === 0xef && content[1] === 0xbb && content[2] === 0xbf;
}

export function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  return left.length === right.length && left.every((byte, index) => byte === right[index]);
}

export function decodeUtf8PreserveBom(content: Uint8Array): { text: string; bom: boolean } {
  const bom = hasUtf8Bom(content);
  const text = new TextDecoder("utf-8", { fatal: true }).decode(bom ? content.slice(3) : content);
  return { text, bom };
}

async function withLock<T>(key: string, fn: () => Promise<T>): Promise<T> {
  // In-process mutex only. Does not lock the file on disk.
  const held = liveHeldLocks();
  if (held) {
    if (!held.get(key)?.active) throw reentryError(key, held);
    return fn();
  }

  const handle = await acquireLock(key);
  try {
    return await heldLocks.run(new Map([[key, handle.token]]), fn);
  } finally {
    handle.release();
  }
}

// Acquire every canonical path once, sorted so opposite input orders share the
// same acquisition order. Nested calls may only re-enter for paths already held
// up front; acquiring an additional path while holding one is rejected because
// it can deadlock against a call that acquired them in a different order.
export async function withFileLocks<T>(
  paths: readonly string[],
  operation: () => Promise<T>,
): Promise<T> {
  const targets = await Promise.all(paths.map((filePath) => resolveMutationTarget(filePath)));
  const keys = [...new Set(targets.map((target) => target.canonical))].sort();

  const held = liveHeldLocks();
  if (held) {
    const missing = keys.filter((key) => !held.get(key)?.active);
    if (missing.length > 0) throw reentryError(missing.join(", "), held);
    return operation();
  }

  const handles: LockHandle[] = [];
  try {
    for (const key of keys) handles.push(await acquireLock(key));
    const store: HeldLockStore = new Map(
      handles.map((handle) => [handle.token.key, handle.token]),
    );
    return await heldLocks.run(store, operation);
  } finally {
    for (let index = handles.length - 1; index >= 0; index -= 1) handles[index].release();
  }
}

/** Diagnostics/test hook: number of lock keys currently held or queued. */
export function fileLockQueueSize(): number {
  return locks.size;
}

export async function writeTextPreservingBom(filePath: string, content: string): Promise<{ canonical: string; existed: boolean }> {
  const target = await resolveMutationTarget(filePath);
  return withLock(target.canonical, async () => {
    let current: Buffer | undefined;
    try {
      current = await readFile(target.canonical);
    } catch (error) {
      if ((error as NodeJS.ErrnoException)?.code !== "ENOENT") throw error;
    }
    const next = splitBom(content);
    await ensureParentDirectory(target.canonical);
    await writeFile(target.canonical, joinBom(next.text, Boolean(current && hasUtf8Bom(current)) || next.bom), "utf-8");
    return { ...target, existed: current !== undefined };
  });
}

export async function writeIfUnchanged(
  filePath: string,
  expected: Uint8Array,
  content: string,
): Promise<{ canonical: string; existed: boolean }> {
  // Optimistic concurrency: `expected` must be the exact on-disk bytes from
  // the last read. withLock only orders writers in this process.
  const target = await resolveMutationTarget(filePath);
  return withLock(target.canonical, async () => {
    const current = await readFile(target.canonical);
    if (!sameBytes(current, expected)) {
      throw new Error(
        "File changed after it was read. Re-run cave__read (or cave__invalidate) and retry the edit.",
      );
    }
    await writeFile(target.canonical, content, "utf-8");
    return { ...target, existed: true };
  });
}

export async function lockedWrite(filePath: string, content: string): Promise<{ canonical: string; existed: boolean }> {
  const target = await resolveMutationTarget(filePath);
  return withLock(target.canonical, async () => {
    await ensureParentDirectory(target.canonical);
    await writeFile(target.canonical, content, "utf-8");
    return target;
  });
}
