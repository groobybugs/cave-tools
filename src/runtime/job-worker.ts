import { spawn } from "child_process";
import {
  appendFileSync,
  closeSync,
  openSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "fs";

interface DurableJobResult {
  version: 1;
  jobId: string;
  endedAt: number;
  exitCode: number | null;
  signal: string | null;
}

const [jobId, logPath, resultPath, shell, command] = process.argv.slice(2);

if (!jobId || !logPath || !resultPath || !shell || command === undefined) {
  process.stderr.write("cave job worker: invalid arguments\n");
  process.exit(1);
}

let finished = false;

function finish(exitCode: number | null, signal: NodeJS.Signals | null, error?: Error): void {
  if (finished) return;
  finished = true;

  if (error) {
    try {
      appendFileSync(logPath, `\n[cave-job worker-error=${JSON.stringify(error.message)}]\n`);
    } catch {
      // Durable result still records failure when the log vanished.
    }
  }

  const result: DurableJobResult = {
    version: 1,
    jobId,
    endedAt: Date.now(),
    exitCode,
    signal,
  };
  const tempPath = `${resultPath}.${process.pid}.tmp`;
  try {
    writeFileSync(tempPath, `${JSON.stringify(result)}\n`, { mode: 0o600 });
    renameSync(tempPath, resultPath);
  } catch (resultError) {
    try {
      const message = resultError instanceof Error ? resultError.message : String(resultError);
      appendFileSync(logPath, `\n[cave-job result-error=${JSON.stringify(message)}]\n`);
    } catch {
      // Parent close handler can still persist completion while server is alive.
    }
  } finally {
    try {
      unlinkSync(tempPath);
    } catch {
      // Rename succeeded or temporary file was never created.
    }
  }

  try {
    appendFileSync(
      logPath,
      `\n[cave-job exit=${exitCode ?? -1}${signal ? ` signal=${signal}` : ""}]\n`,
    );
  } catch {
    // Structured result is authoritative when it could be written.
  }

  process.exitCode = exitCode === null ? 1 : Math.max(0, Math.min(255, exitCode));
}

let fd: number | undefined;
try {
  fd = openSync(logPath, "a");
  const child = spawn(command, [], {
    shell,
    env: process.env,
    stdio: ["ignore", fd, fd],
    windowsHide: process.platform === "win32",
  });
  closeSync(fd);
  fd = undefined;

  child.once("error", (error) => finish(-1, null, error));
  child.once("close", (code, signal) => finish(code, signal));
} catch (error) {
  if (fd !== undefined) closeSync(fd);
  finish(-1, null, error instanceof Error ? error : new Error(String(error)));
}
