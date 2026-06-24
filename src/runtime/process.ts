import { spawn } from "child_process";

export interface RunCommandOptions {
  cwd?: string;
  timeout: number;
  shell?: string;
  maxCaptureBytes?: number;
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

function appendBounded(current: string, chunk: Buffer, max?: number): { value: string; truncated: boolean } {
  const text = chunk.toString("utf-8");
  if (max === undefined) return { value: current + text, truncated: false };
  const remaining = max - Buffer.byteLength(current, "utf-8");
  if (remaining <= 0) return { value: current, truncated: true };
  const next = Buffer.byteLength(text, "utf-8") <= remaining ? text : text.slice(0, remaining);
  return { value: current + next, truncated: next.length < text.length };
}

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

    let stdout = "";
    let stderr = "";
    let stdoutTruncated = false;
    let stderrTruncated = false;
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

    proc.stdout?.on("data", (chunk: Buffer) => {
      const next = appendBounded(stdout, chunk, options.maxCaptureBytes);
      stdout = next.value;
      stdoutTruncated = stdoutTruncated || next.truncated;
    });
    proc.stderr?.on("data", (chunk: Buffer) => {
      const next = appendBounded(stderr, chunk, options.maxCaptureBytes);
      stderr = next.value;
      stderrTruncated = stderrTruncated || next.truncated;
    });

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
      const output = compactOutput(stdout, stderr);
      resolve({ stdout, stderr, output, exitCode: code, signal, timedOut, stdoutTruncated, stderrTruncated });
    });
  });
}
