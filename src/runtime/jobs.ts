import { randomBytes } from "crypto";
import {
  appendFileSync,
  closeSync,
  existsSync,
  mkdirSync,
  openSync,
  readSync,
  readdirSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "fs";
import { readFile } from "fs/promises";
import { join } from "path";
import {
  defaultShell,
  isProcessAlive,
  killProcessGroup,
  startBackgroundCommand,
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

/** Close-waiters for jobs started by this server process, keyed by jobId. */
const closeWaiters = new Map<string, Promise<void>>();

function newJobId(): string {
  return `j${Date.now().toString(36)}${randomBytes(3).toString("hex")}`;
}

function logPathFor(jobId: string): string {
  return join(jobsDir(), `${jobId}.log`);
}

export async function startJob(input: {
  command: string;
  sessionId: string;
  workdir?: string;
}): Promise<JobRecord> {
  mkdirSync(jobsDir(), { recursive: true, mode: 0o700 });
  const jobId = newJobId();
  const logPath = logPathFor(jobId);
  const startedAt = Date.now();
  writeFileSync(
    logPath,
    `# cave-job ${jobId}\n# command: ${input.command}\n# started: ${new Date(startedAt).toISOString()}\n\n`,
    { mode: 0o600 },
  );

  const fd = openSync(logPath, "a");
  let handle: ReturnType<typeof startBackgroundCommand>;
  try {
    handle = startBackgroundCommand(input.command, {
      cwd: input.workdir,
      shell: defaultShell(),
      logFd: fd,
    });
  } finally {
    // The child holds its own dup of the fd via stdio.
    closeSync(fd);
  }

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
    const endedAt = Date.now();
    try {
      appendFileSync(
        logPath,
        `\n[cave-job exit=${code ?? -1}${signal ? ` signal=${signal}` : ""}]\n`,
      );
    } catch {
      // Log vanished; db state still records the outcome.
    }
    // stopJob() marks the row "killed" before signalling; keep that outcome.
    const current = getJob(jobId);
    const state: JobState = current?.state === "killed" ? "killed" : "exited";
    updateJobState(jobId, { state, endedAt, exitCode: code, signal });
    notify();
    closeWaiters.delete(jobId);
  });

  return record;
}

/** Reads the exit marker appended by the close handler; used after a server restart. */
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

/**
 * Waits up to waitMs for the job to finish. Jobs started by this server resolve
 * the moment the close event fires; jobs adopted after a restart fall back to a
 * plain sleep followed by a liveness re-check.
 */
export async function waitForJob(jobId: string, waitMs: number): Promise<JobRecord | null> {
  const waiter = closeWaiters.get(jobId);
  if (waiter) {
    await Promise.race([waiter, sleep(waitMs)]);
  } else {
    await sleep(waitMs);
  }
  return getJobInfo(jobId);
}

/** Reads at most the last maxBytes of a file as UTF-8. */
export async function tailFileBytes(path: string, maxBytes: number): Promise<string> {
  try {
    const size = statSync(path).size;
    if (size <= maxBytes) return await readFile(path, "utf-8");
    const buf = Buffer.alloc(maxBytes);
    const fd = openSync(path, "r");
    try {
      const start = size - maxBytes;
      const read = readSync(fd, buf, 0, maxBytes, start);
      let offset = 0;
      while (offset < read && (buf[offset] & 0xc0) === 0x80) offset++;
      return buf.subarray(offset, read).toString("utf-8");
    } finally {
      closeSync(fd);
    }
  } catch {
    return "";
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
    } catch {
      // Best effort.
    }
    deleteJob(job.jobId);
    removed++;
  }
  try {
    if (existsSync(jobsDir())) {
      for (const name of readdirSync(jobsDir())) {
        if (!name.endsWith(".log")) continue;
        const path = join(jobsDir(), name);
        try {
          if (statSync(path).mtimeMs < cutoff) {
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

export { logPathFor };
