// ============================================================
// logs.js — глобальный просмотрщик «Все логи» + гуманизация
// событий (humanizeEvent). Клиентский буфер наполняется ВСЕГДА,
// даже когда оверлей закрыт; открытие мгновенно показывает всё.
// Зависимости (runtime): ui.js (escapeHtml, fmtDuration, timeHMS),
// app.js (suitesRegistry, getCheckTitle).
// ============================================================

// ---------- Гуманизация событий ----------

const LOG_LEVELS = ['INFO', 'SUCCESS', 'WARN', 'ERROR', 'HTTP'];

function __logsSuiteTitle(suiteKey) {
  if (!suiteKey || typeof suitesRegistry === 'undefined') return '';
  const s = (typeof suitesRegistry !== 'undefined' ? suitesRegistry : []).find(x => x.key === suiteKey);
  return s ? s.title : '';
}

function __logsCheckTitle(suiteKey, checkId) {
  if (typeof getCheckTitle === 'function') {
    const t = getCheckTitle(suiteKey, checkId);
    if (t) return t;
  }
  return '';
}

// Убираем технические эмодзи-префиксы и хвост «(N ms)» из сообщений бэкенда
function tidyEventMessage(msg) {
  let m = String(msg || '');
  m = m.replace(/^[▶✅❌⏭✔✘💥🎉⚠️\s]+/u, '');
  m = m.replace(/\s*\(\d+\s*ms\)\s*$/u, '');
  return m.trim();
}

// Единая функция перевода событий в человеческий вид
function humanizeEvent(ev) {
  if (!ev || typeof ev !== 'object') return String(ev === undefined || ev === null ? '' : ev);
  const t = ev.stepType || '';
  const dur = ev.durationMs ? fmtDuration(ev.durationMs) : '';
  const title = ev.checkId ? (__logsCheckTitle(ev.suiteKey, ev.checkId) || ev.checkId) : '';
  const msg = tidyEventMessage(ev.message);

  switch (t) {
    case 'SUITE_START':
      return 'Запуск: ' + (ev.suiteName || __logsSuiteTitle(ev.suiteKey) || ev.suiteKey || 'сценарий');
    case 'CHECK_START':
      return 'Выполняется: ' + (title || msg || 'проверка');
    case 'CHECK_SUCCESS':
      return 'Успех: ' + (title || msg);
    case 'CHECK_FAILED':
      return 'Сбой: ' + (title || 'проверка') + (msg ? ': ' + msg : '');
    case 'GIVEN':
      return msg ? 'Подготовка: ' + msg : 'Подготовка';
    case 'WHEN':
      return msg ? 'Действие: ' + msg : 'Действие';
    case 'THEN':
      return msg ? 'Проверка: ' + msg : 'Проверка';
    case 'AND':
      return msg ? 'Доп. проверка: ' + msg : 'Доп. проверка';
    case 'SUMMARY':
      if (ev.level === 'SUCCESS' || ev.level === 'PASSED') {
        return 'Успех: все проверки пройдены за ' + (dur || '—');
      }
      return 'Сбой: ' + (msg || 'сценарий завершился с ошибкой');
    default:
      return ev.message || t || '';
  }
}

function normalizeLogLevel(ev) {
  const lvl = String(ev.level || '').toUpperCase();
  if (LOG_LEVELS.includes(lvl)) return lvl;
  switch (ev.stepType) {
    case 'CHECK_SUCCESS': return 'SUCCESS';
    case 'CHECK_FAILED': return 'ERROR';
    case 'SUITE_START': return 'INFO';
    default: return 'INFO';
  }
}

// ---------- Состояние просмотрщика ----------

const LogsState = {
  buffer: [],              // хронологический массив записей {ts, level, suiteKey, runId, text}
  seen: new Set(),         // дедупликация runId|ts(ms)|stepType|message|checkId
  max: 3000,               // FIFO-лимит буфера
  backfillDone: false,     // /api/events/recent догружен один раз
  paused: false,
  pausedPending: 0,        // записей поступило, пока включена пауза отрисовки
  autoScroll: true,
  filterSuite: '',
  filterRunId: null,
  levels: { INFO: true, SUCCESS: true, WARN: true, ERROR: true, HTTP: true },
  search: '',
  renderQueued: false,
};

const LOGS_RENDER_LIMIT = 1200;

function logsDedupKey(ev) {
  const ts = Date.parse(ev.timestamp) || ev.timestamp || '';
  return [ev.runId || '', ts, ev.stepType || '', ev.message || '', ev.checkId || ''].join('|');
}

function logsMakeEntry(ts, level, suiteKey, runId, text) {
  return { ts: ts || Date.now(), level, suiteKey: suiteKey || '', runId: runId || null, text: text || '' };
}

