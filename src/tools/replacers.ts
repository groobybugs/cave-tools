// Fuzzy edit matching, ported from opencode's edit tool replacer chain.
// Each replacer is a generator that yields candidate substrings of `content`
// that should be treated as a match for `find`. The driver tries replacers in
// order (exact first, fuzziest last) and uses the first usable candidate.

type Replacer = (content: string, find: string) => Generator<string>;

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
function* BlockAnchorReplacer(content: string, find: string): Generator<string> {
  const originalLines = content.split("\n");
  const searchLines = find.split("\n");
  if (searchLines[searchLines.length - 1] === "") searchLines.pop();
  if (searchLines.length < 3) return;

  const firstLineSearch = searchLines[0].trim();
  const lastLineSearch = searchLines[searchLines.length - 1].trim();
  const searchBlockSize = searchLines.length;

  const candidates: { start: number; end: number }[] = [];
  for (let i = 0; i < originalLines.length; i++) {
    if (originalLines[i].trim() !== firstLineSearch) continue;
    for (let j = i + 2; j < originalLines.length; j++) {
      if (originalLines[j].trim() === lastLineSearch) {
        candidates.push({ start: i, end: j });
        break;
      }
    }
  }
  if (candidates.length === 0) return;

  const spanOf = (start: number, end: number): string => {
    let matchStart = 0;
    for (let k = 0; k < start; k++) matchStart += originalLines[k].length + 1;
    let matchEnd = matchStart;
    for (let k = start; k <= end; k++) {
      matchEnd += originalLines[k].length;
      if (k < end) matchEnd += 1;
    }
    return content.slice(matchStart, matchEnd);
  };

  if (candidates.length === 1) {
    yield spanOf(candidates[0].start, candidates[0].end);
    return;
  }

  let best: { start: number; end: number } | null = null;
  let bestScore = -1;
  for (const c of candidates) {
    const actualBlockSize = c.end - c.start + 1;
    const sizeDiff = Math.abs(actualBlockSize - searchBlockSize) / searchBlockSize;
    if (sizeDiff > 0.25) continue;

    const linesToCheck = Math.min(searchBlockSize - 2, actualBlockSize - 2);
    let similarity = 1;
    if (linesToCheck > 0) {
      let acc = 0;
      for (let k = 1; k <= linesToCheck; k++) {
        const o = originalLines[c.start + k].trim();
        const s = searchLines[k].trim();
        const maxLen = Math.max(o.length, s.length);
        acc += maxLen === 0 ? 1 : 1 - levenshtein(o, s) / maxLen;
      }
      similarity = acc / linesToCheck;
    }
    if (similarity < 0.65) continue;
    if (similarity > bestScore) {
      best = c;
      bestScore = similarity;
    }
  }
  if (best) yield spanOf(best.start, best.end);
}

// Collapse all whitespace runs to a single space and trim before comparing.
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

const REPLACERS: Replacer[] = [
  SimpleReplacer,
  LineTrimmedReplacer,
  BlockAnchorReplacer,
  WhitespaceNormalizedReplacer,
  IndentationFlexibleReplacer,
  EscapeNormalizedReplacer,
  ContextAwareReplacer,
];

// Guard against a short `find` matching a wildly larger span (e.g. a single
// normalized line collapsing onto a giant region).
function isDisproportionate(find: string, candidate: string): boolean {
  if (candidate.length <= find.length) return false;
  return candidate.length > Math.max(find.length * 3, find.length + 200);
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
export function findReplacement(
  content: string,
  find: string,
  replaceAll: boolean,
): MatchResult {
  let nonUniqueSeen = false;

  for (const replacer of REPLACERS) {
    for (const candidate of replacer(content, find)) {
      if (!candidate) continue;
      const index = content.indexOf(candidate);
      if (index === -1) continue;
      if (isDisproportionate(find, candidate)) continue;

      if (replaceAll) return { search: candidate, index };

      if (content.indexOf(candidate) === content.lastIndexOf(candidate)) {
        return { search: candidate, index };
      }
      nonUniqueSeen = true;
    }
  }

  if (nonUniqueSeen) return { error: "non-unique", nonUnique: true };
  return { error: "not-found" };
}
