// cave-tools — opencode plugin
//
// Mirrors Claude Code hooks as closely as opencode allows.
// OpenCode 2 reads { id, setup(ctx) }. OpenCode 1.18.29+ reads server().
//   V2: session.hook("prompt"|"context"|"compaction"), tool.hook("execute.before")
//   V1: event / chat.message / experimental.chat.system.transform /
//       experimental.session.compacting / tool.execute.before
//
// Always-on ruleset lives in ~/.config/opencode/AGENTS.md (Tier-3 base).
// This plugin handles dynamic state only.
//
// Layout once installed:
//   ~/.config/opencode/plugins/cave-tools/
//   ├── package.json
//   ├── plugin.js              ← this file
//   └── cave-tools-config.cjs  ← copied from claude/hooks/cave-tools-config.js
//
// Register an absolute plugin path. Relative ./plugins/... breaks when Orca
// sets OPENCODE_CONFIG_DIR to ~/.config/orca/opencode-hooks/shared.
//
// See: https://opencode.ai/v2/docs/build/plugins
// Caveman reference: JuliusBrussee/caveman src/plugins/opencode/plugin.js

import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { existsSync, unlinkSync, readFileSync, lstatSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

// Load shared config helpers. Installed: cave-tools-config.cjs next to this
// file. Dev: fall back to claude/hooks/cave-tools-config.js.
// Evaluate as CommonJS by hand — compiled Bun binary rejects require() of
// on-disk files and empty-namespace import() of CJS (same pattern as caveman).
function loadConfig() {
  const installed = join(here, 'cave-tools-config.cjs');
  const dev = join(here, '..', '..', '..', 'claude', 'hooks', 'cave-tools-config.js');
  const target = existsSync(installed) ? installed : dev;
  const code = readFileSync(target, 'utf8').replace(/^#![^\n]*\n/, '');
  const mod = { exports: {} };
  new Function('module', 'exports', 'require', '__dirname', '__filename', code)(
    mod, mod.exports, createRequire(import.meta.url), dirname(target), target
  );
  return mod.exports;
}
const config = loadConfig();
const { getDefaultMode, safeWriteFlag, readFlag, VALID_MODES, bashWrapSkipReason, wrapBashCommand, bashFilterSkipReason, runCaveFilter } = config;

function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  }
  return path.join(os.homedir(), '.config', 'opencode');
}

const flagPath = path.join(opencodeConfigDir(), '.cave-tools-active');
let reloadToolRegistry = null;
// V1 has no per-request tool catalog. V2 sets this from the context hook so a
// native read is not blocked before its Cave replacement is actually offered.
let sawContextTools = false;
let generationReady = new Set();

function readRegistryPath() {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  return path.join(claudeDir, 'cave-tools', 'read-registry.txt');
}

function reinforcementLine(mode) {
  const base = 'CAVE-TOOLS MODE ACTIVE (' + mode + '). ' +
    'Prefer cave__read / cave__grep / cave__find / cave__ls over built-ins. ' +
    'Native shell output is auto-compressed after it runs (cave__bash also fine); never prefix cave-tools or rtk yourself. ' +
    'Background shell for servers/watchers (uncompressed); cave__bash_start + cave__bash_status wait=60 when a long job\'s final output matters; never sleep-poll. ' +
    '[cave: full output … at <path>]: cave__read/cave__grep that path, do not re-run. Unfiltered: cave-tools raw <cmd>. ' +
    'Call cave__invalidate after external edits. ' +
    'String edit: cave__edit with explicit file_path, old_string, new_string (never infer target). ' +
    'Medium+ edits: cave__read line_numbers=true → cave__edit start_line/end_line/content ' +
    '(+ expected_hash or expected_range_checksum); delete:true / insert_before for move.';
  if (mode === 'strict') {
    return base + ' STRICT: built-in edit/write need prior cave__read; range/move need hash or range_checksum. Text reads use cave__read.';
  }
  if (mode === 'enforce') {
    return base + ' ENFORCE: text reads use cave__read, not read. grep/glob/list blocked.';
  }
  return base;
}

