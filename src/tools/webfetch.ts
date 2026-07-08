import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { applyBudget } from "../compression/utils.js";
import { archiveIfLarge } from "../compression/archive.js";
import { redactSecrets } from "../compression/redact.js";

const MAX_RESPONSE_BYTES = 5 * 1024 * 1024; // 5MB
const DEFAULT_TIMEOUT_SECONDS = 30;
const MAX_TIMEOUT_SECONDS = 120;

const CHROME_UA =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36";

interface WebFetchInput {
  url: string;
  format: "text" | "markdown" | "html";
  timeout: number;
  redact_secrets: boolean;
}

function acceptFor(format: WebFetchInput["format"]): string {
  switch (format) {
    case "markdown":
      return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1";
    case "text":
      return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1";
    case "html":
      return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1";
  }
}

function parseInput(args: Record<string, unknown>): WebFetchInput {
  const url = String(args.url || "").trim();
  const format = args.format === "text" || args.format === "html" ? args.format : "markdown";
  const rawTimeout = Number(args.timeout);
  const timeout = Number.isFinite(rawTimeout) && rawTimeout > 0
    ? Math.min(Math.trunc(rawTimeout), MAX_TIMEOUT_SECONDS)
    : DEFAULT_TIMEOUT_SECONDS;
  return { url, format, timeout, redact_secrets: args.redact_secrets !== false };
}

function isCloudflareChallenge(response: Response): boolean {
  return (
    response.status === 403 &&
    response.headers.get("cf-mitigated") === "challenge"
  );
}

class CloudflareChallengeError extends Error {
  constructor() {
    super("Cloudflare bot challenge");
    this.name = "CloudflareChallengeError";
  }
}

interface FetchResult {
  buffer: ArrayBuffer;
  contentType: string;
}

async function fetchBounded(
  url: string,
  headers: Record<string, string>,
  timeoutSeconds: number,
): Promise<FetchResult> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutSeconds * 1000);
  try {
    const response = await fetch(url, {
      method: "GET",
      headers,
      signal: controller.signal,
    });

    // Cloudflare bot challenge: caller retries with honest UA.
    if (isCloudflareChallenge(response)) throw new CloudflareChallengeError();

    if (!response.ok) throw new Error(`Request failed with status ${response.status}`);

    const declared = response.headers.get("content-length");
    const parsed = declared ? Number.parseInt(declared, 10) : undefined;
    if (parsed !== undefined && Number.isSafeInteger(parsed) && parsed > MAX_RESPONSE_BYTES) {
      throw new Error("Response too large (exceeds 5MB limit)");
    }
    const buffer = await response.arrayBuffer();
    if (buffer.byteLength > MAX_RESPONSE_BYTES) {
      throw new Error("Response too large (exceeds 5MB limit)");
    }
    return {
      buffer,
      contentType: response.headers.get("content-type") || "",
    };
  } catch (error) {
    if (error instanceof CloudflareChallengeError) throw error;
    if (error instanceof Error && error.name === "AbortError") {
      throw new Error(`Request timed out after ${timeoutSeconds}s`);
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

function isImageMime(mime: string): boolean {
  return mime.startsWith("image/") && mime !== "image/svg+xml" && mime !== "image/vnd.fastbidsheet";
}

// ── Self-contained HTML converters (no external deps) ──────────────────────

// Container skip-tags: have open+close pairs and suppress their inner content.
// Tracked via skipDepth so nested <noscript><style>…</style></noscript> works.
const CONTAINER_SKIP_TAGS = new Set(["script", "style", "noscript", "iframe", "object"]);

// Void elements: never have closing tags, never carry inner content. Dropping
// them must NOT touch skipDepth or everything after <meta charset> would vanish.
// br/hr are void too but stay out of this set: the markdown converter renders
// them explicitly, and this set is checked before the tag handlers run.
const VOID_TAGS = new Set([
  "meta", "link", "base", "img", "input", "area", "source", "track", "col", "wbr", "embed",
]);

const BLOCK_TAGS = new Set(["p", "div", "section", "article", "header", "footer", "main", "aside", "li", "tr", "blockquote"]);
const HEADING_TAGS: Record<string, string> = { h1: "#", h2: "##", h3: "###", h4: "####", h5: "#####", h6: "######" };

interface TagToken {
  tag: string;
  attrs: Record<string, string>;
  closing: boolean;
  self: boolean;
}

function parseAttrs(span: string): Record<string, string> {
  const attrs: Record<string, string> = {};
  const re = /([a-zA-Z_:][a-zA-Z0-9_:.-]*)\s*(?:=\s*"([^"]*)"|\s*=\s*'([^']*)'|\s*=\s*([^\s>]+))?/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(span)) !== null) {
    attrs[m[1].toLowerCase()] = m[2] ?? m[3] ?? m[4] ?? "";
  }
  return attrs;
}

