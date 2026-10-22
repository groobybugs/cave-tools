// Canonical OpenCode string-edit matching + EOL semantics.
//
// Provenance: ported from OpenCode v2.1.2
// (https://github.com/anomalyco/opencode/blob/v2.1.2/packages/core/src/tool/plugin/edit.ts,
// commit a517687fde98a7329ada3ae644a01a9fc6c4e99e).
// findLineOccurrences follows upstream lines 66-102 (trailing-whitespace /
// LCS-keyed sliding window; same non-overlap dedup counting — intentional,
// not fixed).
//   lineEnding              <- upstream lineEnding (CRLF/LF majority, tie -> fallback)
//   alignLines              <- upstream alignLines (LCS DP + gap flush)
//   withRegionEndings       <- upstream withRegionEndings (region endings, LCS-kept
//                             lines reuse their ending, changed-in-place reuse the
//                             replaced line's ending, extras take fallback)
//   matchView               <- upstream matchView, lines 168-187 (LF-view matching +
//                             source-offset mapping via removed[]/countBefore)
//   countBefore             <- upstream countBefore (binary search, strictly before)
//   endingsOnly branch      <- upstream execute lines 260-270 (old/new differ only in
//                             \r -> raw exact-byte match, replace returns newString
//                             verbatim, no EOL adaptation)
//   computeEdit checks      <- upstream execute: identical/empty guards, 0-match,
//                             >1-without-replaceAll, and computed-unchanged rejects.
// Tier priority mirrors upstream exactly: exact wins outright (unicode/trailing
// skipped when exact non-empty); unicode wins over trailing (trailing skipped
// when either is non-empty). A multi-match tier without replaceAll is an
// immediate uniqueness error — no fall-through to fuzzier tiers. The same
// non-overlap stride counting the reviewer verified is kept intentionally.
//
// This module is pure upstream: no fuzzy fallback, no env reads, no I/O. The
// Cave legacy-fuzzy extension lives in ./replacers.js (computeEditResult),
// clearly marked as an extension — never claimed as upstream.

export interface EditSpan {
  start: number;
  end: number;
}

export interface EditApplied extends EditSpan {
  replacement: string;
}

export type EditResult =
  | {
      ok: true;
      matches: EditSpan[];
      applied: EditApplied[];
      replacements: number;
      updated: string;
      endingsOnly: boolean;
    }
  | {
      ok: false;
      error: string;
      nonUnique?: boolean;
      matches: EditSpan[];
      replacements: number;
      endingsOnly: boolean;
    };

const crlf = "\r\n";

/** One-char-to-one-char punctuation/space normalization; offsets stay valid. */
export const normalizeForMatch = (value: string): string =>
  value
    .replace(/[‘’‚‛]/g, "'")
    .replace(/[“”„‟]/g, '"')
    .replace(/[‐‑‒–—―−]/g, "-")
    .replace(/[\u00A0\u2002-\u200A\u202F\u205F\u3000]/g, " ");

/** Non-overlapping occurrences (upstream stride: offset += search.length). */
export function findOccurrences(content: string, search: string): EditSpan[] {
  const result: EditSpan[] = [];
  if (search.length === 0) return result;
  let offset = 0;
  while (true) {
    const at = content.indexOf(search, offset);
    if (at === -1) break;
    result.push({ start: at, end: at + search.length });
    offset = at + search.length;
  }
  return result;
}

/**
 * Sliding line window comparing trimEnd+normalized lines. Upstream lines
 * 105-161 region. Tolerates trailing-whitespace drift, nothing else.
 * Non-overlap dedup counting kept as upstream (intentional).
 */
