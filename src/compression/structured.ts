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
    return [
      ...kept,
      `... ${value.length - MAX_ARRAY_ELEMENTS} more items (${value.length} total)`,
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

// Compress XML text by stripping namespace declarations and collapsing
// repeated sibling elements (e.g. long <item> lists).
function compressXml(text: string): string | null {
  const lines = text.split("\n");
  const originalCount = lines.length;

  const result: string[] = [];
  let repetitionCount = 0;
  let lastTagName = "";
  let skipping = false;

  for (const line of lines) {
    const cleaned = line.replace(/\s+xmlns(?::\w+)?="[^"]*"/g, "");

    const tagMatch = cleaned.match(/^\s*<(\w+)[\s>]/);
    if (tagMatch) {
      const tagName = tagMatch[1]!;
      if (tagName === lastTagName) {
        repetitionCount++;
        if (repetitionCount > 3) {
          if (!skipping) {
            result.push(`    ... (repeated <${tagName}> elements)`);
            skipping = true;
          }
          continue;
        }
      } else {
        if (skipping) {
          result.push(`    [${repetitionCount} total <${lastTagName}> elements]`);
          skipping = false;
        }
        lastTagName = tagName;
        repetitionCount = 1;
      }
    }

    result.push(cleaned);
  }

  if (skipping) {
    result.push(`    [${repetitionCount} total <${lastTagName}> elements]`);
  }

  const resultCount = result.length;
  if (resultCount >= originalCount * QUALITY_RATIO) {
    return null;
  }

  return `${result.join("\n")}\n\n[XML compressed: ${resultCount} of ${originalCount} lines]`;
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
