// Canonical routing rule for the opt-in Go implementation switch.
//
// The Go binary serves short-lived CLI paths, but two kinds of surface stay in
// TypeScript even when CAVE_TOOLS_IMPL=go:
//   - the stdio MCP server (long-lived: no startup win, and it owns the SDK)
//   - webfetch/websearch (they depend on Node fetch/undici behaviour)
//
// A bare invocation (no arguments) starts the stdio MCP server, so argc 0 is
// routed to TypeScript as well. src/impl-switch.ts imports this module for the
// runtime check and scripts/install-agent-instructions.mjs interpolates the
// same lists into the generated POSIX launcher shim, so the two cannot drift.
//
// The web tools have two dispatch forms in src/cli.ts and both reach the same
// runToolCommand handler: the direct `webfetch <url>` form and the explicit
// `tool webfetch <url>` form. TS_ROUTED_TOOLS feeds both checks.

export const TS_ROUTED_TOOLS = Object.freeze(["webfetch", "websearch"]);

export const TS_ROUTED_COMMANDS = Object.freeze(["mcp", ...TS_ROUTED_TOOLS]);