export function findLineOccurrences(content: string, search: string): EditSpan[] {
  const trailingNewline = search.endsWith("\n");
  const expected = search.split("\n");
  if (trailingNewline) expected.pop();
  if (expected.length === 0) return [];
  const lines: Array<{ start: number; end: number; text: string; contentEnd: number; newline: boolean }> = [];
  for (const match of content.matchAll(/[^\n]*(?:\n|$)/g)) {
    if (match[0] === "") continue;
    const start = match.index ?? 0;
    const newline = match[0].endsWith("\n");
    const text = newline ? match[0].slice(0, -1) : match[0];
    lines.push({
      start,
      end: start + match[0].length,
      text,
      contentEnd: start + text.length - (text.endsWith("\r") ? 1 : 0),
      newline,
    });
  }
  const candidates: EditSpan[] = [];
  for (let index = 0; index < lines.length; index++) {
    const actual = lines.slice(index, index + expected.length);
    if (actual.length !== expected.length) continue;
    let allMatch = true;
    for (let lineIndex = 0; lineIndex < actual.length; lineIndex++) {
      if (
        normalizeForMatch(actual[lineIndex]!.text.trimEnd()) !==
        normalizeForMatch(expected[lineIndex]!.trimEnd())
      ) {
        allMatch = false;
        break;
      }
    }
    if (!allMatch) continue;
    const last = actual[actual.length - 1]!;
    if (trailingNewline && !last.newline) continue;
    candidates.push({ start: actual[0]!.start, end: trailingNewline ? last.end : last.contentEnd });
  }
  const deduped: EditSpan[] = [];
  for (const candidate of candidates) {
    if (deduped.some((kept) => kept.end > candidate.start && kept.start < candidate.end)) continue;
    deduped.push(candidate);
  }
  return deduped;
}

/** The dominant line ending in text, or fallback when there is none/tie. */
export function lineEnding(text: string, fallback: string): string {
  const crlfCount = text.split(crlf).length - 1;
  const lfCount = text.split("\n").length - 1 - crlfCount;
  if (crlfCount === lfCount) return fallback;
  return crlfCount > lfCount ? crlf : "\n";
}

/**
 * For each line of `after`, the index of the `before` line it keeps (longest
 * common subsequence) or, between kept lines, the line it takes the place of;
 * undefined for lines with no counterpart.
 */
export function alignLines(before: string[], after: string[]): (number | undefined)[] {
  const width = after.length + 1;
  const table = new Uint32Array((before.length + 1) * width);
  for (let i = before.length - 1; i >= 0; i--)
    for (let j = after.length - 1; j >= 0; j--)
      table[i * width + j] =
        before[i] === after[j]
          ? table[(i + 1) * width + j + 1] + 1
          : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
  const result: (number | undefined)[] = [];
  const gap = { before: [] as number[], after: [] as number[] };
  const flush = (): void => {
    gap.after.forEach((j, k) => (result[j] = gap.before[k]));
    gap.before = [];
    gap.after = [];
  };
  let i = 0;
  let j = 0;
  while (i < before.length && j < after.length) {
    if (before[i] === after[j]) {
      flush();
      result[j++] = i++;
      continue;
    }
    if (table[(i + 1) * width + j] >= table[i * width + j + 1]) gap.before.push(i++);
    else gap.after.push(j++);
  }
  while (i < before.length) gap.before.push(i++);
  while (j < after.length) gap.after.push(j++);
  flush();
  return result;
}

/**
 * Joins the replacement's lines with line endings taken from the matched
 * source region. A replacement line that keeps a region line (longest common
 * subsequence, compared like the trailing-whitespace tier) reuses that line's
 * ending; a line that changes in place reuses the ending of the line it
 * replaces; only lines beyond those take `fallback`. Unchanged context inside
 * a match therefore keeps its bytes even in a mixed-ending file.
 */
export function withRegionEndings(region: string, replacement: string, fallback: string): string {
  const before = region.split(/\r?\n/);
  const endings = [...region.matchAll(/\r?\n/g)].map((match) => match[0]);
  const after = replacement.split("\n");
  const key = (line: string): string => normalizeForMatch(line.trimEnd());
  const source = alignLines(before.map(key), after.map(key));
  return after
    .map((line, index) => (index === after.length - 1 ? line : line + (endings[source[index]!] ?? fallback)))
    .join("");
}

/** Number of sorted positions strictly before offset. */
export function countBefore(positions: number[], offset: number): number {
  let low = 0;
  let high = positions.length;
  while (low < high) {
    const middle = (low + high) >> 1;
    if (positions[middle]! < offset) low = middle + 1;
    else high = middle;
  }
  return low;
}

/** True when old/new differ only in \r (line-ending-only change request). */
export function isEndingsOnlyChange(oldString: string, newString: string): boolean {
  return oldString.replaceAll("\r", "") === newString.replaceAll("\r", "");
}