function tokenizeHtml(html: string): Array<{ type: "text"; text: string } | { type: "tag"; token: TagToken }> {
  const tokens: Array<{ type: "text"; text: string } | { type: "tag"; token: TagToken }> = [];
  let i = 0;
  while (i < html.length) {
    const lt = html.indexOf("<", i);
    if (lt === -1) {
      tokens.push({ type: "text", text: html.slice(i) });
      break;
    }
    if (lt > i) tokens.push({ type: "text", text: html.slice(i, lt) });
    const gt = html.indexOf(">", lt);
    if (gt === -1) {
      tokens.push({ type: "text", text: html.slice(lt) });
      break;
    }
    const inner = html.slice(lt + 1, gt);
    const closing = inner.startsWith("/");
    const body = closing ? inner.slice(1) : inner;
    const self = body.endsWith("/");
    const tagBody = self ? body.slice(0, -1) : body;
    const space = tagBody.search(/\s/);
    const tag = (space === -1 ? tagBody : tagBody.slice(0, space)).toLowerCase();
    const attrs = space === -1 ? {} : parseAttrs(tagBody.slice(space + 1));
    tokens.push({ type: "tag", token: { tag, attrs, closing, self } });
    i = gt + 1;
  }
  return tokens;
}

function decodeEntities(text: string): string {
  return text
    .replace(/&nbsp;/gi, " ")
    .replace(/&amp;/gi, "&")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/&apos;/gi, "'")
    .replace(/&#(\d+);/g, (_, d) => {
      const n = Number(d);
      return n <= 0x10ffff ? String.fromCodePoint(n) : "";
    })
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => {
      const n = parseInt(h, 16);
      return n <= 0x10ffff ? String.fromCodePoint(n) : "";
    });
}

// Plain-text extraction: drop skip tags entirely, emit text from the rest.
function extractTextFromHtml(html: string): string {
  const tokens = tokenizeHtml(html);
  let out = "";
  let skipDepth = 0;
  for (const tok of tokens) {
    if (tok.type === "tag") {
      const { tag, closing } = tok.token;
      if (VOID_TAGS.has(tag)) continue;
      if (CONTAINER_SKIP_TAGS.has(tag)) {
        if (!closing) skipDepth++;
        else if (skipDepth > 0) skipDepth--;
      } else if (!closing && BLOCK_TAGS.has(tag) && out && !out.endsWith("\n")) {
        out += "\n";
      }
      continue;
    }
    if (skipDepth === 0) out += decodeEntities(tok.text);
  }
  return out.replace(/\n{3,}/g, "\n\n").trim();
}

// Lightweight HTML → markdown. Handles headings, links, lists, emphasis, code,
// blockquotes, hr. Unknown tags fall back to their inner text.
function convertHtmlToMarkdown(html: string): string {
  const tokens = tokenizeHtml(html);
  const out: string[] = [];
  let skipDepth = 0;
  let preDepth = 0;
  const listStack: Array<{ ordered: boolean; index: number }> = [];
  const linkHrefStack: string[] = [];

  const pushText = (text: string) => {
    if (skipDepth > 0) return;
    const decoded = decodeEntities(text);
    if (decoded) out.push(decoded);
  };

  for (const tok of tokens) {
    if (tok.type === "text") {
      pushText(tok.text);
      continue;
    }
    const { tag, attrs, closing, self } = tok.token;
    if (VOID_TAGS.has(tag)) continue;
    if (CONTAINER_SKIP_TAGS.has(tag)) {
      if (!closing && !self) skipDepth++;
      else if (closing && skipDepth > 0) skipDepth--;
      continue;
    }
    if (skipDepth > 0) continue;

    if (HEADING_TAGS[tag] && !closing) {
      out.push(`\n\n${HEADING_TAGS[tag]} `);
    } else if (tag === "br" && !closing) {
      out.push("\n");
    } else if (BLOCK_TAGS.has(tag) || tag === "body" || tag === "html") {
      if (!closing && !self && out.length && out[out.length - 1] !== "\n") out.push("\n");
      if ((closing || self) && out.length && out[out.length - 1] !== "\n") out.push("\n");
    } else if (tag === "a" && !closing && attrs.href) {
      linkHrefStack.push(attrs.href);
      out.push("[");
    } else if (tag === "a" && closing) {
      const href = linkHrefStack.pop() ?? "";
      out.push(`](${href})`);
    } else if ((tag === "strong" || tag === "b") && !closing) {
      out.push("**");
    } else if ((tag === "strong" || tag === "b") && closing) {
      out.push("**");
    } else if ((tag === "em" || tag === "i") && !closing) {
      out.push("*");
    } else if ((tag === "em" || tag === "i") && closing) {
      out.push("*");
    } else if ((tag === "code" || tag === "kbd" || tag === "samp") && !closing) {
      if (preDepth === 0) out.push("`");
    } else if ((tag === "code" || tag === "kbd" || tag === "samp") && closing) {
      if (preDepth === 0) out.push("`");
    } else if (tag === "pre" && !closing) {
      preDepth++;
      out.push("\n```\n");
    } else if (tag === "pre" && closing) {
      if (preDepth > 0) preDepth--;
      out.push("\n```\n");
    } else if (tag === "blockquote" && !closing) {
      out.push("\n> ");
    } else if (tag === "hr" && !closing) {
      out.push("\n\n---\n\n");
    } else if (tag === "ul" && !closing) {
      listStack.push({ ordered: false, index: 0 });
      out.push("\n");
    } else if (tag === "ul" && closing) {
      listStack.pop();
      out.push("\n");
    } else if (tag === "ol" && !closing) {
      listStack.push({ ordered: true, index: 0 });
      out.push("\n");
    } else if (tag === "ol" && closing) {
      listStack.pop();
      out.push("\n");
    } else if (tag === "li" && !closing) {
      const frame = listStack[listStack.length - 1];
      if (frame) {
        frame.index += 1;
        const marker = frame.ordered ? `${frame.index}. ` : "- ";
        out.push(`\n${"  ".repeat(listStack.length - 1)}${marker}`);
      } else {
        out.push("\n- ");
      }
    }
  }

  let text = out.join("");
  // Collapse stray whitespace from inline concatenation while preserving
  // fenced code blocks and explicit newlines.
  text = text
    .replace(/[ \t]+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/[ \t]{2,}/g, " ")
    .trim();
  return text;
}

