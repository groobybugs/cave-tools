#!/usr/bin/env node
// cave-tools — PostToolUseFailure + PermissionDenied hook.
//
// Both events fire only for `mcp__cave-tools__*` tools (see the matcher in
// hooks.json). Their job is to keep a failed or denied cave-tools call from
// pushing the model back onto the built-in Read/Grep/Glob/Bash:
//
//   PostToolUseFailure → additionalContext describing the cave-tools retry
//                        (force, cave__invalidate, allowFailure, bash_start).
//   PermissionDenied   → hookSpecificOutput.retry:true, so Claude Code tells
//                        the model it may retry the denied call. Claude Code
//                        ignores `retry` when the classifier produced no
//                        verdict, so emitting it unconditionally is safe.

'use strict';

const { getDefaultMode } = require('./cave-tools-config');

let raw = '';
let emitted = false;
process.stdin.on('data', (chunk) => { raw += chunk; });
process.stdin.on('end', emit);
process.stdin.on('error', emit);
if (process.stdin.isTTY) process.nextTick(emit);

function emit() {
  if (emitted) return;
  emitted = true;

  // "off" mode — cave-tools is dormant, don't steer anything.
  if (getDefaultMode() === 'off') {
    process.stdout.write('OK');
    process.exit(0);
  }

  let data = {};
  try { data = JSON.parse(raw); } catch (e) { /* keep defaults */ }

  const event = data.hook_event_name || '';
  const tool = String(data.tool_name || '');

  if (event === 'PermissionDenied') {
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: {
        hookEventName: 'PermissionDenied',
        retry: true,
      },
    }));
    process.exit(0);
  }

  if (event === 'PostToolUseFailure') {
    // An interrupt is the user stopping the tool — no recovery advice wanted.
    if (data.is_interrupt === true) {
      process.stdout.write('OK');
      process.exit(0);
    }
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: {
        hookEventName: 'PostToolUseFailure',
        additionalContext: buildRecovery(tool),
      },
    }));
    process.exit(0);
  }

  process.stdout.write('OK');
  process.exit(0);
}

function buildRecovery(tool) {
  const head = 'CAVE-TOOLS: `' + (tool || 'a cave-tools tool') + '` failed. ' +
    'Recover inside cave-tools — do NOT fall back to built-in Read/Grep/Glob/Bash, ' +
    'they are blocked by PreToolUse and cost more tokens.';

  const short = tool.replace(/^mcp__cave-tools__/, '');
  let hint;
  switch (short) {
    case 'cave__read':
      hint = 'Stale dedup cache or a stub? Call `cave__invalidate` with the path, or retry ' +
        '`cave__read` with `force: true`. Binary file? It is rejected by design — inspect it ' +
        'with `cave__bash` instead.';
      break;
    case 'cave__edit':
    case 'cave__apply_patch':
      hint = 'Re-read the target with `cave__read` (`line_numbers: true`) to refresh ' +
        '`file_hash` / `range_checksum`, then retry the edit with the new proof. A fuzzy ' +
        '`old_string` miss usually means the file moved on since the last read.';
      break;
    case 'cave__bash':
      hint = 'Expected a non-zero exit? Retry with `allowFailure: true`. Hit the 10min cap? ' +
        'Start it detached with `cave__bash_start` and poll `cave__bash_status`. Never ' +
        'double-wrap: `cave__bash` already prepends rtk.';
      break;
    case 'cave__websearch':
    case 'cave__webfetch':
      hint = 'Retry once; on a persistent network or upstream error say so rather than ' +
        'switching to built-in WebFetch/WebSearch.';
      break;
    case 'cave__grep':
    case 'cave__find':
    case 'cave__ls':
      hint = 'Check the path exists with `cave__ls`, and widen or simplify the pattern. ' +
        'A regex that fails to compile is the usual cause.';
      break;
    default:
      hint = 'Fix the arguments and retry the same cave-tools tool.';
  }
  return head + ' ' + hint;
}
