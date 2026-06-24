import { createHash } from "crypto";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { applyBudget } from "../compression/utils.js";
import { archiveIfLarge } from "../compression/archive.js";
import { redactSecrets } from "../compression/redact.js";

export const NO_RESULTS = "No search results found. Please try a different query.";
export const EXA_URL = "https://mcp.exa.ai/mcp";
export const PARALLEL_URL = "https://search.parallel.ai/mcp";
export const MAX_NUM_RESULTS = 20;
export const MAX_CONTEXT_CHARACTERS = 50_000;
export const MAX_RESPONSE_BYTES = 256 * 1024;
export const DEFAULT_TIMEOUT_SECONDS = 25;
export const MAX_TIMEOUT_SECONDS = 120;

type Provider = "exa" | "parallel";

interface WebSearchInput {
  query: string;
  provider?: Provider;
  numResults?: number;
  livecrawl?: "fallback" | "preferred";
  type?: "auto" | "fast" | "deep";
  contextMaxCharacters?: number;
  timeout?: number;
  redact_secrets?: boolean;
}

function checksum(value: string): string {
  return createHash("sha1").update(value).digest("hex");
}

function truthy(name: string): boolean {
  return ["1", "true", "yes", "on"].includes(String(process.env[name] || "").toLowerCase());
}

function envProvider(): Provider | undefined {
  const value = process.env.CAVE_TOOLS_WEBSEARCH_PROVIDER || process.env.OPENCODE_WEBSEARCH_PROVIDER;
  return value === "exa" || value === "parallel" ? value : undefined;
}

export function selectProvider(query: string, override?: Provider): Provider {
  if (override) return override;
  const env = envProvider();
  if (env) return env;
  if (truthy("CAVE_TOOLS_ENABLE_PARALLEL") || truthy("OPENCODE_ENABLE_PARALLEL")) return "parallel";
  if (truthy("CAVE_TOOLS_ENABLE_EXA") || truthy("OPENCODE_ENABLE_EXA") || truthy("OPENCODE_EXPERIMENTAL_EXA")) return "exa";
  // Stable default mirrors opencode's split without needing a session id.
  return Number.parseInt(checksum(query).slice(0, 8), 16) % 2 === 0 ? "exa" : "parallel";
}

function exaUrl(apiKey: string | undefined): string {
  if (!apiKey) return EXA_URL;
  const url = new URL(EXA_URL);
  url.searchParams.set("exaApiKey", apiKey);
  return url.toString();
}

function parsePayload(payload: string): string | undefined {
  const trimmed = payload.trim();
  if (!trimmed.startsWith("{")) return undefined;
  let json: any;
  try {
    json = JSON.parse(trimmed);
  } catch {
    return undefined;
  }
  const content = json?.result?.content;
  if (!Array.isArray(content)) return undefined;
  const item = content.find((entry) => entry && typeof entry.text === "string" && entry.text.length > 0);
  return item?.text;
}

export function parseWebsearchResponse(body: string): string | undefined {
  const direct = parsePayload(body);
  if (direct) return direct;
  for (const line of body.split("\n")) {
    if (!line.startsWith("data: ")) continue;
    const data = parsePayload(line.slice(6));
    if (data) return data;
  }
  return undefined;
}

async function collectBoundedResponse(response: Response): Promise<string> {
  const declared = response.headers.get("content-length");
  const parsed = declared ? Number.parseInt(declared, 10) : undefined;
  if (parsed !== undefined && Number.isSafeInteger(parsed) && parsed > MAX_RESPONSE_BYTES) {
    throw new Error(`websearch response exceeded ${MAX_RESPONSE_BYTES} bytes`);
  }
  const bytes = Buffer.from(await response.arrayBuffer());
  if (bytes.byteLength > MAX_RESPONSE_BYTES) throw new Error(`websearch response exceeded ${MAX_RESPONSE_BYTES} bytes`);
  return bytes.toString("utf-8");
}

