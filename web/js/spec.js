// ============================================================
// spec.js — секция «Спецификация API» (таб Инструменты):
// импорт OpenAPI-спеки стенда, перегенерация и удаление
// сгенерированных смоук-сценариев spec_smoke_*.
//
// Контракт бэкенда:
//   POST /api/spec/import     {"url":"https://..."} → {meta:{key,title,version,endpoints:[...]}, generated:["spec_smoke_x",...]} | 400 текст ошибки
//   GET  /api/spec/current    → {meta} | 404
//   POST /api/spec/regenerate → как import, но из сохранённой спеки
//   DELETE /api/spec/current  → ok
//
// Сгенерированные сценарии приходят в обычном /api/suites как
// category:"custom" — после мутаций перезагружаем реестр loadSuites().
// Подключается в index.html ДО app.js (глобальное пространство имён).
// ============================================================

// Кэш последней известной мета-информации о спеке
let specMeta = null;

// ---------- Загрузка состояния ----------

async function loadSpec() {
  try {
    const res = await fetch('/api/spec/current');
    if (res.status === 404) {
      specMeta = null;
      renderSpecEmpty();
      return;
    }
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    specMeta = (data && data.meta) || null;
    renderSpecStatus(specMeta);
  } catch (err) {
    console.error('Failed to load current spec:', err);
    // Блок ошибки здесь не показываем — секция просто выглядит пустой
    specMeta = null;
    renderSpecEmpty();
  }
}

// ---------- Рендер строки состояния #specStatus ----------

function renderSpecEmpty() {
  const box = document.getElementById('specStatus');
  if (!box) return;
  hideSpecResult();
  hideSpecError();
  box.innerHTML =
    '<span class="w-2 h-2 rounded-full bg-slate-600 shrink-0"></span>' +
    '<span>Спецификация не загружена</span>';
}

function renderSpecStatus(meta) {
  const box = document.getElementById('specStatus');
  if (!box) return;
  if (!meta) {
    renderSpecEmpty();
    return;
  }

  const title = meta.title || meta.key || 'Спецификация';
  const version = meta.version || '—';
  const endpoints = Array.isArray(meta.endpoints) ? meta.endpoints.length : 0;

  box.innerHTML =
    '<span class="w-2 h-2 rounded-full bg-green-400 shadow-[0_0_8px_rgba(52,211,153,.8)] animate-pulse shrink-0"></span>' +
    '<span class="font-semibold text-slate-200">' + escapeHtml(title) +
    ' · версия ' + escapeHtml(String(version)) +
    ' · ' + endpoints + ' ' + pluralRu(endpoints, ['эндпоинт', 'эндпоинта', 'эндпоинтов']) + '</span>' +
    '<button onclick="openReplenishModal(false)" title="Умное автопополнение сценариев (Smoke, CRUD, RBAC, Negative)"' +
    ' class="px-2.5 py-1 text-[11px] bg-white hover:bg-zinc-200 text-zinc-950 rounded-lg transition shrink-0 font-semibold shadow-sm"><i class="fa-solid fa-wand-magic-sparkles mr-1"></i>Автопополнение тестов</button>' +
    '<button onclick="regenerateSpec(this)" title="Пересоздать spec_smoke* сценарии из сохранённой спеки"' +
    ' class="px-2.5 py-1 text-[11px] bg-zinc-900 hover:bg-zinc-800 text-zinc-200 border border-darkborder rounded-lg transition shrink-0"><i class="fa-solid fa-rotate mr-1"></i>Перегенерировать</button>' +
    '<button onclick="deleteSpec(this)" title="Удалить спецификацию"' +
    ' class="px-2.5 py-1 text-[11px] bg-zinc-900 hover:bg-red-950 text-red-400 border border-darkborder rounded-lg transition shrink-0"><i class="fa-solid fa-trash mr-1"></i>Удалить</button>';
}

// ---------- Импорт по URL ----------

