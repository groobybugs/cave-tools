import { randomBytes } from "crypto";
import {
  closeSync,
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  readSync,
  readdirSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "fs";
import { join } from "path";
import { fileURLToPath } from "url";
import {
  defaultShell,
  isProcessAlive,
  killProcessGroup,
  startDurableBackgroundCommand,
} from "./process.js";
import {
  deleteJob,
  getJob,
  insertJob,
  jobsDir,
  listJobs,
  listJobsEndedBefore,
  updateJobState,
  type JobRecord,
  type JobState,
} from "../storage/db.js";

const EXIT_MARKER = /\[cave-job exit=(-?\d+)(?: signal=(\w+))?\]\s*$/;
const FORCE_KILL_GRACE_MS = 3000;
const DEFAULT_MAX_AGE_MS = 24 * 60 * 60 * 1000;
const ADOPTED_JOB_POLL_MS = 100;
const JOB_WORKER_PATH = fileURLToPath(new URL("./job-worker.js", import.meta.url));

/** Close-waiters for jobs started by this server process, keyed by jobId. */
const closeWaiters = new Map<string, Promise<void>>();

function newJobId(): string {
  return `j${Date.now().toString(36)}${randomBytes(3).toString("hex")}`;
}

function logPathFor(jobId: string): string {
  return join(jobsDir(), `${jobId}.log`);
}

function resultPathFor(jobId: string): string {
  return join(jobsDir(), `${jobId}.result.json`);
}

export async function startJob(input: {
  command: string;
  sessionId: string;
  workdir?: string;
}): Promise<JobRecord> {
  mkdirSync(jobsDir(), { recursive: true, mode: 0o700 });
  const jobId = newJobId();
  const logPath = logPathFor(jobId);
  const resultPath = resultPathFor(jobId);
  const startedAt = Date.now();
  writeFileSync(
    logPath,
    `# cave-job ${jobId}\n# command: ${input.command}\n# started: ${new Date(startedAt).toISOString()}\n\n`,
    { mode: 0o600 },
  );

  const handle = startDurableBackgroundCommand(input.command, {
    cwd: input.workdir,
    shell: defaultShell(),
    workerPath: JOB_WORKER_PATH,
    jobId,
    logPath,
    resultPath,
  });

  const record: JobRecord = {
    jobId,
    pid: handle.pid,
    sessionId: input.sessionId,
    command: input.command,
    workdir: input.workdir ?? null,
    startedAt,
    endedAt: null,
    exitCode: null,
    signal: null,
    state: "running",
    logPath,
  };
  insertJob(record);

  let notify: () => void = () => {};
  closeWaiters.set(
    jobId,
    new Promise<void>((resolve) => {
      notify = resolve;
    }),
  );

  handle.onClose((code, signal) => {
    const completion = recoverFromResult(jobId, resultPath);
    const endedAt = completion?.endedAt ?? Date.now();
    const exitCode = completion?.exitCode ?? code;
    const completionSignal = completion?.signal ?? signal;
    // stopJob() marks the row "killed" before signalling; keep that outcome.
    const current = getJob(jobId);
    const state: JobState = current?.state === "killed" ? "killed" : "exited";
    updateJobState(jobId, { state, endedAt, exitCode, signal: completionSignal });
    notify();
    closeWaiters.delete(jobId);
  });

  return record;
}

interface DurableJobResult {
  version: 1;
  jobId: string;
  endedAt: number;
  exitCode: number | null;
  signal: string | null;
}

function recoverFromResult(jobId: string, resultPath: string): DurableJobResult | null {
  try {
    const parsed = JSON.parse(readFileSync(resultPath, "utf-8")) as Partial<DurableJobResult>;
    if (
      parsed.version !== 1 ||
      parsed.jobId !== jobId ||
      !Number.isFinite(parsed.endedAt) ||
      (parsed.exitCode !== null && !Number.isFinite(parsed.exitCode)) ||
      (parsed.signal !== null && typeof parsed.signal !== "string")
    ) {
      return null;
    }
    return parsed as DurableJobResult;
  } catch {
    return null;
  }
}

/** Reads the legacy exit marker; retained for jobs launched by older versions. */
function recoverFromMarker(logPath: string): { exitCode: number; signal: string | null } | null {
  let tail: string;
  try {
    const buf = Buffer.alloc(256);
    const size = statSync(logPath).size;
    const fd = openSync(logPath, "r");
    try {
      const start = Math.max(0, size - 256);
      const read = readSync(fd, buf, 0, 256, start);
      tail = buf.subarray(0, read).toString("utf-8");
    } finally {
      closeSync(fd);
    }
  } catch {
    return null;
  }
  const match = EXIT_MARKER.exec(tail);
  if (!match) return null;
  return { exitCode: Number(match[1]), signal: match[2] ?? null };
}

/**
 * Returns the freshest view of a job. Rows still marked "running" from a
 * previous server process are reconciled against pid liveness and the log's
 * exit marker, and the db is updated as a side effect.
 */
export function getJobInfo(jobId: string): JobRecord | null {
  const job = getJob(jobId);
  if (!job || job.state !== "running") return job;
  if (closeWaiters.has(jobId) || isProcessAlive(job.pid)) return job;

  const durable = recoverFromResult(jobId, resultPathFor(jobId));
  if (durable) {
    updateJobState(jobId, {
      state: "exited",
      endedAt: durable.endedAt,
      exitCode: durable.exitCode,
      signal: durable.signal,
    });
    return {
      ...job,
      state: "exited",
      exitCode: durable.exitCode,
      signal: durable.signal,
      endedAt: durable.endedAt,
    };
  }

  const recovered = recoverFromMarker(job.logPath);
  if (recovered) {
    updateJobState(jobId, {
      state: "exited",
      endedAt: Date.now(),
      exitCode: recovered.exitCode,
      signal: recovered.signal,
    });
    return { ...job, state: "exited", exitCode: recovered.exitCode, signal: recovered.signal, endedAt: Date.now() };
  }
  updateJobState(jobId, { state: "lost", endedAt: Date.now() });
  return { ...job, state: "lost", endedAt: Date.now() };
}

export function listSessionJobs(sessionId: string): JobRecord[] {
  return listJobs(sessionId).map((job) => getJobInfo(job.jobId) ?? job);
}

export function stopJob(jobId: string, signal: NodeJS.Signals = "SIGTERM"): JobRecord | null {
  const job = getJobInfo(jobId);
  if (!job) return null;
  if (job.state !== "running") return job;

  updateJobState(jobId, { state: "killed", endedAt: Date.now(), signal });
  killProcessGroup(job.pid, signal);
  setTimeout(() => {
    if (isProcessAlive(job.pid)) killProcessGroup(job.pid, "SIGKILL");
  }, FORCE_KILL_GRACE_MS).unref();

  return { ...job, state: "killed", signal, endedAt: Date.now() };
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

async function waitWithTimeout(waiter: Promise<void>, waitMs: number): Promise<void> {
  if (waitMs <= 0) return;
  let timer: NodeJS.Timeout | undefined;
  try {
    await Promise.race([
      waiter,
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, waitMs);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

/**
 * Waits up to waitMs for the job to finish. Jobs started by this server resolve
 * the moment the close event fires; jobs adopted after a restart fall back to a
 * plain sleep followed by a liveness re-check.
 */
export async function waitForJob(jobId: string, waitMs: number): Promise<JobRecord | null> {
  const waiter = closeWaiters.get(jobId);
  if (waiter) {
    await waitWithTimeout(waiter, waitMs);
    return getJobInfo(jobId);
  }

  const deadline = Date.now() + Math.max(0, waitMs);
  let job = getJobInfo(jobId);
  while (job?.state === "running" && Date.now() < deadline) {
    await sleep(Math.min(ADOPTED_JOB_POLL_MS, deadline - Date.now()));
    job = getJobInfo(jobId);
  }
  return job;
}

export interface FileByteChunk {
  text: string;
  requestedCursor: number;
  startCursor: number;
  nextCursor: number;
  fileSize: number;
  truncated: boolean;
  cursorReset: boolean;
}

function utf8SequenceLength(firstByte: number): number {
  if ((firstByte & 0x80) === 0) return 1;
  if ((firstByte & 0xe0) === 0xc0) return 2;
  if ((firstByte & 0xf0) === 0xe0) return 3;
  if ((firstByte & 0xf8) === 0xf0) return 4;
  return 1;
}

function readFileChunkAtSize(
  path: string,
  requestedCursor: number,
  maxBytes: number,
  fileSize: number,
): FileByteChunk {
  const cursorReset = requestedCursor > fileSize;
  const cursor = cursorReset ? 0 : Math.max(0, Math.floor(requestedCursor));
  const length = Math.min(Math.max(0, Math.floor(maxBytes)), fileSize - cursor);
  if (length === 0) {
    return {
      text: "",
      requestedCursor,
      startCursor: cursor,
      nextCursor: cursor,
      fileSize,
      truncated: cursor < fileSize,
      cursorReset,
    };
  }

  const buf = Buffer.alloc(length);
  const fd = openSync(path, "r");
  let bytesRead = 0;
  try {
    bytesRead = readSync(fd, buf, 0, length, cursor);
  } finally {
    closeSync(fd);
  }

  let begin = 0;
  while (begin < bytesRead && (buf[begin]! & 0xc0) === 0x80) begin++;

  let end = bytesRead;
  if (end > begin) {
    let lead = end - 1;
    while (lead >= begin && (buf[lead]! & 0xc0) === 0x80) lead--;
    if (lead >= begin && end - lead < utf8SequenceLength(buf[lead]!)) end = lead;
  }

  const startCursor = cursor + begin;
  const nextCursor = cursor + end;
  return {
    text: buf.subarray(begin, end).toString("utf-8"),
    requestedCursor,
    startCursor,
    nextCursor,
    fileSize,
    truncated: nextCursor < fileSize,
    cursorReset,
  };
}

/** Reads a bounded, UTF-8-boundary-safe byte range starting at cursor. */
export async function readFileChunk(
  path: string,
  cursor: number,
  maxBytes: number,
): Promise<FileByteChunk> {
  try {
    return readFileChunkAtSize(path, cursor, maxBytes, statSync(path).size);
  } catch {
    return {
      text: "",
      requestedCursor: cursor,
      startCursor: 0,
      nextCursor: 0,
      fileSize: 0,
      truncated: false,
      cursorReset: cursor > 0,
    };
  }
}

/** Reads at most the last maxBytes of a file as UTF-8. */
export async function tailFileBytes(path: string, maxBytes: number): Promise<string> {
  try {
    const size = statSync(path).size;
    return readFileChunkAtSize(path, Math.max(0, size - maxBytes), maxBytes, size).text;
  } catch {
    return "";
  }
}

export function fileByteSize(path: string): number {
  try {
    return statSync(path).size;
  } catch {
    return 0;
  }
}

/**
 * Deletes job rows and logs older than maxAgeMs. Running jobs are never
 * pruned; orphan log files with no row are removed by mtime.
 */
export function pruneJobs(maxAgeMs: number = DEFAULT_MAX_AGE_MS): number {
  const cutoff = Date.now() - maxAgeMs;
  let removed = 0;
  for (const job of listJobsEndedBefore(cutoff)) {
    try {
      if (existsSync(job.logPath)) unlinkSync(job.logPath);
      const resultPath = resultPathFor(job.jobId);
      if (existsSync(resultPath)) unlinkSync(resultPath);
    } catch {
      // Best effort.
    }
    deleteJob(job.jobId);
    removed++;
  }
  const referencedPaths = new Set<string>();
  for (const job of listJobs()) {
    referencedPaths.add(job.logPath);
    referencedPaths.add(resultPathFor(job.jobId));
  }
  try {
    if (existsSync(jobsDir())) {
      for (const name of readdirSync(jobsDir())) {
        if (!name.endsWith(".log") && !name.endsWith(".result.json")) continue;
        const path = join(jobsDir(), name);
        try {
          if (!referencedPaths.has(path) && statSync(path).mtimeMs < cutoff) {
            unlinkSync(path);
          }
        } catch {
          // Best effort.
        }
      }
    }
  } catch {
    // Best effort.
  }
  return removed;
}

export { logPathFor, resultPathFor };
