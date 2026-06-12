#!/usr/bin/env node

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';

const HOME = os.homedir();

const MARKER_BEGIN = '<!-- cave-tools-begin -->';
const MARKER_END = '<!-- cave-tools-end -->';
const DRY_RUN = process.argv.includes('--dry-run');
const VERBOSE = process.argv.includes('--verbose');
const touchedPaths = new Map();

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

function backupOnce(filePath) {
  if (DRY_RUN) return;
  if (!fs.existsSync(filePath)) return;
  const bakPath = `${filePath}.bak`;
  if (!fs.existsSync(bakPath)) fs.copyFileSync(filePath, bakPath);
}

function readJson(filePath) {
  if (!fs.existsSync(filePath)) return null;
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch (error) {
    throw new Error(`${filePath} is not valid JSON: ${error.message}`);
  }
}

function readJsonForRemoval(filePath) {
  try {
    return readJson(filePath);
  } catch (error) {
    log(`skip (invalid JSON): ${filePath}`);
    return null;
  }
}

function writeJson(filePath, value) {
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

function removeBlock(filePath) {
  if (!fs.existsSync(filePath)) {
    log(`skip (missing): ${filePath}`);
    return;
  }
  const current = fs.readFileSync(filePath, 'utf8');
  const next = removeFencedBlock(current).trimEnd() + '\n';
  if (next === current) {
    log(`unchanged: ${filePath}`);
    return;
  }
  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, 'update');
    log(`dry-run: would update ${filePath} removing cave-tools block`);
    return;
  }
  fs.writeFileSync(filePath, next, { mode: 0o644 });
  trackPath(filePath, 'update');
  log(`updated: ${filePath} removed cave-tools block`);
}

function removeMcpServersTarget(label, configPath) {
  const config = readJsonForRemoval(configPath);
  if (!config) {
    log(`skip (missing): ${label} → ${configPath}`);
    return;
  }
  if (!config.mcpServers || typeof config.mcpServers !== 'object' || Array.isArray(config.mcpServers)) {
    log(`unchanged: ${label} has no mcpServers`);
    return;
  }
  if (!Object.prototype.hasOwnProperty.call(config.mcpServers, 'cave-tools')) {
    log(`unchanged: ${label} has no cave-tools server`);
    return;
  }
  delete config.mcpServers['cave-tools'];
  writeJson(configPath, config);
  log(`${DRY_RUN ? 'dry-run: would update' : 'updated'}: ${configPath} removed mcpServers.cave-tools (${label})`);
}

function removeOpencodeMcp() {
  const configDir = process.env.XDG_CONFIG_HOME
    ? path.join(process.env.XDG_CONFIG_HOME, 'opencode')
    : process.platform === 'win32'
      ? path.join(process.env.APPDATA || path.join(HOME, 'AppData', 'Roaming'), 'opencode')
      : path.join(HOME, '.config', 'opencode');
  const configPath = path.join(configDir, 'opencode.json');
  const config = readJsonForRemoval(configPath);
  if (!config) {
    log(`skip (missing): OpenCode → ${configPath}`);
  } else if (!config.mcp || typeof config.mcp !== 'object' || Array.isArray(config.mcp)) {
    log('unchanged: OpenCode has no mcp key');
  } else if (!Object.prototype.hasOwnProperty.call(config.mcp, 'cave-tools')) {
    log('unchanged: OpenCode has no cave-tools server');
  } else {
    delete config.mcp['cave-tools'];
    writeJson(configPath, config);
    log(`${DRY_RUN ? 'dry-run: would update' : 'updated'}: ${configPath} removed mcp.cave-tools`);
  }

  removeBlock(path.join(configDir, 'AGENTS.md'));
}

function claudeConfigDir() {
  const dir = process.env.CLAUDE_CONFIG_DIR;
  if (dir && dir.trim()) return dir.trim();
  return path.join(HOME, '.claude');
}

function claudeMcpJsonPath() {
  const dir = process.env.CLAUDE_CONFIG_DIR;
  if (dir && dir.trim()) return path.join(dir.trim(), '.claude.json');
  return path.join(HOME, '.claude.json');
}

function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  if (process.platform === 'win32') {
    return path.join(process.env.APPDATA || path.join(HOME, 'AppData', 'Roaming'), 'opencode');
  }
  return path.join(HOME, '.config', 'opencode');
}

