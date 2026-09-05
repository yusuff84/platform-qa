// ============================================================
// scenarios-editor.js — редактор кастомных сценариев (CRUD).
// Полностью перенесён из прежнего app.js; логика сохранена,
// alert()/confirm() заменены на тосты и confirmDialog().
// Зависимости (runtime): ui.js, app.js (logTerminal, switchTab,
// loadSuites, markSuitesRunning, finalizeSuiteCard).
// ============================================================

const SCENARIO_KEY_PATTERN = /^[a-z][a-z0-9_]{2,39}$/;
const STEP_ID_PATTERN = /^[a-z][a-z0-9_]*$/;
const HTTP_ROLES = ['client', 'rest', 'courier', 'admin', 'none'];
const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'];
const EXPECT_STATUS_OPTIONS = ['', '200', '201', '204', '400', '401', '403', '404', '405', '409', '422', '429', '2xx', '4xx', '5xx', '!4xx', '!5xx'];
const ASSERT_OPS_HTTP = ['eq', 'neq', 'contains', 'exists'];
const ASSERT_OPS_STEP = ['notEmpty', 'eq', 'neq', 'contains'];
const STEP_TYPES = [
  { value: 'http', label: 'HTTP запрос' },
  { value: 'delay', label: 'Задержка (delay)' },
  { value: 'assert', label: 'Проверка переменной (assert)' },
];

const SC_INP = 'w-full rounded-md px-2 py-1.5 text-[11px]';
const SC_LBL = 'block text-[10px] font-semibold text-zinc-400 uppercase tracking-wider mb-1';
const SC_BTN_MINI = 'px-1.5 py-1 text-[10px] bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border border-darkborder rounded-md transition';

let scenariosRegistry = [];
let editorSuitesRegistry = [];
let editorState = null;

function makeStep(type, usedIds) {
  const base = { type, id: '', title: '' };
  if (type === 'http') {
    base.role = 'client';
    base.method = 'GET';
    base.path = '';
    base.bodyRaw = '';
    base.headers = [];
    base.extract = [];
    base.expectStatus = '';
    base.asserts = [];
  } else if (type === 'delay') {
    base.ms = '500';
  } else {
    base.left = '';
    base.op = 'notEmpty';
    base.value = '';
  }
  const used = new Set(usedIds || []);
  let n = (editorState ? editorState.steps.length : 0) + 1;
  let id = 'step_' + n;
  while (used.has(id)) { n++; id = 'step_' + n; }
  base.id = id;
  return base;
}

function convertStep(oldStep, type) {
  const used = editorState ? editorState.steps.map(s => s.id) : [];
  const fresh = makeStep(type, used);
  const step = { type, id: oldStep.id || fresh.id, title: oldStep.title || '' };
  Object.keys(fresh).forEach(k => {
    if (k !== 'id' && k !== 'title' && step[k] === undefined) step[k] = fresh[k];
  });
  return step;
}

function normalizeStep(s) {
  const type = s.type || 'http';
  const base = { type, id: s.id || '', title: s.title || '' };
  if (type === 'http') {
    base.role = HTTP_ROLES.includes(s.role) ? s.role : 'none';
    base.method = HTTP_METHODS.includes(s.method) ? s.method : 'GET';
    base.path = s.path || '';
    base.bodyRaw = (s.body === undefined || s.body === null) ? '' : JSON.stringify(s.body, null, 2);
    base.headers = Object.entries(s.headers || {}).map(([k, v]) => ({ k, v: String(v) }));
    base.extract = Object.entries(s.extract || {}).map(([k, v]) => ({ k, v: String(v) }));
    base.expectStatus = (s.expectStatus === undefined || s.expectStatus === null) ? '' : String(s.expectStatus);
    base.asserts = (s.asserts || []).map(a => ({
      path: a.path || '',
      op: ASSERT_OPS_HTTP.includes(a.op) ? a.op : 'eq',
      value: (a.value === undefined || a.value === null) ? '' : String(a.value),
    }));
  } else if (type === 'delay') {
    base.ms = (s.ms === undefined || s.ms === null) ? '500' : String(s.ms);
  } else {
    base.left = s.left || '';
    base.op = ASSERT_OPS_STEP.includes(s.check && s.check.op) ? s.check.op : 'notEmpty';
    base.value = (s.check && s.check.value !== undefined && s.check.value !== null) ? String(s.check.value) : '';
  }
  return base;
}

function blankScenarioState() {
  const st = {
    isNew: true,
    originalKey: null,
    key: '',
    title: '',
    description: '',
    tagsRaw: '',
    dependsOn: [],
    vars: [{ name: '', value: '' }],
    steps: [],
  };
  editorState = st;
  st.steps = [makeStep('http')];
  return st;
}

