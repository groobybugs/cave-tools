/**
 * Consecutive identical-line run-length elision.
 *
 * Adapted from caveman engine/compressors/repetition.go
 * (https://github.com/JuliusBrussee/caveman) — MIT License
 * Copyright (c) 2026 Julius Brussee
 *
 * Default: exact-match only (minRun=3). Digit-masked comparison is opt-in
 * (`maskDigits: true`) so request IDs / counters are not over-collapsed.
 * Marker lines are never themselves collapsed (idempotent on re-entry).
 */

export interface CollapseRepetitionOptions {
  /** Collapse lines that differ only in digit runs. Default false. */
  maskDigits?: boolean;
  /** Minimum consecutive identical lines to collapse. Default 3. */
  minRun?: number;
}

const DEFAULT_MIN_RUN = 3;
const MARKER_PREFIX = "… ";
const MARKER_RE = /^… \d+ identical lines elided …$/;

export function repetitionMarker(elided: number): string {
  return `… ${elided} identical lines elided …`;
}

export function isRepetitionMarker(line: string): boolean {
  return (
    MARKER_RE.test(line) ||
    (line.startsWith(MARKER_PREFIX) && line.endsWith(" identical lines elided …"))
  );
}

function lineKey(line: string, maskDigits: boolean): string {
  return maskDigits ? line.replace(/\d+/g, "#") : line;
}

export function collapseRepetition(
  text: string,
  options: CollapseRepetitionOptions = {},
): string {
  const minRun = options.minRun ?? DEFAULT_MIN_RUN;
  const maskDigits = options.maskDigits === true;
  const lines = text.split("\n");
  const out: string[] = [];
  let collapsed = false;

  let i = 0;
  while (i < lines.length) {
    const current = lines[i]!;
    const key = lineKey(current, maskDigits);
    let j = i + 1;
    while (j < lines.length && lineKey(lines[j]!, maskDigits) === key) j++;
    const runLen = j - i;
    out.push(current);
    if (
      runLen >= minRun &&
      current.trim().length > 0 &&
      !isRepetitionMarker(current)
    ) {
      out.push(repetitionMarker(runLen - 1));
      collapsed = true;
    } else {
      for (let k = i + 1; k < j; k++) out.push(lines[k]!);
    }
    i = j;
  }

  if (!collapsed) return text;
  const result = out.join("\n");
  return result.length < text.length ? result : text;
}
