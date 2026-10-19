import { createHash } from "crypto";
import { chmodSync, lstatSync, mkdirSync, readdirSync, readFileSync, statSync } from "fs";
import { homedir, tmpdir } from "os";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { caveToolsDataDir } from "../storage/data-dir.js";

// Socket location for the shared daemon. The key covers the build and the
// data/config dirs, so a new build or a different data dir gets its own
// daemon instead of talking to a stale one.

const distDir = join(dirname(fileURLToPath(import.meta.url)), "..");

function collectJs(dir: string, out: string[]): void {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) collectJs(full, out);
    else if (entry.name.endsWith(".js")) out.push(full);
  }
}

let cachedBuildId: string | undefined;

// Package version + name/size/mtime of every compiled .js file.
export function buildId(): string {
  if (cachedBuildId) return cachedBuildId;
  const hash = createHash("sha256");
  try {
    hash.update(JSON.parse(readFileSync(join(distDir, "..", "package.json"), "utf-8")).version ?? "");
  } catch {
    // no package.json next to dist
  }
  const files: string[] = [];
  collectJs(distDir, files);
  for (const file of files.sort()) {
    const info = statSync(file);
    hash.update(`${file}\0${info.size}\0${info.mtimeMs}\n`);
  }
  cachedBuildId = hash.digest("hex").slice(0, 16);
  return cachedBuildId;
}

function claudeConfigDir(): string {
  return process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude");
}

export function daemonKey(): string {
  return createHash("sha256")
    .update(`${buildId()}\0${caveToolsDataDir()}\0${claudeConfigDir()}`)
    .digest("hex")
    .slice(0, 12);
}

// Unix socket paths are limited to ~107 bytes (sun_path).
const MAX_SOCKET_PATH = 100;

export function socketDir(): string {
  const runtime = process.env.XDG_RUNTIME_DIR?.trim();
  const preferred = runtime ? join(runtime, "cave-tools") : join(caveToolsDataDir(), "run");
  if (join(preferred, `daemon-${daemonKey()}.sock`).length <= MAX_SOCKET_PATH) return preferred;
  const uid = typeof process.getuid === "function" ? process.getuid() : "user";
  return join(tmpdir().length < 40 ? tmpdir() : "/tmp", `cave-tools-${uid}`);
}

// Create the socket dir 0700 and refuse one we don't own or that is a symlink
// (matters for the shared /tmp fallback).
export function ensureSocketDir(): string {
  const dir = socketDir();
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const info = lstatSync(dir);
  if (info.isSymbolicLink() || !info.isDirectory()) throw new Error(`unsafe socket dir: ${dir}`);
  if (typeof process.getuid === "function" && info.uid !== process.getuid()) {
    throw new Error(`socket dir not owned by this user: ${dir}`);
  }
  if ((info.mode & 0o777) !== 0o700) chmodSync(dir, 0o700);
  return dir;
}

export function socketPath(): string {
  if (process.platform === "win32") return `\\\\.\\pipe\\cave-tools-${daemonKey()}`;
  return join(socketDir(), `daemon-${daemonKey()}.sock`);
}

export function lockPath(): string {
  return join(socketDir(), `daemon-${daemonKey()}.lock`);
}

export function daemonLogPath(): string {
  return join(caveToolsDataDir(), "daemon.log");
}

export function cliEntryPath(): string {
  return join(distDir, "cli.js");
}
