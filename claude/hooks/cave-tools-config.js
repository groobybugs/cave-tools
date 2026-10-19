#!/usr/bin/env node
// cave-tools — shared configuration resolver for hooks.
//
// Resolution order for default mode:
//   1. CAVE_TOOLS_MODE environment variable
//   2. Config file defaultMode field:
//      - $XDG_CONFIG_HOME/cave-tools/config.json (if set)
//      - ~/.config/cave-tools/config.json (macOS/Linux fallback)
//      - %APPDATA%\cave-tools\config.json (Windows fallback)
//   3. 'enforce'
//
// Mirrors the security-hardened patterns in caveman-config.js:
//   - safeWriteFlag: symlink-safe, atomic, 0600, ownership-verified
//   - readFlag: symlink-refused, size-capped, whitelist-validated

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');

const VALID_MODES = ['off', 'hint', 'enforce', 'strict'];
const DEFAULT_MODE = 'enforce';
const MAX_FLAG_BYTES = 32; // longest valid value 'enforce' (7) — 32 leaves slack without enabling exfil

function getConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'cave-tools');
  }
  if (process.platform === 'win32') {
    return path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'cave-tools'
    );
  }
  return path.join(os.homedir(), '.config', 'cave-tools');
}

function getConfigPath() {
  return path.join(getConfigDir(), 'config.json');
}

function getDefaultMode() {
  const envMode = process.env.CAVE_TOOLS_MODE;
  if (envMode && VALID_MODES.includes(envMode.toLowerCase())) {
    return envMode.toLowerCase();
  }
  try {
    const config = JSON.parse(fs.readFileSync(getConfigPath(), 'utf8'));
    if (config.defaultMode && VALID_MODES.includes(config.defaultMode.toLowerCase())) {
      return config.defaultMode.toLowerCase();
    }
  } catch (e) {
    // Config file doesn't exist or is invalid — fall through
  }
  return DEFAULT_MODE;
}

