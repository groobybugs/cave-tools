import { spawn } from "child_process";
import { closeSync, openSync, readFileSync, statSync, unlinkSync, writeSync } from "fs";
import net from "net";
import { homedir } from "os";
import type { CallToolResult, Tool } from "@modelcontextprotocol/sdk/types.js";
import { cliEntryPath, daemonKey, daemonLogPath, ensureSocketDir, lockPath, socketPath } from "./daemon/paths.js";

// Thin client for the shared daemon (CAVE_TOOLS_DAEMON=1). Imports no tool
// code: callers fall back to in-process dispatch only when a request was
// never sent.

const CONNECT_TIMEOUT_MS = 300;
const HELLO_TIMEOUT_MS = 2000;
const SPAWN_WAIT_MS = 2000;
const POLL_MS = 50;
const STALE_LOCK_MS = 10_000;
// After the daemon can't be reached, run in-process for a while instead of
// paying the spawn wait on every call.
const RETRY_BACKOFF_MS = 30_000;

export const CONNECTION_LOST_TEXT = "daemon connection lost; command may or may not have run";

export function daemonEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  const value = (env.CAVE_TOOLS_DAEMON ?? "").trim().toLowerCase();
  return value !== "" && value !== "0" && value !== "false" && value !== "off";
}

export type CallOutcome = { sent: false } | { sent: true; result: CallToolResult };

interface Pending {
  resolve: (msg: { result?: unknown; error?: string }) => void;
  reject: (err: Error) => void;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function tryConnect(path: string): Promise<net.Socket | null> {
  return new Promise((resolve) => {
    const socket = net.createConnection(path);
    const timer = setTimeout(() => {
      socket.destroy();
      resolve(null);
    }, CONNECT_TIMEOUT_MS);
    socket.once("connect", () => {
      clearTimeout(timer);
      resolve(socket);
    });
    socket.once("error", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(null);
    });
  });
}

// O_EXCL lock so concurrent clients spawn one daemon. A lock older than
// STALE_LOCK_MS (crashed spawner) is broken.
function acquireLock(key: string): boolean {
  const file = lockPath(key);
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const fd = openSync(file, "wx", 0o600);
      writeSync(fd, `${process.pid} ${Date.now()}`);
      closeSync(fd);
      return true;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") return false;
      try {
        if (Date.now() - statSync(file).mtimeMs > STALE_LOCK_MS) unlinkSync(file);
        else return false;
      } catch {
        // raced with another client removing it; retry once
      }
    }
  }
  return false;
}

function releaseLock(key: string): void {
  try {
    const owner = readFileSync(lockPath(key), "utf-8").split(" ")[0];
    if (owner === String(process.pid)) unlinkSync(lockPath(key));
  } catch {
    // already gone
  }
}

function spawnDaemon(): void {
  let logFd: number | "ignore" = "ignore";
  try {
    logFd = openSync(daemonLogPath(), "a", 0o600);
  } catch {
    // log dir missing; the daemon creates it
  }
  const child = spawn(process.execPath, [cliEntryPath(), "daemon", "start"], {
    detached: true,
    stdio: ["ignore", logFd, logFd],
    env: process.env,
    // Don't pin the spawning client's cwd; calls carry their own.
    cwd: homedir(),
    windowsHide: true,
  });
  child.unref();
  if (typeof logFd === "number") closeSync(logFd);
}

export class DaemonClient {
  private socket: net.Socket | null = null;
  private connecting: Promise<net.Socket | null> | null = null;
  private nextId = 1;
  private pending = new Map<number, Pending>();
  private buffer = "";
  private retryAfter = 0;
  readonly clientId = `${process.pid}-${Math.random().toString(16).slice(2, 10)}`;

  // kind "cli" + explicitSession asks the daemon to key sessions as
  // cli:<sessionId> (shared across CLI processes) instead of per connection.
  constructor(private readonly hello: { kind?: string; explicitSession?: boolean } = {}) {}

  private attach(socket: net.Socket): void {
    socket.setEncoding("utf8");
    socket.on("data", (chunk: string) => {
      this.buffer += chunk;
      let nl;
      while ((nl = this.buffer.indexOf("\n")) !== -1) {
        const line = this.buffer.slice(0, nl);
        this.buffer = this.buffer.slice(nl + 1);
        if (!line.trim()) continue;
        let msg: { id?: number; result?: unknown; error?: string };
        try {
          msg = JSON.parse(line);
        } catch {
          continue;
        }
        const waiter = msg.id === undefined ? undefined : this.pending.get(msg.id);
        if (!waiter) continue;
        this.pending.delete(msg.id as number);
        waiter.resolve(msg);
      }
    });
    const lost = () => {
      if (this.socket === socket) this.socket = null;
      this.buffer = "";
      const waiters = [...this.pending.values()];
      this.pending.clear();
      for (const waiter of waiters) waiter.reject(new Error(CONNECTION_LOST_TEXT));
    };
    socket.on("close", lost);
    socket.on("error", () => socket.destroy());
  }