/**
 * Matches against the LF view Read shows the model, then maps offsets back to
 * the source, so CRLF and mixed line endings neither block matches nor leak
 * into untouched regions. Tier priority is exact, then unicode-normalized
 * (unconditional — runs even when the query is pure ASCII, since the *source*
 * may hold curly/space variants), then trailing-whitespace line window.
 */
export function matchView(
  source: string,
  oldString: string,
  newString: string,
): { matches: EditSpan[]; replace: (match: EditSpan) => string } {
  const view = source.replaceAll(crlf, "\n");
  const removed = [...source.matchAll(/\r\n/g)].map((match, index) => (match.index ?? 0) - index);
  const fileEnding = lineEnding(source, "\n");
  const search = oldString.replaceAll(crlf, "\n");
  const replacement = newString.replaceAll(crlf, "\n");
  const exact = findOccurrences(view, search);
  // These one-to-one mappings preserve offsets into the LF view.
  const unicode = exact.length > 0 ? [] : findOccurrences(normalizeForMatch(view), normalizeForMatch(search));
  const trailing = exact.length > 0 || unicode.length > 0 ? [] : findLineOccurrences(view, search);
  const picked = exact.length > 0 ? exact : unicode.length > 0 ? unicode : trailing;
  return {
    matches: picked.map((match) => ({
      start: match.start + countBefore(removed, match.start),
      end: match.end + countBefore(removed, match.end),
    })),
    replace: (match: EditSpan) => {
      const region = source.slice(match.start, match.end);
      return withRegionEndings(region, replacement, lineEnding(region, fileEnding));
    },
  };
}

/**
 * Pure upstream edit computation (no I/O, no fuzzy, no env). Returns every
 * matched source span plus the per-span replacements and the computed updated
 * text. Handler integration (file read/write, permission, diff) happens later
 * in edit.ts and is intentionally not done here.
 */
export function computeEdit(
  source: string,
  oldString: string,
  newString: string,
  replaceAll: boolean,
): EditResult {
  if (oldString === newString) {
    return {
      ok: false,
      error: "No changes to apply: oldString and newString are identical.",
      matches: [],
      replacements: 0,
      endingsOnly: false,
    };
  }
  if (oldString === "") {
    return {
      ok: false,
      error: "oldString must not be empty. Use write to create or overwrite a file.",
      matches: [],
      replacements: 0,
      endingsOnly: false,
    };
  }
  // Strings that differ only in \r ask to change line endings themselves, so
  // they apply to the exact bytes. Every other edit matches the LF view Read
  // shows the model and keeps the file's endings.
  const endingsOnly = isEndingsOnlyChange(oldString, newString);
  const view = endingsOnly
    ? { matches: findOccurrences(source, oldString), replace: (): string => newString }
    : matchView(source, oldString, newString);
  const matches = view.matches;
  const replacements = matches.length;
  if (replacements === 0) {
    return {
      ok: false,
      error:
        "Could not find oldString in the file. It must match exactly, including whitespace and indentation.",
      matches: [],
      replacements: 0,
      endingsOnly,
    };
  }
  if (replacements > 1 && replaceAll !== true) {
    return {
      ok: false,
      error: `Found ${replacements} matches for oldString, but expected exactly one. Add more surrounding context to make oldString unique, or set replaceAll to true to replace every occurrence.`,
      nonUnique: true,
      matches,
      replacements,
      endingsOnly,
    };
  }
  // NOTE: upstream uses .toReversed() (ES2023); slice().reverse() is the
  // ES2022-compatible equivalent with identical semantics (no input mutation).
  const targets = replaceAll ? matches : matches.slice(0, 1);
  const applied: EditApplied[] = targets.map((match) => ({
    start: match.start,
    end: match.end,
    replacement: view.replace(match),
  }));
  let updated = source;
  for (const item of applied.slice().reverse()) {
    updated = updated.slice(0, item.start) + item.replacement + updated.slice(item.end);
  }
  if (updated === source) {
    return {
      ok: false,
      error: endingsOnly
        ? "No changes to apply: the file already has those line endings."
        : "No changes to apply: the edit leaves the file unchanged. Line endings follow the file; to change them, write \\r explicitly in oldString or newString.",
      matches,
      replacements,
      endingsOnly,
    };
  }
  return { ok: true, matches, applied, replacements, updated, endingsOnly };
}
