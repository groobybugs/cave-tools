// Minimal line-based unified diff (no dependency). Trims the common prefix and
// suffix, then runs an LCS over the changed middle. Returns null when the
// middle is too large to diff cheaply; callers then send full content.

const CONTEXT_LINES = 3;
// LCS table cells (middle old lines × middle new lines). ~4M cells = 16 MB of
// Uint32 at worst, freed right after.
const MAX_LCS_CELLS = 4_000_000;

type Op = { kind: " " | "-" | "+"; line: string; oldNo: number; newNo: number };

function diffOps(a: string[], b: string[]): Op[] | null {
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (
    suf < a.length - pre &&
    suf < b.length - pre &&
    a[a.length - 1 - suf] === b[b.length - 1 - suf]
  ) suf++;

  const midA = a.slice(pre, a.length - suf);
  const midB = b.slice(pre, b.length - suf);
  const n = midA.length;
  const m = midB.length;
  if ((n + 1) * (m + 1) > MAX_LCS_CELLS) return null;

  // lcs[i][j] = LCS length of midA[i..] and midB[j..]
  const w = m + 1;
  const lcs = new Uint32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] = midA[i] === midB[j]
        ? lcs[(i + 1) * w + j + 1] + 1
        : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1]);
    }
  }

  const ops: Op[] = [];
  for (let k = 0; k < pre; k++) ops.push({ kind: " ", line: a[k], oldNo: k + 1, newNo: k + 1 });
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && midA[i] === midB[j]) {
      ops.push({ kind: " ", line: midA[i], oldNo: pre + i + 1, newNo: pre + j + 1 });
      i++;
      j++;
    } else if (i < n && (j === m || lcs[(i + 1) * w + j] >= lcs[i * w + j + 1])) {
      ops.push({ kind: "-", line: midA[i], oldNo: pre + i + 1, newNo: pre + j });
      i++;
    } else {
      ops.push({ kind: "+", line: midB[j], oldNo: pre + i, newNo: pre + j + 1 });
      j++;
    }
  }
  for (let k = 0; k < suf; k++) {
    ops.push({ kind: " ", line: a[a.length - suf + k], oldNo: a.length - suf + k + 1, newNo: b.length - suf + k + 1 });
  }
  return ops;
}

// Unified diff hunks (no ---/+++ file header). "" when the texts are equal.
export function unifiedDiff(oldText: string, newText: string): string | null {
  const ops = diffOps(oldText.split("\n"), newText.split("\n"));
  if (!ops) return null;

  const changed: number[] = [];
  ops.forEach((op, idx) => { if (op.kind !== " ") changed.push(idx); });
  if (changed.length === 0) return "";

  const out: string[] = [];
  let c = 0;
  while (c < changed.length) {
    const start = Math.max(0, changed[c] - CONTEXT_LINES);
    let end = changed[c];
    // Merge changes whose context windows touch.
    while (c + 1 < changed.length && changed[c + 1] - end <= 2 * CONTEXT_LINES) end = changed[++c];
    c++;
    const stop = Math.min(ops.length - 1, end + CONTEXT_LINES);
    const hunk = ops.slice(start, stop + 1);
    const oldCount = hunk.filter((op) => op.kind !== "+").length;
    const newCount = hunk.filter((op) => op.kind !== "-").length;
    // Start = first line present on that side; with no lines on that side,
    // the line number just before the insertion/deletion point.
    const oldStart = hunk.find((op) => op.kind !== "+")?.oldNo ?? hunk[0].oldNo;
    const newStart = hunk.find((op) => op.kind !== "-")?.newNo ?? hunk[0].newNo;
    out.push(`@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`);
    for (const op of hunk) out.push(op.kind + op.line);
  }
  return out.join("\n");
}