export const webfetchTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__webfetch",
  description:
    "Fetch a URL and return its content as markdown, text, or html. Images are returned as base64 image content blocks. Non-2xx responses are errors. Output is redacted, archived if large, and trimmed to a line budget. Use when you need to retrieve and analyze a specific web page. Note: follows redirects and allows fetching any http(s) URL including localhost/private IPs (same posture as the built-in webfetch).",
  inputSchema: {
    type: "object",
    properties: {
      url: { type: "string", description: "Fully-formed http(s) URL to fetch" },
      format: {
        type: "string",
        enum: ["text", "markdown", "html"],
        description: "Return format. Defaults to markdown.",
      },
      timeout: { type: "number", description: `Timeout in seconds (max ${MAX_TIMEOUT_SECONDS})` },
      redact_secrets: { type: "boolean", description: "When false, skip secret redaction. Default true." },
    },
    required: ["url"],
  },
  handler: async (args) => {
    const input = parseInput(args);
    if (!input.url) {
      return { content: [{ type: "text", text: "Error: url is required" }], isError: true };
    }
    if (!/^https?:\/\//i.test(input.url)) {
      return {
        content: [{ type: "text", text: "URL must start with http:// or https://" }],
        isError: true,
      };
    }
    const url = input.url;

    const headers: Record<string, string> = {
      "User-Agent": CHROME_UA,
      Accept: acceptFor(input.format),
      "Accept-Language": "en-US,en;q=0.9",
    };

    try {
      let result: FetchResult;
      try {
        result = await fetchBounded(url, headers, input.timeout);
      } catch (error) {
        // Retry with honest UA only on Cloudflare bot challenge.
        if (error instanceof CloudflareChallengeError) {
          result = await fetchBounded(url, { ...headers, "User-Agent": "opencode" }, input.timeout);
        } else {
          throw error;
        }
      }

      const mime = result.contentType.split(";")[0]?.trim().toLowerCase() || "";

      if (isImageMime(mime)) {
        const base64 = Buffer.from(result.buffer).toString("base64");
        return {
          content: [
            { type: "text", text: `Image fetched successfully (${result.contentType})` },
            { type: "image", data: base64, mimeType: mime },
          ],
        };
      }

      const content = new TextDecoder().decode(result.buffer);
      let output: string;
      if (input.format === "html") {
        output = content;
      } else if (input.format === "text") {
        output = result.contentType.includes("text/html") ? extractTextFromHtml(content) : content;
      } else {
        output = result.contentType.includes("text/html") ? convertHtmlToMarkdown(content) : content;
      }

      if (input.redact_secrets) output = redactSecrets(output);
      const archive = await archiveIfLarge(output, `webfetch:${url}`);
      output = applyBudget(output, "bash");
      const archiveNote = archive ? `\n\n[Archived: ${archive.id} — use cave__compress expand ${archive.id}]` : "";
      return { content: [{ type: "text", text: `${output}${archiveNote}` }] };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return {
        content: [{ type: "text", text: `Unable to fetch ${input.url}: ${message}` }],
        isError: true,
      };
    }
  },
};
