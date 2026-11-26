import { execFile } from 'node:child_process';
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, readSync, statSync, writeFileSync } from 'node:fs';
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

export function discoverLogDirs(primaryDataDir) {
  const explicit = (process.env.PLUGIN_DATA || '').trim();
  if (explicit) {
    const dir = join(explicit, 'logs');
    return existsSync(dir) ? [dir] : [];
  }
  const home = homedir();
  const candidates = [
    primaryDataDir ? join(primaryDataDir, 'logs') : '',
    join(home, '.gemini', 'antigravity-cli', 'plugin_data', 'agy-ptc', 'logs'),
    join(home, '.gemini', 'antigravity', 'plugin_data', 'agy-ptc', 'logs'),
    join(home, '.gemini', 'config', 'plugin_data', 'agy-ptc', 'logs'),
    join(home, '.gemini', 'cache', 'agy-ptc', 'logs'),
  ].filter(Boolean);

  const seen = new Set();
  const out = [];
  for (const c of candidates) {
    const key = process.platform === 'win32' ? resolve(c).toLowerCase() : resolve(c);
    if (seen.has(key)) continue;
    seen.add(key);
    try {
      if (statSync(c).isDirectory()) out.push(c);
    } catch {}
  }
  return out;
}

function resolveBinary(pluginRoot) {
  if (process.env.AGY_PTC_BIN && existsSync(process.env.AGY_PTC_BIN)) {
    return process.env.AGY_PTC_BIN;
  }
  const localBin = join(pluginRoot, 'scripts', BIN_NAME);
  if (existsSync(localBin)) return localBin;
  const dataBin = join(resolveDataDir(), 'bin', BIN_NAME);
  if (existsSync(dataBin)) return dataBin;
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

const jsonlTailCache = new Map();

// Streams only the trailing `<= maxBytes` slice from disk and memoizes by (mtimeMs, size)
// so 2.5s UI polling performs 0 disk reads and 0 JSON parses while idle.
export function readJsonlEntries(filePath, maxBytes = 4 * 1024 * 1024) {
  try {
    if (!existsSync(filePath)) return [];
    const st = statSync(filePath);
    if (st.size === 0) return [];

    const cached = jsonlTailCache.get(filePath);
    if (cached && cached.mtimeMs === st.mtimeMs && cached.size === st.size && cached.maxBytes === maxBytes) {
      return cached.entries;
    }

    const offset = Math.max(0, st.size - maxBytes);
    const len = st.size - offset;
    const buf = Buffer.allocUnsafe(len);
    const fd = openSync(filePath, 'r');
    let bytesRead = 0;
    try {
      while (bytesRead < len) {
        const n = readSync(fd, buf, bytesRead, len - bytesRead, offset + bytesRead);
        if (n <= 0) break;
        bytesRead += n;
      }
    } finally {
      closeSync(fd);
    }

    let raw = buf.subarray(0, bytesRead).toString('utf-8');
    if (offset > 0) {
      const nl = raw.indexOf('\n');
      raw = nl >= 0 ? raw.slice(nl + 1) : '';
    }

    const entries = [];
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        entries.push(JSON.parse(trimmed));
      } catch {}
    }

    if (jsonlTailCache.size >= 16 && !jsonlTailCache.has(filePath)) {
      const oldestKey = jsonlTailCache.keys().next().value;
      if (oldestKey) jsonlTailCache.delete(oldestKey);
    }
    jsonlTailCache.set(filePath, { mtimeMs: st.mtimeMs, size: st.size, maxBytes, entries });
    return entries;
  } catch {
    return [];
  }
}

function loadMergedEntries(primaryDataDir, errorsOnly = false) {
  const fileName = errorsOnly ? 'errors.jsonl' : 'executions.jsonl';
  const dirs = discoverLogDirs(primaryDataDir);
  if (dirs.length === 0) return [];
  if (dirs.length === 1) {
    return readJsonlEntries(join(dirs[0], fileName));
  }
  const seen = new Set();
  const merged = [];
  for (const d of dirs) {
    for (const e of readJsonlEntries(join(d, fileName))) {
      const key = e.id ? `${e.id}:${e.timestamp || ''}` : '';
      if (key && seen.has(key)) continue;
      if (key) seen.add(key);
      merged.push(e);
    }
  }
  merged.sort((a, b) => String(a.timestamp || '').localeCompare(String(b.timestamp || '')));
  return merged;
}

export function computeStats(entries) {
  const stats = {
    totalRuns: entries.length,
    okRuns: 0,
    partialRuns: 0,
    errorRuns: 0,
    spilledRuns: 0,
    totalOps: 0,
    opsPerRun: 0,
    avgDurationMs: 0,
    categories: {},
    failingCapabilities: [],
  };
  let totalDur = 0;
  const capTotal = {};
  const capFail = {};
  const capSample = {};

  for (const e of entries) {
    totalDur += Number(e.durationMs || 0);
    stats.totalOps += Number(e.hostOps || 0);
    if (e.autoSpilled) stats.spilledRuns++;

    if (e.status === 'ok' && !Number(e.failedOps || 0)) {
      stats.okRuns++;
    } else if (e.status === 'partial_op_error') {
      stats.partialRuns++;
    } else {
      stats.errorRuns++;
    }

    if (e.errorCategory) {
      stats.categories[e.errorCategory] = (stats.categories[e.errorCategory] || 0) + 1;
    } else if (e.status && e.status !== 'ok') {
      stats.categories[e.status] = (stats.categories[e.status] || 0) + 1;
    }

    if (Array.isArray(e.traces)) {
      for (const tr of e.traces) {
        const cap = tr.capability || tr.op || '';
        if (!cap) continue;
        capTotal[cap] = (capTotal[cap] || 0) + 1;
        const isTraceErr = tr.status ? tr.status === 'error' : tr.ok === false;
        if (isTraceErr) {
          capFail[cap] = (capFail[cap] || 0) + 1;
          if (!capSample[cap] && tr.error) {
            capSample[cap] = String(tr.error).slice(0, 110);
          }
        }
      }
    }
  }

  if (entries.length > 0) {
    stats.avgDurationMs = Math.round(totalDur / entries.length);
    stats.opsPerRun = Number((stats.totalOps / entries.length).toFixed(1));
  }

  stats.failingCapabilities = Object.keys(capFail)
    .sort((a, b) => capFail[b] - capFail[a] || a.localeCompare(b))
    .slice(0, 8)
    .map(cap => ({
      capability: cap,
      failed: capFail[cap],
      total: capTotal[cap] || capFail[cap],
      sample: capSample[cap] || '',
    }));

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
    const execEntries = loadMergedEntries(dataDir, false);
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

    let entries = loadMergedEntries(dataDir, errorsOnly);
    if (q) {
      entries = entries.filter(e => {
        const caps = Array.isArray(e.traces) ? e.traces.map(tr => tr.capability || tr.op || '').join(' ') : '';
        const hay = `${e.id || ''} ${e.title || ''} ${e.status || ''} ${e.errorCategory || ''} ${e.errorMessage || ''} ${e.cwd || ''} ${caps}`.toLowerCase();
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
    for (const logsDir of discoverLogDirs(dataDir)) {
      for (const name of ['executions.jsonl', 'errors.jsonl', 'executions.jsonl.1', 'errors.jsonl.1']) {
        const p = join(logsDir, name);
        if (existsSync(p)) writeFileSync(p, '', 'utf-8');
        jsonlTailCache.delete(p);
      }
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
