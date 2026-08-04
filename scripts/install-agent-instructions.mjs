#!/usr/bin/env node

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';

const HOME = os.homedir();
const REPO_ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..');
const CAVE_TOOLS_CLI = path.join(REPO_ROOT, 'dist', 'cli.js');
const GLOBAL_RULES_BACKUP_DIR = path.join(REPO_ROOT, 'scripts', 'global-rules-backup');
const CLAUDE_BUNDLE_DIR = path.join(REPO_ROOT, 'claude');

const MARKER_BEGIN = '<!-- cave-tools-begin -->';
const MARKER_END = '<!-- cave-tools-end -->';
const DISCIPLINE_MARKER_BEGIN = '<!-- cave-discipline-begin -->';
const DISCIPLINE_MARKER_END = '<!-- cave-discipline-end -->';
const DRY_RUN = process.argv.includes('--dry-run');
const VERBOSE = process.argv.includes('--verbose');
const WITH_EXTRA_RULES = process.argv.includes('--with-extra-rules');
const touchedPaths = new Map();

// Long-form rules block for agents without a skill/plugin reinforcement path.
// Claude uses the shorter pointer below (SKILL.md + SessionStart are SoT).
// OpenCode uses OPENCODE_CAVE_TOOLS_BLOCK (plugin + this AGENTS.md base).
const CAVE_TOOLS_BLOCK = [
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'When caveman mode loads, also use Cave Tools as the preferred optimized drop-in replacements for built-in file/shell/web tools (same results, fewer tokens).',
  '',
  '- Use `cave__read` instead of the built-in read tool for file reads. Optimized drop-in replacement (read dedup + line budgets).',
  '- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell commands or built-in search tools when available.',
  '- Use `cave__bash` instead of the built-in shell tool for commands. Optimized drop-in replacement: tries RTK rewriting when available, then structured JSON/XML extraction and line budgets.',
  '- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`; pass the raw command.',
  '- Use `cave__edit` / `cave__write` for single-file edits and `cave__apply_patch` for multi-file add/update/delete/move patches.',
  '- Use `cave__websearch` for current web information when a local web search tool is needed; results are redacted, archived if large, and budget-compressed.',
  '- Use `cave__webfetch` to fetch a specific URL and return markdown/text/html (or a base64 image block); output is redacted, archived if large, and budget-compressed.',
  '- After editing a file outside Cave Tools, call `cave__invalidate` with changed path(s) to refresh the read dedup cache.',
  '- Use `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.',
  '- Use `cave__status` to inspect RTK availability, cache state, and savings.',
  MARKER_END,
  '',
].join('\n');

// OpenCode AGENTS.md block — always-on base. Dynamic mode + per-turn reinforce
// + built-in blocking come from the native plugin (plugins/cave-tools/plugin.js).
const OPENCODE_CAVE_TOOLS_BLOCK = [
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'Native opencode plugin (`./plugins/cave-tools/plugin.js`) keeps cave-tools active every turn:',
  '- `session.created` writes mode flag (`~/.config/opencode/.cave-tools-active`)',
  '- `experimental.chat.system.transform` injects per-turn reinforcement (Claude UserPromptSubmit twin)',
  '- `tool.execute.before` blocks built-in read/grep/glob/list in `enforce`/`strict` (points to `cave__*`)',
  '- `experimental.session.compacting` keeps rules across compaction',
  '- Switch: `/cave-tools off|hint|enforce|strict` or natural language ("use cave-tools" / "stop cave-tools")',
  '',
  'Prefer cave__* over built-ins (same results, fewer tokens):',
  '',
  '- Use `cave__read` instead of the built-in read tool for file reads. Optimized drop-in replacement (read dedup + line budgets).',
  '- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell commands or built-in search tools when available.',
  '- Use `cave__bash` instead of the built-in shell tool for commands. Optimized drop-in replacement: tries RTK rewriting when available, then structured JSON/XML extraction and line budgets.',
  '- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`; pass the raw command.',
  '- Use `cave__edit` / `cave__write` for single-file edits and `cave__apply_patch` for multi-file add/update/delete/move patches.',
  '- Use `cave__websearch` for current web information when a local web search tool is needed; results are redacted, archived if large, and budget-compressed.',
  '- Use `cave__webfetch` to fetch a specific URL and return markdown/text/html (or a base64 image block); output is redacted, archived if large, and budget-compressed.',
  '- After editing a file outside Cave Tools, call `cave__invalidate` with changed path(s) to refresh the read dedup cache.',
  '- Use `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.',
  '- Use `cave__status` to inspect RTK availability, cache state, and savings.',
  MARKER_END,
  '',
].join('\n');

const OPENCODE_PLUGIN_REL = './plugins/cave-tools/plugin.js';
const OPENCODE_PLUGIN_SRC = path.join(REPO_ROOT, 'src', 'plugins', 'opencode');
const OPENCODE_CONFIG_SRC = path.join(REPO_ROOT, 'claude', 'hooks', 'cave-tools-config.js');
const OPENCODE_SKILL_SRC = path.join(REPO_ROOT, 'claude', 'skills', 'cave-tools');

// Short pointer block used in Claude installs where the /cave-tools skill +
// SessionStart hook are the source of truth — avoids token-wasting duplication.
const CAVE_TOOLS_POINTER_BLOCK = [
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'See the `/cave-tools` skill for token-saving tool rules (cave__read, cave__bash, cave__grep, cave__apply_patch, cave__websearch, cave__webfetch, etc.).',
  'The SessionStart hook auto-injects the full ruleset every session.',
  MARKER_END,
  '',
].join('\n');

const ZCODE_CAVE_TOOLS_SKILL = [
  '---',
  'name: cave-tools',
  'description: Token-saving file/shell/web tool layer. Use when file, shell, search, edit, or web operations are needed.',
  '---',
  '',
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'Prefer Cave Tools MCP tools over built-in file/shell/search tools when they are available. They are optimized drop-in replacements that return the same results for fewer tokens.',
  '',
  '## Rules',
  '',
  '- Use `cave__read` instead of built-in read tools for file reads.',
  '- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell search/listing commands.',
  '- Use `cave__bash` instead of built-in shell execution for foreground commands.',
  '- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`; pass the raw command.',
  '- Use `cave__edit` / `cave__write` for single-file changes and `cave__apply_patch` for multi-file add/update/delete/move changes.',
  '- Use `cave__websearch` for current web information when a local web search tool is needed.',
  '- Use `cave__webfetch` to fetch a specific URL and return markdown/text/html (or a base64 image block).',
  '- After editing a file outside Cave Tools, call `cave__invalidate` with changed paths so later reads are fresh.',
  '- Use `cave__compress` for large pasted or tool-produced text, and `cave__status` to inspect savings and RTK availability.',
  '',
  '## Fallbacks',
  '',
  'Use built-in tools only when Cave Tools is unavailable or when a task needs background processes, streaming monitors, interactive stdin, or host-specific behavior Cave Tools cannot provide.',
  MARKER_END,
  '',
].join('\n');

// Opt-in discipline block (--with-extra-rules). Broader than the cave-tools
// block: bundles RTK-fallback, codebase-memory-mcp, MCP-init-wait, and
// subagent propagation rules. Distinct markers so it coexists with the
// cave-tools block without collision. Off by default — personal extras.
//
// Rules body lives in rules/cave-discipline.md (single source of truth, like
// caveman's src/rules/caveman-activate.md). The installer reads it at runtime
// and wraps it with the marker fence. Edit the .md, not this constant.
const DISCIPLINE_RULES_PATH = path.join(REPO_ROOT, 'rules', 'cave-discipline.md');
// Kiro steering is file-per-concern with YAML frontmatter (inclusion: always).
// Generic CAVE_TOOLS_BLOCK is Claude/opencode-shaped — wrong for Kiro tool names.
const KIRO_CAVE_TOOLS_PATH = path.join(REPO_ROOT, 'rules', 'cave-tools-kiro.md');
const KIRO_COMPRESSION_PATH = path.join(REPO_ROOT, 'rules', 'cave-tools-kiro-compression.md');
const KIRO_EDIT_SAFETY_PATH = path.join(REPO_ROOT, 'rules', 'cave-tools-kiro-edit-safety.md');
const KIRO_REDIRECT_HOOK_SRC = path.join(REPO_ROOT, 'scripts', 'kiro', 'cave-tools-redirect.sh');