async function loadScenarioEditor() {
  const [scRes, suiteRes] = await Promise.allSettled([
    fetch('/api/scenarios').then(async r => {
      if (!r.ok) throw new Error('HTTP ' + r.status + (await r.text().then(t => t ? ' ' + t : '').catch(() => '')));
      return r.json();
    }),
    fetch('/api/suites').then(r => {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }),
  ]);

  if (scRes.status === 'fulfilled') {
    scenariosRegistry = scRes.value.scenarios || [];
    hideScenariosError();
  } else {
    scenariosRegistry = [];
    renderScenarioList();
    document.getElementById('scenarioList').innerHTML =
      loadRetryHtml('Не удалось загрузить /api/scenarios: ' + scRes.reason.message, 'loadScenarioEditor');
    showScenariosError('Не удалось загрузить /api/scenarios: ' + scRes.reason.message + '\nНажмите «Повторить».');
    return;
  }

  editorSuitesRegistry = suiteRes.status === 'fulfilled' ? (suiteRes.value.suites || []) : [];

  renderScenarioList();
  if (editorState) renderDependsChips();
}

function renderScenarioList() {
  const c = document.getElementById('scenarioList');
  if (!c) return;
  if (!scenariosRegistry.length) {
    c.innerHTML = emptyStateHtml(
      'fa-flask',
      'Кастомных сценариев пока нет. Создайте первый — он появится в сетке тестов.',
      '<button onclick="newScenario()" class="px-3 py-1.5 text-xs bg-white hover:bg-zinc-200 text-zinc-950 font-semibold rounded-lg shadow-sm transition"><i class="fa-solid fa-plus mr-1"></i>Создайте первый сценарий</button>'
    );
    return;
  }
  const activeKey = editorState && !editorState.isNew ? editorState.originalKey : null;
  c.innerHTML = scenariosRegistry.map(sc => {
    const dep = (sc.dependsOn || []).map(d =>
      `<span class="px-1.5 py-0.5 rounded bg-zinc-800 text-[9px] font-mono text-zinc-400 border border-darkborder" title="Зависит от сюта">${escapeHtml(d)}</span>`
    ).join(' ');
    const active = sc.key === activeKey;
    return `<button type="button" onclick="editScenario('${escAttr(sc.key)}')" class="w-full text-left p-2.5 rounded-lg border transition ${active ? 'border-zinc-500 bg-zinc-800 ring-1 ring-zinc-500/40' : 'border-darkborder bg-zinc-900/70 hover:border-zinc-700'}">
      <div class="flex items-center justify-between gap-2">
        <span class="font-mono text-[11px] ${active ? 'text-white font-semibold' : 'text-zinc-300'}">${escapeHtml(sc.key)}</span>
        <span class="text-[9px] font-mono text-zinc-500 shrink-0">${(sc.steps || []).length} шаг.</span>
      </div>
      <div class="text-[11px] text-white mt-0.5 truncate">${escapeHtml(sc.title)}</div>
      ${dep ? `<div class="flex flex-wrap gap-1 mt-1">${dep}</div>` : ''}
    </button>`;
  }).join('');
}

function newScenario() {
  blankScenarioState();
  setKeyInputLocked(false);
  hideScenariosError();
  renderEditorForm();
}

async function editScenario(key) {
  let sc = scenariosRegistry.find(s => s.key === key);
  if (!sc) {
    try {
      const r = await fetch('/api/scenarios/' + encodeURIComponent(key));
      if (!r.ok) throw new Error('HTTP ' + r.status);
      sc = await r.json();
    } catch (err) {
      showScenariosError('Не удалось загрузить сценарий «' + key + '»: ' + err.message);
      toastError('Не удалось загрузить сценарий «' + key + '»');
      return;
    }
  }
  editorState = {
    isNew: false,
    originalKey: key,
    key: sc.key || key,
    title: sc.title || '',
    description: sc.description || '',
    tagsRaw: (sc.tags || []).join(', '),
    dependsOn: [...(sc.dependsOn || [])],
    vars: Object.entries(sc.vars || {}).map(([name, value]) => ({ name, value: String(value) })),
    steps: (sc.steps || []).map(normalizeStep),
  };
  if (!editorState.steps.length) editorState.steps = [makeStep('http')];
  if (!editorState.vars.length) editorState.vars = [{ name: '', value: '' }];
  setKeyInputLocked(true);
  hideScenariosError();
  renderEditorForm();
  renderScenarioList();
}

function openScenarioInEditor(key) {
  switchTab('scenarios');
  editScenario(key);
}

