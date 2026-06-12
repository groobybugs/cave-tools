interface CodebookEntry {
  id: string;
  pattern: string;
  frequency: number;
}

const MIN_PATTERN_LENGTH = 10;
const MIN_DOCUMENT_FREQUENCY = 3;
const MAX_ENTRIES = 50;
const MAX_TOTAL_LINES = 50_000;

export class Codebook {
  private entries: CodebookEntry[] = [];
  private patternToId = new Map<string, string>();
  private files: { path: string; content: string }[] = [];
  private nextId = 0;

  addFile(path: string, content: string): void {
    this.files.push({ path, content });
    if (this.files.length >= MIN_DOCUMENT_FREQUENCY) {
      this.rebuild();
    }
  }

  private rebuild(): void {
    const totalLines = this.files.reduce(
      (sum, f) => sum + f.content.split("\n").length,
      0,
    );
    if (totalLines > MAX_TOTAL_LINES) return;

    const docFreq = new Map<string, Set<number>>();
    const termFreq = new Map<string, number>();

    for (let docIdx = 0; docIdx < this.files.length; docIdx++) {
      const { content } = this.files[docIdx];
      const seenInDoc = new Set<string>();

      for (const line of content.split("\n")) {
        const normalized = this.normalizeLine(line);
        if (normalized.length < MIN_PATTERN_LENGTH) continue;

        termFreq.set(normalized, (termFreq.get(normalized) ?? 0) + 1);

        if (!seenInDoc.has(normalized)) {
          seenInDoc.add(normalized);
          const docs = docFreq.get(normalized) ?? new Set();
          docs.add(docIdx);
          docFreq.set(normalized, docs);
        }
      }
    }

    const candidates: Array<{ pattern: string; freq: number; df: number }> = [];
    for (const [pattern, docs] of docFreq) {
      if (docs.size >= MIN_DOCUMENT_FREQUENCY) {
        candidates.push({
          pattern,
          freq: termFreq.get(pattern) ?? 0,
          df: docs.size,
        });
      }
    }

    candidates.sort((a, b) => b.freq - a.freq);

    this.entries = [];
    this.patternToId.clear();
    this.nextId = 0;

    for (const candidate of candidates.slice(0, MAX_ENTRIES)) {
      const id = `§${this.nextId}`;
      this.nextId++;
      this.patternToId.set(candidate.pattern, id);
      this.entries.push({
        id,
        pattern: candidate.pattern,
        frequency: candidate.freq,
      });
    }
  }

  compress(content: string): { text: string; refsUsed: string[] } {
    if (this.entries.length === 0) {
      return { text: content, refsUsed: [] };
    }

    const refsUsed: string[] = [];
    const lines = content.split("\n");
    const result: string[] = [];

    for (const line of lines) {
      const normalized = this.normalizeLine(line);
      const id = this.patternToId.get(normalized);
      if (id) {
        if (!refsUsed.includes(id)) refsUsed.push(id);
        result.push(`[${id}]`);
      } else {
        result.push(line);
      }
    }

    return { text: result.join("\n"), refsUsed };
  }

  formatLegend(refsUsed: string[]): string {
    if (refsUsed.length === 0) return "";

    const lines = ["§CODEBOOK:"];
    for (const entry of this.entries) {
      if (refsUsed.includes(entry.id)) {
        const short =
          entry.pattern.length > 60
            ? `${entry.pattern.slice(0, 57)}...`
            : entry.pattern;
        lines.push(`  ${entry.id}=${short}`);
      }
    }
    return lines.join("\n");
  }

  private normalizeLine(line: string): string {
    return line.split(/\s+/).filter(Boolean).join(" ");
  }

  size(): number {
    return this.entries.length;
  }
}

const globalCodebook = new Codebook();

export function addCodebookFile(path: string, content: string): void {
  globalCodebook.addFile(path, content);
}

export function compressWithCodebook(
  content: string,
): { text: string; legend: string; refsUsed: string[] } {
  const { text, refsUsed } = globalCodebook.compress(content);
  const legend =
    refsUsed.length >= 3 ? globalCodebook.formatLegend(refsUsed) : "";
  return { text, legend, refsUsed };
}

export function getCodebookSize(): number {
  return globalCodebook.size();
}
