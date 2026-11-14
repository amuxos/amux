'use strict';
// A deliberately small DOM test double. No npm dependency and no network services.
// Layout/visual acceptance belongs to browser verification, not this DOM double.
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

class Node {
  constructor(tag = '', text = '') { this.tagName = tag.toUpperCase(); this.nodeType = tag ? 1 : 3; this.children = []; this.attrs = {}; this.listeners = {}; this._text = String(text); this.value = ''; this.checked = false; this.parentNode = null; this.dataset = {}; this.scrollTop = 0; this.scrollHeight = 300; this.clientHeight = 300; this.offsetParent = {}; }
  get className() { return this.attrs.class || ''; }
  set className(v) { this.attrs.class = v; }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'name' || k === 'id' || k === 'type') this[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(k, v) { (this.listeners[k] ||= []).push(v); }
  async fire(k, props = {}) { for (const fn of this.listeners[k] || []) await fn(Object.assign({ target: this, preventDefault() {} }, props)); }
  async click() { if (!this.disabled) await this.fire('click'); }
  appendChild(n) { if (!n || typeof n !== 'object' || !n.nodeType) throw Error('Non-node appended'); n.parentNode = this; this.children.push(n); return n; }
  append(...ns) { for (const n of ns) this.appendChild(typeof n === 'object' ? n : new Node('', n)); }
  replaceChildren(...ns) { this.children.forEach(n => n.parentNode = null); this.children = []; this._text = ''; this.append(...ns); }
  prepend(...ns) { for (const n of ns.reverse()) { n.parentNode = this; this.children.unshift(n); } }
  after(n) { const i = this.parentNode.children.indexOf(this); n.parentNode = this.parentNode; this.parentNode.children.splice(i + 1, 0, n); }
  remove() { if (this.parentNode) { this.parentNode.children = this.parentNode.children.filter(n => n !== this); this.parentNode = null; } }
  get textContent() { return this._text + this.children.map(n => n.textContent).join(''); }
  set textContent(v) { this._text = String(v); this.children = []; }
  get childElementCount() { return this.children.filter(n => n.nodeType === 1).length; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n.tagName === 'DOCUMENT'; }
  matches(selector) {
    const simple = selector.replace(/:not\(:disabled\)/g, '');
    if (selector.includes(':not(:disabled)') && this.disabled) return false;
    const tag = simple.match(/^[\w-]+/); if (tag && this.tagName !== tag[0].toUpperCase()) return false;
    for (const match of simple.matchAll(/#([\w-]+)/g)) if (this.id !== match[1]) return false;
    for (const match of simple.matchAll(/\.([\w-]+)/g)) if (!this.className.split(' ').includes(match[1])) return false;
    for (const match of simple.matchAll(/\[([^=\]]+)(?:=["']?([^\]"']+)["']?)?\]/g)) {
      if (!(match[1] in this.attrs)) return false;
      if (match[2] !== undefined && this.attrs[match[1]] !== match[2]) return false;
    }
    return this.nodeType === 1;
  }
  querySelectorAll(selector) {
    const result = [];
    const selectors = selector.split(',').map(s => s.trim().split(/\s+/));
    const check = (node, parts) => {
      if (!node.matches(parts[parts.length - 1])) return false;
      let ancestor = node.parentNode;
      for (let i = parts.length - 2; i >= 0; i--) { while (ancestor && !ancestor.matches(parts[i])) ancestor = ancestor.parentNode; if (!ancestor) return false; ancestor = ancestor.parentNode; }
      return true;
    };
    const walk = n => { for (const child of n.children) { if (selectors.some(parts => check(child, parts))) result.push(child); walk(child); } };
    walk(this); return result;
  }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  closest(s) { let n = this; while (n) { if (n.matches(s)) return n; n = n.parentNode; } return null; }
  focus() { this.ownerDocument.activeElement = this; }
  setSelectionRange() {}
  reportValidity() { return this.querySelectorAll('[required]').every(n => n.disabled || String(n.value).trim()); }
}
function environment(hash = '#sessions') {
  const document = new Node('document');
  document.createElement = tag => { const n = new Node(tag); n.ownerDocument = document; return n; };
  document.createTextNode = text => new Node('', text);
  document.createElementNS = (_, tag) => document.createElement(tag);
  document.getElementById = id => document.querySelector('#' + id);
  document.documentElement = document.createElement('html'); document.appendChild(document.documentElement);
  document.body = document.createElement('body'); document.documentElement.appendChild(document.body);
  for (const id of ['app', 'dialogs', 'notifications']) { const n = document.createElement('div'); n.setAttribute('id', id); document.body.appendChild(n); }
  const calls = [], listeners = {}, stored = [], logs = [], timers = new Map();
  let now = Date.now(), nextTimer = 1;
  class ClockDate extends Date { static now() { return now; } }
  const fixture = {
    config: { mode: 'pair', expires_at: 0, insecure_transport: false },
    bootstrap: { authenticated: true, operator: true, csrf_token: 'csrf-test', version: 'test', auth_mode: 'pair', expires_at: 0, insecure_transport: false },
    sessions: { sessions: [{ id: 's', name: '<img src=x onerror=alert(1)>', state: 'running', target_id: 'r', agent_type: 'codex' }], actions: ['create', 'submit', 'archive'] },
    catalogs: { runtimes: [{ name: 'codex', models: [{ ref: 'provider/model', display_name: 'Model' }] }], targets: [{ id: 'r', name: 'Target' }] },
    profiles: { profiles: [{ ref: 'builtin/general', name: 'General', skills: ['tools'] }] },
    bots: { bots: [{ profile: 'bot1', id: 'b', name: 'Assistant', status: 'running', version: 3, settings: { default_agent: 'builtin/general', default_runtime_ref: '', default_model_ref: '', default_reasoning_effort: '', interaction_mode: 'thread', isolation_required: false }, actions: ['get', 'save'] }] }
  };
  const window = {
    document, AMUX_CONSOLE_TEST: true, navigator: { language: 'en-US', onLine: true }, location: { hash, pathname: '/', search: '' }, history: { replaceState(_a, _b, path) { window.location.hash = ''; calls.push({ history: path }); } },
    localStorage: { getItem() { return null; }, setItem(key, value) { stored.push([key, value]); } }, sessionStorage: { setItem(key, value) { stored.push([key, value]); } }, matchMedia() { return { matches: false, addEventListener() {} }; }, addEventListener(k, fn) { (listeners[k] ||= []).push(fn); }, scrollTo() {}, scrollY: 0,
    confirm() { return true; }, crypto: require('node:crypto').webcrypto, atob,
    setTimeout(fn, delay) { const id = nextTimer++; timers.set(id, { fn, at: now + delay }); return id; }, clearTimeout(id) { timers.delete(id); },
    fetch: async (url, options) => { calls.push({ url, options }); const resource = url.split('/').pop().split('?')[0]; const payload = options.method === 'POST' ? { ok: true, status: 'success' } : fixture[resource] || {}; return { ok: true, status: 200, json: async () => payload }; },
    EventSource: class { constructor(url) { this.url = url; this.listeners = {}; } addEventListener(k, fn) { this.listeners[k] = fn; } close() { this.closed = true; } }
  };
  class FormDataDouble { constructor(form) { this.entries = form.querySelectorAll('[name]').filter(n => !n.disabled).map(n => [n.name, n.value]); } [Symbol.iterator]() { return this.entries[Symbol.iterator](); } }
  const safeConsole = Object.fromEntries(['log', 'warn', 'error', 'info', 'debug'].map(key => [key, (...args) => logs.push([key, ...args])]));
  const context = vm.createContext({ window, document, URLSearchParams, TextDecoder, Uint8Array, console: safeConsole, Date: ClockDate, setTimeout, clearTimeout, FormData: FormDataDouble });
  const base = path.join(__dirname, '..');
  vm.runInContext(fs.readFileSync(path.join(base, 'i18n.js'), 'utf8'), context);
  vm.runInContext(fs.readFileSync(path.join(base, 'app.js'), 'utf8'), context);
  return { app: window.ConsoleApp, window, document, calls, fixture, listeners, stored, logs,
    advance(milliseconds) { now += milliseconds; for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); } } };
}
function mount(env, page, snapshot, params = {}) {
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap;
  env.app.state.route = { page, params }; env.app.state.cache.set(page + '?' + new URLSearchParams(params), snapshot);
  env.app.render();
}
const findButton = (env, text) => env.document.querySelectorAll('button').find(b => b.textContent === text);