function parseModeChange(promptRaw) {
  let prompt = (promptRaw || '').trim();
  const wrapped = /^(["'`])([\s\S]*)\1$/.exec(prompt);
  if (wrapped) prompt = wrapped[2].trim();
  const lower = prompt.toLowerCase();
  if (!lower) return null;

  // Deactivation first so "stop cave-tools" does not trip activation.
  if (/\b(stop|disable|deactivate|turn off)\b.*\bcave[- ]?tools\b/i.test(lower) ||
      /\bcave[- ]?tools\b.*\b(stop|disable|deactivate|turn off)\b/i.test(lower)) {
    return 'off';
  }

  // Expanded /cave-tools command template (opencode substitutes command body
  // before chat.message). Recover level from first line.
  const tpl = /^activate cave-tools mode:[ \t]*(\S*)/i.exec(prompt);
  if (tpl) {
    const arg = (tpl[1] || '').toLowerCase();
    if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
    if (arg === 'status') return null; // status is advisory; leave flag alone
    if (VALID_MODES.includes(arg)) return arg;
    return getDefaultMode();
  }

  // Natural-language activation
  if (/\b(use|enable|activate|turn on|start)\b.*\bcave[- ]?tools\b/i.test(lower) ||
      /\bcave[- ]?tools\b.*\b(use|enable|activate|turn on|start|mode)\b/i.test(lower)) {
    const mode = getDefaultMode();
    return mode === 'off' ? null : mode;
  }

  // Literal slash command
  if (lower.startsWith('/cave-tools')) {
    const parts = lower.split(/\s+/);
    const arg = parts[1] || '';
    if (!arg || arg === 'on') return getDefaultMode();
    if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
    if (arg === 'status') return null;
    if (VALID_MODES.includes(arg)) return arg;
    return null;
  }

  return null;
}

function applyModeChange(mode) {
  if (!mode) return;
  // Persist explicit off so execution guards cannot fall back to enforce.
  safeWriteFlag(flagPath, mode);
}

function handleSessionCreated() {
  const mode = getDefaultMode();
  if (mode === 'off') {
    try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
    return;
  }
  safeWriteFlag(flagPath, mode);
}

function normalizeBuiltin(tool) {
  const t = String(tool || '').toLowerCase();
  if (t === 'read' || t === 'read_file' || t === 'readfile') return 'read';
  if (t === 'grep') return 'grep';
  if (t === 'glob' || t === 'list' || t === 'list_dir' || t === 'listdir') return 'glob';
  if (t === 'edit' || t === 'write' || t === 'apply_patch' || t === 'search_replace' || t === 'multiedit' || t === 'write_file') return 'edit';
  if (t === 'bash' || t === 'shell') return 'bash';
  return t;
}

function extractFilePath(args) {
  if (!args || typeof args !== 'object') return '';
  return String(
    args.filePath ||
    args.file_path ||
    args.path ||
    args.target_file ||
    args.targetFile ||
    args.file ||
    ''
  );
}

function isImagePath(fp) {
  return /\.(png|jpe?g|gif|webp|bmp|ico|pdf|svg)$/i.test(fp || '');
}

function wasReadViaCave(fp) {
  if (!fp) return false;
  const reg = readRegistryPath();
  try {
    const st = lstatSync(reg);
    if (st.isSymbolicLink() || !st.isFile()) return false;
    const body = readFileSync(reg, 'utf8');
    return body.split('\n').some((line) => line.trim() === fp);
  } catch (e) {
    return false;
  }
}

function denyMessage(tool) {
  switch (tool) {
    case 'read':
      return 'Use cave__read instead of read — optimized drop-in (dedup + line budgets).';
    case 'grep':
      return 'Use cave__grep instead of grep — optimized drop-in (ripgrep + line budgets).';
    case 'glob':
      return 'Use cave__find or cave__ls instead of glob/list — optimized drop-in.';
    case 'edit':
      return 'STRICT: cave__read this file first before editing — keeps dedup cache coherent.';
    default:
      return 'Use cave-tools MCP (cave__*) instead of this built-in.';
  }
}

const NATIVE_TO_CAVE = Object.freeze({
  edit: 'cave__edit',
  write: 'cave__write',
  patch: 'cave__apply_patch',
  apply_patch: 'cave__apply_patch',
  read: 'cave__read',
});

const MAX_ERROR_FIELDS = 5;

function hasOwn(obj, key) {
  return Object.prototype.hasOwnProperty.call(obj, key);
}

function isCaveToolName(toolName) {
  const id = String(toolName || '');
  return /^cave__[A-Za-z0-9_]+$/.test(id) ||
    /^cave-tools_cave__[A-Za-z0-9_]+$/.test(id) ||
    /^mcp__cave-tools__cave__[A-Za-z0-9_]+$/.test(id);
}

function caveReplacement(tools, replacement) {
  for (const id of [replacement, 'cave-tools_' + replacement, 'mcp__cave-tools__' + replacement]) {
    if (hasOwn(tools, id) && tools[id] != null) return id;
  }
  return null;
}

function filterNativeTools(event, mode) {
  if (mode !== 'enforce' && mode !== 'strict') return;
  const tools = event && event.tools;
  if (!tools || typeof tools !== 'object' || Array.isArray(tools)) return;
  for (const native of Object.keys(NATIVE_TO_CAVE)) {
    if (!hasOwn(tools, native)) continue;
    if (!caveReplacement(tools, NATIVE_TO_CAVE[native])) continue;
    try { delete tools[native]; } catch (e) {}
  }
}

function listedToolIds(editor) {
  if (!editor || typeof editor.list !== 'function') return new Set();
  let listed = [];
  try { listed = editor.list() || []; } catch (e) { return new Set(); }
  if (!Array.isArray(listed)) return new Set();
  const ids = new Set();
  for (const tool of listed) {
    if (!tool) continue;
    const id = typeof tool === 'string' ? tool : tool.id || tool.name;
    if (id) ids.add(String(id));
  }
  return ids;
}

function replacementListed(ids, replacement) {
  return ids.has(replacement) ||
    ids.has('cave-tools_' + replacement) ||
    ids.has('mcp__cave-tools__' + replacement);
}

// Context-hook deletion only changes that prompt's tool map. Execution uses the
// registry, so replaced natives must also be removed there or the model can
// still call them and hit the execution guard.
function noteGenerationTools(tools) {
  sawContextTools = true;
  const ready = new Set();
  if (tools && typeof tools === 'object' && !Array.isArray(tools)) {
    if (caveReplacement(tools, 'cave__read')) ready.add('read');
    if (caveReplacement(tools, 'cave__grep')) ready.add('grep');
    if (caveReplacement(tools, 'cave__find') || caveReplacement(tools, 'cave__ls')) ready.add('glob');
  }
  generationReady = ready;
}

function hideReplacedNatives(editor, mode) {
  if (mode !== 'enforce' && mode !== 'strict') return;
  if (!editor || typeof editor.remove !== 'function') return;
  const ids = listedToolIds(editor);
  for (const native of Object.keys(NATIVE_TO_CAVE)) {
    if (!replacementListed(ids, NATIVE_TO_CAVE[native])) continue;
    try { editor.remove(native); } catch (e) {}
  }
}

function missingTargetMessage(toolName) {
  const raw = String(toolName || '').toLowerCase();
  if (raw === 'write' || raw === 'write_file') {
    return 'STRICT: write target missing - pass path/file_path explicitly (never inferred) or use cave__write with file_path and content.';
  }
  return 'STRICT: edit target missing - pass path/file_path explicitly (never inferred) or use cave__edit with file_path, old_string, new_string.';
}

const EDIT_INPUT_FIELDS = new Set([
  'root', 'path', 'filePath', 'file_path', 'oldString', 'newString', 'replaceAll',
  'old_string', 'new_string', 'replace_all', 'start_line', 'end_line', 'content',
  'delete', 'insert_before', 'expected_hash', 'expected_range_checksum', 'edits', 'patchText',
]);

function safeFieldPath(raw) {
  const value = String(raw || '').trim();
  if (/^edits\[\d{1,6}\]$/.test(value)) return value;
  const field = value.replace(/^edits\[\d{1,6}\]\./, '');
  return EDIT_INPUT_FIELDS.has(field) ? value : 'argument';
}

function safeIssueReason(raw) {
  const text = String(raw || '').toLowerCase();
  if (/^(?:missing key|required\b)/.test(text)) return 'Missing key';
  const expected = /^expected (string|number|boolean|object|array|null)\b/.exec(text);
  if (expected) return 'Expected ' + expected[1];
  if (/^unrecognized key/.test(text)) return 'Unrecognized key';
  if (/^invalid (option|enum|value)/.test(text)) return 'Invalid option';
  if (/^(?:too (small|big)|out of range)/.test(text)) return 'Out of range';
  return 'Invalid value';
}

function parseValidationIssues(body) {
  const issues = [];
  let total = 0;
  for (const line of String(body || '').split('\n')) {
    const match = /^- (.+?): (.*)$/.exec(line);
    if (!match) continue;
    total += 1;
    if (issues.length >= MAX_ERROR_FIELDS) continue;
    issues.push({ field: safeFieldPath(match[1]), reason: safeIssueReason(match[2]) });
  }
  return { issues, total };
}

function compactInputValidationError(event) {
  if (!event || event.status !== 'error') return;
  const error = event.error;
  if (!error || typeof error !== 'object' || typeof error.message !== 'string') return;
  const tool = String(event.tool || '');
  if (!tool) return;
  const header = 'Invalid arguments for tool "' + tool + '":\n';
  const message = error.message;
  if (!message.startsWith(header)) return;

  const argsMarker = '\n\nArguments provided:\n';
  const footer = '\n\nUpdate the arguments and call the tool again.';
  const argsAt = message.indexOf(argsMarker, header.length);
  if (argsAt < 0) return;
  const footerAt = message.lastIndexOf(footer);
  if (footerAt < argsAt || message.slice(footerAt + footer.length).trim()) return;

  const { issues, total } = parseValidationIssues(message.slice(header.length, argsAt));
  const lines = issues.map((issue) => '- ' + issue.field + ': ' + issue.reason);
  if (total > issues.length) {
    const more = total - issues.length;
    lines.push('- ...and ' + more + ' more ' + (more === 1 ? 'issue' : 'issues'));
  }
  const missingTarget = issues.some((issue) =>
    (issue.field === 'path' || issue.field === 'filePath') && issue.reason === 'Missing key');

  let next;
  if (missingTarget && tool === 'edit') {
    next = 'Supply path explicitly (never inferred), or use cave__edit with file_path, old_string, and new_string.';
  } else if (missingTarget && tool === 'write') {
    next = 'Supply path explicitly (never inferred), or use cave__write with file_path and content.';
  } else {
    next = 'Fix the arguments and call the tool again.';
  }
  const compact = header + (lines.length ? lines.join('\n') : '- argument: Invalid value') + '\n\n' + next;
  if (compact.length >= message.length) return;
  try { error.message = compact; } catch (e) {}
}

function promptText(event) {
  if (!event) return '';
  if (typeof event.prompt === 'string') return event.prompt;
  if (event.prompt && typeof event.prompt.text === 'string') return event.prompt.text;
  if (typeof event.text === 'string') return event.text;
  return '';
}

function pushSystem(event, text) {
  if (!event || !text) return;
  if (Array.isArray(event.system)) {
    const last = event.system[event.system.length - 1];
    if (event.system.length === 0 || typeof last === 'string') {
      event.system.push(text);
      return;
    }
    event.system.push({ type: 'text', text });
    return;
  }
  if (Array.isArray(event.context)) event.context.push(text);
}

function denyBuiltin(toolName, args) {
  const active = readFlag(flagPath) || getDefaultMode();
  if (!active || active === 'off' || active === 'hint') return;

  if (isCaveToolName(toolName)) return;

  const tool = normalizeBuiltin(toolName);
  if (!tool || tool === 'bash') return; // bash stays escape-hatch: never denied (wrapBuiltinBash only reroutes its output)

  if (active === 'enforce' || active === 'strict') {
    if (tool === 'read') {
      if (sawContextTools && !generationReady.has('read')) return;
      const fp = extractFilePath(args);
      if (isImagePath(fp)) return; // rare image passthrough
      throw new Error(denyMessage('read'));
    }
    if (tool === 'grep') {
      if (sawContextTools && !generationReady.has('grep')) return;
      throw new Error(denyMessage('grep'));
    }
    if (tool === 'glob') {
      if (sawContextTools && !generationReady.has('glob')) return;
      throw new Error(denyMessage('glob'));
    }
  }

  if (active === 'strict' && tool === 'edit') {
    // Legacy patch aliases carry their targets inside patchText, not path.
    if (String(toolName || '').toLowerCase() === 'apply_patch') return;
    const fp = extractFilePath(args);
    if (!fp) throw new Error(missingTargetMessage(toolName));
    if (wasReadViaCave(fp)) return;
    throw new Error(denyMessage('edit'));
  }
}

// Rewrite the native bash/shell command to run through `cave-tools bash`, so
// output gets cave's compression while opencode keeps its own shell tool.
// Skip rules are shared with the Claude hook (bashWrapSkipReason).
function wrapBuiltinBash(toolName, args, sessionId) {
  if (normalizeBuiltin(toolName) !== 'bash' || !args || typeof args !== 'object') return;
  const skip = bashWrapSkipReason(args.command, {
    runInBackground: args.run_in_background === true || args.background === true,
    mode: readFlag(flagPath) || getDefaultMode(),
    env: process.env,
  });
  if (skip) return;
  args.command = wrapBashCommand(args.command, sessionId);
}

// Compress native bash/shell output after it ran (default; the command is
// untouched, so permission rules keep matching). Text is opencode's combined
// stdout+stderr; a trailing <bash_metadata> block (V1 timeout/abort notes) is
// kept verbatim, and exit code / metadata live outside the text untouched.
// Returns the new text, or null to keep the original.
// Trailing notes opencode appends and the model needs verbatim: V1's
// <bash_metadata> block, V2's "[showing lines …; full output saved to …]".
const BASH_TRAILERS = [
  /\n*<bash_metadata>[\s\S]*<\/bash_metadata>\s*$/,
  /\n*\[showing lines [^\]\n]*full output saved to [^\]\n]*\]\s*$/,
];

