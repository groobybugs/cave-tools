#!/usr/bin/env node
// cave-tools — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Writes flag file at $CLAUDE_CONFIG_DIR/.cave-tools-active (statusline reads this)
//   2. Emits cave-tools ruleset as hidden SessionStart context (from SKILL.md)
//   3. Detects missing statusline config and emits setup nudge

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, buildRuleset } = require('./cave-tools-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.cave-tools-active');
const settingsPath = path.join(claudeDir, 'settings.json');

const mode = getDefaultMode();

// "off" mode — skip activation entirely, don't write flag or emit rules
if (mode === 'off') {
  try { fs.unlinkSync(flagPath); } catch (e) {}
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file (symlink-safe)
safeWriteFlag(flagPath, mode);

// 2. Emit cave-tools ruleset (shared builder — single source of truth, reused
//    by the SubagentStart hook).
let output = 'CAVE-TOOLS MODE ACTIVE — level: ' + mode + '\n\n' + buildRuleset(mode);

// 3. Detect missing statusline config — nudge Claude to help set it up.
//    Only nudge if no statusline configured at all. If caveman owns it, we'll
//    suggest swapping in the wrapper script that chains both badges.
try {
  let hasStatusline = false;
  let hasWrapper = false;
  if (fs.existsSync(settingsPath)) {
    const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    if (settings.statusLine) {
      hasStatusline = true;
      const cmd = String(settings.statusLine.command || '');
      hasWrapper = cmd.includes('statusline-wrapper.sh');
    }
  }

  if (!hasStatusline) {
    const isWindows = process.platform === 'win32';
    const scriptName = isWindows ? 'cave-tools-statusline.ps1' : 'statusline-wrapper.sh';
    const scriptPath = path.join(__dirname, scriptName);
    const command = isWindows
      ? `powershell -ExecutionPolicy Bypass -File "${scriptPath}"`
      : `bash "${scriptPath}"`;
    const statusLineSnippet =
      '"statusLine": { "type": "command", "command": ' + JSON.stringify(command) + ' }';
    output += "\n\n" +
      "STATUSLINE SETUP NEEDED: cave-tools includes a statusline badge showing the active mode and live savings " +
      "(e.g. [CAVE-TOOLS] ↓42% • 18cache). It is not configured yet. " +
      "To enable, add this to " + path.join(claudeDir, 'settings.json') + ": " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  } else if (hasStatusline && !hasWrapper) {
    output += "\n\n" +
      "STATUSLINE WRAPPER AVAILABLE: a single line can show both caveman + cave-tools badges. " +
      "If the user has caveman installed, suggest switching their statusLine command to " +
      "`bash " + path.join(__dirname, 'statusline-wrapper.sh') + "` " +
      "which chains both badges on one line.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