function harvestSteps() {
  const out = [];
  document.querySelectorAll('#stepsContainer .step-card').forEach(card => {
    const q = sel => card.querySelector(sel);
    const val = sel => { const el = q(sel); return el ? el.value.trim() : ''; };
    const type = card.getAttribute('data-cur-type') || 'http';
    const raw = { type, id: val('[data-step-id]'), title: val('[data-step-title]') };

    if (type === 'http') {
      raw.role = q('[data-step-role]') ? q('[data-step-role]').value : 'none';
      raw.method = q('[data-step-method]') ? q('[data-step-method]').value : 'GET';
      raw.path = val('[data-step-path]');
      raw.bodyRaw = q('[data-step-body]') ? q('[data-step-body]').value : '';
      raw.headers = [];
      card.querySelectorAll('[data-sub-h-key]').forEach(inp => {
        const k = inp.value.trim();
        const v = inp.closest('[data-dyn-row]').querySelector('[data-sub-h-value]').value.trim();
        if (k) raw.headers.push({ k, v });
      });
      raw.extract = [];
      card.querySelectorAll('[data-sub-ex-var]').forEach(inp => {
        const k = inp.value.trim();
        const v = inp.closest('[data-dyn-row]').querySelector('[data-sub-ex-path]').value.trim();
        if (k || v) raw.extract.push({ k, v });
      });
      raw.expectStatus = q('[data-step-expect]') ? q('[data-step-expect]').value : '';
      raw.asserts = [];
      card.querySelectorAll('[data-sub-as-path]').forEach(inp => {
        const rowEl = inp.closest('[data-dyn-row]');
        const p = inp.value.trim();
        const op = rowEl.querySelector('[data-sub-as-op]').value;
        const v = rowEl.querySelector('[data-sub-as-value]').value.trim();
        if (p || v) raw.asserts.push({ path: p, op, value: v });
      });
    } else if (type === 'delay') {
      raw.ms = val('[data-step-ms]');
    } else {
      raw.left = val('[data-step-left]');
      raw.op = q('[data-step-op]') ? q('[data-step-op]').value : 'notEmpty';
      raw.value = val('[data-step-value]');
    }
    out.push(raw);
  });
  return out;
}

function harvestEditor() {
  if (!editorState) blankScenarioState();
  editorState.key = (document.getElementById('scKey').value || '').trim();
  editorState.title = (document.getElementById('scTitle').value || '').trim();
  editorState.description = (document.getElementById('scDesc').value || '').trim();
  editorState.tagsRaw = document.getElementById('scTags').value || '';
  editorState.vars = [];
  document.querySelectorAll('#varsContainer [data-var-row]').forEach(row => {
    const n = row.querySelector('[data-var-name]').value.trim();
    const v = row.querySelector('[data-var-value]').value.trim();
    if (n) editorState.vars.push({ name: n, value: v });
  });
  editorState.steps = harvestSteps();
}

function renderEditorForm() {
  const g = id => document.getElementById(id);
  g('scKey').value = editorState.key;
  g('scTitle').value = editorState.title;
  g('scDesc').value = editorState.description;
  g('scTags').value = editorState.tagsRaw || '';
  renderDependsChips();
  renderVarsRows();
  renderSteps();
  updateModeBadge();
}

function setKeyInputLocked(lock) {
  const inp = document.getElementById('scKey');
  if (!inp) return;
  inp.disabled = lock;
  inp.classList.toggle('opacity-60', lock);
  inp.classList.toggle('cursor-not-allowed', lock);
}

function updateModeBadge() {
  const b = document.getElementById('scModeBadge');
  if (!b) return;
  if (editorState.isNew) {
    b.textContent = 'НОВЫЙ';
    b.className = 'text-[10px] font-mono font-medium px-2 py-0.5 rounded border shrink-0 bg-zinc-800 text-zinc-300 border-zinc-700';
  } else {
    b.textContent = 'РЕДАКТИРОВАНИЕ · ' + editorState.originalKey;
    b.className = 'text-[10px] font-mono font-medium px-2 py-0.5 rounded border shrink-0 bg-zinc-800 text-zinc-200 border-zinc-600';
  }
}

