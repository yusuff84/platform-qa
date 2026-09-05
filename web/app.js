// ============================================================
// app.js — ядро админки Locali E2E: табы, статус стенда, SSE,
// сетка тестов/чеклистов, Обзор, История, Отчёт по прогону,
// Token Vault, API Playground.
// Спланировано вместе с js/ui.js, js/logs.js, js/scenarios-editor.js
// (глобальное пространство имён, порядок подключения в index.html).
// ============================================================

// ---------- State ----------

let sseConnection = null;

// Разрыв SSE: поднимается в onerror, сбрасывается после реконсиляции в onopen
let sseWasDown = false;

// Suites / Checklists State
let suitesRegistry = [];
let suiteStates = {};
let historyCache = [];

// Статус стенда из /api/status
let statusInfo = {};

// Количество идущих прямо сейчас прогонов (для пульсирующего бейджа)
let activeRunsCount = 0;

// Прогон, открытый в модалке отчёта
let currentRunDetailsId = null;

// Сюты с незавершённым POST /api/runs — защита от двойного запуска
const runsInFlight = new Set();

// Последнее загруженное состояние Token Vault (для copyProfileToken)
let tokenVault = null;

// Дебаунс-таймер обновления после SUMMARY
let summaryRefreshTimer = null;

// Прогон, ожидающий ввод кода верификации (модалка inputCodeModal)
let pendingInputRunId = null;

// Группа батч-регресса (trigger=regression): groupId из ответа POST /api/runs.
// Используется для агрегированной плашки «Регресс: ✅ N/M» на Обзоре.
let activeRegressionGroupId = null;

function getActiveRunsCount() {
  return activeRunsCount;
}

function bumpActiveRuns(delta) {
  activeRunsCount = Math.max(0, activeRunsCount + delta);
  if (typeof logsUpdateCounter === 'function') logsUpdateCounter();
}

document.addEventListener('DOMContentLoaded', () => {
  initStatus();
  if (typeof loadStands === 'function') loadStands(); // кэш стендов для дропдауна/шапки/Обзора
  initSSE();
  loadVault();
  loadSuites();
  loadHistory();
});

// ---------- Tabs ----------

let currentToolsSubtab = 'history';

function switchToolsSubtab(subId) {
  currentToolsSubtab = subId || 'history';
  document.querySelectorAll('.tools-subtab').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.tools-subview').forEach(v => v.classList.add('hidden'));

  const btn = document.getElementById(`tools-subtab-${currentToolsSubtab}`);
  const view = document.getElementById(`tools-subview-${currentToolsSubtab}`);
  if (btn) btn.classList.add('active');
  if (view) view.classList.remove('hidden');

  if (currentToolsSubtab === 'history') {
    if (typeof loadHistory === 'function') loadHistory();
  } else if (currentToolsSubtab === 'vault') {
    if (typeof loadVault === 'function') loadVault();
  } else if (currentToolsSubtab === 'spec') {
    if (typeof loadSpec === 'function') loadSpec();
  }
}
window.switchToolsSubtab = switchToolsSubtab;

function switchTab(tabId) {
  // If tabId is a sub-tab of tools/management:
  if (tabId === 'history' || tabId === 'vault' || tabId === 'spec' || tabId === 'statemachine' || tabId === 'playground') {
    switchTab('tools');
    switchToolsSubtab(tabId);
    return;
  }

  document.querySelectorAll('.tab-btn').forEach(btn => btn.classList.remove('active'));
  document.querySelectorAll('.tab-view').forEach(view => view.classList.add('hidden'));

  const activeBtn = document.getElementById(`tab-${tabId}`);
  const activeView = document.getElementById(`view-${tabId}`);
  if (activeBtn) activeBtn.classList.add('active');
  if (activeView) activeView.classList.remove('hidden');

  if (tabId === 'overview') {
    refreshOverview();
  } else if (tabId === 'checklists') {
    loadSuites();
  } else if (tabId === 'scenarios') {
    loadScenarioEditor();
  } else if (tabId === 'analysis') {
    if (typeof loadAnalysis === 'function') loadAnalysis();
  } else if (tabId === 'mobile') {
    if (typeof loadMobileContract === 'function') loadMobileContract();
  } else if (tabId === 'tools') {
    switchToolsSubtab(currentToolsSubtab || 'history');
  }
}

// ---------- Status & Config ----------

async function initStatus() {
  try {
    const res = await fetch('/api/status');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    statusInfo = await res.json();
    renderHeaderEnv();
    updateOverviewRoles();
    const cfgUrl = document.getElementById('cfgBaseUrl');
    if (cfgUrl) cfgUrl.value = statusInfo.baseURL || '';
    const cfgLogin = document.getElementById('cfgAdminLogin');
    if (cfgLogin) cfgLogin.value = statusInfo.adminLogin || '';
  } catch (err) {
    console.error('Failed to fetch status:', err);
    toastError('Не удалось получить статус стенда: ' + err.message);
  }
}

// Индикатор стенда в шапке: МОК (жёлтая точка) / РЕАЛЬНЫЙ (зелёная)
function renderHeaderEnv() {
  const dot = document.getElementById('envDot');
  const label = document.getElementById('envLabel');
  const url = document.getElementById('envUrl');
  if (!dot || !label) return;

  const isMock = !!statusInfo.isMockMode;
  const dotCls = 'w-2.5 h-2.5 rounded-full animate-pulse shrink-0 ' +
    (isMock ? 'bg-amber-400 shadow-[0_0_10px_rgba(251,191,36,.8)]' : 'bg-green-400 shadow-[0_0_10px_rgba(52,211,153,.8)]');
  const modeText = isMock ? 'МОК' : 'РЕАЛЬНЫЙ';
  const labelCls = 'text-[10px] font-bold px-2 py-0.5 rounded-full border shrink-0 ' +
    (isMock
      ? 'bg-amber-500/10 text-amber-400 border-amber-500/30'
      : 'bg-green-500/10 text-green-400 border-green-500/30');

  // Имя активного стенда — из кэша stands.js (standsCache)
  const activeStand = (typeof getActiveStand === 'function') ? getActiveStand() : null;
  const standName = (activeStand && activeStand.name) || '';

  dot.className = dotCls;
  label.textContent = modeText;
  label.className = labelCls;
  if (url) url.textContent = statusInfo.baseURL || '—';

  const nameEl = document.getElementById('envName');
  if (nameEl) {
    nameEl.textContent = standName || '—';
    nameEl.classList.toggle('hidden', !standName);
  }
  const trigBtn = document.getElementById('standsDropdownBtn');
  if (trigBtn) {
    trigBtn.title = standName
      ? 'Активный стенд: ' + standName + '. Клик — список стендов'
      : 'Список стендов';
  }

  // Синхронизация карточки «Стенд» на Обзоре
  const ovDot = document.getElementById('ovEnvDot');
  const ovMode = document.getElementById('ovEnvMode');
  const ovUrl = document.getElementById('ovEnvUrl');
  const ovStand = document.getElementById('ovEnvStand');
  if (ovDot) ovDot.className = dotCls;
  if (ovMode) {
    ovMode.textContent = modeText;
    ovMode.className = labelCls;
  }
  if (ovUrl) ovUrl.textContent = statusInfo.baseURL || '—';
  if (ovStand) ovStand.textContent = 'Стенд: ' + (standName || '—');

  // Бейдж хранилища в табе «Токены и доступ» — из последнего ответа /api/status
  const vaultBadge = document.getElementById('vaultStandBadge');
  if (vaultBadge) {
    const sn = statusInfo.standName || '';
    vaultBadge.textContent = 'Хранилище стенда: ' + sn;
    vaultBadge.classList.toggle('hidden', !sn);
  }
}

function openConfigModal() {
  openStandsModal();
}

function closeConfigModal() {
  closeStandsModal();
}

async function saveConfig(btn) {
  const baseURL = document.getElementById('cfgBaseUrl').value.trim();
  const adminLogin = document.getElementById('cfgAdminLogin').value.trim();
  const adminPassword = document.getElementById('cfgAdminPassword').value.trim();

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ baseURL, adminLogin, adminPassword }),
      });
      if (res.ok) {
        closeConfigModal();
        initStatus();
        toastSuccess('Настройки стенда сохранены.');
        logTerminal('INFO', `Конфигурация обновлена. Target Base URL: ${baseURL}`);
      } else {
        const errText = await res.text();
        toastError('Ошибка сохранения настроек: HTTP ' + res.status + ' ' + errText);
      }
    } catch (err) {
      toastError('Ошибка сохранения настроек: ' + err.message);
    }
  });
}

// ---------- Live SSE Stream ----------

function initSSE() {
  if (sseConnection) sseConnection.close();

  sseConnection = new EventSource('/api/events');

  sseConnection.addEventListener('test_event', (e) => {
    try {
      const event = JSON.parse(e.data);
      handleExecutionEvent(event);
    } catch (err) {
      console.error('Error parsing SSE event:', err);
    }
  });

  sseConnection.onopen = () => {
    if (!sseWasDown) return; // первое подключение — реконсиляция не нужна
    sseWasDown = false;
    reconcileAfterSSEGap();
  };

  sseConnection.onerror = () => {
    console.warn('SSE disconnected, retrying...');
    sseWasDown = true;
  };
}

// Реконсиляция после разрыва SSE: подтягиваем историю прогонов и
// финализируем карточки сютов, чьи прогоны завершились «без нас».
async function reconcileAfterSSEGap() {
  logTerminal('INFO', 'SSE восстановлено — синхронизирую статусы прогонов.');
  await loadHistory(); // обновляет historyCache + Обзор из кэша
  finalizeStaleSuiteCards();
}

// Финализация карточек сютов с running=true, чей прогон в истории уже не RUNNING
function finalizeStaleSuiteCards() {
  Object.keys(suiteStates).forEach(key => {
    const st = suiteStates[key];
    if (!st || !st.running) return;
    const run = st.lastRunId
      ? (historyCache || []).find(r => r && r.id === st.lastRunId)
      : latestRunForSuite(key);
    if (run && run.status !== 'RUNNING') {
      finalizeSuiteCard(key, run.status === 'PASSED', { runId: run.id });
    }
  });
  // Синхронизация счётчика активных прогонов с реальностью
  const realRunning = Object.values(suiteStates).filter(s => s.running).length;
  if (realRunning !== activeRunsCount) {
    activeRunsCount = realRunning;
    if (typeof logsUpdateCounter === 'function') logsUpdateCounter();
  }
}

// Центральный обработчик события выполнения: глобальные логи,
// state machine, карточки сютов (чеки, прогресс, итоговый баннер).
function handleExecutionEvent(event) {
  // 1. ВСЕГДА буферизуем в глобальный журнал (оверлей может быть закрыт)
  if (typeof logsPushEvent === 'function') logsPushEvent(event);

  // 2. Подсветка активного узла State Machine
  if (event.currentState) {
    highlightStateNode(event.currentState);
  }

  // 3. Живое обновление матрицы чеклистов
  if (event.suiteKey && suiteStates[event.suiteKey]) {
    if (event.stepType === 'SUITE_START') {
      startSuiteCard(event.suiteKey);
      if (event.runId) suiteStates[event.suiteKey].lastRunId = event.runId;
    } else if (event.stepType === 'CHECK_START') {
      setCheckState(event.suiteKey, event.checkId, 'RUNNING');
      updateSuiteProgress(event.suiteKey, event);
    } else if (event.stepType === 'CHECK_SUCCESS') {
      setCheckState(event.suiteKey, event.checkId, 'PASSED', event.message, event.durationMs);
      updateSuiteProgress(event.suiteKey, event);
    } else if (event.stepType === 'CHECK_FAILED') {
      setCheckState(event.suiteKey, event.checkId, 'FAILED', event.message, event.durationMs);
      updateSuiteProgress(event.suiteKey, event);
    } else if (event.stepType === 'CHECK_SKIPPED') {
      setCheckState(event.suiteKey, event.checkId, 'SKIPPED', event.message, event.durationMs);
      updateSuiteProgress(event.suiteKey, event);
    }
  }

  // 4. Запрос кода верификации: бэкенд приостановил прогон и ждёт POST /api/runs/{runId}/input-code
  if (event.stepType === 'INPUT_REQUIRED') {
    openInputCodeModal(event.runId, event.inputPhone);
    return;
  }

  // 5. Итог прогона: баннер на карточке + обновления разделов
  if (event.stepType === 'SUMMARY') {
    const ok = event.level === 'SUCCESS' || event.level === 'PASSED';
    finalizeSuiteCard(event.suiteKey, ok, event);
    schedulePostSummaryRefresh();
  }
}

// Дебаунс-обновление после SUMMARY: одна пачка loadVault+loadHistory
// вместо N параллельных пар при батч-прогоне.
function schedulePostSummaryRefresh() {
  if (summaryRefreshTimer) clearTimeout(summaryRefreshTimer);
  summaryRefreshTimer = setTimeout(() => {
    summaryRefreshTimer = null;
    loadVault();
    loadHistory().then(() => updateOverviewFromCache());
  }, 300);
}

// ==========================================
// ВВОД КОДА ВЕРИФИКАЦИИ ВО ВРЕМЯ ПРОГОНА
// Бэкенд по SSE присылает stepType=INPUT_REQUIRED с inputPhone,
// приостанавливает прогон (до 3 минут) и ждёт
// POST /api/runs/{runId}/input-code {"code":"..."} → 200 | 404.
// ==========================================

function openInputCodeModal(runId, phone) {
  if (!runId) return;
  const modal = document.getElementById('inputCodeModal');
  if (!modal) return;

  pendingInputRunId = runId;
  const phoneEl = document.getElementById('inputCodePhone');
  if (phoneEl) phoneEl.textContent = phone || '—';
  const input = document.getElementById('inputCodeValue');
  if (input) input.value = '';

  modal.classList.remove('hidden');
  logTerminal('WARN', 'Прогон ожидает код верификации для ' + (phone || 'номера') + '.');
  setTimeout(() => { if (input) input.focus(); }, 50);
}

