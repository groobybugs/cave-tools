import { appendFileSync, chmodSync, existsSync, mkdirSync, unlinkSync } from "fs";
import { dirname } from "path";
import net from "net";
import { randomBytes } from "crypto";
import { callTool } from "../dispatch.js";
import { allTools } from "../tool-list.js";
import { pruneDeadSessions, releaseClientStats } from "../compression/utils.js";
import { pruneJobs } from "../runtime/jobs.js";
import { cleanupArchives } from "../compression/archive.js";
import { buildId, daemonLogPath, ensureSocketDir, socketPath } from "./paths.js";

// Shared daemon: newline-delimited JSON over a unix socket (named pipe on
// Windows). Each call runs with the client's cwd/env; sessions and stats are
// namespaced per connection / client process.

const DEFAULT_IDLE_MS = 10 * 60 * 1000;

function log(message: string): void {
  try {
    mkdirSync(dirname(daemonLogPath()), { recursive: true });
    appendFileSync(daemonLogPath(), `${new Date().toISOString()} [${process.pid}] ${message}\n`);
  } catch {
    // logging is best-effort
  }
}

function idleMs(): number {
  const raw = Number(process.env.CAVE_TOOLS_DAEMON_IDLE_MS);
  return Number.isFinite(raw) && raw > 0 ? raw : DEFAULT_IDLE_MS;
}

function isAlive(path: string): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.createConnection(path);
    socket.once("connect", () => {
      socket.destroy();
      resolve(true);
    });
    socket.once("error", () => resolve(false));
  });
}

interface Request {
  id?: number;
  method?: string;
  clientPid?: number;
  kind?: string;
  explicitSession?: boolean;
  tool?: string;
  args?: Record<string, unknown>;
  ctx?: { sessionId?: string; cwd?: string; env?: NodeJS.ProcessEnv };
}

export async function startDaemon(): Promise<void> {
  const path = socketPath();
  const isPipe = process.platform === "win32";
  if (!isPipe) {
    ensureSocketDir();
    if (existsSync(path)) {
      if (await isAlive(path)) {
        log(`already running at ${path}`);
        return;
      }
      unlinkSync(path);
    }
  }

  void pruneDeadSessions().catch(() => {});
  try {
    pruneJobs();
  } catch {
    // housekeeping only
  }
  void cleanupArchives().catch(() => {});

  let connections = 0;
  let inFlight = 0;
  let idleTimer: NodeJS.Timeout | undefined;
  const pidConnections = new Map<number, number>();

  const shutdown = (reason: string) => {
    log(`shutting down: ${reason}`);
    server.close();
    if (!isPipe) {
      try {
        unlinkSync(path);
      } catch {
        // already gone
      }
    }
    process.exit(0);
  };

  const touchIdle = () => {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = undefined;
    if (connections === 0 && inFlight === 0) {
      idleTimer = setTimeout(() => shutdown("idle"), idleMs());
    }
  };

  const server = net.createServer((socket) => {
    connections++;
    touchIdle();
    // Assigned here, never taken from the client, so namespacing can't be spoofed.
    const clientId = `c${randomBytes(6).toString("hex")}`;
    let clientPid: number | undefined;
    // Explicit CLI --session opt-in (same-uid trust): stable cli:<id> keys.
    let sessionNamespace: string | undefined;
    let buffer = "";
    socket.setEncoding("utf8");

    const reply = (msg: Record<string, unknown>) => {
      if (!socket.destroyed) socket.write(`${JSON.stringify(msg)}\n`);
    };

    const handle = async (req: Request) => {
      const { id } = req;
      switch (req.method) {
        case "hello":
          if (req.kind === "cli" && req.explicitSession === true) sessionNamespace = "cli";
          if (Number.isInteger(req.clientPid) && clientPid === undefined) {
            clientPid = req.clientPid as number;
            pidConnections.set(clientPid, (pidConnections.get(clientPid) ?? 0) + 1);
          }
          reply({ id, result: { buildId: buildId(), pid: process.pid } });
          return;
        case "list":
          reply({ id, result: { tools: JSON.parse(JSON.stringify(allTools)) } });
          return;
        case "shutdown":
          reply({ id, result: { ok: true } });
          setImmediate(() => shutdown("stop requested"));
          return;
        case "call": {
          if (typeof req.tool !== "string" || !req.ctx || typeof req.ctx.cwd !== "string") {
            reply({ id, error: "bad call request" });
            return;
          }
          inFlight++;
          if (idleTimer) clearTimeout(idleTimer);
          try {
            const result = await callTool(req.tool, req.args, {
              sessionId: String(req.ctx.sessionId ?? "default"),
              cwd: req.ctx.cwd,
              env: req.ctx.env ?? {},
              clientId,
              clientPid,
              sessionNamespace,
            });
            reply({ id, result });
          } catch (error) {
            reply({ id, error: error instanceof Error ? error.message : String(error) });
          } finally {
            inFlight--;
            touchIdle();
          }
          return;
        }
        default:
          reply({ id, error: `unknown method: ${String(req.method)}` });
      }
    };

    socket.on("data", (chunk: string) => {
      buffer += chunk;
      let nl;
      while ((nl = buffer.indexOf("\n")) !== -1) {
        const line = buffer.slice(0, nl);
        buffer = buffer.slice(nl + 1);
        if (!line.trim()) continue;
        let req: Request;
        try {
          req = JSON.parse(line);
        } catch {
          reply({ error: "invalid JSON" });
          continue;
        }
        // Calls run concurrently.
        void handle(req);
      }
    });
    socket.on("error", () => socket.destroy());
    socket.on("close", () => {
      connections--;
      if (clientPid !== undefined) {
        const left = (pidConnections.get(clientPid) ?? 1) - 1;
        if (left <= 0) {
          pidConnections.delete(clientPid);
          releaseClientStats(clientPid);
        } else {
          pidConnections.set(clientPid, left);
        }
      }
      touchIdle();
    });
  });

  server.on("error", (error) => {
    log(`server error: ${error.message}`);
    process.exit(1);
  });
  process.on("SIGTERM", () => shutdown("SIGTERM"));
  process.on("SIGINT", () => shutdown("SIGINT"));

  await new Promise<void>((resolve) => server.listen(path, resolve));
  if (!isPipe) chmodSync(path, 0o600);
  log(`listening on ${path} (build ${buildId()}, idle ${idleMs()}ms)`);
  touchIdle();
}
