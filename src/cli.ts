#!/usr/bin/env node

import { startServer } from "./index.js";

const args = process.argv.slice(2);

if (args[0] === "mcp" || args.length === 0) {
  startServer().catch((error) => {
    console.error("Fatal error:", error);
    process.exit(1);
  });
} else if (args[0] === "bench") {
  console.log("Benchmark mode - not yet implemented");
  process.exit(0);
} else if (args[0] === "install-agent") {
  console.log("Agent installer - not yet implemented");
  console.log("Supported agents: claude, codex, opencode, kiro");
  process.exit(0);
} else {
  console.error(`Unknown command: ${args[0]}`);
  console.error("Usage: cave-tools [mcp|bench|install-agent]");
  process.exit(1);
}
