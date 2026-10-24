import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  applyBudget,
  extractStructuredData,
  rewriteCommandWithRtk,
  stripLeadingRtk,
} from "../compression/utils.js";
import { collapseRepetition } from "../compression/repetition.js";
import { compressStructuredSemantic } from "../compression/structured.js";
import { redactSecrets } from "../compression/redact.js";
import { classifyCommand } from "../compression/classify.js";
import { archiveHandle } from "../compression/archive.js";
import { captureNotice, defaultShell, runCommand } from "../runtime/process.js";
import { resolveExistingDirectory } from "../runtime/path.js";
import { constants as osConstants } from "os";

// CLI mode wraps an agent's native shell call, so run it in the same kind of
// shell the agent used (bash/zsh), not /bin/sh, which may be dash.
export function cliShell(): string {
  const shell = process.env.SHELL;
  if (process.platform !== "win32" && shell && /\/(bash|zsh)$/.test(shell)) return shell;
  return defaultShell();
}

const DEFAULT_TIMEOUT = 120000;
const MAX_TIMEOUT = 10 * 60 * 1000;

// Shell-style exit status: real code, 124 on timeout, 128+n on signal.
export function shellExitCode(result: { exitCode: number | null; signal: NodeJS.Signals | null; timedOut: boolean }): number {
  if (result.timedOut) return 124;
  if (result.signal) return 128 + (osConstants.signals[result.signal] ?? 0);
  return result.exitCode ?? 1;
}

export interface ShellOutput {
  command: string;
  stdout: string;
  stderr: string;
  // Accepted for callers' completeness; the pipeline does not branch on it yet.
  exitCode?: number | null;
  // Command actually run (after rtk rewrite); drives structured extraction.
  rewrittenCommand?: string;
  redact?: boolean;
}

// Post-run pipeline shared by cave__bash, its CLI mode and `cave-tools
// filter`: classify by command, redact, structured extraction, repetition
// collapse, budget, archive. stdout comes back compressed (plus an archive
// handle when trimmed); stderr only redacted and budgeted.
export async function filterShellOutput(input: ShellOutput): Promise<{ stdout: string; stderr: string }> {
  const { command } = input;
  const shouldRedact = input.redact !== false;
  const rewritten = input.rewrittenCommand ?? command;
  const redact = (text: string) => (shouldRedact ? redactSecrets(text) : text);
  const stderr = input.stderr ? applyBudget(redact(input.stderr), "bash") : "";
  const policy = classifyCommand(command);
  let processed = redact(input.stdout);

  if (policy === "passthrough") return { stdout: processed, stderr };

  // Full (already redacted) output, archived behind a handle if trimmed.
  const full = processed;

  if (policy === "verbatim") {
    const lines = processed.split("\n");
    const VERBATIM_MAX = 500;
    if (lines.length > VERBATIM_MAX) {
      processed = [
        ...lines.slice(0, 250),
        `\n... (${lines.length - 500} lines truncated) ...\n`,
        ...lines.slice(-250),
      ].join("\n");
    }
    return { stdout: `${processed}${await archiveHandle(full, processed, command)}`, stderr };
  }

  // Try semantic structured compression first; fall back to the
  // whitespace-only extractor when input is non-structured or the
  // semantic pass returned null (compression didn't help / toggle off).
  const semantic = compressStructuredSemantic(processed, rewritten);
  processed = semantic ?? extractStructuredData(processed, rewritten);

  // Collapse runs of identical lines before the line budget, so the
  // budget sees compacted content. Exact-match only (digit-mask opt-in
  // lives on collapseRepetition; off here to avoid eating request IDs).
  processed = collapseRepetition(processed);

  // Apply budget compression
  processed = applyBudget(processed, "bash");

  return { stdout: `${processed}${await archiveHandle(full, processed, command)}`, stderr };
}

