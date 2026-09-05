// ============================================================
// analysis.js — модуль «Анализ & Метрики»:
// 1. Покрытие API (Coverage): анализ эндпоинтов OpenAPI против
//    реестра тестов и кастомных сценариев, список непокрытых ручек.
// 2. Интеллектуальный анализ сбоев (Root Cause Analysis / RCA):
//    автоматическая диагностика причин падений с рекомендациями.
// 3. Метрики качества и производительности: Pass Rate, p50/p95,
//    flaky-тесты и самые медленные проверки.
// 4. Модалка автопополнения сценариев (Smoke, CRUD, RBAC, Negative).
// ============================================================

let currentCoverageReport = null;
let currentQualityMetrics = null;
let currentDiagnosis = null;
let currentMobileReport = null;
let analysisFilterUncovered = '';
let mobileFilterStatus = 'all'; // all | crash | warn | ok
let mobileFilterSearch = '';

// ---------- Загрузка состояния ----------

async function loadAnalysis() {
  const container = document.getElementById('view-analysis');
  if (!container) return;

  const fetchSafe = async (url) => {
    try {
      const res = await fetch(url);
      return res.ok ? await res.json() : null;
    } catch (e) {
      console.warn('Analysis fetch failed for ' + url, e);
      return null;
    }
  };

  const [cov, met, diag] = await Promise.all([
    fetchSafe('/api/analysis/coverage'),
    fetchSafe('/api/analysis/metrics'),
    fetchSafe('/api/analysis/diagnostics'),
  ]);

  if (cov) currentCoverageReport = cov;
  if (met) currentQualityMetrics = met;
  if (diag) currentDiagnosis = diag;

  renderCoverageView();
  renderDiagnosticsView();
  renderMetricsView();
}

async function loadMobileContract() {
  const container = document.getElementById('view-mobile');
  if (!container) return;

  try {
    const [mobRes, cfgRes, appsRes] = await Promise.all([
      fetch('/api/mobilecontract/report'),
      fetch('/api/mobilecontract/gitlab/config'),
      fetch('/api/mobilecontract/apps'),
    ]);
    if (mobRes.ok) currentMobileReport = await mobRes.json();
    if (appsRes.ok) {
      const appsData = await appsRes.json();
      mobileAppsList = appsData.apps || [];
    }
    if (cfgRes.ok) {
      const glCfg = await cfgRes.json();
      if (glCfg.repoUrl) gitlabRepoUrl = glCfg.repoUrl;
      if (glCfg.token) gitlabToken = glCfg.token;
      if (glCfg.lastBranch) gitlabDefaultBranch = glCfg.lastBranch;
    }
  } catch (err) {
    console.warn('Failed to fetch mobile report:', err);
  }
  renderMobileContractView();
}

// ---------- 1. РЕНДЕР ПОКРЫТИЯ API ----------

function renderCoverageView() {
  const box = document.getElementById('coverageContainer');
  if (!box) return;

  const rep = currentCoverageReport;
  if (!rep || rep.totalEndpoints === 0) {
    box.innerHTML = `
      <div class="p-8 text-center text-slate-400 border border-dashed border-darkborder rounded-xl">
        <i class="fa-solid fa-book-open text-3xl mb-3 text-slate-500"></i>
        <p class="text-sm font-semibold text-slate-300">Спецификация API не импортирована</p>
        <p class="text-xs text-zinc-500 mt-1 max-w-md mx-auto">
          Для расчёта покрытия эндпоинтов и автогенерации тестов импортируйте OpenAPI/Swagger спеку во вкладке «Управление &amp; Инструменты → Спецификация API».
        </p>
        <button onclick="switchTab('spec')" class="mt-4 px-4 py-2 text-xs font-semibold bg-white hover:bg-zinc-200 text-zinc-950 rounded-lg shadow-sm transition">
          <i class="fa-solid fa-arrow-right mr-1.5"></i>Перейти к импорту спеки
        </button>
      </div>
    `;
    return;
  }

  const pct = Math.round(rep.coveragePercent || 0);
  const covered = rep.coveredEndpoints || 0;
  const total = rep.totalEndpoints || 0;
  const uncoveredCount = rep.uncovered ? rep.uncovered.length : 0;

  let colorClass = 'text-zinc-100';
  let barColor = 'bg-white';

  // Method breakdown badges
  const methodPills = Object.entries(rep.byMethod || {}).map(([m, data]) => {
    const mpct = Math.round(data.percentage || 0);
    return `
      <div class="bg-zinc-900 border border-darkborder rounded-lg p-2.5 flex items-center justify-between">
        <span class="text-xs font-semibold text-zinc-200 font-mono">${escapeHtml(m)}</span>
        <div class="text-right">
          <span class="text-xs font-semibold text-white">${data.covered}/${data.total}</span>
          <span class="text-[10px] text-zinc-500 ml-1">(${mpct}%)</span>
        </div>
      </div>
    `;
  }).join('');

  // Role breakdown pills
  const rolePills = Object.entries(rep.byRole || {}).map(([r, data]) => {
    const rpct = Math.round(data.percentage || 0);
    return `
      <div class="bg-zinc-900 border border-darkborder rounded-lg p-2.5 flex items-center justify-between">
        <span class="text-xs font-medium text-zinc-300 capitalize">${escapeHtml(r)}</span>
        <div class="text-right">
          <span class="text-xs font-semibold text-white">${data.covered}/${data.total}</span>
          <span class="text-[10px] text-zinc-500 ml-1">(${rpct}%)</span>
        </div>
      </div>
    `;
  }).join('');

  box.innerHTML = `
    <div class="bg-darkcard border border-darkborder rounded-xl p-5 space-y-5">
      <!-- Header row -->
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h3 class="text-base font-semibold text-white flex items-center gap-2">
            <i class="fa-solid fa-chart-pie text-zinc-400"></i>
            <span>Покрытие спецификации API</span>
          </h3>
          <p class="text-xs text-zinc-400 mt-0.5">
            Соотношение описанных в OpenAPI эндпоинтов и проверяющих их сценариев
          </p>
        </div>
        <div class="flex items-center gap-2">
          <button onclick="openReplenishModal(true)" title="Сгенерировать тесты только для непокрытых эндпоинтов"
            class="px-3 py-1.5 bg-white hover:bg-zinc-200 text-zinc-950 font-semibold text-xs rounded-lg shadow-sm transition flex items-center gap-1.5">
            <i class="fa-solid fa-play text-xs"></i><span>Пополнить непокрытые (${uncoveredCount})</span>
          </button>
          <button onclick="openReplenishModal(false)" title="Открыть мастер автопополнения сценариев"
            class="px-3 py-1.5 bg-zinc-900 hover:bg-zinc-800 text-zinc-200 border border-darkborder font-medium text-xs rounded-lg transition flex items-center gap-1.5">
            <i class="fa-solid fa-wand-magic-sparkles text-zinc-400"></i><span>Мастер тестов…</span>
          </button>
        </div>
      </div>

      <!-- Main coverage metric bar -->
      <div class="bg-zinc-900 border border-darkborder rounded-xl p-4 flex flex-col md:flex-row items-center gap-6">
        <div class="text-center md:text-left shrink-0">
          <div class="text-4xl font-bold ${colorClass}">${pct}%</div>
          <div class="text-[11px] text-zinc-400 mt-0.5">${covered} из ${total} эндпоинтов покрыто</div>
        </div>
        <div class="flex-1 w-full space-y-2">
          <div class="w-full bg-zinc-950 h-2.5 rounded-full overflow-hidden p-0.5 border border-darkborder">
            <div class="${barColor} h-full rounded-full transition-all duration-500" style="width: ${pct}%"></div>
          </div>
          <div class="flex items-center justify-between text-[11px] text-zinc-400">
            <span>Покрыто сценариями: <strong class="text-white">${covered}</strong></span>
            <span>Непокрыто: <strong class="${uncoveredCount ? 'text-zinc-200' : 'text-zinc-500'}">${uncoveredCount}</strong></span>
          </div>
        </div>
      </div>

      <!-- Breakdown grids -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <span class="text-[10px] font-semibold text-zinc-400 uppercase tracking-wider block mb-2">По HTTP методам</span>
          <div class="grid grid-cols-2 gap-2">
            ${methodPills}
          </div>
        </div>
        <div>
          <span class="text-[10px] font-semibold text-zinc-400 uppercase tracking-wider block mb-2">По ролям доступа</span>
          <div class="grid grid-cols-2 gap-2">
            ${rolePills}
          </div>
        </div>
      </div>

      <!-- Uncovered Endpoints Explorer -->
      <div class="pt-2 border-t border-darkborder space-y-3">
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
          <span class="text-xs font-semibold text-zinc-200 flex items-center gap-1.5">
            <i class="fa-solid fa-triangle-exclamation text-zinc-400"></i>
            <span>Непокрытые эндпоинты (${uncoveredCount})</span>
          </span>
          <div class="relative w-full sm:w-64">
            <input type="text" id="uncoveredSearchInput" placeholder="Поиск по пути или тегу…"
              value="${escapeHtml(analysisFilterUncovered)}"
              oninput="filterUncoveredEndpoints(this.value)"
              class="w-full bg-zinc-900 border border-darkborder rounded-lg pl-8 pr-3 py-1.5 text-xs text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500">
            <i class="fa-solid fa-magnifying-glass absolute left-2.5 top-2.5 text-zinc-500 text-xs"></i>
          </div>
        </div>

        <div class="overflow-x-auto rounded-lg border border-darkborder max-h-72 overflow-y-auto">
          <table class="w-full text-left text-xs text-zinc-300">
            <thead class="bg-zinc-900 text-[10px] uppercase font-semibold text-zinc-400 sticky top-0 z-10 border-b border-darkborder">
              <tr>
                <th class="px-3 py-2">Метод</th>
                <th class="px-3 py-2">Путь</th>
                <th class="px-3 py-2">Роль</th>
                <th class="px-3 py-2">Тег</th>
                <th class="px-3 py-2 text-right">Действие</th>
              </tr>
            </thead>
            <tbody id="uncoveredTableBody" class="divide-y divide-darkborder bg-zinc-900/40">
              ${renderUncoveredRows(rep.uncovered || [])}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  `;
}

