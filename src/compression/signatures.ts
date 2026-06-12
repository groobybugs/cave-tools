export interface Signature {
  kind: "fn" | "class" | "interface" | "type" | "enum" | "const" | "impl";
  name: string;
  params: string;
  returnType: string;
  isAsync: boolean;
  isExported: boolean;
  indent: number;
}

const TS_PATTERNS = [
  {
    kind: "fn" as const,
    regex:
      /^(\s*)(export\s+)?(async\s+)?function\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*:\s*([^\{]+))?\s*\{?/,
  },
  {
    kind: "class" as const,
    regex: /^(\s*)(export\s+)?(abstract\s+)?class\s+(\w+)/,
  },
  {
    kind: "interface" as const,
    regex: /^(\s*)(export\s+)?interface\s+(\w+)/,
  },
  {
    kind: "type" as const,
    regex: /^(\s*)(export\s+)?type\s+(\w+)/,
  },
  {
    kind: "enum" as const,
    regex: /^(\s*)(export\s+)?enum\s+(\w+)/,
  },
  {
    kind: "const" as const,
    regex:
      /^(\s*)(export\s+)?(const|let|var)\s+(\w+)(?:\s*:\s*([^=]+))?\s*=/,
  },
  {
    kind: "fn" as const,
    regex:
      /^(\s*)(export\s+)?(const|let|var)\s+(\w+)\s*=\s*(async\s+)?\(([^)]*)\)(?:\s*:\s*([^=]+))?\s*=>/,
  },
];

const RUST_PATTERNS = [
  {
    kind: "fn" as const,
    regex:
      /^(\s*)(pub\s+)?(async\s+)?fn\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*->\s*([^\{]+))?\s*\{?/,
  },
  {
    kind: "class" as const,
    regex: /^(\s*)(pub\s+)?struct\s+(\w+)/,
  },
  {
    kind: "enum" as const,
    regex: /^(\s*)(pub\s+)?enum\s+(\w+)/,
  },
  {
    kind: "interface" as const,
    regex: /^(\s*)(pub\s+)?trait\s+(\w+)/,
  },
  {
    kind: "type" as const,
    regex: /^(\s*)(pub\s+)?type\s+(\w+)/,
  },
  {
    kind: "impl" as const,
    regex: /^(\s*)impl\s+(?:(\w+)\s+for\s+)?(\w+)/,
  },
  {
    kind: "const" as const,
    regex: /^(\s*)(pub\s+)?(const|static)\s+(\w+)(?:\s*:\s*([^=]+))?\s*=/,
  },
];

function extractTsSignatures(content: string): Signature[] {
  const sigs: Signature[] = [];
  for (const line of content.split("\n")) {
    for (const pattern of TS_PATTERNS) {
      const match = pattern.regex.exec(line);
      if (!match) continue;

      const indent = match[1]?.length ?? 0;
      const isExported = !!match[2];
      const isAsync = !!(match[3]?.trim() === "async");

      let name: string;
      let params = "";
      let returnType = "";

      if (pattern.kind === "fn") {
        if (match[0].includes("function")) {
          name = match[4] ?? "";
          params = match[5] ?? "";
          returnType = (match[6] ?? "").trim();
        } else {
          name = match[4] ?? "";
          params = match[6] ?? "";
          returnType = (match[7] ?? "").trim();
        }
      } else if (pattern.kind === "const") {
        name = match[4] ?? "";
        returnType = (match[5] ?? "").trim();
      } else {
        name = match[match.length - 1] ?? "";
      }

      sigs.push({
        kind: pattern.kind,
        name: name.trim(),
        params: params.trim(),
        returnType: returnType.trim(),
        isAsync,
        isExported,
        indent,
      });
      break;
    }
  }
  return sigs;
}

function extractRustSignatures(content: string): Signature[] {
  const sigs: Signature[] = [];
  for (const line of content.split("\n")) {
    for (const pattern of RUST_PATTERNS) {
      const match = pattern.regex.exec(line);
      if (!match) continue;

      const indent = match[1]?.length ?? 0;
      const isExported = !!match[2];
      const isAsync = !!(match[3]?.trim() === "async");

      let name: string;
      let params = "";
      let returnType = "";

      if (pattern.kind === "fn") {
        name = match[4] ?? "";
        params = match[5] ?? "";
        returnType = (match[6] ?? "").trim();
      } else if (pattern.kind === "impl") {
        name = match[3] ?? "";
        const forType = match[2];
        if (forType) {
          returnType = `for ${forType.trim()}`;
        }
      } else if (pattern.kind === "const") {
        name = match[4] ?? "";
        returnType = (match[5] ?? "").trim();
      } else {
        name = match[match.length - 1] ?? "";
      }

      sigs.push({
        kind: pattern.kind,
        name: name.trim(),
        params: params.trim(),
        returnType: returnType.trim(),
        isAsync,
        isExported,
        indent,
      });
      break;
    }
  }
  return sigs;
}

export function extractSignatures(content: string, ext: string): Signature[] {
  switch (ext.toLowerCase()) {
    case ".ts":
    case ".tsx":
    case ".js":
    case ".jsx":
      return extractTsSignatures(content);
    case ".rs":
      return extractRustSignatures(content);
    default:
      return [];
  }
}

function compactType(typeStr: string): string {
  return typeStr.replace(/\s+/g, " ").trim();
}

export function formatSignature(sig: Signature): string {
  const exportPrefix = sig.isExported ? "export " : "";
  const asyncPrefix = sig.isAsync ? "async " : "";
  const indent = " ".repeat(sig.indent);

  switch (sig.kind) {
    case "fn": {
      const ret = sig.returnType ? ` → ${compactType(sig.returnType)}` : "";
      const params = compactType(sig.params);
      return `${indent}${exportPrefix}${asyncPrefix}fn ${sig.name}(${params})${ret}`;
    }
    case "class":
      return `${indent}${exportPrefix}class ${sig.name}`;
    case "interface":
      return `${indent}${exportPrefix}interface ${sig.name}`;
    case "type":
      return `${indent}${exportPrefix}type ${sig.name}`;
    case "enum":
      return `${indent}${exportPrefix}enum ${sig.name}`;
    case "const": {
      const ty = sig.returnType ? `: ${compactType(sig.returnType)}` : "";
      return `${indent}${exportPrefix}const ${sig.name}${ty}`;
    }
    case "impl": {
      const ret = sig.returnType ? ` ${compactType(sig.returnType)}` : "";
      return `${indent}impl ${sig.name}${ret}`;
    }
    default:
      return `${indent}${sig.kind} ${sig.name}`;
  }
}

export function formatSignatures(content: string, ext: string): string {
  const sigs = extractSignatures(content, ext);
  if (sigs.length === 0) return "";
  return sigs.map(formatSignature).join("\n");
}