test('all pages render real snapshots without array/null coercion or script injection', () => {
  const env = environment();
  const session = env.fixture.sessions.sessions[0];
  const cases = {
    overview: env.fixture.sessions, sessions: env.fixture.sessions, session: { session, queue: [], actions: ['submit', 'respond-permission', 'queue-clear'] },
    tasks: { tasks: [{ task_id: 't', status: 'running' }], actions: ['cancel'] }, task: { task: { task_id: 't', status: 'running', session_ref: { child_session_id: 's', target_id: 'r' } } },
    instances: { instances: [{ id: 'i', status: 'active' }], actions: ['spawn', 'followup'] }, instance: { instance: { id: 'i', status: 'active' } },
    schedules: { schedules: [{ id: 1, name: 'Daily', spec: '@daily', schedule_type: 'subtask', enabled: true }], actions: ['create', 'update'] }, schedule: { schedule: { id: 1, spec: '@daily', runs: [] } },
    bots: env.fixture.bots, groups: { groups: [{ profile: 'bot1', chat_id: 'g', name: 'Chat' }] }, profiles: env.fixture.profiles,
    profile: { profile: { ref: 'builtin/general', name: 'General' } }, packs: { packs: [{ namespace: 'team', versions: [] }], actions: ['install'] }, credentials: { profiles: [{ profile: 'p', kind: 'env', env_names: ['API_KEY'], file_names: [] }] },
    settings: { settings: [{ key: 'test.enabled', value: 'true', writable: true }], version: 'v', actions: ['save'] }
  };
  for (const [page, snapshot] of Object.entries(cases)) {
    mount(env, page, snapshot);
    assert.ok(env.document.getElementById('page').childElementCount > 0, page);
    assert.doesNotMatch(env.document.getElementById('page').textContent, /\[object (?:Object|Array)\]|null/);
    assert.equal(env.document.querySelectorAll('img,script').length, 0, page);
  }
  mount(env, 'sessions', env.fixture.sessions);
  assert.ok(env.document.getElementById('page').textContent.includes('<img src=x onerror=alert(1)>'));
  assert.equal(findButton(env, 'Runtimes'), undefined);
});

test('writes attach CSRF, keep exact wire values and never automatically retry', async () => {
  const env = environment(); mount(env, 'sessions', env.fixture.sessions);
  await env.app.write('sessions', 'submit', { name: 'one', target: 'r', text: '<script>x</script>' });
  const call = env.calls[0];
  assert.equal(call.options.headers['X-CSRF-Token'], 'csrf-test');
  assert.equal(call.options.credentials, 'same-origin');
  assert.equal(JSON.parse(call.options.body).data.text, '<script>x</script>');
  env.window.fetch = async () => { env.calls.push({ failed: true }); throw Error('down'); };
  await assert.rejects(() => env.app.write('sessions', 'archive', { name: 'one', target: 'r' }));
  assert.equal(env.calls.filter(c => c.failed).length, 1);
});

test('pairing fragment is removed before the first request', async () => {
  const env = environment('#pair=one-time-secret');
  await env.app.start();
  assert.deepEqual(env.calls[0], { history: '/' });
  assert.equal(env.window.location.hash, '');
  const pair = env.calls.find(c => c.url === '/auth/pair');
  assert.equal(JSON.parse(pair.options.body).code, 'one-time-secret');
  assert.ok(env.document.textContent.includes('Overview'));
});

test('bot form sends all six typed settings, including empty strings and false', async () => {
  const env = environment(); mount(env, 'bots', env.fixture.bots);
  env.app.openBotForm(env.fixture.bots.bots[0], true);
  const form = env.document.querySelector('#dialogs form');
  assert.equal(form.querySelectorAll('[name]').length, 8);
  await form.fire('submit');
  const payload = JSON.parse(env.calls.find(c => c.url === '/api/console/bots' && c.options.method === 'POST').options.body).data;
  assert.deepEqual(Object.keys(payload.settings).sort(), ['default_agent', 'default_runtime_ref', 'default_model_ref', 'default_reasoning_effort', 'interaction_mode', 'isolation_required'].sort());
  assert.equal(payload.settings.isolation_required, false);
  assert.equal(payload.settings.default_model_ref, '');
  assert.equal(payload.expected_version, 3);
});

test('schedule types generate distinct payloads without unrelated fields', async () => {
  const env = environment(); mount(env, 'schedules', { schedules: [], actions: ['create'] });
  await env.app.openScheduleForm();
  let form = env.document.querySelector('#dialogs form');
  const fill = (name, value) => { form.querySelector('[name=' + name + ']').value = value; };
  fill('spec', '@daily'); fill('task_agent', 'builtin/general'); fill('task_text', 'Inspect');
  await form.fire('submit');
  const payload = JSON.parse(env.calls.find(c => c.url === '/api/console/schedules' && c.options.method === 'POST').options.body).data;
  assert.deepEqual(Object.keys(payload).sort(), ['name', 'schedule_type', 'spec', 'task']);
  assert.equal(payload.task.agent, 'builtin/general'); assert.equal(payload.task.task, 'Inspect');
  await env.app.openScheduleForm(); form = env.document.querySelector('#dialogs form');
  fill('schedule_type', 'session-prompt'); await form.querySelector('[name=schedule_type]').fire('change');
  fill('spec', '@daily'); fill('target_id', 'r'); fill('session_name', 's'); fill('prompt', 'Hello');
  await form.fire('submit');
  const posts = env.calls.filter(c => c.url === '/api/console/schedules' && c.options.method === 'POST');
  const second = JSON.parse(posts[1].options.body).data;
  assert.equal(second.schedule_type, 'session-prompt'); assert.equal(second.prompt, 'Hello');
  assert.equal('task' in second, false);
});

test('settings save batches dirty keys as strings with CAS and reports restart', async () => {
  const env = environment();
  mount(env, 'settings', { settings: [{ key: 'a', value: 'false', writable: true }, { key: 'b', value: '1', writable: true }], version: 'sha', actions: ['save'] });
  for (const [key, value] of [['a', 'true'], ['b', '2']]) { const input = env.document.getElementById('setting-' + key); input.value = value; await input.fire('input'); }
  await findButton(env, 'Save changes').click();
  const payload = JSON.parse(env.calls.find(c => c.url === '/api/console/settings' && c.options.method === 'POST').options.body).data;
  assert.deepEqual(payload, { expected_version: 'sha', values: { a: 'true', b: '2' } });
});

test('401 clears all private cache and returns to pairing', async () => {
  const env = environment(); mount(env, 'sessions', env.fixture.sessions);
  env.window.fetch = async () => ({ ok: false, status: 401, json: async () => ({ error: 'authentication_required' }) });
  await assert.rejects(() => env.app.request('/api/console/sessions'));
  assert.equal(env.app.state.authenticated, false); assert.equal(env.app.state.cache.size, 0);
  assert.ok(env.document.textContent.includes('Connect to your amux system'));
  assert.equal(env.document.textContent.includes('onerror'), false);
});

test('overview does not present a known activity subset as complete totals', () => {
  const env = environment();
  mount(env, 'overview', {
    sessions: [
      { id: 'known', name: 'Observed work', state: 'running', activity_status: 'known', active_turn: true, turn_state: 'running', pending_permissions: 1 },
      { id: 'unknown', name: 'Unobserved work', state: 'running', activity_status: 'unknown', active_turn: false, turn_state: 'idle', pending_permissions: 0 }
    ], activity_coverage: { known: 1, unknown: 1 }
  });
  const values = env.document.querySelectorAll('.stat strong').map(node => node.textContent);
  assert.deepEqual(values, ['2', '—', '—', '0']);
  assert.ok(env.document.getElementById('page').textContent.includes('confirmed for 1 of 2 live sessions; the other 1 could not be read'));
  assert.ok(env.document.getElementById('page').textContent.includes('Observed work'));
});

test('archived unknown activity does not invalidate current active-session totals', () => {
  const env = environment();
  mount(env, 'overview', {
    sessions: [
      { id: 'active', name: 'Observed work', state: 'running', activity_status: 'known', active_turn: true, turn_state: 'running', pending_permissions: 1 },
      { id: 'archived', name: 'Archived', state: 'archived', activity_status: 'unknown', active_turn: false, turn_state: 'idle', pending_permissions: 0 }
    ], activity_coverage: { known: 1, unknown: 1 }
  });
  const values = env.document.querySelectorAll('.stat strong').map(node => node.textContent);
  // The waiting Turn counts once, under "needs your input", like the lists below.
  assert.deepEqual(values, ['1', '0', '1', '0']);
  assert.equal(env.document.getElementById('page').textContent.includes('others are unknown'), false);
});

