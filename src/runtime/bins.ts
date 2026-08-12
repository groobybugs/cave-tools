import { spawn } from "child_process";
import { createHash } from "crypto";
import { existsSync } from "fs";
import { access, chmod, copyFile, mkdir, mkdtemp, readdir, rename, rm, writeFile } from "fs/promises";
import { homedir, tmpdir } from "os";
import path from "path";
import { constants } from "fs";

export const RG_VERSION = "15.1.0";
export const FD_VERSION = "10.3.0";

export type SearchBin = "rg" | "fd";

interface ArchiveSpec {
  triple: string;
  extension: "tar.gz" | "zip";
  sha256: string;
}

const RG_ARCHIVES: Record<string, ArchiveSpec> = {
  "x64-linux": {
    triple: "x86_64-unknown-linux-musl",
    extension: "tar.gz",
    sha256: "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599",
  },
  "arm64-linux": {
    triple: "aarch64-unknown-linux-gnu",
    extension: "tar.gz",
    sha256: "2b661c6ef508e902f388e9098d9c4c5aca72c87b55922d94abdba830b4dc885e",
  },
  "x64-darwin": {
    triple: "x86_64-apple-darwin",
    extension: "tar.gz",
    sha256: "64811cb24e77cac3057d6c40b63ac9becf9082eedd54ca411b475b755d334882",
  },
  "arm64-darwin": {
    triple: "aarch64-apple-darwin",
    extension: "tar.gz",
    sha256: "378e973289176ca0c6054054ee7f631a065874a352bf43f0fa60ef079b6ba715",
  },
  "x64-win32": {
    triple: "x86_64-pc-windows-msvc",
    extension: "zip",
    sha256: "124510b94b6baa3380d051fdf4650eaa80a302c876d611e9dba0b2e18d87493a",
  },
  "arm64-win32": {
    triple: "aarch64-pc-windows-msvc",
    extension: "zip",
    sha256: "00d931fb5237c9696ca49308818edb76d8eb6fc132761cb2a1bd616b2df02f8e",
  },
  "ia32-win32": {
    triple: "i686-pc-windows-msvc",
    extension: "zip",
    sha256: "725be85a1e8f92878a548f40ee4f6df64bc93b809586462b3c6d884e1de1e83a",
  },
};

const FD_ARCHIVES: Record<string, ArchiveSpec> = {
  "x64-linux": {
    triple: "x86_64-unknown-linux-musl",
    extension: "tar.gz",
    sha256: "2b6bfaae8c48f12050813c2ffe1884c61ea26e750d803df9c9114550a314cd14",
  },
  "arm64-linux": {
    triple: "aarch64-unknown-linux-musl",
    extension: "tar.gz",
    sha256: "996b9b1366433b211cb3bbedba91c9dbce2431842144d925428ead0adf32020b",
  },
  "x64-darwin": {
    triple: "x86_64-apple-darwin",
    extension: "tar.gz",
    sha256: "50d30f13fe3d5914b14c4fff5abcbd4d0cdab4b855970a6956f4f006c17117a3",
  },
  "arm64-darwin": {
    triple: "aarch64-apple-darwin",
    extension: "tar.gz",
    sha256: "0570263812089120bc2a5d84f9e65cd0c25e4a4d724c80075c357239c74ae904",
  },
  "x64-win32": {
    triple: "x86_64-pc-windows-msvc",
    extension: "zip",
    sha256: "318aa2a6fa664325933e81fda60d523fff29444129e91ebf0726b5b3bcd8b059",
  },
  "arm64-win32": {
    triple: "aarch64-pc-windows-msvc",
    extension: "zip",
    sha256: "bf9b1e31bcac71c1e95d49c56f0d872f525b95d03854e94b1d4dd6786f825cc5",
  },
};

export interface EnsureBinsOptions {
  log?: (message: string) => void;
}

export interface BinResolution {
  rg: string | null;
  fd: string | null;
}

function platformKey(): string {
  return `${process.arch}-${process.platform}`;
}

function exeName(tool: SearchBin): string {
  return process.platform === "win32" ? `${tool}.exe` : tool;
}

export function binDir(): string {
  if (process.platform === "win32") {
    const root = process.env.LOCALAPPDATA || path.join(homedir(), "AppData", "Local");
    return path.join(root, "cave-tools", "bin");
  }
  return path.join(homedir(), ".local", "share", "cave-tools", "bin");
}

export function cachedBinPath(tool: SearchBin): string {
  return path.join(binDir(), exeName(tool));
}

