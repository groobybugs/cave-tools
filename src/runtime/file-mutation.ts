import { readFile, writeFile } from "fs/promises";
import { ensureParentDirectory, resolveMutationTarget } from "./path.js";

// Per-process write queue only. Cross-process MCP writers are not serialized
// (no flock/fcntl — too easy to deadlock or leave stale locks). Concurrent
// processes rely on writeIfUnchanged's expected-bytes check; a mismatch throws
// so the caller re-reads and cave__invalidate's the stale cache.
const locks = new Map<string, Promise<void>>();

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
  const previous = locks.get(key) ?? Promise.resolve();
  let release!: () => void;
  const next = new Promise<void>((resolve) => {
    release = resolve;
  });
  locks.set(key, previous.then(() => next, () => next));
  await previous.catch(() => undefined);
  try {
    return await fn();
  } finally {
    release();
    if (locks.get(key) === next) locks.delete(key);
  }
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