export const bashTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash",
  description:
    "Shell with RTK rewriting (never prefix rtk yourself), secret redaction, output budget; archives what it trims. On Claude Code/opencode native shell output is already compressed (successful commands); this also compresses failures.",
  inputSchema: {
    type: "object",
    properties: {
      command: {
        type: "string",
      },
      description: {
        type: "string",
        description: "5-10 word summary",
      },
      workdir: {
        type: "string",
        description: "Use instead of cd",
      },
      timeout: {
        type: "number",
        description: `ms; 0 disables, max ${MAX_TIMEOUT}`,
        default: DEFAULT_TIMEOUT,
      },
      allowFailure: {
        type: "boolean",
        description: "Return non-zero exits as text, not errors",
        default: false,
      },
      redact_secrets: {
        type: "boolean",
        default: true,
      },
    },
    required: ["command", "description"],
  },
  handler: async (args) => {
    const command = String(args.command);
    const workdir = args.workdir ? String(args.workdir) : undefined;
    const allowFailure = args.allowFailure === true;
    const shouldRedact = args.redact_secrets !== false;
    // Set by the CLI only: stdout alone is compressed into the text, stderr and
    // the exit code travel in _meta.caveCli so the CLI can mirror the child.
    const cliMode = args.__cli === true;

    // Validate timeout: reject negative/NaN, fall back to default when
    // missing. 0 disables the timeout entirely (matches opencode shell).
    const rawTimeout = args.timeout;
    const timeout = rawTimeout === undefined ? DEFAULT_TIMEOUT : Number(rawTimeout);
    if (!Number.isFinite(timeout) || timeout < 0) {
      return {
        content: [
          {
            type: "text",
            text: `Invalid timeout value: ${rawTimeout}. Timeout must be a non-negative number.`,
          },
        ],
        isError: true,
      };
    }
    if (timeout > MAX_TIMEOUT) {
      return {
        content: [
          {
            type: "text",
            text: `Invalid timeout value: ${rawTimeout}. Timeout must be <= ${MAX_TIMEOUT}ms.`,
          },
        ],
        isError: true,
      };
    }

    let rewrittenCommand = command;

    try {
      const cwd = await resolveExistingDirectory(workdir);
      const peeled = stripLeadingRtk(command);
      rewrittenCommand = await rewriteCommandWithRtk(peeled.command, {
        incomingAlreadyWrapped: peeled.stripped,
      });
      const result = await runCommand(rewrittenCommand, {
        timeout,
        cwd,
        shell: cliMode ? cliShell() : defaultShell(),
        maxBytes: 100 * 1024, // generous; cave's budget table tightens further downstream
        maxLines: 2000,
      });

      // Exit code as data: non-zero (and timeout/signal) are reported inline
      // rather than thrown, so partial output is never lost.
      const exitFailed =
        result.timedOut ||
        (result.exitCode !== null && result.exitCode !== 0) ||
        result.signal !== null;
      const capture = captureNotice(result.stdoutTruncated, result.stderrTruncated);
      const captureNote = capture ? `\n\n${capture}` : "";
      const exitNote = result.timedOut
        ? `\n\n[timed out after ${timeout}ms - process group killed]`
        : result.signal
          ? `\n\n[killed by signal ${result.signal}]`
          : result.exitCode && result.exitCode !== 0
            ? `\n\n[exit: ${result.exitCode}]`
            : "";

      const markError = exitFailed && !allowFailure ? { isError: true } : {};
      // MCP folds stderr into the text; CLI keeps it apart for fd 2.
      const filtered = await filterShellOutput({
        command,
        rewrittenCommand,
        stdout: cliMode ? result.stdout : result.output,
        stderr: cliMode ? result.stderr : "",
        exitCode: shellExitCode(result),
        redact: shouldRedact,
      });
      if (!cliMode) {
        return { content: [{ type: "text", text: `${filtered.stdout}${captureNote}${exitNote}` }], ...markError };
      }
      // CLI: notes go to stderr, never into stdout.
      const notes = `${captureNote}${result.timedOut || result.signal ? exitNote : ""}`.trim();
      const stderr = [filtered.stderr, notes].filter(Boolean).join(filtered.stderr.endsWith("\n") ? "" : "\n");
      return {
        content: [{ type: "text", text: filtered.stdout }],
        ...markError,
        _meta: { caveCli: { exitCode: shellExitCode(result), stderr } },
      };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      let errorMessage = [
        "Command failed to launch",
        `Original command: ${command}`,
        `Executed command: ${rewrittenCommand}`,
        `Message: ${message}`,
      ].join("\n");
      if (shouldRedact) {
        errorMessage = redactSecrets(errorMessage);
      }
      errorMessage = applyBudget(errorMessage, "bash");
      return {
        content: [
          {
            type: "text",
            text: errorMessage,
          },
        ],
        ...(allowFailure ? {} : { isError: true }),
      };
    }
  },
};
