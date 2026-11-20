export function buildUiHtml() {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>PTC Code Mode</title>
  <script src="/preload.js"></script>
  <style>
    :root {
      color-scheme: dark light;
      --ptc-bg: var(--background, #141416);
      --ptc-fg: var(--foreground, #ececef);
      --ptc-dim: color-mix(in srgb, var(--ptc-fg) 68%, var(--ptc-bg));
      --ptc-surface: color-mix(in srgb, var(--ptc-fg) 5%, var(--ptc-bg));
      --ptc-surface-2: color-mix(in srgb, var(--ptc-fg) 8%, var(--ptc-bg));
      --ptc-border: color-mix(in srgb, var(--ptc-fg) 15%, transparent);
      --ptc-border-strong: color-mix(in srgb, var(--ptc-fg) 26%, transparent);
      --ptc-green: #4ade80;
      --ptc-amber: #f59e0b;
      --ptc-red: #f87171;
      --ptc-blue: #60a5fa;
      --ptc-mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      padding: 0;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      font-size: 12.5px;
      line-height: 1.45;
      background: var(--ptc-bg);
      color: var(--ptc-fg);
    }
    .app { padding: 12px; }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      margin-bottom: 12px;
      flex-wrap: wrap;
    }
    .tabs {
      display: flex;
      gap: 4px;
      background: var(--ptc-surface);
      padding: 3px;
      border-radius: 7px;
      border: 1px solid var(--ptc-border);
    }
    .tab {
      border: 0;
      background: transparent;
      color: var(--ptc-dim);
      padding: 5px 10px;
      border-radius: 5px;
      cursor: pointer;
      font-size: 12px;
      font-weight: 500;
    }
    .tab:hover {
      color: var(--ptc-fg);
      background: color-mix(in srgb, var(--ptc-fg) 7%, transparent);
    }
    .tab.active {
      background: color-mix(in srgb, var(--ptc-fg) 14%, transparent);
      color: var(--ptc-fg);
      font-weight: 600;
    }
    .status-pill {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 11.5px;
      color: var(--ptc-dim);
    }
    .dot {
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background: var(--ptc-dim);
      display: inline-block;
    }
    .dot.ok { background: var(--ptc-green); }
    .dot.active { background: var(--ptc-amber); animation: pulse 1.1s ease-in-out infinite; }
    .dot.err { background: var(--ptc-red); }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }

    .grid-4 {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 6px;
      margin-bottom: 10px;
    }
    .stat-card {
      background: var(--ptc-surface);
      border: 1px solid var(--ptc-border);
      border-radius: 7px;
      padding: 8px 9px;
    }
    .stat-label { font-size: 11px; color: var(--ptc-dim); }
    .stat-val { font-size: 15px; font-weight: 600; margin-top: 2px; font-variant-numeric: tabular-nums; }

    .card {
      background: var(--ptc-surface);
      border: 1px solid var(--ptc-border);
      border-radius: 8px;
      padding: 10px 12px;
      margin-bottom: 8px;
    }
    .row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }
    .toolbar {
      display: flex;
      align-items: center;
      gap: 6px;
      margin-bottom: 8px;
      flex-wrap: wrap;
    }
    input[type="text"], textarea {
      width: 100%;
      background: color-mix(in srgb, var(--ptc-fg) 4%, var(--ptc-bg));
      color: var(--ptc-fg);
      border: 1px solid var(--ptc-border-strong);
      border-radius: 6px;
      padding: 5px 8px;
      font-size: 12px;
      outline: none;
    }
    textarea {
      font-family: var(--ptc-mono);
      font-size: 11.5px;
      min-height: 68px;
      resize: vertical;
    }
    .btn {
      border: 1px solid var(--ptc-border-strong);
      background: var(--ptc-surface-2);
      color: var(--ptc-fg);
      border-radius: 6px;
      padding: 4px 10px;
      font-size: 12px;
      cursor: pointer;
      white-space: nowrap;
    }
    .btn:hover { background: color-mix(in srgb, var(--ptc-fg) 14%, var(--ptc-bg)); }
    .btn.primary {
      border-color: color-mix(in srgb, var(--ptc-blue) 55%, transparent);
      background: color-mix(in srgb, var(--ptc-blue) 18%, transparent);
      color: var(--ptc-blue);
    }
    .btn.danger {
      border-color: color-mix(in srgb, var(--ptc-red) 45%, transparent);
      color: var(--ptc-red);
    }
    .btn.active-filter {
      border-color: color-mix(in srgb, var(--ptc-amber) 55%, transparent);
      background: color-mix(in srgb, var(--ptc-amber) 15%, transparent);
      color: var(--ptc-amber);
    }
    .badge {
      display: inline-flex;
      align-items: center;
      padding: 1px 6px;
      border-radius: 999px;
      font-size: 10.5px;
      font-weight: 600;
      border: 1px solid var(--ptc-border);
    }
    .badge.ok { color: var(--ptc-green); border-color: color-mix(in srgb, var(--ptc-green) 40%, transparent); background: color-mix(in srgb, var(--ptc-green) 10%, transparent); }
    .badge.err { color: var(--ptc-red); border-color: color-mix(in srgb, var(--ptc-red) 40%, transparent); background: color-mix(in srgb, var(--ptc-red) 10%, transparent); }
    .badge.warn { color: var(--ptc-amber); border-color: color-mix(in srgb, var(--ptc-amber) 40%, transparent); background: color-mix(in srgb, var(--ptc-amber) 10%, transparent); }

    .log-item {
      background: var(--ptc-surface);
      border: 1px solid var(--ptc-border);
      border-radius: 7px;
      margin-bottom: 6px;
      overflow: hidden;
    }
    .log-head {
      padding: 8px 10px;
      cursor: pointer;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }
    .log-head:hover { background: var(--ptc-surface-2); }
    .log-title {
      font-weight: 500;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .log-meta {
      font-size: 11px;
      color: var(--ptc-dim);
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
    }
    .log-body {
      border-top: 1px solid var(--ptc-border);
      padding: 8px 10px;
      background: color-mix(in srgb, var(--ptc-fg) 2%, var(--ptc-bg));
    }
    pre {
      margin: 4px 0 8px 0;
      padding: 8px;
      border-radius: 6px;
      background: color-mix(in srgb, var(--ptc-bg) 70%, #000);
      border: 1px solid var(--ptc-border);
      font-family: var(--ptc-mono);
      font-size: 11px;
      line-height: 1.45;
      overflow-x: auto;
      white-space: pre-wrap;
      word-break: break-word;
      max-height: 240px;
    }
    .sec-label { font-size: 11px; font-weight: 600; color: var(--ptc-dim); margin-top: 6px; }
    .muted { color: var(--ptc-dim); font-size: 11.5px; }
    .switch-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      padding: 4px 0;
    }
    .empty {
      text-align: center;
      padding: 24px 12px;
      color: var(--ptc-dim);
    }
  </style>
</head>
<body>
  <div class="app">
    <div class="topbar">
      <div class="tabs">
        <button class="tab active" data-tab="logs" id="t-tab-logs"></button>
        <button class="tab" data-tab="policy" id="t-tab-policy"></button>
        <button class="tab" data-tab="diag" id="t-tab-diag"></button>
      </div>
      <div class="status-pill">
        <span id="live-dot" class="dot"></span>
        <span id="live-text"></span>
        <button class="btn" id="btn-lang" style="padding:2px 7px;font-size:11px;"></button>
        <button class="btn" id="btn-refresh" style="padding:2px 7px;font-size:11px;"></button>
      </div>
    </div>

    <div id="tab-logs">
      <div class="grid-4">
        <div class="stat-card"><div class="stat-label" id="t-lbl-total"></div><div class="stat-val" id="st-total">0</div></div>
        <div class="stat-card"><div class="stat-label" id="t-lbl-rate"></div><div class="stat-val" id="st-rate">-</div></div>
        <div class="stat-card"><div class="stat-label" id="t-lbl-dur"></div><div class="stat-val" id="st-dur">-</div></div>
        <div class="stat-card"><div class="stat-label" id="t-lbl-spill"></div><div class="stat-val" id="st-spill">0</div></div>
      </div>

      <div class="card" id="latest-card" style="display:none;">
        <div class="row">
          <div style="min-width:0;">
            <div style="font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" id="latest-title">-</div>
            <div class="muted" id="latest-sub">-</div>
          </div>
          <span class="badge" id="latest-badge">-</span>
        </div>
      </div>

      <div class="toolbar">
        <input type="text" id="log-search" style="flex:1;min-width:140px;" />
        <button class="btn" id="btn-errors-only"></button>
        <button class="btn danger" id="btn-clear-logs"></button>
      </div>

      <div id="logs-list"></div>
    </div>

    <div id="tab-policy" style="display:none;">
      <div class="card">
        <div class="switch-row">
          <div>
            <div style="font-weight:600;" id="t-pol-strict-title"></div>
            <div class="muted" id="t-pol-strict-desc"></div>
          </div>
          <input type="checkbox" id="pol-strict" style="width:16px;height:16px;cursor:pointer;" />
        </div>
        <div class="muted" id="pol-file" style="margin-top:6px;word-break:break-all;"></div>
      </div>

      <div class="card">
        <div class="sec-label" style="margin-top:0;" id="t-pol-deny-lbl"></div>
        <textarea id="pol-deny" placeholder="write_file(secrets/)"></textarea>
        <div class="sec-label" id="t-pol-ask-lbl"></div>
        <textarea id="pol-ask" placeholder="command(git push)"></textarea>
        <div class="sec-label" id="t-pol-allow-lbl"></div>
        <textarea id="pol-allow" placeholder="command(git)"></textarea>
        <div class="row" style="margin-top:10px;">
          <span class="muted" id="pol-msg"></span>
          <button class="btn primary" id="btn-save-policy"></button>
        </div>
      </div>
    </div>

    <div id="tab-diag" style="display:none;">
      <div class="card">
        <div class="row">
          <div>
            <div style="font-weight:600;" id="t-diag-cats-title"></div>
            <div class="muted" id="diag-env">-</div>
          </div>
        </div>
        <div id="diag-cats" style="margin-top:8px;"></div>
      </div>

      <div class="card">
        <div class="row">
          <div style="font-weight:600;" id="t-diag-doc-title"></div>
          <button class="btn primary" id="btn-doctor"></button>
        </div>
        <pre id="doctor-out" style="margin-top:8px;"></pre>
      </div>

      <div class="card">
        <div class="row">
          <div style="font-weight:600;" id="t-diag-bench-title"></div>
          <button class="btn primary" id="btn-bench"></button>
        </div>
        <pre id="bench-out" style="margin-top:8px;"></pre>
      </div>
    </div>
  </div>

  <script>
    const I18N = {
      en: {
        tabLogs: 'Executions & Logs',
        tabPolicy: 'Policy & Access',
        tabDiag: 'Diagnostics',
        refresh: 'Refresh',
        loading: 'Loading...',
        ready: 'Ready',
        runningPrefix: 'Running: ',
        recentPrefix: 'Last: ',
        totalRuns: 'Total Runs',
        successRate: 'Success Rate',
        avgDuration: 'Avg Duration',
        autoSpilled: 'State Spills',
        latestFallback: 'Latest PTC Execution',
        latestSub: (runs, ops, ms) => runs + ' runs · ' + ops + ' host ops · ' + ms + 'ms',
        searchPlaceholder: 'Filter by title, status, workspace, or error...',
        errorsOnly: 'Errors Only',
        clear: 'Clear',
        noLogs: 'No execution records available.',
        workspaceLabel: 'Workspace: ',
        spilledTag: ' · Spilled to state',
        codeLabel: 'TypeScript Source',
        tracesLabel: 'Operation Traces',
        resultLabel: 'Result Preview',
        strictTitle: 'Strict Workspace Confinement',
        strictDesc: 'Restrict filesystem access to the active workspace (sets allowNonWorkspaceAccess to false).',
        policyFilePrefix: 'Policy file: ',
        denyLabel: 'Deny Rules (one per line, e.g. write_file(secrets/) or command(rm -rf))',
        askLabel: 'Ask Rules (returns NeedsApproval to escalate to host run_command)',
        allowLabel: 'Allow Rules',
        policyHint: 'Changes apply to the next execution immediately.',
        policySaved: 'Saved. Takes effect on the next execution.',
        policySaveFailed: 'Failed to save policy.',
        savePolicy: 'Save Policy',
        errorHotspots: 'Error Distribution',
        dataDirPrefix: 'Data directory: ',
        noErrors: 'No errors recorded.',
        doctorTitle: 'Environment Diagnostics (Doctor)',
        runDoctor: 'Run Doctor',
        doctorHint: 'Run Doctor to verify MCP handshake, SessionStart hook latency, and ripgrep availability.',
        doctorRunning: 'Running agy-ptc doctor...',
        benchTitle: 'Efficiency Benchmark (Recent 15 Sessions)',
        runBench: 'Run Bench',
        benchHint: 'Run Bench to analyze turn compression and response budget metrics across recent sessions.',
        benchRunning: 'Analyzing recent 15 sessions...',
        langBtn: '中文',
      },
      zh: {
        tabLogs: '执行与日志',
        tabPolicy: '策略与权限',
        tabDiag: '诊断与效率',
        refresh: '刷新',
        loading: '加载中...',
        ready: '就绪',
        runningPrefix: '执行中: ',
        recentPrefix: '最近: ',
        totalRuns: '总执行次数',
        successRate: '成功率',
        avgDuration: '平均耗时',
        autoSpilled: '状态转存',
        latestFallback: '最近一次 PTC 调用',
        latestSub: (runs, ops, ms) => '累计 ' + runs + ' 次 · 宿主操作 ' + ops + ' 次 · 耗时 ' + ms + 'ms',
        searchPlaceholder: '按标题、状态、工作区或错误信息筛选...',
        errorsOnly: '仅错误',
        clear: '清空',
        noLogs: '暂无执行记录。',
        workspaceLabel: '工作区: ',
        spilledTag: ' · 已转存至 state',
        codeLabel: 'TypeScript 源码',
        tracesLabel: '操作追踪',
        resultLabel: '结果预览',
        strictTitle: '严格工作区隔离',
        strictDesc: '限制沙箱仅可访问当前工作区（等效于 allowNonWorkspaceAccess: false）。',
        policyFilePrefix: '配置文件: ',
        denyLabel: 'Deny 规则（每行一条，例如 write_file(secrets/) 或 command(rm -rf)）',
        askLabel: 'Ask 规则（返回 NeedsApproval 并交由宿主 run_command 确认）',
        allowLabel: 'Allow 规则',
        policyHint: '保存后将于下次执行时生效。',
        policySaved: '已保存，将于下次执行时生效。',
        policySaveFailed: '保存策略失败。',
        savePolicy: '保存策略',
        errorHotspots: '错误分类统计',
        dataDirPrefix: '数据目录: ',
        noErrors: '暂无错误记录。',
        doctorTitle: '环境诊断 (Doctor)',
        runDoctor: '运行诊断',
        doctorHint: '运行诊断以检查 MCP 握手、SessionStart 钩子延迟及 ripgrep 可用性。',
        doctorRunning: '正在执行 agy-ptc doctor...',
        benchTitle: '效率分析 (Bench · 最近 15 个会话)',
        runBench: '运行分析',
        benchHint: '分析本地会话的回合压缩率与输出预算命中情况。',
        benchRunning: '正在分析最近 15 个会话...',
        langBtn: 'EN',
      }
    };

    const storedLang = (() => {
      try { return localStorage.getItem('agy-ptc-lang'); } catch { return null; }
    })();
    const defaultLang = storedLang || ((navigator.language || '').toLowerCase().startsWith('zh') ? 'zh' : 'en');

    const state = {
      lang: defaultLang === 'zh' ? 'zh' : 'en',
      tab: 'logs',
      errorsOnly: false,
      expanded: new Set(),
      policyLoaded: false,
      lastOverview: null,
    };

    function t(key) {
      return I18N[state.lang][key];
    }

    function applyStaticI18n() {
      document.documentElement.lang = state.lang === 'zh' ? 'zh-CN' : 'en';
      document.getElementById('t-tab-logs').textContent = t('tabLogs');
      document.getElementById('t-tab-policy').textContent = t('tabPolicy');
      document.getElementById('t-tab-diag').textContent = t('tabDiag');
      document.getElementById('btn-refresh').textContent = t('refresh');
      document.getElementById('btn-lang').textContent = t('langBtn');
      document.getElementById('t-lbl-total').textContent = t('totalRuns');
      document.getElementById('t-lbl-rate').textContent = t('successRate');
      document.getElementById('t-lbl-dur').textContent = t('avgDuration');
      document.getElementById('t-lbl-spill').textContent = t('autoSpilled');
      document.getElementById('log-search').placeholder = t('searchPlaceholder');
      document.getElementById('btn-errors-only').textContent = t('errorsOnly');
      document.getElementById('btn-clear-logs').textContent = t('clear');
      document.getElementById('t-pol-strict-title').textContent = t('strictTitle');
      document.getElementById('t-pol-strict-desc').textContent = t('strictDesc');
      document.getElementById('t-pol-deny-lbl').textContent = t('denyLabel');
      document.getElementById('t-pol-ask-lbl').textContent = t('askLabel');
      document.getElementById('t-pol-allow-lbl').textContent = t('allowLabel');
      document.getElementById('pol-msg').textContent = t('policyHint');
      document.getElementById('btn-save-policy').textContent = t('savePolicy');
      document.getElementById('t-diag-cats-title').textContent = t('errorHotspots');
      document.getElementById('t-diag-doc-title').textContent = t('doctorTitle');
      document.getElementById('btn-doctor').textContent = t('runDoctor');
      document.getElementById('t-diag-bench-title').textContent = t('benchTitle');
      document.getElementById('btn-bench').textContent = t('runBench');

      const docOut = document.getElementById('doctor-out');
      if (!docOut.dataset.hasOutput) docOut.textContent = t('doctorHint');
      const benchOut = document.getElementById('bench-out');
      if (!benchOut.dataset.hasOutput) benchOut.textContent = t('benchHint');
    }

    function esc(s) {
      return String(s ?? '').replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
    }

    async function api(url, method = 'GET', body = undefined) {
      const res = await fetch(url, {
        method,
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
      return await res.json();
    }

    document.querySelectorAll('.tab').forEach(btn => {
      btn.addEventListener('click', () => {
        state.tab = btn.dataset.tab;
        document.querySelectorAll('.tab').forEach(b => b.classList.toggle('active', b === btn));
        document.getElementById('tab-logs').style.display = state.tab === 'logs' ? '' : 'none';
        document.getElementById('tab-policy').style.display = state.tab === 'policy' ? '' : 'none';
        document.getElementById('tab-diag').style.display = state.tab === 'diag' ? '' : 'none';
        if (state.tab === 'policy' && !state.policyLoaded) loadPolicy();
      });
    });

    document.getElementById('btn-lang').addEventListener('click', () => {
      state.lang = state.lang === 'zh' ? 'en' : 'zh';
      try { localStorage.setItem('agy-ptc-lang', state.lang); } catch {}
      applyStaticI18n();
      if (state.lastOverview) renderOverview(state.lastOverview);
      if (state.policyLoaded) {
        const pf = document.getElementById('pol-file').dataset.path || '-';
        document.getElementById('pol-file').textContent = t('policyFilePrefix') + pf;
      }
      refreshLogs();
    });

    function badgeClass(status, failedOps) {
      if (status === 'ok' && !failedOps) return 'ok';
      if (status === 'partial_op_error') return 'warn';
      return 'err';
    }

    function renderOverview(ov) {
      const ls = ov.liveStatus || {};
      const st = ov.stats || {};

      const dot = document.getElementById('live-dot');
      const txt = document.getElementById('live-text');
      dot.className = 'dot ' + (ls.active ? 'active' : (ls.lastStatus && ls.lastStatus !== 'ok' && ls.lastStatus !== 'idle' ? 'err' : 'ok'));
      txt.textContent = ls.active
        ? (t('runningPrefix') + (ls.title || ls.currentOp || 'vm_exec'))
        : (ls.lastStatus && ls.lastStatus !== 'idle' ? (t('recentPrefix') + ls.lastStatus + ' · ' + (ls.lastDurationMs || 0) + 'ms') : t('ready'));

      document.getElementById('st-total').textContent = st.totalRuns ?? 0;
      const rate = st.totalRuns > 0 ? Math.round(((st.okRuns || 0) / st.totalRuns) * 100) + '%' : '-';
      document.getElementById('st-rate').textContent = rate;
      document.getElementById('st-dur').textContent = st.totalRuns > 0 ? ((st.avgDurationMs || 0) + 'ms') : '-';
      document.getElementById('st-spill').textContent = st.spilledRuns ?? 0;

      const latestCard = document.getElementById('latest-card');
      if (ls.title || ls.updatedAt) {
        latestCard.style.display = '';
        document.getElementById('latest-title').textContent = ls.title || t('latestFallback');
        document.getElementById('latest-sub').textContent = t('latestSub')(ls.totalRuns || 0, ls.totalHostOps || 0, ls.lastDurationMs || 0);
        const b = document.getElementById('latest-badge');
        b.textContent = ls.active ? 'running' : (ls.lastStatus || 'idle');
        b.className = 'badge ' + (ls.active ? 'warn' : (ls.lastStatus === 'ok' || ls.lastStatus === 'idle' ? 'ok' : 'err'));
      } else {
        latestCard.style.display = 'none';
      }

      document.getElementById('diag-env').textContent = t('dataDirPrefix') + (ov.env?.dataDir || '-');
      const cats = st.categories || {};
      const catKeys = Object.keys(cats);
      document.getElementById('diag-cats').innerHTML = catKeys.length
        ? catKeys.map(k => '<span class="badge err" style="margin-right:6px;margin-bottom:4px;">' + esc(k) + ': ' + cats[k] + '</span>').join('')
        : '<span class="muted">' + esc(t('noErrors')) + '</span>';
    }

    async function refreshOverview() {
      try {
        const ov = await api('/api/overview');
        state.lastOverview = ov;
        renderOverview(ov);
      } catch {}
    }

    async function refreshLogs() {
      const q = encodeURIComponent(document.getElementById('log-search').value || '');
      const res = await api('/api/logs?errorsOnly=' + (state.errorsOnly ? '1' : '0') + '&limit=50&q=' + q);
      const list = document.getElementById('logs-list');
      const entries = res.entries || [];
      if (!entries.length) {
        list.innerHTML = '<div class="empty">' + esc(t('noLogs')) + '</div>';
        return;
      }
      list.innerHTML = entries.map(e => {
        const open = state.expanded.has(e.id);
        const bc = badgeClass(e.status, e.failedOps);
        const tracesText = Array.isArray(e.traces) && e.traces.length
          ? e.traces.map(tr => (tr.ok ? 'OK  ' : 'ERR ') + (tr.op || '') + ' (' + (tr.durationMs || 0) + 'ms)' + (tr.error ? ' -> ' + tr.error : '')).join('\\n')
          : '';
        return '<div class="log-item">' +
          '<div class="log-head" data-id="' + esc(e.id) + '">' +
            '<div style="display:flex;align-items:center;gap:7px;min-width:0;">' +
              '<span class="badge ' + bc + '">' + esc(e.status || 'unknown') + '</span>' +
              '<span class="log-title">' + esc(e.title || e.id) + '</span>' +
            '</div>' +
            '<div class="log-meta">' + (e.durationMs || 0) + 'ms · ' + (e.hostOps || 0) + ' ops · ' + (e.responseBytes || 0) + 'B</div>' +
          '</div>' +
          (open ? (
            '<div class="log-body">' +
              '<div class="muted">ID: ' + esc(e.id) + ' · ' + esc(t('workspaceLabel')) + esc(e.cwd || '-') + (e.autoSpilled ? esc(t('spilledTag')) : '') + '</div>' +
              (e.errorMessage ? '<div class="sec-label" style="color:var(--ptc-red);">Error (' + esc(e.errorCategory || '') + ')</div><pre>' + esc(e.errorMessage) + '</pre>' : '') +
              (e.code ? '<div class="sec-label">' + esc(t('codeLabel')) + '</div><pre>' + esc(e.code) + '</pre>' : '') +
              (tracesText ? '<div class="sec-label">' + esc(t('tracesLabel')) + '</div><pre>' + esc(tracesText) + '</pre>' : '') +
              (e.resultPreview ? '<div class="sec-label">' + esc(t('resultLabel')) + '</div><pre>' + esc(e.resultPreview) + '</pre>' : '') +
            '</div>'
          ) : '') +
        '</div>';
      }).join('');

      list.querySelectorAll('.log-head').forEach(el => {
        el.addEventListener('click', () => {
          const id = el.dataset.id;
          if (state.expanded.has(id)) state.expanded.delete(id);
          else state.expanded.add(id);
          refreshLogs();
        });
      });
    }

    async function loadPolicy() {
      const p = await api('/api/policy');
      document.getElementById('pol-strict').checked = p.allowNonWorkspaceAccess === false;
      document.getElementById('pol-deny').value = (p.permissions?.deny || []).join('\\n');
      document.getElementById('pol-ask').value = (p.permissions?.ask || []).join('\\n');
      document.getElementById('pol-allow').value = (p.permissions?.allow || []).join('\\n');
      const polFileEl = document.getElementById('pol-file');
      polFileEl.dataset.path = p.policyFile || '-';
      polFileEl.textContent = t('policyFilePrefix') + (p.policyFile || '-');
      state.policyLoaded = true;
    }

    document.getElementById('btn-save-policy').addEventListener('click', async () => {
      const splitLines = id => document.getElementById(id).value.split(/\\r?\\n/).map(s => s.trim()).filter(Boolean);
      const res = await api('/api/policy', 'POST', {
        allowNonWorkspaceAccess: !document.getElementById('pol-strict').checked,
        permissions: {
          deny: splitLines('pol-deny'),
          ask: splitLines('pol-ask'),
          allow: splitLines('pol-allow'),
        },
      });
      document.getElementById('pol-msg').textContent = res.ok ? t('policySaved') : t('policySaveFailed');
    });

    document.getElementById('btn-errors-only').addEventListener('click', (ev) => {
      state.errorsOnly = !state.errorsOnly;
      ev.currentTarget.classList.toggle('active-filter', state.errorsOnly);
      refreshLogs();
    });

    document.getElementById('btn-clear-logs').addEventListener('click', async () => {
      await api('/api/logs/clear', 'POST', {});
      state.expanded.clear();
      refreshAll();
    });

    document.getElementById('log-search').addEventListener('input', () => refreshLogs());
    document.getElementById('btn-refresh').addEventListener('click', () => refreshAll());

    document.getElementById('btn-doctor').addEventListener('click', async () => {
      const out = document.getElementById('doctor-out');
      out.dataset.hasOutput = '1';
      out.textContent = t('doctorRunning');
      const res = await api('/api/doctor', 'POST', {});
      out.textContent = res.output || (res.ok ? 'OK' : 'Failed');
    });

    document.getElementById('btn-bench').addEventListener('click', async () => {
      const out = document.getElementById('bench-out');
      out.dataset.hasOutput = '1';
      out.textContent = t('benchRunning');
      const res = await api('/api/bench', 'POST', {});
      out.textContent = res.output || (res.ok ? 'OK' : 'Failed');
    });

    async function refreshAll() {
      await Promise.all([refreshOverview(), refreshLogs()]);
    }

    applyStaticI18n();
    document.getElementById('live-text').textContent = t('loading');
    refreshAll();
    setInterval(refreshAll, 2500);
  </script>
</body>
</html>`;
}
