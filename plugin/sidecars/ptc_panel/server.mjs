import { execFile } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildUiHtml } from './ui.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const BIN_NAME = process.platform === 'win32' ? 'agy-ptc.exe' : 'agy-ptc';

function installEpipeGuard(proc = process) {
  const isEpipe = err => err?.code === 'EPIPE';
  for (const stream of [proc.stdout, proc.stderr]) {
    stream?.on?.('error', err => {
      if (!isEpipe(err)) throw err;
    });
  }
  const fatal = err => {
    if (isEpipe(err)) return;
    try {
      console.error('[agy-ptc-panel] fatal:', err);
    } catch {}
    proc.exit(1);
  };
  proc.on('uncaughtException', fatal);
  proc.on('unhandledRejection', fatal);
}

function resolvePluginRoot() {
  const envRoot = (process.env.PLUGIN_ROOT || '').trim();
  if (envRoot) return envRoot;
  return resolve(__dirname, '..', '..');
}

function resolveDataDir() {
  const envData = (process.env.PLUGIN_DATA || '').trim();
  if (envData) return envData;
  const home = homedir();
  const candidates = [
    join(home, '.gemini', 'antigravity-cli', 'plugin_data', 'agy-ptc'),
    join(home, '.gemini', 'antigravity', 'plugin_data', 'agy-ptc'),
    join(home, '.gemini', 'config', 'plugin_data', 'agy-ptc'),
  ];
  for (const dir of candidates) {
    if (existsSync(join(dir, 'logs'))) return dir;
  }
  for (const dir of candidates) {
    if (existsSync(dir)) return dir;
  }
  return join(home, '.gemini', 'cache', 'agy-ptc');
}

function resolveBinary(pluginRoot) {
  const localBin = join(pluginRoot, 'scripts', BIN_NAME);
  if (existsSync(localBin)) return localBin;
  return BIN_NAME;
}

function readJsonSafe(filePath, fallback = null) {
  try {
    if (!existsSync(filePath)) return fallback;
    const raw = readFileSync(filePath, 'utf-8');
    return JSON.parse(raw);
  } catch {
    return fallback;
  }
}

function readJsonlEntries(filePath, maxBytes = 4 * 1024 * 1024) {
  try {
    if (!existsSync(filePath)) return [];
    const st = statSync(filePath);
    let raw = readFileSync(filePath, 'utf-8');
    if (st.size > maxBytes) {
      const idx = raw.indexOf('\n', raw.length - maxBytes);
      raw = idx >= 0 ? raw.slice(idx + 1) : raw;
    }
    const entries = [];
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        entries.push(JSON.parse(trimmed));
      } catch {}
    }
    return entries;
  } catch {
    return [];
  }
}

function computeStats(entries) {
  const stats = {
    totalRuns: entries.length,
    okRuns: 0,
    errorRuns: 0,
    spilledRuns: 0,
    avgDurationMs: 0,
    categories: {},
  };
  let totalDur = 0;
  for (const e of entries) {
    totalDur += Number(e.durationMs || 0);
    if (e.autoSpilled) stats.spilledRuns++;
    const isErr = e.status !== 'ok' || Number(e.failedOps || 0) > 0;
    if (isErr) {
      stats.errorRuns++;
      const cat = e.errorCategory || e.status || 'unknown';
      stats.categories[cat] = (stats.categories[cat] || 0) + 1;
    } else {
      stats.okRuns++;
    }
  }
  if (entries.length > 0) {
    stats.avgDurationMs = Math.round(totalDur / entries.length);
  }
  return stats;
}

function resolvePolicyPath(dataDir) {
  if ((process.env.PLUGIN_DATA || '').trim()) {
    return join(dataDir, 'policy.json');
  }
  return join(homedir(), '.gemini', 'config', 'agy-ptc', 'policy.json');
}

function readPolicy(dataDir) {
  const primary = resolvePolicyPath(dataDir);
  const globalDesktop = join(homedir(), '.gemini', 'config', 'agy-ptc', 'policy.json');
  const cliSettings = join(homedir(), '.gemini', 'antigravity-cli', 'settings.json');

  const merged = {
    allowNonWorkspaceAccess: true,
    permissions: { allow: [], ask: [], deny: [] },
    policyFile: primary,
  };

  for (const p of [cliSettings, globalDesktop, primary]) {
    const cfg = readJsonSafe(p);
    if (!cfg || typeof cfg !== 'object') continue;
    if (typeof cfg.allowNonWorkspaceAccess === 'boolean') {
      merged.allowNonWorkspaceAccess = cfg.allowNonWorkspaceAccess;
    }
  }

  const local = readJsonSafe(primary) || readJsonSafe(globalDesktop) || {};
  if (typeof local.allowNonWorkspaceAccess === 'boolean') {
    merged.allowNonWorkspaceAccess = local.allowNonWorkspaceAccess;
  }
  if (typeof local.agyPtcTrustWorkspaceMcp === 'boolean') {
    merged.agyPtcTrustWorkspaceMcp = local.agyPtcTrustWorkspaceMcp;
  }
  const perms = local.permissions || {};
  merged.permissions = {
    allow: Array.isArray(perms.allow) ? perms.allow : [],
    ask: Array.isArray(perms.ask) ? perms.ask : [],
    deny: Array.isArray(perms.deny) ? perms.deny : [],
  };
  return merged;
}

