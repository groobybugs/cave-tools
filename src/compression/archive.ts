import { readFile, rm, writeFile, mkdir } from "fs/promises";
import { createHash } from "crypto";
import { join } from "path";
import {
  archiveDir,
  archiveStats,
  deleteArchive,
  listArchives as listDbArchives,
  recordArchive,
} from "../storage/db.js";

const ARCHIVE_THRESHOLD = 50_000;
const ARCHIVE_MAX_AGE_MS = 24 * 60 * 60 * 1000;

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
  return join(entryDir(id), `${id}.txt`);
}

function metaPath(id: string): string {
  return join(entryDir(id), `${id}.meta.json`);
}

export async function archiveIfLarge(
  output: string,
  command: string,
): Promise<{ id: string; summary: string } | null> {
  if (output.length < ARCHIVE_THRESHOLD) return null;

  const id = archiveId(output);
  const dir = entryDir(id);
  const archivedContentPath = contentPath(id);
  await mkdir(dir, { recursive: true });

  await writeFile(archivedContentPath, output, "utf-8");
  const meta: ArchiveEntry = {
    id,
    command,
    sizeChars: output.length,
    createdAt: Date.now(),
    contentPath: archivedContentPath,
  };
  recordArchive({
    id: meta.id,
    command: meta.command,
    sizeChars: meta.sizeChars,
    createdAt: meta.createdAt,
    contentPath: archivedContentPath,
  });

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
