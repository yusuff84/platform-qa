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

let scenarioSearchQuery = '';

function filterScenarioList(query) {
  scenarioSearchQuery = (query || '').toLowerCase().trim();
  renderScenarioList();
}
window.filterScenarioList = filterScenarioList;

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

  let filtered = scenariosRegistry;
  if (scenarioSearchQuery) {
    filtered = filtered.filter(sc =>
      (sc.key && sc.key.toLowerCase().includes(scenarioSearchQuery)) ||
      (sc.title && sc.title.toLowerCase().includes(scenarioSearchQuery)) ||
      (sc.description && sc.description.toLowerCase().includes(scenarioSearchQuery)) ||
      (sc.tags && sc.tags.some(t => String(t).toLowerCase().includes(scenarioSearchQuery)))
    );
  }

  if (!filtered.length) {
    c.innerHTML = `<div class="p-4 text-center text-xs text-zinc-500 italic border border-dashed border-darkborder rounded-lg">Сценарии не найдены</div>`;
    return;
  }

  const activeKey = editorState && !editorState.isNew ? editorState.originalKey : null;
  c.innerHTML = filtered.map(sc => {
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

const PRESET_SCENARIOS = {
  order_flow: {
    key: 'custom_order_flow',
    title: 'Полный флоу заказа: клиент -> ресторан -> курьер',
    description: 'Сквозная проверка создания, подтверждения и отслеживания заказа',
    tags: ['flow', 'orders', 'client'],
    vars: { phone_suffix: '{{uuid}}' },
    dependsOn: [],
    steps: [
      {
        id: 'reg_client',
        title: 'Регистрация тестового клиента',
        type: 'http',
        role: 'client',
        method: 'POST',
        path: '/api/clients/register',
        bodyRaw: '{\n  "phoneNumber": "+7999{{phone_suffix}}",\n  "firstName": "Клиент",\n  "lastName": "Тестовый",\n  "cityKey": "Москва"\n}',
        headers: [],
        extract: [{ name: 'clientId', path: '$.data.id' }],
        expectStatus: '200',
        asserts: [{ path: '$.data.id', op: 'exists' }]
      },
      {
        id: 'create_order',
        title: 'Оформление заказа в ресторане',
        type: 'http',
        role: 'client',
        method: 'POST',
        path: '/api/clients/create-order',
        bodyRaw: '{\n  "deliveryAddress": "Москва, Тверская 12",\n  "paymentMethod": "card",\n  "items": [{ "dishId": "dish_burger_1", "count": 2 }]\n}',
        headers: [],
        extract: [{ name: 'orderId', path: '$.data.id' }],
        expectStatus: '200',
        asserts: [{ path: '$.data.id', op: 'notEmpty' }]
      },
      {
        id: 'check_order',
        title: 'Проверка ID созданного заказа',
        type: 'assert',
        left: '{{orderId}}',
        op: 'notEmpty',
        value: ''
      }
    ]
  },
  client_reg: {
    key: 'custom_client_reg',
    title: 'Регистрация и получение профиля клиента',
    description: 'Проверка ручки регистрации и эндпоинта данных профиля клиента',
    tags: ['client', 'auth', 'smoke'],
    vars: { phone_suffix: '{{uuid}}' },
    dependsOn: [],
    steps: [
      {
        id: 'register',
        title: 'Регистрация клиента по номеру',
        type: 'http',
        role: 'client',
        method: 'POST',
        path: '/api/clients/register',
        bodyRaw: '{\n  "phoneNumber": "+7999{{phone_suffix}}",\n  "firstName": "Тестер",\n  "lastName": "Профильный"\n}',
        headers: [],
        extract: [{ name: 'clientId', path: '$.data.id' }],
        expectStatus: '200',
        asserts: []
      },
      {
        id: 'get_profile',
        title: 'Запрос профиля клиента',
        type: 'http',
        role: 'client',
        method: 'GET',
        path: '/api/clients/profile',
        bodyRaw: '',
        headers: [],
        extract: [],
        expectStatus: '200',
        asserts: [{ path: '$.status', op: 'eq', value: 'success' }]
      }
    ]
  },
  menu_smoke: {
    key: 'custom_menu_smoke',
    title: 'Смоук: каталог меню и модификаторы',
    description: 'Проверка ручек списка блюд, категорий и модификаторов ресторана',
    tags: ['menu', 'rest', 'smoke'],
    vars: {},
    dependsOn: [],
    steps: [
      {
        id: 'get_dishes',
        title: 'Получение каталога блюд',
        type: 'http',
        role: 'client',
        method: 'GET',
        path: '/api/clients/dishes',
        bodyRaw: '',
        headers: [],
        extract: [{ name: 'firstDishId', path: '$.data[0].id' }],
        expectStatus: '200',
        asserts: [{ path: '$.data', op: 'notEmpty' }]
      },
      {
        id: 'get_modificators',
        title: 'Проверка модификаторов блюд ресторана',
        type: 'http',
        role: 'client',
        method: 'GET',
        path: '/api/rests/modificators',
        bodyRaw: '',
        headers: [],
        extract: [],
        expectStatus: '200',
        asserts: []
      }
    ]
  },
  rbac_gate: {
    key: 'custom_rbac_security',
    title: 'RBAC: закрытый доступ без токена (401)',
    description: 'Проверка защиты приватных ручек бэкенда от неавторизованных запросов',
    tags: ['security', 'rbac', '401'],
    vars: {},
    dependsOn: [],
    steps: [
      {
        id: 'unauth_access',
        title: 'Попытка вызова админ-ручки без авторизации',
        type: 'http',
        role: 'none',
        method: 'POST',
        path: '/api/admin/give-order-to-courier',
        bodyRaw: '{\n  "orderId": "test-123"\n}',
        headers: [],
        extract: [],
        expectStatus: '401',
        asserts: []
      }
    ]
  }
};

function insertPresetScenario(presetKey) {
  const tpl = PRESET_SCENARIOS[presetKey];
  if (!tpl) return;
  editorState = {
    isNew: true,
    originalKey: null,
    key: tpl.key,
    title: tpl.title,
    description: tpl.description,
    tagsRaw: (tpl.tags || []).join(', '),
    dependsOn: [...(tpl.dependsOn || [])],
    vars: Object.entries(tpl.vars || {}).map(([name, value]) => ({ name, value: String(value) })),
    steps: JSON.parse(JSON.stringify(tpl.steps))
  };
  if (!editorState.vars.length) editorState.vars = [{ name: '', value: '' }];
  setKeyInputLocked(false);
  hideScenariosError();
  renderEditorForm();
  toastSuccess(`Шаблон «${tpl.title}» загружен в форму.`);
}
window.insertPresetScenario = insertPresetScenario;

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

let currentScenarioEditorMode = 'graph';

function setScenarioEditorMode(mode) {
  currentScenarioEditorMode = mode || 'graph';
  harvestEditor();

  const gView = document.getElementById('scenarioGraphView');
  const fView = document.getElementById('scenarioFormView');
  const jView = document.getElementById('scenarioJsonView');

  if (gView) gView.classList.toggle('hidden', currentScenarioEditorMode !== 'graph');
  if (fView) fView.classList.toggle('hidden', currentScenarioEditorMode !== 'form');
  if (jView) jView.classList.toggle('hidden', currentScenarioEditorMode !== 'json');

  ['graph', 'form', 'json'].forEach(m => {
    const btn = document.getElementById(`sc-mode-btn-${m}`);
    if (btn) {
      if (m === currentScenarioEditorMode) {
        btn.className = 'px-3 py-1.5 rounded-md font-semibold transition flex items-center gap-1.5 bg-white text-zinc-950 shadow-sm';
      } else {
        btn.className = 'px-3 py-1.5 rounded-md font-medium transition flex items-center gap-1.5 text-zinc-400 hover:text-white';
      }
    }
  });

  if (currentScenarioEditorMode === 'graph') {
    renderScenarioGraph();
  } else if (currentScenarioEditorMode === 'form') {
    renderSteps();
  } else if (currentScenarioEditorMode === 'json') {
    updateScenarioJson();
  }
}
window.setScenarioEditorMode = setScenarioEditorMode;

function focusStepInForm(idx) {
  setScenarioEditorMode('form');
  setTimeout(() => {
    const card = document.querySelector(`.step-card[data-step-index="${idx}"]`);
    if (card) {
      card.scrollIntoView({ behavior: 'smooth', block: 'center' });
      card.classList.add('ring-2', 'ring-zinc-400');
      setTimeout(() => card.classList.remove('ring-2', 'ring-zinc-400'), 1500);
    }
  }, 100);
}
window.focusStepInForm = focusStepInForm;

function insertStepAt(idx, type) {
  harvestEditor();
  const step = makeStep(type || 'http');
  editorState.steps.splice(idx, 0, step);
  renderSteps();
  renderScenarioGraph();
  updateScenarioJson();
  toastSuccess(`Узел шага добавлен на позицию #${idx + 1}`);
}
window.insertStepAt = insertStepAt;

function renderScenarioGraph() {
  const canvas = document.getElementById('scenarioGraphCanvas');
  const counter = document.getElementById('graphStepCount');
  if (!canvas || !editorState) return;

  const steps = editorState.steps || [];
  if (counter) {
    counter.textContent = `${steps.length} ${pluralRu(steps.length, ['узел', 'узла', 'узлов'])}`;
  }

  // 1. Start Node (Vars)
  const varCount = (editorState.vars || []).filter(v => v.name).length;
  const varsHtml = varCount
    ? editorState.vars.filter(v => v.name).map(v => `<span class="px-1.5 py-0.5 rounded text-[9px] font-mono bg-zinc-800 text-zinc-300 border border-zinc-700 truncate max-w-[180px]">${escapeHtml(v.name)}=${escapeHtml(v.value || '""')}</span>`).join('')
    : '<span class="text-[10px] text-zinc-500 italic">Без переменных</span>';

  let html = `
    <!-- START NODE -->
    <div class="flow-node p-3 space-y-2">
      <div class="flex items-center justify-between">
        <span class="text-[10px] font-mono font-semibold px-2 py-0.5 rounded bg-zinc-800 text-zinc-200 border border-zinc-700 flex items-center gap-1">
          <i class="fa-solid fa-play text-[8px] text-emerald-400"></i>СТАРТ
        </span>
        <span class="text-[10px] font-mono text-zinc-500">Вход</span>
      </div>
      <div>
        <div class="text-xs font-semibold text-white">Инициализация</div>
        <div class="flex flex-wrap gap-1 mt-1.5">${varsHtml}</div>
      </div>
    </div>
  `;

  // Connector between Start and Step 1
  html += `
    <div class="flow-connector">
      <div class="flow-line"></div>
      <i class="fa-solid fa-chevron-right flow-arrow-icon"></i>
    </div>
  `;

  // 2. Step Nodes
  steps.forEach((step, idx) => {
    const isHttp = step.type === 'http';
    const isDelay = step.type === 'delay';
    const isAssert = step.type === 'assert';

    const typeBadge = isHttp
      ? `<span class="px-1.5 py-0.5 rounded text-[10px] font-mono font-bold bg-zinc-800 text-zinc-100 border border-zinc-700">${escapeHtml(step.method || 'GET')}</span>`
      : isDelay
      ? `<span class="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-300 border border-zinc-700"><i class="fa-solid fa-hourglass-half mr-1 text-zinc-400"></i>${escapeHtml(step.ms || '500')}мс</span>`
      : `<span class="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium bg-zinc-800 text-zinc-300 border border-zinc-700"><i class="fa-solid fa-clipboard-check mr-1 text-zinc-400"></i>ASSERT</span>`;

    const roleBadge = isHttp && step.role && step.role !== 'none'
      ? `<span class="px-1.5 py-0.5 rounded text-[9px] font-mono bg-zinc-800 text-zinc-300 border border-darkborder">${escapeHtml(step.role)}</span>`
      : '';

    const statusExpect = isHttp && step.expectStatus
      ? `<span class="px-1.5 py-0.5 rounded text-[9px] font-mono bg-zinc-800 text-zinc-300 border border-darkborder">${escapeHtml(step.expectStatus)}</span>`
      : '';

    let contentHtml = '';
    if (isHttp) {
      contentHtml = `
        <div class="font-mono text-[11px] text-zinc-200 truncate bg-zinc-950 px-2 py-1 rounded border border-darkborder" title="${escAttr(step.path || '/...')}">
          ${escapeHtml(step.path || '/api/...')}
        </div>
      `;
    } else if (isDelay) {
      contentHtml = `
        <div class="text-xs text-zinc-300 flex items-center gap-1.5">
          <i class="fa-regular fa-clock text-zinc-500"></i>
          <span>Пауза: <strong class="text-white">${escapeHtml(step.ms || '500')} мс</strong></span>
        </div>
      `;
    } else {
      contentHtml = `
        <div class="font-mono text-[11px] text-zinc-200 bg-zinc-950 px-2 py-1 rounded border border-darkborder truncate" title="${escAttr(step.left)} ${escAttr(step.op)} ${escAttr(step.value)}">
          ${escapeHtml(step.left || 'left')} <span class="text-zinc-500">${escapeHtml(step.op)}</span> ${escapeHtml(step.value || '')}
        </div>
      `;
    }

    // Extracted variables
    let extractsHtml = '';
    if (isHttp && step.extract && step.extract.length > 0) {
      extractsHtml = `
        <div class="pt-1.5 border-t border-darkborder flex items-center gap-1 flex-wrap">
          <span class="text-[9px] font-mono text-zinc-500 uppercase">Экстракт:</span>
          ${step.extract.map(e => `<span class="px-1 py-0.2 rounded text-[9px] font-mono bg-zinc-800 text-zinc-200 border border-zinc-700 truncate max-w-[110px]" title="${escAttr(e.k)} ← ${escAttr(e.v)}">+${escapeHtml(e.k)}</span>`).join('')}
        </div>
      `;
    }

    html += `
      <!-- NODE STEP ${idx + 1} -->
      <div class="flow-node p-3 space-y-2 cursor-pointer" onclick="focusStepInForm(${idx})">
        <div class="flex items-center justify-between gap-1">
          <div class="flex items-center gap-1.5 min-w-0">
            ${typeBadge}
            <span class="text-[10px] font-mono text-zinc-400">#${idx + 1}</span>
            ${roleBadge}
            ${statusExpect}
          </div>
          <div class="flex items-center gap-1 shrink-0" onclick="event.stopPropagation()">
            ${idx > 0 ? `<button onclick="moveStep(${idx}, -1)" type="button" class="w-5 h-5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-300 text-[10px] flex items-center justify-center transition" title="Сдвинуть левее"><i class="fa-solid fa-arrow-left"></i></button>` : ''}
            ${idx < steps.length - 1 ? `<button onclick="moveStep(${idx}, 1)" type="button" class="w-5 h-5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-300 text-[10px] flex items-center justify-center transition" title="Сдвинуть правее"><i class="fa-solid fa-arrow-right"></i></button>` : ''}
            <button onclick="removeStep(${idx})" type="button" class="w-5 h-5 rounded bg-zinc-800 hover:bg-red-950 text-zinc-400 hover:text-red-400 text-[10px] flex items-center justify-center transition" title="Удалить узел"><i class="fa-solid fa-xmark"></i></button>
          </div>
        </div>

        <div>
          <div class="text-xs font-semibold text-white truncate" title="${escAttr(step.title || step.id)}">${escapeHtml(step.title || step.id)}</div>
          <div class="text-[10px] font-mono text-zinc-500 truncate">${escapeHtml(step.id)}</div>
        </div>

        ${contentHtml}
        ${extractsHtml}
      </div>
    `;

    // Connector with insert button between nodes
    html += `
      <div class="flow-connector flex flex-col items-center">
        <div class="flex items-center">
          <div class="flow-line"></div>
          <button onclick="event.stopPropagation(); insertStepAt(${idx + 1})" title="Вставить шаг между узлами" class="w-5 h-5 rounded-full bg-zinc-800 hover:bg-white hover:text-zinc-950 text-zinc-400 border border-darkborder flex items-center justify-center text-[10px] transition shadow-sm shrink-0">
            <i class="fa-solid fa-plus text-[9px]"></i>
          </button>
          <div class="flow-line"></div>
          <i class="fa-solid fa-chevron-right flow-arrow-icon"></i>
        </div>
      </div>
    `;
  });

  // 3. End Node (Success)
  html += `
    <!-- END NODE -->
    <div class="flow-node p-3 space-y-2">
      <div class="flex items-center justify-between">
        <span class="text-[10px] font-mono font-semibold px-2 py-0.5 rounded bg-zinc-800 text-zinc-200 border border-zinc-700 flex items-center gap-1">
          <i class="fa-solid fa-flag-checkered text-[9px] text-emerald-400"></i>ФИНИШ
        </span>
        <span class="text-[10px] font-mono text-zinc-500">Завершение</span>
      </div>
      <div>
        <div class="text-xs font-semibold text-white">Успешное выполнение</div>
        <div class="text-[10px] text-zinc-400 mt-1">Все утверждения сценария верны</div>
      </div>
    </div>
  `;

  canvas.innerHTML = html;
}

function updateScenarioJson() {
  const editor = document.getElementById('scenarioJsonEditor');
  if (!editor || !editorState) return;
  const specObj = {
    key: editorState.key,
    title: editorState.title,
    description: editorState.description,
    tags: editorState.tagsRaw ? editorState.tagsRaw.split(',').map(s => s.trim()).filter(Boolean) : [],
    category: 'custom',
    vars: (editorState.vars || []).reduce((acc, v) => { if (v.name) acc[v.name] = v.value; return acc; }, {}),
    dependsOn: editorState.dependsOn || [],
    steps: editorState.steps || []
  };
  editor.value = JSON.stringify(specObj, null, 2);
}

function copyScenarioJson() {
  const editor = document.getElementById('scenarioJsonEditor');
  if (!editor) return;
  navigator.clipboard.writeText(editor.value).then(() => {
    toastSuccess('JSON спецификация сценария скопирована в буфер!');
  });
}
window.copyScenarioJson = copyScenarioJson;

function applyScenarioJson() {
  const editor = document.getElementById('scenarioJsonEditor');
  if (!editor) return;
  try {
    const parsed = JSON.parse(editor.value);
    if (!parsed.key) throw new Error('Поле "key" обязательно');
    editorState = {
      isNew: editorState.isNew,
      originalKey: editorState.originalKey,
      key: parsed.key,
      title: parsed.title || '',
      description: parsed.description || '',
      tagsRaw: (parsed.tags || []).join(', '),
      dependsOn: [...(parsed.dependsOn || [])],
      vars: Object.entries(parsed.vars || {}).map(([name, value]) => ({ name, value: String(value) })),
      steps: (parsed.steps || []).map(normalizeStep)
    };
    renderEditorForm();
    toastSuccess('JSON спецификация успешно синхронизирована с графом!');
    setScenarioEditorMode('graph');
  } catch (err) {
    toastError('Ошибка JSON: ' + err.message);
  }
}
window.applyScenarioJson = applyScenarioJson;

function renderEditorForm() {
  const g = id => document.getElementById(id);
  g('scKey').value = editorState.key;
  g('scTitle').value = editorState.title;
  g('scDesc').value = editorState.description;
  g('scTags').value = editorState.tagsRaw || '';
  renderDependsChips();
  renderVarsRows();
  renderSteps();
  renderScenarioGraph();
  updateScenarioJson();
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
  renderScenarioGraph();
  updateScenarioJson();
}

function moveStep(idx, delta) {
  harvestEditor();
  const target = idx + delta;
  if (target < 0 || target >= editorState.steps.length) return;
  const [moved] = editorState.steps.splice(idx, 1);
  editorState.steps.splice(target, 0, moved);
  renderSteps();
  renderScenarioGraph();
  updateScenarioJson();
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
  renderScenarioGraph();
  updateScenarioJson();
}

function removeStep(idx) {
  harvestEditor();
  editorState.steps.splice(idx, 1);
  if (!editorState.steps.length) editorState.steps.push(makeStep('http'));
  renderSteps();
  renderScenarioGraph();
  updateScenarioJson();
}

function changeStepType(idx, newType) {
  harvestEditor();
  if (editorState.steps[idx].type === newType) return;
  editorState.steps[idx] = convertStep(editorState.steps[idx], newType);
  renderSteps();
  renderScenarioGraph();
  updateScenarioJson();
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
