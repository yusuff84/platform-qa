// ============================================================
// ui.js — общие UI-хелперы: экранирование, форматирование,
// тосты, диалог подтверждения, busy-кнопки, пустые состояния.
// Загружается ПЕРВЫМ (до logs.js / scenarios-editor.js / app.js).
// ============================================================

// ---------- Экранирование ----------

function escapeHtml(str) {
  if (!str) return '';
  return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function escAttr(str) {
  return escapeHtml(String(str === null || str === undefined ? '' : str)).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// ---------- Форматирование времени/длительности ----------

// <1000ms → «N мс», <60s → «N.N с», иначе «M мин N с»
function fmtDuration(ms) {
  if (ms === undefined || ms === null || ms === '' || isNaN(Number(ms))) return '—';
  const n = Number(ms);
  if (n < 1000) return Math.round(n) + ' мс';
  const s = n / 1000;
  if (s < 60) return (Math.round(s * 10) / 10).toString().replace('.', ',') + ' с';
  const m = Math.floor(s / 60);
  const rs = Math.round(s % 60);
  return m + ' мин ' + rs + ' с';
}

// Абсолютное время DD.MM HH:MM
function fmtAbsTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '—';
  const p = x => String(x).padStart(2, '0');
  return p(d.getDate()) + '.' + p(d.getMonth() + 1) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}

// Абсолютное время со секундами DD.MM HH:MM:SS
function fmtAbsTimeSec(ts) {
  if (!ts) return '—';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '—';
  const p = x => String(x).padStart(2, '0');
  return p(d.getDate()) + '.' + p(d.getMonth() + 1) + ' ' +
    p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// Относительное время: только что / N мин назад / N ч назад / дата
function fmtRelTime(ts) {
  if (!ts) return '—';
  const t = new Date(ts).getTime();
  if (isNaN(t)) return '—';
  const diff = Date.now() - t;
  if (diff < 45 * 1000) return 'только что';
  const min = Math.floor(diff / 60000);
  if (min < 60) return min + ' мин назад';
  const h = Math.floor(min / 60);
  if (h < 24) return h + ' ч назад';
  return fmtAbsTime(ts);
}

function timeHMS(ts) {
  let d = ts ? new Date(ts) : new Date();
  if (isNaN(d.getTime())) d = new Date();
  const p = x => String(x).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// Русская плюрализация: pluralRu(5, ['запись','записи','записей'])
function pluralRu(n, forms) {
  const abs = Math.abs(n) % 100;
  const d = abs % 10;
  if (abs > 10 && abs < 20) return forms[2];
  if (d > 1 && d < 5) return forms[1];
  if (d === 1) return forms[0];
  return forms[2];
}

// ---------- Тосты ----------

function showToast(message, type) {
  type = type || 'info';
  let container = document.getElementById('toastContainer');
  if (!container) {
    container = document.createElement('div');
    container.id = 'toastContainer';
    container.className = 'fixed top-20 right-4 z-[100] space-y-2 w-80 max-w-[calc(100vw-2rem)] pointer-events-none';
    document.body.appendChild(container);
  }

  const icons = { success: 'fa-circle-check', error: 'fa-circle-xmark', info: 'fa-circle-info' };
  const colors = {
    success: 'border-zinc-700 bg-zinc-900/95 text-zinc-100',
    error: 'border-red-800/40 bg-red-950/90 text-red-200',
    info: 'border-zinc-700 bg-zinc-900/95 text-zinc-200',
  };
  const iconColors = { success: 'text-emerald-400', error: 'text-red-400', info: 'text-zinc-400' };

  const el = document.createElement('div');
  el.className = 'toast pointer-events-auto flex items-start gap-2.5 px-4 py-3 rounded-xl border shadow-2xl backdrop-blur text-xs ' + (colors[type] || colors.info);
  el.innerHTML = '<i class="fa-solid ' + (icons[type] || icons.info) + ' ' + (iconColors[type] || '') + ' mt-0.5"></i>' +
    '<span class="break-words flex-1">' + escapeHtml(String(message === undefined || message === null ? '' : message)) + '</span>';

  container.appendChild(el);
  setTimeout(() => {
    el.classList.add('hide');
    setTimeout(() => el.remove(), 350);
  }, 3000);
}

function toastSuccess(msg) { showToast(msg, 'success'); }
function toastError(msg) { showToast(msg, 'error'); }
function toastInfo(msg) { showToast(msg, 'info'); }

// ---------- Диалог подтверждения (Promise-based) ----------

let __confirmResolve = null;

function confirmDialog(title, text, confirmLabel) {
  return new Promise(resolve => {
    // Повторный диалог закрывает предыдущий как «отмену»
    if (__confirmResolve) __confirmResolve(false);
    __confirmResolve = resolve;
    const tEl = document.getElementById('confirmTitle');
    const xEl = document.getElementById('confirmText');
    const okEl = document.getElementById('confirmOkBtn');
    if (tEl) tEl.textContent = title || 'Подтвердите действие';
    if (xEl) xEl.textContent = text || '';
    if (okEl) okEl.textContent = confirmLabel || 'Подтвердить';
    const modal = document.getElementById('confirmModal');
    if (modal) modal.classList.remove('hidden');
  });
}

function resolveConfirm(val) {
  const modal = document.getElementById('confirmModal');
  if (modal) modal.classList.add('hidden');
  if (__confirmResolve) {
    __confirmResolve(val);
    __confirmResolve = null;
  }
}

// ---------- Busy-обёртка для кнопок (disabled + спиннер) ----------

async function busyWrap(btn, fn) {
  if (!btn) return fn();
  if (btn.dataset.busy === '1') return;
  btn.dataset.busy = '1';
  const prevDisabled = btn.disabled;
  const prevHtml = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = '<i class="fa-solid fa-spinner fa-spin"></i>';
  try {
    return await fn();
  } finally {
    btn.innerHTML = prevHtml;
    btn.disabled = prevDisabled;
    delete btn.dataset.busy;
  }
}

// ---------- Пустые состояния и ошибки загрузки ----------

function emptyStateHtml(icon, text, actionHtml) {
  return '<div class="py-8 text-center space-y-3">' +
    '<div class="mx-auto w-14 h-14 rounded-2xl bg-slate-800/70 border border-darkborder flex items-center justify-center">' +
    '<i class="fa-solid ' + icon + ' text-xl text-slate-500"></i></div>' +
    '<p class="text-xs text-slate-400 px-4">' + escapeHtml(text) + '</p>' +
    (actionHtml || '') +
    '</div>';
}

// Блок «не удалось загрузить» с кнопкой Повторить (retryFn — имя глобальной функции)
function loadRetryHtml(message, retryFn) {
  return '<div class="py-8 text-center space-y-3">' +
    '<i class="fa-solid fa-plug-circle-xmark text-3xl text-red-400/60"></i>' +
    '<p class="text-xs text-slate-400 break-all px-6">' + escapeHtml(message) + '</p>' +
    '<button onclick="' + retryFn + '()" class="px-3 py-1.5 text-xs bg-slate-800 hover:bg-slate-700 text-slate-200 border border-darkborder rounded-lg transition">' +
    '<i class="fa-solid fa-arrows-rotate mr-1"></i>Повторить</button>' +
    '</div>';
}
