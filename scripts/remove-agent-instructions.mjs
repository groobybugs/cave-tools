#!/usr/bin/env node

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';

const HOME = os.homedir();

const MARKER_BEGIN = '<!-- cave-tools-begin -->';
const MARKER_END = '<!-- cave-tools-end -->';
const DISCIPLINE_MARKER_BEGIN = '<!-- cave-discipline-begin -->';
const DISCIPLINE_MARKER_END = '<!-- cave-discipline-end -->';
const DRY_RUN = process.argv.includes('--dry-run');
const VERBOSE = process.argv.includes('--verbose');
const WITH_EXTRA_RULES = process.argv.includes('--with-extra-rules');
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
  return removeFencedBlockGeneric(content, MARKER_BEGIN, MARKER_END);
}

function removeFencedBlockGeneric(content, beginMarker, endMarker) {
  let next = content;
  while (true) {
    const begin = next.indexOf(beginMarker);
    const end = next.indexOf(endMarker);
    if (begin === -1 || end === -1 || end <= begin) return next;
    next = next.slice(0, begin).trimEnd() + '\n\n' + next.slice(end + endMarker.length).trimStart();
  }
}

// Strip only the discipline block (--with-extra-rules). Leaves the cave-tools
// block intact unless the caller is doing a full remove.
function removeDisciplineBlock(filePath) {
  if (!fs.existsSync(filePath)) {
    log(`skip (missing): ${filePath}`);
    return;
  }
  const current = fs.readFileSync(filePath, 'utf8');
  const next = removeFencedBlockGeneric(current, DISCIPLINE_MARKER_BEGIN, DISCIPLINE_MARKER_END).trimEnd() + '\n';
  if (next === current) {
    log(`unchanged: ${filePath}`);
    return;
  }
  backupOnce(filePath);
  if (DRY_RUN) {
    trackPath(filePath, 'update');
    log(`dry-run: would strip discipline block from ${filePath}`);
    return;
  }
  fs.writeFileSync(filePath, next, { mode: 0o644 });
  trackPath(filePath, 'update');
  log(`updated: ${filePath} stripped discipline block`);
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

function removeTomlSection(content, header) {
  const lines = content.split('\n');
  const start = lines.findIndex((line) => line.trim() === `[${header}]`);
  if (start === -1) return content;

  let end = lines.length;
  for (let i = start + 1; i < lines.length; i += 1) {
    const trimmed = lines[i].trim();
    if (trimmed.startsWith('[') && trimmed.endsWith(']')) {
      end = i;
      break;
    }
  }

  lines.splice(start, end - start);
  return lines.join('\n').replace(/\n{3,}/g, '\n\n');
}

function removeGrokTomlMcp() {
  const configPath = path.join(grokConfigDir(), 'config.toml');
  if (!fs.existsSync(configPath)) {
    log(`skip (missing): Grok Build CLI → ${configPath}`);
    return;
  }
  const current = fs.readFileSync(configPath, 'utf8');
  const next = removeTomlSection(current, 'mcp_servers.cave-tools');
  if (next === current) {
    log(`unchanged: ${configPath} has no [mcp_servers.cave-tools]`);
    return;
  }
  backupOnce(configPath);
  if (DRY_RUN) {
    trackPath(configPath, 'update');
    log(`dry-run: would remove [mcp_servers.cave-tools] from ${configPath}`);
    return;
  }
  fs.writeFileSync(configPath, next.endsWith('\n') || next === '' ? next : `${next}\n`, { mode: 0o644 });
  trackPath(configPath, 'update');
  log(`updated: ${configPath} removed [mcp_servers.cave-tools]`);
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

function removeZcodeMcp() {
  removeMcpServersTarget('ZCode generic .agents import source', genericAgentsMcpPath());
  removeBlock(path.join(zcodeConfigDir(), 'AGENTS.md'));
  removeZcodeGeneratedSkill();
}

function removeFileIfExists(filePath) {
  if (!fs.existsSync(filePath)) {
    log(`skip (missing): ${filePath}`);
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

function removeGrokGeneratedSkill() {
  const skillPath = path.join(grokConfigDir(), 'skills', 'cave-tools', 'SKILL.md');
  if (!fs.existsSync(skillPath)) {
    log(`skip (missing): ${skillPath}`);
    return;
  }
  const current = fs.readFileSync(skillPath, 'utf8');
  if (!current.includes('cave__read') || !current.includes('cave__bash') || !current.includes('cave__apply_patch')) {
    log(`unchanged: ${skillPath}`);
    return;
  }
  backupOnce(skillPath);
  removeFileIfExists(skillPath);
}

function removeGrok() {
  const grokDir = grokConfigDir();
  removeGrokTomlMcp();
  removeBlock(path.join(grokDir, 'AGENTS.md'));
  removeGrokGeneratedSkill();
  for (const filePath of [
    path.join(grokDir, 'hooks', 'cave-tools.json'),
    path.join(grokDir, 'hooks', 'cave-tools-config.js'),
    path.join(grokDir, 'hooks', 'cave-tools-activate.js'),
    path.join(grokDir, 'hooks', 'cave-tools-redirect.sh'),
  ]) {
    removeFileIfExists(filePath);
  }
}

function removeZcodeGeneratedSkill() {
  const skillPath = path.join(zcodeConfigDir(), 'skills', 'cave-tools', 'SKILL.md');
  if (!fs.existsSync(skillPath)) {
    log(`skip (missing): ${skillPath}`);
    return;
  }
  const current = fs.readFileSync(skillPath, 'utf8');
  if (!current.includes(MARKER_BEGIN) || !current.includes(MARKER_END)) {
    log(`unchanged: ${skillPath}`);
    return;
  }
  backupOnce(skillPath);
  if (DRY_RUN) {
    trackPath(skillPath, 'remove');
    log(`dry-run: would remove ${skillPath}`);
    return;
  }
  fs.unlinkSync(skillPath);
  trackPath(skillPath, 'remove');
  log(`removed: ${skillPath}`);
}

function targetDefinitions() {
  return [
    { key: 'claude', label: 'Claude Code', configPath: claudeMcpJsonPath(), detectPath: claudeConfigDir(), special: 'claude' },
    { key: 'codex', label: 'Codex CLI', configPath: path.join(HOME, '.codex', 'config.toml'), detectPath: path.join(HOME, '.codex'), special: 'codex' },
    { key: 'gemini', label: 'Gemini CLI', configPath: path.join(HOME, '.gemini', 'settings.json'), detectPath: path.join(HOME, '.gemini'), shape: 'mcpServers' },
    { key: 'antigravity', label: 'Antigravity IDE', configPath: path.join(HOME, '.gemini', 'antigravity', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity', 'mcp', 'cave-tools') },
    { key: 'antigravity-cli', label: 'Antigravity CLI', configPath: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-cli'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity-cli', 'mcp', 'cave-tools') },
    { key: 'antigravity-ide', label: 'Antigravity IDE (alt)', configPath: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp_config.json'), detectPath: path.join(HOME, '.gemini', 'antigravity-ide'), shape: 'mcpServers', cacheDir: path.join(HOME, '.gemini', 'antigravity-ide', 'mcp', 'cave-tools') },
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
      log('Usage: pnpm run remove:agents -- [--all|--agent <name>|--with-extra-rules|--list|--dry-run|--verbose]');
      log('Targets: ' + targets.map((target) => target.key).join(', '));
      log('Flags:');
      log('  --with-extra-rules  Also strip the cave-discipline block (default: leave it in place).');
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

// Codex CLI removal: strip [mcp_servers.cave-tools] TOML section, remove the
// AGENTS.md fenced block, delete hooks.json (only if it contains our marker),
// and flip [features] hooks = true → false ONLY if no other SessionStart hook
// remains (avoid clobbering caveman's hook enablement).
function removeCodex() {
  const codexDir = path.join(HOME, '.codex');
  const configPath = path.join(codexDir, 'config.toml');
  const agentsMd = path.join(codexDir, 'AGENTS.md');
  const hooksPath = path.join(codexDir, 'hooks.json');
  const CODEX_HOOK_MARKER = 'CAVE-TOOLS ACTIVE';

  // 1. Strip [mcp_servers.cave-tools] from config.toml.
  if (fs.existsSync(configPath)) {
    const cfg = fs.readFileSync(configPath, 'utf8');
    const sectionRe = /\n*\[\s*mcp_servers\.cave-tools\s*\]\s*\n[^\[]*/g;
    if (sectionRe.test(cfg)) {
      const nextCfg = cfg.replace(sectionRe, '\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n';
      backupOnce(configPath);
      if (DRY_RUN) {
        trackPath(configPath, 'update');
        log(`dry-run: would strip [mcp_servers.cave-tools] from ${configPath}`);
      } else {
        fs.writeFileSync(configPath, nextCfg, { mode: 0o644 });
        trackPath(configPath, 'update');
        log(`updated: ${configPath} stripped [mcp_servers.cave-tools]`);
      }
    } else {
      log(`unchanged: ${configPath} no [mcp_servers.cave-tools] section`);
    }

    // Flip hooks = true → false only if hooks.json no longer needs it.
    if (fs.existsSync(hooksPath)) {
      const hooks = fs.readFileSync(hooksPath, 'utf8');
      const stillNeeded = hooks.includes('SessionStart') && !hooks.includes(CODEX_HOOK_MARKER);
      // Also check for any non-cave-tools SessionStart hook.
      const otherHooksPresent = hooks.includes('SessionStart') && (
        hooks.includes('CAVEMAN') || hooks.includes('caveman') ||
        /"command"\s*:\s*"echo[^"]*caveman/i.test(hooks));
      const shouldFlip = /^\s*hooks\s*=\s*true\s*$/m.test(cfg) && !stillNeeded && !otherHooksPresent;
      if (shouldFlip) {
        const nextCfg = cfg.replace(/^(\s*)hooks\s*=\s*true\s*$/m, '$1hooks = false');
        backupOnce(configPath);
        if (!DRY_RUN) {
          fs.writeFileSync(configPath, nextCfg, { mode: 0o644 });
          trackPath(configPath, 'update');
          log(`updated: ${configPath} [features] hooks = false (no other hooks remain)`);
        } else {
          trackPath(configPath, 'update');
          log(`dry-run: would flip hooks = true → false in ${configPath}`);
        }
      }
    }
  }

  // 2. Remove AGENTS.md fenced block.
  removeBlock(agentsMd);

  // 3. Remove our hook entry from hooks.json (merge-safe), or delete file if
  //    only ours remains.
  if (fs.existsSync(hooksPath)) {
    const hooks = fs.readFileSync(hooksPath, 'utf8');
    if (hooks.includes(CODEX_HOOK_MARKER)) {
      try {
        const parsed = JSON.parse(hooks);
        // Codex nests events under a "hooks" wrapper: { hooks: { SessionStart: [...] } }.
        // Strip our hook from parsed.hooks.SessionStart (the correct location).
        // Also clean up any stray root-level SessionStart left by older buggy
        // installs that wrote it at the root.
        for (const root of [parsed.hooks, parsed]) {
          if (!root || !Array.isArray(root.SessionStart)) continue;
          root.SessionStart = root.SessionStart
            .map((g) => g && Array.isArray(g.hooks) ? {
              ...g,
              hooks: g.hooks.filter((h) => !(typeof h?.command === 'string' && h.command.includes(CODEX_HOOK_MARKER))),
            } : g)
            .filter((g) => !g || !Array.isArray(g.hooks) || g.hooks.length > 0);
          if (root.SessionStart.length === 0) delete root.SessionStart;
        }
        // If the root-level SessionStart (the buggy one) is now empty/absent and
        // hooks is empty too, the file is vestigial — delete it.
        const hooksEmpty = !parsed.hooks || Object.keys(parsed.hooks).length === 0;
        const rootKeys = Object.keys(parsed).filter((k) => k !== 'hooks');
        if (hooksEmpty && rootKeys.length === 0) {
          // Only our hook was there — delete the file.
          if (DRY_RUN) {
            trackPath(hooksPath, 'remove');
            log(`dry-run: would delete ${hooksPath} (only cave-tools hook present)`);
          } else {
            fs.unlinkSync(hooksPath);
            trackPath(hooksPath, 'remove');
            log(`removed: ${hooksPath} (only cave-tools hook present)`);
          }
        } else {
          const merged = JSON.stringify(parsed, null, 2) + '\n';
          backupOnce(hooksPath);
          if (DRY_RUN) {
            trackPath(hooksPath, 'update');
            log(`dry-run: would strip cave-tools hook from ${hooksPath}`);
          } else {
            fs.writeFileSync(hooksPath, merged, { mode: 0o644 });
            trackPath(hooksPath, 'update');
            log(`updated: ${hooksPath} stripped cave-tools hook`);
          }
        }
      } catch (_) {
        log(`skip (unparseable): ${hooksPath} — remove manually`);
      }
    } else {
      log(`unchanged: ${hooksPath} no cave-tools hook`);
    }
  }
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
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(claudeConfigDir(), 'CLAUDE.md'));
  }

  if (keys.has('codex')) {
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(HOME, '.codex', 'AGENTS.md'));
    removeCodex();
  }
  if (keys.has('opencode')) {
    removeOpencodeMcp();
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(opencodeConfigDir(), 'AGENTS.md'));
  }
  if (keys.has('grok')) {
    removeGrok();
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(grokConfigDir(), 'AGENTS.md'));
  }
  if (keys.has('zcode')) {
    removeZcodeMcp();
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(zcodeConfigDir(), 'AGENTS.md'));
  }

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
    if (WITH_EXTRA_RULES) {
      removeDisciplineBlock(path.join(HOME, '.gemini', 'AGENTS.md'));
      removeDisciplineBlock(path.join(HOME, '.gemini', 'antigravity-cli', 'AGENTS.md'));
      removeDisciplineBlock(path.join(HOME, '.gemini', 'antigravity-ide', 'agents', 'cave-discipline.md'));
    }
  }
  if (keys.has('kiro')) {
    removeKiroGeneratedRule();
    if (WITH_EXTRA_RULES) removeDisciplineBlock(path.join(HOME, '.kiro', 'steering', 'cave-discipline.md'));
  }
  if (keys.has('cursor') && WITH_EXTRA_RULES) {
    // Cursor discipline rule is a standalone .mdc — delete the whole file.
    const discPath = path.join(HOME, '.cursor', 'rules', 'cave-discipline.mdc');
    if (fs.existsSync(discPath)) {
      const content = fs.readFileSync(discPath, 'utf8');
      if (content.includes(DISCIPLINE_MARKER_BEGIN)) {
        backupOnce(discPath);
        if (DRY_RUN) {
          trackPath(discPath, 'remove');
          log(`dry-run: would remove ${discPath}`);
        } else {
          fs.unlinkSync(discPath);
          trackPath(discPath, 'remove');
          log(`removed: ${discPath}`);
        }
      } else {
        log(`unchanged: ${discPath} (no discipline block)`);
      }
    }
  }
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
