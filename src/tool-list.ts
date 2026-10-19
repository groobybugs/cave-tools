import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { readTool } from "./tools/read.js";
import { bashTool } from "./tools/bash.js";
import { bashStartTool } from "./tools/bash-start.js";
import { bashStatusTool } from "./tools/bash-status.js";
import { bashStopTool } from "./tools/bash-stop.js";
import { writeTool } from "./tools/write.js";
import { invalidateTool } from "./tools/invalidate.js";
import { compressTool } from "./tools/compress.js";
import { statusTool } from "./tools/status.js";
import { configureTool } from "./tools/configure.js";
import { grepTool } from "./tools/grep.js";
import { findTool } from "./tools/find.js";
import { lsTool } from "./tools/ls.js";
import { editTool } from "./tools/edit.js";
import { applyPatchTool } from "./tools/apply-patch.js";
import { websearchTool } from "./tools/websearch.js";
import { webfetchTool } from "./tools/webfetch.js";
import { filterByProfile } from "./profile.js";

// Every tool schema, in tools/list order. Loads all tool modules, so the MCP
// server imports this lazily (only when it serves tools/list itself).
export const allTools: Tool[] = [
  readTool,
  bashTool,
  bashStartTool,
  bashStatusTool,
  bashStopTool,
  grepTool,
  findTool,
  lsTool,
  writeTool,
  invalidateTool,
  editTool,
  applyPatchTool,
  websearchTool,
  webfetchTool,
  compressTool,
  statusTool,
  configureTool,
];

export function listedTools(profile = process.env.CAVE_TOOLS_PROFILE): Tool[] {
  return filterByProfile(allTools, profile);
}
