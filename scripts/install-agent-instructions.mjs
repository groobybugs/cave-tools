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
const DRY_RUN = process.argv.includes('--dry-run');
const VERBOSE = process.argv.includes('--verbose');
const touchedPaths = new Map();

// Long-form rules block. Still written to opencode AGENTS.md (no skill system
// there), but on Claude side we now write the shorter pointer below since
// SKILL.md is the source of truth.
const CAVE_TOOLS_BLOCK = [
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'When caveman mode loads, also use Cave Tools as the preferred compressed tool layer.',
  '',
  '- Use `cave__read` instead of the built-in read tool for file reads. It applies read dedup and Flint Chipper line-budget compression.',
  '- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell commands or built-in search tools when available.',
  '- Use `cave__bash` instead of the built-in shell tool for commands. It tries RTK rewriting when available, then applies Stone Tablet JSON/XML compression and Flint Chipper budgets.',
  '- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`; pass the raw command.',
  '- After editing a file outside Cave Tools, call `cave__write` with changed path(s) to invalidate the read dedup cache.',
  '- Use `cave__compress` for large pasted or tool-produced text.',
  '- Use `cave__status` to inspect RTK availability, cache state, and savings.',
  MARKER_END,
  '',
].join('\n');

// Short pointer block used in Claude installs where the /cave-tools skill +
// SessionStart hook are the source of truth — avoids token-wasting duplication.
const CAVE_TOOLS_POINTER_BLOCK = [
  MARKER_BEGIN,
  '# Cave Tools MCP',
  '',
  'See the `/cave-tools` skill for token-saving tool rules (cave__read, cave__bash, cave__grep, etc.).',
  'The SessionStart hook auto-injects the full ruleset every session.',
  MARKER_END,
  '',
].join('\n');

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

function removeFencedBlock(content) {
  let next = content;
  while (true) {
    const begin = next.indexOf(MARKER_BEGIN);
    const end = next.indexOf(MARKER_END);
    if (begin === -1 || end === -1 || end <= begin) return next;
    next = next.slice(0, begin).trimEnd() + '\n\n' + next.slice(end + MARKER_END.length).trimStart();
  }
}

function hasCaveToolsGuidance(content) {
  return /#\s*Cave Tools MCP/i.test(content) && /`cave__read`/.test(content) && /`cave__bash`/.test(content);
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
}

function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  if (process.platform === 'win32') {
    return path.join(process.env.APPDATA || path.join(HOME, 'AppData', 'Roaming'), 'opencode');
  }
  return path.join(HOME, '.config', 'opencode');
}

