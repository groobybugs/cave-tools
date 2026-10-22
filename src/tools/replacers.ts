// Cave edit matching: upstream tiers (exact/unicode/trailing, LF-view + EOL)
// live in ./edit-match.js (ported from OpenCode v2.1.2 plugin/edit.ts); this
// file keeps the legacy fuzzy replacer chain as a Cave extension fallback plus
// the findReplacement adapter used by edit.ts. Each replacer below yields
// candidate substrings of `content` treated as a match for `find`.

import { getEnv } from "../runtime/context.js";
import {
  computeEdit,
  findOccurrences,
  lineEnding,
  matchView,
  withRegionEndings,
  type EditResult,
} from "./edit-match.js";

type Replacer = (content: string, find: string) => Generator<string>;

// Similarity thresholds for block anchor fallback matching (from opencode)
const SINGLE_CANDIDATE_SIMILARITY_THRESHOLD = 0.65;
const MULTIPLE_CANDIDATES_SIMILARITY_THRESHOLD = 0.65;

function levenshtein(a: string, b: string): number {
  if (a === b) return 0;
  if (a.length === 0) return b.length;
  if (b.length === 0) return a.length;
  let prev = new Array(b.length + 1);
  let curr = new Array(b.length + 1);
  for (let j = 0; j <= b.length; j++) prev[j] = j;
  for (let i = 1; i <= a.length; i++) {
    curr[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      curr[j] = Math.min(prev[j] + 1, curr[j - 1] + 1, prev[j - 1] + cost);
    }
    [prev, curr] = [curr, prev];
  }
  return prev[b.length];
}

// Yields `find` verbatim, plus CRLF/LF-converted variants so an exact match
// still wins (with the right line endings) when only the EOL style differs.
function* SimpleReplacer(_content: string, find: string): Generator<string> {
  yield find;
  if (find.includes("\n") && !find.includes("\r")) {
    yield find.replace(/\n/g, "\r\n");
  } else if (find.includes("\r\n")) {
    yield find.replace(/\r\n/g, "\n");
  }
}

// Match line-by-line after trimming each line; yield the original (untrimmed)
// span from content. Tolerates leading/trailing whitespace drift.
function* LineTrimmedReplacer(content: string, find: string): Generator<string> {
  const originalLines = content.split("\n");
  const searchLines = find.split("\n");
  if (searchLines[searchLines.length - 1] === "") searchLines.pop();
  if (searchLines.length === 0) return;

  for (let i = 0; i <= originalLines.length - searchLines.length; i++) {
    let matches = true;
    for (let j = 0; j < searchLines.length; j++) {
      if (originalLines[i + j].trim() !== searchLines[j].trim()) {
        matches = false;
        break;
      }
    }
    if (!matches) continue;

    let matchStart = 0;
    for (let k = 0; k < i; k++) matchStart += originalLines[k].length + 1;
    let matchEnd = matchStart;
    for (let k = 0; k < searchLines.length; k++) {
      matchEnd += originalLines[i + k].length + 1;
    }
    yield content.slice(matchStart, matchEnd - 1);
  }
}