function renderUncoveredRows(uncoveredList) {
  if (!uncoveredList || uncoveredList.length === 0) {
    return `<tr><td colspan="5" class="px-3 py-4 text-center text-slate-500 italic">Все эндпоинты спецификации покрыты тестами!</td></tr>`;
  }

  const query = (analysisFilterUncovered || '').toLowerCase().trim();
  const filtered = uncoveredList.filter(ep => {
    if (!query) return true;
    return ep.path.toLowerCase().includes(query) ||
      ep.method.toLowerCase().includes(query) ||
      (ep.tag && ep.tag.toLowerCase().includes(query)) ||
      (ep.summary && ep.summary.toLowerCase().includes(query));
  });

  if (filtered.length === 0) {
    return `<tr><td colspan="5" class="px-3 py-4 text-center text-slate-500 italic">Ничего не найдено по фильтру «${escapeHtml(query)}»</td></tr>`;
  }

  return filtered.map(ep => {
    const m = ep.method.toUpperCase();

    return `
      <tr class="hover:bg-zinc-800/40 transition">
        <td class="px-3 py-2 font-mono">
          <span class="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-zinc-800 text-zinc-200 border border-zinc-700">${escapeHtml(m)}</span>
        </td>
        <td class="px-3 py-2 font-mono text-zinc-200">
          <div>${escapeHtml(ep.path)}</div>
          ${ep.summary ? `<div class="text-[10px] text-zinc-400 font-sans truncate max-w-xs">${escapeHtml(ep.summary)}</div>` : ''}
        </td>
        <td class="px-3 py-2 text-zinc-400 capitalize">${escapeHtml(ep.role || 'client')}</td>
        <td class="px-3 py-2 text-zinc-400">${escapeHtml(ep.tag || '—')}</td>
        <td class="px-3 py-2 text-right">
          <button onclick="replenishSingleEndpoint('${escAttr(ep.method)}', '${escAttr(ep.path)}', '${escAttr(ep.tag)}')"
            title="Автоматически создать сценарий для этой ручки"
            class="px-2 py-1 text-[10px] font-medium bg-white hover:bg-zinc-200 text-zinc-950 rounded transition shadow-sm">
            <i class="fa-solid fa-plus mr-1"></i>Создать тест
          </button>
        </td>
      </tr>
    `;
  }).join('');
}

function filterUncoveredEndpoints(query) {
  analysisFilterUncovered = query || '';
  const tbody = document.getElementById('uncoveredTableBody');
  if (!tbody || !currentCoverageReport) return;
  tbody.innerHTML = renderUncoveredRows(currentCoverageReport.uncovered || []);
}

// ---------- 2. РЕНДЕР RCA ДИАГНОСТИКИ СБОЕВ ----------

function renderDiagnosticsView() {
  const box = document.getElementById('diagnosticsContainer');
  if (!box) return;

  const diag = currentDiagnosis;
  if (!diag) {
    box.innerHTML = `
      <div class="bg-darkcard border border-darkborder rounded-xl p-5 text-center text-slate-400">
        <p class="text-xs">Данные диагностики отсутствуют.</p>
      </div>
    `;
    return;
  }

  // Populate runs dropdown if runs are cached
  let runsOptions = '<option value="">Последний завершённый сбой</option>';
  if (Array.isArray(historyCache) && historyCache.length > 0) {
    runsOptions += historyCache.map(r => {
      const isFailed = r.status === 'FAILED';
      const statusTag = isFailed ? '[СБОЙ]' : r.status === 'PASSED' ? '[OK]' : '[ПРОПУСК]';
      const sel = diag.runId === r.id ? 'selected' : '';
      return `<option value="${escAttr(r.id)}" ${sel}>${statusTag} [${fmtAbsTime(r.startTime)}] ${escapeHtml(r.suiteName || r.suiteKey)}</option>`;
    }).join('');
  }

  if (diag.category === 'HEALTHY') {
    box.innerHTML = `
      <div class="bg-darkcard border border-darkborder rounded-xl p-5 space-y-4">
        <div class="flex items-center justify-between">
          <h3 class="text-base font-semibold text-white flex items-center gap-2">
            <i class="fa-solid fa-stethoscope text-zinc-400"></i>
            <span>Интеллектуальная диагностика сбоев (RCA)</span>
          </h3>
          <select onchange="changeDiagnosticsRun(this.value)" class="bg-zinc-900 border border-darkborder rounded-lg px-2 py-1 text-xs text-zinc-300 focus:outline-none">
            ${runsOptions}
          </select>
        </div>
        <div class="p-6 text-center border border-green-500/20 bg-green-500/5 rounded-xl space-y-2">
          <i class="fa-solid fa-circle-check text-green-400 text-3xl"></i>
          <h4 class="text-sm font-bold text-green-300">${escapeHtml(diag.title)}</h4>
          <p class="text-xs text-slate-400 max-w-md mx-auto">${escapeHtml(diag.description)}</p>
        </div>
      </div>
    `;
    return;
  }

  // Failed / Analyzed state
  let sevBadge = 'bg-zinc-800 text-zinc-300 border-zinc-700';
  if (diag.severity === 'CRITICAL') sevBadge = 'bg-red-950/40 text-red-300 border-red-800/40';

  box.innerHTML = `
    <div class="bg-darkcard border border-darkborder rounded-xl p-5 space-y-4">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h3 class="text-base font-semibold text-white flex items-center gap-2">
            <i class="fa-solid fa-stethoscope text-zinc-400"></i>
            <span>Интеллектуальная диагностика сбоев (Root Cause Analysis)</span>
          </h3>
          <p class="text-xs text-zinc-400 mt-0.5">Автоматический разбор причин падений и план устранения</p>
        </div>
        <select onchange="changeDiagnosticsRun(this.value)" class="bg-zinc-900 border border-darkborder rounded-lg px-2.5 py-1.5 text-xs text-zinc-300 focus:outline-none">
          ${runsOptions}
        </select>
      </div>

      <!-- Problem Card -->
      <div class="bg-zinc-900 border border-darkborder rounded-xl p-4 space-y-3">
        <div class="flex items-center gap-2 flex-wrap">
          <span class="px-2 py-0.5 text-[10px] font-mono font-medium uppercase rounded border ${sevBadge}">
            ВАЖНОСТЬ: ${escapeHtml(diag.severity)}
          </span>
          <span class="px-2 py-0.5 text-[10px] font-mono font-medium rounded bg-zinc-800 text-zinc-200 border border-zinc-700">
            ${escapeHtml(diag.category)}
          </span>
          ${diag.httpStatus ? `<span class="px-2 py-0.5 text-[10px] font-mono font-medium rounded bg-zinc-800 text-zinc-300 border border-zinc-700">HTTP ${diag.httpStatus}</span>` : ''}
          <span class="text-xs text-zinc-400 ml-auto font-medium">Сьют: <strong class="text-zinc-200">${escapeHtml(diag.suiteName || diag.suiteKey)}</strong></span>
        </div>

        <h4 class="text-sm font-semibold text-white leading-snug">${escapeHtml(diag.title)}</h4>
        <p class="text-xs text-zinc-300 leading-relaxed">${escapeHtml(diag.description)}</p>

        <!-- Root cause & recommendation blocks -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-3 pt-2">
          <div class="bg-zinc-950 border border-zinc-800 rounded-lg p-3 space-y-1">
            <span class="text-[10px] font-semibold text-zinc-400 uppercase tracking-wider flex items-center gap-1.5">
              <i class="fa-solid fa-magnifying-glass"></i>Первопричина сбоя (Root Cause)
            </span>
            <p class="text-xs text-zinc-300 leading-relaxed">${escapeHtml(diag.rootCause)}</p>
          </div>
          <div class="bg-zinc-950 border border-zinc-800 rounded-lg p-3 space-y-1">
            <span class="text-[10px] font-semibold text-zinc-400 uppercase tracking-wider flex items-center gap-1.5">
              <i class="fa-regular fa-lightbulb"></i>Как исправить (Рекомендация)
            </span>
            <p class="text-xs text-zinc-300 leading-relaxed">${escapeHtml(diag.recommendation)}</p>
          </div>
        </div>

        <!-- Technical details snippet -->
        ${diag.errorDetail ? `
          <div class="pt-2 border-t border-darkborder">
            <span class="text-[10px] font-semibold text-zinc-500 uppercase tracking-wider block mb-1">Технические детали:</span>
            <pre class="bg-black/60 border border-darkborder rounded-lg p-2.5 text-[11px] font-mono text-zinc-300 overflow-x-auto whitespace-pre-wrap">${escapeHtml(diag.errorDetail)}</pre>
          </div>
        ` : ''}
      </div>
    </div>
  `;
}