function closeInputCodeModal() {
  const modal = document.getElementById('inputCodeModal');
  if (modal) modal.classList.add('hidden');
  pendingInputRunId = null;
}

// Отмена: закрываем модалку; прогон продолжит ждать и упадёт по таймауту (3 мин)
async function cancelInputCode() {
  const ok = await confirmDialog(
    'Закрыть без ввода кода?',
    'Прогон продолжит ждать код до таймаута (3 минуты), после чего завершится ошибкой.',
    'Закрыть'
  );
  if (ok) closeInputCodeModal();
}

async function submitInputCode(btn) {
  const input = document.getElementById('inputCodeValue');
  const code = input ? input.value.trim() : '';
  if (!code) {
    toastError('Введите код из Telegram-группы.');
    if (input) input.focus();
    return;
  }
  const runId = pendingInputRunId;
  if (!runId) {
    toastError('Нет активного прогона, ожидающего код.');
    return;
  }

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/runs/' + encodeURIComponent(runId) + '/input-code', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code }),
      });
      if (res.ok) {
        toastSuccess('Код принят — прогон продолжается');
        logTerminal('INFO', 'Код верификации отправлен в прогон ' + String(runId).substring(0, 8) + '.');
        closeInputCodeModal();
      } else if (res.status === 404) {
        toastError('Окно ожидания истекло');
        closeInputCodeModal();
      } else {
        toastError('Ошибка отправки кода: HTTP ' + res.status + ' ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка отправки кода: ' + err.message);
    }
  });
}

// Esc закрывает модалку ввода кода (паттерн существующих модалок)
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  const modal = document.getElementById('inputCodeModal');
  if (modal && !modal.classList.contains('hidden')) closeInputCodeModal();
});

// Системные сообщения UI попадают в тот же глобальный журнал
function logTerminal(level, msg) {
  if (typeof logsPushSystem === 'function') {
    logsPushSystem(msg, level);
  }
}

function highlightStateNode(stateName) {
  document.querySelectorAll('.sm-node').forEach(n => n.classList.remove('active-state'));
  const target = document.getElementById(`node-${stateName}`);
  if (target) {
    target.classList.add('active-state');
  }
}

// ---------- Запуск одного сюта (кнопки карточек / Обзор) ----------

async function triggerRun(suiteKey, btn) {
  if (runsInFlight.has(suiteKey)) return;
  runsInFlight.add(suiteKey);

  switchTab('checklists');
  logTerminal('INFO', `Инициализация запуска сьюта: ${suiteKey}...`);
  markSuitesRunning([suiteKey]);

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ suite: suiteKey }),
      });
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
    } catch (err) {
      finalizeSuiteCard(suiteKey, false);
      logTerminal('ERROR', `Ошибка запуска сьюта ${suiteKey}: ${err.message}`);
      toastError(`Не удалось запустить «${suiteKey}»: ` + err.message);
    }
  });

  runsInFlight.delete(suiteKey);
}

// ==========================================
// SUITES REGISTRY & CHECKLISTS MATRIX
// ==========================================

