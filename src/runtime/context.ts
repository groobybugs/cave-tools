import { AsyncLocalStorage } from "async_hooks";

// Per-call context. In-process calls use this process's cwd/env; the daemon
// runs each call with the calling client's cwd/env so clients never see each
// other's environment or working directory.
export interface CallContext {
  sessionId: string;
  cwd: string;
  env: NodeJS.ProcessEnv;
  // Who is calling: "local" in-process, a per-connection id in the daemon.
  clientId: string;
  // Daemon only: the client process, which owns this call's stats row.
  clientPid?: number;
  // Daemon only: session-key namespace when not per connection (explicit CLI
  // --session uses "cli" so separate CLI processes share read state).
  sessionNamespace?: string;
}

const storage = new AsyncLocalStorage<CallContext>();

export function runWithContext<T>(ctx: CallContext, fn: () => T): T {
  return storage.run(ctx, fn);
}

export function getContext(): CallContext | undefined {
  return storage.getStore();
}

export function getCwd(): string {
  return storage.getStore()?.cwd ?? process.cwd();
}

export function getEnv(): NodeJS.ProcessEnv {
  return storage.getStore()?.env ?? process.env;
}

export function getClientId(): string {
  return storage.getStore()?.clientId ?? "local";
}

export function localContext(sessionId: string): CallContext {
  return { sessionId, cwd: process.cwd(), env: process.env, clientId: "local" };
}
