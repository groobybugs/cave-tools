// Semantic structured compression for bash tool output. Detects JSON/XML,
// keeps relevant keys based on the originating command, caps arrays, truncates
// long strings, and strips XML namespace boilerplate. Gated behind a flag so
// the existing whitespace-only extractStructuredData remains the safe fallback.

type OutputFormat = "json" | "xml" | "text";

// Keywords commonly associated with specific JSON keys in CLI output. When
// the bash command matches one of these patterns, the listed keys are
// prioritized during semantic JSON compression.
const COMMAND_KEY_HINTS: Record<string, string[]> = {
  "docker inspect": ["State", "Config", "NetworkSettings", "Mounts", "HostConfig"],
  "docker ps": ["Names", "Status", "Ports", "Image"],
  "npm ls": ["name", "version", "dependencies"],
  "package.json": ["name", "version", "scripts", "dependencies", "devDependencies"],
  tsconfig: ["compilerOptions", "include", "exclude"],
  kubectl: ["metadata", "spec", "status"],
  "aws ": ["Arn", "Name", "Status", "State", "Id"],
};

// Module-level toggle. CAVE_TOOLS_STRUCTURED_COMPRESSION=off forces the
// fallback path; everything else (including unset) leaves it enabled. Exposed
// via setStructuredCompression / isStructuredCompressionEnabled so tests and
// future config tools can flip it without restarting.
let structuredCompressionEnabled =
  process.env.CAVE_TOOLS_STRUCTURED_COMPRESSION !== "off";

export function isStructuredCompressionEnabled(): boolean {
  return structuredCompressionEnabled;
}

export function setStructuredCompression(enabled: boolean): void {
  structuredCompressionEnabled = enabled;
}

// Detect JSON / XML / text. Only triggers on outputs > 50 lines to avoid
// mutating small results where semantic compression is unlikely to help.
function detectOutputFormat(text: string): OutputFormat {
  const lines = text.split("\n");
  if (lines.length <= 50) return "text";

  const trimmed = text.trimStart();

  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    try {
      JSON.parse(trimmed);
      return "json";
    } catch {
      // Could be truncated JSON — check structural shape before giving up.
      if (/^\s*[[{]/.test(trimmed) && /[}\]]\s*$/.test(text.trimEnd())) {
        return "json";
      }
    }
  }

  if (
    trimmed.startsWith("<?xml") ||
    (trimmed.startsWith("<") && !trimmed.startsWith("<!DOCTYPE html"))
  ) {
    if (trimmed.includes("</") || trimmed.includes("/>")) {
      return "xml";
    }
  }

  return "text";
}

// Resolve which keys are likely relevant given the bash command that produced
// the output. Empty set ⇒ no command hint available.
function extractKeyHints(commandHint?: string): Set<string> {
  const hints = new Set<string>();
  if (!commandHint) return hints;

  const lower = commandHint.toLowerCase();
  for (const [pattern, keys] of Object.entries(COMMAND_KEY_HINTS)) {
    if (lower.includes(pattern.toLowerCase())) {
      for (const key of keys) hints.add(key);
    }
  }
  return hints;
}

// ----- JSON compression ----------------------------------------------------

const MAX_DEPTH = 4;
const MAX_ARRAY_ELEMENTS = 3;
const MAX_STRING_LENGTH = 200;
// Soft cap on retained top-level keys when no command hints matched.
const DEFAULT_MAX_KEYS = 8;
const HINT_FALLBACK_KEYS = 5;