const CATEGORY_STYLES = {
  flow:        { ru: 'Сценарий',     badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-route text-zinc-400',              btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  negative:    { ru: 'Негативный',   badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-triangle-exclamation text-zinc-400', btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  security:    { ru: 'Безопасность', badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-shield-halved text-zinc-400',     btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  reliability: { ru: 'Надёжность',   badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-rotate-left text-zinc-400',         btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  edge:        { ru: 'Граничный',    badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-dice-d6 text-zinc-400',              btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  custom:      { ru: 'Мой сценарий', badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-code text-zinc-400',                  btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
  api:         { ru: 'API-ручка',    badge: 'bg-zinc-800 text-zinc-300 border-zinc-700', icon: 'fa-plug text-zinc-400',                 btn: 'bg-white hover:bg-zinc-200 text-zinc-950 font-semibold shadow-sm' },
};

async function loadSuites() {
  try {
    const res = await fetch('/api/suites');
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }
    const data = await res.json();
    suitesRegistry = data.suites || [];
    document.getElementById('suitesError').classList.add('hidden');
    initSuiteStates();
    renderSuites();
  } catch (err) {
    console.error('Failed to load suites registry:', err);
    suitesRegistry = [];
    document.getElementById('suitesError').classList.remove('hidden');
    document.getElementById('suitesGrid').innerHTML =
      loadRetryHtml('Не удалось загрузить реестр сютов: ' + err.message + '. Проверьте бэкенд.', 'loadSuites');
    updateBatchBar();
    updateChecklistSummary();
    updateRunProgressBar();
  }
  updateOverviewSuites();
  if (typeof logsFillSuiteOptions === 'function') logsFillSuiteOptions();
}

function initSuiteStates() {
  const next = {};
  suitesRegistry.forEach(suite => {
    const prev = suiteStates[suite.key];
    const checks = {};
    (suite.checks || []).forEach(c => {
      checks[c.id] = (prev && prev.checks && prev.checks[c.id]) ? prev.checks[c.id] : { status: 'PENDING', message: '' };
    });
    next[suite.key] = {
      selected: prev ? !!prev.selected : false,
      running: prev ? !!prev.running : false,
      lastResult: prev ? prev.lastResult : null,
      lastRunId: prev ? prev.lastRunId : null,
      failedCheckTitle: prev ? prev.failedCheckTitle : null,
      checks,
    };
  });
  suiteStates = next;
  updateBatchBar();
  updateChecklistSummary();
  updateRunProgressBar();
}

let suitesFilterCategory = 'all'; // 'all' | 'flow' | 'api' | 'custom' | 'failed'
let suitesSearchQuery = '';

function setSuitesFilter(cat) {
  suitesFilterCategory = cat || 'all';
  const group = document.getElementById('suitesFilterGroup');
  if (group) {
    group.querySelectorAll('button').forEach(btn => {
      btn.className = 'px-2.5 py-1 text-xs font-medium rounded-lg bg-zinc-900 hover:bg-zinc-800 text-zinc-300 transition';
    });
    const activeBtn = document.getElementById(`sf-${suitesFilterCategory}`);
    if (activeBtn) {
      activeBtn.className = suitesFilterCategory === 'failed'
        ? 'px-2.5 py-1 text-xs font-semibold rounded-lg bg-red-950/40 text-red-300 border border-red-800/40 transition'
        : 'px-2.5 py-1 text-xs font-semibold rounded-lg bg-zinc-800 text-white transition';
    }
  }
  renderSuites();
}
window.setSuitesFilter = setSuitesFilter;

function filterSuites(query) {
  suitesSearchQuery = (query || '').toLowerCase().trim();
  renderSuites();
}
window.filterSuites = filterSuites;

function selectAllBatchVisible() {
  const visible = getFilteredSuites();
  visible.forEach(s => {
    if (suiteStates[s.key]) suiteStates[s.key].selected = true;
  });
  updateBatchBar();
  renderSuites();
}
window.selectAllBatchVisible = selectAllBatchVisible;

function clearAllBatch() {
  Object.keys(suiteStates).forEach(k => {
    suiteStates[k].selected = false;
  });
  updateBatchBar();
  renderSuites();
}
window.clearAllBatch = clearAllBatch;

function getFilteredSuites() {
  return suitesRegistry.filter(s => {
    if (suitesFilterCategory === 'flow') {
      if (s.category === 'api' || s.category === 'custom') return false;
    } else if (suitesFilterCategory === 'api') {
      if (s.category !== 'api') return false;
    } else if (suitesFilterCategory === 'custom') {
      if (s.category !== 'custom') return false;
    } else if (suitesFilterCategory === 'failed') {
      const st = suiteStates[s.key];
      if (!st || st.lastResult !== false) return false;
    }

    if (suitesSearchQuery) {
      const matchKey = (s.key || '').toLowerCase().includes(suitesSearchQuery);
      const matchTitle = (s.title || '').toLowerCase().includes(suitesSearchQuery);
      const matchDesc = (s.description || '').toLowerCase().includes(suitesSearchQuery);
      const matchTags = (s.tags || []).some(t => String(t).toLowerCase().includes(suitesSearchQuery));
      if (!matchKey && !matchTitle && !matchDesc && !matchTags) return false;
    }

    return true;
  });
}

// Сценарии и API-проверки отвечают на разные вопросы, поэтому и показываются
// раздельно: сценарий доказывает, что флоу работает целиком и обрывается на
// первом сломанном шаге; API-проверка доказывает контракт одной ручки и идёт
// независимо от соседних.
function renderSuites() {
  const grid = document.getElementById('suitesGrid');
  if (!grid) return;
  if (!suitesRegistry.length) {
    grid.innerHTML = '<div class="md:col-span-2 xl:col-span-3 p-6 text-center text-zinc-500 italic text-xs">Загрузка реестра сютов...</div>';
    return;
  }

  const filtered = getFilteredSuites();
  if (!filtered.length) {
    grid.innerHTML = `
      <div class="col-span-full p-8 text-center border border-dashed border-darkborder rounded-xl space-y-3">
        <i class="fa-solid fa-filter text-2xl text-zinc-500 mb-1"></i>
        <p class="text-sm font-semibold text-zinc-300">Нет тестов, соответствующих фильтру</p>
        <p class="text-xs text-zinc-500">Попробуйте выбрать категорию «Все» или очистить строку поиска.</p>
        <button onclick="setSuitesFilter('all'); const el = document.getElementById('suitesSearchInput'); if(el) el.value=''; filterSuites('');" class="px-3 py-1.5 text-xs bg-zinc-800 hover:bg-zinc-700 text-zinc-200 rounded-lg transition font-medium">Сбросить фильтры</button>
      </div>
    `;
    updateChecklistSummary();
    updateRunProgressBar();
    return;
  }

  const scenarios = filtered.filter(s => s.category !== 'api');
  const apiSuites = filtered.filter(s => s.category === 'api');

  let html = '';
  if (scenarios.length) {
    html += suiteSectionHtml(
      'Сценарии',
      'fa-route text-zinc-400',
      'Сквозные флоу: каждый шаг зависит от предыдущего, прогон останавливается на первом провале.',
      scenarios);
  }
  if (apiSuites.length) {
    html += suiteSectionHtml(
      'API-проверки ручек',
      'fa-plug text-zinc-400',
      'Контракты отдельных ручек. Проверки независимы: одна сломанная ручка не скрывает состояние остальных.',
      apiSuites);
  }

  grid.innerHTML = html;
  updateChecklistSummary();
  updateRunProgressBar();
  updateLastRunBadges();
}

// suiteSectionHtml рисует озаглавленную секцию с сеткой карточек внутри.
function suiteSectionHtml(title, icon, subtitle, suites) {
  const checksTotal = suites.reduce((n, s) => n + ((s.checks || []).length), 0);
  return `
    <div class="col-span-full">
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1 mb-3 mt-1">
        <h3 class="text-sm font-bold text-white flex items-center gap-2">
          <i class="fa-solid ${icon}"></i>${escapeHtml(title)}
        </h3>
        <span class="text-[10px] font-mono px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-darkborder">
          ${suites.length} наборов · ${checksTotal} проверок
        </span>
        <span class="text-[11px] text-slate-500 basis-full leading-snug">${escapeHtml(subtitle)}</span>
      </div>
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        ${suites.map(s => suiteCardHtml(s)).join('')}
      </div>
    </div>`;
}

function findSuiteCard(suiteKey) {
  return document.querySelector(`[data-suite-card="${CSS.escape(suiteKey)}"]`);
}

function suiteCardHtml(suite) {
  const st = suiteStates[suite.key] || { selected: false, running: false, checks: {} };
  const cat = CATEGORY_STYLES[suite.category] || CATEGORY_STYLES.flow;
  const runningClass = st.running ? ' suite-card-running border-zinc-500 ring-1 ring-zinc-500/40' : '';

  const itemsHtml = (suite.checks || []).map(c => checkItemHtml(suite.key, c.id, c.title, st.checks[c.id])).join('');

  const tagsHtml = (suite.tags || []).map(t =>
    `<span class="px-1.5 py-0.5 rounded bg-zinc-900 text-[9px] font-mono text-zinc-400 border border-darkborder">${escapeHtml(t)}</span>`
  ).join(' ');

  const resultBadgeHtml = st.lastResult === null || st.running
    ? (st.running
        ? '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-200 border border-zinc-700 animate-pulse">RUNNING</span>'
        : '')
    : (st.lastResult
        ? '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-100 border border-zinc-600">PASSED</span>'
        : '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-red-950/40 text-red-300 border border-red-800/40">FAILED</span>');

  return `
    <div class="suite-card${runningClass} bg-darkcard border border-darkborder rounded-xl p-4 flex flex-col space-y-3 transition" data-suite-card="${escAttr(suite.key)}">
      <div class="flex items-start justify-between gap-2">
        <div class="flex items-center space-x-2 flex-wrap gap-y-1">
          <span class="text-[10px] font-medium px-2 py-0.5 rounded border ${cat.badge}" title="Категория: ${escapeHtml(cat.ru)}">${escapeHtml(cat.ru)}</span>
          ${tagsHtml}
        </div>
        <i class="fa-solid ${cat.icon} shrink-0" title="${escapeHtml(cat.ru)}"></i>
      </div>

      <div>
        <h3 class="font-semibold text-white text-sm">${escapeHtml(suite.title)}</h3>
        <p class="text-[11px] text-zinc-400 mt-1 leading-relaxed">${escapeHtml(suite.description)}</p>
      </div>

      <div class="space-y-1.5 flex-1">
        ${(suite.checks || []).length ? itemsHtml : '<div class="text-[11px] text-zinc-500 italic">Чеклист пуст</div>'}
      </div>

      <div data-suite-progress class="${st.running ? '' : 'hidden'}"></div>
      <div data-suite-summary class="space-y-1"></div>

      <div class="text-[9px] font-mono text-zinc-500 min-h-[14px] truncate" data-last-run-badge="${escAttr(suite.key)}"></div>

      <div class="border-t border-darkborder pt-3 flex items-center justify-between gap-2">
        <label class="flex items-center space-x-2 cursor-pointer select-none" title="Добавить сьют в батч-запуск">
          <input type="checkbox" ${st.selected ? 'checked' : ''} onchange="toggleBatch('${escAttr(suite.key)}', this)" class="rounded border-darkborder bg-zinc-900 text-zinc-200">
          <span class="text-[11px] text-zinc-300">в батч</span>
        </label>
        <div class="flex items-center gap-1.5 min-w-0">
          ${customActionsHtml(suite)}
          <button onclick="runSuiteFromChecklist('${escAttr(suite.key)}')" data-run-btn ${st.running ? 'disabled' : ''}
            class="px-3 py-1.5 text-zinc-950 bg-white hover:bg-zinc-200 text-[11px] font-semibold rounded-lg transition flex items-center space-x-1.5 shadow-sm disabled:opacity-50 disabled:cursor-not-allowed shrink-0">
            <i class="fa-solid fa-play text-[10px]"></i>
            <span>${st.running ? 'Идёт прогон...' : 'Запустить'}</span>
          </button>
          ${resultBadgeHtml}
        </div>
      </div>
    </div>
  `;
}

function checkItemHtml(suiteKey, checkId, title, state) {
  const s = state || { status: 'PENDING', message: '' };
  let msgHtml = '';
  if (s.status === 'FAILED' && s.message) {
    msgHtml = `<div class="check-msg pl-7 text-[10px] text-red-300 bg-red-950/20 border-l-2 border-red-500/50 rounded-r py-0.5 pr-1 break-all">${escapeHtml(s.message)}</div>`;
  } else if (s.status === 'SKIPPED' && s.message) {
    msgHtml = `<div class="check-msg pl-7 text-[10px] text-zinc-500 bg-zinc-900/40 border-l-2 border-zinc-600 rounded-r py-0.5 pr-1 break-all">${escapeHtml(s.message)}</div>`;
  }
  return `
    <div class="check-item flex items-start justify-between gap-2" data-check-item data-check-suite="${escAttr(suiteKey)}" data-check-id="${escAttr(checkId)}">
      <div class="flex-1 min-w-0 space-y-1">
        <div class="flex items-center space-x-2 min-w-0">
          <span class="check-icon w-4 text-center shrink-0">${checkIconHtml(s.status)}</span>
          <span class="text-[11px] text-zinc-300 truncate" title="${escAttr(title)}">${escapeHtml(title)}</span>
        </div>
        ${msgHtml}
      </div>
      ${s.durationMs !== undefined && s.status !== 'RUNNING' && s.status !== 'PENDING' ? `<span class="text-[9px] font-mono text-zinc-500 shrink-0 mt-0.5" title="Длительность проверки">${fmtDuration(s.durationMs)}</span>` : ''}
    </div>
  `;
}

function checkIconHtml(status) {
  switch (status) {
    case 'RUNNING':
      return '<i class="fa-solid fa-circle-notch fa-spin text-zinc-200 text-xs"></i>';
    case 'PASSED':
      return '<i class="fa-solid fa-circle-check text-emerald-400 text-xs"></i>';
    case 'FAILED':
      return '<i class="fa-solid fa-circle-xmark text-red-400 text-xs"></i>';
    case 'SKIPPED':
      return '<i class="fa-solid fa-minus text-zinc-500 text-xs"></i>';
    default:
      return '<i class="fa-regular fa-circle text-zinc-600 text-xs"></i>';
  }
}

function findCheckRow(suiteKey, checkId) {
  const rows = document.querySelectorAll('.check-item[data-check-item]');
  for (const row of rows) {
    if (row.dataset.checkSuite === suiteKey && row.dataset.checkId === checkId) {
      return row;
    }
  }
  return null;
}

function setCheckState(suiteKey, checkId, status, message, durationMs) {
  const st = suiteStates[suiteKey];
  if (!st || !st.checks[checkId]) {
    return;
  }

  if (status === 'FAILED' && !st.failedCheckTitle) {
    st.failedCheckTitle = getCheckTitle(suiteKey, checkId) || checkId;
  }

  st.checks[checkId] = {
    status,
    message: message || '',
    durationMs: durationMs !== undefined ? durationMs : st.checks[checkId].durationMs,
  };

  const row = findCheckRow(suiteKey, checkId);
  if (row) {
    const iconEl = row.querySelector('.check-icon');
    if (iconEl) iconEl.innerHTML = checkIconHtml(status);

    let msgEl = row.querySelector('.check-msg');
    if ((status === 'FAILED' || status === 'SKIPPED') && message) {
      const cls = status === 'FAILED'
        ? 'pl-7 text-[10px] text-red-400 bg-red-500/5 border-l-2 border-red-500/50 rounded-r py-0.5 pr-1 break-all'
        : 'pl-7 text-[10px] text-slate-500 bg-slate-500/5 border-l-2 border-slate-500/40 rounded-r py-0.5 pr-1 break-all';
      if (!msgEl) {
        msgEl = document.createElement('div');
        row.firstElementChild.appendChild(msgEl);
      }
      msgEl.className = 'check-msg ' + cls;
      msgEl.textContent = typeof tidyEventMessage === 'function' ? tidyEventMessage(message) : message;
    } else if (msgEl) {
      msgEl.remove();
    }

    const durEl = row.querySelector('.font-mono.text-slate-500');
    if (durationMs !== undefined && status !== 'RUNNING' && status !== 'PENDING') {
      if (durEl) {
        durEl.textContent = fmtDuration(durationMs);
      } else {
        row.insertAdjacentHTML('beforeend', `<span class="text-[9px] font-mono text-slate-500 shrink-0 mt-0.5" title="Длительность проверки">${fmtDuration(durationMs)}</span>`);
      }
    }
  }

  updateChecklistSummary();
  updateRunProgressBar();
}

// Строка прогресса на карточке: «Шаг N из M — Название» + полоса
function updateSuiteProgress(suiteKey, ev) {
  const st = suiteStates[suiteKey];
  const card = findSuiteCard(suiteKey);
  if (!st || !card) return;
  const prog = card.querySelector('[data-suite-progress]');
  if (!prog) return;

  const suite = suitesRegistry.find(s => s.key === suiteKey);
  const total = (ev && ev.totalSteps) ? ev.totalSteps : ((suite && suite.checks ? suite.checks.length : 0));

  let done = 0;
  Object.values(st.checks).forEach(c => {
    if (c.status === 'PASSED' || c.status === 'FAILED' || c.status === 'SKIPPED') done++;
  });

  const idx = (ev && ev.stepIndex) ? ev.stepIndex : Math.min(done + 1, total || 1);
  const pct = total ? Math.min(100, Math.round((done / total) * 100)) : 0;
  const title = (ev && ev.checkId) ? (getCheckTitle(suiteKey, ev.checkId) || ev.checkId) : '';

  prog.classList.remove('hidden');
  prog.innerHTML = `
    <div class="rounded-lg bg-slate-950/70 border border-emerald-500/20 px-2.5 py-2 space-y-1.5">
      <div class="flex items-center justify-between gap-2 text-[10px]">
        <span class="text-emerald-300 font-semibold truncate" title="${escAttr('Шаг ' + idx + ' из ' + (total || '—') + (title ? ' — ' + title : ''))}">
          <i class="fa-solid fa-circle-notch fa-spin mr-1"></i>Шаг ${idx} из ${total || '—'}${title ? ' — ' + escapeHtml(title) : ''}
        </span>
        <span class="font-mono text-slate-500 shrink-0">${pct}%</span>
      </div>
      <div class="h-1.5 bg-slate-800 rounded-full overflow-hidden">
        <div class="h-full bg-gradient-to-r from-emerald-500 to-green-400 rounded-full transition-all duration-300" style="width:${pct}%"></div>
      </div>
    </div>
  `;
}

function startSuiteCard(suiteKey) {
  const st = suiteStates[suiteKey];
  if (!st) return;

  const wasRunning = st.running;
  st.running = true;
  st.lastRunId = null;
  st.failedCheckTitle = null;
  Object.keys(st.checks).forEach(id => { st.checks[id] = { status: 'PENDING', message: '' }; });

  if (!wasRunning) bumpActiveRuns(1);

  const card = findSuiteCard(suiteKey);
  if (card) {
    card.classList.add('suite-card-running', 'border-zinc-500', 'ring-1', 'ring-zinc-500/40');
    const badge = card.querySelector('.suite-status-badge');
    if (badge) {
      badge.outerHTML = '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-200 border border-zinc-700 animate-pulse">RUNNING</span>';
    }
    const btn = card.querySelector('button[data-run-btn]');
    if (btn) {
      btn.disabled = true;
      btn.innerHTML = '<i class="fa-solid fa-play"></i><span>Идёт прогон...</span>';
    }
    card.querySelectorAll('.check-item').forEach(item => {
      const icon = item.querySelector('.check-icon');
      if (icon) icon.innerHTML = checkIconHtml('PENDING');
      const failMsg = item.querySelector('.check-msg');
      if (failMsg) failMsg.remove();
    });
    // сброс предыдущего итога, показ пустого прогресса
    const summary = card.querySelector('[data-suite-summary]');
    if (summary) summary.innerHTML = '';
    const prog = card.querySelector('[data-suite-progress]');
    if (prog) {
      prog.classList.remove('hidden');
      prog.innerHTML = `
        <div class="rounded-lg bg-zinc-950 border border-zinc-700 px-2.5 py-2 space-y-1.5">
          <div class="flex items-center justify-between gap-2 text-[10px]">
            <span class="text-zinc-200 font-medium"><i class="fa-solid fa-circle-notch fa-spin mr-1"></i>Подготовка запуска…</span>
            <span class="font-mono text-zinc-400 shrink-0">0%</span>
          </div>
          <div class="h-1.5 bg-zinc-800 rounded-full overflow-hidden">
            <div class="h-full bg-white rounded-full transition-all duration-300" style="width:0%"></div>
          </div>
        </div>`;
    }
  }

  updateRunProgressBar();
}

// Итоговый баннер на карточке после SUMMARY
function buildSummaryBanner(st, success, ev) {
  const dur = ev && ev.durationMs ? fmtDuration(ev.durationMs) : '';
  const runId = (st && st.lastRunId) || (ev && ev.runId) || null;

  // Пропущенная проверка ничего не доказала, поэтому зелёный баннер «все
  // проверки пройдены» над четырьмя пропусками — самый дорогой вид зелёного.
  const skipped = countCheckStates(st, 'SKIPPED');
  let html;
  if (!success) {
    html = '<div class="result-banner fail"><i class="fa-solid fa-circle-xmark mr-1.5"></i>ПРОВАЛ НА ШАГЕ: ' + escapeHtml((st && st.failedCheckTitle) || 'неизвестный шаг') + '</div>';
  } else if (skipped > 0) {
    const passed = countCheckStates(st, 'PASSED');
    html = '<div class="result-banner skip"><i class="fa-solid fa-forward mr-1.5"></i>ПРОЙДЕНО ' + passed +
      ', ПРОПУЩЕНО ' + skipped + '</div>';
  } else {
    html = '<div class="result-banner ok"><i class="fa-solid fa-circle-check mr-1.5"></i>ВСЕ ПРОВЕРКИ ПРОЙДЕНЫ</div>';
  }

  html += '<div class="flex items-center justify-between gap-2">';
  html += '<span class="text-[10px] font-mono text-slate-500">' + (dur ? '<i class="fa-regular fa-clock mr-1"></i>' + dur : '') + '</span>';
  if (runId) {
    html += '<span class="flex items-center gap-1.5">' +
      '<button onclick="openRunDetails(\'' + escAttr(runId) + '\')" title="Открыть отчёт по этому прогону" class="px-2 py-1 text-[10px] bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder rounded-lg transition"><i class="fa-solid fa-file-lines mr-1"></i>Отчёт</button>' +
      '<button onclick="openLogsFiltered(\'' + escAttr(runId) + '\')" title="Все логи этого прогона" class="px-2 py-1 text-[10px] bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder rounded-lg transition"><i class="fa-solid fa-scroll mr-1"></i>Логи прогона</button>' +
      '</span>';
  }
  html += '</div>';
  return html;
}

function finalizeSuiteCard(suiteKey, success, ev) {
  if (!suiteKey || !suiteStates[suiteKey]) return;

  const st = suiteStates[suiteKey];
  const wasRunning = st.running;
  st.running = false;
  st.lastResult = success;
  if (ev && ev.runId) st.lastRunId = ev.runId;
  if (wasRunning) bumpActiveRuns(-1);

  const card = findSuiteCard(suiteKey);
  if (card) {
    card.classList.remove('suite-card-running', 'border-emerald-500/70', 'ring-1', 'ring-emerald-500/40');
    const badge = card.querySelector('.suite-status-badge');
    if (badge) {
      badge.outerHTML = success
        ? '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-bold bg-green-500/20 text-green-400 border border-green-500/30">PASSED</span>'
        : '<span class="suite-status-badge px-2 py-0.5 rounded text-[10px] font-bold bg-red-500/20 text-red-400 border border-red-500/30">FAILED</span>';
    }
    const btn = card.querySelector('button[data-run-btn]');
    if (btn) {
      btn.disabled = false;
      btn.innerHTML = '<i class="fa-solid fa-play"></i><span>Запустить</span>';
    }
    const prog = card.querySelector('[data-suite-progress]');
    if (prog) prog.classList.add('hidden');
    const summary = card.querySelector('[data-suite-summary]');
    if (summary) summary.innerHTML = buildSummaryBanner(st, success, ev);
  }

  updateLastRunBadges();
  updateRunProgressBar();
}

function updateChecklistSummary() {
  let passed = 0, total = 0;
  Object.keys(suiteStates).forEach(key => {
    Object.values(suiteStates[key].checks).forEach(c => {
      total++;
      if (c.status === 'PASSED') passed++;
    });
  });
  const el = document.getElementById('checklistProgressText');
  if (el) el.textContent = `Пройдено: ${passed} / ${total}`;
}

function updateRunProgressBar() {
  let total = 0, done = 0;
  Object.keys(suiteStates).forEach(key => {
    const st = suiteStates[key];
    if (!st.running) return;
    const suite = suitesRegistry.find(s => s.key === key);
    if (!suite) return;
    total += (suite.checks || []).length;
    Object.values(st.checks).forEach(c => {
      if (c.status === 'PASSED' || c.status === 'FAILED' || c.status === 'SKIPPED') done++;
    });
  });
  const fill = document.getElementById('checklistProgressBarFill');
  if (fill) fill.style.width = total > 0 ? `${Math.round((done / total) * 100)}%` : '0%';
}

function toggleBatch(suiteKey, checkboxEl) {
  if (suiteStates[suiteKey]) {
    suiteStates[suiteKey].selected = checkboxEl.checked;
  }
  updateBatchBar();
}

function updateBatchBar() {
  const count = Object.values(suiteStates).filter(s => s.selected).length;
  const counter = document.getElementById('batchCount');
  if (counter) counter.textContent = `Выбрано: ${count}`;
  const btn = document.getElementById('runSelectedBtn');
  if (btn) btn.disabled = count === 0;
}

function markSuitesRunning(keys) {
  keys.forEach(key => startSuiteCard(key));
}

async function runSuiteFromChecklist(suiteKey, btn) {
  if (runsInFlight.has(suiteKey)) return;
  runsInFlight.add(suiteKey);

  try {
    markSuitesRunning([suiteKey]);
    logTerminal('INFO', `[Матрица] Запуск сьюта: ${suiteKey}`);

    const doRun = async () => {
      try {
        const res = await fetch('/api/runs', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ suite: suiteKey }),
        });
        if (!res.ok) {
          throw new Error(`HTTP ${res.status}`);
        }
      } catch (err) {
        finalizeSuiteCard(suiteKey, false);
        logTerminal('ERROR', `[Матрица] Ошибка запуска сьюта ${suiteKey}: ${err.message}`);
        toastError(`Не удалось запустить «${suiteKey}»: ` + err.message);
      }
    };

    if (btn) await busyWrap(btn, doRun);
    else await doRun();
  } finally {
    runsInFlight.delete(suiteKey);
  }
}

