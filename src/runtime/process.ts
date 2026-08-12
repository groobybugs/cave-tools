import { spawn } from "child_process";

export interface RunCommandOptions {
  cwd?: string;
  timeout: number;
  shell?: string;
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

export function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export function killProcessGroup(pid: number, signal: NodeJS.Signals): boolean {
  try {
    if (process.platform !== "win32") process.kill(-pid, signal);
    else process.kill(pid, signal);
    return true;
  } catch {
    return false;
  }
}

export interface StartBackgroundOptions {
  cwd?: string;
  shell?: string;
  /** Open fd the child's stdout and stderr are both redirected to. */
  logFd: number;
}

export type CloseListener = (code: number | null, signal: NodeJS.Signals | null) => void;

export interface BackgroundHandle {
  pid: number;
  /** Registers a close listener; replays immediately if the process already exited. */
  onClose: (cb: CloseListener) => void;
}

/**
 * Spawns a detached process group whose output goes to logFd, then unrefs it so
 * the server can exit without waiting. Listeners attached after exit replay the
 * final result instead of hanging forever.
 */
export function startBackgroundCommand(
  command: string,
  options: StartBackgroundOptions,
): BackgroundHandle {
  const detached = process.platform !== "win32";
  const proc = spawn(command, [], {
    shell: options.shell ?? defaultShell(),
    cwd: options.cwd,
    env: process.env,
    stdio: ["ignore", options.logFd, options.logFd],
    detached,
    windowsHide: process.platform === "win32",
  });
  proc.unref();

  const listeners: CloseListener[] = [];
  let result: { code: number | null; signal: NodeJS.Signals | null } | null = null;
  const fire = (code: number | null, signal: NodeJS.Signals | null) => {
    if (result) return;
    result = { code, signal };
    for (const cb of listeners.splice(0)) cb(code, signal);
  };
  proc.on("close", fire);
  proc.on("error", () => fire(null, null));

  return {
    pid: proc.pid as number,
    onClose: (cb) => {
      const settled = result;
      if (settled) queueMicrotask(() => cb(settled.code, settled.signal));
      else listeners.push(cb);
    },
  };
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

    const maxBytes = options.maxBytes ?? 100 * 1024;
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
