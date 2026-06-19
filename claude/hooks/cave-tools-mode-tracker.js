#!/usr/bin/env node
// cave-tools — UserPromptSubmit hook
//
// Two responsibilities:
//   1. Detect /cave-tools commands and natural-language activations/deactivations.
//      Updates the flag file accordingly.
//   2. Per-turn reinforcement: emit a short reminder when cave-tools is active.
//      The SessionStart hook injects the full ruleset once, but models drift
//      when other plugins inject competing style instructions every turn.
//      This keeps cave-tools visible in the model's attention on every prompt.

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
const { getDefaultMode, safeWriteFlag, readFlag, VALID_MODES } = require('./cave-tools-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.cave-tools-active');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim();
    const lower = prompt.toLowerCase();

    // Natural-language activation (e.g. "use cave-tools", "enable cave-tools",
    // "turn on cave-tools mode"). Only fires when no explicit deactivation in
    // the same prompt.
    if (/\b(use|enable|activate|turn on|start)\b.*\bcave[- ]?tools\b/i.test(lower) ||
        /\bcave[- ]?tools\b.*\b(use|enable|activate|turn on|start|mode)\b/i.test(lower)) {
      if (!/\b(stop|disable|turn off|deactivate)\b/i.test(lower)) {
        const mode = getDefaultMode();
        if (mode !== 'off') safeWriteFlag(flagPath, mode);
      }
    }

    // /cave-tools status — block the prompt, run `cave-tools status`, and
    // inject its output as the hook's reason. Skips the model round-trip
    // entirely.
    const statusMatch = /^\/cave-tools(?::cave-tools)?\s+status\s*$/i.exec(prompt);
    if (statusMatch) {
      try {
        const out = execFileSync('cave-tools', ['status'], { encoding: 'utf8', timeout: 5000 });
        process.stdout.write(JSON.stringify({ decision: 'block', reason: out.trim() }));
      } catch (e) {
        process.stdout.write(JSON.stringify({
          decision: 'block',
          reason: 'cave-tools status: could not run CLI. Try manually: cave-tools status'
        }));
      }
      return;
    }

    // /cave-tools [mode] — parse the slash command
    const slashMatch = /^\/cave-tools(?::cave-tools)?(?:\s+(\S+))?\s*$/i.exec(prompt);
    if (slashMatch) {
      const arg = (slashMatch[1] || '').toLowerCase();
      let mode = null;
      if (!arg) {
        mode = getDefaultMode();
      } else if (arg === 'off' || arg === 'stop' || arg === 'disable') {
        mode = 'off';
      } else if (VALID_MODES.includes(arg)) {
        mode = arg;
      }
      // Unknown arg → mode stays null, flag untouched (no silent overwrite)

      if (mode && mode !== 'off') {
        safeWriteFlag(flagPath, mode);
      } else if (mode === 'off') {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      }
    }

    // Natural-language deactivation
    if (/\b(stop|disable|deactivate|turn off)\b.*\bcave[- ]?tools\b/i.test(lower) ||
        /\bcave[- ]?tools\b.*\b(stop|disable|deactivate|turn off)\b/i.test(lower)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement. readFlag enforces symlink-safe read + size cap
    // + VALID_MODES whitelist. Returns null on any anomaly — we emit nothing
    // rather than inject untrusted bytes into model context.
    const activeMode = readFlag(flagPath);
    if (activeMode && activeMode !== 'off') {
      const reminder = buildReminder(activeMode);
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: 'UserPromptSubmit',
          additionalContext: reminder,
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});

function buildReminder(mode) {
  const base = 'CAVE-TOOLS MODE ACTIVE (' + mode + '). ' +
    'Prefer cave__read / cave__bash / cave__grep / cave__find / cave__ls over built-ins. ' +
    'Call cave__invalidate to refresh cache after external edits; cave__write to create/overwrite files. ' +
    'Never run `rtk <cmd>` inside cave__bash (already prepended).';
  if (mode === 'strict') {
    return base + ' STRICT: built-in Edit/Write also requires a prior cave__read of the target.';
  }
  if (mode === 'enforce') {
    return base + ' ENFORCE: built-in Read/Grep/Glob are blocked by PreToolUse.';
  }
  return base;
}
