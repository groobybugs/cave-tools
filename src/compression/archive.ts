import { readdir, readFile, rm, stat, writeFile, mkdir } from "fs/promises";
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

export async function archiveIfLarge(
  output: string,
  command: string,
): Promise<{ id: string; summary: string } | null> {
  if (output.length < ARCHIVE_THRESHOLD) return null;

  const id = archiveId(output);
  const dir = entryDir(id);
  await mkdir(dir, { recursive: true });

  await writeFile(contentPath(id), output, "utf-8");
  const meta: ArchiveEntry = {
    id,
    command,
    sizeChars: output.length,
    createdAt: Date.now(),
  };
  await writeFile(metaPath(id), JSON.stringify(meta), "utf-8");

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
  const entries: ArchiveEntry[] = [];
  try {
    for (const prefix of await readdir(ARCHIVE_DIR)) {
      const prefixDir = join(ARCHIVE_DIR, prefix);
      for (const file of await readdir(prefixDir)) {
        if (!file.endsWith(".meta.json")) continue;
        try {
          const meta = JSON.parse(
            await readFile(join(prefixDir, file), "utf-8"),
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

export async function cleanupArchives(maxAgeMs = ARCHIVE_MAX_AGE_MS): Promise<number> {
  const cutoff = Date.now() - maxAgeMs;
  let removed = 0;
  try {
    for (const prefix of await readdir(ARCHIVE_DIR)) {
      const prefixDir = join(ARCHIVE_DIR, prefix);
      for (const file of await readdir(prefixDir)) {
        const filePath = join(prefixDir, file);
        try {
          const s = await stat(filePath);
          if (s.mtimeMs < cutoff) {
            await rm(filePath, { force: true });
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

export async function getArchiveStats(): Promise<{ count: number; totalChars: number }> {
  const archives = await listArchives();
  return {
    count: archives.length,
    totalChars: archives.reduce((sum, a) => sum + a.sizeChars, 0),
  };
}