async function runSelectedSuites(btn) {
  const keys = Object.keys(suiteStates).filter(k => suiteStates[k].selected);
  if (keys.length === 0) return;

  await busyWrap(btn, async () => {
    markSuitesRunning(keys);
    logTerminal('INFO', `[Матрица] Батч-запуск сютов: ${keys.join(', ')}`);

    try {
      const res = await fetch('/api/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ suites: keys }),
      });
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
      const runs = await res.json();
      const started = Array.isArray(runs) ? runs.length : 1;
      toastSuccess(`Запущено прогонов: ${started}. Статусы обновятся в реальном времени.`);
      logTerminal('INFO', `[Матрица] Запущено прогонов: ${started}. Live-статусы обновятся по SSE.`);
    } catch (err) {
      keys.forEach(k => finalizeSuiteCard(k, false));
      logTerminal('ERROR', `[Матрица] Ошибка батч-запуска: ${err.message}`);
      toastError('Не удалось запустить выбранные сюты: ' + err.message);
    }
  });
  updateBatchBar();
}

function resetChecklist() {
  Object.keys(suiteStates).forEach(key => {
    const st = suiteStates[key];
    if (st.running) bumpActiveRuns(-1);
    st.running = false;
    st.lastResult = null;
    st.lastRunId = null;
    st.failedCheckTitle = null;
    Object.keys(st.checks).forEach(id => {
      st.checks[id] = { status: 'PENDING', message: '' };
    });
  });
  renderSuites();
  logTerminal('INFO', '[Матрица] Локальные статусы чеков сброшены.');
}

// ==========================================
// ОБЗОР (главная вкладка)
// ==========================================

async function refreshOverview() {
  if (typeof loadStands === 'function') loadStands(); // имя активного стенда на Обзоре
  await initStatus();      // стенд + роли
  updateOverviewSuites();  // счётчик сютов из реестра
  await loadHistory();     // история → кэш
  updateOverviewFromCache();
}

// Перерисовка зависящих от истории блоков Обзора без новых запросов
function updateOverviewFromCache() {
  updateOverviewPassRate();
  updateOverviewRecent();
  updateRegressionBanner();
}

function updateOverviewRoles() {
  const roles = [
    ['client', statusInfo.hasClient],
    ['rest', statusInfo.hasRest],
    ['courier', statusInfo.hasCourier],
    ['admin', statusInfo.hasAdmin],
  ];
  let withToken = 0;
  roles.forEach(([role, on]) => {
    const el = document.getElementById('ovRole-' + role);
    if (!el) return;
    if (on) withToken++;
    el.classList.toggle('on', !!on);
    el.classList.toggle('off', !on);
    el.title = on ? 'Токен получен' : 'Токена нет';
  });
  const cnt = document.getElementById('ovRolesCount');
  if (cnt) {
    cnt.textContent = withToken + ' из 4 ' + pluralRu(withToken, ['роли', 'ролей', 'ролей']);
    cnt.className = 'text-[11px] font-semibold ' + (withToken === 4 ? 'text-green-400' : (withToken ? 'text-amber-400' : 'text-slate-400'));
  }
}

function updateOverviewSuites() {
  const total = suitesRegistry.length;
  const custom = suitesRegistry.filter(s => s.category === 'custom').length;
  const el = document.getElementById('ovSuitesTotal');
  if (el) el.textContent = String(total);
  const br = document.getElementById('ovSuitesBreakdown');
  if (br) br.textContent = total ? `встроенных: ${total - custom} · своих: ${custom}` : 'реестр недоступен';
}

function updateOverviewPassRate() {
  const pctEl = document.getElementById('ovPassRate');
  const subEl = document.getElementById('ovPassRateSub');
  if (!pctEl) return;
  const last = (historyCache || []).filter(r => r && r.status && r.status !== 'RUNNING').slice(0, 20);
  if (!last.length) {
    pctEl.textContent = '—';
    pctEl.className = 'text-3xl font-black text-slate-500';
    if (subEl) subEl.textContent = 'нет завершённых прогонов';
    return;
  }
  const passed = last.filter(r => r.status === 'PASSED').length;
  const pct = Math.round((passed / last.length) * 100);
  pctEl.textContent = pct + '%';
  pctEl.className = 'text-3xl font-black ' + (pct >= 50 ? 'text-green-400' : 'text-red-400');
  if (subEl) subEl.textContent = `${passed} из ${last.length} последних прогонов`;
}

function humanSuiteTitle(suiteKey, fallback) {
  const s = suitesRegistry.find(x => x.key === suiteKey);
  return (s && s.title) || fallback || suiteKey || '—';
}

function runStatusIcon(status) {
  if (status === 'PASSED') return 'fa-circle-check text-emerald-400';
  if (status === 'FAILED') return 'fa-circle-xmark text-red-400';
  return 'fa-circle-notch fa-spin text-zinc-400';
}

function updateOverviewRecent() {
  const box = document.getElementById('recentRunsList');
  const empty = document.getElementById('recentRunsEmpty');
  if (!box) return;
  const last = (historyCache || []).slice(0, 6);
  if (!last.length) {
    box.innerHTML = '';
    if (empty) empty.classList.remove('hidden');
    return;
  }
  if (empty) empty.classList.add('hidden');
  box.innerHTML = last.map(r => `
    <button onclick="openRunDetails('${escAttr(r.id || '')}')" class="w-full flex items-center gap-3 px-4 py-2.5 hover:bg-slate-800/60 transition text-left border-b border-darkborder last:border-b-0 group">
      <i class="fa-solid ${runStatusIcon(r.status)} shrink-0"></i>
      <span class="flex-1 min-w-0">
        <span class="block text-xs font-semibold text-white truncate">${escapeHtml(humanSuiteTitle(r.suiteKey, r.suiteName))}</span>
        <span class="block text-[10px] font-mono text-slate-500">${escapeHtml(r.suiteKey || '')}</span>
      </span>
      <span class="text-[10px] text-slate-400 shrink-0 hidden sm:block" title="${escAttr(fmtAbsTimeSec(r.startTime))}">${fmtRelTime(r.startTime)}</span>
      <span class="text-[10px] font-mono text-slate-500 shrink-0 w-20 text-right">${fmtDuration(r.durationMs)}</span>
      <i class="fa-solid fa-chevron-right text-[9px] text-slate-600 group-hover:text-slate-400 shrink-0"></i>
    </button>
  `).join('');
}

// Быстрые действия Обзора: запуск всех тестов заменён кнопкой
// «Регресс после релиза» → openRegressionModal() / startRegression().

function goToChecklists() {
  switchTab('checklists');
}
window.goToChecklists = goToChecklists;
window.gotoChecklists = goToChecklists;

function newScenarioFromOverview() {
  switchTab('scenarios');
  newScenario();
}

// ==========================================
// РЕГРЕСС ПОСЛЕ РЕЛИЗА
// Батч-запуск всех (или выбранных) сютов как одной группы:
// POST /api/runs {"suites":[...], "trigger":"regression"} → массив прогонов,
// у каждого общий groupId. Если выбран не-активный стенд — сначала
// последовательно активируем его (POST /api/stands/{id}/activate),
// дожидаемся загрузки хранилища токенов и только потом запускаем.
// ==========================================

function isRegressionModalOpen() {
  const m = document.getElementById('regressionModal');
  return !!m && !m.classList.contains('hidden');
}

async function openRegressionModal(markAll) {
  if (typeof loadStands === 'function') await loadStands(); // свежий кэш стендов для радио-списка
  if (markAll) selectAllSuitesForBatch();
  renderRegressionStands();
  renderRegressionSuites();
  updateRegressionCounter();
  const modal = document.getElementById('regressionModal');
  if (modal) modal.classList.remove('hidden');
}

// Кнопка «Регресс всех» на чеклистах: выделить все сюты + та же модалка
function openRegressionAll() {
  openRegressionModal(true);
}

function closeRegressionModal() {
  const m = document.getElementById('regressionModal');
  if (m) m.classList.add('hidden');
}

// Выделить все сюты «в батч» на карточках чеклистов
function selectAllSuitesForBatch() {
  suitesRegistry.forEach(s => {
    if (suiteStates[s.key]) suiteStates[s.key].selected = true;
  });
  document.querySelectorAll('[data-suite-card]').forEach(card => {
    const key = card.getAttribute('data-suite-card');
    const cb = card.querySelector('input[type="checkbox"]');
    if (cb && suiteStates[key]) cb.checked = true;
  });
  updateBatchBar();
}

// ---------- Модалка: стенды ----------

// Радио-список стендов из standsCache; активный предвыбран («текущий»)
function renderRegressionStands() {
  const box = document.getElementById('regStandsList');
  if (!box) return;

  // standsCache объявлен через let в stands.js — на window его нет,
  // обращаемся напрямую по глобальному лексическому имени.
  const cache = (typeof standsCache !== 'undefined') ? (standsCache || []) : [];
  if (!cache.length) {
    box.innerHTML = '<div class="py-3 text-center text-slate-500 italic border border-dashed border-darkborder rounded-lg">Стенды не найдены — добавьте через «управлять стендами»</div>';
    updateRegressionStandWarn();
    return;
  }

  box.innerHTML = cache.map(s => {
    const active = !!s.isActive;
    const mockBadge = s.isMock
      ? '<span class="text-[9px] font-bold px-1.5 py-0.5 rounded border bg-amber-500/10 text-amber-400 border-amber-500/30 shrink-0">МОК</span>'
      : '';
    const currentBadge = active
      ? '<span class="text-[9px] font-mono font-medium px-1.5 py-0.5 rounded border bg-zinc-800 text-zinc-300 border-zinc-700 shrink-0">текущий</span>'
      : '';
    return `
      <label class="flex items-start gap-2.5 rounded-lg border px-3 py-2 cursor-pointer transition ${active ? 'border-zinc-500 bg-zinc-800' : 'border-darkborder bg-zinc-900/60 hover:border-zinc-700'}">
        <input type="radio" name="regStand" value="${escAttr(s.id)}" ${active ? 'checked' : ''} onchange="onRegressionStandChange()" class="mt-0.5 shrink-0 accent-zinc-400">
        <span class="flex-1 min-w-0">
          <span class="flex items-center gap-1.5 flex-wrap">
            <span class="font-semibold truncate text-white">${escapeHtml(s.name)}</span>
            ${mockBadge}${currentBadge}
          </span>
          <span class="block font-mono text-[10px] text-zinc-500 truncate">${escapeHtml(s.baseURL || '—')}</span>
        </span>
      </label>`;
  }).join('');
  updateRegressionStandWarn();
}

function onRegressionStandChange() {
  updateRegressionStandWarn();
}

function selectedRegressionStandId() {
  const el = document.querySelector('#regStandsList input[name="regStand"]:checked');
  return el ? el.value : null;
}

// Предупреждение, если выбран НЕ-активный стенд (токены подтянутся из его хранилища)
function updateRegressionStandWarn() {
  const warn = document.getElementById('regStandWarn');
  if (!warn) return;
  const id = selectedRegressionStandId();
  const stand = (id !== null && typeof findStandById === 'function') ? findStandById(id) : null;
  warn.classList.toggle('hidden', !!(stand && stand.isActive));
}

// ---------- Модалка: сюты ----------

