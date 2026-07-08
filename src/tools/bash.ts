import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  applyBudget,
  extractStructuredData,
  rewriteCommandWithRtk,
} from "../compression/utils.js";
import { redactSecrets } from "../compression/redact.js";
import { classifyCommand } from "../compression/classify.js";
import { archiveIfLarge } from "../compression/archive.js";
import { captureNotice, defaultShell, runCommand } from "../runtime/process.js";
import { resolveExistingDirectory } from "../runtime/path.js";

const DEFAULT_TIMEOUT = 120000;
const MAX_TIMEOUT = 10 * 60 * 1000;

export const bashTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash",
  description:
    "Optimized drop-in replacement for the built-in shell tool. Runs the same command and returns its output, applying RTK command rewriting where available and trimming noisy output to the essentials. Sensitive tokens and keys are redacted from output by default. Output is streamed and captured without a buffer ceiling; large output is archived. Use 'workdir' instead of 'cd'.",
  inputSchema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "The shell command to execute",
      },
      description: {
        type: "string",
        description:
          "Clear, concise description of what the command does (5-10 words)",
      },
      workdir: {
        type: "string",
        description:
          "Working directory to run the command in. Use this instead of 'cd'.",
      },
      timeout: {
        type: "number",
        description: `Timeout in milliseconds (must be positive, max ${MAX_TIMEOUT})`,
        default: DEFAULT_TIMEOUT,
      },
      allowFailure: {
        type: "boolean",
        description:
          "When true, non-zero exits are returned as normal text instead of MCP errors. Default false.",
        default: false,
      },
      redact_secrets: {
        type: "boolean",
        description:
          "When false, skip secret redaction. Default true.",
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

    // Validate timeout: reject negative, fall back to default for 0/missing.
    const rawTimeout = args.timeout;
    if (rawTimeout !== undefined && Number(rawTimeout) < 0) {
      return {
        content: [
          {
            type: "text",
            text: `Invalid timeout value: ${rawTimeout}. Timeout must be a positive number.`,
          },
        ],
        isError: true,
      };
    }
    const timeout = Number(rawTimeout) || DEFAULT_TIMEOUT;
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
    const policy = classifyCommand(command);

    try {
      const cwd = await resolveExistingDirectory(workdir);
      rewrittenCommand = await rewriteCommandWithRtk(command);
      const result = await runCommand(rewrittenCommand, {
        timeout,
        cwd,
        shell: defaultShell(),
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
      const spillNote = result.stdoutPath
        ? `\n\n[Full output spilled to: ${result.stdoutPath}]`
        : "";
      const exitNote = result.timedOut
        ? `\n\n[timed out after ${timeout}ms - process group killed]`
        : result.signal
          ? `\n\n[killed by signal ${result.signal}]`
          : result.exitCode && result.exitCode !== 0
            ? `\n\n[exit: ${result.exitCode}]`
            : "";

      let processed = shouldRedact ? redactSecrets(result.output) : result.output;

      const markError = exitFailed && !allowFailure ? { isError: true } : {};

      if (policy === "passthrough") {
        return {
          content: [
            {
              type: "text",
              text: `${processed}${spillNote}${captureNote}${exitNote}`,
            },
          ],
          ...markError,
        };
      }

      const archive = await archiveIfLarge(processed, command);

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
        const archiveNote = archive
          ? `\n\n[Archived: ${archive.id} — use cave__compress expand ${archive.id}]`
          : "";
        return {
          content: [
            {
              type: "text",
              text: `${processed}${archiveNote}${spillNote}${captureNote}${exitNote}`,
            },
          ],
          ...markError,
        };
      }

      // Try structured extraction first
      processed = extractStructuredData(processed, rewrittenCommand);

      // Apply budget compression
      processed = applyBudget(processed, "bash");

      const archiveNote = archive
        ? `\n\n[Archived: ${archive.id} — use cave__compress expand ${archive.id}]`
        : "";

      return {
        content: [
          {
            type: "text",
            text: `${processed}${archiveNote}${spillNote}${captureNote}${exitNote}`,
          },
        ],
        ...markError,
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
