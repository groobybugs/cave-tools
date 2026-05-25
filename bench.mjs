import { readFileSync, statSync, existsSync } from "node:fs";
import { execSync } from "node:child_process";
import {
  applyBudget,
  stripAnsi,
  collapseBlankLines,
  truncateLines,
  extractStructuredData,
  isFileUnchanged,
  updateFileCache,
} from "./dist/compression/utils.js";

const CHARS_PER_TOKEN = 4; // Anthropic rule-of-thumb

function pct(before, after) {
  if (before === 0) return "0%";
  return (((before - after) / before) * 100).toFixed(1) + "%";
}

function tokens(s) {
  return Math.ceil((typeof s === "number" ? s : s.length) / CHARS_PER_TOKEN);
}

function row(label, before, after) {
  const saved = before - after;
  return [
    label.padEnd(36),
    `${before.toString().padStart(8)} chars`,
    `→ ${after.toString().padStart(8)} chars`,
    `(~${tokens(before).toString().padStart(6)} → ${tokens(after).toString().padStart(6)} tok)`,
    `saved ${saved.toString().padStart(8)} chars`,
    pct(before, after).padStart(7),
  ].join("  ");
}

console.log("=".repeat(120));
console.log("Cave Tools — Compression Benchmark");
console.log("=".repeat(120));
console.log(`(token estimate: ${CHARS_PER_TOKEN} chars/token, Anthropic rule-of-thumb)\n`);

// --- Fixture 1: Flint Chipper on a long real file (read budget) -----------
{
  console.log("--- Flint Chipper: long source file (read budget = 300 lines) ---");
  // Use this repo's own README or a fat node_modules file as fixture
  const candidates = [
    "./README.md",
    "./pnpm-lock.yaml",
    "./node_modules/typescript/lib/typescript.js",
  ];
  for (const f of candidates) {
    if (!existsSync(f)) continue;
    const raw = readFileSync(f, "utf-8");
    const out = applyBudget(raw, "read");
    console.log(row(f, raw.length, out.length));
  }
  console.log();
}

// --- Fixture 2: Flint Chipper on bash output (bash budget = 80 lines) -----
{
  console.log("--- Flint Chipper: bash output (bash budget = 80 lines) ---");
  const cases = [
    ["git log --oneline -n 500", () => execSync("git log --oneline -n 500", { encoding: "utf-8" })],
    ["find . -type f (node_modules excl.)", () => execSync("find . -type f -not -path '*/node_modules/*' | head -2000", { encoding: "utf-8" })],
    ["ls -la node_modules", () => execSync("ls -la node_modules 2>/dev/null | head -500", { encoding: "utf-8" })],
  ];
  for (const [label, fn] of cases) {
    try {
      const raw = fn();
      const out = applyBudget(raw, "bash");
      console.log(row(label, raw.length, out.length));
    } catch {
      console.log(`  ${label}: skipped (command failed)`);
    }
  }
  console.log();
}

// --- Fixture 3: ANSI stripping --------------------------------------------
{
  console.log("--- stripAnsi: colorized git log ---");
  try {
    const raw = execSync("git log --color=always --pretty=format:'%C(yellow)%h%Creset %C(cyan)%an%Creset %s' -n 200", { encoding: "utf-8" });
    const out = stripAnsi(raw);
    console.log(row("git log --color=always (200)", raw.length, out.length));
  } catch {
    console.log("  skipped");
  }
  console.log();
}

// --- Fixture 4: collapseBlankLines ----------------------------------------
{
  console.log("--- collapseBlankLines: synthetic 100x4-blank ---");
  const raw = ("line\n" + "\n".repeat(4)).repeat(100);
  const out = collapseBlankLines(raw);
  console.log(row("100 stanzas × 4 blank lines", raw.length, out.length));
  console.log();
}

// --- Fixture 5: Stone Tablet (JSON re-encode) -----------------------------
{
  console.log("--- Stone Tablet: pretty JSON → compact ---");
  const sample = JSON.stringify(
    {
      events: Array.from({ length: 50 }, (_, i) => ({
        id: `evt_${i}`,
        name: `Event ${i}`,
        venue: { name: "Daily Bread Food Bank", address: { street: "191 New Toronto St", city: "Etobicoke", province: "ON", postal: "M8V 2E7" } },
        attendees: Array.from({ length: 20 }, (_, j) => ({ id: j, status: "confirmed" })),
      })),
    },
    null,
    4,
  );
  const out = extractStructuredData(sample);
  console.log(row("50 events × 20 attendees JSON", sample.length, out.length));
  console.log();
}

// --- Fixture 6: Stone Tablet (XML minify) ---------------------------------
{
  console.log("--- Stone Tablet: pretty XML → minified ---");
  const xml =
    "<root>\n" +
    Array.from({ length: 100 }, (_, i) =>
      `  <event id="${i}">\n    <name>Event ${i}</name>\n    <attendees>\n      <count>20</count>\n    </attendees>\n  </event>\n`,
    ).join("") +
    "</root>\n";
  const out = extractStructuredData(xml);
  console.log(row("100-event XML doc", xml.length, out.length));
  console.log();
}

// --- Fixture 7: Dedup cache (the actual mega-win) -------------------------
{
  console.log("--- Dedup: 2nd read of unchanged file ---");
  const f = "./README.md";
  if (existsSync(f)) {
    updateFileCache(f);
    const raw = readFileSync(f, "utf-8");
    const stub = isFileUnchanged(f) ? "<file unchanged since last read>" : "(would re-read)";
    console.log(row(`${f} (2nd read)`, raw.length, stub.length));
  }
  console.log();
}

// --- Combined pipeline (what cave__read actually does) --------------------
{
  console.log("--- Combined: cave__read full pipeline on a fat source file ---");
  const f = "./pnpm-lock.yaml";
  if (existsSync(f)) {
    const raw = readFileSync(f, "utf-8");
    const out = applyBudget(raw, "read");
    console.log(row(`${f} (raw → applyBudget)`, raw.length, out.length));
  }
}

console.log("\n" + "=".repeat(120));
console.log("Done. Reproduce: `node bench.mjs` from the cave-tools repo root.");
console.log("=".repeat(120));