// Чекбокс-список всех сютов реестра (включая кастомные), все отмечены по умолчанию
function renderRegressionSuites() {
  const box = document.getElementById('regSuitesList');
  if (!box) return;

  if (!suitesRegistry.length) {
    box.innerHTML = '<div class="sm:col-span-2 py-3 text-center text-zinc-500 italic border border-dashed border-darkborder rounded-lg">Реестр сютов пуст или недоступен — обновите вкладку «Тесты & Чеклисты»</div>';
    return;
  }

  box.innerHTML = suitesRegistry.map(s => {
    const cat = CATEGORY_STYLES[s.category] || CATEGORY_STYLES.flow;
    return `
      <label class="flex items-center gap-2 rounded-lg border border-darkborder bg-zinc-900/60 hover:border-zinc-700 px-2.5 py-1.5 cursor-pointer transition min-w-0">
        <input type="checkbox" value="${escAttr(s.key)}" checked onchange="updateRegressionCounter()" class="rounded border-darkborder bg-zinc-900 accent-zinc-400 shrink-0">
        <span class="flex-1 min-w-0 truncate text-zinc-200" title="${escAttr(s.title)}">${escapeHtml(s.title)}</span>
        <span class="text-[9px] font-mono font-medium px-1.5 py-0.5 rounded border shrink-0 ${cat.badge}">${escapeHtml(cat.ru)}</span>
      </label>`;
  }).join('');
}

function regressionSelectedKeys() {
  return Array.from(document.querySelectorAll('#regSuitesList input[type="checkbox"]:checked')).map(cb => cb.value);
}

function updateRegressionCounter() {
  const n = regressionSelectedKeys().length;
  const counter = document.getElementById('regSelCount');
  if (counter) counter.textContent = 'Выбрано: ' + n;
  const btn = document.getElementById('regStartBtn');
  if (btn) btn.disabled = n === 0;
}

// ---------- Запуск регресса ----------

async function startRegression(btn) {
  const keys = regressionSelectedKeys();
  if (!keys.length) {
    toastError('Отметьте хотя бы один сьют.');
    return;
  }

  const standId = selectedRegressionStandId();
  const stand = (standId !== null && typeof findStandById === 'function') ? findStandById(standId) : null;
  if (!stand) {
    toastError('Выберите стенд для регресса.');
    return;
  }
  const needActivate = !stand.isActive;

  // Параллельные прогоны искажают результаты — предупреждаем
  if (getActiveRunsCount() > 0) {
    const ok = await confirmDialog(
      'Идут другие прогоны',
      'Параллельные прогоны могут исказить результаты. Запустить регресс?',
      'Всё равно запустить'
    );
    if (!ok) return;
  }

  await busyWrap(btn, async () => {
    try {
      // Шаг 1: последовательно — активировать стенд и дождаться загрузки хранилища
      if (needActivate) {
        logTerminal('INFO', '[Регресс] Активация стенда «' + stand.name + '»…');
        const actRes = await fetch('/api/stands/' + encodeURIComponent(stand.id) + '/activate', { method: 'POST' });
        if (!actRes.ok) throw new Error('активация стенда: HTTP ' + actRes.status + ' ' + (await actRes.text()));
        await Promise.all([
          typeof loadStands === 'function' ? loadStands() : Promise.resolve(),
          initStatus(),
          loadVault(),
        ]);
        toastSuccess('Стенд «' + stand.name + '» активирован.');
        logTerminal('SUCCESS', '[Регресс] Стенд «' + stand.name + '» активирован, токены загружены из его хранилища.');
      }

      // Шаг 2: батч-запуск с trigger=regression
      const res = await fetch('/api/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ suites: keys, trigger: 'regression' }),
      });
      if (!res.ok) throw new Error('HTTP ' + res.status + ' ' + (await res.text()));
      const runs = await res.json();

      // Шаг 3: запоминаем группу для плашки на Обзоре
      activeRegressionGroupId = (Array.isArray(runs) && runs[0] && runs[0].groupId) ? runs[0].groupId : null;

      // Ключи сютов берём из ответа бэкенда (на случай расхождений с реестром UI)
      const respKeys = Array.isArray(runs)
        ? runs.map(r => r && r.suiteKey).filter(Boolean)
        : [];
      markSuitesRunning(respKeys.length ? respKeys : keys);
      closeRegressionModal();
      toastSuccess('Регресс запущен: ' + keys.length + ' ' + pluralRu(keys.length, ['сют', 'сюта', 'сютов']) + '.');
      logTerminal('SUCCESS', '[Регресс] Запущено сютов: ' + keys.length +
        (activeRegressionGroupId ? ' (группа ' + String(activeRegressionGroupId).substring(0, 8) + ')' : '') + '.');

      // Шаг 4: таб чеклистов — карточки оживут по SSE; история подтягивается для плашки
      switchTab('checklists');
      loadHistory();
    } catch (err) {
      logTerminal('ERROR', '[Регресс] Ошибка запуска: ' + err.message);
      toastError('Не удалось запустить регресс: ' + err.message);
    }
  });
}

// Esc закрывает модалку регресса (но не поверх открытого confirmDialog)
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  const modal = document.getElementById('regressionModal');
  if (!modal || modal.classList.contains('hidden')) return;
  const confirmBox = document.getElementById('confirmModal');
  if (confirmBox && !confirmBox.classList.contains('hidden')) return;
  closeRegressionModal();
});

// ==========================================
// ИТОГОВАЯ ПЛАШКА РЕГРЕССА НА ОБЗОРЕ («Последние прогоны», сверху)
// После каждого SUMMARY цепочка schedulePostSummaryRefresh → loadHistory →
// updateOverviewFromCache приводит плашку в актуальное состояние: пока в группе
// есть RUNNING — синий прогресс; когда завершились ВСЕ — зелёный/красный итог.
// ==========================================

function updateRegressionBanner() {
  const box = document.getElementById('regressionBannerBox');
  if (!box) return;

  let gid = activeRegressionGroupId;
  if (!gid) {
    // После перезагрузки страницы подхватываем группу, где ещё идут прогоны
    const runningReg = (historyCache || []).find(r =>
      r && r.trigger === 'regression' && r.groupId && (!r.status || r.status === 'RUNNING'));
    gid = runningReg ? runningReg.groupId : null;
  }

  const runs = gid
    ? (historyCache || []).filter(r => r && r.trigger === 'regression' && String(r.groupId) === String(gid))
    : [];
  if (!runs.length) {
    box.classList.add('hidden');
    box.innerHTML = '';
    return;
  }

  const total = runs.length;
  const passed = runs.filter(r => r.status === 'PASSED').length;
  const failed = runs.filter(r => r.status === 'FAILED').length;
  const pending = total - passed - failed;
  const finished = pending === 0;
  const allOk = finished && failed === 0;

  let cls, icon, title, detail;
  if (finished) {
    cls = allOk
      ? 'border-zinc-700 bg-zinc-900 text-zinc-100'
      : 'border-red-900/50 bg-red-950/20 text-red-300';
    icon = allOk ? 'fa-solid fa-circle-check text-emerald-400' : 'fa-solid fa-circle-xmark text-red-400';
    title = allOk ? 'Регресс пройден' : 'Регресс завершён с провалами';
    detail = passed + ' / ' + total + ' пройдено' + (failed ? ' · ' + failed + ' упало' : '');
  } else {
    cls = 'border-zinc-700 bg-zinc-900 text-zinc-200';
    icon = 'fa-solid fa-circle-notch fa-spin text-zinc-400';
    title = 'Регресс выполняется';
    detail = 'готово ' + (passed + failed) + ' из ' + total +
      (passed ? ' · ' + passed + ' пройдено' : '') + (failed ? ' · ' + failed + ' упало' : '') + ' · в очереди ' + pending;
  }

  box.innerHTML =
    '<div class="rounded-xl border ' + cls + ' px-4 py-3 flex items-center gap-3 flex-wrap">' +
      '<i class="' + icon + ' shrink-0"></i>' +
      '<span class="font-semibold text-xs shrink-0">' + escapeHtml(title) + '</span>' +
      '<span class="font-mono text-xs font-medium shrink-0">(' + escapeHtml(detail) + ')</span>' +
      '<button onclick="switchTab(\'checklists\')" class="ml-auto px-3 py-1.5 text-[11px] bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder rounded-lg transition shrink-0">' +
        '<i class="fa-solid fa-list-check mr-1"></i>К карточкам</button>' +
    '</div>';
  box.classList.remove('hidden');
}

// Возврат из «Управления стендами» в модалку регресса: список стендов мог
// измениться (добавили/удалили/переключили) — перерисовываем радио-список.
if (typeof closeStandsModal === 'function') {
  const __baseCloseStandsModal = closeStandsModal;
  window.closeStandsModal = function () {
    __baseCloseStandsModal();
    if (isRegressionModalOpen()) renderRegressionStands();
  };
}

// ==========================================
// TOKEN VAULT & MULTI-PROFILE LOGIC
// ==========================================

async function loadVault() {
  try {
    const res = await fetch('/api/tokens/vault');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const vault = await res.json();
    tokenVault = vault;

    renderRoleProfiles('client', vault.clients || [], vault.activeClientId, 'Добавьте первый JWT клиента кнопкой «Вставить токен» или зарегистрируйте аккаунт.');
    renderRoleProfiles('rest', vault.rests || [], vault.activeRestId, 'Добавьте JWT ресторана или создайте профиль через «Регистрация аккаунта».');
    renderRoleProfiles('courier', vault.couriers || [], vault.activeCourierId, 'Добавьте JWT курьера или сгенерируйте профиль кнопкой «+ Создать».');
    renderRoleProfiles('admin', vault.admins || [], vault.activeAdminId, 'Выполните вход директора через «Логин через API».');

    document.getElementById('clientCountBadge').textContent = `${(vault.clients || []).length} профилей`;
    document.getElementById('restCountBadge').textContent = `${(vault.rests || []).length} профилей`;
    document.getElementById('courierCountBadge').textContent = `${(vault.couriers || []).length} профилей`;
    document.getElementById('adminCountBadge').textContent = `${(vault.admins || []).length} профилей`;

    renderFixtureAccounts(vault);
  } catch (err) {
    console.error('Failed to load token vault:', err);
    tokenVault = null;
    toastError('Не удалось загрузить хранилище токенов: ' + err.message);
    ['client', 'rest', 'courier', 'admin'].forEach(role => {
      const c = document.getElementById(`${role}ProfilesList`);
      if (c) c.innerHTML = loadRetryHtml('Не удалось загрузить хранилище токенов: ' + err.message, 'loadVault');
    });
  }
}

function renderRoleProfiles(role, profiles, activeId, emptyHint) {
  const container = document.getElementById(`${role}ProfilesList`);
  if (!container) return;

  if (profiles.length === 0) {
    container.innerHTML = `
      <div class="text-center py-5 px-3 bg-slate-900/50 rounded-lg border border-dashed border-darkborder space-y-2">
        <i class="fa-regular fa-folder-open text-xl text-slate-600"></i>
        <p class="text-[11px] text-slate-500 leading-relaxed">${escapeHtml(emptyHint || 'Нет сохраненных токенов')}</p>
      </div>`;
    return;
  }

  container.innerHTML = profiles.map(p => {
    const isActive = (p.id === activeId || p.isActive);
    const borderClass = isActive ? 'border-green-500/60 bg-green-950/10' : 'border-darkborder bg-slate-900/80';
    const badgeHtml = isActive
      ? `<span class="px-2 py-0.5 rounded text-[9px] font-bold bg-green-500/20 text-green-400 border border-green-500/30">ACTIVE</span>`
      : `<button onclick="activateToken('${role}', '${escAttr(p.id)}', this)" class="px-2 py-0.5 rounded text-[9px] font-medium bg-slate-800 hover:bg-slate-700 text-slate-300 border border-darkborder transition" title="Сделать токен активным для тестов">Сделать активным</button>`;

    const tokenSnippet = p.token ? `${p.token.substring(0, 24)}...${p.token.substring(p.token.length - 12)}` : 'No Token';

    return `
      <div class="p-3 rounded-lg border ${borderClass} space-y-2 text-xs transition">
        <div class="flex items-center justify-between gap-2">
          <div class="flex items-center space-x-2 min-w-0">
            <span class="font-bold text-white truncate">${escapeHtml(p.name)}</span>
            <span class="text-[11px] font-mono text-slate-400 truncate">${escapeHtml(p.identifier || '')}</span>
          </div>
          <div class="flex items-center space-x-2 shrink-0">
            ${badgeHtml}
            <button onclick="copyProfileToken('${escAttr(p.id)}')" title="Копировать JWT" class="text-slate-400 hover:text-white transition px-1">
              <i class="fa-regular fa-copy"></i>
            </button>
            <button onclick="deleteToken('${role}', '${escAttr(p.id)}', this)" title="Удалить токен" class="text-red-400 hover:text-red-300 transition px-1">
              <i class="fa-solid fa-trash"></i>
            </button>
          </div>
        </div>
        <div class="font-mono text-[10px] text-slate-400 bg-slate-950 p-1.5 rounded border border-darkborder/50 truncate select-text">
          ${tokenSnippet}
        </div>
      </div>
    `;
  }).join('');
}

async function activateToken(role, id, btn) {
  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/activate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, id }),
      });
      if (res.ok) {
        loadVault();
        toastSuccess(`Активный токен роли [${role.toUpperCase()}] переключён.`);
        logTerminal('INFO', `Переключен активный токен для роли [${role.toUpperCase()}].`);
      } else {
        toastError('Ошибка переключения токена: HTTP ' + res.status);
      }
    } catch (err) {
      toastError('Ошибка переключения токена: ' + err.message);
    }
  });
}

async function deleteToken(role, id, btn) {
  const ok = await confirmDialog('Удалить токен?', 'Профиль будет удалён из хранилища. Действие необратимо.', 'Удалить');
  if (!ok) return;

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, id }),
      });
      if (res.ok) {
        loadVault();
        toastSuccess('Токен удалён из хранилища.');
      } else {
        toastError('Ошибка удаления токена: HTTP ' + res.status + ' ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка удаления токена: ' + err.message);
    }
  });
}