async function callMcp(url: string, tool: string, args: unknown, headers: Record<string, string>, timeoutSeconds: number): Promise<string | undefined> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutSeconds * 1000);
  try {
    const response = await fetch(url, {
      method: "POST",
      signal: controller.signal,
      headers: {
        accept: "application/json, text/event-stream",
        "content-type": "application/json",
        ...headers,
      },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: { name: tool, arguments: args },
      }),
    });
    if (!response.ok) throw new Error(`websearch request failed with HTTP ${response.status}`);
    return parseWebsearchResponse(await collectBoundedResponse(response));
  } finally {
    clearTimeout(timer);
  }
}

function positiveInt(value: unknown, fallback: number, max: number): number {
  const n = Number(value) || fallback;
  return Math.max(1, Math.min(Math.trunc(n), max));
}

function parseInput(args: Record<string, unknown>): WebSearchInput {
  const query = String(args.query || "").trim();
  const provider = args.provider === "exa" || args.provider === "parallel" ? args.provider : undefined;
  const livecrawl = args.livecrawl === "preferred" ? "preferred" : "fallback";
  const type = args.type === "fast" || args.type === "deep" ? args.type : "auto";
  return {
    query,
    provider,
    numResults: positiveInt(args.numResults, 8, MAX_NUM_RESULTS),
    livecrawl,
    type,
    contextMaxCharacters:
      args.contextMaxCharacters === undefined
        ? undefined
        : positiveInt(args.contextMaxCharacters, 10_000, MAX_CONTEXT_CHARACTERS),
    timeout: positiveInt(args.timeout, DEFAULT_TIMEOUT_SECONDS, MAX_TIMEOUT_SECONDS),
    redact_secrets: args.redact_secrets !== false,
  };
}

export const websearchTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__websearch",
  description:
    `Search the web using Exa or Parallel MCP backends, then run the result through Cave Tools redaction, archiving, and line-budget compression. Use for current information beyond knowledge cutoff. Current year: ${new Date().getFullYear()}.`,
  inputSchema: {
    type: "object",
    properties: {
      query: { type: "string", description: "Web search query" },
      provider: { type: "string", enum: ["exa", "parallel"], description: "Optional provider override" },
      numResults: { type: "number", description: `Number of results (default 8, max ${MAX_NUM_RESULTS})` },
      livecrawl: { type: "string", enum: ["fallback", "preferred"], description: "Live crawl mode for Exa" },
      type: { type: "string", enum: ["auto", "fast", "deep"], description: "Search type for Exa" },
      contextMaxCharacters: { type: "number", description: `Maximum context characters (max ${MAX_CONTEXT_CHARACTERS})` },
      timeout: { type: "number", description: `Timeout in seconds (max ${MAX_TIMEOUT_SECONDS})` },
      redact_secrets: { type: "boolean", description: "When false, skip secret redaction. Default true." },
    },
    required: ["query"],
  },
  handler: async (args) => {
    const input = parseInput(args);
    if (!input.query) return { content: [{ type: "text", text: "Error: query is required" }], isError: true };
    const provider = selectProvider(input.query, input.provider);

    try {
      const text = provider === "exa"
        ? await callMcp(
            exaUrl(process.env.EXA_API_KEY),
            "web_search_exa",
            {
              query: input.query,
              type: input.type,
              numResults: input.numResults,
              livecrawl: input.livecrawl,
              contextMaxCharacters: input.contextMaxCharacters,
            },
            {},
            input.timeout!,
          )
        : await callMcp(
            PARALLEL_URL,
            "web_search",
            {
              objective: input.query,
              search_queries: [input.query],
              session_id: checksum(input.query),
            },
            {
              "User-Agent": "cave-tools",
              ...(process.env.PARALLEL_API_KEY ? { Authorization: `Bearer ${process.env.PARALLEL_API_KEY}` } : {}),
            },
            input.timeout!,
          );

      let output = text || NO_RESULTS;
      if (input.redact_secrets) output = redactSecrets(output);
      const archive = await archiveIfLarge(output, `websearch:${provider}:${input.query}`);
      output = applyBudget(output, "bash");
      const archiveNote = archive ? `\n\n[Archived: ${archive.id} - use cave__compress expand ${archive.id}]` : "";
      return { content: [{ type: "text", text: `${output}${archiveNote}` }] };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return {
        content: [{ type: "text", text: `Unable to search the web for ${input.query}: ${message}` }],
        isError: true,
      };
    }
  },
};