  private request(socket: net.Socket, method: string, payload: Record<string, unknown>, timeoutMs?: number) {
    const id = this.nextId++;
    return new Promise<{ result?: unknown; error?: string }>((resolve, reject) => {
      let timer: NodeJS.Timeout | undefined;
      const done = <T>(fn: (v: T) => void) => (v: T) => {
        if (timer) clearTimeout(timer);
        fn(v);
      };
      this.pending.set(id, { resolve: done(resolve), reject: done(reject) });
      if (timeoutMs) {
        timer = setTimeout(() => {
          this.pending.delete(id);
          reject(new Error(`daemon ${method} timed out`));
        }, timeoutMs);
      }
      socket.write(`${JSON.stringify({ id, method, ...payload })}\n`);
    });
  }

  private async handshake(socket: net.Socket): Promise<net.Socket | null> {
    this.attach(socket);
    try {
      const reply = await this.request(socket, "hello", { clientPid: process.pid, clientId: this.clientId, ...this.hello }, HELLO_TIMEOUT_MS);
      if (reply.error) throw new Error(reply.error);
      return socket;
    } catch {
      socket.destroy();
      return null;
    }
  }

  private async connectOrSpawn(): Promise<net.Socket | null> {
    // Recomputed per attempt so a rebuild while this client lives is seen.
    const key = daemonKey();
    // Verify the socket dir (ours, 0700, not a symlink) BEFORE connecting: each
    // call carries the full env, and the /tmp fallback dir could be pre-created
    // by another user with their own socket in it.
    try {
      if (process.platform !== "win32") ensureSocketDir(key);
    } catch {
      return null;
    }
    const path = socketPath(key);
    let socket = await tryConnect(path);
    if (!socket) {
      const owner = acquireLock(key);
      try {
        if (owner) spawnDaemon();
        const deadline = Date.now() + SPAWN_WAIT_MS;
        while (!socket && Date.now() < deadline) {
          await sleep(POLL_MS);
          socket = await tryConnect(path);
        }
      } finally {
        if (owner) releaseLock(key);
      }
      if (!socket) return null;
    }
    return this.handshake(socket);
  }

  // Connected socket, connecting (and spawning the daemon) if needed. null
  // means the daemon is unavailable and nothing was sent.
  private async ensure(): Promise<net.Socket | null> {
    if (this.socket && !this.socket.destroyed) return this.socket;
    if (Date.now() < this.retryAfter) return null;
    if (!this.connecting) {
      this.connecting = this.connectOrSpawn().finally(() => {
        this.connecting = null;
      });
    }
    this.socket = await this.connecting;
    if (!this.socket) this.retryAfter = Date.now() + RETRY_BACKOFF_MS;
    return this.socket;
  }

  async call(
    tool: string,
    args: Record<string, unknown> | undefined,
    ctx: { sessionId: string; cwd: string; env: NodeJS.ProcessEnv },
  ): Promise<CallOutcome> {
    const socket = await this.ensure();
    if (!socket) return { sent: false };
    try {
      const reply = await this.request(socket, "call", { tool, args: args ?? {}, ctx });
      if (reply.error) {
        return { sent: true, result: { content: [{ type: "text", text: reply.error }], isError: true } };
      }
      return { sent: true, result: reply.result as CallToolResult };
    } catch {
      // The request was written: never re-run it (bash/edit have side effects).
      return { sent: true, result: { content: [{ type: "text", text: CONNECTION_LOST_TEXT }], isError: true } };
    }
  }

  async list(): Promise<Tool[] | null> {
    const socket = await this.ensure();
    if (!socket) return null;
    try {
      const reply = await this.request(socket, "list", {}, HELLO_TIMEOUT_MS);
      return reply.error ? null : ((reply.result as { tools: Tool[] }).tools ?? null);
    } catch {
      return null;
    }
  }

  close(): void {
    this.socket?.destroy();
    this.socket = null;
  }
}

let shared: DaemonClient | undefined;

export function getDaemonClient(hello?: { kind?: string; explicitSession?: boolean }): DaemonClient {
  shared ??= new DaemonClient(hello);
  return shared;
}