test('sleeping sessions have no process: they are never unknown, working or waiting', () => {
  const env = environment();
  mount(env, 'overview', {
    sessions: [
      { id: 'live', name: 'Live', state: 'running', activity_status: 'known', active_turn: true, turn_state: 'running', pending_permissions: 0 },
      { id: 'nap-1', name: 'Nap one', state: 'sleeping', activity_status: 'unknown' },
      { id: 'nap-2', name: 'Nap two', state: 'sleeping', activity_status: 'unknown' },
      // Replay left open by a stop: stale facts must not count as work or a question.
      { id: 'nap-3', name: 'Nap three', state: 'sleeping', activity_status: 'known', active_turn: true, turn_state: 'waiting_permission', pending_permissions: 1 }
    ]
  });
  const page = env.document.getElementById('page');
  assert.deepEqual(env.document.querySelectorAll('.stat strong').map(node => node.textContent), ['1', '1', '0', '3']);
  assert.equal(page.textContent.includes('could not be read'), false);
  const panels = page.querySelectorAll('section');
  const panel = title => panels.find(p => p.querySelector('h2') && p.querySelector('h2').textContent === title).textContent;
  assert.equal(panel('Needs attention').includes('Nap three'), false);
  assert.ok(panel('Ongoing work').includes('Live'));
  // Process-alive Sessions beyond the inspected budget still make totals unknown.
  mount(env, 'overview', { sessions: [
    { id: 'seen', name: 'Seen', state: 'running', activity_status: 'known', active_turn: false, turn_state: 'idle' },
    { id: 'starting', name: 'Starting', state: 'created', activity_status: 'unknown' }
  ] });
  assert.deepEqual(env.document.querySelectorAll('.stat strong').map(node => node.textContent), ['2', '—', '—', '0']);
  assert.ok(env.document.getElementById('page').textContent.includes('confirmed for 1 of 2 live sessions'));
});

function passwordEnvironment(language = 'en-US', insecure = true) {
  const env = environment();
  env.window.navigator.language = language;
  env.fixture.config = { mode: 'password', expires_at: Math.floor(Date.now() / 1000) + 1800, insecure_transport: insecure };
  Object.assign(env.fixture.bootstrap, { auth_mode: 'password', expires_at: env.fixture.config.expires_at, insecure_transport: insecure });
  const baseFetch = env.window.fetch;
  let authenticated = false;
  env.window.fetch = async (url, options) => {
    if (url === '/auth/login') {
      const response = await baseFetch(url, options); authenticated = true;
      return { ...response, json: async () => ({ authenticated: true, csrf_token: 'csrf-test', expires_at: env.fixture.config.expires_at }) };
    }
    if (url === '/auth/logout') authenticated = false;
    if (url === '/api/console/bootstrap' && !authenticated) {
      env.calls.push({ url, options });
      return { ok: false, status: 401, json: async () => ({ error: 'authentication_required' }) };
    }
    return baseFetch(url, options);
  };
  return env;
}

async function submitPassword(env, secret = ' temporary-secret ') {
  const form = env.document.querySelector('.login-card form');
  form.querySelector('[name=password]').value = secret;
  await form.fire('submit');
}

test('password sign-in posts once to same origin and clears the field before networking', async () => {
  const env = passwordEnvironment();
  await env.app.start();
  assert.equal(env.calls[0].url, '/auth/config');
  assert.equal(env.calls[0].options.credentials, 'same-origin');
  assert.equal(env.calls[0].options.cache, 'no-store');
  const input = env.document.querySelector('[name=password]');
  assert.equal(input.type, 'password'); assert.equal(input.getAttribute('autocomplete'), 'off');
  assert.ok(env.document.textContent.includes('Management console temporary password'));
  assert.ok(env.document.textContent.includes('No SSH tunnel is required'));
  assert.equal(env.document.textContent.includes('The console is closed'), false);
  const secret = ' temporary-secret ';
  const baseFetch = env.window.fetch;
  let release;
  env.window.fetch = async (url, options) => {
    if (url === '/auth/login') {
      assert.equal(input.value, '');
      assert.equal(env.document.textContent.includes(secret), false);
      await new Promise(resolve => { release = resolve; });
    }
    return baseFetch(url, options);
  };
  const pending = submitPassword(env, secret);
  assert.equal(input.value, '');
  assert.equal(env.document.querySelector('[name=password]').value, '');
  release(); await pending;
  const login = env.calls.filter(c => c.url === '/auth/login');
  assert.equal(login.length, 1);
  assert.deepEqual(JSON.parse(login[0].options.body), { password: secret });
  assert.equal(login[0].options.credentials, 'same-origin');
  assert.equal(login[0].options.headers['Content-Type'], 'application/json');
  assert.equal(env.app.state.authenticated, true);
  assert.equal(JSON.stringify(env.app.state).includes(secret), false);
  assert.equal(JSON.stringify(env.stored).includes(secret), false);
  assert.equal(JSON.stringify(env.logs).includes(secret), false);
  assert.equal(JSON.stringify(env.calls.filter(c => c.history)).includes(secret), false);
  assert.equal(JSON.stringify(env.window.location).includes(secret), false);
  const header = env.document.querySelector('.app-top');
  assert.ok(header.textContent.includes('Closes in 30 min'));
  assert.ok(header.textContent.includes('Unencrypted HTTP'));
  assert.ok(header.querySelector('.chip.warn').getAttribute('title').includes('transmitted in plaintext'));
});

test('auth-config failure or malformed metadata fails closed before business requests', async () => {
  const cases = [null, {}, { mode: 'unknown', expires_at: 0, insecure_transport: false }, { mode: 'password', expires_at: 0, insecure_transport: false }, { mode: 'pair', expires_at: 0 }];
  for (const value of cases) {
    const env = environment('#pair=discard-on-config-failure');
    env.window.fetch = async (url, options) => { env.calls.push({ url, options }); if (value === null) throw Error('unavailable'); return { ok: true, status: 200, json: async () => value }; };
    await env.app.start();
    assert.deepEqual(env.calls[0], { history: '/' });
    assert.deepEqual(env.calls.filter(c => c.url).map(c => c.url), ['/auth/config']);
    assert.equal(env.document.querySelector('.login-card form'), null);
    assert.equal(env.app.state.authenticated, false);
    assert.ok(env.document.textContent.includes('No system data was loaded'));
    assert.equal(env.document.textContent.includes('discard-on-config-failure'), false);
    env.window.fetch = async (url, options) => { env.calls.push({ url, options }); return { ok: true, status: 200, json: async () => url === '/auth/config' ? env.fixture.config : env.fixture.bootstrap }; };
    await findButton(env, 'Try again').click();
    assert.equal(env.app.state.authenticated, true);
    assert.equal(env.calls.some(c => c.url === '/auth/pair'), false);
  }
});

test('password authentication failures are localized, clear secrets and do not recursively expire', async () => {
  for (const language of ['en-US', 'zh-CN']) {
    for (const [code, status] of [['invalid_password', 401], ['authentication_rate_limited', 429]]) {
      const env = passwordEnvironment(language);
      await env.app.start();
      const generation = env.app.state.generation;
      const baseFetch = env.window.fetch;
      env.window.fetch = async (url, options) => {
        if (url !== '/auth/login') return baseFetch(url, options);
        env.calls.push({ url, options });
        return { ok: false, status, json: async () => ({ code, error: 'do-not-render-this-secret' }) };
      };
      const input = env.document.querySelector('[name=password]');
      await submitPassword(env, 'do-not-render-this-secret');
      assert.equal(input.value, '');
      assert.equal(env.document.querySelector('[name=password]').value, '');
      assert.equal(env.app.state.generation, generation);
      assert.equal(env.calls.filter(c => c.url === '/auth/login').length, 1);
      assert.ok(env.document.textContent.includes(env.window.ConsoleI18n.t(code, language)));
      assert.equal(env.document.textContent.includes('do-not-render-this-secret'), false);
      assert.equal(env.app.state.authenticated, false);
    }
  }
});

test('password login, deadline and transport notices render in both languages with English fallback', async () => {
  for (const language of ['en-US', 'zh-CN', 'fr-FR']) {
    const env = passwordEnvironment(language);
    await env.app.start();
    for (const key of ['temporaryPassword', 'temporaryPasswordHelp', 'insecureTransportWarning']) assert.ok(env.document.textContent.includes(env.window.ConsoleI18n.t(key, language)), key);
    const before = env.document.querySelector('.login-card .banner').textContent;
    assert.ok(before.includes(language === 'zh-CN' ? '到期自动关闭' : 'Automatically closes at'));
    await submitPassword(env);
    const header = env.document.querySelector('.app-top');
    assert.ok(header.textContent.includes(env.window.ConsoleI18n.t('closesIn', language, { minutes: 30 })));
    assert.ok(header.textContent.includes(env.window.ConsoleI18n.t('insecureShort', language)));
    assert.equal(env.document.getElementById('session-clock').getAttribute('title'), before);
  }
  const secure = passwordEnvironment('en-US', false);
  await secure.app.start();
  assert.equal(secure.document.textContent.includes('transmitted in plaintext'), false);
  await submitPassword(secure);
  assert.equal(secure.document.textContent.includes('Unencrypted HTTP'), false);
});

test('sign-out retains password mode and allows the same temporary password until expiry', async () => {
  const env = passwordEnvironment();
  await env.app.start(); await submitPassword(env, 'same-temporary-secret');
  const pending = env.document.querySelectorAll('button').find(button => button.getAttribute('aria-label') === 'Sign out').click();
  await findButton(env, 'Confirm').click(); await pending;
  assert.equal(env.app.state.authenticated, false);
  assert.ok(env.document.querySelector('[name=password]'));
  assert.equal(env.document.querySelector('[name=code]'), null);
  await submitPassword(env, 'same-temporary-secret');
  assert.equal(env.app.state.authenticated, true);
  assert.equal(env.calls.filter(c => c.url === '/auth/config').length, 1);
  assert.equal(env.calls.filter(c => c.url === '/auth/login').length, 2);
  assert.equal(env.calls.filter(c => c.url === '/auth/logout').length, 1);
});