// Anchor on first + last line, validate middle lines by Levenshtein similarity.
// Handles blocks whose interior drifted. Requires >= 3 lines.
// Ported from opencode BlockAnchorReplacer (single-candidate similarity + maxLineDelta).
function* BlockAnchorReplacer(content: string, find: string): Generator<string> {
  const originalLines = content.split("\n");
  const searchLines = find.split("\n");

  if (searchLines.length < 3) {
    return;
  }

  if (searchLines[searchLines.length - 1] === "") {
    searchLines.pop();
  }

  const firstLineSearch = searchLines[0]!.trim();
  const lastLineSearch = searchLines[searchLines.length - 1]!.trim();
  const searchBlockSize = searchLines.length;
  const maxLineDelta = Math.max(1, Math.floor(searchBlockSize * 0.25));

  // Collect all candidate positions where both anchors match
  const candidates: Array<{ startLine: number; endLine: number }> = [];
  for (let i = 0; i < originalLines.length; i++) {
    if (originalLines[i]!.trim() !== firstLineSearch) {
      continue;
    }

    // Look for the matching last line after this first line
    for (let j = i + 2; j < originalLines.length; j++) {
      if (originalLines[j]!.trim() === lastLineSearch) {
        const actualBlockSize = j - i + 1;
        if (Math.abs(actualBlockSize - searchBlockSize) <= maxLineDelta) {
          candidates.push({ startLine: i, endLine: j });
        }
        break; // Only match the first occurrence of the last line
      }
    }
  }

  if (candidates.length === 0) {
    return;
  }

  const spanOf = (startLine: number, endLine: number): string => {
    let matchStartIndex = 0;
    for (let k = 0; k < startLine; k++) {
      matchStartIndex += originalLines[k]!.length + 1;
    }
    let matchEndIndex = matchStartIndex;
    for (let k = startLine; k <= endLine; k++) {
      matchEndIndex += originalLines[k]!.length;
      if (k < endLine) {
        matchEndIndex += 1;
      }
    }
    return content.substring(matchStartIndex, matchEndIndex);
  };

  // Handle single candidate scenario (using relaxed threshold)
  if (candidates.length === 1) {
    const { startLine, endLine } = candidates[0]!;
    const actualBlockSize = endLine - startLine + 1;

    let similarity = 0;
    const linesToCheck = Math.min(searchBlockSize - 2, actualBlockSize - 2); // Middle lines only

    if (linesToCheck > 0) {
      for (let j = 1; j < searchBlockSize - 1 && j < actualBlockSize - 1; j++) {
        const originalLine = originalLines[startLine + j]!.trim();
        const searchLine = searchLines[j]!.trim();
        const maxLen = Math.max(originalLine.length, searchLine.length);
        if (maxLen === 0) {
          continue;
        }
        const distance = levenshtein(originalLine, searchLine);
        similarity += (1 - distance / maxLen) / linesToCheck;

        // Exit early when threshold is reached
        if (similarity >= SINGLE_CANDIDATE_SIMILARITY_THRESHOLD) {
          break;
        }
      }
    } else {
      // No middle lines to compare, just accept based on anchors
      similarity = 1.0;
    }

    if (similarity >= SINGLE_CANDIDATE_SIMILARITY_THRESHOLD) {
      yield spanOf(startLine, endLine);
    }
    return;
  }

  // Calculate similarity for multiple candidates
  let bestMatch: { startLine: number; endLine: number } | null = null;
  let maxSimilarity = -1;

  for (const candidate of candidates) {
    const { startLine, endLine } = candidate;
    const actualBlockSize = endLine - startLine + 1;

    let similarity = 0;
    const linesToCheck = Math.min(searchBlockSize - 2, actualBlockSize - 2); // Middle lines only

    if (linesToCheck > 0) {
      for (let j = 1; j < searchBlockSize - 1 && j < actualBlockSize - 1; j++) {
        const originalLine = originalLines[startLine + j]!.trim();
        const searchLine = searchLines[j]!.trim();
        const maxLen = Math.max(originalLine.length, searchLine.length);
        if (maxLen === 0) {
          continue;
        }
        const distance = levenshtein(originalLine, searchLine);
        similarity += 1 - distance / maxLen;
      }
      similarity /= linesToCheck; // Average similarity
    } else {
      // No middle lines to compare, just accept based on anchors
      similarity = 1.0;
    }

    if (similarity > maxSimilarity) {
      maxSimilarity = similarity;
      bestMatch = candidate;
    }
  }

  if (maxSimilarity >= MULTIPLE_CANDIDATES_SIMILARITY_THRESHOLD && bestMatch) {
    yield spanOf(bestMatch.startLine, bestMatch.endLine);
  }
}

// Collapse all whitespace// Collapse all whitespace runs to a single space and trim before comparing.
// Handles re-indented / reflowed code.
function* WhitespaceNormalizedReplacer(content: string, find: string): Generator<string> {
  const normalize = (s: string) => s.replace(/\s+/g, " ").trim();
  const normFind = normalize(find);
  if (normFind.length === 0) return;

  const lines = content.split("\n");
  for (const line of lines) {
    if (normalize(line) === normFind) {
      yield line;
      continue;
    }
    if (normalize(line).includes(normFind)) {
      const words = find.trim().split(/\s+/);
      if (words.length === 0) continue;
      const pattern = words
        .map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
        .join("\\s+");
      try {
        const m = line.match(new RegExp(pattern));
        if (m) yield m[0];
      } catch {
        /* invalid regex, skip */
      }
    }
  }

  const findLines = find.split("\n");
  if (findLines.length > 1) {
    for (let i = 0; i <= lines.length - findLines.length; i++) {
      const block = lines.slice(i, i + findLines.length).join("\n");
      if (normalize(block) === normFind) yield block;
    }
  }
}