function compressValue(
  value: unknown,
  relevantKeys: Set<string>,
  depth: number,
): unknown {
  if (depth > MAX_DEPTH) {
    if (Array.isArray(value)) return `[Array(${value.length})]`;
    if (typeof value === "object" && value !== null) {
      return `{Object(${Object.keys(value).length} keys)}`;
    }
    return value;
  }

  if (Array.isArray(value)) {
    if (value.length <= MAX_ARRAY_ELEMENTS) {
      return value.map((item) => compressValue(item, relevantKeys, depth + 1));
    }
    const kept = value
      .slice(0, MAX_ARRAY_ELEMENTS)
      .map((item) => compressValue(item, relevantKeys, depth + 1));
    // Error-bearing items beyond the cap carry the answer; never drop them
    // under a bare count stub. Bounded so pathological payloads stay capped.
    const ERROR_KEEP_CAP = 5;
    let errorKeeps = 0;
    for (const item of value.slice(MAX_ARRAY_ELEMENTS)) {
      if (errorKeeps >= ERROR_KEEP_CAP) break;
      if (!ERROR_VALUE_RE.test(JSON.stringify(item) ?? "")) continue;
      kept.push(compressValue(item, relevantKeys, depth + 1));
      errorKeeps++;
    }
    const dropped = value.length - MAX_ARRAY_ELEMENTS - errorKeeps;
    return [
      ...kept,
      `... ${dropped} more items (${value.length} total)${errorKeeps > 0 ? `, kept ${errorKeeps} error-bearing` : ""}`,
    ];
  }

  if (typeof value === "object" && value !== null) {
    const obj = value as Record<string, unknown>;
    const keys = Object.keys(obj);

    // Hint-driven retention: at depth 0/1 keep only the relevant keys the
    // caller is likely searching for; everything else is summarised.
    if (relevantKeys.size > 0 && depth <= 1) {
      const result: Record<string, unknown> = {};
      let kept = 0;
      const omitted: string[] = [];

      for (const key of keys) {
        if (relevantKeys.has(key)) {
          result[key] = compressValue(obj[key], relevantKeys, depth + 1);
          kept++;
        } else {
          omitted.push(key);
        }
      }

      if (kept === 0) {
        // No hints matched — fall back to first 5 keys so the call is never
        // empty.
        for (const key of keys.slice(0, HINT_FALLBACK_KEYS)) {
          result[key] = compressValue(obj[key], relevantKeys, depth + 1);
        }
        if (keys.length > HINT_FALLBACK_KEYS) {
          result["..."] = `${keys.length - HINT_FALLBACK_KEYS} more keys omitted`;
        }
      } else if (omitted.length > 0) {
        result["..."] =
          `${omitted.length} keys omitted: ${omitted.slice(0, 5).join(", ")}` +
          (omitted.length > 5 ? "..." : "");
      }

      return result;
    }

    // No hints or deeper level — keep first DEFAULT_MAX_KEYS keys.
    if (keys.length <= DEFAULT_MAX_KEYS) {
      const result: Record<string, unknown> = {};
      for (const key of keys) {
        result[key] = compressValue(obj[key], relevantKeys, depth + 1);
      }
      return result;
    }

    const result: Record<string, unknown> = {};
    for (const key of keys.slice(0, DEFAULT_MAX_KEYS)) {
      result[key] = compressValue(obj[key], relevantKeys, depth + 1);
    }
    result["..."] = `${keys.length - DEFAULT_MAX_KEYS} more keys omitted`;
    return result;
  }

  if (typeof value === "string" && value.length > MAX_STRING_LENGTH) {
    return `${value.slice(0, MAX_STRING_LENGTH)}... (${value.length} chars)`;
  }

  return value;
}

// Quality gate: don't ship a "compressed" payload that's still ≥60% of the
// original line count — at that point the caller is better off reading the
// raw output. Mirrors caveman-code's Stone Tablet heuristic.
const QUALITY_RATIO = 0.6;

function compressJson(text: string, commandHint?: string): string | null {
  const trimmed = text.trim();
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return null;
  }

  const relevantKeys = extractKeyHints(commandHint);
  const compressed = compressValue(parsed, relevantKeys, 0);
  const result = JSON.stringify(compressed, null, 2);
  const originalLines = text.split("\n").length;
  const resultLines = result.split("\n").length;

  if (resultLines >= originalLines * QUALITY_RATIO) {
    return null;
  }

  const retainedInfo =
    relevantKeys.size > 0
      ? `Keys retained: ${[...relevantKeys].join(", ")}`
      : "Top-level keys retained";

  return `${result}\n\n[JSON compressed: ${resultLines} of ${originalLines} lines. ${retainedInfo}]`;
}