function readDisciplineBlock() {
  const body = fs.readFileSync(DISCIPLINE_RULES_PATH, 'utf8').trimEnd() + '\n';
  return `${DISCIPLINE_MARKER_BEGIN}\n${body}${DISCIPLINE_MARKER_END}\n`;
}

function readKiroSteeringFile(bodyPath, beginMarker, endMarker) {
  const body = fs.readFileSync(bodyPath, 'utf8').trimEnd();
  return [
    '---',
    'inclusion: always',
    '---',
    '',
    beginMarker,
    body,
    endMarker,
    '',
  ].join('\n');
}

function log(message) {
  process.stdout.write(`${message}\n`);
}

function pad(value, width) {
  const text = String(value);
  return text.length >= width ? text : text + ' '.repeat(width - text.length);
}

function section(title) {
  log('');
  log(`== ${title} ==`);
}

function trackPath(filePath, action) {
  if (!VERBOSE) return;
  const actions = touchedPaths.get(filePath) || new Set();
  actions.add(action);
  touchedPaths.set(filePath, actions);
}

function printTouchedPaths() {
  if (!VERBOSE) return;
  section(DRY_RUN ? 'Planned file changes' : 'Modified files');
  if (touchedPaths.size === 0) {
    log('  (none)');
    return;
  }
  log(`${pad('Action', 14)} Path`);
  log(`${'-'.repeat(14)} ${'-'.repeat(60)}`);
  for (const [filePath, actions] of touchedPaths) {
    log(`${pad([...actions].join(','), 14)} ${filePath}`);
  }
}

function ensureDir(dir) {
  if (DRY_RUN) return;
  fs.mkdirSync(dir, { recursive: true });
}

function backupOnce(filePath) {
  if (DRY_RUN) return;
  if (!fs.existsSync(filePath)) return;
  const bakPath = `${filePath}.bak`;
  if (!fs.existsSync(bakPath)) fs.copyFileSync(filePath, bakPath);
}

function backupGlobalRule(filePath, backupName) {
  if (!fs.existsSync(filePath)) return;
  ensureDir(GLOBAL_RULES_BACKUP_DIR);
  const dest = path.join(GLOBAL_RULES_BACKUP_DIR, backupName);
  if (fs.existsSync(dest)) return;
  if (DRY_RUN) {
    trackPath(dest, 'backup');
    log(`dry-run: would back up ${filePath} → ${dest}`);
    return;
  }
  fs.copyFileSync(filePath, dest);
  trackPath(dest, 'backup');
  log(`backed up: ${dest}`);
}

function readJson(filePath) {
  if (!fs.existsSync(filePath)) return {};
  const raw = fs.readFileSync(filePath, 'utf8');
  if (raw.trim() === '') return {};
  try {
    return JSON.parse(raw);
  } catch (error) {
    throw new Error(`${filePath} is not valid JSON: ${error.message}`);
  }
}

function writeJson(filePath, value) {
  ensureDir(path.dirname(filePath));
  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, 'write');
    log(`dry-run: would write ${filePath}`);
    return;
  }
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o644 });
  trackPath(filePath, 'write');
}

function writeText(filePath, value) {
  ensureDir(path.dirname(filePath));
  const exists = fs.existsSync(filePath);
  const current = exists ? fs.readFileSync(filePath, 'utf8') : '';
  if (current === value) {
    log(`unchanged: ${filePath}`);
    return;
  }
  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, exists ? 'update' : 'create');
    log(`dry-run: would ${exists ? 'update' : 'create'} ${filePath}`);
    return;
  }
  fs.writeFileSync(filePath, value, { mode: 0o644 });
  trackPath(filePath, exists ? 'update' : 'create');
  log(`${exists ? 'updated' : 'created'}: ${filePath}`);
}

function removeFencedBlock(content) {
  return removeFencedBlockGeneric(content, MARKER_BEGIN, MARKER_END);
}

// Generic marker-fence stripper. Used by the discipline-block path so the
// two marker pairs (cave-tools / cave-discipline) don't collide.
function removeFencedBlockGeneric(content, beginMarker, endMarker) {
  let next = content;
  while (true) {
    const begin = next.indexOf(beginMarker);
    const end = next.indexOf(endMarker);
    if (begin === -1 || end === -1 || end <= begin) return next;
    next = next.slice(0, begin).trimEnd() + '\n\n' + next.slice(end + endMarker.length).trimStart();
  }
}

function hasCaveToolsGuidance(content) {
  // Strip the discipline block too before checking — the discipline rules
  // reference `cave__read`/`cave__bash` and would false-positive this check,
  // causing the cave-tools block to be dedupe-stripped (issue: blocks collide).
  const withoutDiscipline = removeFencedBlockGeneric(content, DISCIPLINE_MARKER_BEGIN, DISCIPLINE_MARKER_END);
  return /#\s*Cave Tools MCP/i.test(withoutDiscipline) && /`cave__read`/.test(withoutDiscipline) && /`cave__bash`/.test(withoutDiscipline);
}

function upsertFencedBlock(filePath, block, options = {}) {
  ensureDir(path.dirname(filePath));
  const exists = fs.existsSync(filePath);
  const current = exists ? fs.readFileSync(filePath, 'utf8') : '';
  const withoutManagedBlock = removeFencedBlock(current);

  if (options.skipIfExistingGuidance && hasCaveToolsGuidance(withoutManagedBlock)) {
    const next = withoutManagedBlock.trimEnd() + '\n';
    if (next !== current) {
      backupOnce(filePath);
      if (DRY_RUN) {
        trackPath(filePath, 'dedupe');
        log(`dry-run: would dedupe ${filePath}`);
        return;
      }
      fs.writeFileSync(filePath, next, { mode: 0o644 });
      trackPath(filePath, 'dedupe');
      log(`deduped: ${filePath}`);
    } else {
      log(`unchanged: ${filePath}`);
    }
    return;
  }

  const source = withoutManagedBlock;
  const sep = source.trimEnd() ? '\n\n' : '';
  const next = `${source.trimEnd()}${sep}${block}`;

  if (next === current) {
    log(`unchanged: ${filePath}`);
    return;
  }

  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, exists ? 'update' : 'create');
    log(`dry-run: would ${exists ? 'update' : 'create'} ${filePath}`);
    return;
  }
  fs.writeFileSync(filePath, next, { mode: 0o644 });
  trackPath(filePath, exists ? 'update' : 'create');
  log(`${exists ? 'updated' : 'created'}: ${filePath}`);
}

// Upsert the discipline block (--with-extra-rules) into a rules file. Uses
// the generic fenced-block stripper so it never touches the cave-tools block.
function upsertDisciplineBlock(filePath) {
  if (!WITH_EXTRA_RULES) return;
  ensureDir(path.dirname(filePath));
  const block = readDisciplineBlock();
  const exists = fs.existsSync(filePath);
  const current = exists ? fs.readFileSync(filePath, 'utf8') : '';
  const withoutDiscipline = removeFencedBlockGeneric(current, DISCIPLINE_MARKER_BEGIN, DISCIPLINE_MARKER_END);
  const sep = withoutDiscipline.trimEnd() ? '\n\n' : '';
  const next = `${withoutDiscipline.trimEnd()}${sep}${block}`;
  if (next === current) {
    log(`unchanged: ${filePath}`);
    return;
  }
  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, exists ? 'update' : 'create');
    log(`dry-run: would ${exists ? 'update' : 'create'} ${filePath} (discipline)`);
    return;
  }
  fs.writeFileSync(filePath, next, { mode: 0o644 });
  trackPath(filePath, exists ? 'update' : 'create');
  log(`${exists ? 'updated' : 'created'}: ${filePath} (discipline)`);
}

