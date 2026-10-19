// CAVE_TOOLS_PROFILE=standard (default) hides rarely used tools from
// tools/list to cut per-session schema tokens. The server still dispatches
// them by name, but clients only expose listed tools, so agents need
// CAVE_TOOLS_PROFILE=full to use them.
const STANDARD_HIDDEN = new Set(["cave__configure", "cave__compress", "cave__status"]);

export function filterByProfile<T extends { name: string }>(
  tools: T[],
  profile = process.env.CAVE_TOOLS_PROFILE,
): T[] {
  if ((profile ?? "").trim().toLowerCase() === "full") return tools;
  return tools.filter((tool) => !STANDARD_HIDDEN.has(tool.name));
}