async function changeDiagnosticsRun(runId) {
  try {
    const url = runId ? `/api/analysis/diagnostics/${encodeURIComponent(runId)}` : '/api/analysis/diagnostics';
    const res = await fetch(url);
    if (res.ok) {
      currentDiagnosis = await res.json();
      renderDiagnosticsView();
    }
  } catch (err) {
    showToast('Не удалось загрузить диагностику: ' + err.message, 'error');
  }
}

// ---------- 3. РЕНДЕР МЕТРИК И ПРОИЗВОДИТЕЛЬНОСТИ ----------

function renderMetricsView() {
  const box = document.getElementById('metricsContainer');
  if (!box) return;

  const m = currentQualityMetrics;
  if (!m || m.totalRuns === 0) {
    box.innerHTML = `
      <div class="bg-darkcard border border-darkborder rounded-xl p-5 text-center text-slate-400">
        <p class="text-xs">История прогонов пуста — запустите сьюты для сбора метрик производительности.</p>
      </div>
    `;
    return;
  }

  const passPct = Math.round(m.passRate || 0);

  // Slowest steps table
  const slowestRows = (m.slowestSteps || []).map(st => `
    <tr class="hover:bg-zinc-800/40 transition">
      <td class="px-3 py-2 font-mono text-zinc-200">${escapeHtml(st.checkId)}</td>
      <td class="px-3 py-2 text-zinc-400">${escapeHtml(st.suiteKey)}</td>
      <td class="px-3 py-2 font-semibold text-zinc-200">${st.avgDurationMs} мс</td>
      <td class="px-3 py-2 text-zinc-400">${st.maxDurationMs} мс</td>
      <td class="px-3 py-2 text-right text-zinc-400 font-mono">${st.executions}</td>
    </tr>
  `).join('');

  // Flaky suites rows
  const flakyRows = (m.flakySuites || []).map(fl => `
    <div class="bg-zinc-900 border border-darkborder rounded-lg p-2.5 flex items-center justify-between">
      <div>
        <span class="text-xs font-semibold text-white">${escapeHtml(fl.suiteName || fl.suiteKey)}</span>
        <div class="text-[10px] text-zinc-400">Успешно: ${fl.passed}, упало: ${fl.failed} из ${fl.total} прогонов</div>
      </div>
      <span class="px-2 py-0.5 rounded bg-zinc-800 text-zinc-300 border border-zinc-700 text-xs font-mono font-medium">
        ${Math.round(fl.flakinessRatio)}% нестабильность
      </span>
    </div>
  `).join('');

  box.innerHTML = `
    <div class="bg-darkcard border border-darkborder rounded-xl p-5 space-y-5">
      <div>
        <h3 class="text-base font-semibold text-white flex items-center gap-2">
          <i class="fa-solid fa-gauge text-zinc-400"></i>
          <span>Метрики качества и производительности</span>
        </h3>
        <p class="text-xs text-zinc-400 mt-0.5">Агрегированные данные прогонов, тайминги и выявление нестабильных сьютов</p>
      </div>

      <!-- 4 stat cards -->
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <div class="stat-card">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Pass Rate</span>
          <div class="text-2xl font-bold text-white mt-1">${passPct}%</div>
          <div class="text-[10px] text-zinc-500">${m.passedRuns} из ${m.totalRuns} успешных</div>
        </div>
        <div class="stat-card">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Среднее время</span>
          <div class="text-2xl font-bold text-zinc-200 mt-1">${m.avgDurationMs} мс</div>
          <div class="text-[10px] text-zinc-500">по всем прогонам</div>
        </div>
        <div class="stat-card">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Медиана p50</span>
          <div class="text-2xl font-bold text-zinc-200 mt-1">${m.p50DurationMs} мс</div>
          <div class="text-[10px] text-zinc-500">50% выполняются быстрее</div>
        </div>
        <div class="stat-card">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Перцентиль p95</span>
          <div class="text-2xl font-bold text-zinc-200 mt-1">${m.p95DurationMs} мс</div>
          <div class="text-[10px] text-zinc-500">хвост задержек (95%)</div>
        </div>
      </div>

      <!-- Flaky alerts & Slowest checks -->
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-4 pt-2">
        <div class="space-y-2">
          <span class="text-xs font-semibold text-zinc-200 flex items-center gap-1.5">
            <i class="fa-solid fa-shuffle text-zinc-400"></i>
            <span>Нестабильные тесты (Flaky Tests)</span>
          </span>
          <div class="space-y-2">
            ${flakyRows || '<p class="text-xs text-zinc-500 italic py-2">Нестабильных тестов не обнаружено — все сьюты дают повторяемый результат.</p>'}
          </div>
        </div>

        <div class="space-y-2">
          <span class="text-xs font-semibold text-zinc-200 flex items-center gap-1.5">
            <i class="fa-solid fa-stopwatch text-zinc-400"></i>
            <span>Самые медленные шаги (Top Latency)</span>
          </span>
          <div class="overflow-x-auto rounded-lg border border-darkborder max-h-48 overflow-y-auto">
            <table class="w-full text-left text-xs text-zinc-300">
              <thead class="bg-zinc-900 text-[10px] uppercase font-semibold text-zinc-400 sticky top-0 border-b border-darkborder">
                <tr>
                  <th class="px-3 py-1.5">Шаг/Проверка</th>
                  <th class="px-3 py-1.5">Сьют</th>
                  <th class="px-3 py-1.5">Ср. время</th>
                  <th class="px-3 py-1.5">Макс</th>
                  <th class="px-3 py-1.5 text-right">Кол-во</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-darkborder bg-zinc-900/40">
                ${slowestRows || '<tr><td colspan="5" class="px-3 py-3 text-center text-zinc-500 italic">Нет данных</td></tr>'}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  `;
}

// ---------- 4. ВАЛИДАЦИЯ КОНТРАКТОВ МОБИЛЬНЫХ DTO (Flutter, iOS, Android) ----------

let mobileSourceMode = 'gitlab'; // gitlab | local
let gitlabBranchesCache = [];
let gitlabDefaultBranch = '';
let gitlabRepoUrl = 'https://lokaligitlabru.ru/app/locali-director-flutter.git';
let gitlabToken = '';
let localRepoPath = '/locali_director';
let mobileAppsList = [];
let selectedMobileAppId = 'all';
let mobileFilterApp = 'all';

async function loadMobileApps() {
  try {
    const res = await fetch('/api/mobilecontract/apps');
    if (res.ok) {
      const data = await res.json();
      mobileAppsList = data.apps || [];
      if (!selectedMobileAppId && mobileAppsList.length > 0) {
        selectedMobileAppId = mobileAppsList[0].id;
      }
    }
  } catch (err) {
    console.warn('Failed to load mobile apps:', err);
  }
}