// ----- XML compression ------------------------------------------------------

// Shared error vocabulary: an element or array item carrying an error state is
// never elided, however deep in a repetitive run it sits.
const ERROR_VALUE_RE =
  /\b(error|errors|exception|failed|failure|critical|fatal|crash|panic|abort|timeout|denied|rejected)\b/i;
const ERROR_KEY_RE =
  /^(error|errors|message|msg|stack|stacktrace|stack_trace|trace|traceback|exception|reason|detail|details|warning|warnings)$/i;

const XML_ATTR_RE = /\s([A-Za-z_][\w.:-]*)="([^"]*)"/g;
const XML_MARKER_RE = /^\s*<!-- \d+ <[^>]+> elements elided/;
const XML_XMLNS_RE = /\s+xmlns(?::\w+)?="[^"]*"/g;

interface XmlElem {
  name: string;
  indent: string;
  lo: number; // inclusive start line
  hi: number; // inclusive end line
}

// Index of the '>' closing the tag token that starts at index 0 of `s` (which
// must begin with '<'), honouring quoted attribute values. -1 when unterminated.
function findTagEnd(s: string): number {
  let inQuote = false;
  for (let i = 1; i < s.length; i++) {
    const ch = s[i];
    if (ch === '"') inQuote = !inQuote;
    else if (ch === '>' && !inQuote) return i;
  }
  return -1;
}

// Net opened-minus-closed tag count on one line, skipping comments, PIs,
// doctypes, CDATA and self-closing tags. null = an unterminated construct —
// the caller fails the whole transform closed rather than guess across lines.
function tagDepthDelta(line: string): number | null {
  let s = line;
  let delta = 0;
  for (;;) {
    const c = s.indexOf("<");
    if (c < 0) return delta;
    s = s.slice(c);
    if (s.startsWith("<!--")) {
      const e = s.indexOf("-->");
      if (e < 0) return null;
      s = s.slice(e + 3);
      continue;
    }
    if (s.startsWith("<![CDATA[")) {
      const e = s.indexOf("]]>");
      if (e < 0) return null;
      s = s.slice(e + 3);
      continue;
    }
    if (s.startsWith("<?")) {
      const e = s.indexOf("?>");
      if (e < 0) return null;
      s = s.slice(e + 2);
      continue;
    }
    if (s.startsWith("<!")) {
      const e = s.indexOf(">");
      if (e < 0) return null;
      s = s.slice(e + 1);
      continue;
    }
    const tokEnd = findTagEnd(s);
    if (tokEnd < 0) return null; // tag split across lines: refuse to guess
    const tok = s.slice(0, tokEnd + 1);
    if (tok.startsWith("</")) delta--;
    else if (!tok.endsWith("/>")) delta++;
    s = s.slice(tokEnd + 1);
  }
}

// Classify the line at index i. Returns null for non-element lines (blank,
// text, comments, PIs, doctypes, our own marker, close-only lines). Accepts
// every well-formed opening tag — tag-only lines, self-closing tags,
// single-line elements with content (`<item>n1</item>`), and multi-line
// starts — resolving the extent by balanced-tag counting. Throws
// UnparseableXml on an element whose extent cannot be fully accounted for.
class UnparseableXml extends Error {}

function scanElem(lines: string[], i: number, hi: number): XmlElem | null {
  const line = lines[i]!;
  const indent = line.match(/^[ \t]*/)![0];
  const rest = line.slice(indent.length);
  if (
    rest.length === 0 ||
    rest[0] !== "<" ||
    rest.startsWith("</") ||
    rest.startsWith("<!--") ||
    rest.startsWith("<?") ||
    rest.startsWith("<!") ||
    XML_MARKER_RE.test(line)
  ) {
    return null;
  }
  const nameMatch = rest.match(/^<([A-Za-z_][\w.:-]*)/);
  if (!nameMatch) return null; // `&lt;`, `<5`, other prose: never touched

  const openerEnd = findTagEnd(rest);
  if (openerEnd < 0) throw new UnparseableXml(); // tag split across lines
  const selfClosing = rest[openerEnd - 1] === "/";
  const name = nameMatch[1]!;
  let depth = 0;
  if (!selfClosing) {
    depth = 1;
    const d = tagDepthDelta(rest.slice(openerEnd + 1));
    if (d === null) throw new UnparseableXml();
    depth += d;
  }
  if (depth <= 0) {
    return { name, indent, lo: i, hi: i }; // self-closing or inline-complete
  }
  for (let j = i + 1; j <= hi; j++) {
    const d = tagDepthDelta(lines[j]!);
    if (d === null) throw new UnparseableXml();
    depth += d;
    if (depth === 0) {
      return { name, indent, lo: i, hi: j };
    }
  }
  throw new UnparseableXml(); // unclosed element → fail closed
}

