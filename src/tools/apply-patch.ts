import { readFile, rm, writeFile } from "fs/promises";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { ensureParentDirectory, resolveMutationTarget } from "../runtime/path.js";
import { writeIfUnchanged } from "../runtime/file-mutation.js";

type Hunk =
  | { type: "add"; path: string; contents: string }
  | { type: "delete"; path: string }
  | { type: "update"; path: string; oldText: string; newText: string };

function parsePatch(text: string): Hunk[] {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  if (lines[0]?.trim() !== "*** Begin Patch") throw new Error("patch must start with *** Begin Patch");
  const hunks: Hunk[] = [];
  let i = 1;
  while (i < lines.length) {
    const line = lines[i];
    if (line?.trim() === "*** End Patch") return hunks;
    if (!line?.startsWith("*** ")) {
      i++;
      continue;
    }

    const add = line.match(/^\*\*\* Add File: (.+)$/);
    if (add) {
      i++;
      const body: string[] = [];
      while (i < lines.length && !lines[i].startsWith("*** ")) {
        const current = lines[i++];
        if (!current.startsWith("+")) throw new Error(`add file lines must start with +: ${add[1]}`);
        body.push(current.slice(1));
      }
      hunks.push({ type: "add", path: add[1], contents: body.join("\n") });
      continue;
    }

    const del = line.match(/^\*\*\* Delete File: (.+)$/);
    if (del) {
      hunks.push({ type: "delete", path: del[1] });
      i++;
      continue;
    }

    const update = line.match(/^\*\*\* Update File: (.+)$/);
    if (update) {
      i++;
      const oldLines: string[] = [];
      const newLines: string[] = [];
      while (i < lines.length && !lines[i].startsWith("*** ")) {
        const current = lines[i++];
        if (current.startsWith("@@")) continue;
        if (current.startsWith("-")) oldLines.push(current.slice(1));
        else if (current.startsWith("+")) newLines.push(current.slice(1));
        else if (current.startsWith(" ")) {
          oldLines.push(current.slice(1));
          newLines.push(current.slice(1));
        }
      }
      hunks.push({ type: "update", path: update[1], oldText: oldLines.join("\n"), newText: newLines.join("\n") });
      continue;
    }

    if (line.startsWith("*** Move to:")) throw new Error("apply_patch moves are not supported yet");
    throw new Error(`unknown patch directive: ${line}`);
  }
  throw new Error("patch must end with *** End Patch");
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}

function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}

export const applyPatchTool: Tool & { handler: (args: Record<string, unknown>) => Promise<ToolResult> } = {
  name: "cave__apply_patch",
  description:
    "Apply one patch containing add, update, and delete file operations. Operations apply sequentially; if a later operation fails, earlier operations remain applied and are reported. Moves are not supported.",
  inputSchema: {
    type: "object",
    properties: {
      patchText: {
        type: "string",
        description: "The full patch text describing add, update, and delete operations",
      },
    },
    required: ["patchText"],
  },
  handler: async (args) => {
    const patchText = String(args.patchText || "");
    if (!patchText.trim()) return err("patchText is required");

    let hunks: Hunk[];
    try {
      hunks = parsePatch(patchText);
    } catch (error) {
      return err(`apply_patch verification failed: ${error instanceof Error ? error.message : String(error)}`);
    }
    if (hunks.length === 0) return err("patch rejected: empty patch");

    const applied: string[] = [];
    const fail = (path: string) =>
      err(
        applied.length === 0
          ? `Unable to apply patch at ${path}`
          : `Patch partially applied before failing at ${path}. Applied: ${applied.join(", ")}`,
      );

    for (const hunk of hunks) {
      try {
        const target = await resolveMutationTarget(hunk.path);
        if (hunk.type === "add") {
          await ensureParentDirectory(target.canonical);
          await writeFile(target.canonical, hunk.contents.endsWith("\n") || hunk.contents === "" ? hunk.contents : `${hunk.contents}\n`, {
            encoding: "utf-8",
            flag: "wx",
          });
        } else if (hunk.type === "delete") {
          await rm(target.canonical);
        } else {
          const source = await readFile(target.canonical);
          const content = new TextDecoder("utf-8", { fatal: true }).decode(source);
          const oldText = content.includes(hunk.oldText) ? hunk.oldText : `${hunk.oldText}\n`;
          if (!content.includes(oldText)) return fail(hunk.path);
          const next = content.replace(oldText, hunk.newText.endsWith("\n") && oldText.endsWith("\n") ? hunk.newText : hunk.newText);
          await writeIfUnchanged(target.canonical, source, next);
        }
        invalidateFileCache(target.canonical);
        invalidateFileCache(hunk.path);
        recordEdit(target.canonical);
        applied.push(`${hunk.type === "add" ? "A" : hunk.type === "delete" ? "D" : "M"} ${hunk.path}`);
      } catch {
        return fail(hunk.path);
      }
    }

    return ok(["Applied patch sequentially:", ...applied].join("\n"));
  },
};