function setMobileFilterApp(appName) {
  mobileFilterApp = appName || 'all';
  if (mobileFilterApp === 'all') {
    selectedMobileAppId = 'all';
  } else {
    const app = mobileAppsList.find(a => a.name === mobileFilterApp || a.id === mobileFilterApp);
    if (app) selectedMobileAppId = app.id;
  }
  renderMobileContractView();
}
window.setMobileFilterApp = setMobileFilterApp;

function selectMobileApp(id) {
  selectedMobileAppId = id || 'all';
  if (selectedMobileAppId === 'all') {
    mobileFilterApp = 'all';
  } else {
    const app = mobileAppsList.find(a => a.id === selectedMobileAppId);
    if (app) mobileFilterApp = app.name;
  }
  renderMobileContractView();
}
window.selectMobileApp = selectMobileApp;

function openAddMobileAppModal() {
  document.getElementById('mobileAppEditId').value = '';
  document.getElementById('mobileAppModalTitle').textContent = 'Добавить мобильное приложение';
  document.getElementById('mobileAppName').value = '';
  document.getElementById('mobileAppPlatform').value = 'flutter';
  document.getElementById('mobileAppRepoUrl').value = '';
  document.getElementById('mobileAppBranch').value = 'main';
  document.getElementById('mobileAppToken').value = gitlabToken || '';
  document.getElementById('mobileAppLocalPath').value = '';
  toggleModalSourceType('gitlab');
  document.getElementById('mobileAppModal').classList.remove('hidden');
}

function openEditMobileAppModal(id) {
  const app = mobileAppsList.find(a => a.id === id);
  if (!app) return;
  document.getElementById('mobileAppEditId').value = app.id;
  document.getElementById('mobileAppModalTitle').textContent = 'Редактирование: ' + app.name;
  document.getElementById('mobileAppName').value = app.name || '';
  document.getElementById('mobileAppPlatform').value = app.platform || 'flutter';
  document.getElementById('mobileAppRepoUrl').value = app.repoUrl || '';
  document.getElementById('mobileAppBranch').value = app.branch || 'main';
  document.getElementById('mobileAppToken').value = app.token || '';
  document.getElementById('mobileAppLocalPath').value = app.localPath || '';
  toggleModalSourceType(app.sourceType || 'gitlab');
  document.getElementById('mobileAppModal').classList.remove('hidden');
}

function closeMobileAppModal() {
  const m = document.getElementById('mobileAppModal');
  if (m) m.classList.add('hidden');
}

function toggleModalSourceType(type) {
  const gl = document.getElementById('modalGitlabFields');
  const loc = document.getElementById('modalLocalFields');
  const isGit = type === 'gitlab';
  if (gl) gl.classList.toggle('hidden', !isGit);
  if (loc) loc.classList.toggle('hidden', isGit);
  document.querySelectorAll('input[name="mobileAppSourceType"]').forEach(r => {
    r.checked = r.value === type;
  });
}

async function saveMobileApp(btn) {
  const editId = document.getElementById('mobileAppEditId').value.trim();
  const name = document.getElementById('mobileAppName').value.trim();
  const platform = document.getElementById('mobileAppPlatform').value;
  const sourceType = (document.querySelector('input[name="mobileAppSourceType"]:checked') || {}).value || 'gitlab';
  const repoUrl = document.getElementById('mobileAppRepoUrl').value.trim();
  const branch = document.getElementById('mobileAppBranch').value.trim() || 'main';
  const token = document.getElementById('mobileAppToken').value.trim();
  const localPath = document.getElementById('mobileAppLocalPath').value.trim();

  if (!name) {
    toastError('Укажите название приложения');
    return;
  }
  if (sourceType === 'gitlab' && !repoUrl) {
    toastError('Укажите URL GitLab репозитория');
    return;
  }
  if (sourceType === 'local' && !localPath) {
    toastError('Укажите локальный путь к папке');
    return;
  }

  const payload = {
    name, platform, sourceType, repoUrl, branch, token, localPath,
  };

  await busyWrap(btn, async () => {
    try {
      const url = editId ? `/api/mobilecontract/apps/${encodeURIComponent(editId)}` : '/api/mobilecontract/apps';
      const method = editId ? 'PUT' : 'POST';
      const res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      toastSuccess(editId ? `Приложение "${name}" обновлено.` : `Приложение "${name}" добавлено.`);
      closeMobileAppModal();
      await loadMobileApps();
      renderMobileContractView();
    } catch (err) {
      toastError('Ошибка сохранения приложения: ' + err.message);
    }
  });
}

async function deleteMobileAppClick(id) {
  const app = mobileAppsList.find(a => a.id === id);
  const name = app ? app.name : id;
  const ok = await confirmDialog(`Удалить приложение "${name}" из списка мониторинга контрактов?`);
  if (!ok) return;

  try {
    const res = await fetch(`/api/mobilecontract/apps/${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
    toastSuccess(`Приложение "${name}" удалено.`);
    if (selectedMobileAppId === id) selectedMobileAppId = 'all';
    await loadMobileApps();
    renderMobileContractView();
  } catch (err) {
    toastError('Ошибка удаления: ' + err.message);
  }
}

async function scanSingleMobileApp(id, btn) {
  await busyWrap(btn, async () => {
    try {
      const res = await fetch(`/api/mobilecontract/apps/${encodeURIComponent(id)}/scan`, { method: 'POST' });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      currentMobileReport = await res.json();
      selectedMobileAppId = id;
      toastSuccess(`Приложение проверено: ${currentMobileReport.totalModels} DTO, ${currentMobileReport.crashes} крашей.`);
      await loadMobileApps();
      renderMobileContractView();
    } catch (err) {
      toastError('Ошибка проверки приложения: ' + err.message);
    }
  });
}

async function scanAllMobileApps(btn) {
  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/mobilecontract/apps/scan-all', { method: 'POST' });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      currentMobileReport = await res.json();
      selectedMobileAppId = 'all';
      toastSuccess(`Все мобильные приложения проверены! Всего: ${currentMobileReport.totalModels} DTO, ${currentMobileReport.crashes} крашей.`);
      await loadMobileApps();
      renderMobileContractView();
    } catch (err) {
      toastError('Ошибка проверки приложений: ' + err.message);
    }
  });
}

function setMobileSourceMode(mode) {
  mobileSourceMode = mode;
  renderMobileContractView();
}

function setMobileFilterStatus(status) {
  mobileFilterStatus = status;
  renderMobileContractView();
}

function setMobileFilterPlatform(plat) {
  mobileFilterPlatform = plat;
  renderMobileContractView();
}

function filterMobileSearch(text) {
  mobileFilterSearch = text.toLowerCase().trim();
  renderMobileContractView();
}

function toggleGitlabTokenVisibility() {
  const inp = document.getElementById('mobileGitlabToken');
  const icon = document.getElementById('glTokenEyeIcon');
  if (!inp || !icon) return;
  if (inp.type === 'password') {
    inp.type = 'text';
    icon.classList.remove('fa-eye');
    icon.classList.add('fa-eye-slash');
  } else {
    inp.type = 'password';
    icon.classList.remove('fa-eye-slash');
    icon.classList.add('fa-eye');
  }
}

function renderGitLabBranchOptions() {
  if (gitlabBranchesCache.length === 0) {
    return `<option value="main">main (по умолчанию)</option><option value="master">master</option><option value="dev">dev</option>`;
  }
  return gitlabBranchesCache.map(b => {
    const isDef = b.name === gitlabDefaultBranch || b.default;
    return `<option value="${escAttr(b.name)}" ${isDef ? 'selected' : ''}>${escapeHtml(b.name)}${isDef ? ' (default)' : ''}</option>`;
  }).join('');
}

async function fetchGitLabBranches(btn) {
  const urlInp = document.getElementById('mobileGitlabRepoUrl');
  const tokInp = document.getElementById('mobileGitlabToken');
  const repoUrl = (urlInp ? urlInp.value : '').trim() || gitlabRepoUrl;
  const token = (tokInp ? tokInp.value : '').trim() || gitlabToken;

  gitlabRepoUrl = repoUrl;
  gitlabToken = token;

  const doFetch = async () => {
    try {
      const res = await fetch('/api/mobilecontract/gitlab/branches', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repoUrl, token }),
      });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      const data = await res.json();
      gitlabBranchesCache = data.branches || [];
      gitlabDefaultBranch = data.defaultBranch || 'main';
      toastSuccess(`Загружено веток: ${gitlabBranchesCache.length}`);

      const sel = document.getElementById('mobileGitlabBranchSelect');
      if (sel) sel.innerHTML = renderGitLabBranchOptions();
    } catch (err) {
      toastError('Ошибка загрузки веток из GitLab: ' + err.message);
    }
  };

  if (btn) {
    await busyWrap(btn, doFetch);
  } else {
    await doFetch();
  }
}

async function runGitLabMobileContractScan(btn) {
  const urlInp = document.getElementById('mobileGitlabRepoUrl');
  const tokInp = document.getElementById('mobileGitlabToken');
  const branchSel = document.getElementById('mobileGitlabBranchSelect');

  const repoUrl = (urlInp ? urlInp.value : '').trim() || gitlabRepoUrl;
  const token = (tokInp ? tokInp.value : '').trim() || gitlabToken;
  const branch = (branchSel ? branchSel.value : '').trim() || gitlabDefaultBranch || 'main';

  gitlabRepoUrl = repoUrl;
  gitlabToken = token;

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/mobilecontract/gitlab/scan', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repoUrl, token, branch }),
      });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      currentMobileReport = await res.json();
      toastSuccess(`Ветка ${branch} проверена! Найдено моделей: ${currentMobileReport.totalModels}.`);
      if (typeof logTerminal === 'function') {
        logTerminal('SUCCESS', `Проверена ветка ${branch} из GitLab: ${currentMobileReport.totalModels} DTO, ${currentMobileReport.crashes} рисков крашей.`);
      }
      renderMobileContractView();
    } catch (err) {
      toastError('Ошибка проверки ветки: ' + err.message);
    }
  });
}

let mobileFilterPlatform = 'all';

function copyTextToClipboard(text) {
  if (!text) return;
  navigator.clipboard.writeText(text).then(() => {
    showToast('Скопировано в буфер обмена', 'success');
  }).catch(() => {
    showToast('Не удалось скопировать', 'error');
  });
}
window.copyTextToClipboard = copyTextToClipboard;

function copyMobileReportMarkdown() {
  if (!currentMobileReport || !currentMobileReport.results) {
    showToast('Сначала выполните проверку мобильных контрактов', 'error');
    return;
  }
  const issues = [];
  currentMobileReport.results.forEach(r => {
    (r.issues || []).forEach(iss => {
      issues.push(`| ${r.appName || r.model.name} | ${r.model.name} | \`${iss.fieldName || '-'}\` | ${iss.severity} | ${iss.description} | ${iss.recommendation} |`);
    });
  });
  if (!issues.length) {
    showToast('Дефектов контракта не обнаружено — все модели совместимы!', 'success');
    return;
  }
  const md = `# Отчет о контрактных рисках мобильных DTO (${new Date().toLocaleDateString()})\n\n` +
    `Всего моделей: ${currentMobileReport.totalModels}, Крашей: ${currentMobileReport.crashes}, Предупреждений: ${currentMobileReport.warnings}\n\n` +
    `| Приложение | Модель | Поле | Важность | Описание ошибки | Рекомендация по фиксу |\n` +
    `|---|---|---|---|---|---|\n` +
    issues.join('\n') + `\n`;
  navigator.clipboard.writeText(md).then(() => {
    showToast(`Markdown-отчёт (${issues.length} ${pluralRu(issues.length, ['дефект', 'дефекта', 'дефектов'])}) скопирован!`, 'success');
  }).catch(() => {
    showToast('Ошибка копирования', 'error');
  });
}
window.copyMobileReportMarkdown = copyMobileReportMarkdown;