function installClaudeMcp() {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(HOME, '.claude');
  const settingsPath = path.join(claudeDir, 'settings.json');
  backupGlobalRule(path.join(claudeDir, 'CLAUDE.md'), 'CLAUDE.md');
  backupGlobalRule(path.join(claudeDir, 'skills', 'caveman', 'SKILL.md'), 'claude-caveman-SKILL.md');

  const settings = readJson(settingsPath);

  if (!settings.mcpServers || typeof settings.mcpServers !== 'object' || Array.isArray(settings.mcpServers)) {
    settings.mcpServers = {};
  }

  settings.mcpServers['cave-tools'] = {
    command: 'node',
    args: [CAVE_TOOLS_CLI, 'mcp'],
  };

  writeJson(settingsPath, settings);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${settingsPath} mcpServers.cave-tools`);

  // Source of truth lives in claude/skills/cave-tools/SKILL.md. Write only a
  // short pointer to avoid token-wasting duplication.
  upsertFencedBlock(path.join(claudeDir, 'CLAUDE.md'), CAVE_TOOLS_POINTER_BLOCK);
  upsertFencedBlock(path.join(claudeDir, 'skills', 'caveman', 'SKILL.md'), CAVE_TOOLS_POINTER_BLOCK);
  upsertDisciplineBlock(path.join(claudeDir, 'CLAUDE.md'));
}

// Codex CLI install: MCP server in config.toml + rules block in AGENTS.md +
// hooks.json SessionStart echo (mirrors caveman's .codex/hooks.json pattern).
// Codex reads AGENTS.md each session; the hook is auto-activation parity with
// Claude Code's SessionStart. Idempotent via marker checks.
const CODEX_HOOK_MARKER = 'CAVE-TOOLS ACTIVE';

function codexConfigDir() {
  return path.join(HOME, '.codex');
}

function upsertTomlSection(content, header, bodyLines) {
  // Surgical TOML section upsert. Not a full parser — finds `[header]` and
  // replaces its body until the next section header (or EOF), else appends.
  const headerRe = new RegExp(`^\\[\\s*${header.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\\\$&')}\\s*\\]\\s*$`, 'm');
  const sectionStart = content.search(headerRe);
  if (sectionStart === -1) {
    // Append new section.
    const block = `\n[${header}]\n${bodyLines.join('\n')}\n`;
    const next = content.endsWith('\n') || content === '' ? content + block : content + '\n' + block;
    return next;
  }
  // Find the line index where the header starts.
  const lineStart = content.lastIndexOf('\n', sectionStart) + 1;
  // Find the next section header after this one (a line starting with '[').
  const after = content.slice(sectionStart);
  const nextSectionMatch = after.slice(after.indexOf('\n') + 1).match(/\n\[[^\]]+\]/);
  const nextSectionRel = nextSectionMatch ? nextSectionMatch.index + 1 : -1;
  const sectionEnd = nextSectionRel === -1 ? content.length : sectionStart + after.indexOf('\n') + 1 + nextSectionRel;
  const before = content.slice(0, lineStart);
  const afterSection = content.slice(sectionEnd);
  const block = `[${header}]\n${bodyLines.join('\n')}\n`;
  return `${before}${block}${before.endsWith('\n') || before === '' ? '' : '\n'}${afterSection}`;
}

function installCodex() {
  const codexDir = codexConfigDir();
  const configPath = path.join(codexDir, 'config.toml');
  const agentsMd = path.join(codexDir, 'AGENTS.md');
  const hooksPath = path.join(codexDir, 'hooks.json');

  if (!fs.existsSync(codexDir)) {
    log(`skip (not installed): Codex CLI → ${codexDir}`);
    return;
  }

  // 1. MCP server in config.toml — upsert [mcp_servers.cave-tools] section.
  const currentConfig = fs.existsSync(configPath) ? fs.readFileSync(configPath, 'utf8') : '';
  const configExists = fs.existsSync(configPath);
  if (/\[\s*mcp_servers\.cave-tools\s*\]/.test(currentConfig)) {
    log(`unchanged: ${configPath} [mcp_servers.cave-tools] already present`);
  } else {
    const mcpBody = [
      'command = "node"',
      `args = ["${CAVE_TOOLS_CLI}", "mcp"]`,
    ];
    const nextConfig = upsertTomlSection(currentConfig, 'mcp_servers.cave-tools', mcpBody);
    backupOnce(configPath);
    if (DRY_RUN) {
      trackPath(configPath, 'update');
      log(`dry-run: would upsert [mcp_servers.cave-tools] into ${configPath}`);
    } else {
      fs.writeFileSync(configPath, nextConfig, { mode: 0o644 });
      trackPath(configPath, 'update');
      log(`updated: ${configPath} [mcp_servers.cave-tools]`);
    }
  }

  // 2. Rules block in AGENTS.md.
  upsertFencedBlock(agentsMd, CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertDisciplineBlock(agentsMd);

  // 3. Hooks enable + SessionStart echo. Idempotent via marker check.
  //    Flip [features] hooks = false → true.
  if (fs.existsSync(configPath)) {
    const cfg = fs.readFileSync(configPath, 'utf8');
    if (/\[\s*features\s*\]/.test(cfg) && /^\s*hooks\s*=\s*false\s*$/m.test(cfg)) {
      const nextCfg = cfg.replace(/^(\s*)hooks\s*=\s*false\s*$/m, '$1hooks = true');
      backupOnce(configPath);
      if (DRY_RUN) {
        trackPath(configPath, 'update');
        log(`dry-run: would flip hooks = false → true in ${configPath}`);
      } else {
        fs.writeFileSync(configPath, nextCfg, { mode: 0o644 });
        trackPath(configPath, 'update');
        log(`updated: ${configPath} [features] hooks = true`);
      }
    } else if (!/\[\s*features\s*\]/.test(cfg)) {
      // No [features] section — append one.
      const nextCfg = cfg + (cfg.endsWith('\n') ? '' : '\n') + '\n[features]\nhooks = true\n';
      backupOnce(configPath);
      if (DRY_RUN) {
        trackPath(configPath, 'update');
        log(`dry-run: would append [features] hooks = true to ${configPath}`);
      } else {
        fs.writeFileSync(configPath, nextCfg, { mode: 0o644 });
        trackPath(configPath, 'update');
        log(`updated: ${configPath} appended [features] hooks = true`);
      }
    } else {
      log(`unchanged: ${configPath} hooks already enabled (or feature section present)`);
    }
  }

  //    Write hooks.json with a SessionStart echo (only if ours missing).
  //    Codex's hooks.json schema requires events nested under a "hooks" wrapper:
  //    { "hooks": { "SessionStart": [...] } }. A root-level SessionStart is
  //    rejected ("unknown field `SessionStart`, expected `description` or `hooks`").
  const hooksBody = JSON.stringify({
    hooks: {
      SessionStart: [{
        matcher: 'startup|resume',
        hooks: [{
          type: 'command',
          command: `echo '${CODEX_HOOK_MARKER}. Prefer cave__read/cave__bash/cave__grep/cave__find/cave__ls/cave__write/cave__edit/cave__apply_patch/cave__websearch/cave__webfetch over built-ins. Do not double-wrap rtk inside cave__bash.'`,
          timeout: 5,
          statusMessage: 'Loading cave-tools rules...',
        }],
      }],
    },
  }, null, 2) + '\n';

  if (fs.existsSync(hooksPath)) {
    const existing = fs.readFileSync(hooksPath, 'utf8');
    if (existing.includes(CODEX_HOOK_MARKER)) {
      log(`unchanged: ${hooksPath} already has cave-tools SessionStart hook`);
    } else {
      // Append our hook to the existing hooks.json. Merge into hooks.SessionStart
      // (creating the hooks wrapper if the file lacks it). Best-effort parse;
      // if it fails, back up + overwrite.
      try {
        const parsed = JSON.parse(existing);
        if (!parsed.hooks || typeof parsed.hooks !== 'object' || Array.isArray(parsed.hooks)) parsed.hooks = {};
        if (!Array.isArray(parsed.hooks.SessionStart)) parsed.hooks.SessionStart = [];
        const ours = JSON.parse(hooksBody).hooks.SessionStart[0];
        const hasOurs = parsed.hooks.SessionStart.some((g) =>
          Array.isArray(g.hooks) && g.hooks.some((h) =>
            typeof h.command === 'string' && h.command.includes(CODEX_HOOK_MARKER)));
        if (!hasOurs) parsed.hooks.SessionStart.push(ours);
        const merged = JSON.stringify(parsed, null, 2) + '\n';
        backupOnce(hooksPath);
        if (DRY_RUN) {
          trackPath(hooksPath, 'update');
          log(`dry-run: would merge cave-tools hook into ${hooksPath}`);
        } else {
          fs.writeFileSync(hooksPath, merged, { mode: 0o644 });
          trackPath(hooksPath, 'update');
          log(`updated: ${hooksPath} merged cave-tools SessionStart hook`);
        }
      } catch (_) {
        backupOnce(hooksPath);
        if (DRY_RUN) {
          trackPath(hooksPath, 'update');
          log(`dry-run: would overwrite ${hooksPath} (existing parse failed)`);
        } else {
          fs.writeFileSync(hooksPath, hooksBody, { mode: 0o644 });
          trackPath(hooksPath, 'update');
          log(`updated: ${hooksPath} (overwrote unparseable existing)`);
        }
      }
    }
  } else {
    if (DRY_RUN) {
      trackPath(hooksPath, 'create');
      log(`dry-run: would create ${hooksPath}`);
    } else {
      ensureDir(path.dirname(hooksPath));
      fs.writeFileSync(hooksPath, hooksBody, { mode: 0o644 });
      trackPath(hooksPath, 'create');
      log(`created: ${hooksPath}`);
    }
  }
}

function opencodeConfigDir() {
  // opencode uses ~/.config/opencode on every platform (incl. Windows via
  // os.homedir()), NOT %APPDATA% — same as caveman install.
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  return path.join(HOME, '.config', 'opencode');
}

function copyDirRecursive(src, dest) {
  ensureDir(dest);
  if (DRY_RUN) return;
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const s = path.join(src, entry.name);
    const d = path.join(dest, entry.name);
    if (entry.isDirectory()) copyDirRecursive(s, d);
    else if (entry.isFile()) fs.copyFileSync(s, d);
  }
}

function copyFileIfNeeded(src, dest, label) {
  if (!fs.existsSync(src)) {
    log(`skip (missing source): ${src}`);
    return;
  }
  if (DRY_RUN) {
    trackPath(dest, 'write');
    log(`dry-run: would install ${label || dest}`);
    return;
  }
  ensureDir(path.dirname(dest));
  backupOnce(dest);
  fs.copyFileSync(src, dest);
  trackPath(dest, 'write');
  log(`installed: ${dest}`);
}

function zcodeConfigDir() {
  return path.join(HOME, '.zcode');
}

function grokConfigDir() {
  return process.env.GROK_HOME && process.env.GROK_HOME.trim()
    ? process.env.GROK_HOME.trim()
    : path.join(HOME, '.grok');
}

function genericAgentsMcpPath() {
  return path.join(HOME, '.agents', 'mcp.json');
}

function findExecutable(name) {
  const pathEnv = process.env.PATH || '';
  const exts = process.platform === 'win32'
    ? (process.env.PATHEXT || '.EXE;.CMD;.BAT;.COM').split(';')
    : [''];
  for (const dir of pathEnv.split(path.delimiter)) {
    if (!dir) continue;
    for (const ext of exts) {
      const candidate = path.join(dir, process.platform === 'win32' && ext ? `${name}${ext.toLowerCase()}` : name);
      try {
        fs.accessSync(candidate, fs.constants.X_OK);
        return candidate;
      } catch (_) {
        // Try next PATH entry.
      }
    }
  }
  return null;
}

function resolveCaveToolsMcpCommand() {
  if (findExecutable('cave-tools')) return { command: 'cave-tools', args: ['mcp'] };
  if (fs.existsSync(CAVE_TOOLS_CLI)) return { command: process.execPath, args: [CAVE_TOOLS_CLI, 'mcp'] };
  throw new Error('cannot configure Grok MCP: neither `cave-tools` is on PATH nor dist/cli.js exists. Run `pnpm run build` or install cave-tools first.');
}

function tomlString(value) {
  return JSON.stringify(value);
}

function tomlArray(values) {
  return `[${values.map(tomlString).join(', ')}]`;
}

function installOpencodeMcp() {
  // Native opencode install (mirrors caveman):
  //   1. MCP server registration
  //   2. Plugin (session flag + per-turn reinforce + tool.execute.before)
  //   3. Skill + slash command
  //   4. AGENTS.md always-on ruleset
  //   5. Inject into agent defs (subagents may not inherit AGENTS.md)
  const configDir = opencodeConfigDir();
  const configPath = path.join(configDir, 'opencode.json');
  const pluginDir = path.join(configDir, 'plugins', 'cave-tools');
  const commandsDir = path.join(configDir, 'command');
  // opencode discovers commands from both `command/` and `commands/` across
  // versions — write the common `command/` path (global config) and also
  // `commands/` when present so either layout picks it up.
  const commandsDirAlt = path.join(configDir, 'commands');
  const skillsDir = path.join(configDir, 'skills', 'cave-tools');
  const agentsMd = path.join(configDir, 'AGENTS.md');

  backupGlobalRule(agentsMd, 'opencode-AGENTS.md');

  // 1–2. MCP + plugin entry in opencode.json
  const config = readJson(configPath);
  if (!config.$schema) config.$schema = 'https://opencode.ai/config.json';
  if (!config.mcp || typeof config.mcp !== 'object' || Array.isArray(config.mcp)) {
    config.mcp = {};
  }
  config.mcp['cave-tools'] = {
    type: 'local',
    command: ['node', CAVE_TOOLS_CLI, 'mcp'],
    enabled: true,
  };
  if (!Array.isArray(config.plugin)) config.plugin = [];
  if (!config.plugin.includes(OPENCODE_PLUGIN_REL)) {
    config.plugin.push(OPENCODE_PLUGIN_REL);
  }
  writeJson(configPath, config);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${configPath} mcp.cave-tools + plugin ${OPENCODE_PLUGIN_REL}`);

  // 3. Plugin payload
  ensureDir(pluginDir);
  copyFileIfNeeded(
    path.join(OPENCODE_PLUGIN_SRC, 'plugin.js'),
    path.join(pluginDir, 'plugin.js'),
    'opencode plugin.js',
  );
  copyFileIfNeeded(
    path.join(OPENCODE_PLUGIN_SRC, 'package.json'),
    path.join(pluginDir, 'package.json'),
    'opencode plugin package.json',
  );
  // Renamed .cjs because plugin dir is "type": "module".
  copyFileIfNeeded(
    OPENCODE_CONFIG_SRC,
    path.join(pluginDir, 'cave-tools-config.cjs'),
    'opencode cave-tools-config.cjs',
  );

  // 4. Slash command
  copyFileIfNeeded(
    path.join(OPENCODE_PLUGIN_SRC, 'commands', 'cave-tools.md'),
    path.join(commandsDir, 'cave-tools.md'),
    'opencode command/cave-tools.md',
  );
  if (fs.existsSync(commandsDirAlt) || DRY_RUN) {
    copyFileIfNeeded(
      path.join(OPENCODE_PLUGIN_SRC, 'commands', 'cave-tools.md'),
      path.join(commandsDirAlt, 'cave-tools.md'),
      'opencode commands/cave-tools.md',
    );
  }

  // 5. Skill (opencode auto-discovers ~/.config/opencode/skills/*/SKILL.md)
  if (fs.existsSync(OPENCODE_SKILL_SRC)) {
    if (DRY_RUN) {
      trackPath(path.join(skillsDir, 'SKILL.md'), 'write');
      log(`dry-run: would install skill ${skillsDir}/`);
    } else {
      copyDirRecursive(OPENCODE_SKILL_SRC, skillsDir);
      trackPath(path.join(skillsDir, 'SKILL.md'), 'write');
      log(`installed: ${skillsDir}/`);
    }
  } else {
    log(`skip (missing source): ${OPENCODE_SKILL_SRC}`);
  }

  // 6. AGENTS.md always-on ruleset (plugin handles dynamic reinforce/block)
  upsertFencedBlock(agentsMd, OPENCODE_CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });

  // Subagent defs may not inherit AGENTS.md — inject ruleset into each agent md.
  injectOpencodeAgentDefs([
    path.join(configDir, 'agent'),
    path.join(configDir, 'agents'),
    path.join(process.cwd(), '.opencode', 'agent'),
    path.join(process.cwd(), '.opencode', 'agents'),
  ]);
  upsertDisciplineBlock(agentsMd);
}

function installZcodeMcp() {
  const zcodeDir = zcodeConfigDir();
  const genericMcpPath = genericAgentsMcpPath();
  backupGlobalRule(path.join(zcodeDir, 'AGENTS.md'), 'zcode-AGENTS.md');
  backupGlobalRule(path.join(zcodeDir, 'skills', 'cave-tools', 'SKILL.md'), 'zcode-cave-tools-SKILL.md');

  const mcpConfig = readJson(genericMcpPath);
  if (!mcpConfig.mcpServers || typeof mcpConfig.mcpServers !== 'object' || Array.isArray(mcpConfig.mcpServers)) {
    mcpConfig.mcpServers = {};
  }

  mcpConfig.mcpServers['cave-tools'] = {
    command: 'node',
    args: [CAVE_TOOLS_CLI, 'mcp'],
  };

  writeJson(genericMcpPath, mcpConfig);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${genericMcpPath} mcpServers.cave-tools (ZCode import source)`);

  upsertFencedBlock(path.join(zcodeDir, 'AGENTS.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertDisciplineBlock(path.join(zcodeDir, 'AGENTS.md'));
  writeText(path.join(zcodeDir, 'skills', 'cave-tools', 'SKILL.md'), ZCODE_CAVE_TOOLS_SKILL);
  log('note: in ZCode, open Settings → MCP Servers → Import → Generic .agents, then import cave-tools');
}

function installGrok() {
  const grokDir = grokConfigDir();
  const configPath = path.join(grokDir, 'config.toml');
  const hooksDir = path.join(grokDir, 'hooks');
  const skillsDir = path.join(grokDir, 'skills', 'cave-tools');
  const agentsMd = path.join(grokDir, 'AGENTS.md');

  if (!fs.existsSync(grokDir)) {
    log(`skip (not installed): Grok Build CLI → ${grokDir}`);
    return;
  }

  backupGlobalRule(agentsMd, 'grok-AGENTS.md');
  backupGlobalRule(path.join(skillsDir, 'SKILL.md'), 'grok-cave-tools-SKILL.md');

  const mcp = resolveCaveToolsMcpCommand();
  const configExists = fs.existsSync(configPath);
  const currentConfig = fs.existsSync(configPath) ? fs.readFileSync(configPath, 'utf8') : '';
  const mcpBody = [
    `command = ${tomlString(mcp.command)}`,
    `args = ${tomlArray(mcp.args)}`,
    'enabled = true',
    'startup_timeout_sec = 30',
  ];
  const nextConfig = upsertTomlSection(currentConfig, 'mcp_servers.cave-tools', mcpBody);
  if (nextConfig === currentConfig) {
    log(`unchanged: ${configPath} [mcp_servers.cave-tools]`);
  } else {
    backupOnce(configPath);
    if (DRY_RUN) {
      trackPath(configPath, configExists ? 'update' : 'create');
      log(`dry-run: would upsert [mcp_servers.cave-tools] into ${configPath}`);
    } else {
      ensureDir(path.dirname(configPath));
      fs.writeFileSync(configPath, nextConfig, { mode: 0o644 });
      trackPath(configPath, configExists ? 'update' : 'create');
      log(`updated: ${configPath} [mcp_servers.cave-tools]`);
    }
  }

  // Grok ignores SessionStart stdout, so put always-on guidance in AGENTS.md
  // and install the skill in Grok's native skill path.
  upsertFencedBlock(agentsMd, CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertDisciplineBlock(agentsMd);
  copyFile(path.join(CLAUDE_BUNDLE_DIR, 'skills/cave-tools/SKILL.md'), path.join(skillsDir, 'SKILL.md'), 0o644);

  // Copy only hooks that are safe in Grok's model: activate writes the shared
  // mode flag as a side effect, redirect enforces built-in tool replacement.
  const activatePath = path.join(hooksDir, 'cave-tools-activate.js');
  const redirectPath = path.join(hooksDir, 'cave-tools-redirect.sh');
  copyFile(path.join(CLAUDE_BUNDLE_DIR, 'hooks/cave-tools-config.js'), path.join(hooksDir, 'cave-tools-config.js'), 0o644);
  copyFile(path.join(CLAUDE_BUNDLE_DIR, 'hooks/cave-tools-activate.js'), activatePath, 0o755);
  copyFile(path.join(CLAUDE_BUNDLE_DIR, 'hooks/cave-tools-redirect.sh'), redirectPath, 0o755);

  const hooksPath = path.join(hooksDir, 'cave-tools.json');
  const hooksBody = {
    description: 'cave-tools Grok integration: writes mode flag and redirects built-in tools to cave__* MCP tools.',
    hooks: {
      SessionStart: [{
        hooks: [{
          type: 'command',
          command: `${tomlString(process.execPath)} ${tomlString(activatePath)}`,
          timeout: 5,
        }],
      }],
      PreToolUse: [{
        matcher: 'Read|Grep|Glob|Edit|Write|read_file|grep|list_dir|search_replace',
        hooks: [{
          type: 'command',
          command: `bash ${tomlString(redirectPath)}`,
          timeout: 3,
        }],
      }],
    },
  };
  writeJson(hooksPath, hooksBody);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${hooksPath} hooks for Grok`);
}

function injectOpencodeAgentDefs(agentDirs, block = OPENCODE_CAVE_TOOLS_BLOCK) {
  for (const dir of agentDirs) {
    let entries;
    try {
      entries = fs.readdirSync(dir);
    } catch (e) {
      continue; // dir doesn't exist — nothing to patch
    }
    for (const entry of entries) {
      if (!entry.endsWith('.md')) continue;
      upsertFencedBlock(path.join(dir, entry), block, { skipIfExistingGuidance: true });
    }
  }
}

function claudeMcpJsonPath() {
  const dir = process.env.CLAUDE_CONFIG_DIR;
  if (dir && dir.trim()) return path.join(dir.trim(), '.claude.json');
  return path.join(HOME, '.claude.json');
}

function installMcpServersTarget(label, configPath, detectPath) {
  if (!fs.existsSync(configPath) && detectPath && !fs.existsSync(detectPath)) {
    log(`skip (not installed): ${label} → ${detectPath}`);
    return;
  }

  const config = readJson(configPath);
  if (!config.mcpServers || typeof config.mcpServers !== 'object' || Array.isArray(config.mcpServers)) {
    config.mcpServers = {};
  }

  config.mcpServers['cave-tools'] = {
    command: 'node',
    args: [CAVE_TOOLS_CLI, 'mcp'],
  };

  writeJson(configPath, config);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${configPath} mcpServers.cave-tools (${label})`);
}

function targetDefinitions() {
  return [
    { key: 'claude', label: 'Claude Code', configPath: claudeMcpJsonPath(), detectPath: process.env.CLAUDE_CONFIG_DIR || path.join(HOME, '.claude'), special: 'claude' },
    { key: 'codex', label: 'Codex CLI', configPath: path.join(HOME, '.codex', 'config.toml'), detectPath: path.join(HOME, '.codex'), special: 'codex' },
    { key: 'gemini', label: 'Gemini CLI', configPath: path.join(HOME, '.gemini', 'settings.json'), detectPath: path.join(HOME, '.gemini'), shape: 'mcpServers' },
    { key: 'antigravity', label: 'Antigravity IDE', configPath: path.join(HOME, '.gemini', 'antigravity', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity'), shape: 'mcpServers' },
    { key: 'antigravity-cli', label: 'Antigravity CLI', configPath: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-cli'), shape: 'mcpServers' },
    { key: 'antigravity-ide', label: 'Antigravity IDE (alt)', configPath: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-ide'), shape: 'mcpServers' },
    { key: 'antigravity-shared', label: 'Antigravity shared', configPath: path.join(HOME, '.gemini', 'config', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'config'), shape: 'mcpServers' },
    { key: 'antigravity-backup', label: 'Antigravity backup', configPath: path.join(HOME, '.gemini', 'antigravity-backup', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-backup'), shape: 'mcpServers' },
    { key: 'kiro', label: 'Kiro CLI', configPath: path.join(HOME, '.kiro', 'settings', 'mcp.json'), detectPath: path.join(HOME, '.kiro'), shape: 'mcpServers' },
    { key: 'cursor', label: 'Cursor', configPath: path.join(HOME, '.cursor', 'mcp.json'), detectPath: path.join(HOME, '.cursor'), shape: 'mcpServers' },
    { key: 'opencode', label: 'OpenCode', configPath: path.join(opencodeConfigDir(), 'opencode.json'), detectPath: opencodeConfigDir(), special: 'opencode' },
    { key: 'grok', label: 'Grok Build CLI', configPath: path.join(grokConfigDir(), 'config.toml'), detectPath: grokConfigDir(), special: 'grok' },
    { key: 'zcode', label: 'ZCode', configPath: path.join(zcodeConfigDir(), 'AGENTS.md'), detectPath: zcodeConfigDir(), special: 'zcode' },
  ];
}

function isDetected(target) {
  return fs.existsSync(target.configPath) || fs.existsSync(target.detectPath);
}

function printTargets(targets) {
  section('Targets');
  log(`${pad('Key', 20)} ${pad('Status', 9)} Config path`);
  log(`${'-'.repeat(20)} ${'-'.repeat(9)} ${'-'.repeat(60)}`);
  for (const target of targets) {
    const status = isDetected(target) ? 'detected' : 'missing';
    log(`${pad(target.key, 20)} ${pad(status, 9)} ${target.configPath}`);
  }
}

function printSelectedTargets(targets) {
  if (!VERBOSE) return;
  section('Selected targets');
  log(`${pad('Key', 20)} Config path`);
  log(`${'-'.repeat(20)} ${'-'.repeat(60)}`);
  for (const target of targets) log(`${pad(target.key, 20)} ${target.configPath}`);
}

function parseRequestedKeys(raw, targets) {
  const byKey = new Map(targets.map((target, index) => [String(index + 1), target.key]));
  for (const target of targets) byKey.set(target.key, target.key);
  const keys = raw
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean)
    .flatMap((part) => (part === 'all' ? targets.filter(isDetected).map((target) => target.key) : [part]));

  const resolved = [];
  for (const key of keys) {
    const resolvedKey = byKey.get(key);
    if (!resolvedKey) throw new Error(`unknown target: ${key}`);
    if (!resolved.includes(resolvedKey)) resolved.push(resolvedKey);
  }
  return resolved;
}

async function selectTargets(targets) {
  const args = process.argv.slice(2);
  const requested = [];
  let all = false;

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === '--') continue;
    if (arg === '--help' || arg === '-h') {
      log('Usage: pnpm run install:agents -- [--all|--agent <name>|--with-extra-rules|--list|--dry-run|--verbose]');
      log('Targets: ' + targets.map((target) => target.key).join(', '));
      log('Flags:');
      log('  --with-extra-rules  Also write the cave-discipline block (cave-tools + RTK-proxy +');
      log('                      codebase-memory-mcp + mcp-init-wait + subagent rules). Off by default.');
      process.exit(0);
    }
    if (arg === '--dry-run') continue;
    if (arg === '--verbose') continue;
    if (arg === '--with-extra-rules') continue;
    if (arg === '--list') {
      printTargets(targets);
      process.exit(0);
    }
    if (arg === '--all') {
      all = true;
      continue;
    }
    if (arg === '--agent') {
      requested.push(...parseRequestedKeys(args[++i] || '', targets));
      continue;
    }
    if (arg.startsWith('--agent=')) {
      requested.push(...parseRequestedKeys(arg.slice('--agent='.length), targets));
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }

  if (all) return targets.filter(isDetected);
  if (requested.length > 0) return targets.filter((target) => requested.includes(target.key));
  if (!process.stdin.isTTY) return targets.filter(isDetected);

  const detected = targets.filter(isDetected);
  section('Detected targets');
  detected.forEach((target, index) => log(`  ${index + 1}. ${target.key} (${target.label})`));
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const answer = (await rl.question('Install targets (all, comma numbers/names, cancel) [all]: ')).trim() || 'all';
  rl.close();
  if (answer === 'cancel' || answer === 'none') return [];
  const keys = parseRequestedKeys(answer, detected);
  return targets.filter((target) => keys.includes(target.key));
}

function installSelectedTargets(targets) {
  const keys = new Set(targets.map((target) => target.key));
  if (keys.has('claude')) {
    const claudeTarget = targets.find((target) => target.key === 'claude');
    installMcpServersTarget(claudeTarget.label, claudeTarget.configPath, claudeTarget.detectPath);
    installClaudeMcp();
    installSharedCavemanSkill();
    installCaveToolsClaudeHooks();
  }
  if (keys.has('codex')) installCodex();
  if (keys.has('opencode')) installOpencodeMcp();
  if (keys.has('grok')) installGrok();
  if (keys.has('zcode')) installZcodeMcp();

  for (const target of targets) {
    if (target.shape === 'mcpServers') installMcpServersTarget(target.label, target.configPath, target.detectPath);
  }

  if ([...keys].some((key) => key === 'gemini' || key.startsWith('antigravity'))) {
    installGeminiAndAntigravityRules();
  }
  if (keys.has('kiro')) installKiroRules();
  if (keys.has('cursor')) installCursorRules();
}

function installGeminiAndAntigravityRules() {
  const geminiDir = path.join(HOME, '.gemini');
  if (!fs.existsSync(geminiDir)) {
    log(`skip (not installed): Gemini/Antigravity rules → ${geminiDir}`);
    return;
  }

  upsertFencedBlock(path.join(geminiDir, 'GEMINI.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertFencedBlock(path.join(geminiDir, 'AGENTS.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertDisciplineBlock(path.join(geminiDir, 'AGENTS.md'));

  // Antigravity variants have their own rules locations alongside the shared
  // gemini config. With --with-extra-rules, write the discipline block to the
  // cli/AGENTS.md and the ide/agents/ dir so both variants pick it up.
  if (WITH_EXTRA_RULES) {
    const agCliAgents = path.join(geminiDir, 'antigravity-cli', 'AGENTS.md');
    if (fs.existsSync(path.dirname(agCliAgents))) upsertDisciplineBlock(agCliAgents);
    const agIdeAgentsDir = path.join(geminiDir, 'antigravity-ide', 'agents');
    if (fs.existsSync(agIdeAgentsDir)) upsertDisciplineBlock(path.join(agIdeAgentsDir, 'cave-discipline.md'));
  }
}

function installKiroRules() {
  const kiroDir = path.join(HOME, '.kiro');
  if (!fs.existsSync(kiroDir)) {
    log(`skip (not installed): Kiro rules → ${kiroDir}`);
    return;
  }
  const steering = path.join(kiroDir, 'steering');
  ensureDir(steering);

  // Full-file writes with inclusion: always frontmatter. Kiro tool names in
  // rules/cave-tools-kiro.md (fs_read/execute_bash → cave__*). Not the generic
  // Claude block ("When caveman mode loads…").
  if (!fs.existsSync(KIRO_CAVE_TOOLS_PATH)) {
    log(`skip (missing): ${KIRO_CAVE_TOOLS_PATH}`);
  } else {
    writeText(
      path.join(steering, 'cave-tools.md'),
      readKiroSteeringFile(KIRO_CAVE_TOOLS_PATH, MARKER_BEGIN, MARKER_END),
    );
  }

  if (fs.existsSync(KIRO_COMPRESSION_PATH)) {
    writeText(path.join(steering, '07-compression-tools.md'), fs.readFileSync(KIRO_COMPRESSION_PATH, 'utf8'));
  }
  if (fs.existsSync(KIRO_EDIT_SAFETY_PATH)) {
    writeText(path.join(steering, '08-edit-safety.md'), fs.readFileSync(KIRO_EDIT_SAFETY_PATH, 'utf8'));
  }

  const hookDest = path.join(kiroDir, 'hooks', 'cave-tools-redirect.sh');
  copyFile(KIRO_REDIRECT_HOOK_SRC, hookDest, 0o755);

  // kiro convention: one steering file per concern. Discipline gets its own.
  if (WITH_EXTRA_RULES && fs.existsSync(DISCIPLINE_RULES_PATH)) {
    writeText(
      path.join(steering, 'cave-discipline.md'),
      readKiroSteeringFile(DISCIPLINE_RULES_PATH, DISCIPLINE_MARKER_BEGIN, DISCIPLINE_MARKER_END),
    );
  }
}

// Cursor rules live in ~/.cursor/rules/*.mdc with YAML frontmatter. The
// discipline block gets its own .mdc file (alwaysApply: true) so Cursor loads
// it every session. Only runs with --with-extra-rules.
function installCursorRules() {
  if (!WITH_EXTRA_RULES) return;
  const cursorRulesDir = path.join(HOME, '.cursor', 'rules');
  if (!fs.existsSync(cursorRulesDir)) return;
  const discPath = path.join(cursorRulesDir, 'cave-discipline.mdc');
  const frontmatter = '---\ndescription: Cave discipline rules — cave-tools MCP, RTK, codebase-memory-mcp, MCP init, subagent propagation.\nglobs: *\nalwaysApply: true\n---\n\n';
  const body = fs.readFileSync(DISCIPLINE_RULES_PATH, 'utf8').trimEnd() + '\n';
  const block = `${DISCIPLINE_MARKER_BEGIN}\n${body}${DISCIPLINE_MARKER_END}\n`;
  const next = frontmatter + block;
  if (fs.existsSync(discPath)) {
    const current = fs.readFileSync(discPath, 'utf8');
    if (current === next) {
      log(`unchanged: ${discPath}`);
      return;
    }
    backupOnce(discPath);
  }
  if (DRY_RUN) {
    trackPath(discPath, 'create');
    log(`dry-run: would create ${discPath}`);
    return;
  }
  fs.writeFileSync(discPath, next, { mode: 0o644 });
  trackPath(discPath, 'create');
  log(`created: ${discPath}`);
}

function installSharedCavemanSkill() {
  backupGlobalRule(path.join(HOME, '.agents', 'skills', 'caveman', 'SKILL.md'), 'agents-caveman-SKILL.md');
  upsertFencedBlock(path.join(HOME, '.agents', 'skills', 'caveman', 'SKILL.md'), CAVE_TOOLS_POINTER_BLOCK);
}

// Copy a file with mode 0755 for scripts. Backs up the destination on first
// overwrite. Silently skips if source is missing — lets the install script
// run cleanly even if the claude/ bundle is partially absent.
function copyFile(src, dest, mode = 0o644) {
  if (!fs.existsSync(src)) {
    log(`skip (missing): ${src}`);
    return;
  }
  ensureDir(path.dirname(dest));
  if (fs.existsSync(dest)) backupOnce(dest);
  if (DRY_RUN) {
    trackPath(dest, 'copy');
    log(`dry-run: would copy ${src} → ${dest}`);
    return;
  }
  fs.copyFileSync(src, dest);
  try { fs.chmodSync(dest, mode); } catch (e) { /* best-effort on Windows */ }
  trackPath(dest, 'copy');
  log(`copied: ${dest}`);
}

// Stable identity for a hook handler so `node path` and `/usr/bin/node path`
// (or args-form vs inline command) collapse to one entry. Cave-tools scripts
// key on basename; statusline emit keys on a fixed token; everything else uses
// the full command+args string.
function hookHandlerIdentity(handler) {
  if (!handler || typeof handler !== 'object') return '';
  const cmd = String(handler.command || '');
  const argsJoined = Array.isArray(handler.args) ? handler.args.map(String).join(' ') : '';
  const full = `${cmd} ${argsJoined}`.trim();
  const script = full.match(/cave-tools-[\w.-]+\.(?:js|sh|ps1)/i);
  if (script) return `cave-tools:${script[0].toLowerCase()}`;
  if (/cave-tools(?:\s+|").*status/.test(full) && /emit-statusline/.test(full)) {
    return 'cave-tools:status-emit-statusline';
  }
  if (full.includes('statusline-wrapper.sh')) return 'cave-tools:statusline-wrapper';
  return full;
}

// Ensure a hook handler exists in settings.hooks[event][...].hooks[].
// Match by hookHandlerIdentity so node-path variants of the same script are
// not duplicated on repeated installs (or after older installers used a
// different process.execPath / bare `node`).
function upsertHookHandler(settings, event, matcher, handler) {
  if (!settings.hooks || typeof settings.hooks !== 'object' || Array.isArray(settings.hooks)) {
    settings.hooks = {};
  }
  if (!Array.isArray(settings.hooks[event])) settings.hooks[event] = [];

  const matchKey = matcher || null;
  const handlerKey = hookHandlerIdentity(handler);

  // Prefer updating an existing same-identity handler anywhere under this event
  // (even if matcher grouping differs), then skip insert.
  for (const group of settings.hooks[event]) {
    if (!Array.isArray(group.hooks)) continue;
    for (let i = 0; i < group.hooks.length; i++) {
      if (hookHandlerIdentity(group.hooks[i]) !== handlerKey) continue;
      // Refresh command/timeout to the preferred form from this install.
      group.hooks[i] = { ...group.hooks[i], ...handler };
      return false;
    }
  }

  for (const group of settings.hooks[event]) {
    const groupMatcher = group.matcher || null;
    if (groupMatcher !== matchKey) continue;
    if (!Array.isArray(group.hooks)) group.hooks = [];
    group.hooks.push(handler);
    return true;
  }

  const group = { hooks: [handler] };
  if (matchKey) group.matcher = matchKey;
  settings.hooks[event].push(group);
  return true;
}

// Drop duplicate cave-tools handlers left by older installs (same script,
// different node binary path). Keeps the first occurrence; non-cave-tools
// hooks are untouched.
function dedupeCaveToolsHooks(settings) {
  if (!settings.hooks || typeof settings.hooks !== 'object') return 0;
  let removed = 0;
  for (const event of Object.keys(settings.hooks)) {
    const groups = settings.hooks[event];
    if (!Array.isArray(groups)) continue;
    const seen = new Set();
    for (const group of groups) {
      if (!Array.isArray(group.hooks)) continue;
      const next = [];
      for (const h of group.hooks) {
        const id = hookHandlerIdentity(h);
        if (id.startsWith('cave-tools:')) {
          if (seen.has(id)) {
            removed++;
            continue;
          }
          seen.add(id);
        }
        next.push(h);
      }
      group.hooks = next;
    }
    settings.hooks[event] = groups.filter((g) => Array.isArray(g.hooks) && g.hooks.length > 0);
  }
  return removed;
}

function installCaveToolsClaudeHooks() {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(HOME, '.claude');
  const hooksDir = path.join(claudeDir, 'hooks');
  const skillsDir = path.join(claudeDir, 'skills', 'cave-tools');
  const commandsDir = path.join(claudeDir, 'commands');
  const settingsPath = path.join(claudeDir, 'settings.json');

  ensureDir(hooksDir);
  ensureDir(skillsDir);
  ensureDir(commandsDir);

  // 1. Copy hook scripts + skill + slash command.
  const files = [
    ['hooks/cave-tools-config.js', path.join(hooksDir, 'cave-tools-config.js'), 0o644],
    ['hooks/cave-tools-activate.js', path.join(hooksDir, 'cave-tools-activate.js'), 0o755],
    ['hooks/cave-tools-subagent.js', path.join(hooksDir, 'cave-tools-subagent.js'), 0o755],
    ['hooks/cave-tools-mode-tracker.js', path.join(hooksDir, 'cave-tools-mode-tracker.js'), 0o755],
    ['hooks/cave-tools-statusline.sh', path.join(hooksDir, 'cave-tools-statusline.sh'), 0o755],
    ['hooks/cave-tools-statusline.ps1', path.join(hooksDir, 'cave-tools-statusline.ps1'), 0o644],
    ['hooks/statusline-wrapper.sh', path.join(hooksDir, 'statusline-wrapper.sh'), 0o755],
    ['hooks/cave-tools-redirect.sh', path.join(hooksDir, 'cave-tools-redirect.sh'), 0o755],
    ['skills/cave-tools/SKILL.md', path.join(skillsDir, 'SKILL.md'), 0o644],
    ['commands/cave-tools.md', path.join(commandsDir, 'cave-tools.md'), 0o644],
  ];
  for (const [rel, dest, mode] of files) {
    copyFile(path.join(CLAUDE_BUNDLE_DIR, rel), dest, mode);
  }

  // 2. Patch settings.json — add lifecycle hooks. Each upsertHookHandler call
  //    is idempotent: re-running the installer won't duplicate entries.
  const settings = readJson(settingsPath);

  const nodeBin = process.execPath;
  const activatePath = path.join(hooksDir, 'cave-tools-activate.js');
  const subagentPath = path.join(hooksDir, 'cave-tools-subagent.js');
  const trackerPath = path.join(hooksDir, 'cave-tools-mode-tracker.js');
  const redirectPath = path.join(hooksDir, 'cave-tools-redirect.sh');

  upsertHookHandler(settings, 'SessionStart', null, {
    type: 'command',
    command: `"${nodeBin}" "${activatePath}"`,
    timeout: 5,
    statusMessage: 'Loading cave-tools rules...',
  });
  // SessionStart does not fire for subagents; SubagentStart does. Inject the
  // same ruleset into every spawned subagent (built-in + custom).
  upsertHookHandler(settings, 'SubagentStart', null, {
    type: 'command',
    command: `"${nodeBin}" "${subagentPath}"`,
    timeout: 5,
  });
  upsertHookHandler(settings, 'SessionStart', null, {
    type: 'command',
    command: 'cave-tools status --emit-statusline',
    timeout: 3,
    async: true,
  });
  upsertHookHandler(settings, 'UserPromptSubmit', null, {
    type: 'command',
    command: `"${nodeBin}" "${trackerPath}"`,
    timeout: 5,
  });

  // Replace any pre-existing Read|Grep|Glob redirect group with the new
  // mode-aware path. Match by matcher string AND by command containing
  // 'cave-tools-redirect.sh' so we don't trample unrelated PreToolUse hooks.
  // While we're here, widen the matcher to include Edit|Write so the strict
  // tier can also intercept those — the script is mode-aware and exits 0 in
  // lower modes, so widening is safe.
  let widenedExisting = false;
  if (Array.isArray(settings.hooks?.PreToolUse)) {
    for (const group of settings.hooks.PreToolUse) {
      if (!Array.isArray(group.hooks)) continue;
      const hasRedirect = group.hooks.some(
        (h) => typeof h.command === 'string' && h.command.includes('cave-tools-redirect.sh'),
      );
      if (!hasRedirect) continue;
      for (const h of group.hooks) {
        if (typeof h.command === 'string' && h.command.includes('cave-tools-redirect.sh')) {
          h.command = redirectPath;
        }
      }
      if (group.matcher !== 'Read|Grep|Glob|Edit|Write') {
        group.matcher = 'Read|Grep|Glob|Edit|Write';
        log(`patched: PreToolUse matcher widened → Read|Grep|Glob|Edit|Write`);
      } else {
        log(`unchanged: PreToolUse redirect already wired`);
      }
      widenedExisting = true;
    }
  }

  // Install the redirect group only if no existing one was widened — prevents
  // duplicate handlers that would double-fire on every Read/Grep/Glob call.
  if (!widenedExisting) {
    upsertHookHandler(settings, 'PreToolUse', 'Read|Grep|Glob|Edit|Write', {
      type: 'command',
      command: redirectPath,
      timeout: 3,
    });
  }

  upsertHookHandler(settings, 'PostToolUse', 'mcp__cave-tools__.*', {
    type: 'command',
    command: 'cave-tools status --emit-statusline',
    timeout: 3,
    async: true,
  });

  const deduped = dedupeCaveToolsHooks(settings);
  if (deduped > 0) {
    log(`deduped: removed ${deduped} duplicate cave-tools hook handler(s) from settings.json`);
  }

  // 3. Statusline — swap caveman-statusline.sh to wrapper if caveman is set;
  //    install wrapper if no statusline; leave alone otherwise.
  const wrapperPath = path.join(hooksDir, 'statusline-wrapper.sh');
  if (!settings.statusLine) {
    settings.statusLine = {
      type: 'command',
      command: `bash "${wrapperPath}"`,
    };
    log(`${DRY_RUN ? 'dry-run: would install' : 'installed'}: statusLine → ${wrapperPath}`);
  } else if (typeof settings.statusLine.command === 'string') {
    const cmd = settings.statusLine.command;
    if (cmd.includes('caveman-statusline.sh') && !cmd.includes('statusline-wrapper.sh')) {
      settings.statusLine.command = `bash "${wrapperPath}"`;
      log(`patched: statusLine swapped caveman script → wrapper`);
    }
  }

  writeJson(settingsPath, settings);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${settingsPath} hooks + statusLine for cave-tools`);
}

try {
  const targets = targetDefinitions();
  const selected = await selectTargets(targets);
  if (selected.length === 0) {
    log('cancelled: no targets selected');
    process.exit(0);
  }
  printSelectedTargets(selected);
  installSelectedTargets(selected);
  printTouchedPaths();
  log(`${DRY_RUN ? 'dry-run done' : 'done'}: cave-tools MCP, hooks, skill, and instruction blocks installed`);
} catch (error) {
  process.stderr.write(`error: ${error.message}\n`);
  process.exit(1);
}
