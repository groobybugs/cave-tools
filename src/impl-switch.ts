// Opt-in Go implementation switch for the TypeScript entry point.
//
// The switch deliberately lives at the CLI boundary instead of every command:
// once `CAVE_TOOLS_IMPL=go` is selected, the Go binary receives the original
// argv/env/cwd and owns stdin/stdout/stderr, exit status and signals. The
// TypeScript implementation remains the default for unset or any other value.
//
// Two surfaces stay in TypeScript even under the opt-in (see
// scripts/impl-routes.mjs, the single source of truth shared with the POSIX
// launcher shim generator):
//   - the stdio MCP server (`mcp` or a bare invocation): long-lived, so there
//     is no startup win, and it owns the MCP SDK session.
//   - `webfetch`/`websearch`: they depend on Node fetch/undici behaviour
//     (Brotli, stacked Content-Encoding, zstd), which the Go port documents as
//     gaps.

import { spawn, type ChildProcess } from "node:child_process";
import { accessSync, constants, statSync, writeSync } from "node:fs";
import { constants as osConstants } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { TS_ROUTED_COMMANDS, TS_ROUTED_TOOLS } from "../scripts/impl-routes.mjs";

export const IMPL_ENV = "CAVE_TOOLS_IMPL";
export const GO_BIN_ENV = "CAVE_TOOLS_GO_BIN";

/** Commands that keep running in TypeScript under `CAVE_TOOLS_IMPL=go`. */
export { TS_ROUTED_COMMANDS, TS_ROUTED_TOOLS };

/**
 * Whether argv must stay in TypeScript even when Go is selected. Mirrors
 * src/cli.ts dispatch exactly: no arguments and first token `mcp` start the
 * stdio MCP server, and `webfetch`/`websearch` are the Node-fetch-backed tool
 * commands. Those tools are also reachable through the explicit
 * `tool <name>` dispatcher, which loads the same handler, so that form is
 * routed here too. Everything else (including `__job-worker`, `filter`,
 * `status`, `raw`, `bash`, `jobs`, daemon control and `tool <other>`) keeps
 * going to Go.
 */
export function routesToTypeScript(argv: readonly string[]): boolean {
  if (argv.length === 0) return true;
  if (TS_ROUTED_COMMANDS.includes(argv[0])) return true;
  return argv[0] === "tool" && TS_ROUTED_TOOLS.includes(argv[1] ?? "");
}

export interface GoBinaryResolution {
  path: string;
  source: "env" | "default";
}

export interface ResolveGoBinaryOptions {
  env?: NodeJS.ProcessEnv;
  cwd?: string;
  platform?: NodeJS.Platform;
  /** Package root; defaults to the parent of the compiled module directory. */
  packageRoot?: string;
}

export interface RunGoOptions extends ResolveGoBinaryOptions {
  argv: string[];
}

/** Exact `go` (lowercase) selects Go; every other value keeps TypeScript. */
export function shouldUseGo(env: NodeJS.ProcessEnv = process.env): boolean {
  return env[IMPL_ENV] === "go";
}

/** Repository/package root containing `go/bin/` and `dist/`. */
export function packageRootFromModule(): string {
  return fileURLToPath(new URL("..", import.meta.url));
}

/**
 * Resolve the Go binary path. `CAVE_TOOLS_GO_BIN` wins when non-empty; a
 * relative override is resolved against the caller's cwd, matching how the
 * process would be spawned. The fallback is `<package>/go/bin/cave-tools-go`
 * (`.exe` on Windows).
 */
export function resolveGoBinary(options: ResolveGoBinaryOptions = {}): GoBinaryResolution {
  const env = options.env ?? process.env;
  const cwd = options.cwd ?? process.cwd();
  const platform = options.platform ?? process.platform;
  const override = env[GO_BIN_ENV];
  if (typeof override === "string" && override.length > 0) {
    return { path: resolve(cwd, override), source: "env" };
  }
  const root = options.packageRoot ?? packageRootFromModule();
  const name = platform === "win32" ? "cave-tools-go.exe" : "cave-tools-go";
  return { path: join(root, "go", "bin", name), source: "default" };
}

/** Returns the reason a path cannot be executed, or null when it can. */
export function goBinaryProblem(
  binaryPath: string,
  platform: NodeJS.Platform = process.platform,
): "missing" | "not-executable" | null {
  try {
    if (!statSync(binaryPath).isFile()) return "not-executable";
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    return code === "ENOENT" || code === "ENOTDIR" ? "missing" : "not-executable";
  }
  if (platform !== "win32") {
    try {
      accessSync(binaryPath, constants.X_OK);
    } catch {
      return "not-executable";
    }
  }
  return null;
}

