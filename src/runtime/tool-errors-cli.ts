// Minimal local reader for the persistent tool-error log.
// Read-only: never writes. Best-effort: an unreadable store prints zero
// records, never an error trace.
import { countToolErrors, listToolErrors, toolErrorsDbPath } from "./tool-errors.js";

function usage(): never {
  console.error("Usage: cave-tools errors [--limit N]");
  process.exit(2);
}

function parseLimit(argv: string[]): number {
  if (argv.length === 0) return 20;
  let value: string | undefined;
  if (argv.length === 2 && argv[0] === "--limit") {
    value = argv[1];
  } else if (argv.length === 1 && argv[0].startsWith("--limit=")) {
    value = argv[0].slice("--limit=".length);
  } else {
    usage();
  }
  // Integer only: fractional, negative, empty, or non-numeric values exit 2.
  if (value === undefined || !/^\d+$/.test(value)) usage();
  const n = Number(value);
  if (!Number.isSafeInteger(n) || n < 1) usage();
  return Math.min(500, n);
}

export async function runErrorsCommand(argv: string[]): Promise<number> {
  const limit = parseLimit(argv);
  const counts = countToolErrors();
  console.log(`error log: ${toolErrorsDbPath()}`);
  console.log(`total: ${counts.total}`);
  if (counts.total === 0) {
    console.log("No tool errors recorded.");
    return 0;
  }
  const byCode = Object.entries(counts.byCode)
    .sort((a, b) => b[1] - a[1])
    .map(([k, v]) => `${k}=${v}`)
    .join(" ");
  console.log(`by code: ${byCode}`);
  const byTool = Object.entries(counts.byTool)
    .sort((a, b) => b[1] - a[1])
    .map(([k, v]) => `${k}=${v}`)
    .join(" ");
  console.log(`by tool: ${byTool}`);
  console.log(`recent (last ${limit}):`);
  for (const r of listToolErrors(limit)) {
    const when = new Date(r.createdAt).toISOString();
    const op = r.operation ? ` op=${r.operation}` : "";
    const batch = r.batchIndex !== null ? ` batch=${r.batchIndex}` : "";
    const file = r.fileHash ? ` file=${r.fileHash}` : "";
    const kind = r.errorKind ? ` kind=${r.errorKind}` : "";
    const args =
      r.argKeys.length > 0 || r.unknownArgCount > 0
        ? ` args=[${r.argKeys.join(",")}]${r.unknownArgCount > 0 ? ` +${r.unknownArgCount} unknown` : ""}`
        : "";
    console.log(
      `${when} id=${r.id} tool=${r.tool} outcome=${r.outcome} code=${r.reasonCode}` +
        ` elapsed=${r.elapsedMs}ms mode=${r.mode} agent=${r.agent}${op}${batch}` +
        ` session=${r.sessionHash}${file}${kind}${args}`,
    );
  }
  return 0;
}