function writePolicy(dataDir, payload) {
  const target = resolvePolicyPath(dataDir);
  mkdirSync(dirname(target), { recursive: true });
  const existing = readJsonSafe(target);
  const base = existing && typeof existing === 'object' && !Array.isArray(existing) ? existing : {};
  const normList = arr =>
    Array.isArray(arr)
      ? arr.map(s => String(s ?? '').trim()).filter(Boolean)
      : [];
  const next = {
    ...base,
    allowNonWorkspaceAccess: payload?.allowNonWorkspaceAccess !== false,
    permissions: {
      allow: normList(payload?.permissions?.allow),
      ask: normList(payload?.permissions?.ask),
      deny: normList(payload?.permissions?.deny),
    },
  };
  if (typeof payload?.agyPtcTrustWorkspaceMcp === 'boolean') {
    next.agyPtcTrustWorkspaceMcp = payload.agyPtcTrustWorkspaceMcp;
  }
  writeFileSync(target, JSON.stringify(next, null, 2) + '\n', 'utf-8');
  return { ok: true, policyFile: target, ...next };
}

function runCliCommand(binPath, args, { env = process.env, timeoutMs = 15000 } = {}) {
  return new Promise(resolvePromise => {
    const started = Date.now();
    execFile(
      binPath,
      args,
      {
        encoding: 'utf-8',
        env,
        windowsHide: true,
        timeout: timeoutMs,
        maxBuffer: 2 * 1024 * 1024,
      },
      (err, stdout, stderr) => {
        const out = [stdout, stderr].filter(Boolean).join('\n').trim();
        resolvePromise({
          ok: !err,
          durationMs: Date.now() - started,
          output: out || (err ? err.message : ''),
        });
      },
    );
  });
}

installEpipeGuard();

const pluginRoot = resolvePluginRoot();
const dataDir = resolveDataDir();
const binPath = resolveBinary(pluginRoot);

if (process.env.ANTIGRAVITY_SIDECAR_WEB_PORT) {
  const { SidecarApp } = await import('sidecar_sdk');
  const app = new SidecarApp();
  const uiHtml = buildUiHtml();

  app.page('/', () => uiHtml);
  app.page('/ui.html', () => uiHtml);

  app.api('/api/overview', () => {
    const liveStatus = readJsonSafe(join(dataDir, 'live-status.json'), {
      active: false,
      totalRuns: 0,
      totalHostOps: 0,
      lastDurationMs: 0,
      lastStatus: 'idle',
      updatedAt: '',
    });
    const execEntries = readJsonlEntries(join(dataDir, 'logs', 'executions.jsonl'));
    const stats = computeStats(execEntries);
    const policy = readPolicy(dataDir);
    return {
      liveStatus,
      stats,
      policy,
      env: {
        dataDir,
        pluginRoot,
        binPath,
        binExists: existsSync(binPath),
      },
    };
  }, 'GET');

  app.api('/api/logs', (_data, req) => {
    const url = new URL(req?.url || '/api/logs', 'http://localhost');
    const errorsOnly = url.searchParams.get('errorsOnly') === '1';
    const limit = Math.min(200, Math.max(1, Number(url.searchParams.get('limit') || 50)));
    const q = (url.searchParams.get('q') || '').trim().toLowerCase();

    const logFile = join(dataDir, 'logs', errorsOnly ? 'errors.jsonl' : 'executions.jsonl');
    let entries = readJsonlEntries(logFile);
    if (q) {
      entries = entries.filter(e => {
        const hay = `${e.id || ''} ${e.title || ''} ${e.status || ''} ${e.errorMessage || ''} ${e.cwd || ''}`.toLowerCase();
        return hay.includes(q);
      });
    }
    const recent = entries.slice(-limit).reverse();
    return {
      total: entries.length,
      entries: recent,
    };
  }, 'GET');

  app.api('/api/logs/clear', () => {
    const logsDir = join(dataDir, 'logs');
    mkdirSync(logsDir, { recursive: true });
    for (const name of ['executions.jsonl', 'errors.jsonl']) {
      const p = join(logsDir, name);
      if (existsSync(p)) writeFileSync(p, '', 'utf-8');
    }
    return { ok: true };
  }, 'POST');

  app.api('/api/policy', () => readPolicy(dataDir), 'GET');
  app.api('/api/policy', data => writePolicy(dataDir, data), 'POST');

  app.api('/api/doctor', async () => {
    return await runCliCommand(binPath, ['doctor', '--plugin-dir', pluginRoot], {
      timeoutMs: 15000,
    });
  }, 'POST');

  app.api('/api/bench', async () => {
    return await runCliCommand(binPath, ['bench', '--recent', '15'], {
      timeoutMs: 15000,
    });
  }, 'POST');

  await app.run();

  const shutdown = async () => {
    try {
      await app?.close?.();
    } finally {
      process.exit(0);
    }
  };
  for (const sig of ['SIGTERM', 'SIGINT']) {
    process.once(sig, () => void shutdown());
  }
}