function showScenariosError(msg) {
  const box = document.getElementById('scenariosErrorBox');
  if (!box) return;
  box.textContent = msg;
  box.classList.remove('hidden');
  box.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

function hideScenariosError() {
  const box = document.getElementById('scenariosErrorBox');
  if (box) box.classList.add('hidden');
}

function renderDependsChips() {
  const c = document.getElementById('dependsChips');
  if (!c) return;
  const exclude = editorState.isNew ? null : editorState.originalKey;
  const opts = editorSuitesRegistry.filter(s => s.key !== exclude);
  if (!opts.length) {
    c.innerHTML = '<span class="text-[10px] text-zinc-500 italic">Список сютов пуст (/api/suites недоступен?). Зависимости можно указать позже.</span>';
    return;
  }
  c.innerHTML = opts.map(s => {
    const sel = editorState.dependsOn.includes(s.key);
    return `<button type="button" onclick="toggleDepends('${escAttr(s.key)}')" title="${escAttr(s.title || s.key)}"
      class="px-2 py-1 rounded-lg border text-[10px] font-mono transition ${sel ? 'bg-white text-zinc-950 border-white font-semibold' : 'bg-zinc-800 text-zinc-400 border-darkborder hover:border-zinc-500'}">
      <i class="fa-${sel ? 'solid fa-square-check' : 'regular fa-square'} mr-1"></i>${escapeHtml(s.key)}
    </button>`;
  }).join('');
}

function toggleDepends(key) {
  const i = editorState.dependsOn.indexOf(key);
  if (i >= 0) editorState.dependsOn.splice(i, 1);
  else editorState.dependsOn.push(key);
  renderDependsChips();
}

function varRowHtml(v) {
  return `<div class="flex items-center gap-1.5" data-var-row>
    <input data-var-name placeholder="имя" value="${escAttr(v.name)}" class="${SC_INP} w-44 shrink-0 font-mono">
    <span class="text-slate-500 text-xs">=</span>
    <input data-var-value placeholder="значение (можно {{uuid}}, {{today}})" value="${escAttr(v.value)}" class="${SC_INP} flex-1 font-mono">
    <button type="button" onclick="this.closest('[data-var-row]').remove()" class="px-1.5 py-1 text-red-400 hover:text-red-300 shrink-0" title="Удалить переменную"><i class="fa-solid fa-xmark"></i></button>
  </div>`;
}

function addVarRow() {
  document.getElementById('varsContainer').insertAdjacentHTML('beforeend', varRowHtml({ name: '', value: '' }));
}

function renderVarsRows() {
  document.getElementById('varsContainer').innerHTML = editorState.vars.map(varRowHtml).join('');
}

function subRowTemplate(kind) {
  const delBtn = '<button type="button" onclick="removeDynRow(this)" class="px-1.5 py-1 text-red-400 hover:text-red-300 shrink-0" title="Убрать строку"><i class="fa-solid fa-xmark"></i></button>';
  if (kind === 'headers') {
    return `<div class="flex items-center gap-1.5" data-dyn-row>
      <input data-sub-h-key placeholder="Header (Idempotency-Key)" value="" class="${SC_INP} w-44 shrink-0 font-mono">
      <input data-sub-h-value placeholder="значение" value="" class="${SC_INP} flex-1 font-mono">
      ${delBtn}
    </div>`;
  }
  if (kind === 'extract') {
    return `<div class="flex items-center gap-1.5" data-dyn-row>
      <input data-sub-ex-var placeholder="имя переменной" value="" class="${SC_INP} w-36 shrink-0 font-mono">
      <input data-sub-ex-path placeholder="$.data.id" value="" class="${SC_INP} flex-1 font-mono">
      ${delBtn}
    </div>`;
  }
  return `<div class="flex items-center gap-1.5" data-dyn-row>
    <input data-sub-as-path placeholder="$.status" value="" class="${SC_INP} flex-1 font-mono">
    <select data-sub-as-op class="${SC_INP} w-24 shrink-0">${ASSERT_OPS_HTTP.map(o => `<option value="${o}">${o}</option>`).join('')}</select>
    <input data-sub-as-value placeholder="ожидание (для eq/neq/contains)" value="" class="${SC_INP} w-44 font-mono">
    ${delBtn}
  </div>`;
}

function addStepSubRow(idx, kind) {
  const cont = document.getElementById(`sub-${kind}-${idx}`);
  if (cont) cont.insertAdjacentHTML('beforeend', subRowTemplate(kind));
}

function removeDynRow(btn) {
  const row = btn.closest('[data-dyn-row]');
  if (row) row.remove();
}

function stepCardHtml(step, idx) {
  const header = `
    <div class="flex items-start justify-between gap-2">
      <div class="grid grid-cols-1 sm:grid-cols-4 gap-2 flex-1 min-w-0">
        <select onchange="changeStepType(${idx}, this.value)" class="${SC_INP}">
          ${STEP_TYPES.map(t => `<option value="${t.value}" ${step.type === t.value ? 'selected' : ''}>${t.label}</option>`).join('')}
        </select>
        <input data-step-id placeholder="id (snake_case)" value="${escAttr(step.id)}" class="${SC_INP} font-mono">
        <input data-step-title placeholder="Название шага" value="${escAttr(step.title)}" class="${SC_INP} sm:col-span-2">
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <button type="button" onclick="moveStep(${idx}, -1)" title="Переместить выше" class="${SC_BTN_MINI}"><i class="fa-solid fa-arrow-up"></i></button>
        <button type="button" onclick="moveStep(${idx}, 1)" title="Переместить ниже" class="${SC_BTN_MINI}"><i class="fa-solid fa-arrow-down"></i></button>
        <button type="button" onclick="dupStep(${idx})" title="Дублировать шаг" class="${SC_BTN_MINI}"><i class="fa-solid fa-copy"></i></button>
        <button type="button" onclick="removeStep(${idx})" title="Удалить шаг" class="px-1.5 py-1 text-[10px] bg-slate-800 hover:bg-red-950 text-red-400 border border-darkborder rounded-md transition"><i class="fa-solid fa-trash"></i></button>
      </div>
    </div>`;

  let body = '';
  if (step.type === 'http') body = httpStepBodyHtml(step, idx);
  else if (step.type === 'delay') body = delayStepBodyHtml(step);
  else body = assertStepBodyHtml(step);

  return `<div class="step-card bg-slate-900/70 border border-darkborder rounded-xl p-3 space-y-3" data-step-index="${idx}" data-cur-type="${step.type}">
    <div class="text-[9px] font-bold uppercase tracking-wider text-slate-500">Шаг ${idx + 1}</div>
    ${header}
    ${body}
  </div>`;
}

function httpStepBodyHtml(step, idx) {
  const rows = (list, tpl) => list.map(tpl).join('');
  return `
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
      <div>
        <label class="${SC_LBL}">Роль (чей Bearer токен)</label>
        <select data-step-role class="${SC_INP}">${HTTP_ROLES.map(r => `<option value="${r}" ${step.role === r ? 'selected' : ''}>${r}</option>`).join('')}</select>
      </div>
      <div>
        <label class="${SC_LBL}">Метод</label>
        <select data-step-method class="${SC_INP}">${HTTP_METHODS.map(m => `<option value="${m}" ${step.method === m ? 'selected' : ''}>${m}</option>`).join('')}</select>
      </div>
      <div>
        <label class="${SC_LBL}">Path</label>
        <input data-step-path list="pathDatalist" placeholder="/api/..." value="${escAttr(step.path)}" class="${SC_INP} font-mono">
      </div>
    </div>

    <div>
      <label class="${SC_LBL}">Body — JSON, внутри строк работает подстановка &#123;&#123;var&#125;&#125;</label>
      <textarea data-step-body rows="4" placeholder='{ "phoneNumber": "+7{{uuid}}" }' class="${SC_INP} font-mono leading-relaxed">${escapeHtml(step.bodyRaw || '')}</textarea>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <div class="flex items-center justify-between mb-1">
          <label class="${SC_LBL} mb-0">Headers</label>
          <button type="button" onclick="addStepSubRow(${idx}, 'headers')" class="${SC_BTN_MINI}" title="Добавить заголовок"><i class="fa-solid fa-plus mr-0.5"></i>строка</button>
        </div>
        <div id="sub-headers-${idx}" class="space-y-1.5">${rows(step.headers || [], h => subRowHtmlWithValues('headers', h))}</div>
      </div>
      <div>
        <div class="flex items-center justify-between mb-1">
          <label class="${SC_LBL} mb-0">Extract (переменная ← JSONPath ответа)</label>
          <button type="button" onclick="addStepSubRow(${idx}, 'extract')" class="${SC_BTN_MINI}" title="Добавить извлечение переменной"><i class="fa-solid fa-plus mr-0.5"></i>строка</button>
        </div>
        <div id="sub-extract-${idx}" class="space-y-1.5">${rows(step.extract || [], e => subRowHtmlWithValues('extract', e))}</div>
      </div>
    </div>

    <div class="sm:w-56">
      <label class="${SC_LBL}">Ожидаемый статус (expectStatus)</label>
      <select data-step-expect class="${SC_INP}">
        ${EXPECT_STATUS_OPTIONS.map(o => `<option value="${o}" ${String(step.expectStatus || '') === o ? 'selected' : ''}>${o === '' ? 'любой' : o}</option>`).join('')}
      </select>
    </div>

    <div>
      <div class="flex items-center justify-between mb-1">
        <label class="${SC_LBL} mb-0">Asserts — проверки ответа по JSONPath</label>
        <button type="button" onclick="addStepSubRow(${idx}, 'asserts')" class="${SC_BTN_MINI}" title="Добавить проверку ответа"><i class="fa-solid fa-plus mr-0.5"></i>строка</button>
      </div>
      <div id="sub-asserts-${idx}" class="space-y-1.5">${rows(step.asserts || [], a => subRowHtmlWithValues('asserts', a))}</div>
    </div>`;
}

function delayStepBodyHtml(step) {
  return `<div class="sm:w-56">
    <label class="${SC_LBL}">Задержка, мс</label>
    <input type="number" min="1" step="1" data-step-ms value="${escAttr(step.ms)}" class="${SC_INP} font-mono">
  </div>`;
}

function assertStepBodyHtml(step) {
  return `<div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
    <div>
      <label class="${SC_LBL}">Left — значение/переменная</label>
      <input data-step-left placeholder="{{clientId}}" value="${escAttr(step.left)}" class="${SC_INP} font-mono">
    </div>
    <div>
      <label class="${SC_LBL}">Оператор</label>
      <select data-step-op class="${SC_INP}">${ASSERT_OPS_STEP.map(o => `<option value="${o}" ${step.op === o ? 'selected' : ''}>${o}</option>`).join('')}</select>
    </div>
    <div>
      <label class="${SC_LBL}">Value (не нужен для notEmpty)</label>
      <input data-step-value placeholder="значение" value="${escAttr(step.value)}" class="${SC_INP} font-mono">
    </div>
  </div>`;
}

function subRowHtmlWithValues(kind, entry) {
  const delBtn = '<button type="button" onclick="removeDynRow(this)" class="px-1.5 py-1 text-red-400 hover:text-red-300 shrink-0" title="Убрать строку"><i class="fa-solid fa-xmark"></i></button>';
  if (kind === 'headers') {
    return `<div class="flex items-center gap-1.5" data-dyn-row>
      <input data-sub-h-key placeholder="Header (Idempotency-Key)" value="${escAttr(entry.k)}" class="${SC_INP} w-44 shrink-0 font-mono">
      <input data-sub-h-value placeholder="значение" value="${escAttr(entry.v)}" class="${SC_INP} flex-1 font-mono">
      ${delBtn}
    </div>`;
  }
  if (kind === 'extract') {
    return `<div class="flex items-center gap-1.5" data-dyn-row>
      <input data-sub-ex-var placeholder="имя переменной" value="${escAttr(entry.k)}" class="${SC_INP} w-36 shrink-0 font-mono">
      <input data-sub-ex-path placeholder="$.data.id" value="${escAttr(entry.v)}" class="${SC_INP} flex-1 font-mono">
      ${delBtn}
    </div>`;
  }
  return `<div class="flex items-center gap-1.5" data-dyn-row>
    <input data-sub-as-path placeholder="$.status" value="${escAttr(entry.path)}" class="${SC_INP} flex-1 font-mono">
    <select data-sub-as-op class="${SC_INP} w-24 shrink-0">${ASSERT_OPS_HTTP.map(o => `<option value="${o}" ${entry.op === o ? 'selected' : ''}>${o}</option>`).join('')}</select>
    <input data-sub-as-value placeholder="ожидание (для eq/neq/contains)" value="${escAttr(entry.value)}" class="${SC_INP} w-44 font-mono">
    ${delBtn}
  </div>`;
}

function renderSteps() {
  const c = document.getElementById('stepsContainer');
  if (!c) return;
  c.innerHTML = editorState.steps.map((s, i) => stepCardHtml(s, i)).join('');
}

function addStep(type) {
  harvestEditor();
  editorState.steps.push(makeStep(type || 'http'));
  renderSteps();
}

function moveStep(idx, delta) {
  harvestEditor();
  const target = idx + delta;
  if (target < 0 || target >= editorState.steps.length) return;
  const [moved] = editorState.steps.splice(idx, 1);
  editorState.steps.splice(target, 0, moved);
  renderSteps();
}

function dupStep(idx) {
  harvestEditor();
  const clone = JSON.parse(JSON.stringify(editorState.steps[idx]));
  const used = new Set(editorState.steps.map(s => s.id));
  let nid = clone.id + '_copy';
  let k = 2;
  while (used.has(nid)) { nid = clone.id + '_copy' + k; k++; }
  clone.id = nid;
  editorState.steps.splice(idx + 1, 0, clone);
  renderSteps();
}

function removeStep(idx) {
  harvestEditor();
  editorState.steps.splice(idx, 1);
  if (!editorState.steps.length) editorState.steps.push(makeStep('http'));
  renderSteps();
}

function changeStepType(idx, newType) {
  harvestEditor();
  if (editorState.steps[idx].type === newType) return;
  editorState.steps[idx] = convertStep(editorState.steps[idx], newType);
  renderSteps();
}

function parseTags(raw) {
  return (raw || '').split(',').map(t => t.trim()).filter(Boolean);
}

function collectScenarioPayload() {
  harvestEditor();
  const errs = [];
  const st = editorState;

  if (!SCENARIO_KEY_PATTERN.test(st.key)) {
    errs.push('Key обязателен: 3–40 символов, строчные латинские буквы/цифры/«_», начинается с буквы (^[a-z][a-z0-9_]{2,39}$).');
  }
  if (!st.title) errs.push('Название (Title) обязательно.');
  if (!st.steps.length) errs.push('Добавьте хотя бы один шаг.');

  const seenIds = new Set();
  const stepsOut = [];
  st.steps.forEach((raw, i) => {
    const n = i + 1;
    const label = raw.id || '#' + n;
    if (!raw.id) errs.push(`Шаг ${n}: не заполнен id.`);
    else if (!STEP_ID_PATTERN.test(raw.id)) errs.push(`Шаг ${n}: id «${raw.id}» должен быть snake_case (^[a-z][a-z0-9_]*$).`);
    else if (seenIds.has(raw.id)) errs.push(`Шаг ${n}: дублирующийся id «${raw.id}».`);
    seenIds.add(raw.id);

    if (raw.type === 'http') {
      if (!raw.path) errs.push(`Шаг «${label}»: путь (path) обязателен.`);
      const out = { id: raw.id, title: raw.title, type: 'http', role: raw.role || 'none', method: raw.method || 'GET', path: raw.path };
      if (raw.bodyRaw && raw.bodyRaw.trim()) {
        try {
          out.body = JSON.parse(raw.bodyRaw);
        } catch (e) {
          errs.push(`Шаг «${label}»: body не является корректным JSON (${e.message}).`);
        }
      }
      if (raw.headers.length) {
        const h = {};
        raw.headers.forEach(x => { h[x.k] = x.v; });
        out.headers = h;
      }
      if (raw.extract.length) {
        const ex = {};
        raw.extract.forEach(x => { ex[x.k] = x.v; });
        out.extract = ex;
      }
      if (raw.expectStatus) out.expectStatus = /^\d+$/.test(raw.expectStatus) ? Number(raw.expectStatus) : raw.expectStatus;
      if (raw.asserts.length) {
        out.asserts = raw.asserts.map(a => {
          const o = { path: a.path, op: a.op };
          if (a.op !== 'exists' && a.value !== '') o.value = a.value;
          return o;
        });
      }
      stepsOut.push(out);
    } else if (raw.type === 'delay') {
      const ms = Number(raw.ms);
      if (raw.ms === '' || !Number.isFinite(ms) || ms <= 0) {
        errs.push(`Шаг «${label}»: задержка ms должна быть положительным числом.`);
      }
      stepsOut.push({ id: raw.id, title: raw.title, type: 'delay', ms: Number.isFinite(ms) ? ms : raw.ms });
    } else {
      if (!raw.left) errs.push(`Шаг «${label}»: left обязателен (например, {{имя_переменной}}).`);
      const check = { op: raw.op || 'notEmpty' };
      if ((raw.op || 'notEmpty') !== 'notEmpty' && raw.value !== '') check.value = raw.value;
      stepsOut.push({ id: raw.id, title: raw.title, type: 'assert', left: raw.left, check });
    }
  });

  if (errs.length) return { ok: false, errors: errs };

  const payload = {
    key: st.key,
    title: st.title,
    tags: parseTags(st.tagsRaw),
    category: 'custom',
    steps: stepsOut,
  };
  if (st.description) payload.description = st.description;
  if (st.dependsOn.length) payload.dependsOn = [...st.dependsOn];
  if (st.vars.length) {
    const vars = {};
    st.vars.forEach(x => { vars[x.name] = x.value; });
    payload.vars = vars;
  }
  return { ok: true, payload };
}

async function saveScenario(btn) {
  const collected = collectScenarioPayload();
  if (!collected.ok) {
    showScenariosError('Исправьте ошибки валидации:\n• ' + collected.errors.join('\n• '));
    logTerminal('WARN', '[Редактор] Сценарий не сохранён: ошибки валидации (' + collected.errors.length + ').');
    toastError('Сценарий не сохранён: исправьте ошибки валидации (' + collected.errors.length + ')');
    return false;
  }
  hideScenariosError();

  const doSave = async () => {
    const isNew = editorState.isNew;
    const url = isNew ? '/api/scenarios' : '/api/scenarios/' + encodeURIComponent(collected.payload.key);

    try {
      const r = await fetch(url, {
        method: isNew ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(collected.payload),
      });
      if (!r.ok) {
        const t = await r.text();
        showScenariosError(`Сервер отклонил сохранение (HTTP ${r.status}):\n${t || '(пустой ответ)'}`);
        toastError('Сервер отклонил сохранение сценария (HTTP ' + r.status + ')');
        return false;
      }
      toastSuccess('Сценарий «' + collected.payload.key + '» сохранён.');
      logTerminal('SUCCESS', `[Редактор] Сценарий «${collected.payload.key}» сохранён.`);
      editorState.isNew = false;
      editorState.originalKey = collected.payload.key;
      setKeyInputLocked(true);
      updateModeBadge();
      await loadScenarioEditor();
      return true;
    } catch (err) {
      showScenariosError('Сеть недоступна или бэкенд не отвечает:\n' + err.message);
      toastError('Сеть недоступна: ' + err.message);
      return false;
    }
  };

  if (btn) return busyWrap(btn, doSave);
  return doSave();
}

let scenarioRunInFlight = false;

async function runScenarioFromEditor(btn) {
  const doRun = async () => {
    if (scenarioRunInFlight) return;
    scenarioRunInFlight = true;
    try {
      const saved = await saveScenario();
      if (!saved || !editorState.originalKey) return;

      const key = editorState.originalKey;
      logTerminal('INFO', `[Редактор] Запуск сценария «${key}»...`);
      toastInfo('Запуск сценария «' + key + '»…');

      switchTab('checklists');
      markSuitesRunning([key]);

      try {
        const r = await fetch('/api/runs', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ suite: key }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
      } catch (err) {
        finalizeSuiteCard(key, false);
        logTerminal('ERROR', `[Редактор] Не удалось запустить «${key}»: ${err.message}`);
        toastError('Не удалось запустить сценарий «' + key + '»: ' + err.message);
      }
    } finally {
      scenarioRunInFlight = false;
    }
  };
  return busyWrap(btn, doRun);
}

function suggestCopyKey(key) {
  if (!key) return '';
  return key.slice(0, 35) + '_copy';
}

function duplicateScenario() {
  harvestEditor();
  const srcKey = editorState.key;
  const srcTitle = editorState.title;
  editorState.isNew = true;
  editorState.originalKey = null;
  editorState.key = suggestCopyKey(srcKey);
  editorState.title = srcTitle ? srcTitle + ' (копия)' : 'Копия сценария';
  setKeyInputLocked(false);
  hideScenariosError();
  document.getElementById('scKey').value = editorState.key;
  document.getElementById('scTitle').value = editorState.title;
  updateModeBadge();
  renderScenarioList();
  renderDependsChips();
  toastInfo('Сценарий скопирован в новый черновик — поправьте key и сохраните.');
  logTerminal('INFO', '[Редактор] Сценарий скопирован в новый черновик — при необходимости поправьте key и сохраните.');
}

async function deleteCustomScenario(key) {
  const ok = await confirmDialog(
    'Удалить сценарий?',
    'Сценарий «' + key + '» будет удалён безвозвратно.',
    'Удалить'
  );
  if (!ok) return;
  try {
    const r = await fetch('/api/scenarios/' + encodeURIComponent(key), { method: 'DELETE' });
    if (!r.ok) {
      const t = await r.text();
      toastError('Не удалось удалить сценарий «' + key + '»: HTTP ' + r.status + ' ' + t);
      return;
    }
    toastSuccess('Сценарий «' + key + '» удалён.');
    logTerminal('SUCCESS', `[Редактор] Сценарий «${key}» удалён.`);
    if (editorState && editorState.originalKey === key) newScenario();
    await Promise.all([loadScenarioEditor(), loadSuites()]);
  } catch (err) {
    toastError('Ошибка удаления сценария: ' + err.message);
  }
}

async function deleteScenarioFromEditor() {
  if (!editorState || !editorState.originalKey) {
    toastInfo('Черновик ещё не сохранён — удалять на сервере нечего.');
    return;
  }
  await deleteCustomScenario(editorState.originalKey);
}

function cancelScenarioEdit() {
  hideScenariosError();
  newScenario();
  logTerminal('INFO', '[Редактор] Форма очищена.');
}

function insertExampleScenario() {
  editorState = {
    isNew: true,
    originalKey: null,
    key: 'my_smoke_example',
    title: 'Пример: смоук регистрации клиента',
    description: 'Мини-сценарий из шпаргалки: регистрация клиента, пауза и проверка извлечённой переменной.',
    tagsRaw: 'example, smoke',
    dependsOn: [],
    vars: [{ name: 'phone_suffix', value: '{{uuid}}' }],
    steps: [
      {
        type: 'http',
        id: 'register_client',
        title: 'Регистрация клиента',
        role: 'client',
        method: 'POST',
        path: '/api/clients/register',
        bodyRaw: JSON.stringify({
          phoneNumber: '+7999{{phone_suffix}}',
          firstName: 'Иван',
          lastName: 'Тестовый',
          cityKey: 'Москва',
        }, null, 2),
        headers: [],
        extract: [{ k: 'clientId', v: '$.data.id' }],
        expectStatus: '200',
        asserts: [],
      },
      { type: 'delay', id: 'wait_a_bit', title: 'Подождать обработки', ms: '300' },
      { type: 'assert', id: 'client_created', title: 'ID клиента извлечён', left: '{{clientId}}', op: 'notEmpty', value: '' },
    ],
  };
  setKeyInputLocked(false);
  hideScenariosError();
  renderEditorForm();
  toastInfo('Пример вставлен в форму — адаптируйте под свой стенд и сохраните.');
  logTerminal('INFO', '[Редактор] Пример вставлен в форму — адаптируйте под свой стенд и сохраните.');
}
