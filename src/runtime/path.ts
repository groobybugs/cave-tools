import { mkdir, realpath, stat } from "fs/promises";
import path from "path";

export function toPosixPath(value: string): string {
  return value.replace(/\\/g, "/");
}

export function containsPath(parent: string, child: string): boolean {
  const relative = path.relative(parent, child);
  return relative === "" || (!path.isAbsolute(relative) && relative !== ".." && !relative.startsWith(`..${path.sep}`));
}

export async function realPathSafe(value: string): Promise<string> {
  try {
    return await realpath(value);
  } catch (error) {
    const code = (error as NodeJS.ErrnoException)?.code;
    if (code !== "ENOENT") throw error;
    return path.resolve(value);
  }
}

export async function resolveExistingDirectory(value?: string): Promise<string> {
  const resolved = path.resolve(value || ".");
  const info = await stat(resolved);
  if (!info.isDirectory()) throw new Error(`Not a directory: ${resolved}`);
  return await realpath(resolved);
}

export async function ensureParentDirectory(filePath: string): Promise<void> {
  await mkdir(path.dirname(filePath), { recursive: true });
}

export async function resolveMutationTarget(filePath: string): Promise<{ canonical: string; existed: boolean }> {
  const absolute = path.resolve(filePath);
  try {
    return { canonical: await realpath(absolute), existed: true };
  } catch (error) {
    const code = (error as NodeJS.ErrnoException)?.code;
    if (code !== "ENOENT") throw error;
    const parent = await realPathSafe(path.dirname(absolute));
    return { canonical: path.join(parent, path.basename(absolute)), existed: false };
  }
}