async function isExecutable(filePath: string): Promise<boolean> {
  try {
    await access(filePath, process.platform === "win32" ? constants.F_OK : constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

export function whichOnPath(names: string[]): string | null {
  const pathEnv = process.env.PATH || "";
  const dirs = pathEnv.split(path.delimiter).filter(Boolean);
  const winExts = process.platform === "win32" ? ["", ".exe", ".cmd"] : [""];
  for (const name of names) {
    if (path.isAbsolute(name) && existsSync(name)) return name;
    for (const dir of dirs) {
      for (const ext of winExts) {
        if (ext && name.toLowerCase().endsWith(ext)) continue;
        const candidate = path.join(dir, name + ext);
        if (existsSync(candidate)) return candidate;
      }
    }
  }
  return null;
}

export function whichUserRg(): string | null {
  return whichOnPath(process.platform === "win32" ? ["rg.exe", "rg"] : ["rg"]);
}

export function whichUserFd(): string | null {
  return whichOnPath(
    process.platform === "win32" ? ["fd.exe", "fd"] : ["fd", "fdfind"],
  );
}

function run(command: string, args: string[]): Promise<{ code: number | null; stderr: string }> {
  return new Promise((resolve) => {
    const child = spawn(command, args, { stdio: ["ignore", "ignore", "pipe"], windowsHide: true });
    let stderr = "";
    child.stderr?.on("data", (chunk: Buffer) => {
      stderr += chunk.toString("utf-8");
    });
    child.on("error", (error) => resolve({ code: null, stderr: error.message }));
    child.on("close", (code) => resolve({ code, stderr }));
  });
}

async function download(url: string): Promise<Buffer> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`download failed ${response.status}: ${url}`);
  const bytes = Buffer.from(await response.arrayBuffer());
  if (bytes.byteLength === 0) throw new Error(`empty download: ${url}`);
  return bytes;
}

function sha256(buf: Buffer): string {
  return createHash("sha256").update(buf).digest("hex");
}

async function extractArchive(archivePath: string, destDir: string, extension: "tar.gz" | "zip"): Promise<void> {
  if (extension === "tar.gz") {
    const result = await run("tar", ["-xzf", archivePath, "-C", destDir]);
    if (result.code !== 0) throw new Error(result.stderr.trim() || "tar extract failed");
    return;
  }
  const tarResult = await run("tar", ["-xf", archivePath, "-C", destDir]);
  if (tarResult.code === 0) return;
  if (process.platform === "win32") {
    const shell = whichOnPath(["powershell.exe", "pwsh.exe"]) || "powershell.exe";
    const ps = await run(shell, [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      `$global:ProgressPreference = 'SilentlyContinue'; Expand-Archive -LiteralPath '${archivePath.replaceAll("'", "''")}' -DestinationPath '${destDir.replaceAll("'", "''")}' -Force`,
    ]);
    if (ps.code !== 0) throw new Error(ps.stderr.trim() || "zip extract failed");
    return;
  }
  throw new Error(tarResult.stderr.trim() || "zip extract failed");
}

async function findExtractedBinary(root: string, tool: SearchBin): Promise<string> {
  const want = exeName(tool);
  const stack = [root];
  while (stack.length) {
    const dir = stack.pop()!;
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        stack.push(full);
        continue;
      }
      if (entry.isFile() && entry.name === want) return full;
    }
  }
  throw new Error(`archive did not contain ${want}`);
}

async function installFromGithub(tool: SearchBin, log?: (message: string) => void): Promise<string | null> {
  const spec = (tool === "rg" ? RG_ARCHIVES : FD_ARCHIVES)[platformKey()];
  if (!spec || !spec.sha256) return null;
  const filename =
    tool === "rg"
      ? `ripgrep-${RG_VERSION}-${spec.triple}.${spec.extension}`
      : `fd-v${FD_VERSION}-${spec.triple}.${spec.extension}`;
  const url =
    tool === "rg"
      ? `https://github.com/BurntSushi/ripgrep/releases/download/${RG_VERSION}/${filename}`
      : `https://github.com/sharkdp/fd/releases/download/v${FD_VERSION}/${filename}`;

  log?.(`downloading ${tool} ${tool === "rg" ? RG_VERSION : FD_VERSION} → ${filename}`);
  const bytes = await download(url);
  const digest = sha256(bytes);
  if (digest !== spec.sha256) {
    throw new Error(`${tool} checksum mismatch: got ${digest}, expected ${spec.sha256}`);
  }

  const work = await mkdtemp(path.join(tmpdir(), `cave-${tool}-`));
  const archivePath = path.join(work, filename);
  try {
    await writeFile(archivePath, bytes);
    await extractArchive(archivePath, work, spec.extension);
    const extracted = await findExtractedBinary(work, tool);
    const destDir = binDir();
    await mkdir(destDir, { recursive: true });
    const dest = cachedBinPath(tool);
    const tmpDest = `${dest}.${process.pid}.tmp`;
    await copyFile(extracted, tmpDest);
    if (process.platform !== "win32") await chmod(tmpDest, 0o755);
    await rename(tmpDest, dest);
    log?.(`installed: ${dest}`);
    return dest;
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

async function resolveTool(tool: SearchBin, userPath: string | null, log?: (message: string) => void): Promise<string | null> {
  if (userPath) return userPath;
  const cached = cachedBinPath(tool);
  if (await isExecutable(cached)) return cached;
  try {
    return await installFromGithub(tool, log);
  } catch (error) {
    log?.(`${tool} download failed: ${error instanceof Error ? error.message : String(error)}`);
    return null;
  }
}

let pending: Promise<BinResolution> | null = null;

export async function ensureBins(options: EnsureBinsOptions = {}): Promise<BinResolution> {
  if (!pending) {
    pending = (async () => {
      const rg = await resolveTool("rg", whichUserRg(), options.log);
      const fd = await resolveTool("fd", whichUserFd(), options.log);
      return { rg, fd };
    })();
  }
  return pending;
}

export async function resolveRg(log?: (message: string) => void): Promise<string | null> {
  return (await ensureBins({ log })).rg;
}

export async function resolveFd(log?: (message: string) => void): Promise<string | null> {
  return (await ensureBins({ log })).fd;
}

export function resetBinsCache(): void {
  pending = null;
}
