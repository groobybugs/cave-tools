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
const { getDefaultMode, safeWriteFlag } = require('./cave-tools-config');

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

// 2. Emit cave-tools ruleset.
//    Reads SKILL.md at runtime so edits to the source of truth propagate
//    automatically — no hardcoded duplication to go stale.
//
//    Plugin installs: __dirname = <plugin_root>/hooks/, SKILL.md at <plugin_root>/skills/cave-tools/SKILL.md
//    Standalone installs: SKILL.md may live at $CLAUDE_CONFIG_DIR/skills/cave-tools/SKILL.md.
//    Falls back to a hardcoded mini-ruleset if not found.

const skillCandidates = [
  path.join(__dirname, '..', 'skills', 'cave-tools', 'SKILL.md'),
  path.join(claudeDir, 'skills', 'cave-tools', 'SKILL.md'),
];

let skillContent = '';
for (const candidate of skillCandidates) {
  try {
    skillContent = fs.readFileSync(candidate, 'utf8');
    if (skillContent) break;
  } catch (e) { /* try next */ }
}

let output;

if (skillContent) {
  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

  // Filter intensity table: keep header rows + only the active level's row
  const filtered = body.split('\n').reduce((acc, line) => {
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      if (tableRowMatch[1] === mode) acc.push(line);
      return acc;
    }
    acc.push(line);
    return acc;
  }, []);

  output = 'CAVE-TOOLS MODE ACTIVE — level: ' + mode + '\n\n' + filtered.join('\n');
} else {
  // Fallback when SKILL.md is not found.
  output =
    'CAVE-TOOLS MODE ACTIVE — level: ' + mode + '\n\n' +
    'Prefer cave-tools over built-in Read/Grep/Glob/Bash.\n\n' +
    '## Persistence\n\n' +
    'ACTIVE EVERY RESPONSE. Off only: `/cave-tools off` or "stop cave-tools".\n\n' +
    'Current level: **' + mode + '**. Switch: `/cave-tools off|hint|enforce|strict`.\n\n' +
    '## Rules\n\n' +
    '- `cave__read` instead of Read — optimized drop-in replacement (dedup cache + line budgets).\n' +
    '- `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.\n' +
    '- `cave__bash` instead of Bash — RTK rewriting + Stone Tablet + Flint Chipper.\n' +
    '- Never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).\n' +
    '- `cave__write` to create/overwrite a single file; `cave__edit` for string replacement (fuzzy whitespace-tolerant matching).\n' +
    '- After editing a file outside Cave Tools, call `cave__invalidate` with the changed path(s) so the next cave__read returns fresh content.\n' +
    '- Use `cave__compress` for large pasted or tool-produced text.\n' +
    '- Use `cave__status` to inspect savings, hit rate, budgets.\n\n' +
    '## Edit Safety\n\n' +
    'In Plan Mode / read-only phase, never call write tools. Before any edit, read the exact target path first. Do not batch read+edit in parallel.';
}

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
