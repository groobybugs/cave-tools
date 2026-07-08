import { spawn } from "child_process";

export interface RunCommandOptions {
  cwd?: string;
  timeout: number;
  shell?: string;
  /** @deprecated use maxBytes. Kept as alias for back-compat. */
  maxCaptureBytes?: number;
  /** Max bytes retained in memory per stream (stdout, stderr). Default 100KB. */
  maxBytes?: number;
  /** Max lines retained in the final tail per stream. Default 2000. */
  maxLines?: number;
  forceKillAfterMs?: number;
}

export interface RunCommandResult {
  stdout: string;
  stderr: string;
  output: string;
  exitCode: number | null;
  signal: NodeJS.Signals | null;
  timedOut: boolean;
  stdoutTruncated: boolean;
  stderrTruncated: boolean;
  /** Present when output exceeded the in-memory budget and was spilled to disk.
   * The returned stdout/stderr are the bounded tail; the file holds the full record. */
  stdoutPath?: string;
  stderrPath?: string;
}

export const defaultShell = (): string =>
  process.platform === "win32" ? (process.env.COMSPEC ?? "cmd.exe") : "/bin/sh";

export function compactOutput(stdout: string, stderr: string): string {
  if (stdout && stderr) return `${stdout}\n\nstderr:\n${stderr}`;
  if (stderr) return `stderr:\n${stderr}`;
  return stdout || "(no output)";
}

export function captureNotice(stdoutTruncated: boolean, stderrTruncated: boolean): string | undefined {
  if (stdoutTruncated && stderrTruncated) return "[stdout and stderr capture truncated at the in-memory safety limit]";
  if (stdoutTruncated) return "[stdout capture truncated at the in-memory safety limit]";
  if (stderrTruncated) return "[stderr capture truncated at the in-memory safety limit]";
  return undefined;
}

// ── Bounded ring buffer ────────────────────────────────────────────────────
// Accumulates chunks in memory up to `keep = maxBytes * 2`, shifting oldest
// out when over budget so memory stays bounded regardless of output size.
// The final string is UTF-8-boundary safe via tailUtf8Safe.
class BoundedCapture {
  private chunks: Buffer[] = [];
  private used = 0;
  private readonly keep: number;
  cut = false;

  constructor(readonly maxBytes: number, readonly maxLines: number) {
    this.keep = maxBytes * 2;
  }

  push(chunk: Buffer): void {
    // A single chunk larger than the keep budget: keep only its tail. Prevents
    // a single huge `data` event from blowing memory before the ring can shift.
    if (chunk.byteLength > this.keep) {
      chunk = chunk.subarray(chunk.byteLength - this.keep);
      this.cut = true;
    }
    this.chunks.push(chunk);
    this.used += chunk.byteLength;
    while (this.used > this.keep) {
      const dropped = this.chunks.shift();
      if (dropped) this.used -= dropped.byteLength;
      this.cut = true;
    }
  }

  finalize(): { text: string; truncated: boolean } {
    const raw = Buffer.concat(this.chunks).toString("utf-8");
    const text = tailUtf8Safe(raw, this.maxLines, this.maxBytes);
    return { text, truncated: this.cut };
  }
}

// UTF-8-boundary-safe tail: keep the last `maxLines` lines, capped at
// `maxBytes`. Walks back to a char boundary if a byte-slice would split a
// multibyte sequence.
function tailUtf8Safe(raw: string, maxLines: number, maxBytes: number): string {
  let lines = raw;
  if (maxLines > 0) {
    const all = raw.split("\n");
    if (all.length > maxLines) {
      lines = all.slice(all.length - maxLines).join("\n");
    }
  }
  if (Buffer.byteLength(lines, "utf-8") <= maxBytes) return lines;
  const buf = Buffer.from(lines, "utf-8");
  const end = buf.length;
  let start = Math.max(0, end - maxBytes);
  while (start < end && (buf[start] & 0xc0) === 0x80) start++;
  return buf.subarray(start, end).toString("utf-8");
}

export function runCommand(command: string, options: RunCommandOptions): Promise<RunCommandResult> {
  return new Promise((resolve, reject) => {
    const detached = process.platform !== "win32";
    const proc = spawn(command, [], {
      shell: options.shell ?? defaultShell(),
      cwd: options.cwd,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
      detached,
      windowsHide: process.platform === "win32",
    });

    const maxBytes = options.maxBytes ?? options.maxCaptureBytes ?? 100 * 1024;
    const maxLines = options.maxLines ?? 2000;
    const stdoutCap = new BoundedCapture(maxBytes, maxLines);
    const stderrCap = new BoundedCapture(maxBytes, maxLines);
    let timedOut = false;
    let settled = false;
    let forceTimer: NodeJS.Timeout | undefined;

    const kill = (signal: NodeJS.Signals) => {
      try {
        if (detached && proc.pid !== undefined) process.kill(-proc.pid, signal);
        else proc.kill(signal);
      } catch {
        // Process already exited.
      }
    };

    proc.stdout?.on("data", (chunk: Buffer) => stdoutCap.push(chunk));
    proc.stderr?.on("data", (chunk: Buffer) => stderrCap.push(chunk));

    const timer = setTimeout(() => {
      timedOut = true;
      kill("SIGTERM");
      forceTimer = setTimeout(() => kill("SIGKILL"), options.forceKillAfterMs ?? 3000);
    }, options.timeout);

    proc.on("error", (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (forceTimer) clearTimeout(forceTimer);
      reject(err);
    });

    proc.on("close", (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (forceTimer) clearTimeout(forceTimer);
      const out = stdoutCap.finalize();
      const err = stderrCap.finalize();
      const output = compactOutput(out.text, err.text);
      resolve({
        stdout: out.text,
        stderr: err.text,
        output,
        exitCode: code,
        signal,
        timedOut,
        stdoutTruncated: out.truncated,
        stderrTruncated: err.truncated,
      });
    });
  });
}