async function inlineSaveToken(btn, role) {
  const cap = role.charAt(0).toUpperCase() + role.slice(1);
  const tokenInput = document.getElementById(`inlineToken${cap}`);
  const nameInput = document.getElementById(`inlineName${cap}`);
  if (!tokenInput) return;

  const token = tokenInput.value.trim();
  const name = nameInput ? nameInput.value.trim() : '';

  if (!token) {
    toastError('Вставьте JWT токен в поле перед добавлением.');
    return;
  }

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/add', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, name: name || `${role} inline`, token, setActive: true }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      tokenInput.value = '';
      if (nameInput) nameInput.value = '';
      loadVault();
      toastSuccess(`Токен [${role.toUpperCase()}] сохранён и сделан активным.`);
      logTerminal('SUCCESS', `Инлайн-токен сохранен для роли [${role.toUpperCase()}] и сделан активным.`);
    } catch (err) {
      toastError('Ошибка сохранения инлайн-токена: ' + err.message);
    }
  });
}

function openAddTokenModal() {
  document.getElementById('addTokenModal').classList.remove('hidden');
}

function closeAddTokenModal() {
  document.getElementById('addTokenModal').classList.add('hidden');
}

async function submitAddToken(btn) {
  const role = document.getElementById('addTokenRole').value;
  const name = document.getElementById('addTokenName').value.trim();
  const identifier = document.getElementById('addTokenIdentifier').value.trim();
  const token = document.getElementById('addTokenValue').value.trim();
  const setActive = document.getElementById('addTokenSetActive').checked;

  if (!token) {
    toastError('Введите JWT токен.');
    return;
  }

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/add', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, name, identifier, token, setActive }),
      });
      if (res.ok) {
        closeAddTokenModal();
        loadVault();
        toastSuccess(`Токен [${role.toUpperCase()}] добавлен в хранилище.`);
        logTerminal('SUCCESS', `Добавлен пользовательский токен для роли [${role.toUpperCase()}].`);
      } else {
        toastError('Ошибка добавления токена: ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка: ' + err.message);
    }
  });
}

function openQuickAuthModal() {
  document.getElementById('quickAuthModal').classList.remove('hidden');
  toggleQuickAuthFields();
}

function closeQuickAuthModal() {
  document.getElementById('quickAuthModal').classList.add('hidden');
}

function toggleQuickAuthFields() {
  const role = document.getElementById('quickAuthRole').value;
  const loginInput = document.getElementById('quickAuthLogin');
  const pwdInput = document.getElementById('quickAuthPassword');
  const pwdLabel = document.getElementById('quickAuthPasswordLabel');
  const pwdHint = document.getElementById('quickAuthPasswordHint');

  // Клиент входит по одноразовому коду, а не по паролю: на DEBUG-стенде код
  // приходит в ответе на запрос, поэтому достаточно одного номера.
  const isClient = role === 'client';
  pwdInput.type = isClient ? 'text' : 'password';
  pwdLabel.textContent = isClient ? 'Код из SMS (необязательно)' : 'Пароль';
  pwdHint.classList.toggle('hidden', !isClient);

  if (role === 'admin') {
    loginInput.placeholder = 'Логин директора';
    pwdInput.placeholder = 'Пароль супер-админа';
  } else if (isClient) {
    loginInput.placeholder = '+79991234567';
    pwdInput.placeholder = 'оставьте пустым — код заберётся сам';
  } else if (role === 'rest') {
    loginInput.placeholder = 'rest_login';
    pwdInput.placeholder = 'Пароль ресторана';
  } else if (role === 'courier') {
    loginInput.placeholder = '+79997654321';
    pwdInput.placeholder = 'Пароль курьера';
  }
}

async function submitQuickAuth(btn) {
  const role = document.getElementById('quickAuthRole').value;
  const profileName = document.getElementById('quickAuthProfileName').value.trim();
  const login = document.getElementById('quickAuthLogin').value.trim();
  const password = document.getElementById('quickAuthPassword').value.trim();

  if (!login) {
    toastError(role === 'client' ? 'Укажите номер телефона клиента.' : 'Укажите логин или телефон.');
    return;
  }

  const payload = {
    role,
    profileName,
    login,
    phoneNumber: login,
    password,
    // Для клиента это одноразовый код: пустое значение означает «запроси сам».
    code: role === 'client' ? password : '',
  };

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/auth-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });

      if (res.ok) {
        const profile = await res.json().catch(() => null);
        closeQuickAuthModal();
        loadVault();
        const who = profile && profile.identifier ? ` (${profile.identifier})` : '';
        toastSuccess(`Вход выполнен — токен [${role.toUpperCase()}]${who} сохранён в хранилище.`);
        logTerminal('SUCCESS', `Вход через API выполнен, токен [${role.toUpperCase()}]${who} сохранён.`);
      } else {
        toastError('Ошибка авторизации: ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка сети: ' + err.message);
    }
  });
}

async function generatePresetPool(btn) {
  await busyWrap(btn, async () => {
    logTerminal('INFO', 'Генерация готового пула токенов (2 клиента, 2 ресторана, 2 курьера)...');
    try {
      const res = await fetch('/api/tokens/preset', { method: 'POST' });
      if (res.ok) {
        loadVault();
        toastSuccess('Пул токенов создан: 2×2×2 профиля доступны в хранилище.');
        logTerminal('SUCCESS', 'Пул токенов успешно создан! Все профили доступны в хранилище.');
      } else {
        toastError('Ошибка генерации пресета: HTTP ' + res.status + ' ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка генерации пресета: ' + err.message);
    }
  });
}

async function generateSession(btn, role) {
  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/sessions/generate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role }),
      });
      if (res.ok) {
        loadVault();
        toastSuccess(`Новый аккаунт и токен для роли [${role.toUpperCase()}] созданы.`);
        logTerminal('SUCCESS', `Сгенерирован новый аккаунт и токен для роли [${role.toUpperCase()}].`);
      } else {
        toastError('Ошибка генерации сессии: HTTP ' + res.status + ' ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка генерации сессии: ' + err.message);
    }
  });
}

function copyToClipboard(text, btn) {
  navigator.clipboard.writeText(text).then(() => {
    toastSuccess('Скопировано в буфер обмена.');
  }).catch(() => {
    prompt('Скопируйте вручную:', text);
  });
}

// Копирование JWT по id профиля: сырое значение токена ищется в состоянии
// vault и никогда не попадает в HTML-атрибуты.
function copyProfileToken(profileId) {
  const lists = ['clients', 'rests', 'couriers', 'admins'];
  let profile = null;
  lists.some(list => {
    const found = ((tokenVault && tokenVault[list]) || []).find(p => String(p.id) === String(profileId));
    if (found) {
      profile = found;
      return true;
    }
    return false;
  });
  if (!profile || !profile.token) {
    toastError('Профиль не найден в хранилище — обновите список токенов.');
    return;
  }
  copyToClipboard(profile.token);
}

// ==========================================
// API PLAYGROUND
// ==========================================

async function executePlaygroundAction(btn) {
  const action = document.getElementById('playAction').value;
  const orderId = document.getElementById('playOrderId').value.trim();
  const status = document.getElementById('playStatus').value;

  const payload = { action, orderId, status };

  document.getElementById('playResponse').textContent = '// Отправка запроса...';

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/manual/action', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });

      if (!res.ok) {
        throw new Error('HTTP ' + res.status + ': ' + (await res.text()));
      }

      const data = await res.json();
      document.getElementById('playResponse').textContent = JSON.stringify(data, null, 2);

      if (data.order_id) {
        document.getElementById('playOrderId').value = data.order_id;
      }
    } catch (err) {
      document.getElementById('playResponse').textContent = `// Ошибка: ${err.message}`;
      toastError('Ошибка запроса playground: ' + err.message);
    }
  });
}

// ==========================================
// HISTORY
// ==========================================

let historyFilterStatus = 'all'; // 'all' | 'PASSED' | 'FAILED'
let historySearchQuery = '';

function setHistoryFilter(status) {
  historyFilterStatus = status || 'all';
  const group = document.getElementById('historyFilterGroup');
  if (group) {
    group.querySelectorAll('button').forEach(btn => {
      btn.className = 'px-2.5 py-1 text-xs font-medium rounded-lg bg-zinc-900 hover:bg-zinc-800 text-zinc-300 transition';
    });
    const active = document.getElementById(`hf-${historyFilterStatus.toLowerCase()}`);
    if (active) {
      active.className = historyFilterStatus === 'FAILED'
        ? 'px-2.5 py-1 text-xs font-semibold rounded-lg bg-red-950/40 text-red-300 border border-red-800/40 transition'
        : 'px-2.5 py-1 text-xs font-semibold rounded-lg bg-zinc-800 text-white transition';
    }
  }
  renderHistoryTable();
}
window.setHistoryFilter = setHistoryFilter;

function filterHistory(query) {
  historySearchQuery = (query || '').toLowerCase().trim();
  renderHistoryTable();
}
window.filterHistory = filterHistory;

function renderHistoryTable() {
  const tbody = document.getElementById('historyTableBody');
  if (!tbody) return;

  if (!historyCache.length) {
    tbody.innerHTML = `<tr><td colspan="8">${emptyStateHtml(
      'fa-flask',
      'Пока нет прогонов. Запустите тесты во вкладке «Тесты и чеклисты» или нажмите «Flow A» на Обзоре.',
      '<button onclick="goToChecklists()" class="px-3 py-1.5 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm transition"><i class="fa-solid fa-list-check mr-1"></i>К чеклистам</button>'
    )}</td></tr>`;
    return;
  }

  let filtered = historyCache;
  if (historyFilterStatus !== 'all') {
    filtered = filtered.filter(r => r.status === historyFilterStatus);
  }
  if (historySearchQuery) {
    filtered = filtered.filter(r =>
      (r.id && r.id.toLowerCase().includes(historySearchQuery)) ||
      (r.suiteKey && r.suiteKey.toLowerCase().includes(historySearchQuery)) ||
      (r.suiteName && r.suiteName.toLowerCase().includes(historySearchQuery))
    );
  }

  if (!filtered.length) {
    tbody.innerHTML = `<tr><td colspan="8" class="p-8 text-center text-xs text-zinc-500 italic">Нет прогонов, соответствующих выбранному фильтру</td></tr>`;
    return;
  }

  tbody.innerHTML = filtered.map(r => {
    const statusBadge = r.status === 'PASSED'
      ? '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-100 border border-zinc-700"><i class="fa-solid fa-circle-check mr-1 text-emerald-400"></i>PASSED</span>'
      : (r.status === 'FAILED'
          ? '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-red-950/40 text-red-300 border border-red-800/40"><i class="fa-solid fa-circle-xmark mr-1 text-red-400"></i>FAILED</span>'
          : '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-300 border border-zinc-700 animate-pulse"><i class="fa-solid fa-spinner fa-spin mr-1 text-zinc-400"></i>RUNNING</span>');

    const checksCell = (r.passedChecks !== undefined && r.totalChecks)
      ? `<span class="${r.failedChecks ? 'text-red-400' : 'text-zinc-100'} font-semibold">${r.passedChecks}</span><span class="text-zinc-500">/${r.totalChecks}</span>`
      : '<span class="text-zinc-500">—</span>';

    const stepsCell = (r.passedSteps !== undefined && r.totalSteps)
      ? `${r.passedSteps}<span class="text-zinc-500">/${r.totalSteps}</span>`
      : (r.passedSteps !== undefined ? r.passedSteps : '—');

    const runId = escAttr(r.id || '');

    // Триггер запуска: регресс после релиза / webhook (задел на будущее)
    const triggerBadge = r.trigger === 'regression'
      ? '<span class="shrink-0 px-1.5 py-0.5 rounded text-[9px] font-mono font-semibold bg-zinc-800 text-zinc-300 border border-zinc-700 whitespace-nowrap" title="Запущено как регресс после релиза"><i class="fa-solid fa-play text-[8px] mr-1"></i>Регресс</span>'
      : (r.trigger === 'webhook'
          ? '<span class="shrink-0 px-1.5 py-0.5 rounded text-[9px] font-mono font-semibold bg-zinc-800 text-zinc-300 border border-zinc-700 whitespace-nowrap" title="Запущено вебхуком"><i class="fa-solid fa-link text-[8px] mr-1"></i>Webhook</span>'
          : '');

    return `<tr onclick="openRunDetails('${runId}')" class="cursor-pointer hover:bg-zinc-800/40 transition">
      <td class="p-3 font-mono text-[11px] text-zinc-400" title="${runId}">${runId.substring(0, 8)}...</td>
      <td class="p-3 font-semibold text-white"><span class="flex items-center gap-1.5 min-w-0"><span class="truncate">${escapeHtml(humanSuiteTitle(r.suiteKey, r.suiteName))}</span>${triggerBadge}</span></td>
      <td class="p-3 font-mono text-[11px] text-zinc-300">${escapeHtml(r.suiteKey || '—')}</td>
      <td class="p-3">${statusBadge}</td>
      <td class="p-3 font-mono">${checksCell}</td>
      <td class="p-3 font-mono">${stepsCell}</td>
      <td class="p-3 font-mono text-zinc-400" title="Длительность прогона">${fmtDuration(r.durationMs)}</td>
      <td class="p-3 text-zinc-400" title="${escAttr(fmtAbsTimeSec(r.startTime))}">${fmtRelTime(r.startTime)}</td>
    </tr>`;
  }).join('');
}

async function loadHistory() {
  try {
    const res = await fetch('/api/runs');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const runs = await res.json();

    historyCache = Array.isArray(runs) ? runs : [];
    updateLastRunBadges();
    updateOverviewFromCache();
    renderHistoryTable();
  } catch (err) {
    console.error('Failed to load history:', err);
    if (tbody) {
      tbody.innerHTML = `<tr><td colspan="8">${loadRetryHtml('Не удалось загрузить историю прогонов: ' + err.message, 'loadHistory')}</td></tr>`;
    }
  }
}

// ==========================================
// RUN DETAILS («ОТЧЁТ») — вертикальный таймлайн
// ==========================================

