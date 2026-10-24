export interface RedactPattern {
  label: string;
  regex: RegExp;
}

const PATTERNS: RedactPattern[] = [
  {
    label: "Bearer token",
    regex: /(bearer\s+)([a-zA-Z0-9\-_.]{8,})/gi,
  },
  {
    label: "Authorization header",
    regex: /(authorization:\s*(?:basic|bearer|token)\s+)([^\s\r\n\[]+)/gi,
  },
  {
    label: "AWS key",
    regex: /(AKIA[0-9A-Z]{16})/g,
  },
  {
    label: "API key param",
    regex: /((?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|token|password|passwd|pwd|secret)\s*[=:]\s*)([^\s\r\n,;&"'\[]+)/gi,
  },
  {
    label: "Private key block",
    regex: /(-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----)([\s\S]*?)(-----END\s+(?:RSA\s+)?PRIVATE\s+KEY-----)/g,
  },
  {
    label: "GitHub token",
    regex: /(gh[pousr]_[a-zA-Z0-9]{20,})/g,
  },
  {
    label: "GitLab PAT",
    regex: /(glpat-[A-Za-z0-9_\-]{20,})/g,
  },
  {
    label: "OpenAI key",
    regex: /(sk-[A-Za-z0-9]{20,})/g,
  },
  {
    label: "Anthropic key",
    regex: /(sk-ant-[A-Za-z0-9_\-]{20,})/g,
  },
  {
    label: "npm token",
    regex: /(npm_[A-Za-z0-9]{10,})/g,
  },
  {
    label: "JWT",
    regex: /(eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]*)/g,
  },
  {
    label: "Database URL",
    regex: /((?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|rediss?):\/\/[^\s]*:)([^\s]+)(@)/g,
  },
  {
    label: "Generic long secret",
    regex: /((?:key|token|secret|password|credential|auth)\s*[=:]\s*['"]?)([a-zA-Z0-9+/=\-_]{32,})(['"]?)/gi,
  },
];

let redactionCount = 0;

export function redactSecrets(input: string): string {
  let result = input;

  for (const pattern of PATTERNS) {
    result = result.replace(pattern.regex, (...args: unknown[]) => {
      redactionCount++;
      // replace() passes (match, ...groups, offset, input): keep only the
      // capture groups. One group is the secret itself; with two or more the
      // first is a prefix to keep and the third an optional suffix.
      const groups = args.slice(1, args.findIndex((a, i) => i > 0 && typeof a === "number"));
      if (groups.length < 2) return `[REDACTED:${pattern.label}]`;
      const prefix = (groups[0] as string | undefined) ?? "";
      const after = (groups[2] as string | undefined) ?? "";
      return `${prefix}[REDACTED:${pattern.label}]${after}`;
    });
  }

  return result;
}