// The opening-tag token of the element starting at lines[lo] (used for
// attribute extraction on any line shape — tag-only, self-closing, or
// inline-complete).
function openingTagToken(line: string): string | null {
  const indentEnd = line.length - line.replace(/^[ \t]+/, "").length;
  const rest = line.slice(indentEnd);
  if (!rest.startsWith("<") || rest.startsWith("</")) return null;
  const end = findTagEnd(rest);
  if (end < 0) return null;
  return rest.slice(0, end + 1);
}

// Attribute statistics over the dropped elements' opening tags: `all name=val`
// for invariants, `name: K distinct, min..max` for varying ones.
function summarizeXmlAttrs(elems: XmlElem[], lines: string[]): string {
  const byAttr = new Map<string, Map<string, number>>();
  for (const e of elems) {
    const tok = openingTagToken(lines[e.lo]!);
    if (!tok) continue;
    XML_ATTR_RE.lastIndex = 0;
    let am: RegExpExecArray | null;
    while ((am = XML_ATTR_RE.exec(tok)) !== null) {
      const [, name, value] = am;
      let values = byAttr.get(name!);
      if (!values) {
        values = new Map();
        byAttr.set(name!, values);
      }
      values.set(value!, (values.get(value!) ?? 0) + 1);
    }
  }
  const parts: string[] = [];
  for (const [name, values] of byAttr) {
    if (parts.length >= 2) break;
    const distinct = [...values.keys()];
    if (distinct.length === 1) {
      parts.push(`all ${name}=${distinct[0]}`);
    } else {
      distinct.sort();
      parts.push(
        `${name}: ${distinct.length} distinct, ${distinct[0]}..${distinct[distinct.length - 1]}`,
      );
    }
  }
  return parts.join("; ").slice(0, 140);
}

const XML_RUN_MIN = 4;
const XML_KEEP_HEAD = 2;
const XML_KEEP_TAIL = 1;
const XML_MAX_DEPTH = 6;

