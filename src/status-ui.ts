import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export interface ToolBreakdown {
  name: string;
  calls: number;
  rawChars: number;
  savedChars: number;
}

export interface RtkGainSummary {
  totalCommands: number;
  totalInput: number;
  totalOutput: number;
  totalSaved: number;
  avgSavingsPct: number;
  totalTimeMs: number;
  avgTimeMs: number;
}

export interface StatusRenderInput {
  rtkAvailable: boolean;
  reductionPct: number;
  hitRatePct: number;
  cacheHits: number;
  cacheMisses: number;
  filesTracked: number;
  rtkRewrites: number;
  rtkAlreadyWrapped: number;
  rtkPassthrough: number;
  totalCalls: number;
  rawChars: number;
  compressedChars: number;
  compressionSavedChars: number;
  dedupSavedChars: number;
  savedChars: number;
  tokensSaved: number;
  liveSessions: number;
  endedSessions: number;
  lastUpdate: number;
  hasData: boolean;
  budgets: Record<string, { maxLines: number; headLines: number; tailLines: number }>;
  byTool: ToolBreakdown[];
  rtkGain: RtkGainSummary | null;
  rtkGainSkipped: boolean;
  verbose: boolean;
}

// ── color / style ──────────────────────────────────────────────────

function envTruthy(name: string): boolean {
  const v = process.env[name];
  return v !== undefined && v !== "" && v !== "0" && v.toLowerCase() !== "false";
}

export function supportsColor(): boolean {
  if (envTruthy("NO_COLOR")) return false;
  if (process.env.CAVE_TOOLS_ASCII === "1") return false;
  if (envTruthy("FORCE_COLOR")) return true;
  return Boolean(process.stdout.isTTY);
}

export function useAscii(): boolean {
  return process.env.CAVE_TOOLS_ASCII === "1";
}

const C = {
  reset: "\x1b[0m",
  bold: "\x1b[1m",
  dim: "\x1b[2m",
  amber: "\x1b[38;5;214m",
  green: "\x1b[38;5;114m",
  brightGreen: "\x1b[38;5;48m",
  cyan: "\x1b[38;5;81m",
  red: "\x1b[38;5;203m",
  gray: "\x1b[38;5;240m",
  white: "\x1b[38;5;255m",
  yellow: "\x1b[38;5;221m",
};

type Style = keyof typeof C;

function paint(enabled: boolean, style: Style, text: string): string {
  if (!enabled) return text;
  return `${C[style]}${text}${C.reset}`;
}

function multi(enabled: boolean, styles: Style[], text: string): string {
  if (!enabled) return text;
  return `${styles.map((s) => C[s]).join("")}${text}${C.reset}`;
}

// ── formatters ─────────────────────────────────────────────────────

/** Compact number: 87525037 → 87.5M, 1200 → 1.2k */
export function formatCompact(n: number, digits = 1): string {
  const abs = Math.abs(n);
  const sign = n < 0 ? "-" : "";
  if (abs >= 1_000_000_000) return `${sign}${(abs / 1_000_000_000).toFixed(digits).replace(/\.0$/, "")}B`;
  if (abs >= 1_000_000) return `${sign}${(abs / 1_000_000).toFixed(digits).replace(/\.0$/, "")}M`;
  if (abs >= 10_000) return `${sign}${(abs / 1_000).toFixed(0)}k`;
  if (abs >= 1_000) return `${sign}${(abs / 1_000).toFixed(digits).replace(/\.0$/, "")}k`;
  return `${sign}${Math.round(abs)}`;
}

export function formatTokens(n: number): string {
  return formatCompact(n);
}