function logsTrimBuffer() {
  while (LogsState.buffer.length > LogsState.max) {
    LogsState.buffer.shift();
  }
}

// Добавить SSE-событие (всегда, независимо от открытости оверлея)
function logsPushEvent(ev) {
  try {
    const key = logsDedupKey(ev);
    if (LogsState.seen.has(key)) return;
    LogsState.seen.add(key);
    if (LogsState.seen.size > 6000) {
      const toDrop = Math.floor(LogsState.seen.size / 2);
      let dropped = 0;
      for (const oldKey of LogsState.seen) {
        if (dropped >= toDrop) break;
        LogsState.seen.delete(oldKey);
        dropped++;
      }
    }

    const entry = logsMakeEntry(
      Date.parse(ev.timestamp),
      normalizeLogLevel(ev),
      ev.suiteKey || '',
      ev.runId || null,
      humanizeEvent(ev)
    );
    LogsState.buffer.push(entry);
    logsTrimBuffer();
    logsUpdateCounter();
    logsScheduleRender();
  } catch (err) {
    console.error('logsPushEvent failed:', err);
  }
}

// Системные сообщения UI (бывший logTerminal)
function logsPushSystem(msg, level) {
  const lvl = level === 'ERROR' ? 'ERROR' : (level === 'SUCCESS' ? 'SUCCESS' : (level === 'WARN' ? 'WARN' : 'INFO'));
  LogsState.buffer.push(logsMakeEntry(Date.now(), lvl, '', null, msg));
  logsTrimBuffer();
  logsUpdateCounter();
  logsScheduleRender();
}

// Догрузка истории при первом открытии
async function logsBackfillIfNeeded() {
  if (LogsState.backfillDone) return;
  try {
    const res = await fetch('/api/events/recent?limit=500');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    LogsState.backfillDone = true;
    (data.events || []).forEach(ev => logsPushEvent(ev));
    LogsState.buffer.sort((a, b) => a.ts - b.ts);
    logsRender();
  } catch (err) {
    console.warn('Не удалось догрузить /api/events/recent:', err);
  }
}

// ---------- Фильтрация и рендер ----------

function logsVisibleEntries() {
  const q = LogsState.search.toLowerCase();
  return LogsState.buffer.filter(e =>
    (!LogsState.filterSuite || e.suiteKey === LogsState.filterSuite) &&
    (!LogsState.filterRunId || e.runId === LogsState.filterRunId) &&
    LogsState.levels[e.level] &&
    (!q || (e.text || '').toLowerCase().includes(q))
  );
}

function logsLineHtml(e) {
  const cls = e.level === 'ERROR' ? ' log-line-error' : (e.level === 'SUCCESS' ? ' log-line-success' : '');
  const suiteTag = e.suiteKey
    ? '<span class="suite-tag shrink-0">' + escapeHtml(e.suiteKey.toUpperCase().slice(0, 14)) + '</span>'
    : '<span class="suite-tag shrink-0 opacity-40">SYS</span>';
  const clickable = e.runId ? ' data-run-id="' + escAttr(e.runId) + '" title="Клик — фильтр по этому прогону"' : '';
  return '<div class="log-line flex items-start gap-2 px-3 py-1 border-b border-slate-900/60 select-text ' +
    (e.runId ? 'cursor-pointer hover:bg-slate-800/50' : '') + cls + '"' + clickable + '>' +
    '<span class="text-slate-600 font-mono text-[10px] leading-5 shrink-0">' + timeHMS(e.ts) + '</span>' +
    '<span class="lvl-tag lvl-' + e.level + ' leading-5 my-[1px]">' + e.level + '</span>' +
    suiteTag +
    '<span class="flex-1 break-all text-[11px] leading-5 ' +
    (e.level === 'ERROR' ? 'text-red-300 font-medium' : (e.level === 'SUCCESS' ? 'text-emerald-300' : (e.level === 'WARN' ? 'text-amber-300' : 'text-zinc-300'))) +
    '">' + escapeHtml(e.text) + '</span>' +
    '</div>';
}

