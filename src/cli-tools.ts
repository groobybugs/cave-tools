import { randomBytes } from "crypto";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { callTool, isToolName, loadTool, TOOL_NAMES, type ToolDefinition } from "./dispatch.js";
import { flushStats, suppressSubagentEnvLog } from "./compression/utils.js";
import { daemonEnabled, getDaemonClient, type CallOutcome } from "./daemon-client.js";

// `cave-tools <tool> [positional...] [--prop value...]`: runs one MCP tool
// in-process. Flags come from the tool's inputSchema.

export class UsageError extends Error {}

interface PropSchema {
  type?: string;
  description?: string;
  default?: unknown;
  enum?: unknown[];
  items?: { type?: string };
}

export interface ParsedToolArgs {
  args: Record<string, unknown>;
  sessionId?: string;
  help: boolean;
}

// For these, everything after the first positional is the command line: one
// positional is used verbatim (the way to pass shell syntax), several are
// quoted individually so argument boundaries survive (see joinCommandArgs).
const COMMAND_TOOLS = new Set(["cave__bash", "cave__bash_start"]);
const DEFAULT_CLI_DESCRIPTION = "cave-tools CLI";

function schemaOf(tool: ToolDefinition): { props: Record<string, PropSchema>; required: string[] } {
  const schema = tool.inputSchema as { properties?: Record<string, PropSchema>; required?: string[] };
  return { props: schema.properties ?? {}, required: schema.required ?? [] };
}

const SHELL_SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/;