function renderMobileContractView() {
  const container = document.getElementById('mobileContractMainContainer');
  if (!container) return;

  const rep = currentMobileReport;
  const allResults = (rep && rep.results) || [];
  const scanned = rep ? rep.totalModels : 0;
  const crashes = rep ? rep.crashes : 0;
  const warnings = rep ? rep.warnings : 0;
  const compatible = rep ? rep.compatible : 0;

  // Filter models
  const filtered = allResults.filter(r => {
    if (mobileFilterStatus === 'crash' && r.status !== 'INCOMPATIBLE_CRASH') return false;
    if (mobileFilterStatus === 'warn' && r.status !== 'WARNINGS') return false;
    if (mobileFilterStatus === 'ok' && r.status !== 'COMPATIBLE') return false;

    // Filter by Application Name
    if (mobileFilterApp !== 'all') {
      const matchName = r.appName === mobileFilterApp;
      const targetApp = mobileAppsList.find(a => a.name === mobileFilterApp);
      const matchPlat = targetApp && r.model && r.model.platform === targetApp.platform;
      if (!matchName && !matchPlat) return false;
    }

    if (mobileFilterPlatform !== 'all' && r.model.platform !== mobileFilterPlatform) return false;
    if (mobileFilterSearch) {
      const matchName = (r.model.name || '').toLowerCase().includes(mobileFilterSearch);
      const matchFile = (r.model.filePath || '').toLowerCase().includes(mobileFilterSearch);
      const matchApp = (r.appName || '').toLowerCase().includes(mobileFilterSearch);
      if (!matchName && !matchFile && !matchApp) return false;
    }
    return true;
  });

  // Platform breakdown tags
  const platPills = Object.entries((rep && rep.byPlatform) || {}).map(([p, c]) => {
    const icon = p === 'flutter' ? 'fa-brands fa-flutter text-zinc-400' :
                 p === 'ios' ? 'fa-brands fa-apple text-zinc-400' :
                 'fa-brands fa-android text-zinc-400';
    return `<span class="px-2 py-0.5 rounded bg-zinc-800 border border-darkborder text-[11px] font-medium text-zinc-300 flex items-center gap-1.5"><i class="${icon}"></i><span class="capitalize">${p}</span>: ${c}</span>`;
  }).join(' ');

  // Apps Selector Pills
  const appsPills = [
    `<button onclick="selectMobileApp('all')" class="px-3 py-1.5 rounded-lg text-xs font-semibold transition shrink-0 flex items-center gap-1.5 ${selectedMobileAppId === 'all' ? 'bg-white text-zinc-950 shadow-sm' : 'bg-zinc-800/80 text-zinc-300 hover:text-white border border-darkborder'}">
       <i class="fa-solid fa-layer-group text-xs"></i><span>Все приложения (${scanned})</span>
     </button>`
  ];

  mobileAppsList.forEach(app => {
    const isSelected = selectedMobileAppId === app.id;
    const pIcon = app.platform === 'ios' ? 'fa-brands fa-apple text-zinc-300' :
                  app.platform === 'android' ? 'fa-brands fa-android text-zinc-300' :
                  'fa-brands fa-flutter text-zinc-300';

    let appBadge = '';
    if (app.lastReport) {
      if (app.lastReport.crashes > 0) {
        appBadge = `<span class="px-1.5 py-0.5 rounded text-[10px] font-mono bg-red-950/40 text-red-300 border border-red-800/40"><i class="fa-solid fa-triangle-exclamation mr-1"></i>${app.lastReport.crashes}</span>`;
      } else {
        appBadge = `<span class="px-1.5 py-0.5 rounded text-[10px] font-mono bg-zinc-800 text-zinc-300 border border-zinc-700"><i class="fa-solid fa-check mr-1 text-emerald-400"></i>OK</span>`;
      }
    }

    appsPills.push(`
      <div class="inline-flex items-center rounded-lg ${isSelected ? 'bg-zinc-800 border border-zinc-500 ring-1 ring-zinc-500/40 shadow-sm' : 'bg-zinc-900 border border-darkborder'} overflow-hidden shrink-0 text-xs">
        <button onclick="selectMobileApp('${escAttr(app.id)}')" class="px-3 py-1.5 font-medium ${isSelected ? 'text-white font-semibold' : 'text-zinc-300 hover:text-white'} flex items-center gap-1.5 transition">
          <i class="${pIcon}"></i><span>${escapeHtml(app.name)}</span>${appBadge}
        </button>
        <button onclick="scanSingleMobileApp('${escAttr(app.id)}', this)" title="Проверить это приложение" class="px-2 py-1.5 text-zinc-400 hover:text-white hover:bg-zinc-800 border-l border-darkborder transition">
          <i class="fa-solid fa-play text-[9px]"></i>
        </button>
        <button onclick="openEditMobileAppModal('${escAttr(app.id)}')" title="Редактировать приложение" class="px-2 py-1.5 text-zinc-400 hover:text-white hover:bg-zinc-800 border-l border-darkborder transition">
          <i class="fa-solid fa-gear text-[10px]"></i>
        </button>
        <button onclick="deleteMobileAppClick('${escAttr(app.id)}')" title="Удалить приложение" class="px-2 py-1.5 text-zinc-500 hover:text-red-400 hover:bg-zinc-800 border-l border-darkborder transition">
          <i class="fa-solid fa-xmark text-[10px]"></i>
        </button>
      </div>
    `);
  });

  const activeApp = selectedMobileAppId !== 'all' ? mobileAppsList.find(a => a.id === selectedMobileAppId) : null;

  // Models cards/rows
  let modelsHtml = '';
  if (filtered.length === 0) {
    modelsHtml = `
      <div class="p-6 text-center text-zinc-400 border border-dashed border-darkborder rounded-xl space-y-2">
        <i class="fa-solid fa-mobile-screen text-3xl text-zinc-500 mb-1"></i>
        <p class="text-sm font-semibold text-zinc-300">Нет моделей, удовлетворяющих фильтру</p>
        <p class="text-xs text-zinc-500 max-w-lg mx-auto">
          Всего просканировано: ${scanned} моделей. Попробуйте сбросить фильтры поиска.
        </p>
      </div>
    `;
  } else {
    modelsHtml = filtered.map(m => {
      const mod = m.model;
      const isCrash = m.status === 'INCOMPATIBLE_CRASH';
      const isWarn = m.status === 'WARNINGS';
      const statusBadge = isCrash ?
        '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-semibold status-badge-crash"><i class="fa-solid fa-triangle-exclamation mr-1"></i>РИСК КРАША</span>' :
        isWarn ?
        '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-semibold status-badge-warn"><i class="fa-solid fa-circle-exclamation mr-1"></i>ПРЕДУПРЕЖДЕНИЕ</span>' :
        '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-semibold status-badge-ok"><i class="fa-solid fa-check mr-1 text-emerald-400"></i>СОВМЕСТИМО</span>';

      const platIcon = mod.platform === 'flutter' ? '<i class="fa-brands fa-flutter text-zinc-400 mr-1"></i>Flutter' :
                       mod.platform === 'ios' ? '<i class="fa-brands fa-apple text-zinc-400 mr-1"></i>iOS Swift' :
                       '<i class="fa-brands fa-android text-zinc-400 mr-1"></i>Android Kotlin';

      const appLabel = m.appName ? `<span class="px-2 py-0.5 rounded text-[10px] font-mono app-tag">${escapeHtml(m.appName)}</span>` : '';

      const issuesHtml = (m.issues || []).map(iss => {
        const issClass = iss.severity === 'CRITICAL_CRASH' ? 'mobile-issue-crash' : (iss.severity === 'WARNING' ? 'mobile-issue-warn' : 'mobile-issue-crash');
        const fixText = String(iss.recommendation || '');
        return `
          <div class="${issClass} text-xs space-y-1.5">
            <div class="flex items-center justify-between gap-2">
              <span class="issue-field font-mono font-bold">${escapeHtml(iss.fieldName || '(поле)')} (${escapeHtml(iss.expectedType || 'any')})</span>
              <div class="flex items-center gap-1.5 shrink-0">
                <span class="issue-badge">${escapeHtml(iss.severity)}</span>
                <button onclick="copyTextToClipboard('${escAttr(fixText)}')" title="Скопировать рекомендацию по исправлению" class="px-2 py-0.5 rounded text-[10px] font-medium bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder transition flex items-center gap-1">
                  <i class="fa-regular fa-copy text-[9px]"></i><span>Копировать фикс</span>
                </button>
              </div>
            </div>
            <p class="issue-desc font-medium leading-relaxed">${escapeHtml(iss.description)}</p>
            <div class="issue-rec font-medium"><i class="fa-regular fa-lightbulb mr-1"></i>Рекомендация: ${escapeHtml(iss.recommendation)}</div>
            ${iss.lineNumber ? `<div class="issue-file font-mono">Файл: ${escapeHtml(iss.sourceFile)}:${iss.lineNumber}</div>` : ''}
          </div>
        `;
      }).join('');

      return `
        <details class="mobile-model-card mb-2.5" ${isCrash ? 'open' : ''}>
          <summary>
            <div class="flex items-center gap-2.5 min-w-0 flex-wrap">
              <span class="mobile-model-title truncate font-mono font-bold">${escapeHtml(mod.name)}</span>
              ${appLabel}
              <span class="mobile-model-path flex items-center">${platIcon}</span>
              <span class="mobile-model-path truncate hidden sm:inline">(${escapeHtml(mod.filePath)})</span>
            </div>
            <div class="flex items-center gap-2.5 shrink-0">
              ${statusBadge}
              <span class="text-xs font-mono font-semibold ${isCrash ? 'text-red-400' : 'text-zinc-300'}">${Math.round(m.matchRatio)}%</span>
              <i class="fa-solid fa-chevron-down chev text-xs text-zinc-500"></i>
            </div>
          </summary>
          <div class="mobile-model-body space-y-2.5">
            <div class="text-xs font-semibold mobile-section-header mb-1">Поля модели (${mod.fields ? mod.fields.length : 0}) и проверка совместимости:</div>
            ${issuesHtml || '<p class="text-xs text-emerald-400 font-medium italic">Все поля полностью совместимы с контрактом бэкенда. Ошибок декодирования нет.</p>'}
          </div>
        </details>
      `;
    }).join('');
  }

  const html = `
    <div class="bg-darkcard border border-darkborder rounded-xl p-5 space-y-5">
      <!-- Header -->
      <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-3">
        <div>
          <h3 class="text-base font-semibold text-white flex items-center gap-2">
            <i class="fa-solid fa-mobile-screen text-zinc-400"></i>
            <span>Валидация контрактов мобильных DTO</span>
            <span class="px-2 py-0.5 text-[10px] font-mono font-medium rounded bg-zinc-900 text-zinc-400 border border-darkborder">${mobileAppsList.length} ${pluralRu(mobileAppsList.length, ['мобилка', 'мобилки', 'мобилок'])}</span>
          </h3>
          <p class="text-xs text-zinc-400 mt-0.5">
            Контрактное тестирование моделей Flutter, iOS Swift и Android Kotlin против API бэкенда
          </p>
        </div>
        <div class="flex items-center gap-2 flex-wrap">
          <button onclick="copyMobileReportMarkdown()" title="Скопировать отчёт обо всех выявленных рисках в Markdown для задач Jira / GitLab" class="px-3 py-1.5 text-xs bg-zinc-900 hover:bg-zinc-800 text-zinc-200 border border-darkborder rounded-lg font-medium flex items-center gap-1.5 transition shadow-sm">
            <i class="fa-solid fa-copy text-zinc-400"></i><span>Скопировать отчёт</span>
          </button>
          <button onclick="openAddMobileAppModal()" class="px-3 py-1.5 text-xs bg-zinc-900 hover:bg-zinc-800 text-zinc-200 border border-zinc-700 rounded-lg font-semibold flex items-center gap-1.5 transition">
            <i class="fa-solid fa-plus text-zinc-400"></i><span>Добавить мобилку</span>
          </button>
          <button onclick="scanAllMobileApps(this)" class="px-3.5 py-1.5 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm flex items-center gap-1.5 transition">
            <i class="fa-solid fa-play text-xs"></i><span>Проверить все мобилки</span>
          </button>
        </div>
      </div>

      <!-- App Switcher Bar -->
      <div class="flex items-center gap-2 overflow-x-auto pb-1 pt-0.5 border-b border-darkborder">
        ${appsPills.join('')}
      </div>

      <!-- Source Switcher: GitLab vs Local or Active App -->
      ${activeApp ? `
      <div class="bg-zinc-900 border border-darkborder rounded-xl p-4 flex flex-col sm:flex-row items-center justify-between gap-3">
        <div class="flex items-center gap-3 min-w-0">
          <div class="w-9 h-9 rounded-lg bg-zinc-800 border border-darkborder flex items-center justify-center text-zinc-300 text-sm shrink-0">
            <i class="${activeApp.platform === 'ios' ? 'fa-brands fa-apple' : activeApp.platform === 'android' ? 'fa-brands fa-android' : 'fa-brands fa-flutter'}"></i>
          </div>
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <h4 class="text-sm font-semibold text-white truncate">${escapeHtml(activeApp.name)}</h4>
              <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-400 border border-darkborder uppercase">${escapeHtml(activeApp.platform)}</span>
            </div>
            <p class="text-xs text-zinc-400 font-mono truncate mt-0.5">
              ${activeApp.sourceType === 'gitlab' ? `<i class="fa-brands fa-gitlab text-zinc-400 mr-1"></i>${escapeHtml(activeApp.repoUrl)} [ветка: ${escapeHtml(activeApp.branch || 'main')}]` : `<i class="fa-solid fa-folder-open text-zinc-400 mr-1"></i>${escapeHtml(activeApp.localPath)}`}
            </p>
          </div>
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <button onclick="scanSingleMobileApp('${escAttr(activeApp.id)}', this)" class="px-3.5 py-1.5 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm flex items-center gap-1.5 transition">
            <i class="fa-solid fa-play text-xs"></i><span>Проверить сейчас</span>
          </button>
          <button onclick="openEditMobileAppModal('${escAttr(activeApp.id)}')" class="px-2.5 py-1.5 text-xs bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder rounded-lg transition">
            <i class="fa-solid fa-gear"></i>
          </button>
          <button onclick="deleteMobileAppClick('${escAttr(activeApp.id)}')" class="px-2.5 py-1.5 text-xs bg-zinc-800 hover:bg-red-950/60 text-red-400 border border-darkborder rounded-lg transition">
            <i class="fa-solid fa-trash"></i>
          </button>
        </div>
      </div>
      ` : `
      <div class="bg-zinc-900 border border-darkborder rounded-xl p-4 space-y-3">
        <div class="flex items-center justify-between border-b border-darkborder pb-2.5">
          <div class="flex items-center gap-2">
            <span class="text-xs font-semibold text-zinc-300">Быстрое сканирование:</span>
            <div class="inline-flex rounded-lg p-0.5 bg-zinc-950 border border-darkborder">
              <button onclick="setMobileSourceMode('gitlab')" class="px-3 py-1 rounded text-xs font-semibold transition flex items-center gap-1.5 ${mobileSourceMode === 'gitlab' ? 'bg-white text-zinc-950 shadow-sm' : 'text-zinc-400 hover:text-white'}">
                <i class="fa-brands fa-gitlab text-zinc-400"></i><span>GitLab Репозиторий</span>
              </button>
              <button onclick="setMobileSourceMode('local')" class="px-3 py-1 rounded text-xs font-semibold transition flex items-center gap-1.5 ${mobileSourceMode === 'local' ? 'bg-white text-zinc-950 shadow-sm' : 'text-zinc-400 hover:text-white'}">
                <i class="fa-solid fa-folder-open text-zinc-400"></i><span>Локальная папка</span>
              </button>
            </div>
          </div>
          ${mobileSourceMode === 'gitlab' ? `<span class="text-[11px] text-zinc-500 hidden sm:inline">Поддерживает self-hosted GitLab и gitlab.com</span>` : ''}
        </div>

        ${mobileSourceMode === 'gitlab' ? `
        <!-- GitLab Form -->
        <div class="grid grid-cols-1 md:grid-cols-12 gap-3 text-xs">
          <div class="md:col-span-5">
            <label class="block font-semibold text-zinc-300 mb-1">GitLab URL репозитория <span class="text-red-400">*</span></label>
            <div class="relative">
              <input type="text" id="mobileGitlabRepoUrl"
                value="${escapeHtml(gitlabRepoUrl || 'https://lokaligitlabru.ru/app/locali-director-flutter.git')}"
                placeholder="https://lokaligitlabru.ru/app/locali-director-flutter.git"
                class="w-full bg-zinc-950 border border-darkborder rounded-lg pl-8 pr-3 py-2 font-mono text-zinc-100 placeholder-zinc-500 focus:outline-none focus:border-zinc-500">
              <i class="fa-brands fa-gitlab absolute left-2.5 top-2.5 text-zinc-500 text-xs"></i>
            </div>
          </div>

          <div class="md:col-span-4">
            <label class="block font-semibold text-zinc-300 mb-1">Access Token (Private / Project Token)</label>
            <div class="relative">
              <input type="password" id="mobileGitlabToken"
                value="${escapeHtml(gitlabToken)}"
                placeholder="glpat-... (токен доступа к репозиторию)"
                class="w-full bg-zinc-950 border border-darkborder rounded-lg pl-8 pr-8 py-2 font-mono text-zinc-100 placeholder-zinc-500 focus:outline-none focus:border-zinc-500">
              <i class="fa-solid fa-key absolute left-2.5 top-2.5 text-zinc-500 text-xs"></i>
              <button onclick="toggleGitlabTokenVisibility()" type="button" class="absolute right-2.5 top-2.5 text-zinc-400 hover:text-zinc-200 text-xs"><i id="glTokenEyeIcon" class="fa-solid fa-eye"></i></button>
            </div>
          </div>

          <div class="md:col-span-3">
            <label class="block font-semibold text-zinc-300 mb-1 flex items-center justify-between">
              <span>Ветка (Branch)</span>
              <button onclick="fetchGitLabBranches(this)" class="text-[10px] text-zinc-400 hover:text-zinc-200 font-normal"><i class="fa-solid fa-rotate mr-1"></i>Обновить ветки</button>
            </label>
            <select id="mobileGitlabBranchSelect" class="w-full bg-zinc-950 border border-darkborder rounded-lg px-2.5 py-2 font-mono text-zinc-100 focus:outline-none focus:border-zinc-500">
              ${renderGitLabBranchOptions()}
            </select>
          </div>
        </div>

        <div class="flex items-center justify-between pt-1 border-t border-darkborder flex-wrap gap-2">
          <span class="text-[11px] text-zinc-500">Токен сохраняется на стенде и не передаётся наружу.</span>
          <button onclick="runGitLabMobileContractScan(this)" class="px-4 py-2 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm transition flex items-center gap-1.5">
            <i class="fa-solid fa-play text-xs"></i><span>Проверить ветку из GitLab</span>
          </button>
        </div>
        ` : `
        <!-- Local Path Form -->
        <div class="flex flex-col sm:flex-row items-center gap-2.5">
          <div class="relative w-full flex-1">
            <input type="text" id="mobileRepoPathInput"
              value="${escapeHtml(localRepoPath || '/locali_director')}"
              placeholder="/locali_director или путь к локальному репозиторию..."
              class="w-full bg-zinc-950 border border-darkborder rounded-lg pl-8 pr-3 py-2 text-xs font-mono text-zinc-100 placeholder-zinc-500 focus:outline-none focus:border-zinc-500">
            <i class="fa-solid fa-folder-open absolute left-2.5 top-2.5 text-zinc-500 text-xs"></i>
          </div>
          <button onclick="runMobileContractScan(this)" class="px-4 py-2 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm transition flex items-center gap-1.5 shrink-0">
            <i class="fa-solid fa-play text-xs"></i><span>Проверить локальную папку</span>
          </button>
        </div>
        `}
      </div>
      `}

      <!-- Stat cards (Clickable filters) -->
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <button onclick="setMobileFilterStatus('all')" class="stat-card text-left hover:border-zinc-500 transition ${mobileFilterStatus === 'all' ? 'ring-1 ring-white' : ''}">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Всего DTO моделей</span>
          <div class="text-2xl font-bold text-white mt-1">${scanned}</div>
          <div class="text-[10px] text-zinc-500">клик: показать все</div>
        </button>
        <button onclick="setMobileFilterStatus('ok')" class="stat-card text-left hover:border-zinc-500 transition ${mobileFilterStatus === 'ok' ? 'ring-1 ring-white' : ''}">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider">Совместимы 100%</span>
          <div class="text-2xl font-bold text-zinc-100 mt-1">${compatible}</div>
          <div class="text-[10px] text-zinc-500">клик: показать совместимые</div>
        </button>
        <button onclick="setMobileFilterStatus('crash')" class="stat-card text-left hover:border-red-500 transition ${mobileFilterStatus === 'crash' ? 'ring-1 ring-red-400' : ''}">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider flex items-center gap-1.5"><i class="fa-solid fa-triangle-exclamation text-red-400"></i><span>Риски краша экрана</span></span>
          <div class="text-2xl font-bold ${crashes > 0 ? 'text-red-400' : 'text-zinc-400'} mt-1">${crashes}</div>
          <div class="text-[10px] text-zinc-500">клик: только краши</div>
        </button>
        <button onclick="setMobileFilterStatus('warn')" class="stat-card text-left hover:border-amber-500 transition ${mobileFilterStatus === 'warn' ? 'ring-1 ring-amber-400' : ''}">
          <span class="text-[9px] font-semibold text-zinc-400 uppercase tracking-wider flex items-center gap-1.5"><i class="fa-solid fa-circle-exclamation text-amber-400"></i><span>Предупреждения</span></span>
          <div class="text-2xl font-bold ${warnings > 0 ? 'text-amber-400' : 'text-zinc-400'} mt-1">${warnings}</div>
          <div class="text-[10px] text-zinc-500">алиасы / опциональные</div>
        </button>
      </div>

      <!-- Filter / Search toolbar -->
      <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-3 pt-1">
        <div class="flex items-center gap-1.5 flex-wrap">
          <span class="text-xs font-semibold text-zinc-400 mr-1">Приложение:</span>
          <button onclick="setMobileFilterApp('all')" class="px-2.5 py-1 rounded-lg text-xs font-medium transition ${mobileFilterApp === 'all' ? 'bg-white text-zinc-950 font-semibold shadow-sm' : 'bg-zinc-800 text-zinc-300 hover:text-white border border-darkborder'}">
            Все (${scanned})
          </button>
          ${mobileAppsList.map(app => {
            const isSel = mobileFilterApp === app.name;
            const pIcon = app.platform === 'ios' ? 'fa-brands fa-apple' :
                          app.platform === 'android' ? 'fa-brands fa-android' : 'fa-brands fa-flutter';
            const countForApp = allResults.filter(r => r.appName === app.name || (r.model && r.model.platform === app.platform)).length;
            return `
              <button onclick="setMobileFilterApp('${escAttr(app.name)}')" class="px-2.5 py-1 rounded-lg text-xs font-medium flex items-center gap-1.5 transition ${isSel ? 'bg-white text-zinc-950 font-semibold shadow-sm' : 'bg-zinc-800 text-zinc-300 hover:text-white border border-darkborder'}">
                <i class="${pIcon} text-xs"></i>
                <span>${escapeHtml(app.name)}</span>
                ${countForApp ? `<span class="px-1 text-[10px] font-mono opacity-70">(${countForApp})</span>` : ''}
              </button>
            `;
          }).join('')}
        </div>

        <div class="relative w-full sm:w-64">
          <input type="text" placeholder="Поиск по имени модели или файлу..."
            value="${escapeHtml(mobileFilterSearch)}"
            oninput="filterMobileSearch(this.value)"
            class="w-full pl-8 pr-3 py-1.5 text-xs">
          <i class="fa-solid fa-magnifying-glass absolute left-2.5 top-2.5 text-zinc-500 text-xs"></i>
        </div>
      </div>

      <!-- Scanned models list -->
      <div class="space-y-2.5">
        ${modelsHtml}
      </div>
    </div>
  `;

  container.innerHTML = html;
}

