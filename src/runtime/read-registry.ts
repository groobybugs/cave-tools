import { constants } from "fs";
import { lstat, mkdir, open } from "fs/promises";
import { homedir } from "os";
import { dirname, isAbsolute, join } from "path";
import { getEnv } from "./context.js";

// Compatibility writer for the flat text registry still consumed by the
// strict-mode built-in guards:
//   OpenCode  src/plugins/opencode/plugin.js  (wasReadViaCave)
//   Claude    claude/hooks/cave-tools-redirect.sh (grep -qxF)
//   Hermes    src/plugins/hermes/__init__.py  (_was_read_via_cave)
//
// SQLite owns the canonical read registry now (db.registerReadPath), but those
// guards cannot see it. This module restores the legacy append contract
// without putting SQLite on the read critical path.
//
// Contract with consumers: one absolute path per line, deduplicated, flat and
// shared across sessions. The file may be appended by concurrent MCP
// processes. We therefore only ever append (never rewrite) so unrelated
// entries can never be truncated. O_APPEND writes of a single short line keep
// concurrent appends from interleaving on local filesystems.
//
// Dedup is best-effort: each process seeds an in-memory Set once, then skips
// known paths without touching the file. A cross-process race can still
// append the same path twice; consumers use line matching, so duplicates are
// harmless. Seeding is byte-capped and the Set is entry-capped so a hostile
// or accidentally huge registry cannot consume unbounded memory.

const MAX_KNOWN_PATHS = 20_000;
const MAX_SEED_BYTES = 1024 * 1024;
const MAX_PATH_CHARS = 4096;
const MAX_STATES = 16;

interface RegistryState {
  known: Set<string>;
  pending: Map<string, Promise<void>>;
  seedPromise: Promise<void> | null;
}

const states = new Map<string, RegistryState>();

