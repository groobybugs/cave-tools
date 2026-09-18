#!/usr/bin/env node
// check-upstream-drift.mjs — compare cave-tools behavior constants against
// upstream sst/opencode at a given ref (default v2.0.7).
//
// Read-only: fetches raw files from GitHub, compares against local sources.
// Usage: node scripts/check-upstream-drift.mjs [ref]
// Exit code: 0 = no drift, 1 = drift found or fetch failed.

import { readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";

const ref = process.argv[2] || "v2.0.7";
const BASE = `https://raw.githubusercontent.com/sst/opencode/${ref}/`;
const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

const upstreamCache = new Map();

async function upstream(path) {
  if (!upstreamCache.has(path)) {
    const res = await fetch(BASE + path);
    if (!res.ok) throw new Error(`fetch ${path}: HTTP ${res.status}`);
    upstreamCache.set(path, res.text());
  }
  return upstreamCache.get(path);
}

function local(path) {
  return readFileSync(join(ROOT, path), "utf-8");
}

// Normalize numeric expressions like "20 * 1024 * 1024" or "30_000" to a number.
function num(value) {
  const clean = String(value).replace(/_/g, "").trim();
  if (/^[\d\s*]+$/.test(clean)) {
    return clean.split("*").reduce((acc, part) => acc * Number(part.trim()), 1);
  }
  return clean;
}

const results = [];
function report(label, ok, detail) {
  results.push({ label, ok, detail });
  console.log(`${ok ? "OK   " : "DRIFT"}  ${label}${detail ? ` — ${detail}` : ""}`);
}

async function valueCheck(label, upFile, upRe, caveFile, caveRe, opts = {}) {
  try {
    const upText = await upstream(upFile);
    const upMatch = upText.match(upRe);
    if (!upMatch) return report(label, false, `upstream pattern not found in ${upFile}`);
    const upVal = opts.raw ? upMatch[1].trim() : num(upMatch[1]);
    if (caveFile === null) return report(label, true, `upstream=${upVal} (cave: n/a, informational)`);
    const caveText = local(caveFile);
    const caveMatch = caveText.match(caveRe);
    if (!caveMatch) return report(label, false, `cave pattern not found in ${caveFile}`);
    const caveVal = opts.raw ? caveMatch[1].trim() : num(caveMatch[1]);
    const same = opts.contains
      ? String(caveVal).includes(String(upVal)) || String(upVal).includes(String(caveVal))
      : upVal === caveVal;
    // Known intentional deltas still pass, but stay visible in the output.
    if (!same && opts.allow?.(upVal, caveVal)) {
      return report(label, true, `upstream=${upVal} cave=${caveVal} (intentional)`);
    }
    report(label, same, `upstream=${upVal} cave=${caveVal}`);
  } catch (error) {
    report(label, false, String(error.message || error));
  }
}

async function markerCheck(label, upFile, { must = [], mustNot = [], caveFile = null, caveMust = [] }) {
  try {
    const upText = await upstream(upFile);
    const missing = must.filter((re) => !re.test(upText));
    const stray = mustNot.filter((re) => re.test(upText));
    if (missing.length > 0) return report(label, false, `upstream missing: ${missing.map(String).join(", ")}`);
    if (stray.length > 0) return report(label, false, `upstream unexpectedly has: ${stray.map(String).join(", ")}`);
    if (caveFile) {
      const caveText = local(caveFile);
      const caveMissing = caveMust.filter((re) => !re.test(caveText));
      if (caveMissing.length > 0) {
        return report(label, false, `cave ${caveFile} missing: ${caveMissing.map(String).join(", ")}`);
      }
    }
    report(label, true, "markers hold");
  } catch (error) {
    report(label, false, String(error.message || error));
  }
}

const FS = "packages/core/src/filesystem.ts";
const RFS = "packages/core/src/tool/read-filesystem.ts";

await valueCheck("grep default limit", FS, /DEFAULT_SEARCH_LIMIT\s*=\s*([\d_]+)/, "src/tools/grep.ts", /DEFAULT_LIMIT\s*=\s*(\d+)/);
await valueCheck(
  "search timeout",
  FS,
  /DEFAULT_SEARCH_TIMEOUT_MS\s*=\s*([\d_]+)/,
  null,
  null,
);
await valueCheck("read max lines", RFS, /MAX_READ_LINES\s*=\s*([\d_]+)/, "src/tools/read.ts", /Number\(args\.limit\)\s*\|\|\s*(\d+)/, {
  allow: (up, cave) => Number(cave) < Number(up), // cave default 200 < upstream 2000 by design
});
await valueCheck("read max bytes", RFS, /MAX_READ_BYTES\s*=\s*([\d_\s*]+)/, "src/tools/read.ts", /const MAX_BYTES\s*=\s*([\d_\s*]+);/);
await valueCheck("media ingest bytes", RFS, /MAX_MEDIA_INGEST_BYTES\s*=\s*([\d_\s*]+)/, "src/tools/read.ts", /const MAX_IMAGE_BYTES\s*=\s*([\d_\s*]+);/);
await valueCheck("read max line length", RFS, /MAX_LINE_LENGTH\s*=\s*([\d_]+)/, "src/tools/read.ts", /const MAX_LINE_LENGTH\s*=\s*(\d+);/);
await valueCheck("shell default timeout ms", "packages/core/src/tool/plugin/shell.ts", /DEFAULT_TIMEOUT_MS\s*=\s*([\d_\s*]+)/, "src/tools/bash.ts", /const DEFAULT_TIMEOUT\s*=\s*([\d_\s*]+);/);
await valueCheck(
  "webfetch user agent",
  "packages/core/src/tool/plugin/webfetch.ts",
  /"(Mozilla\/5\.0[^"]*OpenCode-User[^"]*)"/,
  "src/tools/webfetch.ts",
  /"(Mozilla\/5\.0[^"]*OpenCode-User[^"]*)"/,
  { raw: true },
);
await valueCheck("webfetch max response bytes", "packages/core/src/tool/html-markdown.ts", /MAX_MARKDOWN_BYTES\s*=\s*([\d_\s*]+)/, "src/tools/webfetch.ts", /const MAX_RESPONSE_BYTES\s*=\s*([\d_\s*]+);/);
await valueCheck("webfetch default timeout s", "packages/core/src/tool/plugin/webfetch.ts", /DEFAULT_TIMEOUT_SECONDS\s*=\s*(\d+)/, "src/tools/webfetch.ts", /const DEFAULT_TIMEOUT_SECONDS\s*=\s*(\d+);/);
await valueCheck("webfetch max timeout s", "packages/core/src/tool/plugin/webfetch.ts", /MAX_TIMEOUT_SECONDS\s*=\s*(\d+)/, "src/tools/webfetch.ts", /const MAX_TIMEOUT_SECONDS\s*=\s*(\d+);/);

await markerCheck("edit matching tiers", "packages/core/src/tool/plugin/edit.ts", {
  must: [/findOccurrences/, /normalizeForMatch/, /findLineOccurrences/],
  mustNot: [/BlockAnchorReplacer/, /WhitespaceNormalizedReplacer/],
  caveFile: "src/tools/replacers.ts",
  caveMust: [/findOccurrences/, /normalizeForMatch/, /findLineOccurrences/],
});
await markerCheck("glob hidden param", "packages/core/src/tool/plugin/glob.ts", {
  must: [/hidden/],
  caveFile: "src/tools/find.ts",
  caveMust: [/hidden/],
});
await markerCheck("grep literal+caseSensitive", "packages/core/src/ripgrep.ts", {
  must: [/literal\?/, /caseSensitive\?/],
  caveFile: "src/runtime/ripgrep.ts",
  caveMust: [/--fixed-strings/, /--ignore-case/],
});
await markerCheck("webfetch rejects images", "packages/core/src/tool/plugin/webfetch.ts", {
  must: [/Unsupported fetched image content type/],
  caveFile: "src/tools/webfetch.ts",
  caveMust: [/Unsupported fetched image content type/],
});
await markerCheck("patch move support", "packages/core/src/tool/plugin/patch.ts", {
  must: [/moveTarget/],
  caveFile: "src/tools/apply-patch.ts",
  caveMust: [/move/],
});

const drifted = results.filter((r) => !r.ok);
console.log(`\n${results.length - drifted.length}/${results.length} checks clean against ${ref}.`);
if (drifted.length > 0) {
  console.log("Drifted:");
  for (const d of drifted) console.log(`  - ${d.label}: ${d.detail}`);
  process.exit(1);
}