function filterBashText(text, command, sessionId) {
  if (typeof text !== 'string') return null;
  let body = text;
  for (const re of BASH_TRAILERS) {
    const m = re.exec(body);
    if (m) { body = body.slice(0, m.index); break; }
  }
  const meta = text.slice(body.length).replace(/^\n+/, '');
  const mode = readFlag(flagPath) || getDefaultMode();
  if (bashFilterSkipReason(command, body, { mode, env: process.env })) return null;
  const out = runCaveFilter({ command: String(command || ''), stdout: body, stderr: '' }, sessionId, process.env);
  if (!out || out.stdout.length >= body.length) return null;
  return out.stdout.replace(/\s*$/, '') + (meta ? '\n\n' + meta : '');
}

// V2 (opencode2 2.1.4): a completed shell result carries the text twice —
// result.output.output (what the model reads) and result.content, an array
// of text parts (a plain string is accepted too). Both get the same filtered
// text; exit, truncated, status and metadata are left alone. When opencode
// already truncated (full output saved to a file), the preview is filtered
// and its "[showing lines …; full output saved to …]" pointer kept.
// Background launches (input.background, or a still-running result) pass.
function filterV2BashResult(event) {
  if (!event || event.status !== 'completed' || normalizeBuiltin(event.tool || event.name) !== 'bash') return;
  const input = event.input || event.args || {};
  const result = event.result;
  if (!result || input.background === true) return;
  const out = result.output && typeof result.output === 'object' ? result.output : null;
  if (out && out.status === 'running') return;
  const parts = Array.isArray(result.content) ? result.content : null;
  // Only all-text content; anything else (images, files) is left as is.
  if (parts && (parts.length === 0 || !parts.every((p) => p && p.type === 'text' && typeof p.text === 'string'))) return;
  const text = out && typeof out.output === 'string'
    ? out.output
    : parts ? parts.map((p) => p.text).join('\n') : result.content;
  const next = filterBashText(text, input.command, event.sessionID || event.sessionId);
  if (next === null) return;
  const updated = { ...result };
  if (out && typeof out.output === 'string') updated.output = { ...out, output: next };
  if (parts) updated.content = [{ ...parts[0], text: next }];
  else if (typeof result.content === 'string') updated.content = next;
  event.result = updated;
}