function registryFile(): string {
  const env = getEnv();
  return join(env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude"), "cave-tools", "read-registry.txt");
}

function stateFor(registry: string): RegistryState {
  let state = states.get(registry);
  if (!state) {
    state = { known: new Set(), pending: new Map(), seedPromise: null };
    states.set(registry, state);
    if (states.size > MAX_STATES) {
      const oldest = states.keys().next().value;
      if (oldest !== undefined && oldest !== registry) states.delete(oldest);
    }
  }
  return state;
}

function isRegistrablePath(filePath: unknown): filePath is string {
  if (typeof filePath !== "string" || filePath.length === 0 || filePath.length > MAX_PATH_CHARS) {
    return false;
  }
  // A flat line registry cannot represent CR/LF (or NUL) in a path. Reject
  // rather than writing a line that splits into bogus paths.
  if (/[\r\n\0]/.test(filePath)) return false;
  // Consumers compare the exact tool argument; leading/trailing whitespace is
  // trimmed by some readers and can never round-trip reliably.
  if (filePath.trim() !== filePath) return false;
  return isAbsolute(filePath);
}

function remember(state: RegistryState, filePath: string): void {
  state.known.add(filePath);
  if (state.known.size > MAX_KNOWN_PATHS) {
    const oldest = state.known.values().next().value;
    if (oldest !== undefined) state.known.delete(oldest);
  }
}

async function seedKnown(state: RegistryState, registry: string): Promise<void> {
  // Existing entries may only be trusted after the registry container and
  // file modes are checked/tightened. In particular, a symlinked final
  // `cave-tools` directory must not be traversed just to seed dedup state.
  try {
    await ensureRegistryDir(dirname(registry));
  } catch {
    return;
  }

  try {
    const existing = await lstat(registry);
    if (existing.isSymbolicLink() || !existing.isFile()) return;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") return;
  }

  let handle;
  try {
    // O_NOFOLLOW: never read through a symlinked registry (consumers also
    // refuse symlinks).
    handle = await open(registry, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
  } catch {
    return;
  }
  try {
    const st = await handle.stat();
    if (!st.isFile()) return;
    await handle.chmod(0o600).catch(() => {});

    // Read at most MAX_SEED_BYTES even if the file grows between stat and
    // read: the advertised seed cap must bound memory and work, not merely
    // reject oversized snapshots.
    const cap = Math.min(st.size, MAX_SEED_BYTES);
    if (cap <= 0) return;
    const buffer = Buffer.alloc(cap);
    let read = 0;
    while (read < cap) {
      const { bytesRead } = await handle.read(buffer, read, cap - read, read);
      if (bytesRead <= 0) break;
      read += bytesRead;
    }
    const raw = buffer.subarray(0, read).toString("utf8");
    for (const line of raw.split("\n")) {
      const path = line.trim();
      if (!path) continue;
      if (!state.known.has(path)) {
        state.known.add(path);
        if (state.known.size >= MAX_KNOWN_PATHS) break;
      }
    }
  } catch {
    // Advisory; an unreadable registry only costs duplicate detection.
  } finally {
    await handle.close().catch(() => {});
  }
}

function ensureSeeded(state: RegistryState, registry: string): Promise<void> {
  if (!state.seedPromise) state.seedPromise = seedKnown(state, registry);
  return state.seedPromise;
}

async function ensureRegistryDir(dir: string): Promise<void> {
  await mkdir(dir, { recursive: true, mode: 0o700 });
  if (process.platform === "win32") {
    // Directory handles behave differently on Windows; verify without opening.
    const st = await lstat(dir);
    if (!st.isDirectory() || st.isSymbolicLink()) {
      throw new Error(`read registry path is not a directory: ${dir}`);
    }
    return;
  }
  const nofollow = constants.O_NOFOLLOW ?? 0;
  let handle;
  try {
    handle = await open(
      dir,
      constants.O_RDONLY | (constants.O_DIRECTORY ?? 0) | nofollow,
    );
  } catch {
    throw new Error(`read registry directory is not usable: ${dir}`);
  }
  try {
    const st = await handle.stat();
    if (!st.isDirectory()) throw new Error(`read registry path is not a directory: ${dir}`);
    // mkdir is subject to umask; enforce owner-only explicitly. Failure is
    // advisory (e.g. Windows) but a non-directory/symlink is refused above.
    await handle.chmod(0o700).catch(() => {});
  } finally {
    await handle.close().catch(() => {});
  }
}

async function appendRegistryLine(registry: string, filePath: string): Promise<void> {
  await ensureRegistryDir(dirname(registry));

  // O_NOFOLLOW: a symlink at the registry path refuses with ELOOP instead of
  // writing to its target. O_APPEND makes each line write land at EOF even
  // when other processes append concurrently.
  const nofollow = constants.O_NOFOLLOW ?? 0;
  try {
    const existing = await lstat(registry);
    if (existing.isSymbolicLink() || !existing.isFile()) {
      throw new Error(`read registry is not a regular file: ${registry}`);
    }
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
  const handle = await open(
    registry,
    constants.O_RDWR | constants.O_APPEND | constants.O_CREAT | nofollow,
    0o600,
  );
  try {
    const st = await handle.stat();
    if (!st.isFile()) throw new Error(`read registry is not a regular file: ${registry}`);
    await handle.chmod(0o600).catch(() => {});

    // Preserve a pre-existing entry when the file has no trailing newline.
    // Read the byte before writing so the new path cannot be glued onto it.
    let prefix = "";
    if (st.size > 0) {
      const last = Buffer.alloc(1);
      const { bytesRead } = await handle.read(last, 0, 1, st.size - 1);
      if (bytesRead === 1 && last[0] !== 0x0a) prefix = "\n";
    }

    const payload = Buffer.from(`${prefix}${filePath}\n`, "utf8");
    const { bytesWritten } = await handle.write(payload, 0, payload.length, null);
    if (bytesWritten !== payload.length) {
      // Never mark a partial append as complete: the caller drops the dedup
      // entry so a later read retries. The partial tail is left in place (no
      // truncation); the retry's missing-LF prefix logic repairs it.
      throw new Error(`short read registry append: ${bytesWritten}/${payload.length}`);
    }
  } finally {
    await handle.close().catch(() => {});
  }
}

/**
 * Append one absolute path to the legacy flat read registry. Resolves only
 * after the append attempt is complete (or after a best-effort failure), so a
 * successful cave__read is a deterministic barrier for an immediately
 * following strict-mode built-in edit check.
 *
 * Never rejects: the registry is advisory and must not fail a read.
 */
export async function registerReadPathCompat(filePath: string): Promise<void> {
  try {
    if (!isRegistrablePath(filePath)) return;
    const path = filePath;
    const registry = registryFile();
    const state = stateFor(registry);

    // Coalesce concurrent registrations of the same path in this process:
    // every caller awaits the same append instead of returning before it is
    // visible.
    const inFlight = state.pending.get(path);
    if (inFlight) {
      await inFlight;
      return;
    }
    if (state.known.has(path)) return;

    const job = (async () => {
      await ensureSeeded(state, registry);
      if (state.known.has(path)) return;
      remember(state, path);
      try {
        await appendRegistryLine(registry, path);
      } catch {
        // Allow a later read to retry; never fail the read.
        state.known.delete(path);
      }
    })();

    state.pending.set(path, job);
    try {
      await job;
    } finally {
      state.pending.delete(path);
    }
  } catch {
    // The registry is advisory: no filesystem, env, or state failure may
    // escape into a read.
    return;
  }
}