// Symlink-safe flag file write.
// Uses O_NOFOLLOW where available, writes atomically via temp + rename with
// 0600 permissions. Protects against local attackers replacing the predictable
// flag path (~/.claude/.cave-tools-active) with a symlink to clobber other files.
//
// When the parent directory is itself a symlink (legitimate pattern: ~/.claude
// symlinked to another drive or shared config dir), resolves through to the
// real path and verifies ownership on Unix (uid match). On Windows, uid checks
// are unavailable — falls back to verifying the resolved path lives under the
// user's home directory.
//
// Silent-fails on any filesystem error — the flag is best-effort.
// Set CAVE_TOOLS_DEBUG=1 to emit stderr diagnostics when writes are refused.
function safeWriteFlag(flagPath, content) {
  const debug = process.env.CAVE_TOOLS_DEBUG === '1';
  try {
    const flagDir = path.dirname(flagPath);
    fs.mkdirSync(flagDir, { recursive: true });

    let realFlagDir;
    try {
      const lstat = fs.lstatSync(flagDir);
      if (lstat.isSymbolicLink()) {
        realFlagDir = fs.realpathSync(flagDir);
        const realStat = fs.statSync(realFlagDir);
        if (!realStat.isDirectory()) {
          if (debug) process.stderr.write(`[cave-tools] safeWriteFlag: symlink target ${realFlagDir} is not a directory\n`);
          return;
        }
        if (typeof process.getuid === 'function') {
          if (realStat.uid !== process.getuid()) {
            if (debug) process.stderr.write(`[cave-tools] safeWriteFlag: symlink target ${realFlagDir} owned by uid ${realStat.uid}, not current user ${process.getuid()}\n`);
            return;
          }
        } else {
          const home = os.homedir();
          const normalizedReal = path.resolve(realFlagDir);
          const normalizedHome = path.resolve(home);
          if (!normalizedReal.toLowerCase().startsWith(normalizedHome.toLowerCase() + path.sep) &&
              normalizedReal.toLowerCase() !== normalizedHome.toLowerCase()) {
            if (debug) process.stderr.write(`[cave-tools] safeWriteFlag: symlink target ${normalizedReal} is outside home directory ${normalizedHome}\n`);
            return;
          }
        }
      } else {
        realFlagDir = flagDir;
      }
    } catch (e) {
      return;
    }

    const realFlagPath = path.join(realFlagDir, path.basename(flagPath));
    try {
      if (fs.lstatSync(realFlagPath).isSymbolicLink()) return;
    } catch (e) {
      if (e.code !== 'ENOENT') return;
    }

    const tempPath = path.join(realFlagDir, `.cave-tools-active.${process.pid}.${Date.now()}`);
    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | O_NOFOLLOW;
    let fd;
    try {
      fd = fs.openSync(tempPath, flags, 0o600);
      fs.writeSync(fd, String(content));
      try { fs.fchmodSync(fd, 0o600); } catch (e) { /* best-effort on Windows */ }
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    fs.renameSync(tempPath, realFlagPath);
  } catch (e) {
    // Silent fail
  }
}

// Symlink-safe, size-capped, whitelist-validated flag file read.
// Refuses symlinks at the target, caps the read at MAX_FLAG_BYTES, and rejects
// anything that isn't a known mode. Returns null on any anomaly.
//
// Without this, a local attacker with write access to ~/.claude/ could replace
// the flag with a symlink to ~/.ssh/id_rsa. Every reader would slurp that
// content and either echo it to the terminal or inject it into model context.
function readFlag(flagPath) {
  try {
    let st;
    try {
      st = fs.lstatSync(flagPath);
    } catch (e) {
      return null;
    }
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (st.size > MAX_FLAG_BYTES) return null;

    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let out;
    try {
      fd = fs.openSync(flagPath, flags);
      const buf = Buffer.alloc(MAX_FLAG_BYTES);
      const n = fs.readSync(fd, buf, 0, MAX_FLAG_BYTES, 0);
      out = buf.slice(0, n).toString('utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }

    const raw = out.trim().toLowerCase();
    if (!VALID_MODES.includes(raw)) return null;
    return raw;
  } catch (e) {
    return null;
  }
}

const COMPACT_BEGIN = '<!-- cave-tools-compact-begin -->';
const COMPACT_END = '<!-- cave-tools-compact-end -->';

// Keep non-table lines plus only the intensity-table row for the active mode.
function filterModeRows(text, mode) {
  return text.split('\n').filter((line) => {
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    return !tableRowMatch || tableRowMatch[1] === mode;
  }).join('\n');
}

// Pull the compact block delimited by the cave-tools-compact markers out of
// SKILL.md. Returns null when the markers are missing.
function extractCompactRuleset(skillContent, mode) {
  const begin = skillContent.indexOf(COMPACT_BEGIN);
  if (begin === -1) return null;
  const end = skillContent.indexOf(COMPACT_END, begin + COMPACT_BEGIN.length);
  if (end === -1) return null;
  return filterModeRows(skillContent.slice(begin + COMPACT_BEGIN.length, end), mode).trim();
}

// Hardcoded copy of the SKILL.md compact block, used when SKILL.md can't be
// found. test.mjs asserts it matches the SKILL.md block.
function fallbackRuleset(mode) {
  return filterModeRows(
    'Use cave tools instead of built-ins (same results, fewer tokens):\n' +
    '- Read→`cave__read`, Grep→`cave__grep`, Glob→`cave__find`, ls→`cave__ls`, Bash→`cave__bash`, WebSearch/WebFetch→`cave__websearch`/`cave__webfetch`.\n' +
    '- Never prefix `rtk` inside `cave__bash`; use `workdir`, not `cd`.\n' +
    '- Jobs over ~2 min: `cave__bash_start`, poll `cave__bash_status` (`wait` ≤60s), stop `cave__bash_stop`. Never sleep-poll.\n' +
    '- Edit: `cave__edit` old_string/new_string, or line range after `cave__read` `line_numbers=true` (pass `expected_hash`/`expected_range_checksum`). `cave__write` whole files, `cave__apply_patch` multi-file.\n' +
    '- After editing outside Cave Tools, call `cave__invalidate`.\n' +
    '- Read before edit, never in parallel. No write tools in Plan Mode.\n' +
    '| **hint** | Advisory; built-ins allowed. |\n' +
    '| **enforce** | Built-in Read/Grep/Glob/WebFetch/WebSearch are blocked. |\n' +
    '| **strict** | Also blocks Bash (background jobs/monitors OK); Edit/Write need prior cave__read; range/move need a hash proof. |',
    mode
  );
}

// Build the cave-tools ruleset text for a given mode.
//
// Reads SKILL.md at runtime (source of truth) and injects only its compact
// block (between the cave-tools-compact markers) to keep per-session tokens
// low. If the markers are missing, falls back to the old behavior: whole
// SKILL.md minus YAML frontmatter, intensity table filtered to the active
// level. Without SKILL.md, uses fallbackRuleset(). Shared by the SessionStart
// activation hook and the SubagentStart hook so the rule text never drifts.
function buildRuleset(mode) {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
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

  if (skillContent) {
    const compact = extractCompactRuleset(skillContent, mode);
    if (compact) return compact;
    // Strip YAML frontmatter
    const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');
    return filterModeRows(body, mode);
  }

  return fallbackRuleset(mode);
}

module.exports = {
  VALID_MODES,
  DEFAULT_MODE,
  MAX_FLAG_BYTES,
  getConfigDir,
  getConfigPath,
  getDefaultMode,
  safeWriteFlag,
  readFlag,
  buildRuleset,
  extractCompactRuleset,
  fallbackRuleset,
};
