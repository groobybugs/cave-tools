#!/usr/bin/env node
// cave-tools — Claude Code PostToolUse hook for the native Bash tool.
//
// Compresses Bash output after it ran: pipes {command, stdout, stderr} through
// `cave-tools filter --json` and returns hookSpecificOutput.updatedToolOutput,
// the original tool_response with only stdout/stderr replaced (Claude ignores
// a value that does not match the Bash output shape). The command is never
// changed, so permission allow/deny rules keep matching.
//
// Observed Bash tool_response: {stdout, stderr, interrupted, isImage,
// noOutputExpected, ...optional returnCodeInterpretation, persistedOutputPath,
// persistedOutputSize, bashEditDiff, gitOperation}. Background launches add
// backgroundTaskId (plus backgroundCwdHint / timedOutAfterMs /
// backgroundedToDeliverMessage) with empty stdout.
//
// Passes through (no output) when: mode off, CAVE_TOOLS_FILTER_BASH=0,
// interrupted, isImage, background launch, output persisted to a file,
// command already starts with cave-tools (or rtk proxy / CAVE_RAW), output
// under 2 KB, or the filter call fails / exceeds ~5 s. Failed commands go to
// PostToolUseFailure, which cannot replace output, so they are left alone.

'use strict';

const path = require('path');
const os = require('os');
const { readFlag, bashFilterSkipReason, runCaveFilter } = require('./cave-tools-config');

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
  const res = input && input.tool_response;
  if (!input || input.tool_name !== 'Bash' || !res || typeof res !== 'object') process.exit(0);
  if (typeof res.stdout !== 'string' || res.interrupted === true || res.isImage === true) process.exit(0);
  if (res.backgroundTaskId || res.persistedOutputPath) process.exit(0);

  const command = input.tool_input && input.tool_input.command;
  const stderr = typeof res.stderr === 'string' ? res.stderr : '';
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  // Same as cave-tools-redirect.sh: missing/invalid flag means enforce.
  const mode = readFlag(path.join(claudeDir, '.cave-tools-active')) || 'enforce';
  if (bashFilterSkipReason(command, res.stdout + stderr, { mode, env: process.env })) process.exit(0);

  const out = runCaveFilter({ command: String(command || ''), stdout: res.stdout, stderr }, input.session_id, process.env);
  if (!out || out.stdout.length + out.stderr.length >= res.stdout.length + stderr.length) process.exit(0);

  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'PostToolUse',
      updatedToolOutput: { ...res, stdout: out.stdout, stderr: out.stderr },
    },
  }));
  process.exit(0);
}