function eventType(event) {
  if (!event) return '';
  return String(event.type || (event.event && event.event.type) || '');
}

// V1 has no context tool-list hook; native-tool filtering is V2-only.
function v1HookMap() {
  return {
    event: async ({ event } = {}) => {
      if (event && event.type === 'session.created') handleSessionCreated();
    },

    'chat.message': async (_input, output) => {
      if (!output || !output.parts) return;
      for (const part of output.parts) {
        if (part && part.type === 'text' && part.text) {
          const change = parseModeChange(part.text);
          if (change) applyModeChange(change);
        }
      }
    },

    'experimental.chat.system.transform': async (_input, output) => {
      if (!output || !Array.isArray(output.system)) return;
      const active = readFlag(flagPath);
      if (active && active !== 'off') {
        output.system.push(reinforcementLine(active));
      }
    },

    'experimental.session.compacting': async (_input, output) => {
      if (!output || !Array.isArray(output.context)) return;
      const active = readFlag(flagPath);
      if (active && active !== 'off') {
        output.context.push(
          reinforcementLine(active) +
          '\nCave-tools AGENTS.md rules still apply after compaction. Prefer cave__* tools.'
        );
      }
    },

    'tool.execute.before': async (input, output) => {
      denyBuiltin(input && input.tool, output && output.args);
      wrapBuiltinBash(input && input.tool, output && output.args, input && input.sessionID);
    },

    'tool.execute.after': async (input, output) => {
      if (!input || normalizeBuiltin(input.tool) !== 'bash' || !output) return;
      const command = input.args && input.args.command;
      const next = filterBashText(output.output, command, input.sessionID);
      if (next !== null) output.output = next;
    },
  };
}

