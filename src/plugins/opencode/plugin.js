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
const { getDefaultMode, safeWriteFlag, readFlag, VALID_MODES } = config;

function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  }
  return path.join(os.homedir(), '.config', 'opencode');
}

const flagPath = path.join(opencodeConfigDir(), '.cave-tools-active');

function readRegistryPath() {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  return path.join(claudeDir, 'cave-tools', 'read-registry.txt');
}

function reinforcementLine(mode) {
  const base = 'CAVE-TOOLS MODE ACTIVE (' + mode + '). ' +
    'Prefer cave__read / cave__bash / cave__grep / cave__find / cave__ls over built-ins. ' +
    'Call cave__invalidate after external edits; never run `rtk <cmd>` inside cave__bash. ' +
    'Commands >2-3min: cave__bash_start + poll cave__bash_status wait=60; never sleep-poll. ' +
    'Medium+ edits: cave__read line_numbers=true → cave__edit start_line/end_line/content ' +
    '(+ expected_hash or expected_range_checksum); delete:true / insert_before for move.';
  if (mode === 'strict') {
    return base + ' STRICT: built-in edit/write need prior cave__read; range/move need hash or range_checksum.';
  }
  if (mode === 'enforce') {
    return base + ' ENFORCE: built-in read/grep/glob/list blocked — use cave__* equivalents.';
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
  if (mode === 'off') {
    try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
    return;
  }
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

  if (String(toolName || '').includes('cave__')) return;

  const tool = normalizeBuiltin(toolName);
  if (!tool || tool === 'bash') return; // bash stays escape-hatch

  if (active === 'enforce' || active === 'strict') {
    if (tool === 'read') {
      const fp = extractFilePath(args);
      if (isImagePath(fp)) return; // rare image passthrough
      throw new Error(denyMessage('read'));
    }
    if (tool === 'grep') throw new Error(denyMessage('grep'));
    if (tool === 'glob') throw new Error(denyMessage('glob'));
  }

  if (active === 'strict' && tool === 'edit') {
    const fp = extractFilePath(args);
    if (!fp) return;
    if (wasReadViaCave(fp)) return;
    throw new Error(denyMessage('edit'));
  }
}

function eventType(event) {
  if (!event) return '';
  return String(event.type || (event.event && event.event.type) || '');
}

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

  if (ctx && ctx.session && typeof ctx.session.hook === 'function') {
    remember(await ctx.session.hook('prompt', (event) => {
      const change = parseModeChange(promptText(event));
      if (change) applyModeChange(change);
    }));

    remember(await ctx.session.hook('context', (event) => {
      const active = readFlag(flagPath);
      if (active && active !== 'off') pushSystem(event, reinforcementLine(active));
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
