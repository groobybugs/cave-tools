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
    'Prefer cave tools over built-ins (same results, fewer tokens):\n' +
    '- Read→`cave__read`, Grep→`cave__grep`, Glob→`cave__find`, ls→`cave__ls`, WebSearch/WebFetch→`cave__websearch`/`cave__webfetch`.\n' +
    '- Native Bash auto-compressed (failures: `cave__bash`); never prefix `cave-tools`/`rtk`.\n' +
    '- Background for servers/watchers; long jobs: `cave__bash_start`/`_status` (`wait` ≤60s). No sleep-poll.\n' +
    '- `[cave: full output … at <path>]`: `cave__read`/`cave__grep` it.\n' +
    '- Edit: `cave__edit` old_string/new_string, or line range after `cave__read` `line_numbers=true` (pass `expected_hash`/`expected_range_checksum`). `cave__write` whole files, `cave__apply_patch` multi-file.\n' +
    '- `cave__invalidate` after outside edits. Read before edit, never parallel. No writes in Plan Mode.\n' +
    '| **hint** | Advisory; built-ins allowed. |\n' +
    '| **enforce** | Built-in Read/Grep/Glob/WebFetch/WebSearch are blocked. |\n' +
    '| **strict** | Also blocks native Bash (background/monitors OK); Edit/Write need prior cave__read; range/move need hash. |',
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

// ── Native Bash wrapping ──────────────────────────────────────────────────
// Shared by the Claude Code PreToolUse hook (cave-tools-bash-wrap.js) and the
// opencode plugin: rewrite a native shell command to run through
// `cave-tools bash`, so the agent keeps its own shell tool (background jobs,
// permissions UI) while output still gets cave's compression.

const SHELL_SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/;