test('deadline closes an authenticated page, clears private state and blocks writes without replay', async () => {
  for (const language of ['en-US', 'zh-CN']) {
    const env = passwordEnvironment(language);
    await env.app.start(); await submitPassword(env);
    env.app.state.drafts.set('draft', { text: 'private-content' });
    const stream = new env.window.EventSource('/api/console/events'); env.app.state.streams = stream;
    const callsBefore = env.calls.length;
    env.advance(1801000);
    assert.equal(env.app.state.authenticated, false); assert.equal(env.app.state.cache.size, 0); assert.equal(env.app.state.drafts.size, 0);
    assert.equal(env.app.state.bootstrap, null); assert.equal(stream.closed, true);
    assert.equal(env.document.querySelector('#page'), null);
    assert.equal(env.document.querySelector('.login-card form'), null);
    assert.ok(env.document.textContent.includes(env.window.ConsoleI18n.t('console_expired', language)));
    await assert.rejects(() => env.app.write('sessions', 'archive', { name: 'one' }), error => error.status === 401);
    for (const fn of env.listeners.online || []) await fn();
    assert.equal(env.calls.length, callsBefore);
  }
});

test('expired auth config never bootstraps and an expired login response closes the form', async () => {
  const env = passwordEnvironment(); env.fixture.config.expires_at = 1;
  await env.app.start();
  assert.deepEqual(env.calls.filter(c => c.url).map(c => c.url), ['/auth/config']);
  assert.ok(env.document.textContent.includes('The console has closed'));
  const login = passwordEnvironment(); await login.app.start();
  login.window.fetch = async () => ({ ok: false, status: 401, json: async () => ({ code: 'console_expired' }) });
  await submitPassword(login);
  assert.equal(login.app.state.consoleExpired, true);
  assert.equal(login.document.querySelector('.login-card form'), null);
  assert.ok(login.document.textContent.includes('The console has closed'));
});

test('business 401 clears password sessions and tells the owner how to reopen without replay', async () => {
  const env = passwordEnvironment(); await env.app.start(); await submitPassword(env);
  env.calls.length = 0;
  env.window.fetch = async (url, options) => { env.calls.push({ url, options }); return { ok: false, status: 401, json: async () => ({ code: 'authentication_required' }) }; };
  await assert.rejects(() => env.app.write('sessions', 'archive', { name: 'one' }));
  assert.equal(env.calls.length, 1);
  assert.equal(env.app.state.authenticated, false); assert.equal(env.app.state.cache.size, 0);
  assert.ok(env.document.textContent.includes('The console is closed or your authentication expired'));
  assert.ok(env.document.textContent.includes('/amux'));
  assert.ok(env.document.textContent.includes('Writes are never replayed automatically'));
});

test('auth-config HTTP errors never fall through to login, pairing or system data', async () => {
  for (const status of [401, 403, 500]) {
    const env = environment();
    env.window.fetch = async (url, options) => { env.calls.push({ url, options }); return { ok: false, status, json: async () => ({ error: 'raw-diagnostic-must-not-render' }) }; };
    await env.app.start();
    assert.deepEqual(env.calls.map(c => c.url), ['/auth/config']);
    assert.equal(env.document.querySelector('.login-card form'), null);
    assert.ok(env.document.textContent.includes('Unable to verify console sign-in settings'));
    assert.equal(env.document.textContent.includes('raw-diagnostic-must-not-render'), false);
  }
  const closed = environment();
  closed.window.fetch = async (url, options) => { closed.calls.push({ url, options }); return { ok: false, status: 401, json: async () => ({ error: 'console_expired' }) }; };
  await closed.app.start();
  assert.deepEqual(closed.calls.map(c => c.url), ['/auth/config']);
  assert.ok(closed.document.textContent.includes('The console has closed'));
});

test('bootstrap metadata refreshes auth notices and can shorten but not extend the opening window', async () => {
  const env = passwordEnvironment('en-US', false);
  env.fixture.bootstrap.expires_at -= 1200; env.fixture.bootstrap.insecure_transport = true;
  await env.app.start(); await submitPassword(env);
  assert.equal(env.app.state.authConfig.expires_at, env.fixture.bootstrap.expires_at);
  assert.equal(env.app.state.authConfig.mode, 'password');
  assert.ok(env.document.textContent.includes('Unencrypted HTTP'));
  assert.ok(env.document.textContent.includes('Closes in 10 min'));
  env.advance(601000);
  assert.equal(env.app.state.authenticated, false);
  const extended = passwordEnvironment();
  extended.fixture.bootstrap.expires_at += 600;
  await extended.app.start(); await submitPassword(extended);
  assert.equal(extended.app.state.authConfig.expires_at, extended.fixture.config.expires_at);
  extended.advance(1801000);
  assert.equal(extended.app.state.authenticated, false);
});

const buttonTexts = env => env.document.getElementById('page').querySelectorAll('button').map(b => b.textContent);

test('session lists default to unarchived, newest first, and paginate large fleets', async () => {
  const env = environment();
  const sessions = Array.from({ length: 120 }, (_, i) => ({ id: 'id-' + i, name: 'session-' + i, state: i % 3 === 0 ? 'archived' : 'sleeping', target_id: 'r', last_activity_at: 1794500000 + i }));
  mount(env, 'sessions', { sessions, actions: [] });
  const page = env.document.getElementById('page');
  const names = () => page.querySelectorAll('tbody tr').map(row => row.querySelector('.link-button').textContent);
  assert.equal(names().length, 50);
  assert.equal(names()[0], 'session-119');
  assert.ok(names().every(name => Number(name.split('-')[1]) % 3 !== 0));
  assert.ok(page.textContent.includes('80 records') && page.textContent.includes('Page 1 of 2'));
  await page.querySelectorAll('button').find(b => b.getAttribute('aria-label') === 'Next page').click();
  assert.equal(names().length, 30);
  assert.ok(env.document.getElementById('page').textContent.includes('Page 2 of 2'));
  await env.document.getElementById('page').querySelectorAll('.tab').find(tab => tab.textContent.startsWith('Archived')).click();
  assert.equal(names().length, 40);
  assert.ok(env.document.getElementById('page').textContent.includes('Page 1 of 1') === false);
});

test('session controls follow lifecycle and the observed Turn', () => {
  const env = environment();
  const actions = ['submit', 'interrupt', 'restart', 'archive', 'sleep', 'resume'];
  const show = (session, extra = {}) => { env.app.state.timeline.clear(); mount(env, 'session', Object.assign({ session, queue: [], actions }, extra), { name: session.name, target: 'r' }); return buttonTexts(env); };
  let texts = show({ id: 's1', name: 'sleepy', state: 'sleeping', target_id: 'r' });
  assert.ok(texts.includes('Resume session') && texts.includes('Archive session'));
  assert.equal(texts.includes('Interrupt turn') || texts.includes('Sleep session'), false);
  assert.ok(env.document.querySelector('.composer'));
  texts = show({ id: 's2', name: 'done', state: 'archived', target_id: 'r' });
  assert.equal(['Resume session', 'Archive session', 'Restart session', 'Interrupt turn'].some(text => texts.includes(text)), false);
  assert.equal(env.document.querySelector('.composer'), null);
  texts = show({ id: 's3', name: 'idle', state: 'running', target_id: 'r' }, { activity: { active_turn: false, turn_state: 'succeeded' } });
  assert.ok(texts.includes('Sleep session') && texts.includes('Restart session'));
  assert.equal(texts.includes('Interrupt turn'), false);
  assert.ok(env.document.getElementById('turn-state').textContent.includes('Succeeded'));
  texts = show({ id: 's4', name: 'busy', state: 'running', target_id: 'r' }, { activity: { active_turn: true, turn_state: 'running' } });
  assert.ok(texts.includes('Interrupt turn'));
});

test('terminal task runs never offer cancellation and tasks sort by request time', () => {
  const env = environment();
  mount(env, 'tasks', { tasks: [
    { task_id: 'task-old', status: 'running', task: 'Old but running', requested_at: '2026-11-01T00:00:00Z', started_at: '2026-11-01T00:00:00Z' },
    { task_id: 'task-new', status: 'succeeded', task: 'Newest done\nsecond line', requested_at: '2026-11-13T00:00:00Z', started_at: '2026-11-13T00:00:00Z', finished_at: '2026-11-13T00:02:05Z' }
  ], actions: ['cancel'] });
  const rows = env.document.getElementById('page').querySelectorAll('tbody tr');
  assert.equal(rows[0].querySelector('.link-button').textContent, 'Newest done');
  assert.ok(rows[0].textContent.includes('2m 5s'));
  assert.equal(rows[0].querySelectorAll('button').some(b => b.textContent === 'Cancel task'), false);
  assert.ok(rows[1].querySelectorAll('button').some(b => b.textContent === 'Cancel task'));
});