// Collapse sibling runs in lines[lo..hi] into marker comments, writing results
// into `out`. Returns how many bytes were elided.
function xmlCompressRange(
  lines: string[],
  out: (string | null)[],
  lo: number,
  hi: number,
  depth: number,
): number {
  if (lo > hi || depth > XML_MAX_DEPTH) return 0;

  const elems: XmlElem[] = [];
  let i = lo;
  while (i <= hi) {
    const e = scanElem(lines, i, hi); // throws UnparseableXml on doubt
    if (e === null) {
      i++;
      continue;
    }
    elems.push(e);
    i = e.hi + 1;
  }

  let elidedBytes = 0;
  let g = 0;
  while (g < elems.length) {
    let h = g;
    while (
      g + 1 < elems.length &&
      isXmlSibling(lines, elems[g]!, elems[g + 1]!)
    ) {
      g++;
    }
    const run = elems.slice(h, g + 1);
    g++;

    if (run.length < XML_RUN_MIN) {
      for (const e of run) {
        if (e.hi > e.lo) {
          elidedBytes += xmlCompressRange(lines, out, e.lo + 1, e.hi - 1, depth + 1);
        }
      }
      continue;
    }

    const n = run.length;
    const keep = new Array<boolean>(n).fill(false);
    for (let k = 0; k < n; k++) {
      keep[k] = k < XML_KEEP_HEAD || k >= n - XML_KEEP_TAIL;
    }
    for (let k = 0; k < n; k++) {
      if (keep[k]) continue;
      const span = lines.slice(run[k]!.lo, run[k]!.hi + 1).join("\n");
      if (ERROR_VALUE_RE.test(span) || xmlHasErrorAttr(lines, run[k]!.lo)) {
        keep[k] = true;
      }
    }

    for (let k = 0; k < n; k++) {
      if (!keep[k]) continue;
      if (run[k]!.hi > run[k]!.lo) {
        elidedBytes += xmlCompressRange(
          lines,
          out,
          run[k]!.lo + 1,
          run[k]!.hi - 1,
          depth + 1,
        );
      }
    }
    for (let k = 0; k < n; ) {
      if (keep[k]) {
        k++;
        continue;
      }
      let j = k;
      while (j < n && !keep[j]) j++;
      const dropped = run.slice(k, j);
      const first = dropped[0]!;
      const last = dropped[dropped.length - 1]!;
      const elided =
        lines.slice(first.lo, last.hi + 1).reduce((s, l) => s + l.length + 1, 0);
      const summary = summarizeXmlAttrs(dropped, lines);
      let marker = `${first.indent}<!-- ${dropped.length} <${first.name}> elements elided`;
      if (summary) marker += `: ${summary}`;
      marker += " -->";
      if (marker.length < elided) {
        for (let p = first.lo; p <= last.hi; p++) out[p] = null;
        out[first.lo] = marker;
        elidedBytes += elided;
      }
      k = j;
    }
  }
  return elidedBytes;
}

function isXmlSibling(lines: string[], a: XmlElem, b: XmlElem): boolean {
  if (a.name !== b.name || a.indent !== b.indent) return false;
  for (let k = a.hi + 1; k < b.lo; k++) {
    if (lines[k]!.trim().length > 0) return false;
  }
  return true;
}

function xmlHasErrorAttr(lines: string[], start: number): boolean {
  const tok = openingTagToken(lines[start]!);
  if (!tok) return false;
  XML_ATTR_RE.lastIndex = 0;
  let am: RegExpExecArray | null;
  while ((am = XML_ATTR_RE.exec(tok)) !== null) {
    if (ERROR_KEY_RE.test(am[1]!)) return true;
  }
  return false;
}

// Compress XML by stripping namespace declarations and collapsing runs of
// repeated sibling elements (e.g. long <item> lists) into marker comments that
// carry an attribute summary of exactly the elements they replace. Kept
// content passes through byte-for-byte; markers are comments so the payload
// stays well-formed markup. Returns null — caller falls back to the
// whitespace-only extractor — on any structure this scanner cannot fully
// account for.
function compressXml(text: string): string | null {
  const originalCount = text.split("\n").length;
  const lines = text.split("\n").map((l) => l.replace(XML_XMLNS_RE, ""));

  const out: (string | null)[] = [...lines];
  try {
    xmlCompressRange(lines, out, 0, lines.length - 1, 0);
  } catch (err) {
    if (err instanceof UnparseableXml) return null;
    throw err;
  }

  const kept = out.filter((l): l is string => l !== null);
  const resultCount = kept.length;
  if (resultCount >= originalCount * QUALITY_RATIO) {
    return null;
  }

  return `${kept.join("\n")}\n\n[XML compressed: ${resultCount} of ${originalCount} lines]`;
}

// ----- Public entry point ---------------------------------------------------

/**
 * Attempt semantic compression of a tool output payload. Returns the
 * compressed string with an annotation footer, or `null` when:
 *   - the toggle is off,
 *   - the input is not structured JSON/XML,
 *   - or compression didn't shrink the payload meaningfully.
 *
 * Falls back to whitespace-only compaction at the call site.
 */
export function compressStructuredSemantic(
  text: string,
  commandHint?: string,
): string | null {
  if (!structuredCompressionEnabled) return null;

  const format = detectOutputFormat(text);
  switch (format) {
    case "json":
      return compressJson(text, commandHint);
    case "xml":
      return compressXml(text);
    case "text":
      return null;
  }
}
