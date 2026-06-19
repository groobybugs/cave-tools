#!/usr/bin/env node
// cave-tools — Claude Code SubagentStart hook
//
// SessionStart does NOT fire for subagents, so the prose ruleset injected by
// cave-tools-activate.js never reaches them. The PreToolUse redirect already
// hard-routes Read/Grep/Glob/Edit/Write inside subagents, but the steer toward
// cave__bash / cave__compress (which PreToolUse can't safely force) is missing.
//
// This hook fires when any subagent spawns (built-in Explore/Plan/general-purpose
// or custom) and injects the same cave-tools ruleset via additionalContext, so
// subagents prefer the optimized tools too. No flag write, no statusline nudge —
// those are host-session concerns and would be noise inside a subagent.

'use strict';

const { getDefaultMode, buildRuleset } = require('./cave-tools-config');

// Drain stdin (the SubagentStart payload: agent_type, agent_id, …). We don't
// need its fields, but consuming it avoids EPIPE on the writer side.
let raw = '';
process.stdin.on('data', (chunk) => { raw += chunk; });
process.stdin.on('end', emit);
// If stdin is not piped (manual invocation), don't hang.
process.stdin.on('error', emit);

function emit() {
  const mode = getDefaultMode();

  // "off" mode — inject nothing.
  if (mode === 'off') {
    process.stdout.write('OK');
    process.exit(0);
  }

  const ruleset =
    'CAVE-TOOLS ACTIVE (subagent) — level: ' + mode + '\n\n' +
    'You are a subagent. Prefer the cave-tools MCP tools over the built-ins for ' +
    'the same reasons the main session does — same results, fewer tokens.\n\n' +
    buildRuleset(mode);

  // Structured form is the reliable way to inject context from a SubagentStart
  // hook (mirrors PreToolUse/SessionStart hookSpecificOutput).
  const payload = {
    hookSpecificOutput: {
      hookEventName: 'SubagentStart',
      additionalContext: ruleset,
    },
  };
  process.stdout.write(JSON.stringify(payload));
  process.exit(0);
}