test('overview separates attention from ongoing work and shows tasks and bots', () => {
  const env = environment();
  mount(env, 'overview', {
    sessions: [
      { id: 'a', name: 'Needs answer', state: 'running', activity_status: 'known', active_turn: true, turn_state: 'waiting_permission', pending_permissions: 1 },
      { id: 'b', name: 'Busy work', state: 'running', activity_status: 'known', active_turn: true, turn_state: 'running', pending_permissions: 0 }
    ],
    recent_tasks: [{ task_id: 'task-1', status: 'failed', task: 'Rebuild index', requested_at: '2026-11-13T00:00:00Z' }],
    task_counts: { active: 2, succeeded_24h: 5, failed_24h: 1 },
    bots: [{ name: 'Helper', status: 'running', settings: { default_agent: 'builtin/general@0123456789abcdef0123' } }]
  });
  const panels = env.document.getElementById('page').querySelectorAll('section');
  const panel = title => panels.find(p => p.querySelector('h2') && p.querySelector('h2').textContent === title).textContent;
  assert.ok(panel('Needs attention').includes('Needs answer'));
  assert.equal(panel('Ongoing work').includes('Needs answer'), false);
  assert.ok(panel('Ongoing work').includes('Busy work'));
  assert.ok(panel('Recent task runs').includes('Rebuild index') && panel('Recent task runs').includes('1 failed in 24h') && panel('Recent task runs').includes('5 succeeded in 24h'));
  // One bucket per Session: the totals equal the rows listed under each heading.
  assert.deepEqual(env.document.querySelectorAll('.stat strong').map(node => node.textContent), ['2', '1', '1', '0']);
  assert.ok(panel('Bot connections').includes('Helper') && panel('Bot connections').includes('builtin/general@0123456'));
});

test('settings use typed controls and still save changed values as strings', async () => {
  const env = environment();
  mount(env, 'settings', { settings: [
    { key: 'bridge.feishu.group_settings.enabled', value: 'false', writable: true, inherited: true, restart_required: true },
    { key: 'maintenance.daily_cleanup.at', value: '06:17', writable: true, restart_required: true }
  ], version: 'v1', actions: ['save'] });
  // The DOM double's selectors treat dots as classes; look controls up by name.
  const control = key => env.document.querySelectorAll('select,input').find(node => node.name === key);
  const toggle = control('bridge.feishu.group_settings.enabled');
  assert.equal(toggle.tagName, 'SELECT');
  assert.equal(control('maintenance.daily_cleanup.at').type, 'time');
  const page = env.document.getElementById('page').textContent;
  assert.ok(page.includes('Chat-specific settings') && page.includes('Default') && page.includes('take effect after the system restarts'));
  toggle.value = 'true'; await toggle.fire('change');
  await findButton(env, 'Save changes').click();
  const payload = JSON.parse(env.calls.find(c => c.url === '/api/console/settings' && c.options.method === 'POST').options.body).data;
  assert.deepEqual(payload, { expected_version: 'v1', values: { 'bridge.feishu.group_settings.enabled': 'true' } });
});

test('local pairing deadline points to the CLI instead of the owner card', async () => {
  for (const language of ['en-US', 'zh-CN']) {
    const env = environment();
    env.window.navigator.language = language;
    env.fixture.config = { mode: 'pair', expires_at: Math.floor(Date.now() / 1000) + 1800, insecure_transport: false };
    await env.app.start();
    assert.equal(env.app.state.authenticated, true);
    const deadline = env.document.querySelector('#session-clock').getAttribute('title');
    assert.ok(deadline.includes('amux console'));
    assert.equal(deadline.includes('/amux'), false);
  }
});

test('profiles without a namespace get their own filter instead of aliasing All', async () => {
  const env = environment();
  mount(env, 'profiles', { profiles: [{ ref: 'team/reviewer', name: 'Reviewer' }, { name: 'orphan' }, { ref: 'builtin/general', name: 'General' }] });
  const page = () => env.document.getElementById('page');
  const cards = () => page().querySelectorAll('.profile-card').length;
  assert.equal(cards(), 3);
  const tab = label => page().querySelectorAll('.tab').find(item => item.textContent.startsWith(label));
  await tab('Unknown').click();
  assert.equal(cards(), 1);
  assert.ok(page().textContent.includes('orphan'));
  assert.equal(tab('All').getAttribute('aria-pressed') === 'true', false);
  await tab('team').click();
  assert.equal(cards(), 1);
  await tab('All').click();
  assert.equal(cards(), 3);
});

test('answering a permission keeps the strong confirmation warning', async () => {
  const env = environment();
  const permission = { id: 'perm-1', turn_id: 't1', question: 'Run migration?', tool: { title: 'Bash' }, options: [{ id: 'allow', name: 'Allow once', kind: 'allow_once' }, { id: 'deny', name: 'Deny', kind: 'reject_once' }] };
  mount(env, 'session', { session: { id: 's1', name: 'chat', state: 'running', target_id: 'r' }, queue: [], permissions: [permission], actions: ['submit', 'respond-permission'] }, { name: 'chat', target: 'r' });
  const callsBefore = env.calls.length;
  const pending = findButton(env, 'Allow once').click();
  const dialog = env.document.getElementById('dialogs').textContent;
  assert.ok(dialog.includes('This may affect ongoing work'), dialog);
  await env.document.getElementById('dialogs').querySelectorAll('button').find(b => b.textContent === 'Cancel').click(); await pending;
  assert.equal(env.calls.length, callsBefore);
});