async function runMobileContractScan(btn) {
  const input = document.getElementById('mobileRepoPathInput');
  const path = (input ? input.value : '').trim() || '.';

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/mobilecontract/scan', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
      currentMobileReport = await res.json();
      showToast(`Просканировано ${currentMobileReport.totalModels} мобильных DTO моделей!`, 'success');
      renderMobileContractView();
    } catch (err) {
      showToast('Ошибка сканирования мобильного контракта: ' + err.message, 'error');
    }
  });
}

// ---------- 5. МОДАЛКА АВТОПОПОЛНЕНИЯ СЦЕНАРИЕВ ----------

function openReplenishModal(uncoveredOnly) {
  const modal = document.getElementById('replenishModal');
  if (!modal) return;

  const uncBox = document.getElementById('repOptUncoveredOnly');
  if (uncBox) uncBox.checked = !!uncoveredOnly;

  document.getElementById('replenishPreviewBox').classList.add('hidden');
  document.getElementById('replenishPreviewContent').innerHTML = '';

  modal.classList.remove('hidden');
}

function closeReplenishModal() {
  const modal = document.getElementById('replenishModal');
  if (modal) modal.classList.add('hidden');
}

async function runReplenishPreview(btn) {
  const strategies = getSelectedReplenishStrategies();
  if (strategies.length === 0) {
    showToast('Выберите хотя бы одну стратегию генерации', 'error');
    return;
  }

  const uncoveredOnly = !!(document.getElementById('repOptUncoveredOnly') && document.getElementById('repOptUncoveredOnly').checked);

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/scenarios/replenish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          strategies,
          uncoveredOnly,
          preview: true,
          maxScenarios: 50,
        }),
      });

      if (!res.ok) {
        throw new Error((await res.text()) || ('HTTP ' + res.status));
      }

      const data = await res.json();
      renderReplenishPreview(data);
    } catch (err) {
      showToast('Ошибка предпросмотра: ' + err.message, 'error');
    }
  });
}