function openRunDetails(id) {
  if (!id) return;
  currentRunDetailsId = id;
  const modal = document.getElementById('runDetailsModal');
  modal.classList.remove('hidden');
  document.getElementById('runDetailsBody').innerHTML = '<div class="text-slate-500 italic text-center py-12"><i class="fa-solid fa-spinner fa-spin mr-2"></i>Загрузка деталей прогона...</div>';

  fetch(`/api/runs/${encodeURIComponent(id)}`)
    .then(res => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    })
    .then(run => renderRunDetails(run))
    .catch(err => {
      document.getElementById('runDetailsBody').innerHTML =
        `<div class="py-8 text-center space-y-3">
          <i class="fa-solid fa-triangle-exclamation text-2xl text-red-400/70"></i>
          <p class="text-xs text-red-400 break-all">Не удалось загрузить детали прогона: ${escapeHtml(err.message)}</p>
          <button onclick="openRunDetails('${escAttr(id)}')" class="px-3 py-1.5 text-xs bg-slate-800 hover:bg-slate-700 text-slate-200 border border-darkborder rounded-lg transition"><i class="fa-solid fa-arrows-rotate mr-1"></i>Повторить</button>
        </div>`;
    });
}

function closeRunDetails() {
  document.getElementById('runDetailsModal').classList.add('hidden');
}

function openLogsForDetailsRun() {
  openLogsFiltered(currentRunDetailsId);
}

