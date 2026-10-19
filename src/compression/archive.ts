import { readFile, rm, writeFile, mkdir } from "fs/promises";
import { createHash } from "crypto";
import { join, resolve } from "path";
import {
  archiveDir,
  archiveStats,
  deleteArchive,
  listArchives as listDbArchives,
  recordArchive,
} from "../storage/db.js";

const ARCHIVE_THRESHOLD = 50_000;
const ARCHIVE_MAX_AGE_MS = 24 * 60 * 60 * 1000;
// Only archive dropped output when the drop outweighs the handle line itself.
export const HANDLE_MIN_DROPPED_CHARS = 200;

export interface ArchiveEntry {
  id: string;
  command: string;
  sizeChars: number;
  createdAt: number;
  contentPath?: string;
}

function archiveId(content: string): string {
  return createHash("sha256").update(content).digest("hex");
}

function entryDir(id: string): string {
  const prefix = id.slice(0, 2);
  return join(archiveDir(), prefix);
}

function contentPath(id: string): string {
  return resolve(entryDir(id), `${id}.txt`);
}

function metaPath(id: string): string {
  return join(entryDir(id), `${id}.meta.json`);
}

// Write output to the archive store (content-addressed by sha256) and return
// its id and absolute path. Retention is cleanupArchives().
export async function storeArchive(
  output: string,
  command: string,
): Promise<{ id: string; path: string }> {
  const id = archiveId(output);
  const archivedContentPath = contentPath(id);
  await mkdir(entryDir(id), { recursive: true });
  await writeFile(archivedContentPath, output, "utf-8");
  recordArchive({
    id,
    command,
    sizeChars: output.length,
    createdAt: Date.now(),
    contentPath: archivedContentPath,
  });
  return { id, path: archivedContentPath };
}

// When `shown` dropped more than HANDLE_MIN_DROPPED_CHARS of `full`, archive
// `full` and return a one-line handle (with leading blank line) pointing at
// it; otherwise "". Callers pass `full` already redacted when redaction is on,
// so secrets never reach disk. Archive failures never fail the tool call.
export async function archiveHandle(
  full: string,
  shown: string,
  command: string,
): Promise<string> {
  if (full.length - shown.length <= HANDLE_MIN_DROPPED_CHARS) return "";
  try {
    const { path } = await storeArchive(full, command);
    const lines = full.split("\n").length;
    return `\n\n[cave: full output ${lines} lines archived → cave__read ${path} offset/limit, or cave__grep path=${path}]`;
  } catch {
    return "";
  }
}

export async function archiveIfLarge(
  output: string,
  command: string,
): Promise<{ id: string; summary: string } | null> {
  if (output.length < ARCHIVE_THRESHOLD) return null;

  const { id } = await storeArchive(output, command);

  const lines = output.split("\n");
  const head = lines.slice(0, 20).join("\n");
  const tail = lines.slice(-10).join("\n");
  const summary = `${head}\n\n... (${lines.length - 30} lines archived) ...\n\n${tail}`;

  return { id, summary };
}

export async function expandArchive(id: string): Promise<string | null> {
  try {
    return await readFile(contentPath(id), "utf-8");
  } catch {
    return null;
  }
}

export async function listArchives(): Promise<ArchiveEntry[]> {
  try {
    return listDbArchives();
  } catch {
    return [];
  }
}

export async function cleanupArchives(maxAgeMs = ARCHIVE_MAX_AGE_MS): Promise<number> {
  const cutoff = Date.now() - maxAgeMs;
  let removed = 0;
  for (const archive of await listArchives()) {
    if (archive.createdAt >= cutoff) continue;
    try {
      await rm(archive.contentPath || contentPath(archive.id), { force: true });
      await rm(metaPath(archive.id), { force: true });
      deleteArchive(archive.id);
      removed++;
    } catch {
      try {
        deleteArchive(archive.id);
      } catch {
        // skip
      }
    }
  }
  return removed;
}

export async function getArchiveStats(): Promise<{ count: number; totalChars: number }> {
  try {
    return archiveStats();
  } catch {
    return { count: 0, totalChars: 0 };
  }
}