function pad(s: string, w: number, right = false): string {
  // Strip ANSI for width calc
  const bare = s.replace(/\x1b\[[0-9;]*m/g, "");
  const padLen = Math.max(0, w - bare.length);
  const spaces = " ".repeat(padLen);
  return right ? spaces + s : s + spaces;
}

function ageString(lastUpdate: number): string {
  if (lastUpdate <= 0) return "—";
  const age = Math.round((Date.now() - lastUpdate) / 1000);
  if (age < 60) return `${age}s ago`;
  if (age < 3600) return `${Math.round(age / 60)}m ago`;
  return `${Math.round(age / 3600)}h ago`;
}

export function coloredMeter(percent: number, width = 22, color = true): string {
  const clamped = Math.max(0, Math.min(100, percent));
  const filled = Math.round((clamped / 100) * width);
  const empty = width - filled;
  const fillChar = useAscii() ? "#" : "█";
  const emptyChar = useAscii() ? "-" : "░";
  const bar = fillChar.repeat(filled) + emptyChar.repeat(empty);
  if (!color) return bar;
  // green → amber gradient by band
  let style: Style = "green";
  if (clamped >= 70) style = "brightGreen";
  else if (clamped >= 40) style = "amber";
  else if (clamped > 0) style = "yellow";
  else style = "gray";
  return paint(true, style, fillChar.repeat(filled)) + paint(true, "gray", emptyChar.repeat(empty));
}

// ── RTK gain fetch ─────────────────────────────────────────────────

export async function fetchRtkGainSummary(): Promise<RtkGainSummary | null> {
  if (process.env.CAVE_TOOLS_STATUS_RTK === "0") return null;
  try {
    const { stdout } = await execFileAsync("rtk", ["gain", "-f", "json"], {
      encoding: "utf-8",
      timeout: 2500,
      maxBuffer: 2 * 1024 * 1024,
    });
    const data = JSON.parse(stdout) as {
      summary?: {
        total_commands?: number;
        total_input?: number;
        total_output?: number;
        total_saved?: number;
        avg_savings_pct?: number;
        total_time_ms?: number;
        avg_time_ms?: number;
      };
    };
    const s = data.summary;
    if (!s) return null;
    return {
      totalCommands: s.total_commands ?? 0,
      totalInput: s.total_input ?? 0,
      totalOutput: s.total_output ?? 0,
      totalSaved: s.total_saved ?? 0,
      avgSavingsPct: s.avg_savings_pct ?? 0,
      totalTimeMs: s.total_time_ms ?? 0,
      avgTimeMs: s.avg_time_ms ?? 0,
    };
  } catch {
    return null;
  }
}

// ── box drawing ────────────────────────────────────────────────────

function boxChars() {
  if (useAscii()) {
    return { tl: "+", tr: "+", bl: "+", br: "+", h: "-", v: "|", mid: "+" };
  }
  return { tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│", mid: "├" };
}

function visibleLen(s: string): number {
  return s.replace(/\x1b\[[0-9;]*m/g, "").length;
}

function boxLine(content: string, width: number, color: boolean): string {
  const b = boxChars();
  const inner = width - 2;
  const bare = visibleLen(content);
  const padRight = Math.max(0, inner - bare);
  const side = paint(color, "gray", b.v);
  return `  ${side}${content}${" ".repeat(padRight)}${side}`;
}

function boxTop(width: number, color: boolean): string {
  const b = boxChars();
  return `  ${paint(color, "gray", b.tl + b.h.repeat(width - 2) + b.tr)}`;
}

function boxBottom(width: number, color: boolean): string {
  const b = boxChars();
  return `  ${paint(color, "gray", b.bl + b.h.repeat(width - 2) + b.br)}`;
}

function rule(width: number, color: boolean): string {
  const ch = useAscii() ? "-" : "─";
  return paint(color, "gray", ch.repeat(width));
}

// ── main renderer ──────────────────────────────────────────────────

export function renderStatusCli(input: StatusRenderInput): string {
  const color = supportsColor();
  const w = 57;
  const icon = useAscii() ? "*" : "⛏";
  const lines: string[] = [];

  // Header box
  lines.push("");
  lines.push(boxTop(w, color));
  lines.push(
    boxLine(
      `  ${paint(color, "amber", icon)}  ${multi(color, ["bold", "amber"], "cave-tools")}${paint(color, "dim", " · status")}`,
      w,
      color,
    ),
  );
  lines.push(boxLine("", w, color));

  if (!input.hasData) {
    lines.push(
      boxLine(
        `  ${paint(color, "dim", "No session data yet — start an MCP session first.")}`,
        w,
        color,
      ),
    );
    lines.push(boxLine("", w, color));
    lines.push(
      boxLine(
        `  ${paint(color, "dim", "RTK")}  ${input.rtkAvailable ? paint(color, "green", "● online") : paint(color, "red", "○ offline")}`,
        w,
        color,
      ),
    );
    lines.push(boxBottom(w, color));
    lines.push("");
    appendRtkGain(lines, input, color);
    appendBudgets(lines, input, color, true);
    lines.push("");
    return lines.join("\n");
  }

  // Hero KPIs: tokens · compress% · calls · cache hit
  const tokVal = formatTokens(input.tokensSaved);
  const pctVal = `${input.reductionPct.toFixed(0)}%`;
  const callsVal = formatCompact(input.totalCalls, 0);
  const hitVal = `${input.hitRatePct.toFixed(0)}%`;

  const col = 12;
  const v1 = pad(multi(color, ["bold", "brightGreen"], tokVal), col);
  const v2 = pad(multi(color, ["bold", "amber"], pctVal), col);
  const v3 = pad(multi(color, ["bold", "cyan"], callsVal), col);
  const v4 = pad(multi(color, ["bold", "white"], hitVal), col);
  lines.push(boxLine(`  ${v1}${v2}${v3}${v4}`, w, color));

  const ul = useAscii() ? "----" : "────";
  const u1 = pad(paint(color, "green", ul.slice(0, Math.min(4, tokVal.length))), col);
  const u2 = pad(paint(color, "amber", ul.slice(0, Math.min(4, pctVal.length))), col);
  const u3 = pad(paint(color, "cyan", ul.slice(0, Math.min(4, callsVal.length))), col);
  const u4 = pad(paint(color, "gray", ul.slice(0, Math.min(4, hitVal.length))), col);
  lines.push(boxLine(`  ${u1}${u2}${u3}${u4}`, w, color));

  const l1 = pad(paint(color, "dim", "tokens"), col);
  const l2 = pad(paint(color, "dim", "compress"), col);
  const l3 = pad(paint(color, "dim", "calls"), col);
  const l4 = pad(paint(color, "dim", "cache hit"), col);
  lines.push(boxLine(`  ${l1}${l2}${l3}${l4}`, w, color));
  lines.push(boxLine("", w, color));

  // Meter
  const meter = coloredMeter(input.reductionPct, 22, color);
  const meterPct = multi(color, ["bold", "amber"], `${input.reductionPct.toFixed(1)}%`);
  lines.push(boxLine(`  ${meter}  ${meterPct}`, w, color));
  lines.push(boxLine("", w, color));

  // Session meta
  const sessions = `${input.liveSessions} live · ${input.endedSessions} ended`;
  const meta = paint(
    color,
    "dim",
    `${sessions} · updated ${ageString(input.lastUpdate)}`,
  );
  lines.push(boxLine(`  ${meta}`, w, color));

  // RTK rewrite status pill
  const rtkDot = input.rtkAvailable
    ? paint(color, "green", "● online")
    : paint(color, "red", "○ offline");
  lines.push(boxLine(`  ${paint(color, "dim", "RTK")}  ${rtkDot}`, w, color));

  lines.push(boxBottom(w, color));
  lines.push("");

  // By tool table
  appendByTool(lines, input, color);

  // RTK gain panel (shell savings — separate layer)
  appendRtkGain(lines, input, color);

  // Health / secondary
  appendHealth(lines, input, color);

  // Budgets
  appendBudgets(lines, input, color, input.verbose);

  // Verbose detail
  if (input.verbose) {
    appendVerbose(lines, input, color);
  }

  lines.push("");
  return lines.join("\n");
}

function appendByTool(lines: string[], input: StatusRenderInput, color: boolean): void {
  const tools = [...input.byTool]
    .filter((t) => t.calls > 0 || t.savedChars > 0)
    .sort((a, b) => b.savedChars - a.savedChars)
    .slice(0, 10);

  if (tools.length === 0) return;

  const maxSaved = Math.max(...tools.map((t) => t.savedChars), 1);
  lines.push(`  ${multi(color, ["bold", "amber"], "by tool")}`);
  lines.push(`  ${rule(52, color)}`);
  lines.push(
    `  ${paint(color, "dim", pad("#", 3) + pad("tool", 10) + pad("calls", 8, true) + pad("saved", 9, true) + pad("avg%", 8, true) + "  impact")}`,
  );
  lines.push(`  ${rule(52, color)}`);

  tools.forEach((t, i) => {
    const tok = Math.round(t.savedChars / 4);
    const avg =
      t.rawChars > 0 ? ((t.savedChars / t.rawChars) * 100).toFixed(0) : "0";
    const impactW = 10;
    const filled = Math.round((t.savedChars / maxSaved) * impactW);
    const impact = coloredMeter((filled / impactW) * 100, impactW, color);
    const s1 = pad(String(i + 1), 3);
    const s2 = pad(t.name.slice(0, 9), 10);
    const s3 = pad(formatCompact(t.calls, 0), 8, true);
    const s4Bare = formatTokens(tok);
    const s4 = " ".repeat(Math.max(0, 9 - s4Bare.length)) + paint(color, "green", s4Bare);
    const s5 = pad(`${avg}%`, 8, true);
    lines.push(`  ${paint(color, "dim", s1)}${s2}${s3}${s4}${s5}  ${impact}`);
  });
  lines.push(`  ${rule(52, color)}`);
  lines.push("");
}

function appendRtkGain(lines: string[], input: StatusRenderInput, color: boolean): void {
  lines.push(`  ${multi(color, ["bold", "amber"], "rtk gain")}${paint(color, "dim", "  · shell compression (separate layer)")}`);
  lines.push(`  ${rule(52, color)}`);

  if (input.rtkGainSkipped) {
    lines.push(`  ${paint(color, "dim", "skipped (CAVE_TOOLS_STATUS_RTK=0)")}`);
    lines.push("");
    return;
  }

  if (!input.rtkAvailable) {
    lines.push(`  ${paint(color, "red", "○")} ${paint(color, "dim", "rtk not installed — cave__bash will not rewrite")}`);
    lines.push("");
    return;
  }

  if (!input.rtkGain) {
    lines.push(`  ${paint(color, "yellow", "●")} ${paint(color, "dim", "online, but gain data unavailable")}`);
    lines.push("");
    return;
  }

  const g = input.rtkGain;
  const tok = formatTokens(g.totalSaved);
  const pct = `${g.avgSavingsPct.toFixed(1)}%`;
  const cmds = formatCompact(g.totalCommands, 0);
  lines.push(
    `  ${multi(color, ["bold", "brightGreen"], tok)} ${paint(color, "dim", "tok saved")}` +
      `  ${multi(color, ["bold", "amber"], pct)}` +
      `  ${paint(color, "cyan", cmds)} ${paint(color, "dim", "cmds")}`,
  );
  const meter = coloredMeter(g.avgSavingsPct, 22, color);
  lines.push(`  ${meter}  ${multi(color, ["bold", "amber"], pct)}`);
  if (input.verbose && g.totalInput > 0) {
    lines.push(
      `  ${paint(color, "dim", `in ${formatTokens(g.totalInput)} → out ${formatTokens(g.totalOutput)}`)}`,
    );
  }
  lines.push(`  ${rule(52, color)}`);
  lines.push("");
}

function appendHealth(lines: string[], input: StatusRenderInput, color: boolean): void {
  lines.push(`  ${multi(color, ["bold", "amber"], "health")}`);
  lines.push(
    `  ${paint(color, "dim", "rewrites")} ${input.rtkRewrites}` +
      `  ${paint(color, "dim", "·")}  ${paint(color, "dim", "wrapped")} ${input.rtkAlreadyWrapped}` +
      `  ${paint(color, "dim", "·")}  ${paint(color, "dim", "pass")} ${input.rtkPassthrough}`,
  );
  lines.push(
    `  ${paint(color, "dim", "dedup")} ${formatCompact(input.cacheHits, 0)} hits` +
      `  ${paint(color, "dim", "·")}  ${paint(color, "green", formatTokens(Math.round(input.dedupSavedChars / 4)))} tok avoided`,
  );
  if (input.filesTracked > 0) {
    lines.push(`  ${paint(color, "dim", "files tracked")} ${input.filesTracked}`);
  }
  lines.push("");
}

function appendBudgets(
  lines: string[],
  input: StatusRenderInput,
  color: boolean,
  full: boolean,
): void {
  const entries = Object.entries(input.budgets);
  if (entries.length === 0) return;

  lines.push(`  ${multi(color, ["bold", "amber"], "budgets")}`);
  if (full) {
    for (const [name, b] of entries) {
      lines.push(
        `  ${pad(name, 12)} ${paint(color, "dim", "max")}=${b.maxLines}  ${paint(color, "dim", "head")}=${b.headLines}  ${paint(color, "dim", "tail")}=${b.tailLines}`,
      );
    }
  } else {
    // compact one-liners, 2 per row-ish
    const parts = entries.map(
      ([name, b]) => `${name} ${paint(color, "dim", `${b.maxLines}/${b.headLines}/${b.tailLines}`)}`,
    );
    // chunk into lines of ~3
    for (let i = 0; i < parts.length; i += 3) {
      lines.push(`  ${parts.slice(i, i + 3).join(paint(color, "dim", "  ·  "))}`);
    }
    lines.push(`  ${paint(color, "dim", "hint: cave-tools status --verbose")}`);
  }
  lines.push("");
}

function appendVerbose(lines: string[], input: StatusRenderInput, color: boolean): void {
  lines.push(`  ${multi(color, ["bold", "amber"], "detail")}`);
  lines.push(
    `  ${paint(color, "dim", "raw chars")}      ${formatCompact(input.rawChars)}`,
  );
  lines.push(
    `  ${paint(color, "dim", "trimmed chars")}  ${formatCompact(input.compressedChars)}`,
  );
  lines.push(
    `  ${paint(color, "dim", "saved chars")}    ${formatCompact(input.compressionSavedChars)}`,
  );
  lines.push(
    `  ${paint(color, "dim", "dedup chars")}    ${formatCompact(input.dedupSavedChars)}`,
  );
  lines.push(
    `  ${paint(color, "dim", "cache")}          ${input.cacheHits} hit / ${input.cacheMisses} miss`,
  );
  lines.push("");
}