function logsRender() {
  const box = document.getElementById('logLines');
  if (!box) return;

  const visible = logsVisibleEntries();
  const hidden = Math.max(0, visible.length - LOGS_RENDER_LIMIT);
  const slice = hidden > 0 ? visible.slice(hidden) : visible;

  let html = '';
  if (LogsState.filterRunId) {
    html += '<div class="px-3 py-1.5 text-[10px] bg-zinc-800 text-zinc-200 border-b border-darkborder flex items-center gap-2">' +
      '<i class="fa-solid fa-filter"></i>Фильтр по прогону: <span class="font-mono">' + escapeHtml(String(LogsState.filterRunId).slice(0, 12)) + '…</span>' +
      '<button onclick="clearLogRunFilter()" class="ml-auto text-zinc-400 hover:text-white" title="Сбросить фильтр по прогону"><i class="fa-solid fa-xmark"></i></button></div>';
  }
  if (hidden > 0) {
    html += '<div class="px-3 py-1 text-[10px] text-slate-500 italic">…скрыто ' + hidden + ' старых ' + pluralRu(hidden, ['запись', 'записи', 'записей']) + ' (рендер ограничен)</div>';
  }
  html += slice.map(logsLineHtml).join('');

  if (!slice.length) {
    html += '<div class="py-16 text-center text-slate-500 italic text-xs"><i class="fa-solid fa-moon mr-2"></i>' +
      (LogsState.buffer.length ? 'Нет записей, подходящих под фильтры.' : 'Журнал пуст — запустите тесты, и события появятся здесь.') + '</div>';
  }

  box.innerHTML = html;
  logsMaybeScroll();

  const cnt = document.getElementById('logCount');
  if (cnt) {
    cnt.textContent = LogsState.buffer.length + ' ' + pluralRu(LogsState.buffer.length, ['запись', 'записи', 'записей']);
  }
}

function logsScheduleRender() {
  // На паузе отрисовку не обновляем — записи копятся в буфере
  if (LogsState.paused) {
    LogsState.pausedPending++;
    const btn = document.getElementById('logPauseBtn');
    if (btn) btn.innerHTML = '<i class="fa-solid fa-play mr-1"></i>Продолжить (' + LogsState.pausedPending + ')';
    return;
  }
  const overlay = document.getElementById('logsOverlay');
  if (!overlay || overlay.classList.contains('hidden')) return;
  if (LogsState.renderQueued) return;
  LogsState.renderQueued = true;
  requestAnimationFrame(() => {
    LogsState.renderQueued = false;
    logsRender();
  });
}

function logsUpdateCounter() {
  const badge = document.getElementById('openLogsBadge');
  const running = typeof getActiveRunsCount === 'function' ? getActiveRunsCount() : 0;
  if (badge) {
    badge.style.display = running > 0 ? 'inline-flex' : 'none';
    if (running > 0) badge.textContent = String(running);
  }
}

// ---------- Автопрокрутка ----------

function logsMaybeScroll() {
  const box = document.getElementById('logLinesBox');
  if (!box) return;
  if (LogsState.autoScroll) {
    box.scrollTop = box.scrollHeight;
  }
}

function logsOnScroll() {
  const box = document.getElementById('logLinesBox');
  if (!box) return;
  const nearBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 60;
  const jumpBtn = document.getElementById('logJumpLatest');
  if (!nearBottom && LogsState.autoScroll) {
    LogsState.autoScroll = false;
    syncAutoScrollToggle();
  }
  if (jumpBtn) jumpBtn.classList.toggle('hidden', nearBottom);
}

function jumpToLatestLogs() {
  LogsState.autoScroll = true;
  syncAutoScrollToggle();
  const box = document.getElementById('logLinesBox');
  if (box) box.scrollTop = box.scrollHeight;
  const jumpBtn = document.getElementById('logJumpLatest');
  if (jumpBtn) jumpBtn.classList.add('hidden');
}

function syncAutoScrollToggle() {
  const cb = document.getElementById('logAutoScroll');
  if (cb) cb.checked = LogsState.autoScroll;
}

// ---------- Публичное API ----------

function openLogsOverlay(filterRunId) {
  const overlay = document.getElementById('logsOverlay');
  if (!overlay) return;
  overlay.classList.remove('hidden');
  document.body.style.overflow = 'hidden';

  if (typeof filterRunId === 'string' && filterRunId) LogsState.filterRunId = filterRunId;
  logsFillSuiteOptions();

  const searchEl = document.getElementById('logSearch');
  if (searchEl) searchEl.value = LogsState.search;

  logsRender();
  logsBackfillIfNeeded();
}

function closeLogsOverlay() {
  const overlay = document.getElementById('logsOverlay');
  if (!overlay) return;
  overlay.classList.add('hidden');
  document.body.style.overflow = '';
  hideLogPopover();
}

function isLogsOverlayOpen() {
  const overlay = document.getElementById('logsOverlay');
  return !!overlay && !overlay.classList.contains('hidden');
}

// Открыть логи с фильтром по runId (кнопки «Логи прогона»)
function openLogsFiltered(runId) {
  if (!runId) {
    openLogsOverlay();
    toastInfo('У этого прогона нет идентификатора — показаны все логи.');
    return;
  }
  openLogsOverlay(runId);
}

