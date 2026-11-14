'use strict';
// A deliberately small DOM test double. No npm dependency and no network services.
// Layout/visual acceptance belongs to browser verification, not this DOM double.
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

class Node {
  constructor(tag = '', text = '') { this.tagName = tag.toUpperCase(); this.nodeType = tag ? 1 : 3; this.children = []; this.attrs = {}; this.listeners = {}; this._text = String(text); this.value = ''; this.parentNode = null; this.dataset = {}; this.scrollTop = 0; this.scrollHeight = 300; this.clientHeight = 300; this.offsetParent = {}; }
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