test('a closed stream re-reads the Session so lifecycle controls follow the Hub', async () => {
  const env = environment('#session?name=chat&target=r');
  const streams = []; const Base = env.window.EventSource;
  env.window.EventSource = class extends Base { constructor(url) { super(url); streams.push(this); } };
  const actions = ['submit', 'interrupt', 'restart', 'archive', 'sleep', 'resume'];
  const snapshot = state => ({ session: { id: 's1', name: 'chat', state, target_id: 'r' }, queue: [], actions, activity: { active_turn: false, turn_state: 'succeeded' } });
  env.fixture.session = snapshot('running');
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap; env.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
  await env.app.loadRoute();
  assert.equal(streams.length, 1);
  assert.ok(buttonTexts(env).includes('Sleep session') && !buttonTexts(env).includes('Resume session'));
  const sessionReads = () => env.calls.filter(call => /\/session\?/.test(call.url)).length;
  const before = sessionReads();
  env.fixture.session = snapshot('sleeping');   // slept elsewhere (idle reclaim, another tab)
  await streams[0].listeners.disconnected();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(sessionReads(), before + 1);
  assert.ok(buttonTexts(env).includes('Resume session') && !buttonTexts(env).includes('Sleep session'));
  assert.ok(env.document.getElementById('stream-status').textContent.includes('Reconnecting'));
  // Later stream events redraw the controls from the refreshed state, not the load-time one.
  await streams[0].onmessage({ data: JSON.stringify({ cursor: 'c-1', kind: 'OUTPUT', text: 'late output' }) });
  await new Promise(resolve => setTimeout(resolve, 150));
  assert.ok(buttonTexts(env).includes('Resume session') && !buttonTexts(env).includes('Sleep session'));
  // Repeated stream errors do not hammer the Hub.
  await streams[0].onerror(); await streams[0].onerror();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(sessionReads(), before + 1);
  // A Session that is no longer open on this page is never re-read.
  env.app.state.streams = null;
  env.advance(60000); await streams[0].onerror(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(sessionReads(), before + 1);
});

test('leaving a Session page clears its stream-only reconnecting indicator', async () => {
  const env = environment('#session?name=chat&target=r');
  const streams = []; const Base = env.window.EventSource;
  env.window.EventSource = class extends Base { constructor(url) { super(url); streams.push(this); } };
  env.fixture.session = { session: { id: 's1', name: 'chat', state: 'running', target_id: 'r' }, queue: [], actions: ['submit'], activity: { active_turn: false, turn_state: 'idle' } };
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap; env.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
  await env.app.loadRoute();
  await streams[0].onerror();
  assert.equal(env.app.state.connection, 'reconnecting');
  assert.ok(env.document.getElementById('connection').textContent.includes('Reconnecting'));
  env.app.state.route = { page: 'sessions', params: {} };
  await env.app.loadRoute();
  assert.equal(streams[0].closed, true);
  assert.equal(env.app.state.connection, 'connected');
  assert.equal(env.document.getElementById('connection').textContent.includes('Reconnecting'), false);
});

test('a stopped Session settles into history instead of reconnecting forever', async () => {
  const setup = state => {
    const env = environment('#session?name=chat&target=r');
    const streams = []; const Base = env.window.EventSource;
    env.window.EventSource = class extends Base { constructor(url) { super(url); streams.push(this); } };
    const actions = ['submit', 'interrupt', 'restart', 'archive', 'sleep', 'resume'];
    const snapshot = value => ({ session: { id: 's1', name: 'chat', state: value, target_id: 'r' }, queue: [], actions, activity: { active_turn: false, turn_state: 'succeeded' } });
    env.fixture.session = snapshot(state);
    env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap; env.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
    return { env, streams, snapshot, caughtUp: stream => stream.onmessage({ data: JSON.stringify({ cursor: 'c-0', caught_up: true }) }) };
  };
  const reads = env => env.calls.filter(call => /\/session\?/.test(call.url)).length;
  const settle = () => new Promise(resolve => setImmediate(resolve));

  // A sleeping Session ends its stream once the history is delivered: stop retrying.
  let { env, streams, snapshot, caughtUp } = setup('sleeping');
  await env.app.loadRoute();
  await caughtUp(streams[0]);
  await streams[0].listeners.disconnected(); await streams[0].onerror();
  assert.equal(streams[0].closed, true);
  assert.ok(env.document.getElementById('stream-status').textContent.includes('History only'));
  assert.equal(env.app.state.connection, 'connected');
  assert.equal(reads(env), 1);

  // An interrupted replay is not complete history: keep the browser's retry.
  ({ env, streams } = setup('sleeping'));
  await env.app.loadRoute();
  await streams[0].listeners.disconnected();
  assert.notEqual(streams[0].closed, true);
  assert.ok(env.document.getElementById('stream-status').textContent.includes('Reconnecting'));

  // A running Session that goes to sleep elsewhere settles as soon as that is observed.
  ({ env, streams, snapshot, caughtUp } = setup('running'));
  await env.app.loadRoute();
  await caughtUp(streams[0]);
  env.fixture.session = snapshot('sleeping');
  await streams[0].listeners.disconnected(); await settle();
  assert.equal(streams[0].closed, true);
  assert.ok(env.document.getElementById('stream-status').textContent.includes('History only'));
  assert.ok(buttonTexts(env).includes('Resume session') && !buttonTexts(env).includes('Sleep session'));
  assert.equal(env.app.state.connection, 'connected');
});

test('an open Session page polls the lifecycle and follows changes made elsewhere', async () => {
  const env = environment('#session?name=chat&target=r');
  const actions = ['submit', 'interrupt', 'restart', 'archive', 'sleep', 'resume'];
  const snapshot = state => ({ session: { id: 's1', name: 'chat', state, target_id: 'r' }, queue: [], actions, activity: { active_turn: false, turn_state: 'succeeded' } });
  env.fixture.session = snapshot('running');
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap; env.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
  await env.app.loadRoute();
  const reads = () => env.calls.filter(call => /\/session\?/.test(call.url));
  const tick = async ms => { env.advance(ms); await new Promise(resolve => setImmediate(resolve)); };
  assert.equal(reads().length, 1);
  assert.ok(buttonTexts(env).includes('Sleep session'));

  // Nothing changed: the poll is a light read and leaves the page alone.
  const composer = env.document.querySelector('.composer');
  await tick(20000);
  assert.equal(reads().length, 2);
  assert.ok(reads()[1].url.includes('activity=0'), reads()[1].url);
  assert.equal(env.document.querySelector('.composer'), composer);

  // A hidden tab does not poll.
  env.document.hidden = true; await tick(20000);
  assert.equal(reads().length, 2);
  env.document.hidden = false;

  // Slept elsewhere: the next poll updates the controls and drops stale Turn facts.
  env.fixture.session = snapshot('sleeping');
  await tick(20000);
  assert.equal(reads().length, 3);
  assert.ok(buttonTexts(env).includes('Resume session') && !buttonTexts(env).includes('Sleep session'));
  assert.equal(env.app.state.cache.get('session?name=chat&target=r').activity, undefined);

  // Leaving the page ends the polling.
  env.app.state.route = { page: 'sessions', params: {} };
  await env.app.loadRoute();
  const before = env.calls.length;
  await tick(60000);
  assert.equal(env.calls.length, before);
});

// ---------------------------------------------------------------------------
// Expert library: packs, removal and what refers to an expert.
// ---------------------------------------------------------------------------
const tick = () => new Promise(resolve => setImmediate(resolve));
const dialogs = env => env.document.getElementById('dialogs');
const dialogButton = (env, text) => dialogs(env).querySelectorAll('button').find(b => b.textContent === text);
const lastWrite = env => { const call = [...env.calls].reverse().find(c => c.options && c.options.method === 'POST'); return call && JSON.parse(call.options.body); };
const usageFixture = (extra = {}) => Object.assign({
  namespace: 'team', counts: { sessions: 3, bots: 1, schedules: 2, instances: 0 }, unavailable: [],
  refs: { 'team/a': { sessions: 2, bots: 1, schedules: 1, instances: 0 }, 'team/b': { sessions: 1, bots: 0, schedules: 1, instances: 0 } },
  items: { sessions: [{ id: 's1', name: 'review-1', state: 'running', target: 'r' }], bots: [{ id: 'default', name: 'Helper', state: 'running' }], schedules: [{ id: '1', name: 'nightly', state: 'enabled' }, { id: '2', name: 'weekly', state: 'disabled' }], instances: [] }
}, extra);
async function openRemove(env, usage, pack = { namespace: 'team', source_kind: 'git', source_host: 'example.com', profile_count: 2 }, label = 'Remove pack') {
  env.fixture.usage = usage;
  mount(env, 'packs', { packs: [pack], actions: ['install', 'sync', 'rollback', 'remove'] });
  await env.document.getElementById('page').querySelectorAll('button').find(b => b.textContent === label).click();
  await tick(); await tick();
  return { submit: dialogButton(env, label), force: dialogs(env).querySelector('#remove-force'), keep: dialogs(env).querySelector('#remove-keep-files') };
}
const unusedUsage = () => usageFixture({ counts: { sessions: 3, bots: 0, schedules: 0, instances: 0 }, refs: { 'team/a': { sessions: 2, bots: 0, schedules: 0, instances: 0 }, 'team/b': { sessions: 1, bots: 0, schedules: 0, instances: 0 } }, items: { sessions: [{ id: 's1', name: 'review-1', state: 'running', target: 'r' }], bots: [], schedules: [], instances: [] } });

test('packs show how many experts each provides and offer removal only where the console may', async () => {
  const env = environment();
  mount(env, 'packs', { packs: [
    { namespace: 'team', source_kind: 'git', source_host: 'example.com', current_revision: 'abc', profile_count: 3, versions: [] },
    { namespace: 'owned', source_kind: 'git', managed_by: 'amux', profile_count: 1 },
    { namespace: 'scratch', source_kind: 'git', temporary: true },
    { namespace: 'nocount', source_kind: 'git' }
  ], actions: ['install', 'sync', 'rollback', 'remove'] });
  const rows = env.document.getElementById('page').querySelectorAll('tbody tr');
  const row = ns => rows.find(r => r.querySelector('.link-button, td') && r.textContent.includes(ns));
  const labels = r => r.querySelectorAll('button').map(b => b.textContent);
  assert.ok(labels(row('team')).includes('Remove pack'));
  for (const ns of ['owned', 'scratch']) assert.equal(labels(row(ns)).includes('Remove pack'), false, ns);
  assert.ok(labels(row('team')).includes('3'));
  assert.equal(labels(row('nocount')).some(text => /^\d+$/.test(text)), false);
  // The count leads to that pack's experts.
  await row('team').querySelectorAll('button').find(b => b.textContent === '3').click();
  assert.equal(env.window.location.hash, '#profiles');
  assert.equal(env.app.state.filters.get('profiles').namespace, 'team');
  // Without the capability nothing offers removal.
  mount(env, 'packs', { packs: [{ namespace: 'team', source_kind: 'git' }], actions: ['install', 'sync'] });
  assert.equal(buttonTexts(env).includes('Remove pack'), false);
});

test('removing an unused pack asks once and sends exactly what was chosen', async () => {
  const env = environment();
  const { submit, force } = await openRemove(env, unusedUsage());
  const text = dialogs(env).textContent;
  assert.ok(text.includes('Remove “team” from this host') && text.includes('team/a') && text.includes('Used by 2') && text.includes('Used by 1'), text);
  assert.ok(text.includes('restart or resume may need pack files'));
  assert.equal(text.includes('These will fail to start new work'), false);
  assert.notEqual(force.parentNode.hidden, false, 'no "remove anyway" for an unused pack');
  assert.equal(submit.disabled, false);
  env.fixture.packs = { packs: [], actions: ['remove'] };
  await submit.click(); await tick(); await tick();
  assert.deepEqual(lastWrite(env), { action: 'remove', data: { namespace: 'team', confirm: true } });
  assert.equal(dialogs(env).querySelector('.modal'), null, 'the dialog closes after removal');

  // Keeping the files is a separate, explicit choice.
  const again = await openRemove(env, usageFixture({ counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, refs: {}, items: { sessions: [], bots: [], schedules: [], instances: [] } }));
  again.keep.checked = true;
  await again.submit.click(); await tick();
  assert.deepEqual(lastWrite(env), { action: 'remove', data: { namespace: 'team', confirm: true, keep_files: true } });
});

test('a pack that Bots, Schedules or instances use needs an explicit "remove anyway"', async () => {
  const env = environment();
  const { submit, force } = await openRemove(env, usageFixture());
  const text = dialogs(env).textContent;
  assert.ok(text.includes('These will fail to start new work once the pack is removed.'), text);
  assert.ok(text.includes('Bot default experts') && text.includes('Helper') && text.includes('nightly') && text.includes('weekly'));
  assert.equal(force.parentNode.hidden, false);
  assert.equal(submit.disabled, true);
  const callsBefore = env.calls.length;
  await submit.click();
  assert.equal(env.calls.length, callsBefore, 'a blocked removal sent nothing');
  force.checked = true; await force.fire('change');
  assert.equal(submit.disabled, false);
  env.fixture.packs = { packs: [], actions: ['remove'] };
  await submit.click(); await tick(); await tick();
  assert.deepEqual(lastWrite(env), { action: 'remove', data: { namespace: 'team', confirm: true, force: true } });
  // Cancel never removes.
  const second = environment();
  await openRemove(second, usageFixture());
  const writes = second.calls.length;
  await dialogButton(second, 'Cancel').click();
  assert.equal(second.calls.length, writes);
  assert.equal(dialogs(second).querySelector('.modal'), null);
});

test('use that cannot be read blocks removal, including Bot references', async () => {
  let env = environment();
  let r = await openRemove(env, usageFixture({ counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, refs: {}, items: { sessions: [], bots: [], schedules: [], instances: [] }, unavailable: ['schedules'] }));
  assert.ok(dialogs(env).textContent.includes('Some usage could not be read (Schedules)'));
  assert.equal(r.force.parentNode.hidden, false); assert.equal(r.submit.disabled, true);

  env = environment();
  r = await openRemove(env, usageFixture({ counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, refs: {}, items: { sessions: [], bots: [], schedules: [], instances: [] }, unavailable: ['bots', 'groups'] }));
  assert.equal(r.force.parentNode.hidden, false);
  assert.equal(r.submit.disabled, true);

  // When the usage request itself fails the dialog stays safe and says why.
  env = environment();
  const baseFetch = env.window.fetch;
  env.window.fetch = async (url, options) => url.includes('/api/console/usage') ? { ok: false, status: 503, json: async () => ({ error: 'backend_unavailable' }) } : baseFetch(url, options);
  mount(env, 'packs', { packs: [{ namespace: 'team', source_kind: 'git' }], actions: ['remove'] });
  await env.document.getElementById('page').querySelectorAll('button').find(b => b.textContent === 'Remove pack').click();
  await tick(); await tick();
  assert.equal(dialogs(env).querySelector('#remove-force').parentNode.hidden, false);
  assert.equal(dialogButton(env, 'Remove pack').disabled, true);
  assert.ok(dialogs(env).querySelector('.banner.bad'));
});

test('a removal the backend refuses explains itself and then offers the explicit choice', async () => {
  const env = environment();
  const { submit, force } = await openRemove(env, usageFixture({ counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, refs: {}, items: { sessions: [], bots: [], schedules: [], instances: [] } }));
  assert.notEqual(force.parentNode.hidden, false);
  const baseFetch = env.window.fetch;
  env.window.fetch = async (url, options) => options && options.method === 'POST' ? { ok: false, status: 409, json: async () => ({ code: 'pack_in_use', error: 'pack_in_use' }) } : baseFetch(url, options);
  await submit.click(); await tick(); await tick();
  assert.ok(dialogs(env).textContent.includes('still referenced by a Bot, group, Schedule or persistent instance'), dialogs(env).textContent);
  assert.equal(force.parentNode.hidden, false);
  assert.equal(submit.disabled, true);
});

test('pack revisions can be rolled back one by one and managed packs cannot', async () => {
  const env = environment();
  const pack = { namespace: 'team', source_kind: 'git', current_revision: 'bbb222', versions: [{ revision: 'aaa111', synced_at: 1794000000 }, { revision: 'bbb222', synced_at: 1794100000 }] };
  mount(env, 'packs', { packs: [pack], actions: ['rollback', 'remove'] });
  await findButton(env, 'Details').click();
  assert.deepEqual(dialogs(env).querySelectorAll('button').map(b => b.textContent).filter(text => text === 'Roll back to this'), ['Roll back to this']);
  assert.ok(dialogs(env).textContent.includes('Current'));
  await dialogButton(env, 'Roll back to this').click();
  assert.equal(dialogs(env).querySelector('#field-revision').value, 'aaa111');
  env.fixture.packs = { packs: [pack], actions: ['rollback'] };
  await dialogs(env).querySelector('form').fire('submit'); await tick(); await tick();
  assert.deepEqual(lastWrite(env), { action: 'rollback', data: { namespace: 'team', revision: 'aaa111', confirm: true } });

  mount(env, 'packs', { packs: [Object.assign({}, pack, { managed_by: 'amux' })], actions: ['rollback', 'remove'] });
  await findButton(env, 'Details').click();
  assert.equal(dialogs(env).querySelectorAll('button').some(b => b.textContent === 'Roll back to this'), false);
});

test('the profiles page shows what each expert is used by and filters used from unused', async () => {
  const env = environment();
  mount(env, 'profiles', {
    profiles: [{ ref: 'team/a', name: 'alpha' }, { ref: 'team/b', name: 'beta' }, { ref: 'builtin/c', name: 'gamma' }],
    usage: { 'team/a': { sessions: 2, bots: 1, schedules: 0, instances: 0 }, 'team/b': { sessions: 0, bots: 0, schedules: 0, instances: 0 } }, usage_unavailable: []
  });
  const page = () => env.document.getElementById('page');
  const names = () => page().querySelectorAll('.profile-card h2').map(h => h.textContent).sort();
  assert.deepEqual(names(), ['alpha', 'beta', 'gamma']);
  assert.ok(page().textContent.includes('Used by 3'));
  const chipTitle = page().querySelectorAll('.chip').find(c => c.textContent === 'Used by 3').getAttribute('title');
  assert.equal(chipTitle, 'Bot default experts 1 · Sessions 2');
  const select = () => page().querySelectorAll('select').find(item => item.getAttribute('aria-label') === 'Usage');
  assert.deepEqual(select().querySelectorAll('option').map(o => o.textContent), ['All experts', 'In use (1)', 'Unused (2)']);
  select().value = 'unused'; await select().fire('change');
  assert.deepEqual(names(), ['beta', 'gamma']);
  select().value = 'used'; await select().fire('change');
  assert.deepEqual(names(), ['alpha']);
  select().value = ''; await select().fire('change');
  assert.equal(names().length, 3);
  // Without usage data there is no claim about use and no usage filter.
  mount(env, 'profiles', { profiles: [{ ref: 'team/a', name: 'alpha' }] });
  assert.equal(page().textContent.includes('Unused'), false);
  assert.equal(select(), undefined);
});

test('only the expert pages ask for usage; option lists read profiles without it', async () => {
  const env = environment();
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap;
  env.app.state.route = { page: 'profiles', params: {} };
  await env.app.loadRoute();
  env.app.state.route = { page: 'profile', params: { ref: 'team/a' } };
  await env.app.loadRoute();
  const urls = env.calls.map(call => call.url);
  assert.ok(urls.includes('/api/console/profiles?usage=1') && urls.includes('/api/console/profile?ref=team%2Fa&usage=1'), urls.join(' '));
  env.calls.length = 0;
  await env.app.openNewSession();
  assert.ok(env.calls.map(call => call.url).includes('/api/console/profiles'));
  assert.equal(env.calls.some(call => call.url.includes('usage=1')), false);
});

test('an expert page lists where it is used, with links, and which pack provides it', () => {
  const env = environment();
  const usage = { ref: 'team/a', counts: { sessions: 25, bots: 1, schedules: 1, instances: 0 }, unavailable: [], items: {
    sessions: Array.from({ length: 20 }, (_, i) => ({ id: 's' + i, name: 'review-' + i, state: 'running', target: 'r' })),
    bots: [{ id: 'default', name: 'Helper', state: 'running' }], schedules: [{ id: '7', name: 'nightly', state: 'enabled' }], instances: [] } };
  mount(env, 'profile', { profile: { ref: 'team/a', name: 'alpha', skills: [] }, usage, pack: { namespace: 'team', source_kind: 'git', source_host: 'example.com', current_revision: 'abcdef1234567890', auto_sync: true, sync_interval: '30s' } }, { ref: 'team/a' });
  const page = env.document.getElementById('page');
  const text = page.textContent;
  for (const expected of ['Where this expert is used', 'Bot default experts', 'Helper', 'nightly', 'review-0', '+5 more', 'Provided by', 'team', 'example.com', 'abcdef123456', 'Auto sync']) assert.ok(text.includes(expected), expected);
  assert.equal(text.includes('Persistent instances'), false, 'an empty source is not listed');
  const hrefs = page.querySelectorAll('a').map(a => a.getAttribute('href'));
  assert.ok(hrefs.includes('#session?name=review-0&target=r') && hrefs.includes('#schedule?schedule_id=7') && hrefs.includes('#bots?') && hrefs.includes('#packs?'), hrefs.join(' '));
  // Nothing refers to it, and the builtin pack has no source to show.
  mount(env, 'profile', { profile: { ref: 'builtin/codex', name: 'codex' }, usage: { ref: 'builtin/codex', counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, items: {}, unavailable: [] }, pack: { namespace: 'builtin', source_kind: 'builtin' } }, { ref: 'builtin/codex' });
  assert.ok(env.document.getElementById('page').textContent.includes('No references found in the checked sources.'));
  assert.equal(env.document.getElementById('page').textContent.includes('Provided by'), false);
  // Unreadable sources are named, never counted as zero use.
  mount(env, 'profile', { profile: { ref: 'team/a', name: 'alpha' }, usage: { ref: 'team/a', counts: { sessions: 0, bots: 0, schedules: 0, instances: 0 }, items: {}, unavailable: ['schedules', 'bots'] } }, { ref: 'team/a' });
  const incomplete = env.document.getElementById('page').textContent;
  assert.ok(incomplete.includes('Some usage could not be read (Schedules, Bot default experts)') && !incomplete.includes('No references found in the checked sources.'));
});

test('group defaults block removal and appear as usage', async () => {
  const env = environment();
  const usage = usageFixture({ counts: { sessions: 0, bots: 0, groups: 1, schedules: 0, instances: 0 }, refs: { 'team/a': { groups: 1 } }, items: { groups: [{ id: 'oc-group', name: 'Operations' }] } });
  const { submit, force } = await openRemove(env, usage);
  assert.ok(dialogs(env).textContent.includes('Group default experts'));
  assert.ok(dialogs(env).textContent.includes('Operations'));
  assert.equal(force.parentNode.hidden, false);
  assert.equal(submit.disabled, true);
});

test('incomplete usage never labels an expert unused', () => {
  const env = environment();
  mount(env, 'profiles', { profiles: [{ ref: 'team/a', name: 'alpha' }], usage: {}, usage_unavailable: ['bots', 'groups'] });
  const page = env.document.getElementById('page');
  assert.ok(page.textContent.includes('Usage unknown'));
  assert.equal(page.textContent.includes('Unused'), false);
  const options = page.querySelectorAll('select').find(item => item.getAttribute('aria-label') === 'Usage').querySelectorAll('option').map(o => o.textContent);
  assert.equal(options.some(text => text.startsWith('Unused')), false);
});

test('an empty filtered namespace still exposes the All tab', async () => {
  const env = environment();
  env.app.state.filters.set('profiles', { query: '', state: '', target: '', group: '', kind: '', namespace: 'absent', use: '', page: 1 });
  mount(env, 'profiles', { profiles: [{ ref: 'team/a', name: 'alpha' }] });
  const page = () => env.document.getElementById('page');
  assert.equal(page().querySelectorAll('.profile-card').length, 0);
  await page().querySelectorAll('.tab').find(tab => tab.textContent.startsWith('All')).click();
  assert.equal(page().querySelectorAll('.profile-card').length, 1);
});

test('rollback row selection supersedes a previous draft and names the revision', async () => {
  const env = environment();
  let confirmation = '';
  env.window.confirm = text => { confirmation = text; return true; };
  const pack = { namespace: 'team', current_revision: 'ccc333', versions: [{ revision: 'aaa111' }, { revision: 'bbb222' }, { revision: 'ccc333' }] };
  mount(env, 'packs', { packs: [pack], actions: ['rollback'] });
  await findButton(env, 'Roll back pack').click();
  const select = dialogs(env).querySelector('#field-revision');
  select.value = 'bbb222'; await dialogs(env).querySelector('form').fire('change');
  await dialogButton(env, 'Cancel').click();
  await findButton(env, 'Details').click();
  const first = dialogs(env).querySelectorAll('tbody tr')[0];
  await first.querySelectorAll('button').find(b => b.textContent === 'Roll back to this').click();
  assert.equal(dialogs(env).querySelector('#field-revision').value, 'aaa111');
  env.fixture.packs = { packs: [pack], actions: ['rollback'] };
  await dialogs(env).querySelector('form').fire('submit'); await tick();
  assert.ok(confirmation.includes('team') && confirmation.includes('aaa111'), confirmation);
  assert.equal(lastWrite(env).data.revision, 'aaa111');
});

test('partial Pack removal displays a warning and refreshes the list', async () => {
  const env = environment();
  const { submit } = await openRemove(env, unusedUsage());
  const baseFetch = env.window.fetch;
  env.window.fetch = async (url, options) => {
    if (url === '/api/console/packs' && options.method === 'POST') {
      env.calls.push({ url, options });
      return { ok: true, status: 200, json: async () => ({ ok: true, warning_code: 'pack_files_not_removed' }) };
    }
    return baseFetch(url, options);
  };
  env.fixture.packs = { packs: [], actions: ['remove'] };
  await submit.click(); await tick(); await tick();
  assert.ok(env.document.getElementById('notifications').textContent.includes('unregistered, but its downloaded files could not be removed'));
  assert.equal(dialogs(env).querySelector('.modal'), null);
  assert.ok(env.calls.some(call => call.url.startsWith('/api/console/packs') && (!call.options.method || call.options.method === 'GET')));
});

test('expert library screens read naturally in Chinese with no raw keys', async () => {
  const env = environment();
  env.window.navigator.language = 'zh-CN';
  const { submit } = await openRemove(env, usageFixture(), undefined, '移除 Pack');
  const dialog = dialogs(env).textContent;
  for (const expected of ['移除 Pack', '从本机移除“team”', '助手默认专家', '移除后，它们将无法再启动新的工作。', '我已了解，仍然移除', '保留已下载的文件']) assert.ok(dialog.includes(expected), expected);
  assert.ok(submit);
  mount(env, 'profile', { profile: { ref: 'team/a', name: 'alpha' }, usage: { ref: 'team/a', counts: { sessions: 1, bots: 0, schedules: 0, instances: 0 }, items: { sessions: [{ id: 's', name: 'one', state: 'running', target: 'r' }] }, unavailable: [] }, pack: { namespace: 'team', source_kind: 'git', source_host: 'example.com' } }, { ref: 'team/a' });
  const page = env.document.getElementById('page').textContent;
  for (const expected of ['使用情况', '来源 Pack', '重启或恢复时可能需要 Pack 文件']) assert.ok(page.includes(expected), expected);
  for (const key of ['usageTitle', 'removePackLead', 'providedBy', 'usageSessionsNote', 'pack_in_use']) assert.equal((page + dialog).includes(key), false, key);
});

test('a long replay the Hub cuts off is shown as loading history, not as a lost connection', async () => {
  const env = environment('#session?name=chat&target=r');
  const streams = []; const Base = env.window.EventSource;
  env.window.EventSource = class extends Base { constructor(url) { super(url); streams.push(this); } };
  env.fixture.session = { session: { id: 's1', name: 'chat', state: 'running', target_id: 'r' }, queue: [], actions: ['submit'], activity: { active_turn: false, turn_state: 'idle' } };
  env.app.state.authenticated = true; env.app.state.bootstrap = env.fixture.bootstrap; env.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
  await env.app.loadRoute();
  const status = () => env.document.getElementById('stream-status').textContent;
  const connection = () => env.document.getElementById('connection').textContent;
  assert.ok(status().includes('Loading history…'), status());
  // Some history arrives, then the Hub cuts the stream: the browser will resume from the last event.
  await streams[0].onopen();
  await streams[0].onmessage({ data: JSON.stringify({ cursor: 'c-1', kind: 'OUTPUT', text: 'one' }) });
  await streams[0].listeners.disconnected();
  assert.ok(status().includes('Loading history…'), status());
  assert.equal(env.app.state.connection, 'connected');
  assert.equal(connection().includes('Reconnecting'), false);
  // Reconnected and still replaying; then the replay completes and the stream is live.
  await streams[0].onopen();
  assert.ok(status().includes('Loading history…'));
  await streams[0].onmessage({ data: JSON.stringify({ cursor: 'c-2', caught_up: true }) });
  assert.ok(status().includes('Live events'), status());
  // Losing the live stream afterwards is a real reconnect.
  env.advance(1000);
  await streams[0].onerror();
  assert.ok(status().includes('Reconnecting'), status());
  assert.equal(env.app.state.connection, 'reconnecting');
  assert.ok(connection().includes('Reconnecting'));
  // A stream that fails before delivering anything is a reconnect too, not loading.
  const fresh = environment('#session?name=chat&target=r');
  const second = []; const FreshBase = fresh.window.EventSource;
  fresh.window.EventSource = class extends FreshBase { constructor(url) { super(url); second.push(this); } };
  fresh.fixture.session = env.fixture.session;
  fresh.app.state.authenticated = true; fresh.app.state.bootstrap = fresh.fixture.bootstrap; fresh.app.state.route = { page: 'session', params: { name: 'chat', target: 'r' } };
  await fresh.app.loadRoute();
  await second[0].onerror();
  assert.ok(fresh.document.getElementById('stream-status').textContent.includes('Reconnecting'));
});