function writeStderr(message: string): void {
  try {
    writeSync(2, message);
  } catch {
    // The process is already exiting; there is nowhere else to report it.
  }
}

function failMissing(binaryPath: string): never {
  writeStderr(
    `cave-tools: ${IMPL_ENV}=go but the Go binary was not found: ${binaryPath}\n` +
      "Build it with `cd go && go build -o bin/cave-tools-go ./cmd/cave-tools-go`, " +
      `or set ${GO_BIN_ENV} to an existing binary.\n`,
  );
  process.exit(127);
}

function failNotExecutable(binaryPath: string): never {
  writeStderr(
    `cave-tools: ${IMPL_ENV}=go but the Go binary is not executable: ${binaryPath}\n` +
      `Set ${GO_BIN_ENV} to an executable file.\n`,
  );
  process.exit(126);
}

const FORWARDED_SIGNALS: NodeJS.Signals[] = ["SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT"];

function signalExitCode(signal: NodeJS.Signals): number {
  const number = (osConstants.signals as Record<string, number | undefined>)[signal];
  return 128 + (number ?? 1);
}

/**
 * Spawn the Go implementation with inherited stdio and the exact caller
 * environment, then mirror its exit status or terminating signal. This never
 * falls back to TypeScript: a selected-but-broken Go binary is a hard error.
 */
export function runGoImplementation(options: RunGoOptions): Promise<never> {
  const resolution = resolveGoBinary(options);
  const problem = goBinaryProblem(resolution.path, options.platform ?? process.platform);
  if (problem === "missing") failMissing(resolution.path);
  if (problem === "not-executable") failNotExecutable(resolution.path);

  return new Promise<never>((_, reject) => {
    let child: ChildProcess;
    try {
      child = spawn(resolution.path, options.argv, {
        cwd: options.cwd ?? process.cwd(),
        env: options.env ?? process.env,
        stdio: "inherit",
        windowsHide: false,
      });
    } catch (error) {
      writeStderr(
        `cave-tools: failed to start the Go implementation (${resolution.path}): ${
          error instanceof Error ? error.message : String(error)
        }\n`,
      );
      process.exit(126);
    }

    const handlers = new Map<NodeJS.Signals, () => void>();
    const cleanup = () => {
      for (const [signal, handler] of handlers) process.removeListener(signal, handler);
      handlers.clear();
    };
    const forward = (signal: NodeJS.Signals) => {
      if (child.exitCode !== null || child.signalCode !== null) return;
      try {
        child.kill(signal);
      } catch {
        // The child may have exited between the check and the signal.
      }
    };
    for (const signal of FORWARDED_SIGNALS) {
      const handler = () => forward(signal);
      try {
        process.on(signal, handler);
        handlers.set(signal, handler);
      } catch {
        // Signal is not supported on this platform.
      }
    }

    child.once("error", (error) => {
      cleanup();
      const code = (error as NodeJS.ErrnoException).code;
      writeStderr(
        `cave-tools: failed to start the Go implementation (${resolution.path}): ${error.message}\n`,
      );
      reject(error);
      process.exit(code === "ENOENT" ? 127 : 126);
    });

    child.once("exit", (code, signal) => {
      cleanup();
      if (signal) {
        // Re-deliver the signal so the parent's wait status matches the Go
        // process instead of being flattened into a numeric exit code.
        try {
          process.kill(process.pid, signal);
        } catch {
          process.exit(signalExitCode(signal));
        }
        // Fallback for platforms where re-raising the signal does not
        // terminate the process. The re-delivered signal normally wins.
        setTimeout(() => process.exit(signalExitCode(signal)), 1000);
        return;
      }
      process.exit(code ?? 1);
    });
  });
}

/**
 * Entry-point hook. Returns immediately when TypeScript is selected, and also
 * when argv is one of the surfaces that stays in TypeScript under the Go
 * opt-in. Otherwise it only settles by terminating the process with the Go
 * result.
 */
export async function maybeRunGoImplementation(
  argv: string[] = process.argv.slice(2),
  env: NodeJS.ProcessEnv = process.env,
): Promise<void> {
  if (!shouldUseGo(env)) return;
  if (routesToTypeScript(argv)) return;
  await runGoImplementation({ argv, env, cwd: process.cwd() });
}