function renderReplenishPreview(data) {
  const box = document.getElementById('replenishPreviewBox');
  const content = document.getElementById('replenishPreviewContent');
  if (!box || !content) return;

  const count = data.count || (data.scenarios ? data.scenarios.length : 0);
  const byStrat = data.byStrategy || {};

  if (count === 0) {
    content.innerHTML = `
      <div class="p-4 text-center text-slate-400 italic text-xs">
        По заданным критериям не найдено эндпоинтов для генерации (все эндпоинты уже покрыты).
      </div>
    `;
    box.classList.remove('hidden');
    return;
  }

  const stratBadges = Object.entries(byStrat).map(([st, c]) => `
    <span class="px-2 py-0.5 rounded text-[10px] font-bold bg-slate-800 text-slate-300 border border-darkborder">
      ${escapeHtml(st)}: ${c}
    </span>
  `).join(' ');

  const scenariosList = (data.scenarios || []).slice(0, 10).map(sc => `
    <div class="bg-slate-900 border border-darkborder rounded p-2 text-xs flex items-center justify-between">
      <div>
        <span class="font-bold text-slate-200">${escapeHtml(sc.title)}</span>
        <span class="text-[10px] font-mono text-slate-500 ml-1.5">(${escapeHtml(sc.key)})</span>
      </div>
      <span class="text-[10px] text-emerald-400 font-semibold">${sc.steps ? sc.steps.length : 0} шагов</span>
    </div>
  `).join('');

  content.innerHTML = `
    <div class="space-y-2">
      <div class="flex items-center justify-between text-xs">
        <span class="font-bold text-white">Будет создано: ${count} ${pluralRu(count, ['сценарий', 'сценария', 'сценариев'])}</span>
        <div class="space-x-1">${stratBadges}</div>
      </div>
      <div class="space-y-1 max-h-48 overflow-y-auto pr-1">
        ${scenariosList}
      </div>
      ${count > 10 ? `<div class="text-[10px] text-slate-500 italic text-center">...и ещё ${count - 10} сценариев</div>` : ''}
    </div>
  `;

  box.classList.remove('hidden');
}