function targetDefinitions() {
  return [
    { key: 'claude', label: 'Claude Code', configPath: claudeMcpJsonPath(), detectPath: claudeConfigDir(), special: 'claude' },
    { key: 'gemini', label: 'Gemini CLI', configPath: path.join(HOME, '.gemini', 'settings.json'), detectPath: path.join(HOME, '.gemini'), shape: 'mcpServers' },
    { key: 'antigravity', label: 'Antigravity IDE', configPath: path.join(HOME, '.gemini', 'antigravity', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity', 'mcp', 'cave-tools') },
    { key: 'antigravity-cli', label: 'Antigravity CLI', configPath: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-cli'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp', 'cave-tools') },
    { key: 'antigravity-ide', label: 'Antigravity IDE (alt)', configPath: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-ide'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp', 'cave-tools') },
    { key: 'antigravity-shared', label: 'Antigravity shared', configPath: path.join(HOME, '.gemini', 'config', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'config'), shape: 'mcpServers' },
    { key: 'antigravity-backup', label: 'Antigravity backup', configPath: path.join(HOME, '.gemini', 'antigravity-backup', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-backup'), shape: 'mcpServers' },
    { key: 'kiro', label: 'Kiro CLI', configPath: path.join(HOME, '.kiro', 'settings', 'mcp.json'), detectPath: path.join(HOME, '.kiro'), shape: 'mcpServers' },
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
      log('Usage: pnpm run remove:agents -- [--all|--agent <name>|--list|--dry-run|--verbose]');
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
  const answer = (await rl.question('Remove targets (all, comma numbers/names, cancel) [all]: ')).trim() || 'all';
  rl.close();
  if (answer === 'cancel' || answer === 'none') return [];
  const keys = parseRequestedKeys(answer, detected);
  return targets.filter((target) => keys.includes(target.key));
}

function removeClaudeHooks() {
  const settingsPath = path.join(claudeConfigDir(), 'settings.json');
  const settings = readJsonForRemoval(settingsPath);
  if (!settings) {
    log(`skip (missing): Claude settings → ${settingsPath}`);
    return;
  }

  let changed = false;
  if (settings.mcpServers && typeof settings.mcpServers === 'object' && !Array.isArray(settings.mcpServers)) {
    if (Object.prototype.hasOwnProperty.call(settings.mcpServers, 'cave-tools')) {
      delete settings.mcpServers['cave-tools'];
      changed = true;
    }
  }

  if (settings.hooks && typeof settings.hooks === 'object' && !Array.isArray(settings.hooks)) {
    for (const [event, groups] of Object.entries(settings.hooks)) {
      if (!Array.isArray(groups)) continue;
      const nextGroups = groups
        .map((group) => {
          if (!group || typeof group !== 'object' || !Array.isArray(group.hooks)) return group;
          const nextHooks = group.hooks.filter((hook) => {
            const command = typeof hook?.command === 'string' ? hook.command : '';
            return !command.includes('cave-tools') && !command.includes('statusline-wrapper.sh');
          });
          if (nextHooks.length === group.hooks.length) return group;
          changed = true;
          return { ...group, hooks: nextHooks };
        })
        .filter((group) => !group || typeof group !== 'object' || !Array.isArray(group.hooks) || group.hooks.length > 0);
      if (nextGroups.length !== groups.length) changed = true;
      settings.hooks[event] = nextGroups;
    }
  }

  if (settings.statusLine?.command && String(settings.statusLine.command).includes('statusline-wrapper.sh')) {
    delete settings.statusLine;
    changed = true;
  }

  if (!changed) {
    log(`unchanged: ${settingsPath}`);
    return;
  }
  writeJson(settingsPath, settings);
  log(`${DRY_RUN ? 'dry-run: would update' : 'updated'}: ${settingsPath} removed cave-tools MCP/hooks/statusLine`);
}

function removeKnownFiles() {
  const claudeDir = claudeConfigDir();
  for (const filePath of [
    path.join(claudeDir, 'hooks', 'cave-tools-config.js'),
    path.join(claudeDir, 'hooks', 'cave-tools-activate.js'),
    path.join(claudeDir, 'hooks', 'cave-tools-mode-tracker.js'),
    path.join(claudeDir, 'hooks', 'cave-tools-statusline.sh'),
    path.join(claudeDir, 'hooks', 'cave-tools-statusline.ps1'),
    path.join(claudeDir, 'hooks', 'statusline-wrapper.sh'),
    path.join(claudeDir, 'hooks', 'cave-tools-redirect.sh'),
    path.join(claudeDir, 'commands', 'cave-tools.md'),
    path.join(claudeDir, 'skills', 'cave-tools', 'SKILL.md'),
  ]) {
    if (!fs.existsSync(filePath)) continue;
    if (DRY_RUN) {
      trackPath(filePath, 'remove');
      log(`dry-run: would remove ${filePath}`);
      continue;
    }
    fs.unlinkSync(filePath);
    trackPath(filePath, 'remove');
    log(`removed: ${filePath}`);
  }
}

function removeKiroGeneratedRule() {
  const filePath = path.join(HOME, '.kiro', 'steering', 'cave-tools.md');
  if (!fs.existsSync(filePath)) {
    log(`skip (missing): ${filePath}`);
    return;
  }
  const current = fs.readFileSync(filePath, 'utf8');
  if (!current.includes(MARKER_BEGIN) || !current.includes(MARKER_END)) {
    log(`unchanged: ${filePath}`);
    return;
  }
  if (DRY_RUN) {
    trackPath(filePath, 'remove');
    log(`dry-run: would remove ${filePath}`);
    return;
  }
  fs.unlinkSync(filePath);
  trackPath(filePath, 'remove');
  log(`removed: ${filePath}`);
}

function removeDirIfExists(dirPath) {
  if (!fs.existsSync(dirPath)) return;
  if (DRY_RUN) {
    trackPath(dirPath, 'remove-dir');
    log(`dry-run: would remove ${dirPath}`);
    return;
  }
  fs.rmSync(dirPath, { recursive: true, force: true });
  trackPath(dirPath, 'remove-dir');
  log(`removed: ${dirPath}`);
}

function removeAntigravityOauthToken(target) {
  const tokenPath = path.join(target.detectPath, 'mcp_oauth_tokens.json');
  const tokens = readJsonForRemoval(tokenPath);
  if (!tokens || typeof tokens !== 'object' || Array.isArray(tokens)) return;
  if (!Object.prototype.hasOwnProperty.call(tokens, 'cave-tools')) return;
  delete tokens['cave-tools'];
  writeJson(tokenPath, tokens);
  log(`${DRY_RUN ? 'dry-run: would update' : 'updated'}: ${tokenPath} removed cave-tools OAuth token`);
}

function removeSelectedTargets(targets) {
  const keys = new Set(targets.map((target) => target.key));

  if (keys.has('claude')) {
    removeMcpServersTarget('Claude Code', claudeMcpJsonPath());
    removeClaudeHooks();
    removeBlock(path.join(claudeConfigDir(), 'CLAUDE.md'));
    removeBlock(path.join(claudeConfigDir(), 'skills', 'caveman', 'SKILL.md'));
    removeBlock(path.join(HOME, '.agents', 'skills', 'caveman', 'SKILL.md'));
    removeKnownFiles();
  }

  if (keys.has('opencode')) removeOpencodeMcp();

  for (const target of targets) {
    if (target.shape === 'mcpServers') removeMcpServersTarget(target.label, target.configPath);
    if (target.key.startsWith('antigravity')) {
      if (target.cacheDir) removeDirIfExists(target.cacheDir);
      removeAntigravityOauthToken(target);
    }
  }

  if ([...keys].some((key) => key === 'gemini' || key.startsWith('antigravity'))) {
    removeBlock(path.join(HOME, '.gemini', 'GEMINI.md'));
    removeBlock(path.join(HOME, '.gemini', 'AGENTS.md'));
  }
  if (keys.has('kiro')) removeKiroGeneratedRule();
}

try {
  const targets = targetDefinitions();
  const selected = await selectTargets(targets);
  if (selected.length === 0) {
    log('cancelled: no targets selected');
    process.exit(0);
  }
  printSelectedTargets(selected);
  removeSelectedTargets(selected);
  printTouchedPaths();
  log(`${DRY_RUN ? 'dry-run done' : 'done'}: cave-tools MCP, hooks, skill, and instruction blocks removed`);
} catch (error) {
  process.stderr.write(`error: ${error.message}\n`);
  process.exit(1);
}
