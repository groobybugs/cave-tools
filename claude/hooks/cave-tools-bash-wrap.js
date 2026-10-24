#!/usr/bin/env node
// cave-tools — Claude Code PreToolUse hook for the native Bash tool.
//
// Rewrites tool_input.command to `cave-tools bash --session <id> -- '<cmd>'`
// so Claude keeps its own Bash tool (background shells, permission UI) while
// output still gets cave's rtk rewrite, redaction and budget compression.
//
// Returns only hookSpecificOutput.updatedInput — never permissionDecision —
// so the normal permission flow still decides. Claude Code evaluates
// permission rules against the updated input.
//
// Opt-in (CAVE_TOOLS_WRAP_BASH=1): the default is cave-tools-bash-filter.js,
// which compresses output after the run without touching the command.
// Passes the command through unchanged (no output) when: run_in_background,
// already wrapped (cave-tools / rtk proxy / CAVE_RAW), interactive, mode off,
// or cave-tools not on PATH. See
// bashWrapSkipReason in cave-tools-config.js (shared with the opencode plugin).

'use strict';

const path = require('path');
const os = require('os');
const { readFlag, bashWrapSkipReason, wrapBashCommand } = require('./cave-tools-config');

let raw = '';
process.stdin.on('data', (chunk) => { raw += chunk; });
process.stdin.on('end', run);
process.stdin.on('error', run);

function run() {
  let input;
  try {
    input = JSON.parse(raw || '{}');
  } catch (e) {
    process.exit(0);
  }
  if (!input || input.tool_name !== 'Bash' || !input.tool_input) process.exit(0);

  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  // Same as cave-tools-redirect.sh: missing/invalid flag means enforce.
  const mode = readFlag(path.join(claudeDir, '.cave-tools-active')) || 'enforce';
  const toolInput = input.tool_input;
  const command = toolInput.command;
  const skip = bashWrapSkipReason(command, {
    runInBackground: toolInput.run_in_background === true,
    mode,
    env: process.env,
  });
  if (skip) process.exit(0);

  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      updatedInput: { ...toolInput, command: wrapBashCommand(command, input.session_id) },
    },
  }));
  process.exit(0);
}