// Join several CLI args into one shell command without letting the shell
// re-split or interpret them: each unsafe arg is POSIX single-quoted.
export function joinCommandArgs(argv: string[], platform: string = process.platform): string {
  if (platform === "win32") {
    throw new UsageError('on Windows pass the command as one quoted string, e.g. "dir | findstr x"');
  }
  return argv.map((arg) => (SHELL_SAFE.test(arg) ? arg : `'${arg.replace(/'/g, `'\\''`)}'`)).join(" ");
}

function parseBoolean(flag: string, value: string): boolean {
  if (value === "true" || value === "1") return true;
  if (value === "false" || value === "0") return false;
  throw new UsageError(`--${flag} expects true or false, got "${value}"`);
}

function coerce(flag: string, prop: PropSchema, value: string): unknown {
  switch (prop.type) {
    case "number": {
      const n = Number(value);
      if (value.trim() === "" || !Number.isFinite(n)) throw new UsageError(`--${flag} expects a number, got "${value}"`);
      return n;
    }
    case "boolean":
      return parseBoolean(flag, value);
    case "array": {
      try {
        const parsed = JSON.parse(value);
        if (Array.isArray(parsed)) return parsed;
      } catch {
        // not JSON
      }
      if (prop.items?.type === "string") return [value];
      throw new UsageError(`--${flag} expects a JSON array`);
    }
    case "object": {
      try {
        const parsed = JSON.parse(value);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed;
      } catch {
        // fall through
      }
      throw new UsageError(`--${flag} expects a JSON object`);
    }
    default:
      return value;
  }
}

export async function parseToolArgs(
  tool: ToolDefinition,
  argv: string[],
  readStdin: () => Promise<string>,
): Promise<ParsedToolArgs> {
  const { props, required } = schemaOf(tool);
  const isCommandTool = COMMAND_TOOLS.has(tool.name);
  const raw: Record<string, string | boolean> = {};
  const positionals: string[] = [];
  let sessionId: string | undefined;
  let help = false;
  let flagsDone = false;

  for (let i = 0; i < argv.length; i++) {
    const token = argv[i];
    if (!flagsDone && token === "--") {
      flagsDone = true;
      continue;
    }
    if (flagsDone || !token.startsWith("--")) {
      positionals.push(token);
      if (isCommandTool) flagsDone = true;
      continue;
    }
    const eq = token.indexOf("=");
    const flag = eq === -1 ? token.slice(2) : token.slice(2, eq);
    const inline = eq === -1 ? undefined : token.slice(eq + 1);
    const name = flag.replace(/-/g, "_");
    const takeValue = (): string => {
      if (inline !== undefined) return inline;
      if (i + 1 >= argv.length) throw new UsageError(`--${flag} needs a value`);
      return argv[++i];
    };
    if (name === "help") {
      help = true;
      continue;
    }
    if (name === "session") {
      sessionId = takeValue();
      continue;
    }
    const prop = props[name];
    if (!prop) throw new UsageError(`unknown flag --${flag}`);
    if (prop.type === "boolean") {
      if (inline !== undefined) raw[name] = parseBoolean(flag, inline);
      else if (argv[i + 1] === "true" || argv[i + 1] === "false") raw[name] = argv[++i] === "true";
      else raw[name] = true;
    } else {
      raw[name] = takeValue();
    }
  }
  if (help) return { args: {}, sessionId, help };

  const args: Record<string, unknown> = {};
  let stdinUsed = false;
  const resolve = async (value: string): Promise<string> => {
    if (value !== "-") return value;
    if (stdinUsed) throw new UsageError("only one value can be read from stdin (-)");
    stdinUsed = true;
    return readStdin();
  };

  for (const [name, value] of Object.entries(raw)) {
    args[name] = typeof value === "boolean" ? value : coerce(name, props[name], await resolve(value));
  }

  if (isCommandTool) {
    if (positionals.length > 0) {
      if (args.command !== undefined) throw new UsageError("give the command either positionally or with --command");
      args.command = positionals.length === 1 ? await resolve(positionals[0]) : joinCommandArgs(positionals);
    }
    if (args.description === undefined) args.description = DEFAULT_CLI_DESCRIPTION;
  } else {
    // Positionals fill required props, then optional ones, in schema order.
    const order = [...required, ...Object.keys(props).filter((key) => !required.includes(key))]
      .filter((key) => args[key] === undefined);
    let p = 0;
    for (const key of order) {
      if (p >= positionals.length) break;
      const prop = props[key];
      if (prop.type === "array") {
        const rest = await Promise.all(positionals.slice(p).map(resolve));
        args[key] = prop.items?.type === "string" ? rest : rest.map((v) => coerce(key, prop.items ?? {}, v));
        p = positionals.length;
      } else {
        args[key] = coerce(key, prop, await resolve(positionals[p++]));
      }
    }
    if (p < positionals.length) throw new UsageError(`unexpected argument "${positionals[p]}"`);
  }

  const missing = required.filter((key) => args[key] === undefined);
  if (missing.length > 0) throw new UsageError(`missing required: ${missing.join(", ")}`);
  return { args, sessionId, help: false };
}

const shortName = (name: string) => name.replace(/^cave__/, "");

function usageLine(tool: ToolDefinition): string {
  const { props, required } = schemaOf(tool);
  const pos = COMMAND_TOOLS.has(tool.name)
    ? ["<command...>"]
    : required.map((key) => (props[key].type === "array" ? `<${key}...>` : `<${key}>`));
  const flags = Object.keys(props)
    .filter((key) => !(COMMAND_TOOLS.has(tool.name) ? key === "command" : required.includes(key)))
    .map((key) => (props[key].type === "boolean" ? `[--${key}]` : `[--${key} <${props[key].type ?? "value"}>]`));
  return ["cave-tools", shortName(tool.name), ...pos, ...flags].join(" ");
}

export function toolHelp(tool: ToolDefinition): string {
  const { props, required } = schemaOf(tool);
  const rows = Object.entries(props).map(([key, prop]) => {
    const bits = [prop.type ?? "any"];
    if (required.includes(key) && !(COMMAND_TOOLS.has(tool.name) && key === "description")) bits.push("required");
    if (prop.default !== undefined) bits.push(`default ${JSON.stringify(prop.default)}`);
    if (prop.enum) bits.push(`one of ${prop.enum.join("|")}`);
    return `  --${key.padEnd(24)} ${bits.join(", ")}${prop.description ? ` — ${prop.description}` : ""}`;
  });
  return [
    `${shortName(tool.name)}: ${tool.description ?? ""}`,
    "",
    `usage: ${usageLine(tool)}`,
    "",
    ...rows,
    "",
    COMMAND_TOOLS.has(tool.name)
      ? 'One argument runs verbatim as shell (e.g. "ls | wc -l"); several arguments are each quoted, so shell syntax inside them stays literal.'
      : "Positionals fill required params, then optional ones, in the order above.",
    "A value of - reads that param from stdin. --session <id> reuses read state across calls (needs CAVE_TOOLS_DAEMON=1).",
  ].join("\n");
}

export async function generalHelp(): Promise<string> {
  const lines = ["cave-tools <tool> [args...] [--param value...]   (run `cave-tools help <tool>` for params)", ""];
  for (const name of TOOL_NAMES) {
    const tool = await loadTool(name);
    const first = (tool.description ?? "").split(/(?<=\.)\s/)[0];
    lines.push(`  ${shortName(name).padEnd(12)} ${first}`);
  }
  lines.push(
    "",
    "The status tool runs as `cave-tools tool status` (`cave-tools status` is the savings report).",
    "Other commands: mcp, status, jobs, bins, init.",
  );
  return lines.join("\n");
}

export function formatResult(result: CallToolResult): string {
  const parts: string[] = [];
  for (const block of result.content ?? []) {
    if (block.type === "text") parts.push(block.text);
    else if (block.type === "image") parts.push(`[image ${block.mimeType} ${Buffer.from(block.data, "base64").length} B]`);
    else if (block.type === "resource") {
      const res = block.resource as { mimeType?: string; blob?: string; text?: string };
      const bytes = res.blob !== undefined ? Buffer.from(res.blob, "base64").length : Buffer.byteLength(res.text ?? "");
      parts.push(`[resource ${res.mimeType ?? "unknown"} ${bytes} B]`);
    } else parts.push(`[${block.type}]`);
  }
  return parts.join("\n");
}

function readAllStdin(): Promise<string> {
  return new Promise((resolve, reject) => {
    let data = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => (data += chunk));
    process.stdin.on("end", () => resolve(data));
    process.stdin.on("error", reject);
  });
}

function finish(stream: NodeJS.WriteStream, text: string, code: number): void {
  const out = text.endsWith("\n") || text === "" ? text : `${text}\n`;
  stream.write(out, () => process.exit(code));
}

// Exit codes: 0 ok, 1 tool error, 2 usage error.
export async function runToolCommand(short: string | undefined, argv: string[]): Promise<void> {
  suppressSubagentEnvLog();
  const name = `cave__${short ?? ""}`;
  if (!short || !isToolName(name)) {
    finish(process.stderr, `cave-tools: unknown tool "${short ?? ""}". Run \`cave-tools help\` for the list.`, 2);
    return;
  }
  const tool = await loadTool(name);
  let parsed: ParsedToolArgs;
  try {
    parsed = await parseToolArgs(tool, argv, readAllStdin);
  } catch (error) {
    if (!(error instanceof UsageError)) throw error;
    finish(process.stderr, `cave-tools ${short}: ${error.message}\nusage: ${usageLine(tool)}`, 2);
    return;
  }
  if (parsed.help) {
    finish(process.stdout, toolHelp(tool), 0);
    return;
  }
  const sessionId = parsed.sessionId ?? `cli-${process.pid}-${randomBytes(4).toString("hex")}`;
  let result: CallToolResult;
  // Only a call this process ran itself has local stats worth writing; a
  // daemon-served call is accounted in the daemon under this client's pid.
  let ranLocally = false;
  try {
    let outcome: CallOutcome = { sent: false };
    if (daemonEnabled()) {
      outcome = await getDaemonClient({ kind: "cli", explicitSession: parsed.sessionId !== undefined })
        .call(name, parsed.args, { sessionId, cwd: process.cwd(), env: process.env });
    }
    // In-process unless the daemon actually received the call.
    if (outcome.sent) {
      result = outcome.result;
    } else {
      ranLocally = true;
      result = await callTool(name, parsed.args, { sessionId });
    }
  } catch (error) {
    if (ranLocally) flushStats();
    finish(process.stderr, `cave-tools ${short}: ${error instanceof Error ? error.message : String(error)}`, 1);
    return;
  }
  if (ranLocally) flushStats();
  if (result.isError) finish(process.stderr, formatResult(result), 1);
  else finish(process.stdout, formatResult(result), 0);
}

export async function runHelpCommand(short: string | undefined): Promise<void> {
  if (!short) {
    finish(process.stdout, await generalHelp(), 0);
    return;
  }
  const name = `cave__${short}`;
  if (!isToolName(name)) {
    finish(process.stderr, `cave-tools: unknown tool "${short}". Run \`cave-tools help\` for the list.`, 2);
    return;
  }
  finish(process.stdout, toolHelp(await loadTool(name)), 0);
}