async function executeReplenish(btn) {
  const strategies = getSelectedReplenishStrategies();
  if (strategies.length === 0) {
    showToast('Выберите хотя бы одну стратегию генерации', 'error');
    return;
  }

  const uncoveredOnly = !!(document.getElementById('repOptUncoveredOnly') && document.getElementById('repOptUncoveredOnly').checked);
  const overwrite = !!(document.getElementById('repOptOverwrite') && document.getElementById('repOptOverwrite').checked);

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/scenarios/replenish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          strategies,
          uncoveredOnly,
          overwrite,
          preview: false,
          maxScenarios: 50,
        }),
      });

      if (!res.ok) {
        throw new Error((await res.text()) || ('HTTP ' + res.status));
      }

      const data = await res.json();
      const count = data.savedCount || 0;

      showToast(`Успешно сгенерировано и сохранено: ${count} ${pluralRu(count, ['сценарий', 'сценария', 'сценариев'])}!`, 'success');
      closeReplenishModal();

      // Refresh registries and analysis
      if (typeof loadSuites === 'function') await loadSuites();
      await loadAnalysis();
    } catch (err) {
      showToast('Ошибка автопополнения: ' + err.message, 'error');
    }
  });
}

function getSelectedReplenishStrategies() {
  const list = [];
  if (document.getElementById('stratSmoke') && document.getElementById('stratSmoke').checked) list.push('smoke');
  if (document.getElementById('stratCRUD') && document.getElementById('stratCRUD').checked) list.push('crud');
  if (document.getElementById('stratRBAC') && document.getElementById('stratRBAC').checked) list.push('rbac');
  if (document.getElementById('stratNegative') && document.getElementById('stratNegative').checked) list.push('negative');
  return list;
}

// 1-click replenish for a specific single endpoint from the table
async function replenishSingleEndpoint(method, path, tag) {
  try {
    const res = await fetch('/api/scenarios/replenish', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        strategies: ['smoke', 'crud', 'negative'],
        tags: tag ? [tag] : [],
        preview: false,
        maxScenarios: 5,
      }),
    });

    if (!res.ok) throw new Error((await res.text()) || ('HTTP ' + res.status));
    const data = await res.json();

    showToast(`Создан тестовый сценарий для ${method} ${path}!`, 'success');
    if (typeof loadSuites === 'function') await loadSuites();
    await loadAnalysis();
  } catch (err) {
    showToast('Ошибка создания сценария: ' + err.message, 'error');
  }
}