export const CaveToolsPlugin = async (_ctx) => {
  // Factory-time flag write covers one-shot `opencode run` race where
  // session.created may fire before event dispatch is wired.
  handleSessionCreated();
  return v1HookMap();
};

async function setup(ctx) {
  handleSessionCreated();

  const registrations = [];
  const remember = (reg) => {
    if (reg && typeof reg.dispose === 'function') registrations.push(reg);
    return reg;
  };
  reloadToolRegistry = ctx && ctx.tool && typeof ctx.tool.reload === 'function'
    ? () => ctx.tool.reload()
    : null;
  if (ctx && ctx.tool && typeof ctx.tool.transform === 'function') {
    remember(await ctx.tool.transform((editor) => {
      hideReplacedNatives(editor, readFlag(flagPath) || getDefaultMode());
    }));
  }

  if (ctx && ctx.session && typeof ctx.session.hook === 'function') {
    remember(await ctx.session.hook('prompt', (event) => {
      const change = parseModeChange(promptText(event));
      if (change) applyModeChange(change);
    }));

    remember(await ctx.session.hook('context', async (event) => {
      const active = readFlag(flagPath);
      if (!active || active === 'off') return;
      if ((active === 'enforce' || active === 'strict') && reloadToolRegistry) {
        try { await reloadToolRegistry(); } catch (e) {}
      }
      noteGenerationTools(event && event.tools);
      filterNativeTools(event, active);
      pushSystem(event, reinforcementLine(active));
    }));

    remember(await ctx.session.hook('compaction', (event) => {
      const active = readFlag(flagPath);
      if (active && active !== 'off') {
        pushSystem(
          event,
          reinforcementLine(active) +
            '\nCave-tools AGENTS.md rules still apply after compaction. Prefer cave__* tools.'
        );
      }
    }));
  }

  if (ctx && ctx.tool && typeof ctx.tool.hook === 'function') {
    remember(await ctx.tool.hook('execute.before', (event) => {
      const toolName = event && (event.tool || event.name);
      const args = (event && (event.input || event.args)) || {};
      denyBuiltin(toolName, args);
      wrapBuiltinBash(toolName, args, event && (event.sessionID || event.sessionId));
    }));

    remember(await ctx.tool.hook('execute.after', (event) => {
      const active = readFlag(flagPath);
      if (active && active !== 'off') compactInputValidationError(event);
      filterV2BashResult(event);
    }));
  }

  let abort = null;
  if (ctx && ctx.event && typeof ctx.event.subscribe === 'function') {
    const controller = new AbortController();
    abort = () => controller.abort();
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          const type = eventType(event);
          if (type === 'session.created' || type.endsWith('.session.created')) {
            handleSessionCreated();
          }
        }
      } catch (e) {
        if (!e || e.name === 'AbortError') return;
      }
    })();
  }

  return () => {
    if (abort) {
      try { abort(); } catch (e) {}
    }
    for (const reg of registrations) {
      try { void reg.dispose(); } catch (e) {}
    }
  };
}

// V2 reads id + setup(); V1 1.18.29+ reads server(). Do not import
// @opencode/plugin — this file is copied to ~/.config/opencode/plugins and
// must load without extra node_modules.
export default {
  id: 'cave-tools',
  setup,
  server: CaveToolsPlugin,
};
