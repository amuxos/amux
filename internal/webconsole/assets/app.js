/* amux Console: dependency-free, same-origin operator client. No user content is HTML. */
(function (root) {
  'use strict';
  const I18n = root.ConsoleI18n;
  const own = (o, k) => Object.prototype.hasOwnProperty.call(o || {}, k);
  function field(object, name, fallback) {
    if (!object) return fallback;
    const camel = name.replace(/_([a-z])/g, (_, c) => c.toUpperCase());
    return object[name] ?? object[camel] ?? fallback;
  }
  function list(snapshot, key) {
    if (Array.isArray(snapshot)) return snapshot;
    const value = field(snapshot, key, []);
    return Array.isArray(value) ? value : [];
  }
  function decodeBase64(value) {
    if (!value) return '';
    try {
      const binary = root.atob(String(value));
      return new TextDecoder('utf-8', { fatal: false }).decode(Uint8Array.from(binary, c => c.charCodeAt(0)));
    } catch (_) { return null; }
  }
  function decodeJSON(value) {
    if (value && typeof value === 'object') return value;
    if (!value) return null;
    try { return JSON.parse(decodeBase64(value)); } catch (_) { return null; }
  }
  function normalizePermission(value, id, turnID) {
    const p = value && typeof value === 'object' ? value : {};
    const tool = field(p, 'tool_call', {});
    return {
      id: id || p.id || p.permission_id || '', turn_id: turnID || p.turn_id || '',
      tool, question: tool.question || p.question || '',
      multi: Boolean(field(tool, 'multi_select', false)),
      secret: Boolean(tool.secret), allowOther: Boolean(field(tool, 'allow_other', false)),
      options: list(p, 'options').map(o => ({ id: o.id || o.optionId || o.option_id || '', name: o.name || o.label || o.id || o.optionId || o.option_id || '', kind: o.kind || '', description: o.description || '' })).filter(o => o.id),
      raw: p
    };
  }
  const kindNames = ['OUTPUT', 'STATE_CHANGE', 'HEARTBEAT', 'PERMISSION_REQUEST', 'STRUCTURED', 'ERROR', 'USAGE'];
  function normalizeEvent(event) {
    const canonical = decodeJSON(field(event, 'canonical_json'));
    if (canonical && canonical.kind) {
      const b = canonical.body || {};
      const tool = b.tool || {};
      return {
        id: event.cursor || field(event, 'event_id') || JSON.stringify(canonical.cursor || ''),
        kind: canonical.kind, text: b.text || b.message || b.input_summary || '',
        tool: canonical.kind === 'tool_changed' ? { id: tool.id, name: tool.title || tool.name, status: b.phase, input: tool.input, output: tool.output_preview || tool.error } : null,
        permission: canonical.kind === 'permission_requested' ? normalizePermission(b.request, null, canonical.turn && canonical.turn.id) : null,
        resolved: canonical.kind === 'permission_resolved' ? b.request_id : '',
        state: b.terminal && b.terminal.outcome, body: b, at: canonical.at || '',
        historical: Boolean(event.historical), raw: event
      };
    }
    const kind = typeof event.kind === 'number' ? kindNames[event.kind] : String(event.kind || 'OUTPUT').toUpperCase();
    const permission = kind === 'PERMISSION_REQUEST' ? normalizePermission(decodeJSON(field(event, 'permission_payload')), field(event, 'permission_id'), field(event, 'turn_id')) : null;
    const toolID = field(event, 'tool_id');
    return {
      id: event.cursor || field(event, 'event_id') || (field(event, 'seq') ? 'seq:' + field(event, 'seq') : ''),
      kind: kind === 'OUTPUT' ? (event.input ? 'user_message' : 'assistant_delta') : kind.toLowerCase(),
      text: event.text || field(event, 'error_message') || (kind === 'OUTPUT' ? decodeBase64(event.data) : '') || '',
      decodeError: kind === 'OUTPUT' && event.data && decodeBase64(event.data) === null,
      tool: toolID || field(event, 'tool_name') ? { id: toolID, name: field(event, 'tool_title') || field(event, 'tool_name'), status: field(event, 'tool_status'), input: decodeJSON(field(event, 'tool_input_json')), output: field(event, 'tool_output_preview') } : null,
      permission, state: event.state || '', body: decodeJSON(field(event, 'structured_json')) || decodeJSON(field(event, 'usage_json')), at: '', historical: Boolean(event.historical), raw: event
    };
  }
  function supports(snapshot, action, item) {
    if (item && Array.isArray(item.actions)) return item.actions.includes(action);
    if (snapshot && Array.isArray(snapshot.actions)) return snapshot.actions.includes(action);
    return Boolean(snapshot && snapshot.capabilities && snapshot.capabilities[action] === true);
  }
  function deriveCounts(sessions) {
    return {
      active: sessions.filter(s => s.state !== 'archived').length,
      working: sessions.filter(s => Boolean(field(s, 'active_turn', false)) || ['running', 'in_progress'].includes(field(s, 'turn_state'))).length,
      attention: sessions.filter(s => Number(field(s, 'pending_permissions', 0)) > 0 || ['waiting_permission', 'failed', 'error'].includes(field(s, 'turn_state'))).length,
      sleeping: sessions.filter(s => s.state === 'sleeping').length
    };
  }
  function routeFromHash(hash) {
    const text = String(hash || '').replace(/^#\/?/, '');
    const [page, query] = text.split('?');
    const allowed = ['overview', 'sessions', 'session', 'tasks', 'task', 'instances', 'instance', 'schedules', 'schedule', 'bots', 'groups', 'profiles', 'profile', 'packs', 'credentials', 'settings'];
    return { page: allowed.includes(page) ? page : 'overview', params: Object.fromEntries(new URLSearchParams(query || '')) };
  }
  class APIError extends Error {
    constructor(message, status, payload) { super(message); this.status = status || 0; this.payload = payload; }
  }
  const state = {
    language: 'auto', theme: 'auto', bootstrap: null, authenticated: false, loading: false,
    authConfig: null, authConfigLoading: false, authConfigError: '', consoleExpired: false,
    route: routeFromHash(root.location && root.location.hash), cache: new Map(), filters: new Map(), drafts: new Map(), scroll: new Map(),
    online: !root.navigator || root.navigator.onLine !== false, connection: 'connected', lastSync: null,
    routeError: null, busy: false, generation: 0, timeline: new Map(), streams: null, modal: null, navOpen: false
  };
  const navGroups = [['operations', ['overview', 'sessions', 'tasks', 'instances', 'schedules']], ['resources', ['bots', 'groups', 'profiles', 'packs', 'credentials']], ['system', ['settings']]];
  const pageKeys = { task: 'tasks', instance: 'instances', profile: 'profiles', session: 'sessions', schedule: 'schedules' };
  const icons = {
    overview: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
    sessions: 'M4 4h16v12H9l-5 4z M8 8h8 M8 12h5', tasks: 'm12 3 10 5-10 5L2 8z M3 13l9 5 9-5 M3 18l9 5 9-5',
    instances: 'M8 3h8v7H8z M3 16h7v5H3z M14 16h7v5h-7z M12 10v3 M6 16v-3h12v3',
    schedules: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M12 7v6l4 2',
    bots: 'M5 7h14a2 2 0 0 1 2 2v10H3V9a2 2 0 0 1 2-2 M12 7V3 M9 3h6 M8 12v3 M16 12v3',
    profiles: 'M4 3h16v18H4z M8 8h8 M8 12h8 M8 16h5', packs: 'm12 3 9 5v9l-9 5-9-5V8z M3 8l9 5 9-5 M12 13v9',
    credentials: 'M13 8a5 5 0 1 0-5 5 M12 12l8 8 M16 16l3-3 M18 18l3-3',
    settings: 'M4 6h16 M4 12h16 M4 18h16 M9 4v4 M15 10v4 M8 16v4',
    search: 'M10 4a6 6 0 1 0 0 12 6 6 0 0 0 0-12 M15 15l6 6', menu: 'M4 6h16 M4 12h16 M4 18h16',
    refresh: 'M20 7v5h-5 M4 17v-5h5 M6 7a7 7 0 0 1 12-2l2 3 M4 16l2 3a7 7 0 0 0 12-2',
    close: 'm5 5 14 14 M5 19 19 5', plus: 'M12 5v14 M5 12h14', alert: 'm12 3 10 18H2z M12 8v5 M12 17v1',
    shield: 'm12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6z M8 12l3 3 5-6', user: 'M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M4 22v-3a8 8 0 0 1 16 0v3',
    tool: 'm4 6 6 6-6 6 M13 18h7', logout: 'M9 3H3v18h6 M9 12h12 M16 7l5 5-5 5', arrow: 'M5 12h14 M14 7l5 5-5 5'
  };
  function language() { return I18n.normalize(state.language === 'auto' ? root.navigator.language : state.language); }
  const t = (key, params) => I18n.t(key, language(), params);
  function e(tag, attrs, ...children) {
    const node = document.createElement(tag);
    for (const [name, value] of Object.entries(attrs || {})) {
      if (value === null || value === undefined || value === false) continue;
      if (name === 'class') node.className = value;
      else if (name === 'text') node.textContent = String(value);
      else if (name.startsWith('on')) node.addEventListener(name.slice(2).toLowerCase(), value);
      else if (name === 'value' || name === 'checked' || name === 'disabled' || name === 'selected') node[name] = value;
      else node.setAttribute(name, value === true ? '' : String(value));
    }
    for (const child of children.flat(Infinity)) {
      if (child === null || child === undefined || child === false) continue;
      node.appendChild(typeof child === 'object' ? child : document.createTextNode(String(child)));
    }
    return node;
  }
  function replace(host, ...children) {
    host.replaceChildren(...children.flat(Infinity).filter(value => value !== null && value !== undefined && value !== false));
  }
  function icon(name, size) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    for (const [k, v] of Object.entries({ width: size || 17, height: size || 17, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 1.6, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' })) svg.setAttribute(k, v);
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', icons[name] || icons.tasks); svg.appendChild(path); return svg;
  }
  function button(key, onclick, tone, glyph) {
    return e('button', { type: 'button', class: 'ui-btn ' + (tone || ''), onclick }, glyph ? icon(glyph, 14) : null, t(key));
  }
  function iconButton(key, glyph, onclick) { return e('button', { type: 'button', class: 'icon-button', 'aria-label': t(key), title: t(key), onclick }, icon(glyph)); }
  function labelState(value) { return own(I18n.messages, String(value)) ? t(String(value)) : (value || t('unknown')); }
  function badge(value) {
    const good = ['success', 'succeeded', 'completed', 'connected', 'enabled'];
    const warn = ['waiting_permission', 'pending', 'waiting', 'reconnecting', 'paused'];
    const bad = ['error', 'failed', 'offline'];
    const tone = good.includes(value) ? 'good' : warn.includes(value) ? 'warn' : bad.includes(value) ? 'bad' : ['running', 'in_progress', 'active'].includes(value) ? 'blue' : '';
    return e('span', { class: 'badge ' + tone }, e('span', { class: 'dot', 'aria-hidden': true }), labelState(value));
  }
  function pretty(value) { return typeof value === 'string' ? value : JSON.stringify(value, null, 2); }
  function code(value) { return e('pre', { class: 'code', text: pretty(value) ?? '' }); }
  function date(value) {
    if (!value) return t('unknown');
    const number = Number(value);
    const d = new Date(Number.isFinite(number) ? (number < 1e12 ? number * 1000 : number) : value);
    return Number.isNaN(d.valueOf()) ? String(value) : new Intl.DateTimeFormat(language(), { dateStyle: 'medium', timeStyle: 'short' }).format(d);
  }
  function display(item) { return item.title || item.name || item.display_name || item.profile || item.ref || item.namespace || item.task_id || item.id || t('unknown'); }
  function ident(item) { return item.id ?? item.task_id ?? item.agent_instance_id ?? item.ref ?? item.namespace ?? item.profile ?? item.name ?? ''; }
  function section(key, body, right, extraClass) {
    return e('section', { class: 'panel ' + (extraClass || '') }, e('div', { class: 'panel-head' }, e('h2', { text: t(key) }), right), body);
  }
  function definition(entries) {
    return e('dl', {}, entries.map(([key, value]) => e('div', { class: 'definition' }, e('dt', { text: t(key) }), e('dd', {}, value && typeof value === 'object' && value.nodeType ? value : String(value ?? t('unknown'))))));
  }
  function empty(key, hint) { return e('div', { class: 'empty' }, icon('sessions', 30), e('h2', { text: t(key || 'empty') }), e('p', { text: t(hint || 'emptyHint') })); }
  function notify(message, bad) {
    const host = document.getElementById('notifications');
    host.replaceChildren(e('div', { class: 'toast ' + (bad ? 'bad' : ''), text: message }));
    clearTimeout(notify.timer); notify.timer = setTimeout(() => host.replaceChildren(), bad ? 12000 : 6000);
  }
  function storageGet(key) { try { return root.localStorage.getItem('amux.console.' + key); } catch (_) { return null; } }
  function storageSet(key, value) { try { root.localStorage.setItem('amux.console.' + key, value); } catch (_) { /* Private mode can disable preference storage. */ } }
  function applyPreferences() {
    document.documentElement.lang = language();
    document.documentElement.dataset.theme = state.theme === 'auto' ? (root.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : state.theme;
  }
  function selectOptions(values, value, onchange, attrs) {
    return e('select', Object.assign({ value, onchange: ev => onchange(ev.target.value) }, attrs), values.map(option => e('option', { value: option.value, selected: String(option.value) === String(value), text: option.label })));
  }
  function preferenceControls() {
    return [selectOptions([{ value: 'auto', label: t('auto') }, { value: 'en', label: 'English' }, { value: 'zh-CN', label: '简体中文' }], state.language, value => {
      const modal = captureModal(); state.language = value; storageSet('language', value); applyPreferences(); render(); restoreModal(modal);
    }, { class: 'top-select', 'aria-label': t('language') }),
    selectOptions(['auto', 'light', 'dark'].map(value => ({ value, label: t(value) })), state.theme, value => { state.theme = value; storageSet('theme', value); applyPreferences(); }, { class: 'top-select', 'aria-label': t('theme') })];
  }
  function errorMessage(error, write) {
    const machineCode = error.payload && (error.payload.code || error.payload.error);
    const known = typeof machineCode === 'string' && own(I18n.messages, machineCode) ? t(machineCode) : '';
    if (error.status === 403) return known || t('forbidden');
    if (error.status === 409) return t('conflict') + (known ? '\n' + known : '');
    if (error.status === 401) return known || t('loginExpired');
    return (write && !error.status ? t('writeUncertain') : t('requestFailed')) + (known ? '\n' + known : !machineCode && error.message ? '\n' + error.message : '');
  }
  function windowExpired() {
    return state.consoleExpired || Boolean(state.authConfig && state.authConfig.expires_at > 0 && state.authConfig.expires_at * 1000 <= Date.now());
  }
  function scheduleExpiry() {
    root.clearTimeout(state.expiryTimer); state.expiryTimer = null;
    const expiresAt = state.authConfig && state.authConfig.expires_at;
    if (!expiresAt || state.consoleExpired) return;
    if (windowExpired()) { expire(true); return; }
    state.expiryTimer = root.setTimeout(scheduleExpiry, Math.min(expiresAt * 1000 - Date.now(), 2147483647));
    if (state.expiryTimer && state.expiryTimer.unref) state.expiryTimer.unref();
  }
  function expire(closed, messageKey) {
    state.consoleExpired = Boolean(closed || windowExpired());
    if (state.consoleExpired) { root.clearTimeout(state.expiryTimer); state.expiryTimer = null; }
    state.loginErrorKey = state.consoleExpired ? 'console_expired' : messageKey || ''; state.loginError = '';
    state.authenticated = false; state.bootstrap = null; state.loading = false; state.busy = false;
    state.routeError = null; state.cache.clear(); state.timeline.clear(); state.drafts.clear(); state.filters.clear(); state.scroll.clear();
    state.generation++; closeStream(); closeModal(true);
    const notifications = document.getElementById('notifications'); if (notifications) notifications.replaceChildren();
    render();
  }
  async function request(path, options) {
    if (path !== '/auth/config' && windowExpired()) {
      expire(true); throw new APIError(t('console_expired'), 401, { code: 'console_expired' });
    }
    const method = options && options.method || 'GET';
    const headers = { Accept: 'application/json' };
    if (method === 'POST') {
      headers['Content-Type'] = 'application/json';
      if (state.bootstrap && state.bootstrap.csrf_token) headers['X-CSRF-Token'] = state.bootstrap.csrf_token;
    }
    let response;
    try { response = await root.fetch(path, Object.assign({ credentials: 'same-origin', cache: 'no-store', headers }, options)); }
    catch (error) { if (error.name === 'AbortError') throw error; state.connection = 'offline'; updateConnection(); throw new APIError(t('networkError')); }
    let payload = null;
    try { payload = await response.json(); } catch (_) { if (response.status !== 204) throw new APIError(t('invalidResponse'), response.status); }
    if (!response.ok) {
      const machineCode = payload && (payload.code || payload.error);
      if (machineCode === 'console_expired') expire(true);
      else if (response.status === 401 && !['/auth/pair', '/auth/login', '/auth/config'].includes(path) && (state.authenticated || path !== '/api/console/bootstrap')) expire(false, 'consoleClosedOrExpired');
      throw new APIError(payload && (typeof payload.error === 'string' ? payload.error : payload.detail || payload.message || payload.error && payload.error.message) || response.statusText || t('requestFailed'), response.status, payload);
    }
    if (payload && (payload.ok === false || typeof payload.error === 'string' && payload.error)) throw new APIError(payload.error || payload.message || t('requestFailed'), response.status, payload);
    state.lastSync = Date.now();
    if (state.connection === 'offline' && state.online) state.connection = 'connected';
    updateConnection();
    return payload || {};
  }
  function get(resource, params) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params || {})) if (value !== undefined && value !== null && value !== '') query.set(key, value);
    return request('/api/console/' + resource + (query.size ? '?' + query.toString() : ''));
  }
  async function write(resource, action, data) {
    if (!state.online) throw new APIError(t('noWritesOffline'));
    const response = await request('/api/console/' + resource, { method: 'POST', body: JSON.stringify({ action, data }) });
    if (response.status === 'state_changed') throw new APIError(t('conflict'), 409, response);
    return response;
  }
  function warnings(snapshot) {
    const messages = [];
    if (snapshot && (snapshot.partial || snapshot.warning_code)) messages.push(t('partial'));
    for (const key of ['errors', 'unavailable', 'warnings']) {
      const raw = snapshot && snapshot[key];
      if (!raw || Array.isArray(raw) && !raw.length) continue;
      const values = Array.isArray(raw) ? raw : typeof raw === 'object' ? Object.entries(raw).map(([k, v]) => k + ': ' + pretty(v)) : [raw];
      for (const value of values) messages.push(typeof value === 'string' ? value : pretty(value));
    }
    return messages.length ? e('div', { class: 'banner warn', role: 'status' }, icon('alert'), e('div', {}, e('strong', { text: t('partial') }), messages.map(text => e('p', { text })))) : null;
  }
  function updateConnection() {
    const host = document.getElementById('connection');
    if (host) host.replaceChildren(badge(!state.online ? 'offline' : state.connection), e('span', { class: 'connection-text', text: state.lastSync ? t('lastSync', { time: date(state.lastSync) }) : '' }));
    const offline = document.getElementById('offline-banner');
    if (offline) { offline.hidden = state.online && state.connection !== 'offline'; offline.textContent = t('offline') + ' · ' + t('writeUncertain'); }
  }
  function render() {
    const oldTimeline = document.getElementById('timeline');
    if (oldTimeline && state.activeStreamRef) timelineFor(state.activeStreamRef).scrollTop = oldTimeline.scrollTop;
    const app = document.getElementById('app');
    if (!state.authenticated) { renderLogin(app); return; }
    const activePage = pageKeys[state.route.page] || state.route.page;
    const nav = e('aside', { class: 'sidebar ' + (state.navOpen ? 'expanded' : ''), id: 'navigation', 'aria-label': t('menu') }, navGroups.map(([group, pages], index) => [index ? e('div', { class: 'nav-group', text: t(group) }) : null, pages.map(page => e('button', { type: 'button', class: 'nav-item ' + (activePage === page ? 'active' : ''), 'aria-current': activePage === page ? 'page' : null, onclick: () => navigate(page) }, icon(page, 16), t(page)))]),
      e('div', { class: 'sidebar-bottom' }, badge('authenticated'), e('p', { text: t('singleHub') }), e('p', { text: state.bootstrap.version || t('unknown') })));
    const header = e('header', { class: 'app-top' }, e('button', { type: 'button', class: 'icon-button mobile-menu', 'aria-label': t('menu'), 'aria-controls': 'navigation', 'aria-expanded': String(state.navOpen), onclick: () => { state.navOpen = !state.navOpen; render(); } }, icon('menu')),
      e('div', { class: 'app-logo' }, icon('tasks', 24), 'amux', e('small', { text: 'Console' })),
      e('button', { type: 'button', class: 'app-search', 'aria-label': t('search'), onclick: openSearch }, icon('search'), e('span', { text: t('search') }), e('kbd', { text: '⌘ K' })),
      e('div', { class: 'app-top-right' }, e('div', { id: 'connection', class: 'actions' }), preferenceControls(), iconButton('logout', 'logout', logout)));
    const main = e('main', { class: 'main', id: 'main', tabindex: '-1' }, e('div', { class: 'scope', text: 'amux / ' + t(activePage) }), authNotices(), e('div', { id: 'offline-banner', class: 'banner warn', role: 'status', hidden: state.online }), e('div', { id: 'page' }));
    app.replaceChildren(header, e('div', { class: 'app-body' }, e('div', { class: 'mobile-shade ' + (state.navOpen ? 'expanded' : ''), onclick: () => { state.navOpen = false; render(); } }), nav, main));
    updateConnection(); renderPage();
  }
  function title(key, description, actions, literal) {
    return e('div', { class: 'page-title' }, e('div', {}, e('h1', { text: !literal && own(I18n.messages, key) ? t(key) : key }), description ? e('p', { text: own(I18n.messages, description) ? t(description) : description }) : null), e('div', { class: 'actions' }, actions));
  }
  function literalTitle(key, description, actions) { return title(key, description, actions, true); }
  function renderPage() {
    const host = document.getElementById('page');
    if (!host) return;
    const snapshot = state.cache.get(cacheKey()) || null;
    if (state.loading && !snapshot) { host.replaceChildren(e('div', { class: 'empty', role: 'status' }, e('span', { class: 'loading-mark' }), e('p', { text: t('loading') }))); return; }
    if (state.routeError && (!snapshot || [401, 403, 404].includes(state.routeError.status))) {
      host.replaceChildren(title(pageKeys[state.route.page] || state.route.page), e('div', { class: 'banner bad', role: 'alert' }, e('div', { class: 'grow' }, e('strong', { text: t('unavailable') }), e('p', { text: errorMessage(state.routeError) })), button('retry', () => loadRoute(), '', 'refresh'))); return;
    }
    const page = state.route.page;
    const renders = { overview: renderOverview, sessions: renderSessions, session: renderSession, tasks: renderTasks, task: renderTask, instances: renderInstances, instance: renderInstance, schedules: renderSchedules, schedule: renderSchedule, bots: renderBots, groups: renderGroups, profiles: renderProfiles, profile: renderProfile, packs: renderPacks, credentials: renderCredentials, settings: renderSettings };
    const content = renders[page](snapshot || {});
    replace(host, warnings(snapshot), state.routeError ? e('div', { class: 'banner warn', role: 'status' }, e('div', { class: 'grow' }, e('strong', { text: t('staleData') }), e('p', { text: errorMessage(state.routeError) })), button('retry', () => loadRoute(), '', 'refresh')) : null, state.loading ? e('div', { class: 'rail', role: 'status' }, e('span', { class: 'loading-mark' }), t('loading')) : null, content);
    if (page === 'session') drawTimeline();
  }
  function cacheKey(route) { const r = route || state.route; return r.page + '?' + new URLSearchParams(r.params).toString(); }
  function navigate(page, params) {
    const route = '#' + page + (params && Object.keys(params).length ? '?' + new URLSearchParams(params) : '');
    if (root.location.hash === route) { loadRoute(); return; }
    state.scroll.set(cacheKey(), root.scrollY || 0);
    root.location.hash = route;
  }
  async function loadRoute() {
    if (!state.authenticated) return;
    closeStream(); const generation = ++state.generation;
    state.loading = true; state.routeError = null; render();
    try {
      const snapshot = await get(state.route.page, state.route.params);
      if (generation !== state.generation || !state.authenticated) return;
      state.cache.set(cacheKey(), snapshot); state.loading = false; state.lastSync = Date.now(); renderPage();
      if (state.route.page === 'session') openStream(snapshot);
      const y = state.scroll.get(cacheKey()); if (y !== undefined) root.scrollTo(0, y);
    } catch (error) {
      if (generation !== state.generation || !state.authenticated) return;
      state.loading = false; state.routeError = error; renderPage();
    }
  }
  function refreshButton() { const b = button('refresh', () => loadRoute(), '', 'refresh'); b.disabled = state.loading; return b; }
  function target(item) { return field(item, 'target', field(item, 'target_id', '')); }
  function sessionRef(item) { return { name: item.name || item.session_name, target: target(item) || state.route.params.target || state.route.params.target_id || '' }; }
  function capability(snapshot, action, item) {
    return state.bootstrap && state.bootstrap.operator !== false && supports(snapshot, action, item);
  }
  function actionButton(snapshot, resource, action, data, key, item, danger) {
    if (!capability(snapshot, action, item)) return null;
    const b = button(key || action, async () => {
      if (danger && !await confirmAction(key || action, display(item || data))) return;
      b.disabled = true;
      try { await write(resource, action, data); notify(t('saved')); await loadRoute(); }
      catch (error) { notify(errorMessage(error, true), true); }
      finally { if (b.isConnected) b.disabled = false; }
    }, danger ? 'danger' : '');
    b.disabled = !state.online || state.busy; return b;
  }
  function table(columns, rows) {
    return e('div', { class: 'panel' }, rows.length ? e('table', { class: 'table' }, e('thead', {}, e('tr', {}, columns.map(c => e('th', { scope: 'col', text: t(c.key) })))), e('tbody', {}, rows.map(row => e('tr', {}, columns.map(c => e('td', { 'data-label': t(c.key) }, c.render(row))))))) : empty(), e('div', { class: 'pagination', text: t('count', { count: rows.length }) }));
  }
  function rowName(item, page, params) {
    return e('div', {}, e('button', { type: 'button', class: 'link-button', onclick: () => navigate(page, params), text: display(item) }), e('p', { class: 'mono', text: String(ident(item)) }));
  }
  function filterState(page) { if (!state.filters.has(page)) state.filters.set(page, { query: '', state: '', target: '', group: '' }); return state.filters.get(page); }
  function filtered(rows, page) {
    const f = filterState(page || state.route.page);
    return rows.filter(row => (!f.query || JSON.stringify(row).toLocaleLowerCase().includes(f.query.toLocaleLowerCase())) && (!f.state || (row.state || row.status || field(row, 'turn_state')) === f.state) && (!f.target || String(target(row)) === f.target) && (!f.group || (f.group === 'archived' ? row.state === 'archived' : row.state !== 'archived')));
  }
  function filters(rows, options) {
    const page = state.route.page; const f = filterState(page);
    const query = e('input', { type: 'search', value: f.query, placeholder: t('filter'), 'aria-label': t('filter'), oninput: ev => { f.query = ev.target.value; const cursor = ev.target.selectionStart; renderPage(); const input = document.querySelector('.filterbar input'); input.focus(); input.setSelectionRange(cursor, cursor); } });
    const states = [...new Set(rows.map(r => r.state || r.status || field(r, 'turn_state')).filter(Boolean))];
    const targets = [...new Map(rows.filter(r => target(r)).map(r => [String(target(r)), field(r, 'target_name', target(r))])).entries()];
    return e('div', { class: 'filterbar' }, query,
      states.length && options !== 'no-state' ? selectOptions([{ value: '', label: t('all') + ' · ' + t('status') }, ...states.map(value => ({ value, label: labelState(value) }))], f.state, v => { f.state = v; renderPage(); }, { 'aria-label': t('status') }) : null,
      targets.length ? selectOptions([{ value: '', label: t('all') + ' · ' + t('target') }, ...targets.map(([value, label]) => ({ value, label }))], f.target, v => { f.target = v; renderPage(); }, { 'aria-label': t('target') }) : null);
  }
  function renderOverview(snapshot) {
    const sessions = list(snapshot, 'sessions'); const counts = deriveCounts(sessions);
    const active = sessions.filter(s => s.state !== 'archived');
    const activityKnown = s => field(s, 'activity_status') === 'known' || !field(s, 'activity_status') && (own(s, 'turn_state') || own(s, 'turnState') || own(s, 'active_turn') || own(s, 'pending_permissions'));
    const known = active.filter(activityKnown);
    const unknown = active.length - known.length;
    const knownCounts = deriveCounts(known);
    counts.working = unknown ? t('unknown') : knownCounts.working;
    counts.attention = unknown ? t('unknown') : knownCounts.attention;
    const hasTurnFacts = known.length > 0 || !active.length;
    const coverage = unknown ? e('p', { class: 'banner warn', role: 'status', text: t('activityCoverage', { known: known.length, unknown }) }) : null;
    const attention = known.filter(s => Number(field(s, 'pending_permissions', 0)) > 0 || ['waiting_permission', 'failed', 'error'].includes(field(s, 'turn_state')));
    const working = known.filter(s => field(s, 'active_turn') || ['running', 'in_progress'].includes(field(s, 'turn_state')));
    const visibleWork = hasTurnFacts ? working : active.filter(s => s.state === 'running');
    const sorted = sessions.slice().sort((a, b) => Number(field(b, 'last_activity_at', field(b, 'updated_at', 0))) - Number(field(a, 'last_activity_at', field(a, 'updated_at', 0))));
    const sessionRow = item => e('div', { class: 'run-row' }, e('div', { class: 'grow' }, e('h3', { text: display(item) }), e('p', { class: 'row-sub', text: [item.name, field(item, 'target_name', target(item))].filter(Boolean).join(' · ') })), badge(field(item, 'turn_state', item.state)), button('open', () => navigate('session', sessionRef(item))));
    const bots = list(snapshot, 'bots');
    return [title('overview', 'overviewDesc', refreshButton()), coverage, e('div', { class: 'stats' }, [['activeSessions', counts.active], ['working', counts.working], ['needsYou', counts.attention], ['sleepingSessions', counts.sleeping]].map(([key, count]) => e('div', { class: 'stat' }, e('label', { text: t(key) }), e('strong', { text: count }), e('p', { text: t('fromSessions') })))),
      e('div', { class: 'overview-grid' }, e('div', { class: 'stack' }, section('attention', attention.length ? attention.map(sessionRow) : !hasTurnFacts && sessions.length ? empty('unknown', 'turnUnknown') : empty()), section(hasTurnFacts ? 'currentWork' : 'runningSessions', [!hasTurnFacts && sessions.length ? e('p', { class: 'panel-body help', text: t('turnUnknown') }) : null, visibleWork.length ? visibleWork.map(sessionRow) : empty()], button('viewAll', () => navigate('sessions'), 'quiet'))), e('div', { class: 'stack' }, section('recentActivity', sorted.length ? sorted.slice(0, 8).map(item => e('div', { class: 'run-row' }, e('div', { class: 'grow' }, e('button', { type: 'button', class: 'link-button', text: display(item), onclick: () => navigate('session', sessionRef(item)) }), e('p', { class: 'row-sub', text: date(field(item, 'last_activity_at', field(item, 'updated_at'))) })), badge(item.state))) : empty()), bots.length ? section('botConnections', bots.map(bot => e('div', { class: 'run-row' }, e('div', { class: 'avatar', text: display(bot).slice(0, 1) }), e('strong', { class: 'grow', text: display(bot) }), badge(bot.status)))) : null))];
  }
  function renderSessions(snapshot) {
    const rows = list(snapshot, 'sessions'); const f = filterState('sessions');
    return [title('sessions', 'sessionsDesc', [refreshButton(), capability(snapshot, 'create') ? button('newSession', openNewSession, 'primary', 'plus') : null]),
      e('div', { class: 'tabs' }, ['', 'active', 'archived'].map(value => e('button', { type: 'button', class: 'tab ' + (f.group === value ? 'active' : ''), 'aria-pressed': String(f.group === value), onclick: () => { f.group = value; renderPage(); }, text: t(value || 'all') }))), filters(rows),
      table([{ key: 'sessions', render: s => rowName(s, 'session', sessionRef(s)) }, { key: 'turn', render: s => badge(field(s, 'turn_state', 'unknown')) }, { key: 'lifecycle', render: s => badge(s.state) }, { key: 'agentType', render: s => field(s, 'agent_type', t('unknown')) }, { key: 'target', render: s => field(s, 'target_name', target(s)) || t('unknown') }, { key: 'updated', render: s => date(field(s, 'last_activity_at', field(s, 'updated_at'))) }, { key: 'actions', render: s => button('open', () => navigate('session', sessionRef(s))) }], filtered(rows))];
  }
  function renderSession(snapshot) {
    const session = snapshot.session;
    if (!session) return [title('session'), empty('unavailable')];
    const ref = sessionRef(session); const caps = snapshot.actions ? snapshot : (state.cache.get('sessions?') || snapshot);
    const stream = timelineFor(ref); const identity = field(session, 'execution_identity', {});
    const perms = [...stream.permissions.values()];
    for (const permission of list(snapshot, 'permissions')) {
      const normalized = normalizePermission(permission, permission.id || permission.permission_id);
      if (!perms.some(p => p.id === normalized.id)) perms.push(normalized);
    }
    const queue = Array.isArray(snapshot.queue) ? snapshot.queue : list(snapshot.queue, 'items');
    return [literalTitle(display(session), session.id || '', [button('back', () => navigate('sessions')), refreshButton(), ...['interrupt', 'restart', 'archive', 'sleep', 'resume'].map(action => actionButton(caps, 'sessions', action, ref, action, session, action !== 'resume'))]),
      e('div', { class: 'context-strip' }, [['profile', field(session, 'agent_profile')], ['model', field(session, 'effective_model')], ['target', field(session, 'target_name', target(session))], ['principal', field(identity, 'effective_principal_id')]].map(([key, value]) => e('div', {}, e('label', { text: t(key) }), e('strong', { class: 'mono', text: value || t('unknown') })))),
      e('div', { class: 'detail-grid' }, e('div', {}, section('timeline', e('div', { id: 'timeline', class: 'timeline', role: 'log', 'aria-label': t('timeline'), 'aria-live': 'off' }), e('span', { class: 'badge', id: 'stream-status', text: t('live') })),
        capability(caps, 'submit', session) ? composer(ref) : null, e('p', { class: 'note', text: t('reconnectHint') })),
      e('aside', { class: 'stack', id: 'session-aside' }, perms.map(p => permissionPanel(p, ref, caps)), section('details', e('div', { class: 'panel-body' }, definition([['lifecycle', badge(session.state)], ['agentType', field(session, 'agent_type')], ['cwd', session.cwd], ['principal', field(identity, 'effective_principal_id')], ['id', session.id]]))),
        section('queue', e('div', { class: 'panel-body' }, queue.length ? queue.map(item => e('div', { class: 'stack' }, code(item.text || item.prompt || ''), e('div', { class: 'actions' }, actionButton(caps, 'sessions', 'queue-cancel', Object.assign({}, ref, { queue_id: item.id }), 'queue_cancel', session, true)))) : empty(), actionButton(caps, 'sessions', 'queue-clear', ref, 'queue_clear', session, true)))))];
  }
  function composer(ref) {
    const key = 'composer:' + JSON.stringify(ref); const value = state.drafts.get(key) || '';
    const textarea = e('textarea', { value, placeholder: t('draft'), 'aria-label': t('draft'), rows: '3', oninput: ev => state.drafts.set(key, ev.target.value) });
    const send = e('button', { type: 'submit', class: 'ui-btn primary', text: t('send'), disabled: !state.online });
    const form = e('form', { class: 'composer', onsubmit: async ev => {
      ev.preventDefault(); const text = textarea.value;
      if (!text.trim() || send.disabled) return;
      send.disabled = true;
      try { await write('sessions', 'submit', Object.assign({}, ref, { text })); if (textarea.value === text) { textarea.value = ''; state.drafts.delete(key); } notify(t('saved')); }
      catch (error) { notify(errorMessage(error, true), true); }
      finally { send.disabled = !state.online; }
    } }, textarea, e('div', { class: 'actions' }, send));
    textarea.addEventListener('keydown', event => { if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); form.requestSubmit(); } });
    return form;
  }
  function permissionPanel(permission, ref, caps) {
    const tool = permission.tool || {};
    const controls = e('div', { class: 'actions' });
    const canAnswer = capability(caps, 'respond-permission');
    const respond = async data => {
      if (!await confirmAction('respond', permission.question || tool.title || permission.id)) return;
      for (const b of controls.querySelectorAll('button')) b.disabled = true;
      try {
        await write('sessions', 'respond-permission', Object.assign({}, ref, { permission_id: permission.id, turn_id: permission.turn_id }, data));
        timelineFor(ref).permissions.delete(permission.id); notify(t('saved')); await loadRoute();
      } catch (error) { notify(errorMessage(error, true), true); for (const b of controls.querySelectorAll('button')) b.disabled = !state.online; }
    };
    if (canAnswer && permission.options.length) {
      if (permission.multi) {
        const selected = new Set();
        const choices = permission.options.map(o => e('label', { class: 'checkbox-label' }, e('input', { type: 'checkbox', value: o.id, onchange: ev => ev.target.checked ? selected.add(o.id) : selected.delete(o.id) }), e('span', { text: o.name })));
        const b = button('respond', () => selected.size && respond({ option_ids: [...selected], option_id: [...selected][0] }), 'primary'); b.disabled = !state.online;
        controls.append(...choices, b);
      } else for (const option of permission.options) {
        const b = e('button', { type: 'button', class: 'ui-btn ' + (/reject|deny/.test(option.kind) ? 'danger' : ''), text: option.name, title: option.description || option.kind, disabled: !state.online, onclick: () => respond({ option_id: option.id }) });
        controls.appendChild(b);
      }
    }
    return section('permission', e('div', { class: 'panel-body' }, e('p', { text: t('permissionHelp') }), permission.question ? e('p', { text: permission.question }) : null,
      definition([['id', permission.id], ['tool', tool.title || tool.name || t('unknown')]]), field(tool, 'raw_input') ? code(field(tool, 'raw_input')) : null,
      controls.childElementCount ? controls : e('p', { class: 'note', text: t('permissionUnavailable') })), icon('shield'), 'permission');
  }
  function timelineFor(ref) {
    const key = JSON.stringify(ref);
    if (!state.timeline.has(key)) state.timeline.set(key, { items: [], seen: new Set(), permissions: new Map(), cursor: '', scrollTop: null });
    return state.timeline.get(key);
  }
  function ingestEvent(ref, event) {
    const timeline = timelineFor(ref); const normalized = normalizeEvent(event);
    if (normalized.id && timeline.seen.has(normalized.id)) return false;
    if (normalized.id) timeline.seen.add(normalized.id);
    if (event.cursor) timeline.cursor = event.cursor;
    if (normalized.permission && normalized.permission.id) timeline.permissions.set(normalized.permission.id, normalized.permission);
    if (normalized.resolved) timeline.permissions.delete(normalized.resolved);
    if (['heartbeat', 'session_metadata_changed'].includes(normalized.kind) || event.caught_up && !field(event, 'canonical_json')) return true;
    const last = timeline.items[timeline.items.length - 1];
    if (normalized.kind === 'assistant_delta' && last && last.kind === 'assistant_delta' && !last.decodeError && !normalized.decodeError && last.text.length + normalized.text.length < 100000) last.text += normalized.text;
    else if (normalized.tool && normalized.tool.id) {
      const prior = timeline.items.findIndex(item => item.tool && item.tool.id === normalized.tool.id);
      if (prior >= 0) timeline.items[prior] = Object.assign({}, timeline.items[prior], normalized, { tool: Object.assign({}, timeline.items[prior].tool, normalized.tool) });
      else timeline.items.push(normalized);
    } else timeline.items.push(normalized);
    if (timeline.items.length > 1500) timeline.items.splice(0, timeline.items.length - 1500);
    if (timeline.seen.size > 10000) timeline.seen = new Set([...timeline.seen].slice(-5000));
    return true;
  }
  function drawTimeline() {
    if (state.route.page !== 'session') return;
    const snapshot = state.cache.get(cacheKey()); if (!snapshot || !snapshot.session) return;
    const timeline = timelineFor(sessionRef(snapshot.session)); const host = document.getElementById('timeline'); if (!host) return;
    const atBottom = host.scrollHeight - host.scrollTop - host.clientHeight < 70;
    const y = host.scrollTop;
    replace(host, timeline.items.length ? timeline.items.map(eventNode) : empty('noEvents'));
    if (timeline.items.length >= 1500) host.prepend(e('p', { class: 'note', text: t('eventLimit', { count: 1500 }) }));
    host.scrollTop = timeline.scrollTop !== null ? timeline.scrollTop : atBottom ? host.scrollHeight : y;
    timeline.scrollTop = null;
  }
  function eventNode(item) {
    const labels = { assistant_delta: 'agent', assistant_message: 'agent', result_observed: 'result', user_message: 'user', turn_started: 'turnStarted', turn_finished: 'turnFinished', permission_requested: 'permission', permission_request: 'permission', permission_resolved: 'permissionResolved', usage: 'usage', usage_observed: 'usage', thinking_delta: 'thinking', plan_replaced: 'plan', diagnostic: 'diagnostic', session_diagnostic: 'diagnostic', state_change: 'sessionState', error: 'error' };
    const label = item.tool ? 'tool' : labels[item.kind] || 'details';
    let content;
    if (item.tool) content = e('details', { open: item.tool.status === 'error' }, e('summary', {}, item.tool.name || t('tool'), ' · ', labelState(item.tool.status)), item.tool.input ? code(item.tool.input) : null, item.tool.output ? code(item.tool.output) : null);
    else if (item.kind === 'permission_requested' || item.kind === 'permission_request') content = e('p', { class: 'content', text: item.permission && (item.permission.question || item.permission.tool.title || item.permission.id) || t('permission') });
    else if (item.kind === 'thinking_delta') content = e('details', {}, e('summary', { text: t('thinking') }), e('p', { class: 'content', text: item.text }));
    else if (item.text || item.decodeError) content = e('p', { class: 'content', text: item.decodeError ? t('invalidEncoding') : item.text });
    else if (item.state) content = badge(item.state);
    else content = item.body ? code(item.body) : e('span', { class: 'muted', text: item.kind });
    return e('article', { class: 'timeline-item ' + (['turn_started', 'user_message'].includes(item.kind) ? 'user' : '') }, e('div', { class: 'timeline-glyph' }, icon(item.tool ? 'tool' : label === 'permission' ? 'shield' : label === 'user' || item.kind === 'turn_started' ? 'user' : 'bots', 15)), e('div', {}, e('h3', {}, t(label), item.at ? e('time', { datetime: item.at, text: date(item.at) }) : null), content));
  }
  function closeStream() {
    if (state.streams) { state.streams.close(); state.streams = null; }
    if (state.drawTimer) { clearTimeout(state.drawTimer); state.drawTimer = null; }
    const timelineHost = document.getElementById('timeline');
    if (timelineHost && state.activeStreamRef) timelineFor(state.activeStreamRef).scrollTop = timelineHost.scrollTop;
    state.activeStreamRef = null;
  }
  function openStream(snapshot) {
    const session = snapshot.session; if (!session) return;
    const ref = sessionRef(session); const timeline = timelineFor(ref);
    for (const event of list(snapshot, 'events')) ingestEvent(ref, event);
    drawTimeline();
    if (!root.EventSource) { state.connection = 'offline'; updateConnection(); return; }
    const query = new URLSearchParams({ target_id: ref.target || '', name: ref.name });
    // Attach replays on reconnect; deduplication is scoped to this exact session.
    const stream = new root.EventSource('/api/console/events?' + query, { withCredentials: true });
    state.streams = stream; state.activeStreamRef = ref;
    stream.onopen = () => { if (state.streams !== stream) return; state.connection = 'connected'; updateConnection(); const status = document.getElementById('stream-status'); if (status) status.textContent = t('live'); };
    stream.onmessage = message => {
      if (state.streams !== stream) return;
      let event;
      try { event = JSON.parse(message.data); } catch (_) { notify(t('invalidResponse'), true); return; }
      if (event.error && !own(event, 'kind')) { notify(String(event.error), true); return; }
      if (!ingestEvent(ref, event)) return;
      state.lastSync = Date.now();
      if (!state.drawTimer) state.drawTimer = setTimeout(() => {
        state.drawTimer = null; drawTimeline();
        const aside = document.getElementById('session-aside');
        if (aside) {
          const previous = [...aside.querySelectorAll('.permission')]; previous.forEach(p => p.remove());
          const caps = snapshot.actions ? snapshot : state.cache.get('sessions?') || snapshot;
          for (const permission of timeline.permissions.values()) aside.prepend(permissionPanel(permission, ref, caps));
        }
      }, 80);
    };
    stream.addEventListener('disconnected', () => {
      if (state.streams !== stream) return;
      state.connection = 'reconnecting'; updateConnection();
      get('bootstrap').catch(() => {});
    });
    stream.onerror = () => {
      if (state.streams !== stream) return;
      state.connection = state.online ? 'reconnecting' : 'offline'; updateConnection();
      const status = document.getElementById('stream-status'); if (status) status.textContent = t('reconnecting');
      // Native EventSource retries only GET. An auth check redirects expired sessions.
      get('bootstrap').catch(() => {});
    };
  }
  function renderTasks(snapshot) {
    const rows = list(snapshot, 'tasks');
    return [title('tasks', 'tasksDesc', refreshButton()), filters(rows), table([
      { key: 'tasks', render: row => rowName(row, 'task', { task_id: row.task_id || row.id }) }, { key: 'result', render: row => badge(row.status || row.state) },
      { key: 'profile', render: row => row.agent || row.agent_profile || row.profile || t('unknown') }, { key: 'target', render: row => row.target_name || row.target_id || t('unknown') },
      { key: 'instances', render: row => row.agent_instance_id || t('unknown') }, { key: 'actions', render: row => e('div', { class: 'actions' }, button('open', () => navigate('task', { task_id: row.task_id || row.id })), actionButton(snapshot, 'tasks', 'cancel', { task_id: row.task_id || row.id }, 'cancelTask', row, true)) }
    ], filtered(rows))];
  }
  function renderTask(snapshot) {
    const item = snapshot.task; if (!item) return [title('tasks'), empty('unavailable')];
    const child = item.session_ref || {};
    return [literalTitle(display(item), 'tasksDesc', [button('back', () => navigate('tasks')), refreshButton(), child.child_session_id ? button('session', () => navigate('session', { name: child.child_session_id, target: child.target_id || '' })) : null, actionButton(snapshot, 'tasks', 'cancel', { task_id: item.task_id || item.id }, 'cancelTask', item, true)]),
      section('details', e('div', { class: 'panel-body' }, definition([['id', item.task_id || item.id], ['result', badge(item.status || item.state)], ['profile', item.agent || item.agent_profile || item.profile], ['target', item.target_name || item.target_id], ['ownerSession', item.parent_session_id || item.owner_session_id], ['instances', item.agent_instance_id]]), item.result ? code(item.result) : null, item.error ? code(item.error) : null,
        e('details', {}, e('summary', { text: t('details') }), code(item))))];
  }
  function renderInstances(snapshot) {
    const rows = list(snapshot, 'instances');
    return [title('instances', 'instancesDesc', [refreshButton(), capability(snapshot, 'spawn') ? button('spawn', () => openInstanceTask(), 'primary', 'plus') : null]), filters(rows), table([
      { key: 'instances', render: row => rowName(row, 'instance', { agent_instance_id: row.id || row.agent_instance_id }) }, { key: 'status', render: row => badge(row.status) },
      { key: 'sessionState', render: row => badge(field(row, 'child_session_state')) }, { key: 'profile', render: row => field(row, 'agent_profile', t('unknown')) },
      { key: 'target', render: row => field(row, 'target_name', target(row)) || t('unknown') }, { key: 'actions', render: row => instanceActions(snapshot, row) }
    ], filtered(rows))];
  }
  function instanceActions(snapshot, row) {
    return e('div', { class: 'actions' }, button('open', () => navigate('instance', { agent_instance_id: row.id || row.agent_instance_id })), capability(snapshot, 'followup', row) ? button('followup', () => openInstanceTask(row)) : null, actionButton(snapshot, 'instances', 'interrupt', { agent_instance_id: row.id || row.agent_instance_id }, 'interrupt', row, true), ['send', 'steer'].map(action => capability(snapshot, action, row) ? button(action === 'steer' ? 'respond' : 'send', () => openInstanceMessage(row, action)) : null));
  }
  function renderInstance(snapshot) {
    const item = snapshot.instance; if (!item) return [title('instances'), empty('unavailable')];
    return [literalTitle(display(item), 'instancesDesc', [button('back', () => navigate('instances')), refreshButton()]), section('details', e('div', { class: 'panel-body' }, definition([['id', item.id], ['status', badge(item.status)], ['sessionState', badge(field(item, 'child_session_state'))], ['ownerSession', field(item, 'owner_session_id')], ['profile', field(item, 'agent_profile')], ['target', field(item, 'target_name', target(item))]]), instanceActions(snapshot, item), e('details', {}, e('summary', { text: t('details') }), code(item))))];
  }
  function renderSchedules(snapshot) {
    const rows = list(snapshot, 'schedules');
    return [title('schedules', 'schedulesDesc', [refreshButton(), capability(snapshot, 'create') ? button('newSchedule', () => openScheduleForm(), 'primary', 'plus') : null]), filters(rows), table([
      { key: 'schedules', render: row => rowName(row, 'schedule', { schedule_id: row.id }) }, { key: 'scheduleType', render: row => row.schedule_type === 'session-prompt' ? t('sessionPrompt') : row.schedule_type === 'subtask' ? t('subtask') : row.schedule_type || t('unknown') },
      { key: 'spec', render: row => e('code', { text: row.spec || '' }) }, { key: 'status', render: row => badge(row.enabled ? 'enabled' : 'paused') }, { key: 'nextRun', render: row => date(row.next_run_at) },
      { key: 'actions', render: row => scheduleActions(snapshot, row) }
    ], filtered(rows)), e('p', { class: 'note', text: t('schedulePauseNote') })];
  }
  function scheduleActions(snapshot, row) {
    const id = row.id || row.schedule_id;
    return e('div', { class: 'actions' }, button('open', () => navigate('schedule', { schedule_id: id })), capability(snapshot, 'update', row) ? button('edit', () => openScheduleForm(row)) : null,
      actionButton(snapshot, 'schedules', row.enabled ? 'disable' : 'enable', { schedule_id: id }, row.enabled ? 'disable' : 'enable', row, true), actionButton(snapshot, 'schedules', 'delete', { schedule_id: id }, 'delete', row, true));
  }
  function renderSchedule(snapshot) {
    const item = snapshot.schedule; if (!item) return [title('schedule'), empty('unavailable')];
    const history = list(snapshot, 'runs').length ? list(snapshot, 'runs') : list(item, 'runs');
    return [literalTitle(display(item), 'schedulesDesc', [button('back', () => navigate('schedules')), refreshButton()]),
      section('details', e('div', { class: 'panel-body' }, definition([['id', item.id], ['scheduleType', item.schedule_type], ['status', badge(item.enabled ? 'enabled' : 'paused')], ['spec', item.spec], ['target', item.target_name || item.target_id], ['sessionName', item.session_name], ['nextRun', date(item.next_run_at)], ['lastRun', date(item.last_run_at)], ['result', item.last_status || t('unknown')]]), item.prompt ? code(item.prompt) : null, scheduleActions(snapshot, item))),
      e('div', { class: 'stack spaced' }, section('history', table([{ key: 'firedAt', render: row => date(row.fired_at) }, { key: 'status', render: row => badge(row.status) }, { key: 'taskID', render: row => row.task_id ? e('button', { type: 'button', class: 'link-button', text: row.task_id, onclick: () => navigate('task', { task_id: row.task_id }) }) : t('unknown') }], history))), e('p', { class: 'note', text: t('schedulePauseNote') })];
  }
  function renderBots(snapshot) {
    const rows = list(snapshot, 'bots');
    return [title('bots', 'botsDesc', refreshButton()), filters(rows, 'no-state'), table([
      { key: 'bots', render: row => e('div', {}, e('strong', { text: display(row) }), e('p', { class: 'mono', text: row.profile || row.id || '' })) }, { key: 'status', render: row => badge(row.status) }, { key: 'version', render: row => String(row.version ?? t('unknown')) },
      { key: 'profile', render: row => row.settings && (row.settings.default_agent || row.settings.agent_profile || row.settings.profile) || t('unknown') }, { key: 'actions', render: row => e('div', { class: 'actions' }, button('open', () => openBotForm(row, false)), capability(snapshot, 'save', row) ? button('edit', () => openBotForm(row, true)) : null, button('groups', () => navigate('groups', { profile: row.profile || row.id }))) }
    ], filtered(rows))];
  }
  function renderGroups(snapshot) {
    const rows = list(snapshot, 'groups');
    return [title('groups', 'groupsDesc', refreshButton()), e('p', { class: 'rail group-note', text: t('knownBindings') }), filters(rows, 'no-state'), table([
      { key: 'groups', render: row => e('div', {}, e('strong', { text: row.name || row.chat_name || row.chat_id || row.id }), e('p', { class: 'mono', text: row.chat_id || row.id || '' })) },
      { key: 'bots', render: row => row.profile || snapshot.profile || t('unknown') }, { key: 'source', render: row => row.source || snapshot.source || t('unknown') },
      { key: 'actions', render: row => e('div', { class: 'actions' }, button('open', () => openGroupForm(row, snapshot))) }
    ], filtered(rows)), snapshot.settings && state.route.params.chat_id ? section('settings', e('div', { class: 'panel-body' }, definition([['source', snapshot.source], ['profile', snapshot.profile]]), code(snapshot.settings), capability(snapshot, 'save') ? button('edit', () => editGroupSettings(snapshot, snapshot.settings)) : null)) : null];
  }
  function renderProfiles(snapshot) {
    const rows = list(snapshot, 'profiles');
    const cards = filtered(rows).map(row => e('section', { class: 'panel profile-card' },
      e('div', { class: 'avatar', text: display(row).slice(0, 1) }), e('h2', { text: display(row) }),
      e('code', { class: 'muted', text: row.ref || row.profile || '' }), e('p', { text: row.description || '' }),
      definition([['source', row.namespace || row.pack || row.source], ['skills', Array.isArray(row.skills) ? row.skills.map(skill => typeof skill === 'string' ? skill : skill.name).join(' · ') : t('unknown')]]),
      e('footer', {}, e('span', { class: 'muted', text: row.version || row.revision || '' }), button('definition', () => navigate('profile', { ref: row.ref || row.profile || row.name })))));
    return [title('profiles', 'profilesDesc', refreshButton()), filters(rows, 'no-state'), e('div', { class: 'profile-grid' }, cards), !rows.length ? empty() : null];
  }
  function renderProfile(snapshot) {
    const item = snapshot.profile; if (!item) return [title('profiles'), empty('unavailable')];
    const skills = list(item, 'skills');
    return [literalTitle(display(item), 'profilesDesc', [button('back', () => navigate('profiles')), refreshButton()]),
      section('definition', e('div', { class: 'panel-body' }, definition([['profile', item.ref || item.profile || item.name], ['description', item.description], ['source', item.namespace || item.source || item.pack], ['version', item.version || item.revision]]),
        item.instructions || item.system_prompt || item.prompt ? code(item.instructions || item.system_prompt || item.prompt) : null,
        skills.length ? e('div', { class: 'stack' }, e('h3', { text: t('skills') }), skills.map(skill => typeof skill === 'string' ? e('div', { class: 'rail', text: skill }) : e('div', { class: 'rail' }, e('div', {}, e('strong', { text: skill.name || skill.ref || '' }), e('p', { class: 'help', text: skill.description || skill.source || '' }))))) : null,
        e('details', {}, e('summary', { text: t('advanced') }), code(item))))];
  }
  function renderPacks(snapshot) {
    const rows = list(snapshot, 'packs');
    return [title('packs', 'packsDesc', [refreshButton(), capability(snapshot, 'install') ? button('install', openInstallPack, 'primary', 'plus') : null]), filters(rows, 'no-state'), table([
      { key: 'packs', render: row => e('div', {}, e('strong', { text: row.namespace || row.name || row.id || '' }), e('p', { text: row.source_host || row.source_kind || row.url || row.source || '' })) }, { key: 'revision', render: row => row.revision || row.current_revision || row.ref || row.version || t('unknown') },
      { key: 'actions', render: row => e('div', { class: 'actions' }, button('definition', () => showObject('packs', row)), actionButton(snapshot, 'packs', 'sync', { namespace: row.namespace || row.name }, 'sync', row, true), capability(snapshot, 'rollback', row) ? button('rollback', () => openRollbackPack(row), 'danger') : null) }
    ], filtered(rows))];
  }
  function renderCredentials(snapshot) {
    const rows = list(snapshot, 'profiles').length ? list(snapshot, 'profiles') : list(snapshot, 'credentials');
    return [title('credentials', 'credentialsDesc', refreshButton()), e('div', { class: 'rail secret-note' }, icon('shield'), t('secretHidden')), filters(rows, 'no-state'), table([
      { key: 'credentialProfile', render: row => row.profile || row.name || '' }, { key: 'kind', render: row => row.kind || t('unknown') },
      { key: 'envNames', render: row => list(row, 'env_names').join(', ') || '—' }, { key: 'fileNames', render: row => list(row, 'file_names').join(', ') || '—' }
    ], filtered(rows))];
  }
  function renderSettings(snapshot) {
    const rows = list(snapshot, 'settings');
    const key = 'settings-draft:' + snapshot.version;
    const draft = state.drafts.get(key) || {};
    const dirty = () => Object.keys(draft).filter(k => String(rows.find(r => r.key === k).value) !== draft[k]);
    const save = button('save', async () => {
      const values = Object.fromEntries(dirty().map(k => [k, draft[k]]));
      if (!Object.keys(values).length) return;
      save.disabled = true;
      try { const response = await write('settings', 'save', { expected_version: snapshot.version, values }); state.drafts.delete(key); notify(t(response.restart_required ? 'restartRequired' : 'saved')); await loadRoute(); }
      catch (error) { notify(errorMessage(error, true), true); save.disabled = !state.online; }
    }, 'primary');
    const changeNote = e('span', { class: 'muted', text: dirty().length ? t('dirty') : '' });
    save.disabled = !state.online || !dirty().length;
    const panels = rows.map(row => {
      const control = capability(snapshot, 'save') && row.writable === true ? e('input', {
        id: 'setting-' + row.key, value: own(draft, row.key) ? draft[row.key] : String(row.value ?? ''),
        oninput: event => { draft[row.key] = event.target.value; state.drafts.set(key, draft); save.disabled = !state.online || !dirty().length; changeNote.textContent = dirty().length ? t('dirty') : ''; }
      }) : e('span', { text: String(row.value ?? '') });
      return e('section', { class: 'panel panel-body' }, e('div', { class: 'form-row' },
        e('label', { for: 'setting-' + row.key, class: 'mono', text: row.key }),
        e('div', { class: 'form-field' }, control, row.description ? e('p', { class: 'help', text: row.description }) : null, row.restart_required ? e('p', { class: 'help', text: t('restartRequired') }) : null)));
    });
    return [title('settings', 'settingsDesc', [refreshButton(), capability(snapshot, 'save') ? save : null]), changeNote,
      e('div', { class: 'stack' }, panels), !rows.length ? empty() : null];
  }
  // Dialogs retain drafts in memory only. No messages, credentials or tokens enter storage.
  function captureModal() {
    if (!state.modal) return null;
    const values = {};
    for (const input of document.querySelectorAll('#dialogs [name]')) values[input.name] = input.type === 'checkbox' ? input.checked : input.value;
    return Object.assign({}, state.modal, { values });
  }
  function restoreModal(modal) { if (modal && modal.rebuild) modal.rebuild(modal.values); }
  function closeModal(force) {
    const current = state.modal;
    if (current && current.pending && !force) return false;
    if (current && current.onClose) current.onClose();
    document.getElementById('dialogs').replaceChildren(); state.modal = null;
    if (current && current.previousFocus && current.previousFocus.isConnected) current.previousFocus.focus();
    return true;
  }
  function modal(key, build, options) {
    closeModal(true);
    const previousFocus = document.activeElement;
    const body = e('div', { class: 'modal-body' });
    const panel = e('section', { class: 'modal ' + (options && options.small ? 'small' : ''), role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'modal-title', tabindex: '-1' }, e('div', { class: 'modal-head' }, e('h2', { id: 'modal-title', text: t(key) }), iconButton('close', 'close', () => closeModal())), body);
    const backdrop = e('div', { class: 'backdrop' }, panel);
    const current = { key, previousFocus, pending: false, rebuild: options && options.rebuild };
    state.modal = current;
    panel.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); closeModal(); }
      if (event.key === 'Tab') {
        const focusable = [...panel.querySelectorAll('button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),a[href],[tabindex="0"]')].filter(n => !n.hidden && n.offsetParent !== null);
        if (!focusable.length) { event.preventDefault(); panel.focus(); return; }
        const first = focusable[0]; const last = focusable[focusable.length - 1];
        if (event.shiftKey && (document.activeElement === first || document.activeElement === panel)) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    });
    document.getElementById('dialogs').replaceChildren(backdrop); build(body, current);
    setTimeout(() => { if (state.modal === current) (body.querySelector('input,textarea,select,button') || panel).focus(); }, 0);
    return current;
  }
  function formField(name, key, type, value, options) {
    const opts = options || {}; const id = 'field-' + name;
    let input;
    if (opts.options) input = selectOptions(opts.options, value ?? '', () => {}, { id, name, required: opts.required });
    else input = e(type === 'textarea' ? 'textarea' : 'input', { id, name, type: type === 'textarea' ? null : type || 'text', value: value ?? '', required: opts.required, readonly: opts.readonly, autocomplete: 'off', spellcheck: 'false', rows: type === 'textarea' ? opts.rows || '5' : null, 'aria-describedby': opts.help ? id + '-help' : null });
    return e('div', { class: 'form-row' }, e('label', { for: id, text: t(key) + (opts.required ? ' *' : '') }), e('div', { class: 'form-field' }, input, opts.help ? e('p', { id: id + '-help', class: 'help', text: t(opts.help) }) : null));
  }
  function formDialog(key, fields, submit, options) {
    const opts = options || {};
    const draftKey = opts.draftKey || 'form:' + key + ':' + cacheKey();
    modal(key, (body, current) => {
      const errorHost = e('div', { class: 'modal-error banner bad', role: 'alert' });
      const save = e('button', { type: 'submit', class: 'ui-btn primary', text: t(opts.submitKey || 'save'), disabled: !state.online });
      const form = e('form', { class: 'form', onsubmit: async event => {
        event.preventDefault(); if (current.pending || !form.reportValidity()) return;
        const data = Object.fromEntries(new FormData(form));
        if (opts.confirm && !root.confirm(t('confirmDanger', { action: t(key), name: opts.confirm }))) return;
        current.pending = true; save.disabled = true; save.textContent = t('saving'); errorHost.replaceChildren();
        try {
          await submit(data); state.drafts.delete(draftKey); if (state.modal === current) closeModal(true); notify(t('saved')); await loadRoute();
        } catch (error) {
          if (state.modal === current) errorHost.textContent = errorMessage(error, true);
          else notify(errorMessage(error, true), true);
        } finally { current.pending = false; if (save.isConnected) { save.disabled = !state.online; save.textContent = t(opts.submitKey || 'save'); } }
      } }, errorHost, opts.help ? e('p', { class: 'help', text: t(opts.help) }) : null, fields, e('div', { class: 'form-actions' }, button('cancel', () => closeModal()), save));
      const draft = state.drafts.get(draftKey);
      if (draft) for (const input of form.querySelectorAll('[name]')) if (own(draft, input.name) && !input.readOnly) input.value = draft[input.name];
      const capture = () => state.drafts.set(draftKey, Object.fromEntries(new FormData(form)));
      form.addEventListener('input', capture); form.addEventListener('change', capture);
      body.appendChild(form);
    }, { rebuild: opts.rebuild });
  }
  function showObject(key, object) { modal(key, body => body.append(code(object), e('div', { class: 'form-actions' }, button('close', () => closeModal())))); }
  function confirmAction(action, name) {
    return new Promise(resolve => {
      modal('confirm', (body, current) => {
        body.appendChild(e('p', { text: t('confirmDanger', { action: t(action), name: name || t('unknown') }) }));
        const finish = value => { current.resolved = true; closeModal(true); resolve(value); };
        current.onClose = () => { if (!current.resolved) resolve(false); };
        body.appendChild(e('div', { class: 'form-actions' }, button('cancel', () => finish(false)), button('confirm', () => finish(true), 'danger')));
      }, { small: true });
    });
  }
  async function executionCatalog() {
    const value = await get('catalogs'); state.cache.set('catalogs?', value); return value;
  }
  async function openNewSession(values) {
    try {
      const catalog = await executionCatalog(); const profiles = await get('profiles');
      const p = values || {};
      const fields = [formField('name', 'name', 'text', p.name || '', { required: true }), formField('profile', 'profile', 'text', p.profile || '', { required: true, options: [{ value: '', label: t('inherit') }, ...list(profiles, 'profiles').map(row => ({ value: row.ref || row.profile || row.name, label: display(row) + ' · ' + (row.ref || row.profile || row.name) }))] }),
        formField('target', 'target', 'text', p.target || '', { options: [{ value: '', label: t('inherit') }, ...list(catalog, 'targets').map(row => ({ value: row.id, label: row.name || row.id }))] }),
        formField('runtime', 'agentType', 'text', p.runtime || '', { options: [{ value: '', label: t('inherit') }, ...list(catalog, 'runtimes').map(row => ({ value: row.name || row.agent_type, label: row.display_name || row.name || row.agent_type }))] }),
        formField('model', 'model', 'text', p.model || '', { help: 'fieldUnsupported' }), formField('cwd', 'cwd', 'text', p.cwd || ''), formField('prompt', 'prompt', 'textarea', p.prompt || '')];
      formDialog('newSession', fields, async data => {
        const prompt = data.prompt; delete data.prompt;
        const response = await write('sessions', 'create', data);
        const session = response.session || response;
        if (prompt && session.name) {
          try { await write('sessions', 'submit', Object.assign(sessionRef(session), { text: prompt })); }
          catch (error) { state.drafts.set('composer:' + JSON.stringify(sessionRef(session)), prompt); notify(errorMessage(error, true), true); }
        }
        if (session.name) navigate('session', sessionRef(session));
      }, { submitKey: 'create', rebuild: openNewSession });
      wireModelOptions(catalog, p.model);
    } catch (error) { notify(errorMessage(error), true); }
  }
  function wireModelOptions(catalog, previous) {
    const runtime = document.querySelector('#dialogs [name=runtime]'); const model = document.querySelector('#dialogs [name=model]'); if (!runtime || !model) return;
    const datalist = e('datalist', { id: 'model-options' }); model.setAttribute('list', 'model-options'); model.after(datalist);
    const update = () => {
      const selected = list(catalog, 'runtimes').filter(row => !runtime.value || (row.name || row.agent_type) === runtime.value);
      replace(datalist, selected.flatMap(row => list(row, 'models')).map(row => e('option', { value: row.ref, label: row.display_name || row.ref })));
    };
    runtime.addEventListener('change', () => { model.value = ''; update(); }); update(); if (previous) model.value = previous;
  }
  function openInstanceMessage(row, action) {
    formDialog('send', [formField('text', 'text', 'textarea', '', { required: true })], data => write('instances', action, { agent_instance_id: row.id || row.agent_instance_id, text: data.text }), { submitKey: 'send' });
  }
  function taskFields(task) {
    const value = task || {};
    return [formField('task_agent', 'taskProfile', 'text', value.agent || '', { required: true }), formField('task_text', 'taskInstruction', 'textarea', value.task || '', { required: true }), formField('runtime', 'agentType', 'text', value.runtime || ''), formField('model', 'model', 'text', value.model || ''), formField('reasoning_effort', 'reasoningEffort', 'text', value.reasoning_effort || '')];
  }
  function taskData(data, initial) {
    const task = Object.assign({}, initial || {}, { agent: data.task_agent, task: data.task_text });
    for (const key of ['runtime', 'model', 'reasoning_effort']) if (data[key]) task[key] = data[key]; else delete task[key];
    return task;
  }
  async function openInstanceTask(item, values) {
    const initial = values || {};
    const task = { agent: initial.task_agent || item && item.agent_profile || '', task: initial.task_text || '', runtime: initial.runtime || item && item.agent_type || '', model: initial.model || item && item.model_ref || '', reasoning_effort: initial.reasoning_effort || '' };
    const advanced = e('details', {}, e('summary', { text: t('advanced') }), formField('run_as', 'principal', 'text', initial.run_as || ''), formField('credential_profile', 'credentialProfile', 'text', initial.credential_profile || ''), formField('permission_policy', 'permission', 'text', initial.permission_policy || item && item.permission_policy || '', { options: ['', 'policy-allow', 'deny-all', 'escalate-to-main'].map(value => ({ value, label: value || t('inherit') })) }), formField('isolation', 'isolationRequired', 'text', initial.isolation || '', { options: ['', 'process', 'private-home'].map(value => ({ value, label: value || t('inherit') })) }), formField('timeout_seconds', 'timeout', 'number', initial.timeout_seconds || ''), formField('workspace', 'cwd', 'textarea', initial.workspace || '', { help: 'jsonHelp' }), formField('extra_args', 'nativeArgs', 'textarea', initial.extra_args || ''));
    formDialog(item ? 'followup' : 'spawn', [...taskFields(task), advanced], async data => {
      const payload = taskData(data);
      for (const key of ['run_as', 'credential_profile', 'permission_policy', 'isolation']) if (data[key]) payload[key] = data[key];
      if (data.timeout_seconds) { payload.timeout_seconds = Number(data.timeout_seconds); if (!Number.isInteger(payload.timeout_seconds) || payload.timeout_seconds < 0) throw new APIError(t('invalidField', { field: t('timeout') })); }
      if (data.workspace) payload.workspace = safeSettings(data.workspace);
      if (data.extra_args) payload.extra_args = data.extra_args.split('\n').filter(Boolean);
      if (item) payload.agent_instance_id = item.id || item.agent_instance_id;
      await write('instances', item ? 'followup' : 'spawn', payload);
    }, { submitKey: 'create', draftKey: 'instance-task:' + (item ? item.id : 'new'), rebuild: saved => openInstanceTask(item, saved) });
  }
  async function openScheduleForm(item, values) {
    try {
      const edit = Boolean(item);
      if (edit && item.schedule_type === 'subtask' && !item.task) {
        const detail = await get('schedule', { schedule_id: item.id || item.schedule_id });
        item = Object.assign({}, detail.schedule || item, { task: detail.task });
      }
      const p = Object.assign({}, item || {}, values || {});
      const task = Object.assign({}, item && item.task || {}, values ? { agent: values.task_agent, task: values.task_text, runtime: values.runtime, model: values.model, reasoning_effort: values.reasoning_effort } : {});
      const fields = [formField('name', 'name', 'text', p.name || ''), formField('spec', 'spec', 'text', p.spec || '', { required: true, help: 'specHelp' })];
      if (!edit) fields.unshift(formField('schedule_type', 'scheduleType', 'text', p.schedule_type || 'subtask', { options: [{ value: 'subtask', label: t('subtask') }, { value: 'session-prompt', label: t('sessionPrompt') }] }));
      if (!edit) fields.push(e('div', { id: 'session-schedule-fields', class: 'form' }, formField('target_id', 'target', 'text', p.target_id || ''), formField('session_name', 'sessionName', 'text', p.session_name || ''), formField('prompt', 'prompt', 'textarea', p.prompt || ''), formField('run_mode', 'runMode', 'text', p.run_mode || 'reuse-session', { options: ['reuse-session', 'new-session-per-run'].map(value => ({ value, label: value })) })));
      if (!edit || item.schedule_type === 'subtask') fields.push(e('div', { id: 'task-schedule-fields', class: 'form' }, taskFields(task)));
      formDialog(edit ? 'edit' : 'newSchedule', fields, async data => {
        const payload = { name: data.name, spec: data.spec };
        if (edit) payload.schedule_id = item.id || item.schedule_id;
        else payload.schedule_type = data.schedule_type;
        if ((!edit && data.schedule_type === 'subtask') || edit && item.schedule_type === 'subtask') {
          payload.task = taskData(data, item && item.task);
          if (!payload.task.agent || !payload.task.task) throw new APIError(t('required'));
        } else if (!edit) {
          for (const key of ['target_id', 'session_name', 'prompt', 'run_mode']) payload[key] = data[key];
          if (!payload.target_id || !payload.session_name || !payload.prompt) throw new APIError(t('required'));
        }
        await write('schedules', edit ? 'update' : 'create', payload);
      }, { submitKey: edit ? 'save' : 'create', draftKey: 'schedule:' + (item ? item.id : 'new'), rebuild: saved => openScheduleForm(item, saved) });
      if (!edit) {
        const type = document.querySelector('#dialogs [name=schedule_type]');
        const update = () => {
          const child = type.value === 'subtask';
          document.getElementById('session-schedule-fields').hidden = child;
          document.getElementById('task-schedule-fields').hidden = !child;
          document.querySelectorAll('#task-schedule-fields [required]').forEach(input => { input.disabled = !child; });
        }; type.addEventListener('change', update); update();
      }
    } catch (error) { notify(errorMessage(error), true); }
  }
  function safeSettings(value) {
    let object;
    try { object = JSON.parse(value); } catch (_) { throw new APIError(t('invalidJSON')); }
    if (!object || Array.isArray(object) || typeof object !== 'object') throw new APIError(t('invalidJSON'));
    return object;
  }
  const botFieldDefinitions = [
    ['default_agent', 'profile'], ['default_runtime_ref', 'agentType'], ['default_model_ref', 'model'], ['default_reasoning_effort', 'reasoningEffort'],
    ['interaction_mode', 'interactionMode', ['thread', 'singleton']], ['isolation_required', 'isolationRequired', 'boolean']
  ];
  function editableSettingFields(settings, definitions) {
    return definitions.map(([key, label, type]) => formField(key, label, type === 'number' ? 'number' : 'text', type === 'boolean' ? String(Boolean(settings[key])) : settings[key] ?? '', {
      options: type === 'boolean' ? [{ value: 'true', label: t('booleanTrue') }, { value: 'false', label: t('booleanFalse') }] : Array.isArray(type) ? type.map(value => ({ value, label: labelState(value) })) : null
    }));
  }
  function readSettingFields(data, definitions) {
    const result = {};
    for (const [key, label, type] of definitions) {
      if (type === 'boolean') result[key] = data[key] === 'true';
      else if (type === 'number') {
        const number = Number(data[key]);
        if (!Number.isFinite(number) || !Number.isInteger(number) || number < 0) throw new APIError(t('invalidField', { field: t(label) }));
        result[key] = number;
      } else result[key] = String(data[key] ?? '');
    }
    return result;
  }
  async function saveBotTransition(payload) {
    let response = await write('bots', 'save', payload);
    for (let round = 0; round < 3 && response.status !== 'success'; round++) {
      if (!['needs_confirmation', 'needs_recovery_choice'].includes(response.status)) throw new APIError(t('invalidResponse'));
      const impact = typeof response.impact === 'object' ? response.impact[language()] || response.impact.en || '' : response.impact || '';
      const recovery = response.status === 'needs_recovery_choice';
      if (!root.confirm(t(recovery ? 'recoveryChoice' : 'confirmImpact', { impact }))) throw new APIError(t('operationCancelled'));
      if (!response.confirmation_token) throw new APIError(t('confirmRequired'));
      const next = Object.assign({}, payload, { confirmed: true, confirmation_token: response.confirmation_token });
      if (recovery) { next.recovery_action = 'create_blank'; next.recovery_attempt_id = response.recovery_attempt_id || response.confirmation_token; }
      response = await write('bots', 'save', next);
    }
    if (response.status !== 'success') throw new APIError(t('confirmRequired'));
    return response;
  }
  function openBotForm(bot, editable, values) {
    if (!editable) {
      modal('bots', body => body.append(definition([['profile', bot.profile], ['version', bot.version], ...botFieldDefinitions.map(([key, label]) => [label, typeof (bot.settings || {})[key] === 'boolean' ? t(bot.settings[key] ? 'booleanTrue' : 'booleanFalse') : (bot.settings || {})[key]])]), e('div', { class: 'form-actions' }, button('close', () => closeModal()))));
      return;
    }
    const initial = Object.assign({}, bot.settings || {}, values || {});
    const requestID = root.crypto.randomUUID();
    formDialog('bots', [formField('profile', 'profile', 'text', bot.profile || bot.id, { readonly: true }), formField('expected_version', 'expectedVersion', 'text', bot.version ?? '', { readonly: true }), ...editableSettingFields(initial, botFieldDefinitions)], data => saveBotTransition({ profile: bot.profile || bot.id, expected_version: bot.version, settings: readSettingFields(data, botFieldDefinitions), request_id: requestID }), { help: 'appliesNew', draftKey: 'bot:' + bot.profile + ':' + bot.version, rebuild: saved => openBotForm(bot, editable, saved) });
  }
  async function openGroupForm(row, snapshot) {
    try {
      const profile = row.profile || snapshot.profile || state.route.params.profile;
      const chatID = row.chat_id || row.id;
      const detail = await get('groups', { profile, chat_id: chatID });
      if (capability(detail, 'save') || capability(snapshot, 'save', row)) editGroupSettings(Object.assign({}, detail, { profile, chat_id: chatID }), detail.settings || row.settings || {});
      else showObject('groups', detail);
    } catch (error) { notify(errorMessage(error), true); }
  }
  function editGroupSettings(snapshot, settings, values) {
    const groupDefinitions = [...botFieldDefinitions, ['mode', 'governance', ['owner_only', 'shared']], ['thinking_process', 'thinkingProcess', ['collapsed', 'expanded']], ['default_target', 'defaultTarget'], ['default_cwd', 'cwd'], ['idle_timeout_seconds', 'idleTimeout', 'number'], ['thread_context_limit', 'threadContextLimit', 'number'], ['max_sessions_total', 'maxSessions', 'number'], ['max_sessions_per_user', 'maxSessionsPerUser', 'number']];
    const initial = Object.assign({}, settings, values || {});
    const advanced = e('details', {}, e('summary', { text: t('advanced') }), ...editableSettingFields(initial, groupDefinitions.slice(6)), formField('default_args', 'nativeArgs', 'textarea', values && values.default_args !== undefined ? values.default_args : list(settings, 'default_args').join('\n')));
    formDialog('groups', [formField('profile', 'profile', 'text', snapshot.profile, { readonly: true }), formField('chat_id', 'chatID', 'text', snapshot.chat_id || state.route.params.chat_id, { readonly: true }), e('p', { class: 'rail', text: t('source') + ': ' + labelState(snapshot.settings_source === 'bot_defaults' ? 'botDefaults' : 'savedSettings') }), ...editableSettingFields(initial, groupDefinitions.slice(0, 6)), advanced], data => {
      const next = Object.assign({}, settings, readSettingFields(data, groupDefinitions), { default_args: String(data.default_args || '').split('\n').filter(Boolean) });
      return write('groups', 'save', { profile: snapshot.profile, chat_id: snapshot.chat_id || state.route.params.chat_id, expected_version: settings.version || 0, settings: next });
    }, { draftKey: 'group:' + snapshot.profile + ':' + (snapshot.chat_id || state.route.params.chat_id) + ':' + settings.version, rebuild: saved => editGroupSettings(snapshot, settings, saved) });
  }
  function openInstallPack() {
    formDialog('install', [formField('namespace', 'namespace', 'text', '', { required: true }), formField('url', 'source', 'text', '', { required: true }), formField('ref', 'revision', 'text', '')], data => write('packs', 'install', Object.assign(data, { confirm: true })), { submitKey: 'install', confirm: t('packs') });
  }
  function openRollbackPack(pack) {
    const revisions = list(pack, 'versions').length ? list(pack, 'versions') : list(pack, 'revisions');
    formDialog('rollback', [formField('namespace', 'namespace', 'text', pack.namespace || pack.name, { readonly: true }), formField('revision', 'revision', 'text', '', { required: true, options: revisions.length ? [{ value: '', label: t('unknown') }, ...revisions.map(r => ({ value: typeof r === 'string' ? r : r.revision || r.id, label: typeof r === 'string' ? r : r.revision || r.id }))] : null })], data => write('packs', 'rollback', Object.assign(data, { confirm: true })), { submitKey: 'rollback', confirm: pack.namespace || pack.name });
  }
  async function openSearch() {
    const resources = ['sessions', 'tasks', 'instances', 'schedules', 'bots', 'groups', 'profiles', 'packs'];
    let records = []; let current = null; let query = '';
    modal('searchTitle', (body, modalState) => {
      current = modalState;
      const resultHost = e('div', { class: 'search-results' });
      const errors = e('div');
      const input = e('input', { type: 'search', placeholder: t('search'), 'aria-label': t('search'), class: 'full-width', oninput: event => { query = event.target.value; draw(); } });
      function draw() {
        const matches = records.filter(record => !query || JSON.stringify(record.item).toLocaleLowerCase().includes(query.toLocaleLowerCase())).slice(0, 100);
        replace(resultHost, matches.length ? matches.map(({ resource, item }) => e('button', { type: 'button', class: 'search-result', onclick: () => {
          closeModal();
          const map = { sessions: ['session', sessionRef(item)], tasks: ['task', { task_id: item.task_id || item.id }], instances: ['instance', { agent_instance_id: item.id || item.agent_instance_id }], schedules: ['schedule', { schedule_id: item.id }], profiles: ['profile', { ref: item.ref || item.profile || item.name }] };
          if (map[resource]) navigate(...map[resource]); else { filterState(resource).query = String(ident(item)); navigate(resource); }
        } }, icon(resource), e('div', {}, e('strong', { text: display(item) }), e('small', { text: t(resource) + ' · ' + ident(item) })))) : empty('noResults'));
      }
      body.append(input, e('p', { class: 'note', text: t('searchHint') }), errors, resultHost); resultHost.appendChild(e('p', { text: t('loading') }));
      Promise.allSettled(resources.map(async resource => ({ resource, snapshot: await get(resource) }))).then(results => {
        if (state.modal !== current || !state.authenticated) return;
        records = results.filter(r => r.status === 'fulfilled').flatMap(r => list(r.value.snapshot, r.value.resource).map(item => ({ resource: r.value.resource, item })));
        const failures = results.map((r, i) => r.status === 'rejected' ? resources[i] + ': ' + r.reason.message : '').filter(Boolean);
        errors.replaceChildren(failures.length ? e('div', { class: 'banner warn', text: t('partial') + '\n' + failures.join('\n') }) : e('p', { class: 'note', text: t('searchScope', { count: results.filter(r => r.status === 'fulfilled').length }) })); draw();
      });
    });
  }
  function authNotices() {
    const config = state.authConfig;
    if (!config) return [];
    return [config.expires_at > 0 ? e('div', { class: 'banner', role: 'status', text: t('consoleExpiry', { time: date(config.expires_at) }) }) : null,
      config.insecure_transport ? e('div', { class: 'banner warn', role: 'note', text: t('insecureTransportWarning') }) : null];
  }
  function loginFailure(error) {
    const machineCode = error.payload && (error.payload.code || error.payload.error);
    state.loginErrorKey = windowExpired() ? 'console_expired' : typeof machineCode === 'string' && own(I18n.messages, machineCode) ? machineCode : error.status === 401 ? 'consoleClosedOrExpired' : 'loginFailed';
    state.loginError = '';
  }
  function renderLogin(app) {
    closeStream();
    const passwordMode = state.authConfig && state.authConfig.mode === 'password';
    let content;
    if (state.authConfigLoading) content = e('p', { role: 'status', text: t('loadingAuthConfig') });
    else if (state.authConfigError) content = e('div', {}, e('p', { class: 'banner bad', role: 'alert', text: t(state.authConfigError) }), button('retry', retryAuthentication));
    else if (windowExpired()) content = e('p', { class: 'banner bad', role: 'alert', text: t('console_expired') });
    else if (!state.authConfig) content = e('p', { role: 'status', text: t('loadingAuthConfig') });
    else {
      const label = passwordMode ? 'temporaryPassword' : 'pairCode';
      const inputID = passwordMode ? 'temporary-password' : 'pair-code';
      const input = e('input', { id: inputID, type: passwordMode ? 'password' : 'text', name: passwordMode ? 'password' : 'code', autocomplete: passwordMode ? 'off' : 'one-time-code', required: true, spellcheck: 'false', autocapitalize: 'off', 'aria-label': t(label) });
      const errors = e('div', { class: 'modal-error', role: 'alert', text: state.loginErrorKey ? t(state.loginErrorKey) : state.loginError || '' });
      const submit = e('button', { type: 'submit', class: 'ui-btn primary', text: state.pairing ? t('loading') : t(passwordMode ? 'signIn' : 'pair'), disabled: Boolean(state.pairing) });
      content = e('form', { class: 'form', autocomplete: passwordMode ? 'off' : null, onsubmit: async event => {
        event.preventDefault(); let value = passwordMode ? input.value : input.value.trim(); if (!value || state.pairing) return;
        input.value = ''; submit.disabled = true;
        const pending = authenticate(passwordMode ? 'password' : 'pair', value); value = ''; await pending;
      } }, e('div', { class: 'form-row' }, e('label', { for: inputID, text: t(label) }), input), errors, submit);
    }
    app.replaceChildren(e('main', { class: 'login-page' }, e('section', { class: 'panel login-card' }, e('div', { class: 'app-logo' }, icon('tasks', 28), 'amux', e('small', { text: 'Console' })), e('h1', { text: t('pairTitle') }), state.authConfig && !windowExpired() ? e('p', { text: t(passwordMode ? 'temporaryPasswordHelp' : 'pairHelp') }) : null, e('p', { class: 'rail operator-notice', text: t('operatorNotice') }), authNotices(), content, e('div', { class: 'login-preferences' }, preferenceControls()))));
  }
  async function authenticate(mode, secret) {
    state.pairing = true; state.loginErrorKey = ''; state.loginError = ''; render();
    try {
      const pending = request(mode === 'password' ? '/auth/login' : '/auth/pair', { method: 'POST', body: JSON.stringify(mode === 'password' ? { password: secret } : { code: secret }) });
      secret = ''; await pending; await bootstrap();
    } catch (error) { loginFailure(error); }
    finally { state.pairing = false; if (!state.authenticated) render(); }
  }
  async function loadAuthConfig() {
    state.authConfigLoading = true; state.authConfigError = ''; state.authConfig = null;
    root.clearTimeout(state.expiryTimer); state.expiryTimer = null; render();
    try {
      const config = await request('/auth/config');
      if (!['password', 'pair'].includes(config.mode) || !Number.isFinite(config.expires_at) || config.expires_at < 0 || config.mode === 'password' && config.expires_at === 0 || typeof config.insecure_transport !== 'boolean') throw new APIError(t('invalidResponse'));
      state.authConfig = { mode: config.mode, expires_at: config.expires_at, insecure_transport: config.insecure_transport };
      state.consoleExpired = false; state.loginErrorKey = ''; state.loginError = ''; scheduleExpiry();
      return !windowExpired();
    } catch (error) {
      if (error.payload && (error.payload.code || error.payload.error) === 'console_expired') { state.consoleExpired = true; state.loginErrorKey = 'console_expired'; }
      else state.authConfigError = 'authConfigUnavailable';
      return false;
    } finally { state.authConfigLoading = false; if (!state.authenticated) render(); }
  }
  async function retryAuthentication() {
    if (!await loadAuthConfig()) return;
    try { await bootstrap(); } catch (error) { if (error.status !== 401 || state.consoleExpired) loginFailure(error); render(); }
  }
  async function bootstrap() {
    const response = await get('bootstrap');
    if (!response.authenticated) { expire(false, 'consoleClosedOrExpired'); throw new APIError(t('loginExpired'), 401); }
    if (state.authConfig) {
      if (['password', 'pair'].includes(response.auth_mode)) state.authConfig.mode = response.auth_mode;
      if (typeof response.insecure_transport === 'boolean') state.authConfig.insecure_transport = response.insecure_transport;
      if (Number.isFinite(response.expires_at) && response.expires_at > 0) state.authConfig.expires_at = state.authConfig.expires_at > 0 ? Math.min(state.authConfig.expires_at, response.expires_at) : response.expires_at;
      scheduleExpiry();
    }
    if (windowExpired()) throw new APIError(t('console_expired'), 401, { code: 'console_expired' });
    state.bootstrap = response; state.authenticated = true; state.loginErrorKey = ''; state.loginError = ''; await loadRoute();
  }
  async function logout() {
    if (!await confirmAction('logout', 'amux Console')) return;
    try { await request('/auth/logout', { method: 'POST', body: '{}' }); expire(); }
    catch (error) { notify(errorMessage(error, true), true); }
  }
  async function start() {
    state.language = ['auto', 'en', 'zh-CN'].includes(storageGet('language')) ? storageGet('language') : 'auto';
    state.theme = ['auto', 'light', 'dark'].includes(storageGet('theme')) ? storageGet('theme') : 'auto';
    // A pairing secret is removed synchronously, before any asynchronous work or request.
    let pairing = '';
    const fragment = new URLSearchParams(root.location.hash.replace(/^#/, ''));
    if (fragment.has('pair')) { pairing = fragment.get('pair') || ''; root.history.replaceState(null, '', root.location.pathname + root.location.search); state.route = routeFromHash(''); }
    applyPreferences(); render();
    root.addEventListener('hashchange', () => { state.route = routeFromHash(root.location.hash); state.navOpen = false; closeModal(true); if (state.authenticated) loadRoute(); });
    root.addEventListener('offline', () => { state.online = false; state.connection = 'offline'; updateConnection(); });
    root.addEventListener('online', () => { state.online = true; state.connection = 'reconnecting'; updateConnection(); if (state.authenticated) loadRoute(); });
    root.addEventListener('pagehide', closeStream);
    root.addEventListener('pageshow', event => { if (event.persisted && state.authenticated) loadRoute(); });
    root.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (state.theme === 'auto') applyPreferences(); });
    document.addEventListener('keydown', event => { if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && state.authenticated) { event.preventDefault(); openSearch(); } });
    if (!await loadAuthConfig()) { pairing = ''; return; }
    try {
      if (pairing) { const pending = authenticate('pair', pairing); pairing = ''; await pending; }
      else await bootstrap();
    } catch (error) { if (error.status !== 401 || state.consoleExpired) loginFailure(error); render(); }
  }
  const api = { start, state, field, list, decodeBase64, decodeJSON, normalizeEvent, normalizePermission, deriveCounts, supports, routeFromHash, ingestEvent, request, write, navigate, loadRoute, render, renderPage, openNewSession, openScheduleForm, openBotForm, openSearch, closeModal, timelineFor, e };
  root.ConsoleApp = api;
  if (typeof module !== 'undefined') module.exports = api;
  if (typeof document !== 'undefined' && !root.AMUX_CONSOLE_TEST) api.ready = start();
})(typeof window === 'undefined' ? globalThis : window);