function clearLogRunFilter() {
  LogsState.filterRunId = null;
  logsRender();
}

// Селектор фильтра по сюту (динамически из реестра)
function logsFillSuiteOptions() {
  const sel = document.getElementById('logSuiteFilter');
  if (!sel) return;
  const prev = LogsState.filterSuite;
  const list = (typeof suitesRegistry !== 'undefined' ? suitesRegistry : []);
  let html = '<option value="">Все сюты</option>';
  list.forEach(s => {
    html += '<option value="' + escAttr(s.key) + '">' + escapeHtml(s.key) + '</option>';
  });
  sel.innerHTML = html;
  sel.value = prev;
  if (sel.value !== prev) { sel.value = ''; LogsState.filterSuite = ''; }
}

// Обработчики тулбара
function applyLogFilters() {
  const sel = document.getElementById('logSuiteFilter');
  const search = document.getElementById('logSearch');
  LogsState.filterSuite = sel ? sel.value : '';
  LogsState.search = search ? search.value.trim() : '';
  logsRender();
}

function toggleLogLevel(cb, level) {
  LogsState.levels[level] = cb.checked;
  logsRender();
}

function toggleAutoScroll(cb) {
  LogsState.autoScroll = cb.checked;
  if (cb.checked) jumpToLatestLogs();
}

function toggleLogPause(btn) {
  LogsState.paused = !LogsState.paused;
  if (LogsState.paused) {
    LogsState.pausedPending = 0;
    if (btn) btn.innerHTML = '<i class="fa-solid fa-play mr-1"></i>Продолжить';
  } else {
    if (btn) btn.innerHTML = '<i class="fa-solid fa-pause mr-1"></i>Пауза потока';
    logsRender();
  }
}

function copyLogsToClipboard() {
  const text = logsPlainText(logsVisibleEntries());
  navigator.clipboard.writeText(text).then(() => {
    toastSuccess('Логи скопированы в буфер обмена (' + logsVisibleEntries().length + ').');
  }).catch(() => {
    toastError('Браузер запретил доступ к буферу обмена.');
  });
}

function downloadLogsFile() {
  const entries = logsVisibleEntries();
  if (!entries.length) {
    toastInfo('Нечего скачивать — журнал пуст.');
    return;
  }
  const stamp = new Date().toISOString().replace(/[:T]/g, '-').slice(0, 19);
  const blob = new Blob([logsPlainText(entries)], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'e2e-logs-' + stamp + '.log';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1500);
  toastSuccess('Файл логов сохранён (' + entries.length + ').');
}

function logsPlainText(entries) {
  return entries.map(e =>
    '[' + timeHMS(e.ts) + '] [' + e.level + ']' + (e.suiteKey ? ' [' + e.suiteKey + ']' : '') + ' ' + e.text
  ).join('\n');
}

// ---------- Поповер «Фильтровать по этому прогону» ----------

function hideLogPopover() {
  const pop = document.getElementById('logPopover');
  if (pop) pop.classList.add('hidden');
}

function showLogPopover(runId, x, y) {
  const pop = document.getElementById('logPopover');
  if (!pop) return;
  pop.innerHTML = '<button onclick="openLogsFiltered(\'' + escAttr(runId) + '\')" class="w-full whitespace-nowrap px-3 py-1.5 text-left text-[11px] bg-zinc-800 hover:bg-zinc-700 text-zinc-100 rounded-lg shadow-xl border border-darkborder transition">' +
    '<i class="fa-solid fa-filter mr-1.5 text-zinc-400"></i>Фильтровать по этому прогону</button>';
  pop.classList.remove('hidden');
  const rect = pop.getBoundingClientRect();
  pop.style.left = Math.min(Math.max(8, x - rect.width / 2), window.innerWidth - rect.width - 8) + 'px';
  pop.style.top = Math.max(8, y - rect.height - 10) + 'px';
}

function onLogLineClick(event) {
  const line = event.target.closest('.log-line[data-run-id]');
  hideLogPopover();
  if (!line) return;
  event.stopPropagation();
  showLogPopover(line.getAttribute('data-run-id'), event.clientX, event.clientY);
}

// Закрытие поповера кликом мимо
document.addEventListener('click', () => hideLogPopover());

// Делегирование клика по строке лога (поповер «Фильтровать по этому прогону»)
(function attachLogLineClick() {
  const host = document.getElementById('logLines');
  if (host) host.addEventListener('click', onLogLineClick);
})();

// Esc закрывает поповер, затем оверлей
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  const pop = document.getElementById('logPopover');
  if (pop && !pop.classList.contains('hidden')) { hideLogPopover(); return; }
  if (isLogsOverlayOpen()) closeLogsOverlay();
});