async function importSpec(btn) {
  const input = document.getElementById('specImportUrl');
  const url = ((input && input.value) || '').trim();
  if (!url) {
    toastError('Укажите URL OpenAPI-спеки.');
    if (input) input.focus();
    return;
  }

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/spec/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url }),
      });
      if (!res.ok) {
        throw new Error((await res.text()) || ('HTTP ' + res.status));
      }
      const data = await res.json();
      specMeta = (data && data.meta) || null;
      renderSpecStatus(specMeta);

      const nEndpoints = (specMeta && Array.isArray(specMeta.endpoints)) ? specMeta.endpoints.length : 0;
      const nGenerated = Array.isArray(data.generated) ? data.generated.length : 0;
      showSpecResult(
        'Импортировано: ' + nEndpoints + ' ' + pluralRu(nEndpoints, ['эндпоинт', 'эндпоинта', 'эндпоинтов']) +
        ', создано ' + nGenerated + ' ' + pluralRu(nGenerated, ['смоук-сценарий', 'смоук-сценария', 'смоук-сценариев'])
      );

      if (input) input.value = '';
      toastSuccess('Спецификация импортирована.');
      logTerminal('SUCCESS', '[Спека] Импортировано эндпоинтов: ' + nEndpoints + ', создано смоук-сценариев: ' + nGenerated + '.');
      if (typeof loadSuites === 'function') loadSuites(); // spec_smoke_* появляются в реестре чеклистов
    } catch (err) {
      showSpecError(err.message); // текст от сервера при 400 приходит как есть
      toastError('Ошибка импорта спеки: ' + err.message);
    }
  });
}

// ---------- Перегенерация из сохранённой спеки ----------

async function regenerateSpec(btn) {
  const ok = await confirmDialog(
    'Перегенерировать сценарии?',
    'Будут пересозданы spec_smoke* сценарии из сохранённой спецификации.',
    'Перегенерировать'
  );
  if (!ok) return;

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/spec/regenerate', { method: 'POST' });
      if (!res.ok) {
        throw new Error((await res.text()) || ('HTTP ' + res.status));
      }
      const data = await res.json();
      specMeta = (data && data.meta) || specMeta;
      renderSpecStatus(specMeta);

      const nGenerated = Array.isArray(data.generated) ? data.generated.length : 0;
      if (typeof loadSuites === 'function') loadSuites();
      toastSuccess('Сценарии перегенерированы (' + nGenerated + ').');
      logTerminal('SUCCESS', '[Спека] Пересоздано смоук-сценариев: ' + nGenerated + '.');
    } catch (err) {
      toastError('Ошибка перегенерации сценариев: ' + err.message);
    }
  });
}

// ---------- Удаление спеки ----------

async function deleteSpec(btn) {
  const ok = await confirmDialog(
    'Удалить спецификацию?',
    'Спецификация будет удалена вместе со всеми сгенерированными spec_smoke* сценариями.',
    'Удалить'
  );
  if (!ok) return;

  await busyWrap(btn, async () => {
    try {
      const res = await fetch('/api/spec/current', { method: 'DELETE' });
      if (!res.ok) {
        throw new Error((await res.text()) || ('HTTP ' + res.status));
      }
      specMeta = null;
      renderSpecEmpty();
      if (typeof loadSuites === 'function') loadSuites();
      toastSuccess('Спецификация удалена.');
      logTerminal('INFO', '[Спека] Спецификация и сгенерированные сценарии удалены.');
    } catch (err) {
      toastError('Ошибка удаления спецификации: ' + err.message);
    }
  });
}

// ---------- Навигация ----------

function gotoChecklists() {
  if (typeof switchTab === 'function') switchTab('checklists');
}

// ---------- Блоки результата/ошибки ----------

function showSpecResult(text) {
  const box = document.getElementById('specResultBox');
  const txt = document.getElementById('specResultText');
  if (!box) return;
  if (txt) txt.textContent = text; // textContent — эмодзи ✅ безопасны, HTML не интерпретируется
  hideSpecError();
  box.classList.remove('hidden');
}

function hideSpecResult() {
  const box = document.getElementById('specResultBox');
  if (box) box.classList.add('hidden');
}

function showSpecError(message) {
  const box = document.getElementById('specErrorBox');
  if (!box) return;
  box.textContent = message || '';
  box.classList.remove('hidden');
  hideSpecResult();
}

function hideSpecError() {
  const box = document.getElementById('specErrorBox');
  if (box) box.classList.add('hidden');
}