// Strip the common minimum indent from both sides before comparing.
// Handles code pasted at a different nesting level.
function* IndentationFlexibleReplacer(content: string, find: string): Generator<string> {
  const removeIndent = (text: string): string => {
    const lines = text.split("\n");
    const nonEmpty = lines.filter((l) => l.trim().length > 0);
    if (nonEmpty.length === 0) return text;
    const minIndent = Math.min(
      ...nonEmpty.map((l) => (l.match(/^\s*/)?.[0].length ?? 0)),
    );
    return lines.map((l) => l.slice(minIndent)).join("\n");
  };

  const normFind = removeIndent(find);
  const contentLines = content.split("\n");
  const findLines = find.split("\n");
  for (let i = 0; i <= contentLines.length - findLines.length; i++) {
    const block = contentLines.slice(i, i + findLines.length).join("\n");
    if (removeIndent(block) === normFind) yield block;
  }
}

// Unescape common escape sequences in `find`, then match against real content.
// Handles a model emitting literal "\n", "\t", etc.
function* EscapeNormalizedReplacer(content: string, find: string): Generator<string> {
  const unescape = (s: string): string =>
    s
      .replace(/\\n/g, "\n")
      .replace(/\\t/g, "\t")
      .replace(/\\r/g, "\r")
      .replace(/\\'/g, "'")
      .replace(/\\"/g, '"')
      .replace(/\\`/g, "`")
      .replace(/\\\$/g, "$")
      .replace(/\\\\/g, "\\");

  const unescapedFind = unescape(find);
  if (unescapedFind === find) return;
  if (content.includes(unescapedFind)) yield unescapedFind;

  const lines = content.split("\n");
  const findLines = unescapedFind.split("\n");
  for (let i = 0; i <= lines.length - findLines.length; i++) {
    const block = lines.slice(i, i + findLines.length).join("\n");
    if (unescape(block) === unescapedFind) yield block;
  }
}

// Match when `find` includes extra leading/trailing whitespace the file lacks.
function* TrimmedBoundaryReplacer(content: string, find: string): Generator<string> {
  const trimmedFind = find.trim();
  if (trimmedFind === find) return;
  if (content.includes(trimmedFind)) yield trimmedFind;
  const lines = content.split("\n");
  const findLines = find.split("\n");
  for (let i = 0; i <= lines.length - findLines.length; i++) {
    const block = lines.slice(i, i + findLines.length).join("\n");
    if (block.trim() === trimmedFind) yield block;
  }
}

// Anchor on first + last line, accept if >= 50% of middle non-empty lines match
// when trimmed. Looser than BlockAnchor. Requires >= 3 lines.
function* ContextAwareReplacer(content: string, find: string): Generator<string> {
  const findLines = find.split("\n");
  if (findLines[findLines.length - 1] === "") findLines.pop();
  if (findLines.length < 3) return;

  const contentLines = content.split("\n");
  const first = findLines[0].trim();
  const last = findLines[findLines.length - 1].trim();
  const middle = findLines.slice(1, -1).filter((l) => l.trim().length > 0);

  for (let i = 0; i < contentLines.length; i++) {
    if (contentLines[i].trim() !== first) continue;
    for (let j = i + 2; j < contentLines.length; j++) {
      if (contentLines[j].trim() !== last) continue;
      const block = contentLines.slice(i, j + 1);
      let matches = 0;
      for (const fl of middle) {
        if (block.some((bl) => bl.trim() === fl.trim())) matches++;
      }
      if (middle.length === 0 || matches / middle.length >= 0.5) {
        yield block.join("\n");
      }
      break;
    }
  }
}

// Yields every exact occurrence of `find`, enabling replaceAll to replace each one.
function* MultiOccurrenceReplacer(content: string, find: string): Generator<string> {
  let startIndex = 0;
  while (true) {
    const index = content.indexOf(find, startIndex);
    if (index === -1) break;
    yield find;
    startIndex = index + find.length;
  }
}

const REPLACERS: Replacer[] = [
  SimpleReplacer,
  LineTrimmedReplacer,
  BlockAnchorReplacer,
  WhitespaceNormalizedReplacer,
  IndentationFlexibleReplacer,
  EscapeNormalizedReplacer,
  TrimmedBoundaryReplacer,
  ContextAwareReplacer,
  MultiOccurrenceReplacer,
];

// ── Upstream tiers ──────────────────────────────────────────────────────────
// Exact / unicode-normalized / trailing-whitespace tiers live in
// ./edit-match.js — the single canonical port of OpenCode v2.1.2
// packages/core/src/tool/plugin/edit.ts (LF-view matching, unconditional
// unicode tier, region-aware EOL). Imported above; do not duplicate here.

// Guard against a short `find` matching a wildly larger span (e.g. a single
// normalized line collapsing onto a giant region).
function isDisproportionateMatch(search: string, oldString: string): boolean {
  const oldLines = oldString.split("\n").length;
  const searchLines = search.split("\n").length;
  if (searchLines >= Math.max(oldLines + 3, oldLines * 2)) return true;
  if (oldLines === 1) return false;
  return search.trim().length > Math.max(oldString.trim().length + 500, oldString.trim().length * 4);
}

export interface MatchResult {
  search?: string;
  index?: number;
  error?: string;
  nonUnique?: boolean;
}

// Returns the actual substring of `content` to replace plus its first index.
// `search` is the literal text present in `content` (may differ from `find`
// when a fuzzy replacer matched). For replace_all the caller replaces every
// occurrence of `search`; otherwise uniqueness is enforced here.
//
// Tiers 1-3 (exact → unicode-normalized → trailing-whitespace line window)
// run on the LF view with source-offset mapping via matchView from
// ./edit-match.js — the canonical OpenCode v2.1.2 port. The first non-empty
// tier wins; a multi-match tier without replace_all is an immediate uniqueness
// error (no fall-through to fuzzier tiers). When no tier matches, the legacy
// fuzzy replacers run as a final cave-extension fallback — set
// CAVE_TOOLS_FUZZY_EDIT=0 to disable them for strict upstream parity.
// NOTE: this adapter cannot apply the endingsOnly raw-byte branch (it has no
// newString); the complete API computeEditResult below handles that. For
// CRLF/mixed files the spans here are still correct source offsets.
export function findReplacement(
  content: string,
  find: string,
  replaceAll: boolean,
): MatchResult {
  if (find.length === 0) {
    return { error: "not-found: old_string cannot be empty." };
  }
  // Tiers 1-3 via the canonical LF-view matcher (matches carry source offsets).
  // findReplacement takes no newString, so the replacement half is unused here.
  const spans = matchView(content, find, find).matches;
  if (spans.length > 0) {
    if (replaceAll || spans.length === 1) {
      const m = spans[0]!;
      return { search: content.slice(m.start, m.end), index: m.start };
    }
    return { error: "non-unique", nonUnique: true };
  }

  // Tier 4 (cave extension): legacy fuzzy chain.
  return findFuzzyCandidate(content, find, replaceAll);
}

// Legacy fuzzy replacer chain (Cave extension — not upstream OpenCode).
// Honors CAVE_TOOLS_FUZZY_EDIT=0 for strict upstream parity.
function findFuzzyCandidate(content: string, find: string, replaceAll: boolean): MatchResult {
  if (getEnv().CAVE_TOOLS_FUZZY_EDIT === "0") {
    return { error: "not-found: Could not find oldString in the file. It must match exactly, including whitespace, indentation, and line endings. Re-read the file and provide the full exact oldString." };
  }

  let nonUniqueSeen = false;

  for (const replacer of REPLACERS) {
    for (const candidate of replacer(content, find)) {
      if (!candidate) continue;
      const index = content.indexOf(candidate);
      if (index === -1) continue;
      if (isDisproportionateMatch(candidate, find)) continue;

      if (replaceAll) return { search: candidate, index };

      if (content.indexOf(candidate) === content.lastIndexOf(candidate)) {
        return { search: candidate, index };
      }
      nonUniqueSeen = true;
    }
  }

  if (nonUniqueSeen) return { error: "non-unique", nonUnique: true };
  return { error: "not-found: Could not find oldString in the file. It must match exactly (or via fuzzy match), including whitespace, indentation, and line endings. Re-read the file and provide the full exact oldString." };
}

export type { EditResult } from "./edit-match.js";

/**
 * Complete edit computation for future edit.ts handler integration.
 *
 * Pure upstream OpenCode v2.1.2 semantics first (computeEdit: LF-view tiers,
 * unconditional unicode tier, per-span region-aware EOL replacements,
 * endingsOnly raw branch, computed-unchanged reject). Only a pure *not-found*
 * (zero upstream spans) falls through to the legacy fuzzy chain — and only
 * when CAVE_TOOLS_FUZZY_EDIT is not "0". Ambiguity, identical/empty, and
 * unchanged rejects are final and never fuzzy-retried. Fuzzy hits are marked
 * with `fuzzy: true` so callers (and tests) can tell extension from upstream.
 */
export function computeEditResult(
  source: string,
  oldString: string,
  newString: string,
  replaceAll: boolean,
): EditResult & { fuzzy?: boolean } {
  const pure = computeEdit(source, oldString, newString, replaceAll);
  if (pure.ok) return { ...pure, fuzzy: false };
  // Final rejects: ambiguity carries spans; identical/empty/unchanged and the
  // endingsOnly raw miss never fall through to fuzzy.
  if (pure.replacements > 0 || pure.endingsOnly) return pure;
  if ("nonUnique" in pure && pure.nonUnique) return pure;
  if (oldString === "" || oldString === newString) return pure;
  if (getEnv().CAVE_TOOLS_FUZZY_EDIT === "0") return pure;
  const fb = findFuzzyCandidate(source, oldString, replaceAll);
  if ("nonUnique" in fb && fb.nonUnique) {
    // Zero upstream spans but ambiguous legacy-fuzzy candidates: keep the
    // historical non-unique semantics (not not-found). Upstream ambiguity and
    // no-op rejects above never reach here (intentional no fall-through).
    return {
      ok: false,
      error:
        "old_string is not unique (2+ matches). Add more surrounding context or set replace_all.",
      nonUnique: true,
      matches: [],
      replacements: 0,
      endingsOnly: false,
      fuzzy: true,
    };
  }
  if (fb.error || fb.search === undefined || fb.index === undefined) return pure;
  const spans = findOccurrences(source, fb.search);
  if (spans.length === 0) return pure;
  if (spans.length > 1 && replaceAll !== true) {
    return {
      ok: false,
      error: `Found ${spans.length} matches for oldString, but expected exactly one. Add more surrounding context to make oldString unique, or set replaceAll to true to replace every occurrence.`,
      nonUnique: true,
      matches: spans,
      replacements: spans.length,
      endingsOnly: false,
      fuzzy: true,
    };
  }
  // Per-span region-aware EOL (same rule as upstream): each fuzzy span keeps
  // its own region endings instead of sharing the first span's bytes.
  const fileEnding = lineEnding(source, "\n");
  const replacementView = newString.replaceAll("\r\n", "\n");
  const targets = replaceAll ? spans : spans.slice(0, 1);
  const applied = targets.map((m) => {
    const region = source.slice(m.start, m.end);
    return {
      start: m.start,
      end: m.end,
      replacement: withRegionEndings(region, replacementView, lineEnding(region, fileEnding)),
    };
  });
  let updated = source;
  for (const item of applied.slice().reverse()) {
    updated = updated.slice(0, item.start) + item.replacement + updated.slice(item.end);
  }
  if (updated === source) {
    return {
      ok: false,
      error:
        "No changes to apply: the edit leaves the file unchanged. Line endings follow the file; to change them, write \\r explicitly in oldString or newString.",
      matches: spans,
      replacements: spans.length,
      endingsOnly: false,
      fuzzy: true,
    };
  }
  return {
    ok: true,
    matches: spans,
    applied,
    replacements: spans.length,
    updated,
    endingsOnly: false,
    fuzzy: true,
  };
}
