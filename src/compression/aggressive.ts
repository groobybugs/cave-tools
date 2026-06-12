interface CommentConfig {
  line: string[];
  blockStart: string;
  blockEnd: string;
  preserveDoc: boolean;
}

const LANG_CONFIG: Record<string, CommentConfig> = {
  ".ts": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".tsx": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".js": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".jsx": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".rs": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".go": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".c": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".cpp": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".cc": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".h": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".hpp": { line: ["//"], blockStart: "/*", blockEnd: "*/", preserveDoc: true },
  ".py": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".sh": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".bash": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".zsh": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".yml": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".yaml": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".toml": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".rb": { line: ["#"], blockStart: "", blockEnd: "", preserveDoc: false },
  ".html": { line: [], blockStart: "<!--", blockEnd: "-->", preserveDoc: false },
  ".htm": { line: [], blockStart: "<!--", blockEnd: "-->", preserveDoc: false },
  ".xml": { line: [], blockStart: "<!--", blockEnd: "-->", preserveDoc: false },
  ".svg": { line: [], blockStart: "<!--", blockEnd: "-->", preserveDoc: false },
  ".vue": { line: [], blockStart: "<!--", blockEnd: "-->", preserveDoc: false },
  ".sql": { line: ["--"], blockStart: "/*", blockEnd: "*/", preserveDoc: false },
};

function isInsideString(line: string, position: number): boolean {
  let inString: '"' | "'" | "`" | null = null;
  let escaped = false;

  for (let i = 0; i < position; i++) {
    const c = line[i];
    if (!c) continue;

    if (inString) {
      if (escaped) {
        escaped = false;
      } else if (c === "\\") {
        escaped = true;
      } else if (c === inString) {
        inString = null;
      }
    } else if (c === '"' || c === "'" || c === "`") {
      inString = c;
    }
  }

  return inString !== null;
}

function isDocComment(line: string, config: CommentConfig): boolean {
  if (!config.preserveDoc) return false;
  const trimmed = line.trim();
  return trimmed.startsWith("///") || trimmed.startsWith("/**") || trimmed.startsWith("//!");
}

export function aggressiveCompress(content: string, ext: string): string {
  const config = LANG_CONFIG[ext.toLowerCase()];
  if (!config) return content;

  const lines = content.split("\n");
  const result: string[] = [];
  let inBlockComment = false;
  let blockNesting = 0;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!line) {
      if (result.length > 0 && result[result.length - 1] !== "") {
        result.push("");
      }
      continue;
    }

    // Preserve shebang
    if (i === 0 && line.trimStart().startsWith("#!")) {
      result.push(line);
      continue;
    }

    // Block comments
    if (inBlockComment) {
      if (config.blockEnd && line.includes(config.blockEnd)) {
        blockNesting--;
        if (blockNesting <= 0) {
          inBlockComment = false;
          blockNesting = 0;
        }
      } else if (config.blockStart && line.includes(config.blockStart)) {
        blockNesting++;
      }
      continue;
    }

    if (config.blockStart && line.includes(config.blockStart)) {
      const trimmed = line.trim();
      if (!isDocComment(trimmed, config)) {
        const startIdx = line.indexOf(config.blockStart);
        const endIdx = line.indexOf(config.blockEnd, startIdx + config.blockStart.length);
        if (endIdx === -1) {
          inBlockComment = true;
          blockNesting = 1;
        }
        // Keep code before block comment
        const before = line.slice(0, startIdx).trimEnd();
        if (before) result.push(before);
        continue;
      }
    }

    // Line comments
    let codeLine = line;
    for (const marker of config.line) {
      let idx = codeLine.indexOf(marker);
      while (idx !== -1) {
        if (!isInsideString(codeLine, idx) && !isDocComment(codeLine.slice(idx), config)) {
          codeLine = codeLine.slice(0, idx).trimEnd();
          break;
        }
        idx = codeLine.indexOf(marker, idx + 1);
      }
    }

    const trimmed = codeLine.trim();
    if (!trimmed) {
      if (result.length > 0 && result[result.length - 1] !== "") {
        result.push("");
      }
      continue;
    }

    result.push(codeLine);
  }

  // Collapse multiple blank lines to single
  const collapsed: string[] = [];
  for (const line of result) {
    if (line === "") {
      if (collapsed.length > 0 && collapsed[collapsed.length - 1] !== "") {
        collapsed.push(line);
      }
    } else {
      collapsed.push(line);
    }
  }

  // Trim trailing blank
  while (collapsed.length > 0 && collapsed[collapsed.length - 1] === "") {
    collapsed.pop();
  }

  const output = collapsed.join("\n");
  return output.length < content.length ? output : content;
}
