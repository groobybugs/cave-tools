import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "fs";
import { createHash } from "crypto";
import { homedir } from "os";
import { join } from "path";

const ARCHIVE_THRESHOLD = 50_000;
const ARCHIVE_MAX_AGE_MS = 24 * 60 * 60 * 1000;
const ARCHIVE_DIR = join(homedir(), ".cache", "cave-tools", "archives");

export interface ArchiveEntry {
  id: string;
  command: string;
  sizeChars: number;
  createdAt: number;
}

function archiveId(content: string): string {
  return createHash("sha256").update(content).digest("hex");
}

function entryDir(id: string): string {
  const prefix = id.slice(0, 2);
  return join(ARCHIVE_DIR, prefix);
}

function contentPath(id: string): string {
  return join(entryDir(id), `${id}.txt`);
}

function metaPath(id: string): string {
  return join(entryDir(id), `${id}.meta.json`);
}

export function archiveIfLarge(
  output: string,
  command: string,
): { id: string; summary: string } | null {
  if (output.length < ARCHIVE_THRESHOLD) return null;

  const id = archiveId(output);
  const dir = entryDir(id);
  mkdirSync(dir, { recursive: true });

  writeFileSync(contentPath(id), output, "utf-8");
  const meta: ArchiveEntry = {
    id,
    command,
    sizeChars: output.length,
    createdAt: Date.now(),
  };
  writeFileSync(metaPath(id), JSON.stringify(meta), "utf-8");

  const lines = output.split("\n");
  const head = lines.slice(0, 20).join("\n");
  const tail = lines.slice(-10).join("\n");
  const summary = `${head}\n\n... (${lines.length - 30} lines archived) ...\n\n${tail}`;

  return { id, summary };
}

export function expandArchive(id: string): string | null {
  try {
    return readFileSync(contentPath(id), "utf-8");
  } catch {
    return null;
  }
}

export function listArchives(): ArchiveEntry[] {
  const entries: ArchiveEntry[] = [];
  try {
    for (const prefix of readdirSync(ARCHIVE_DIR)) {
      const prefixDir = join(ARCHIVE_DIR, prefix);
      for (const file of readdirSync(prefixDir)) {
        if (!file.endsWith(".meta.json")) continue;
        try {
          const meta = JSON.parse(
            readFileSync(join(prefixDir, file), "utf-8"),
          ) as ArchiveEntry;
          entries.push(meta);
        } catch {
          // skip corrupt meta
        }
      }
    }
  } catch {
    // archive dir may not exist
  }
  return entries.sort((a, b) => b.createdAt - a.createdAt);
}

export function cleanupArchives(maxAgeMs = ARCHIVE_MAX_AGE_MS): number {
  const cutoff = Date.now() - maxAgeMs;
  let removed = 0;
  try {
    for (const prefix of readdirSync(ARCHIVE_DIR)) {
      const prefixDir = join(ARCHIVE_DIR, prefix);
      for (const file of readdirSync(prefixDir)) {
        const path = join(prefixDir, file);
        try {
          const stat = statSync(path);
          if (stat.mtimeMs < cutoff) {
            rmSync(path, { force: true });
            removed++;
          }
        } catch {
          // skip
        }
      }
    }
  } catch {
    // archive dir may not exist
  }
  return removed;
}

export function getArchiveStats(): { count: number; totalChars: number } {
  const archives = listArchives();
  return {
    count: archives.length,
    totalChars: archives.reduce((sum, a) => sum + a.sizeChars, 0),
  };
}