async function downloadReport(id, fmt) {
  const runId = id || currentRunDetailsId;
  if (!runId) {
    toastError('Прогон не выбран — сначала откройте отчёт по прогону.');
    return;
  }
  const format = fmt === 'allure' ? 'allure' : 'junit';
  try {
    const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/export/${format}`);
    if (!res.ok) {
      toastError(`Не удалось скачать отчёт (HTTP ${res.status}).`);
      return;
    }
    const blob = await res.blob();
    const cd = res.headers.get('Content-Disposition') || '';
    const m = cd.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i);
    const fallback = `e2e-${format}-${runId.substring(0, 8)}.${format === 'allure' ? 'zip' : 'xml'}`;
    let filename = fallback;
    if (m && m[1]) {
      try { filename = decodeURIComponent(m[1].trim()); } catch (e) { filename = m[1].trim(); }
    }
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1500);
    toastInfo('Отчёт скачивается…');
  } catch (err) {
    toastError('Ошибка скачивания отчёта: ' + err.message);
  }
}

function getCheckTitle(suiteKey, checkId) {
  const suite = suitesRegistry.find(s => s.key === suiteKey);
  if (suite) {
    const check = (suite.checks || []).find(c => c.id === checkId);
    if (check) return check.title;
  }
  return '';
}

function resultStatusBadge(status) {
  switch (status) {
    case 'PASSED':  return '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-100 border border-zinc-700">PASSED</span>';
    case 'FAILED':  return '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-red-950/40 text-red-300 border border-red-800/40">FAILED</span>';
    case 'SKIPPED': return '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-400 border border-zinc-700">SKIPPED</span>';
    case 'RUNNING': return '<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-200 border border-zinc-700 animate-pulse">RUNNING</span>';
    default:        return `<span class="px-2 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-400">${escapeHtml(status || '—')}</span>`;
  }
}

// Переводчик типовых ошибок → человеческая подсказка
function translateErrorHint(msg) {
  const m = String(msg || '');
  if (/401|unauthorized/i.test(m)) {
    return 'Нет доступа: нужен действующий токен роли. Проверьте вкладку «Токены и доступ».';
  }
  if (/403|forbidden|permission denied/i.test(m)) {
    return 'Доступ запрещён: у этой роли нет прав на операцию.';
  }
  if (/\b400\b/.test(m)) {
    return 'Бэкенд отклонил запрос — проверьте параметры шага.';
  }
  if (/неверный код верификации|код верификации|verification\s*code/i.test(m)) {
    return 'Реальный бэкенд требует код из SMS/Telegram. Задайте VERIFICATION_CODE или используйте готовые токены.';
  }
  if (/connection refused|timeout|deadline|no such host|i\/o timeout|context canceled/i.test(m)) {
    return 'Бэкенд недоступен: проверьте адрес стенда в настройках.';
  }
  return null;
}

// httpDetails события, относящиеся к конкретному чеку (между его CHECK_START и следующим)
function httpDetailsForCheck(run, checkId) {
  let current = null;
  const events = run.events || [];
  for (const ev of events) {
    if (ev && ev.stepType === 'CHECK_START' && ev.checkId) {
      current = ev.checkId;
      continue;
    }
    if (ev && ev.httpDetails && current === checkId) {
      return ev.httpDetails;
    }
  }
  return null;
}

function httpStatusClass(code) {
  if (code >= 500) return 'text-red-400 font-bold';
  if (code >= 400) return 'text-amber-400 font-bold';
  if (code >= 300) return 'text-zinc-300 font-bold';
  if (code >= 200) return 'text-zinc-100 font-bold';
  return 'text-zinc-400';
}

function tlDotIcon(status) {
  switch (status) {
    case 'PASSED':  return '<i class="fa-solid fa-check text-emerald-400"></i>';
    case 'FAILED':  return '<i class="fa-solid fa-xmark text-red-400"></i>';
    case 'SKIPPED': return '<i class="fa-solid fa-minus text-zinc-500"></i>';
    case 'RUNNING': return '<i class="fa-solid fa-circle-notch fa-spin text-zinc-400"></i>';
    default:        return '<i class="fa-regular fa-circle text-zinc-600"></i>';
  }
}

function renderRunDetails(run) {
  const title = document.getElementById('runDetailsTitle');
  title.innerHTML = `<i class="fa-solid fa-chart-line text-zinc-400"></i><span>Отчёт: ${escapeHtml(humanSuiteTitle(run.suiteKey, run.suiteName || run.id))}</span>`;

  const exportJunitBtn = document.getElementById('runExportJunitBtn');
  const exportAllureBtn = document.getElementById('runExportAllureBtn');
  const exportRunId = escAttr(run.id || '');
  if (exportJunitBtn) exportJunitBtn.setAttribute('onclick', `downloadReport('${exportRunId}','junit')`);
  if (exportAllureBtn) exportAllureBtn.setAttribute('onclick', `downloadReport('${exportRunId}','allure')`);

  // --- Шапка ---
  const statusBanner = run.status === 'PASSED'
    ? '<span class="px-3 py-1 rounded-lg text-xs font-mono font-medium bg-zinc-800 text-zinc-100 border border-zinc-600"><i class="fa-solid fa-circle-check mr-1.5 text-emerald-400"></i>Пройден</span>'
    : (run.status === 'FAILED'
        ? '<span class="px-3 py-1 rounded-lg text-xs font-mono font-medium bg-red-950/40 text-red-300 border border-red-800/40"><i class="fa-solid fa-circle-xmark mr-1.5 text-red-400"></i>Провален</span>'
        : '<span class="px-3 py-1 rounded-lg text-xs font-mono font-medium bg-zinc-800 text-zinc-200 border border-zinc-700 animate-pulse"><i class="fa-solid fa-hourglass-half mr-1.5 text-zinc-400"></i>Выполняется</span>');

  const results = run.results || {};
  const entries = Object.entries(results);
  const passedCount = run.passedChecks !== undefined ? run.passedChecks : entries.filter(([, v]) => v && v.status === 'PASSED').length;
  const failedCount = run.failedChecks !== undefined ? run.failedChecks : entries.filter(([, v]) => v && v.status === 'FAILED').length;
  const skippedCount = entries.filter(([, v]) => v && v.status === 'SKIPPED').length;
  const totalCount = run.totalChecks || entries.length;

  const checksSummary = skippedCount > 0
    ? `Проверки: ${passedCount} из ${totalCount} пройдено, ${skippedCount} пропущено`
    : `Проверки: ${passedCount} из ${totalCount} пройдено`;

  const metaEl = document.getElementById('runDetailsFooterMeta');
  if (metaEl) {
    metaEl.textContent = `${fmtAbsTimeSec(run.startTime)} · ${fmtDuration(run.durationMs)} · run ${(run.id || '').substring(0, 8)}`;
  }

  const errorBlock = run.error
    ? (() => {
        const hint = translateErrorHint(run.error);
        return `<div class="bg-red-500/10 border border-red-500/40 rounded-lg p-3 space-y-1">
          <div class="text-red-300 font-mono break-all text-[11px]"><i class="fa-solid fa-bug mr-1"></i><b>Ошибка:</b> ${escapeHtml(run.error)}</div>
          ${hint ? `<div class="text-amber-200/90 text-[11px]"><i class="fa-solid fa-lightbulb mr-1 text-amber-400"></i>${hint}</div>` : ''}
        </div>`;
      })()
    : '';

  // --- Таймлайн: порядок шагов из последовательности CHECK_START ---
  const orderedIds = [];
  (run.events || []).forEach(ev => {
    if (ev && ev.stepType === 'CHECK_START' && ev.checkId && !orderedIds.includes(ev.checkId)) {
      orderedIds.push(ev.checkId);
    }
  });
  entries.forEach(([checkId]) => {
    if (!orderedIds.includes(checkId)) orderedIds.push(checkId);
  });

  const timelineHtml = orderedIds.map(checkId => {
    const res = results[checkId] || null;
    const status = res ? res.status : 'UNKNOWN';
    const titleText = getCheckTitle(run.suiteKey, checkId) || (res && res.title) || checkId;
    const dur = res && res.durationMs !== undefined && status !== 'UNKNOWN'
      ? `<span class="ml-auto text-[10px] font-mono text-slate-500 shrink-0">${fmtDuration(res.durationMs)}</span>` : '';

    let body = '';
    if (status === 'FAILED') {
      const msg = (res && res.message) || '';
      const hint = translateErrorHint(msg);
      body = `<div class="mt-1.5 rounded-lg bg-red-500/10 border border-red-500/40 p-2.5 space-y-1">
        <div class="text-red-300 break-all"><i class="fa-solid fa-triangle-exclamation mr-1"></i>${escapeHtml(typeof tidyEventMessage === 'function' ? tidyEventMessage(msg) : msg)}</div>
        ${hint ? `<div class="text-amber-200/90 text-[11px] leading-relaxed pt-0.5 border-t border-red-500/20 mt-1 pt-1.5"><i class="fa-solid fa-lightbulb mr-1 text-amber-400"></i>${hint}</div>` : ''}
      </div>`;
    } else if (status === 'PASSED') {
      const hd = httpDetailsForCheck(run, checkId);
      const tech = hd
        ? `${hd.method} ${hd.url || ''} → ${hd.statusCode}`
        : (res && res.message ? (typeof tidyEventMessage === 'function' ? tidyEventMessage(res.message) : res.message) : '');
      body = tech
        ? `<div class="mt-0.5 font-mono text-[10px] text-slate-500 break-all">${escapeHtml(tech)}</div>` : '';
    } else if (status === 'SKIPPED') {
      body = (res && res.message)
        ? `<div class="mt-0.5 text-[10px] text-slate-500 italic break-all">${escapeHtml(res.message)}</div>` : '';
    } else if (status === 'RUNNING') {
      body = '<div class="mt-0.5 text-[10px] text-zinc-400 italic">шаг выполняется…</div>';
    }

    return `<div class="tl-item">
      <div class="tl-dot">${tlDotIcon(status)}</div>
      <div class="flex items-start gap-2 min-w-0">
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="text-xs font-semibold ${status === 'FAILED' ? 'text-red-300' : (status === 'PASSED' ? 'text-slate-200' : 'text-slate-400')}">${escapeHtml(titleText)}</span>
            ${resultStatusBadge(status)}
          </div>
          ${body}
        </div>
        ${dur}
      </div>
    </div>`;
  }).join('');

  // --- Технические детали (скрытый блок) ---
  const events = run.events || [];
  const eventsHtml = events.length
    ? events.map(ev => {
        if (typeof ev === 'string') {
          return `<div class="whitespace-pre-wrap break-all">${escapeHtml(ev)}</div>`;
        }
        const t = ev.timestamp ? timeHMS(Date.parse(ev.timestamp)) : '--:--:--';
        const lvlClass = ev.level === 'ERROR' ? 'text-red-400 font-bold'
          : (ev.level === 'SUCCESS' ? 'text-green-400 font-semibold'
            : (ev.level === 'WARN' ? 'text-amber-400' : 'text-slate-400'));
        const dur = ev.durationMs ? ` <span class="text-slate-600">${escapeHtml(String(ev.durationMs))}ms</span>` : '';
        return `<div class="whitespace-pre-wrap break-all"><span class="text-slate-600">${t}</span> <span class="${lvlClass}">[${escapeHtml(ev.level || ev.stepType || '')}]</span> ${escapeHtml(ev.stepType ? `[${ev.stepType}] ` : '')}${escapeHtml(ev.message || '')}${dur}</div>`;
      }).join('')
    : '<div class="text-slate-500 italic">Лог событий отсутствует</div>';

  const httpEvents = events.filter(ev => ev && ev.httpDetails);
  const httpBlocks = httpEvents.length
    ? httpEvents.map(ev => {
        const hd = ev.httpDetails;
        const reqJson = hd.request !== undefined && hd.request !== null
          ? `<div class="mt-1"><span class="text-[9px] uppercase tracking-wider text-slate-500 font-bold">Request</span><pre class="bg-slate-950 border border-darkborder rounded p-2 mt-0.5 max-h-40 overflow-auto text-[10px] text-slate-300 whitespace-pre-wrap break-all">${escapeHtml(JSON.stringify(hd.request, null, 2))}</pre></div>` : '';
        const respJson = hd.response !== undefined && hd.response !== null
          ? `<div class="mt-1"><span class="text-[9px] uppercase tracking-wider text-slate-500 font-bold">Response</span><pre class="bg-slate-950 border border-darkborder rounded p-2 mt-0.5 max-h-40 overflow-auto text-[10px] text-slate-300 whitespace-pre-wrap break-all">${escapeHtml(JSON.stringify(hd.response, null, 2))}</pre></div>` : '';
        return `<div class="rounded-lg border border-darkborder bg-slate-900/60 p-2.5">
          <div class="flex items-center gap-2 flex-wrap font-mono text-[11px]">
            <span class="font-bold text-white">${escapeHtml(hd.method || '?')}</span>
            <span class="text-slate-400 break-all">${escapeHtml(hd.url || '')}</span>
            <span class="${httpStatusClass(hd.statusCode)}">→ ${hd.statusCode}</span>
            ${hd.role ? `<span class="px-1.5 py-0.5 rounded bg-slate-800 text-slate-400 text-[9px] border border-darkborder">role: ${escapeHtml(hd.role)}</span>` : ''}
          </div>
          ${reqJson}
          ${respJson}
        </div>`;
      }).join('')
    : '<div class="text-slate-500 italic">HTTP-деталей нет</div>';

  document.getElementById('runDetailsBody').innerHTML = `
    <div class="space-y-3">
      <div class="flex items-center justify-between gap-3 flex-wrap">
        ${statusBanner}
        <span class="text-[11px] text-slate-400 font-medium">${escapeHtml(checksSummary)}</span>
      </div>

      <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs">
        <div class="stat-card !p-3">
          <div class="text-[9px] uppercase tracking-wider text-slate-500 font-bold mb-1">Начало</div>
          <div class="font-mono text-slate-200">${fmtAbsTimeSec(run.startTime)}</div>
        </div>
        <div class="stat-card !p-3">
          <div class="text-[9px] uppercase tracking-wider text-slate-500 font-bold mb-1">Длительность</div>
          <div class="font-mono text-slate-200">${fmtDuration(run.durationMs)}</div>
        </div>
        <div class="stat-card !p-3">
          <div class="text-[9px] uppercase tracking-wider text-slate-500 font-bold mb-1">Сьют</div>
          <div class="text-slate-200 font-semibold truncate" title="${escAttr(run.suiteKey || '')}">${escapeHtml(run.suiteKey || '—')}</div>
        </div>
        <div class="stat-card !p-3">
          <div class="text-[9px] uppercase tracking-wider text-zinc-500 font-bold mb-1">Чеки (OK / Сбой)</div>
          <div class="font-mono font-semibold"><span class="text-zinc-100">${passedCount}</span> / <span class="${failedCount ? 'text-red-400' : 'text-zinc-500'}">${failedCount}</span></div>
        </div>
      </div>

      ${errorBlock}

      <div>
        <h4 class="text-[10px] font-bold text-zinc-300 uppercase tracking-wider mb-3"><i class="fa-solid fa-list-ol mr-1 text-zinc-400"></i>Ход выполнения (${orderedIds.length} ${pluralRu(orderedIds.length, ['шаг', 'шага', 'шагов'])})</h4>
        <div class="space-y-0">${orderedIds.length ? timelineHtml : '<div class="text-zinc-500 italic text-sm">Результаты проверок отсутствуют.</div>'}</div>
      </div>

      <details class="border border-darkborder rounded-lg bg-zinc-900/50 text-xs">
        <summary class="px-3 py-2 cursor-pointer select-none font-semibold text-zinc-300 flex items-center gap-2">
          <i class="fa-solid fa-code text-zinc-400"></i>
          <span>Технические детали (сырой лог${events.length ? ', ' + events.length + ' событий' : ''})</span>
          <i class="fa-solid fa-chevron-down chev ml-auto text-[10px] text-slate-500"></i>
        </summary>
        <div class="px-3 pb-3 space-y-3">
          <pre class="bg-slate-950 border border-darkborder rounded-lg p-3 max-h-72 overflow-auto font-mono text-[10px] leading-relaxed text-slate-300 select-text">${eventsHtml}</pre>
          <div class="space-y-2">
            <div class="text-[9px] uppercase tracking-wider text-slate-500 font-bold">HTTP-вызовы (${httpEvents.length})</div>
            ${httpBlocks}
          </div>
        </div>
      </details>
    </div>
  `;
}

// ==========================================
// MANUAL REGISTRATION MODAL
// ==========================================

function openRegisterModal() {
  document.getElementById('registerModal').classList.remove('hidden');
  toggleRegisterFields();
}

function closeRegisterModal() {
  document.getElementById('registerModal').classList.add('hidden');
}

function toggleRegisterFields() {
  const role = document.getElementById('regRole').value;
  const restNameGroup = document.getElementById('regFieldRestName');
  const restToggleGroup = document.getElementById('regFieldRestToggle');
  const loginGroup = document.getElementById('regFieldLogin');
  const passwordGroup = document.getElementById('regFieldPassword');
  const phoneGroup = document.getElementById('regFieldPhone');
  const namesGroup = document.getElementById('regFieldNames');
  const deliveryTypeGroup = document.getElementById('regFieldDeliveryType');
  const addressGroup = document.getElementById('regFieldAddress');
  const codeGroup = document.getElementById('regFieldCode');
  const cityGroup = document.getElementById('regFieldCity');

  restNameGroup.classList.add('hidden');
  restToggleGroup.classList.add('hidden');
  loginGroup.classList.add('hidden');
  passwordGroup.classList.add('hidden');
  phoneGroup.classList.add('hidden');
  namesGroup.classList.add('hidden');
  deliveryTypeGroup.classList.add('hidden');
  addressGroup.classList.add('hidden');
  codeGroup.classList.add('hidden');
  cityGroup.classList.add('hidden');

  if (role === 'rest') {
    restNameGroup.classList.remove('hidden');
    restToggleGroup.classList.remove('hidden');
    loginGroup.classList.remove('hidden');
    passwordGroup.classList.remove('hidden');
    cityGroup.classList.remove('hidden');
  } else if (role === 'courier') {
    namesGroup.classList.remove('hidden');
    phoneGroup.classList.remove('hidden');
    loginGroup.classList.remove('hidden');
    passwordGroup.classList.remove('hidden');
    deliveryTypeGroup.classList.remove('hidden');
    cityGroup.classList.remove('hidden');
  } else if (role === 'client') {
    phoneGroup.classList.remove('hidden');
    namesGroup.classList.remove('hidden');
    addressGroup.classList.remove('hidden');
    codeGroup.classList.remove('hidden');
    cityGroup.classList.remove('hidden');
  } else if (role === 'admin') {
    loginGroup.classList.remove('hidden');
    passwordGroup.classList.remove('hidden');
  }
}

async function submitRegister(btn) {
  const role = document.getElementById('regRole').value;
  const profileName = document.getElementById('regProfileName').value.trim();
  const restName = document.getElementById('regRestName').value.trim();
  const login = document.getElementById('regLogin').value.trim();
  const password = document.getElementById('regPassword').value.trim();
  const phoneNumber = document.getElementById('regPhone').value.trim();
  const firstName = document.getElementById('regFirstName').value.trim();
  const lastName = document.getElementById('regLastName').value.trim();
  const deliveryType = document.getElementById('regDeliveryType').value;
  const address = document.getElementById('regAddress').value.trim();
  const code = document.getElementById('regCode').value.trim();
  const city = document.getElementById('regCity').value.trim();
  const loginOnly = document.getElementById('regRestLoginOnly').checked;

  const payload = {
    role,
    profileName,
    restName,
    login,
    password,
    phoneNumber,
    firstName,
    lastName,
    deliveryType,
    address,
    code,
    cityKey: city,
    city,
    deliveryMethod: 'locali',
    loginOnly
  };

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/tokens/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });

      if (res.ok) {
        closeRegisterModal();
        loadVault();
        toastSuccess(`Аккаунт зарегистрирован — JWT для роли [${role.toUpperCase()}] получен.`);
        logTerminal('SUCCESS', `Аккаунт и профиль успешно зарегистрированы! JWT получен для роли [${role.toUpperCase()}].`);
      } else {
        toastError('Ошибка регистрации: ' + (await res.text()));
      }
    } catch (err) {
      toastError('Ошибка сети: ' + err.message);
    }
  });
}

// ==========================================
// Карточки custom-сютов: действия + бейджи последнего прогона
// ==========================================

function customActionsHtml(suite) {
  if (suite.category !== 'custom') return '';
  return `
    <button onclick="openScenarioInEditor('${escAttr(suite.key)}')" title="Открыть в редакторе сценариев" class="px-2 py-1.5 text-[11px] bg-slate-800 hover:bg-slate-700 text-slate-200 border border-darkborder rounded-lg transition shrink-0">
      <i class="fa-solid fa-pen"></i>
    </button>
    <button onclick="deleteCustomScenario('${escAttr(suite.key)}')" title="Удалить сценарий" class="px-2 py-1.5 text-[11px] bg-slate-800 hover:bg-red-950 text-red-400 border border-darkborder rounded-lg transition shrink-0">
      <i class="fa-solid fa-trash"></i>
    </button>`;
}

function latestRunForSuite(suiteKey) {
  let best = null;
  (historyCache || []).forEach(r => {
    if (!r || r.suiteKey !== suiteKey) return;
    const t = r.startTime ? new Date(r.startTime).getTime() : 0;
    if (!best || t > best.t) best = { t, r };
  });
  return best ? { status: best.r.status, startTime: best.r.startTime } : null;
}

function updateLastRunBadges() {
  document.querySelectorAll('[data-last-run-badge]').forEach(el => {
    const key = el.getAttribute('data-last-run-badge');
    const info = latestRunForSuite(key);
    if (!info || !info.status || info.status === 'RUNNING') {
      el.innerHTML = '';
      return;
    }
    const ok = info.status === 'PASSED';
    const time = info.startTime ? fmtAbsTime(info.startTime) : '';
    el.innerHTML = `Последний прогон: <span class="${ok ? 'text-green-400' : 'text-red-400'} font-bold">${escapeHtml(ok ? 'пройден' : 'провален')}</span>${time ? ' · ' + time : ''}`;
  });
}

// ---------------------------------------------------------------------------
// Аккаунты, под которыми идут прогоны
//
// По умолчанию каждый сьют создаёт свои аккаунты, и прогоны не мешают друг
// другу. Привязка меняет это на конкретный профиль — когда на стенде есть
// подготовленные вручную данные, которых у сгенерированной фикстуры нет.
// ---------------------------------------------------------------------------

const FIXTURE_ROLE_FIELDS = [
  { role: 'client', select: 'fixtureAccountClient', hint: 'fixtureAccountClientHint', pool: 'clients' },
  { role: 'rest', select: 'fixtureAccountRest', hint: 'fixtureAccountRestHint', pool: 'rests' },
  { role: 'courier', select: 'fixtureAccountCourier', hint: 'fixtureAccountCourierHint', pool: 'couriers' },
  { role: 'admin', select: 'fixtureAccountAdmin', hint: 'fixtureAccountAdminHint', pool: 'admins' },
];

function renderFixtureAccounts(vault) {
  const bound = (vault && vault.testAccounts) || {};

  FIXTURE_ROLE_FIELDS.forEach(({ role, select, hint, pool }) => {
    const el = document.getElementById(select);
    if (!el) return;

    const profiles = (vault && vault[pool]) || [];
    // Сохраняем выбор оператора, если он ещё не применён: перерисовка списка
    // не должна молча сбрасывать несохранённый выбор.
    const previous = el.dataset.dirty === '1' ? el.value : (bound[role] || '');

    el.innerHTML = '<option value="">Создавать нового</option>' +
      profiles.map(p => {
        const label = p.identifier ? `${p.name} — ${p.identifier}` : p.name;
        return `<option value="${escapeHtml(p.id)}">${escapeHtml(label)}</option>`;
      }).join('');

    el.value = profiles.some(p => p.id === previous) ? previous : '';
    el.onchange = () => { el.dataset.dirty = '1'; updateFixtureAccountHint(role); };
    updateFixtureAccountHint(role);
  });
}

function updateFixtureAccountHint(role) {
  const field = FIXTURE_ROLE_FIELDS.find(f => f.role === role);
  if (!field) return;
  const el = document.getElementById(field.select);
  const hintEl = document.getElementById(field.hint);
  if (!el || !hintEl) return;

  if (!el.value) {
    hintEl.textContent = 'новый аккаунт на каждый прогон';
    hintEl.className = 'text-[10px] text-slate-500 truncate';
    return;
  }

  const profile = ((tokenVault && tokenVault[field.pool]) || []).find(p => p.id === el.value);
  const entityId = profile && profile.payload ? (profile.payload.user_id || profile.payload.admin_id) : null;
  hintEl.textContent = entityId ? `id: ${entityId}` : 'фиксированный аккаунт';
  hintEl.className = 'text-[10px] text-zinc-400 font-mono truncate';
}

async function saveFixtureAccounts(btn) {
  const payload = {};
  FIXTURE_ROLE_FIELDS.forEach(({ role, select }) => {
    const el = document.getElementById(select);
    if (el) payload[role] = el.value || '';
  });

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/fixtures/accounts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });

      if (!res.ok) {
        toastError('Не удалось привязать аккаунты: ' + (await res.text()));
        return;
      }

      const data = await res.json();
      FIXTURE_ROLE_FIELDS.forEach(({ select }) => {
        const el = document.getElementById(select);
        if (el) delete el.dataset.dirty;
      });

      const bound = Object.entries(data.accounts || {})
        .filter(([, v]) => v && v.bound)
        .map(([role, v]) => `${role}: ${v.name}`);

      await loadVault();
      if (bound.length === 0) {
        toastSuccess('Все роли снова создают новые аккаунты на каждый прогон.');
        logTerminal('INFO', 'Привязка аккаунтов снята — сьюты генерируют фикстуры.');
      } else {
        toastSuccess('Аккаунты для прогонов сохранены: ' + bound.join(', '));
        logTerminal('SUCCESS', 'Сьюты будут работать под аккаунтами — ' + bound.join(', '));
      }
    } catch (err) {
      toastError('Ошибка сети: ' + err.message);
    }
  });
}

// countCheckStates считает проверки сьюта в заданном состоянии.
function countCheckStates(st, status) {
  if (!st || !st.checks) return 0;
  return Object.values(st.checks).filter(c => c && c.status === status).length;
}