function installOpencodeMcp() {
  const configDir = opencodeConfigDir();
  const configPath = path.join(configDir, 'opencode.json');
  backupGlobalRule(path.join(configDir, 'AGENTS.md'), 'opencode-AGENTS.md');

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

  writeJson(configPath, config);
  log(`${DRY_RUN ? 'dry-run: would configure' : 'configured'}: ${configPath} mcp.cave-tools`);

  upsertFencedBlock(path.join(configDir, 'AGENTS.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
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
    { key: 'gemini', label: 'Gemini CLI', configPath: path.join(HOME, '.gemini', 'settings.json'), detectPath: path.join(HOME, '.gemini'), shape: 'mcpServers' },
    { key: 'antigravity', label: 'Antigravity IDE', configPath: path.join(HOME, '.gemini', 'antigravity', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity'), shape: 'mcpServers' },
    { key: 'antigravity-cli', label: 'Antigravity CLI', configPath: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-cli'), shape: 'mcpServers' },
    { key: 'antigravity-ide', label: 'Antigravity IDE (alt)', configPath: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-ide'), shape: 'mcpServers' },
    { key: 'antigravity-shared', label: 'Antigravity shared', configPath: path.join(HOME, '.gemini', 'config', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'config'), shape: 'mcpServers' },
    { key: 'antigravity-backup', label: 'Antigravity backup', configPath: path.join(HOME, '.gemini', 'antigravity-backup', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-backup'), shape: 'mcpServers' },
    { key: 'kiro', label: 'Kiro CLI', configPath: path.join(HOME, '.kiro', 'settings', 'mcp.json'), detectPath: path.join(HOME, '.kiro'), shape: 'mcpServers', rules: 'kiro' },
    { key: 'cursor', label: 'Cursor', configPath: path.join(HOME, '.cursor', 'mcp.json'), detectPath: path.join(HOME, '.cursor'), shape: 'mcpServers' },
    { key: 'opencode', label: 'OpenCode', configPath: path.join(opencodeConfigDir(), 'opencode.json'), detectPath: opencodeConfigDir(), special: 'opencode' },
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
      log('Usage: pnpm run install:agents -- [--all|--agent <name>|--list|--dry-run|--verbose]');
      log('Targets: ' + targets.map((target) => target.key).join(', '));
      process.exit(0);
    }
    if (arg === '--dry-run') continue;
    if (arg === '--verbose') continue;
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
  if (keys.has('opencode')) installOpencodeMcp();

  for (const target of targets) {
    if (target.shape === 'mcpServers') installMcpServersTarget(target.label, target.configPath, target.detectPath);
  }

  if ([...keys].some((key) => key === 'gemini' || key.startsWith('antigravity'))) {
    installGeminiAndAntigravityRules();
  }
  if (keys.has('kiro')) installKiroRules();
}

function installGeminiAndAntigravityRules() {
  const geminiDir = path.join(HOME, '.gemini');
  if (!fs.existsSync(geminiDir)) {
    log(`skip (not installed): Gemini/Antigravity rules → ${geminiDir}`);
    return;
  }

  upsertFencedBlock(path.join(geminiDir, 'GEMINI.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
  upsertFencedBlock(path.join(geminiDir, 'AGENTS.md'), CAVE_TOOLS_BLOCK, { skipIfExistingGuidance: true });
}

function installKiroRules() {
  const kiroDir = path.join(HOME, '.kiro');
  if (!fs.existsSync(kiroDir)) {
    log(`skip (not installed): Kiro rules → ${kiroDir}`);
    return;
  }
  upsertFencedBlock(path.join(kiroDir, 'steering', 'cave-tools.md'), CAVE_TOOLS_BLOCK);
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

// Ensure a hook handler exists in settings.hooks[event][...].hooks[].
// Match by `command` substring so equivalent entries (with different timeouts
// or statusMessages) aren't duplicated on repeated installs.
function upsertHookHandler(settings, event, matcher, handler) {
  if (!settings.hooks || typeof settings.hooks !== 'object' || Array.isArray(settings.hooks)) {
    settings.hooks = {};
  }
  if (!Array.isArray(settings.hooks[event])) settings.hooks[event] = [];

  const matchKey = matcher || null;
  const handlerKey = (handler.command || '') + ' ' + ((handler.args || []).join(' '));

  for (const group of settings.hooks[event]) {
    const groupMatcher = group.matcher || null;
    if (groupMatcher !== matchKey) continue;
    if (!Array.isArray(group.hooks)) group.hooks = [];
    for (const h of group.hooks) {
      const existingKey = (h.command || '') + ' ' + ((h.args || []).join(' '));
      if (existingKey === handlerKey) return false; // already present
    }
    group.hooks.push(handler);
    return true;
  }

  const group = { hooks: [handler] };
  if (matchKey) group.matcher = matchKey;
  settings.hooks[event].push(group);
  return true;
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
  const trackerPath = path.join(hooksDir, 'cave-tools-mode-tracker.js');
  const redirectPath = path.join(hooksDir, 'cave-tools-redirect.sh');

  upsertHookHandler(settings, 'SessionStart', null, {
    type: 'command',
    command: `"${nodeBin}" "${activatePath}"`,
    timeout: 5,
    statusMessage: 'Loading cave-tools rules...',
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