// POSIX single-quote escaping, same rules as joinCommandArgs in src/cli-tools.ts.
function shellQuote(arg) {
  const s = String(arg);
  return SHELL_SAFE.test(s) ? s : "'" + s.replace(/'/g, "'\\''") + "'";
}

const ENV_PREFIX = String.raw`(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*`;
const ALREADY_WRAPPED = new RegExp('^\\s*' + ENV_PREFIX + '(?:rtk\\s+(?:proxy\\b|cave-tools\\b)|cave-tools\\b)');
const INTERACTIVE_CMDS = new Set(['vim', 'nvim', 'vi', 'nano', 'less', 'more', 'man', 'top', 'htop', 'ssh']);

// First word of each command segment (split on ; & | && || and newlines),
// skipping env assignments plus sudo/env/exec/rtk prefixes.
function segmentHeads(command) {
  const heads = [];
  for (const seg of command.split(/[;&|\n]+|\$\(|`/)) {
    const words = seg.trim().replace(/^[({\s]+/, '').split(/\s+/).filter(Boolean);
    let i = 0;
    while (i < words.length && (/^[A-Za-z_][A-Za-z0-9_]*=/.test(words[i]) || ['sudo', 'env', 'exec', 'command', 'rtk'].includes(words[i]))) i++;
    if (i < words.length) heads.push({ head: path.basename(words[i]), rest: words.slice(i + 1) });
  }
  return heads;
}

function isInteractiveCommand(command) {
  if (/<</.test(command)) return true; // heredoc / here-string stdin
  for (const { head, rest } of segmentHeads(command)) {
    if (INTERACTIVE_CMDS.has(head)) return true;
    if (head === 'docker' && (rest[0] === 'run' || rest[0] === 'exec')) {
      const short = rest.filter((w) => /^-[a-zA-Z]+$/.test(w)).join('');
      if ((short.includes('i') || rest.includes('--interactive')) && (short.includes('t') || rest.includes('--tty'))) return true;
    }
    if (head === 'git' && rest[0] === 'rebase' && rest.some((w) => w === '-i' || w === '--interactive')) return true;
    if (head === 'git' && rest[0] === 'add' && rest.some((w) => w === '-p' || w === '--patch' || w === '-i' || w === '--interactive')) return true;
  }
  return false;
}

function isOnPath(bin, env) {
  const dirs = String((env && env.PATH) || '').split(path.delimiter).filter(Boolean);
  const exts = process.platform === 'win32' ? ['', '.cmd', '.exe', '.bat'] : [''];
  for (const dir of dirs) {
    for (const ext of exts) {
      try {
        fs.accessSync(path.join(dir, bin + ext), fs.constants.X_OK);
        return true;
      } catch (e) {
        // keep looking
      }
    }
  }
  return false;
}

// Returns the reason to leave the command alone, or null when it should be
// wrapped. opts: { runInBackground, mode, env }.
function bashWrapSkipReason(command, opts) {
  const o = opts || {};
  const env = o.env || process.env;
  if (typeof command !== 'string' || !command.trim()) return 'empty';
  // Opt-in: rewriting the command stops permission allow/deny rules matching.
  if (env.CAVE_TOOLS_WRAP_BASH !== '1') return 'not-enabled';
  if (o.mode === 'off') return 'mode-off';
  if (o.runInBackground === true) return 'background';
  if (ALREADY_WRAPPED.test(command) || /CAVE_RAW/.test(command)) return 'already-wrapped';
  if (isInteractiveCommand(command)) return 'interactive';
  if (!isOnPath('cave-tools', env)) return 'no-cave-tools';
  return null;
}

function wrapBashCommand(command, sessionId) {
  const session = sessionId ? ' --session ' + shellQuote(sessionId) : '';
  return 'cave-tools bash' + session + ' -- ' + "'" + command.replace(/'/g, "'\\''") + "'";
}

// ── Native Bash output filtering (default) ────────────────────────────────
// After a native shell call, hand its output to `cave-tools filter --json`
// and show the model the compressed result. Unlike the wrap, the command
// itself is never changed, so permission allow/deny rules keep matching.

// Below ~2 KB (~500 tokens) the pipeline rarely saves anything, and a node
// spawn per call (~100-200 ms) would cost more than it saves.
const FILTER_MIN_BYTES = 2048;
const FILTER_TIMEOUT_MS = 5000;

function bashFilterSkipReason(command, combined, opts) {
  const o = opts || {};
  const env = o.env || process.env;
  if (env.CAVE_TOOLS_FILTER_BASH === '0') return 'opt-out';
  if (o.mode === 'off') return 'mode-off';
  const cmd = typeof command === 'string' ? command : '';
  if (ALREADY_WRAPPED.test(cmd) || /CAVE_RAW/.test(cmd)) return 'already-wrapped';
  if (Buffer.byteLength(String(combined || ''), 'utf8') < FILTER_MIN_BYTES) return 'small';
  return null;
}

// Returns {stdout, stderr} from the CLI, or null on any failure/timeout so
// callers keep the original output. Calls the binary, never dist/ directly.
function runCaveFilter(payload, sessionId, env) {
  const { spawnSync } = require('child_process');
  const args = ['filter', '--json'];
  if (sessionId) args.push('--session', String(sessionId));
  try {
    const r = spawnSync('cave-tools', args, {
      input: JSON.stringify(payload),
      encoding: 'utf8',
      env: env || process.env,
      timeout: FILTER_TIMEOUT_MS,
      maxBuffer: 16 * 1024 * 1024,
      stdio: ['pipe', 'pipe', 'ignore'],
    });
    if (r.error || r.status !== 0 || !r.stdout) return null;
    const out = JSON.parse(r.stdout);
    if (!out || typeof out.stdout !== 'string' || typeof out.stderr !== 'string') return null;
    return out;
  } catch (e) {
    return null;
  }
}

module.exports = {
  FILTER_MIN_BYTES,
  bashFilterSkipReason,
  runCaveFilter,
  shellQuote,
  isInteractiveCommand,
  bashWrapSkipReason,
  wrapBashCommand,
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
