/* amux Console: dependency-free, same-origin operator client. No user content is HTML. */
(function (root) {
  'use strict';
  const I18n = root.ConsoleI18n;
  const own = (o, k) => Object.prototype.hasOwnProperty.call(o || {}, k);

  // ---------------------------------------------------------------------------
  // Transport normalization. The server keeps established snake_case shapes;
  // camelCase variants are accepted so protobuf JSON and Go maps both work.
  // ---------------------------------------------------------------------------
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
        origin: canonical.origin || null, turn: canonical.turn || null,
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
      permission, state: event.state || '', body: decodeJSON(field(event, 'structured_json')) || decodeJSON(field(event, 'usage_json')), at: '', origin: null, turn: null, historical: Boolean(event.historical), raw: event
    };
  }
  function supports(snapshot, action, item) {
    if (item && Array.isArray(item.actions)) return item.actions.includes(action);
    if (snapshot && Array.isArray(snapshot.actions)) return snapshot.actions.includes(action);
    return Boolean(snapshot && snapshot.capabilities && snapshot.capabilities[action] === true);
  }
  const terminalTaskStates = ['succeeded', 'failed', 'cancelled', 'canceled', 'timed_out'];
  const isTerminalTask = status => terminalTaskStates.includes(status);
  const isArchived = s => s.state === 'archived';
  const isWorking = s => Boolean(field(s, 'active_turn', false)) || ['running', 'in_progress'].includes(field(s, 'turn_state'));
  const needsAttention = s => Number(field(s, 'pending_permissions', 0)) > 0 || ['waiting_permission', 'failed', 'error'].includes(field(s, 'turn_state'));
  // Turn facts only count for unarchived sessions; an archived failure is history.
  function deriveCounts(sessions) {
    const current = sessions.filter(s => !isArchived(s));
    return {
      active: current.length,
      running: sessions.filter(s => s.state === 'running').length,
      sleeping: sessions.filter(s => s.state === 'sleeping').length,
      archived: sessions.length - current.length,
      // Each Session sits in one bucket: a Turn waiting on the owner counts as
      // needing attention, not as working, so totals match the lists they head.
      working: current.filter(s => isWorking(s) && !needsAttention(s)).length,
      attention: current.filter(needsAttention).length
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

  // ---------------------------------------------------------------------------
  // State and static presentation tables.
  // ---------------------------------------------------------------------------
  const state = {
    language: 'auto', theme: 'auto', bootstrap: null, authenticated: false, loading: false,
    authConfig: null, authConfigLoading: false, authConfigError: '', consoleExpired: false,
    route: routeFromHash(root.location && root.location.hash), cache: new Map(), filters: new Map(), drafts: new Map(), scroll: new Map(),
    online: !root.navigator || root.navigator.onLine !== false, connection: 'connected', lastSync: null,
    routeError: null, busy: false, generation: 0, timeline: new Map(), streams: null, modal: null, navOpen: false,
    expiryTimer: null, tickTimer: null
  };
  const navGroups = [['operations', ['overview', 'sessions', 'tasks', 'instances', 'schedules']], ['resources', ['bots', 'groups', 'profiles', 'packs', 'credentials']], ['system', ['settings']]];
  const pageKeys = { task: 'tasks', instance: 'instances', profile: 'profiles', session: 'sessions', schedule: 'schedules' };
  const detailKeys = { task: 'taskDetail', instance: 'instanceDetail', profile: 'profileDetail', session: 'session', schedule: 'schedule' };
  const icons = {
    overview: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
    sessions: 'M4 4h16v12H9l-5 4z M8 8h8 M8 12h5', tasks: 'm12 3 10 5-10 5L2 8z M3 13l9 5 9-5 M3 18l9 5 9-5',
    instances: 'M8 3h8v7H8z M3 16h7v5H3z M14 16h7v5h-7z M12 10v3 M6 16v-3h12v3',
    schedules: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M12 7v6l4 2',
    bots: 'M5 7h14a2 2 0 0 1 2 2v10H3V9a2 2 0 0 1 2-2 M12 7V3 M9 3h6 M8 12v3 M16 12v3',
    groups: 'M8 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6 M2 20v-1a6 6 0 0 1 12 0v1 M16 11a3 3 0 1 0 0-6 M18 20v-1a6 6 0 0 0-3-5.2',
    profiles: 'M4 3h16v18H4z M8 8h8 M8 12h8 M8 16h5', packs: 'm12 3 9 5v9l-9 5-9-5V8z M3 8l9 5 9-5 M12 13v9',
    credentials: 'M13 8a5 5 0 1 0-5 5 M12 12l8 8 M16 16l3-3 M18 18l3-3',
    settings: 'M4 6h16 M4 12h16 M4 18h16 M9 4v4 M15 10v4 M8 16v4',
    search: 'M10 4a6 6 0 1 0 0 12 6 6 0 0 0 0-12 M15 15l6 6', menu: 'M4 6h16 M4 12h16 M4 18h16',
    refresh: 'M20 7v5h-5 M4 17v-5h5 M6 7a7 7 0 0 1 12-2l2 3 M4 16l2 3a7 7 0 0 0 12-2',
    close: 'm5 5 14 14 M5 19 19 5', plus: 'M12 5v14 M5 12h14', alert: 'm12 3 10 18H2z M12 8v5 M12 17v1',
    shield: 'm12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6z M8 12l3 3 5-6', user: 'M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M4 22v-3a8 8 0 0 1 16 0v3',
    tool: 'm4 6 6 6-6 6 M13 18h7', logout: 'M9 3H3v18h6 M9 12h12 M16 7l5 5-5 5', arrow: 'M5 12h14 M14 7l5 5-5 5',
    globe: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M3 12h18 M12 3a14 14 0 0 1 0 18 M12 3a14 14 0 0 0 0 18',
    contrast: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M12 3v18', chevronLeft: 'm15 6-6 6 6 6', chevronRight: 'm9 6 6 6-6 6',
    check: 'm5 12 5 5 9-10', circle: 'M12 6a6 6 0 1 0 0 12 6 6 0 0 0 0-12', half: 'M12 6a6 6 0 1 0 0 12 6 6 0 0 0 0-12 M12 6v12',
    thinking: 'M9 18h6 M10 21h4 M12 3a6 6 0 0 0-4 10.5V16h8v-2.5A6 6 0 0 0 12 3'
  };
  // Status values come from many backends; each maps to one visual tone.
  const tones = {
    good: ['success', 'succeeded', 'completed', 'connected', 'enabled', 'ok', 'running_ok'],
    warn: ['waiting_permission', 'pending', 'waiting', 'reconnecting', 'paused', 'skipped', 'submitting', 'interrupted', 'cancelling'],
    bad: ['error', 'failed', 'offline', 'timed_out'],
    blue: ['running', 'in_progress', 'active', 'scheduled', 'reserving', 'preparing', 'collecting', 'reconciling', 'queued']
  };
  const PAGE_SIZE = 50;

  // ---------------------------------------------------------------------------
  // Formatting and DOM helpers. Every value is inserted as text.
  // ---------------------------------------------------------------------------
  function language() { return I18n.normalize(state.language === 'auto' ? root.navigator.language : state.language); }
  const t = (key, params) => I18n.t(key, language(), params);
  const has = key => own(I18n.messages, key);
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
  const dash = () => e('span', { class: 'muted', text: '—' });
  function stateLabel(value) {
    const text = value === undefined || value === null ? '' : String(value);
    if (!text) return t('unknown');
    return has('state.' + text) ? t('state.' + text) : text;
  }
  function toneOf(value) {
    for (const [tone, values] of Object.entries(tones)) if (values.includes(value)) return tone;
    return '';
  }
  function badge(value) {
    if (value === undefined || value === null || value === '') return dash();
    return e('span', { class: 'badge ' + toneOf(String(value)) }, e('span', { class: 'dot', 'aria-hidden': true }), stateLabel(value));
  }
  function chip(text, tone, title) { return e('span', { class: 'chip ' + (tone || ''), title: title || null, text }); }
  function optionLabel(value) { return has(String(value)) ? t(String(value)) : String(value); }
  function pretty(value) { return typeof value === 'string' ? value : JSON.stringify(value, null, 2); }
  function code(value) { return e('pre', { class: 'code', text: pretty(value) ?? '' }); }
  function toDate(value) {
    if (value === undefined || value === null || value === '' || value === 0 || value === '0') return null;
    const number = Number(value);
    const d = new Date(Number.isFinite(number) ? (number < 1e12 ? number * 1000 : number) : value);
    // Go zero times ("0001-01-01T00:00:00Z") mean "not set".
    return Number.isNaN(d.valueOf()) || d.getUTCFullYear() < 2000 ? null : d;
  }
  function date(value) {
    const d = toDate(value);
    return d ? new Intl.DateTimeFormat(language(), { dateStyle: 'medium', timeStyle: 'short' }).format(d) : t('unknown');
  }
  function clockTime(value) {
    const d = toDate(value);
    return d ? new Intl.DateTimeFormat(language(), { hour: '2-digit', minute: '2-digit' }).format(d) : '';
  }
  function relative(value) {
    const d = toDate(value);
    if (!d) return '—';
    const seconds = Math.round((d.valueOf() - Date.now()) / 1000);
    const abs = Math.abs(seconds);
    if (abs < 45) return t(seconds <= 0 ? 'justNow' : 'inMoments');
    for (const [unit, size, limit] of [['minute', 60, 3600], ['hour', 3600, 86400], ['day', 86400, 86400 * 30]]) {
      if (abs < limit) return new Intl.RelativeTimeFormat(language(), { numeric: 'auto' }).format(Math.round(seconds / size), unit);
    }
    return date(value);
  }
  function timeCell(value) {
    const d = toDate(value);
    return d ? e('time', { datetime: d.toISOString(), title: date(value), text: relative(value) }) : dash();
  }
  function durationText(start, end, running) {
    const from = toDate(start);
    const to = toDate(end) || (running ? new Date(Date.now()) : null);
    if (!from || !to) return '';
    const total = Math.max(0, Math.round((to.valueOf() - from.valueOf()) / 1000));
    if (total < 60) return t('durationSeconds', { seconds: total });
    if (total < 3600) return t('durationMinutes', { minutes: Math.floor(total / 60), seconds: total % 60 });
    return t('durationHours', { hours: Math.floor(total / 3600), minutes: Math.floor(total % 3600 / 60) });
  }
  // Content-addressed refs end with a full commit hash; keep the readable part.
  const shortRef = ref => String(ref || '').replace(/@([0-9a-f]{12,})$/i, (_, hash) => '@' + hash.slice(0, 7));
  function refText(ref) { return ref ? e('span', { class: 'mono ref', title: String(ref), text: shortRef(ref) }) : dash(); }
  // Dense tables drop the content hash and the model binding; titles keep the full ref.
  function compactRef(ref) { return ref ? e('span', { class: 'mono ref', title: String(ref), text: String(ref).replace(/@[0-9a-f]{12,}$/i, '') }) : dash(); }
  function modelText(ref) { return ref ? e('span', { class: 'mono ref', title: String(ref), text: String(ref).split('/').pop() }) : dash(); }
  function shortID(id) {
    const text = String(id || '');
    const match = text.match(/^(.*?)([0-9a-f]{8})-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i);
    return match ? match[1] + match[2] : text;
  }
  function idText(id) { return id ? e('span', { class: 'mono', title: String(id), text: shortID(id) }) : null; }
  const firstLine = text => String(text || '').split('\n').map(line => line.trim()).find(Boolean) || '';
  function truncate(text, size) {
    const chars = [...String(text || '')];
    return chars.length > size ? chars.slice(0, size).join('') + '…' : chars.join('');
  }
  function display(item) { return item.title || item.name || item.display_name || item.profile || item.ref || item.namespace || item.task_id || item.agent_instance_id || item.id || t('unknown'); }
  function ident(item) { return item.id ?? item.task_id ?? item.agent_instance_id ?? item.ref ?? item.namespace ?? item.profile ?? item.name ?? ''; }
  function linkTo(text, page, params) { return e('a', { href: '#' + page + '?' + new URLSearchParams(params), text }); }
  // Row clicks open details unless the click landed on a nested control.
  function interactive(node) {
    for (let n = node; n && n.tagName !== 'TR' && n.tagName !== 'SECTION'; n = n.parentNode) {
      if (['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA', 'SUMMARY', 'LABEL'].includes(n.tagName)) return true;
    }
    return false;
  }
  function rowClick(open) { return ev => { if (!interactive(ev.target)) open(); }; }
  function section(key, body, right, extraClass) {
    return e('section', { class: 'panel ' + (extraClass || '') }, e('div', { class: 'panel-head' }, e('h2', { text: t(key) }), right), body);
  }
  function definition(entries) {
    return e('dl', { class: 'definitions' }, entries.filter(Boolean).map(([key, value]) => e('div', { class: 'definition' }, e('dt', { text: t(key) }),
      e('dd', {}, value && typeof value === 'object' && value.nodeType ? value : value === undefined || value === null || value === '' ? dash() : String(value)))));
  }
  function empty(key, hint, compact) {
    return e('div', { class: 'empty' + (compact ? ' compact' : '') }, compact ? null : icon('sessions', 30), e('h2', { text: t(key || 'empty') }), hint === false ? null : e('p', { text: t(hint || 'emptyHint') }));
  }
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
    const languageSelect = selectOptions([{ value: 'auto', label: t('auto') }, { value: 'en', label: 'English' }, { value: 'zh-CN', label: '简体中文' }], state.language, value => {
      const modal = captureModal(); state.language = value; storageSet('language', value); applyPreferences(); render(); restoreModal(modal);
    }, { class: 'top-select', 'aria-label': t('language') });
    const themeSelect = selectOptions(['auto', 'light', 'dark'].map(value => ({ value, label: t(value) })), state.theme, value => { state.theme = value; storageSet('theme', value); applyPreferences(); }, { class: 'top-select', 'aria-label': t('theme') });
    return e('div', { class: 'prefs' }, e('label', { class: 'pref', title: t('language') }, icon('globe', 15), languageSelect), e('label', { class: 'pref', title: t('theme') }, icon('contrast', 15), themeSelect));
  }

  // ---------------------------------------------------------------------------
  // Requests, errors and the opening window.
  // ---------------------------------------------------------------------------
  // A rejected field is named with its form label, or explained when it is a
  // condition code (for example why a development worktree was kept).
  function errorDetail(fieldName) {
    if (typeof fieldName !== 'string' || !fieldName) return '';
    if (has('reason.' + fieldName)) return t('reason.' + fieldName);
    return t('invalidField', { field: has('field.' + fieldName) ? t('field.' + fieldName) : fieldName });
  }
  function errorMessage(error, write) {
    const machineCode = error.payload && (error.payload.code || error.payload.error);
    const detail = errorDetail(error.payload && error.payload.field);
    const known = (typeof machineCode === 'string' && has(machineCode) ? t(machineCode) : '') + (detail ? (has(machineCode) ? '\n' : '') + detail : '');
    if (error.status === 403) return known || t('forbidden');
    if (error.status === 409) return t('conflict') + (known ? '\n' + known : '');
    if (error.status === 401) return known || t('loginExpired');
    if (error.status === 404) return known || t('not_found');
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
    scheduleTick();
  }
  // Refreshes the header countdown; the expiry timer above owns closing.
  function scheduleTick() {
    root.clearTimeout(state.tickTimer); state.tickTimer = null;
    updateClock();
    if (!state.authConfig || !(state.authConfig.expires_at > 0) || state.consoleExpired) return;
    state.tickTimer = root.setTimeout(scheduleTick, 30000);
    if (state.tickTimer && state.tickTimer.unref) state.tickTimer.unref();
  }
  function remainingMinutes() {
    const at = state.authConfig && state.authConfig.expires_at;
    return at > 0 ? Math.max(0, Math.ceil((at * 1000 - Date.now()) / 60000)) : null;
  }
  function expiryCopy() {
    const config = state.authConfig || {};
    return t(config.mode === 'password' ? 'consoleExpiry' : 'consoleExpiryLocal', { time: date(config.expires_at) });
  }
  function updateClock() {
    if (typeof document === 'undefined') return;
    const minutes = remainingMinutes();
    const chipHost = document.getElementById('session-clock');
    if (chipHost) {
      chipHost.hidden = minutes === null;
      if (minutes !== null) { chipHost.textContent = t('closesIn', { minutes }); chipHost.setAttribute('title', expiryCopy()); chipHost.className = 'chip ' + (minutes <= 5 ? 'warn' : ''); }
    }
    const banner = document.getElementById('expiry-banner');
    if (banner) {
      const soon = minutes !== null && minutes <= 5;
      banner.hidden = !soon;
      if (soon) banner.textContent = t('closingSoon', { minutes }) + ' ' + expiryCopy();
    }
  }
  function expire(closed, messageKey) {
    state.consoleExpired = Boolean(closed || windowExpired());
    if (state.consoleExpired) { root.clearTimeout(state.expiryTimer); state.expiryTimer = null; }
    root.clearTimeout(state.tickTimer); state.tickTimer = null;
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
    if (snapshot && snapshot.warning_code && has(snapshot.warning_code)) messages.push(t(snapshot.warning_code));
    for (const key of ['errors', 'unavailable', 'warnings']) {
      const raw = snapshot && snapshot[key];
      if (!raw || Array.isArray(raw) && !raw.length) continue;
      const values = Array.isArray(raw) ? raw : typeof raw === 'object' ? Object.entries(raw).map(([k, v]) => k + ': ' + pretty(v)) : [raw];
      for (const value of values) messages.push(typeof value === 'string' ? (has('unavailable.' + value) ? t('unavailable.' + value) : value) : pretty(value));
    }
    if (!messages.length && !(snapshot && snapshot.partial)) return null;
    return e('div', { class: 'banner warn', role: 'status' }, icon('alert'), e('div', {}, e('strong', { text: t('partial') }), messages.map(text => e('p', { text }))));
  }
  function updateConnection() {
    if (typeof document === 'undefined') return;
    const host = document.getElementById('connection');
    if (host) host.replaceChildren(badge(!state.online ? 'offline' : state.connection), e('span', { class: 'connection-text', text: state.lastSync ? t('lastSync', { time: clockTime(state.lastSync) }) : '' }));
    const offline = document.getElementById('offline-banner');
    if (offline) { offline.hidden = state.online && state.connection !== 'offline'; offline.textContent = t('offline') + ' · ' + t('writeUncertain'); }
  }

  // ---------------------------------------------------------------------------
  // Shell and routing.
  // ---------------------------------------------------------------------------
  function render() {
    const oldTimeline = document.getElementById('timeline');
    if (oldTimeline && state.activeStreamRef) timelineFor(state.activeStreamRef).scrollTop = oldTimeline.scrollTop;
    const app = document.getElementById('app');
    if (!state.authenticated) { renderLogin(app); return; }
    const activePage = pageKeys[state.route.page] || state.route.page;
    const config = state.authConfig || {};
    const nav = e('aside', { class: 'sidebar ' + (state.navOpen ? 'expanded' : ''), id: 'navigation', 'aria-label': t('menu') },
      navGroups.map(([group, pages], index) => [index ? e('div', { class: 'nav-group', text: t(group) }) : null, pages.map(page => e('button', { type: 'button', class: 'nav-item ' + (activePage === page ? 'active' : ''), 'aria-current': activePage === page ? 'page' : null, onclick: () => navigate(page) }, icon(page, 16), t(page)))]),
      e('div', { class: 'sidebar-bottom' }, chip(t('authenticated'), 'good'), e('p', { class: 'mono', text: t('version') + ' ' + (state.bootstrap.version || t('unknown')) }), e('div', { class: 'sidebar-prefs' }, preferenceControls())));
    const header = e('header', { class: 'app-top' },
      e('button', { type: 'button', class: 'icon-button mobile-menu', 'aria-label': t('menu'), 'aria-controls': 'navigation', 'aria-expanded': String(state.navOpen), onclick: () => { state.navOpen = !state.navOpen; render(); } }, icon('menu')),
      e('a', { class: 'app-logo', href: '#overview', 'aria-label': t('overview') }, icon('tasks', 24), 'amux', e('small', { text: 'Console' })),
      e('button', { type: 'button', class: 'app-search', 'aria-label': t('search'), onclick: openSearch }, icon('search'), e('span', { text: t('search') }), e('kbd', { text: '⌘ K' })),
      e('div', { class: 'app-top-right' }, e('div', { id: 'connection', class: 'connection' }),
        e('span', { id: 'session-clock', class: 'chip', hidden: true }),
        config.insecure_transport ? chip(t('insecureShort'), 'warn', t('insecureTransportWarning')) : null,
        e('div', { class: 'header-prefs' }, preferenceControls()), iconButton('logout', 'logout', logout)));
    const main = e('main', { class: 'main', id: 'main', tabindex: '-1' }, breadcrumb(activePage),
      e('div', { id: 'expiry-banner', class: 'banner warn', role: 'status', hidden: true }),
      e('div', { id: 'offline-banner', class: 'banner warn', role: 'status', hidden: state.online }), e('div', { id: 'page' }));
    app.replaceChildren(header, e('div', { class: 'app-body' }, e('div', { class: 'mobile-shade ' + (state.navOpen ? 'expanded' : ''), onclick: () => { state.navOpen = false; render(); } }), nav, main));
    updateConnection(); updateClock(); renderPage();
  }
  function breadcrumb(activePage) {
    const detail = state.route.page !== activePage;
    return e('nav', { class: 'scope', 'aria-label': t('breadcrumb') }, e('span', { text: 'amux' }), ' / ',
      detail ? [e('a', { href: '#' + activePage, text: t(activePage) }), ' / ', e('span', { text: t(detailKeys[state.route.page]) })] : e('span', { text: t(activePage) }));
  }
  function title(key, description, actions, literal) {
    const heading = !literal && has(key) ? t(key) : key;
    const subtitle = description && typeof description === 'object' ? description : description ? (has(description) ? t(description) : description) : null;
    return e('div', { class: 'page-title' }, e('div', { class: 'grow' }, e('h1', { text: heading }), subtitle ? e('p', {}, subtitle) : null), e('div', { class: 'actions' }, actions));
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
  // The expert pages also ask what refers to each expert; the option lists that
  // read the same resources do not pay for it.
  const routeQueries = { profiles: { usage: '1' }, profile: { usage: '1' } };
  async function loadRoute() {
    if (!state.authenticated) return;
    closeStream(); const generation = ++state.generation;
    state.loading = true; state.routeError = null; render();
    try {
      const snapshot = await get(state.route.page, Object.assign({}, state.route.params, routeQueries[state.route.page]));
      if (generation !== state.generation || !state.authenticated) return;
      state.cache.set(cacheKey(), Object.assign(snapshot, { loaded_at: Date.now() })); state.loading = false; state.lastSync = Date.now(); renderPage();
      if (state.route.page === 'session') openStream(snapshot);
      const y = state.scroll.get(cacheKey()); if (y !== undefined) root.scrollTo(0, y);
    } catch (error) {
      if (generation !== state.generation || !state.authenticated) return;
      state.loading = false; state.routeError = error; renderPage();
    }
  }
  function refreshButton() { const b = button('refresh', () => loadRoute(), '', 'refresh'); b.disabled = state.loading; return b; }
  function target(item) { return field(item, 'target', field(item, 'target_id', '')); }
  function targetLabel(item) { return field(item, 'target_name', '') || target(item) || ''; }
  function sessionRef(item) { return { name: item.name || item.session_name, target: target(item) || state.route.params.target || state.route.params.target_id || '' }; }
  function capability(snapshot, action, item) {
    return state.bootstrap && state.bootstrap.operator !== false && supports(snapshot, action, item);
  }
  // Writes require a confirmation unless explicitly reversible and harmless.
  function actionButton(snapshot, resource, action, data, options) {
    const o = options || {};
    if (!capability(snapshot, action, o.item)) return null;
    const key = o.key || action;
    const danger = String(o.tone || '').includes('danger');
    const b = button(key, async () => {
      if (o.confirm !== false && !await confirmAction(key, o.name || display(o.item || data), danger)) return;
      b.disabled = true;
      try { await write(resource, action, data); notify(t(o.successKey || 'saved')); await loadRoute(); }
      catch (error) { notify(errorMessage(error, true), true); }
      finally { if (b.isConnected) b.disabled = !state.online; }
    }, o.tone || '', o.glyph);
    b.disabled = !state.online || state.busy; return b;
  }

  // ---------------------------------------------------------------------------
  // Lists: filtering, sorting and client-side pagination.
  // ---------------------------------------------------------------------------
  function filterState(page) {
    if (!state.filters.has(page)) state.filters.set(page, { query: '', state: '', target: '', group: page === 'sessions' ? 'active' : '', kind: '', namespace: '', use: '', page: 1 });
    return state.filters.get(page);
  }
  const defaultStateOf = row => row.state || row.status || field(row, 'turn_state');
  function filtered(rows, options) {
    const o = options || {}; const f = filterState(o.page || state.route.page);
    const stateOf = o.stateOf || defaultStateOf;
    const query = f.query.toLocaleLowerCase();
    return rows.filter(row => (!query || JSON.stringify(row).toLocaleLowerCase().includes(query))
      && (!f.state || String(stateOf(row)) === f.state)
      && (!f.target || String(target(row)) === f.target)
      && (!f.kind || !o.kindOf || o.kindOf(row) === f.kind)
      && (!f.group || (f.group === 'archived' ? isArchived(row) : !isArchived(row))));
  }
  function filters(rows, options) {
    const o = options || {}; const f = filterState(state.route.page);
    const stateOf = o.stateOf || defaultStateOf;
    const reset = () => { f.page = 1; renderPage(); };
    const query = e('input', { type: 'search', value: f.query, placeholder: t('filter'), 'aria-label': t('filter'), oninput: ev => {
      f.query = ev.target.value; f.page = 1; const cursor = ev.target.selectionStart; renderPage();
      const input = document.querySelector('.filterbar input'); if (input) { input.focus(); input.setSelectionRange(cursor, cursor); }
    } });
    const states = o.noState ? [] : [...new Set(rows.map(stateOf).filter(Boolean).map(String))].sort();
    const kinds = o.kindOf ? [...new Set(rows.map(o.kindOf).filter(Boolean))].sort() : [];
    const targets = [...new Map(rows.filter(r => target(r)).map(r => [String(target(r)), targetLabel(r)])).entries()];
    return e('div', { class: 'filterbar' }, query,
      states.length ? selectOptions([{ value: '', label: t('allStatuses') }, ...states.map(value => ({ value, label: stateLabel(value) }))], f.state, v => { f.state = v; reset(); }, { 'aria-label': t('status') }) : null,
      kinds.length > 1 ? selectOptions([{ value: '', label: t('allSources') }, ...kinds.map(value => ({ value, label: kindLabel(value) }))], f.kind, v => { f.kind = v; reset(); }, { 'aria-label': t('source') }) : null,
      targets.length > 1 || f.target ? selectOptions([{ value: '', label: t('allTargets') }, ...targets.map(([value, label]) => ({ value, label }))], f.target, v => { f.target = v; reset(); }, { 'aria-label': t('target') }) : null,
      ...(o.extra || []));
  }
  function timeValue(row, names) {
    for (const name of names) { const d = toDate(field(row, name)); if (d) return d.valueOf(); }
    return 0;
  }
  function sortByTime(rows, ...names) { return rows.slice().sort((a, b) => timeValue(b, names) - timeValue(a, names)); }
  function pager(f, pages) {
    const go = page => { f.page = page; renderPage(); const table = document.querySelector('.table-panel'); if (table && table.scrollIntoView) table.scrollIntoView({ block: 'start' }); };
    return e('div', { class: 'pager' },
      e('button', { type: 'button', class: 'ui-btn small', 'aria-label': t('previousPage'), title: t('previousPage'), disabled: f.page <= 1, onclick: () => go(f.page - 1) }, icon('chevronLeft', 14)),
      e('span', { text: t('pageOf', { page: f.page, pages }) }),
      e('button', { type: 'button', class: 'ui-btn small', 'aria-label': t('nextPage'), title: t('nextPage'), disabled: f.page >= pages, onclick: () => go(f.page + 1) }, icon('chevronRight', 14)));
  }
  function dataTable(columns, rows, options) {
    const o = options || {}; const f = filterState(o.pageKey || state.route.page);
    const size = o.pageSize || PAGE_SIZE;
    const pages = Math.max(1, Math.ceil(rows.length / size));
    f.page = Math.min(Math.max(1, f.page || 1), pages);
    const visible = rows.slice((f.page - 1) * size, f.page * size);
    const body = visible.map(row => {
      const open = o.rowLink && o.rowLink(row);
      return e('tr', { class: open ? 'clickable' : null, onclick: open ? rowClick(open) : null }, columns.map(c => e('td', { 'data-label': t(c.key), class: c.class || null }, c.render(row))));
    });
    const table = e('div', { class: 'table-scroll' }, e('table', { class: 'table' }, e('thead', {}, e('tr', {}, columns.map(c => e('th', { scope: 'col', class: c.class || null, text: t(c.key) })))), e('tbody', {}, body)));
    return e('div', { class: 'panel table-panel' }, rows.length ? table : empty(o.emptyKey, o.emptyHint), e('div', { class: 'table-footer' }, e('span', { text: t('count', { count: rows.length }) }), pages > 1 ? pager(f, pages) : null));
  }
  function cellTitle(text, sub, onclick) {
    const parts = (sub || []).filter(part => part !== null && part !== undefined && part !== '');
    return e('div', { class: 'cell-title' }, onclick ? e('button', { type: 'button', class: 'link-button', text, onclick }) : e('strong', { text }),
      parts.length ? e('p', { class: 'cell-sub' }, parts.map((part, index) => [index ? ' · ' : '', part])) : null);
  }
  function tabs(entries, current, onselect) {
    return e('div', { class: 'tabs' }, entries.map(([value, label, count]) => e('button', { type: 'button', class: 'tab ' + (current === value ? 'active' : ''), 'aria-pressed': String(current === value), onclick: () => onselect(value) }, label, count === undefined ? null : e('span', { class: 'tab-count', text: String(count) }))));
  }

  // ---------------------------------------------------------------------------
  // Overview.
  // ---------------------------------------------------------------------------
  function sessionKind(s) {
    if (field(s, 'owner_task_id')) return 'task';
    if (field(s, 'agent_instance_id')) return 'instance';
    return field(s, 'workspace_source', '') || 'other';
  }
  function kindLabel(kind) { return has('kind.' + kind) ? t('kind.' + kind) : kind; }
  const activityKnown = s => field(s, 'activity_status') === 'known' || !field(s, 'activity_status') && (own(s, 'turn_state') || own(s, 'turnState') || own(s, 'active_turn') || own(s, 'pending_permissions'));
  function sessionRow(item, status) {
    const open = () => navigate('session', sessionRef(item));
    return e('div', { class: 'run-row clickable', onclick: rowClick(open) },
      e('div', { class: 'grow' }, e('h3', { text: display(item) }), e('p', { class: 'row-sub', text: [kindLabel(sessionKind(item)), shortRef(field(item, 'agent_profile', '')), targetLabel(item)].filter(Boolean).join(' · ') })),
      badge(status), button('open', open, 'small'));
  }
  function taskTitle(row) { return truncate(firstLine(row.task), 80) || row.task_id || row.id || t('unknown'); }
  const taskID = row => row.task_id || row.id;
  function taskRow(item) {
    const open = () => navigate('task', { task_id: taskID(item) });
    return e('div', { class: 'run-row clickable', onclick: rowClick(open) },
      e('div', { class: 'grow' }, e('h3', { class: 'one-line', title: firstLine(item.task), text: taskTitle(item) }), e('p', { class: 'row-sub' }, timeCell(item.requested_at), ' · ', String(item.agent_profile || '').replace(/@[0-9a-f]{12,}$/i, '') || item.agent_type || shortID(taskID(item)))),
      badge(item.status || item.state));
  }
  function renderOverview(snapshot) {
    const sessions = list(snapshot, 'sessions'); const counts = deriveCounts(sessions);
    const current = sessions.filter(s => !isArchived(s));
    // A sleeping Session has no process and cannot be mid-Turn, so only live
    // Sessions need confirmed Turn facts before the totals can be trusted.
    const live = current.filter(s => s.state !== 'sleeping');
    const known = live.filter(activityKnown);
    const unknown = live.length - known.length;
    const coverage = { known: known.length, total: live.length, unknown };
    const knownCounts = deriveCounts(known);
    const hasTurnFacts = known.length > 0 || !live.length;
    const attention = known.filter(needsAttention);
    const working = known.filter(s => isWorking(s) && !needsAttention(s));
    const visibleWork = hasTurnFacts ? working : current.filter(s => s.state === 'running');
    const turnScope = unknown ? t('coverageShort', coverage) : t('turnScopeAll');
    const stats = [
      ['liveSessions', live.length, t('unarchivedTotal', { count: counts.active })],
      ['working', unknown ? '—' : knownCounts.working, turnScope],
      ['needsYou', unknown ? '—' : knownCounts.attention, turnScope],
      ['sleepingSessions', counts.sleeping, t('archivedTotal', { count: counts.archived })]
    ];
    const recent = sortByTime(current, 'last_activity_at', 'updated_at').slice(0, 8);
    const bots = list(snapshot, 'bots');
    const tasks = list(snapshot, 'recent_tasks');
    const taskCounts = snapshot.task_counts || null;
    const tasksUnavailable = list(snapshot, 'unavailable').includes('tasks');
    const taskHead = e('div', { class: 'actions' }, taskCounts && taskCounts.active ? chip(t('tasksActive', { count: taskCounts.active }), 'blue') : null, taskCounts && taskCounts.succeeded_24h ? chip(t('tasksSucceeded24h', { count: taskCounts.succeeded_24h }), 'good') : null, taskCounts && taskCounts.failed_24h ? chip(t('tasksFailed24h', { count: taskCounts.failed_24h }), 'bad') : null, button('viewAll', () => navigate('tasks'), 'quiet small'));
    return [title('overview', 'overviewDesc', refreshButton()),
      unknown ? e('p', { class: 'banner', role: 'status', text: t('activityCoverage', coverage) }) : null,
      e('div', { class: 'stats' }, stats.map(([key, value, sub]) => e('div', { class: 'stat' }, e('label', { text: t(key) }), e('strong', { text: String(value) }), e('p', { text: sub })))),
      e('div', { class: 'overview-grid' },
        e('div', { class: 'stack' },
          section('attention', attention.length ? attention.map(s => sessionRow(s, field(s, 'turn_state'))) : !hasTurnFacts ? empty('unknown', 'turnUnknown', true) : empty('nothingNeedsYou', false, true), null, attention.length ? 'attention-panel' : ''),
          section(hasTurnFacts ? 'currentWork' : 'runningSessions', visibleWork.length ? visibleWork.map(s => sessionRow(s, hasTurnFacts ? field(s, 'turn_state', s.state) : s.state)) : empty('noWorkInProgress', false, true), button('viewAll', () => navigate('sessions'), 'quiet small')),
          tasksUnavailable ? null : section('recentTasks', tasks.length ? tasks.map(taskRow) : empty('empty', false, true), taskHead)),
        e('div', { class: 'stack' },
          bots.length ? section('botConnections', bots.map(bot => e('div', { class: 'run-row' }, e('div', { class: 'avatar', 'aria-hidden': 'true' }, icon('bots', 16)), e('div', { class: 'grow' }, e('h3', { text: display(bot) }), e('p', { class: 'row-sub', text: shortRef(field(bot.settings, 'default_agent', '')) })), badge(bot.status)))) : null,
          section('recentActivity', recent.length ? recent.map(item => e('div', { class: 'run-row compact clickable', onclick: rowClick(() => navigate('session', sessionRef(item))) },
            e('div', { class: 'grow' }, e('button', { type: 'button', class: 'link-button', text: display(item), onclick: () => navigate('session', sessionRef(item)) }), e('p', { class: 'row-sub' }, kindLabel(sessionKind(item)), ' · ', timeCell(field(item, 'last_activity_at', field(item, 'updated_at'))))), badge(item.state))) : empty('empty', false, true))))];
  }

  // ---------------------------------------------------------------------------
  // Sessions and the live Timeline.
  // ---------------------------------------------------------------------------
  function sessionStatus(s) {
    const turn = activityKnown(s) ? field(s, 'turn_state') : '';
    return e('div', { class: 'badges' }, badge(s.state), turn && turn !== 'idle' && !isArchived(s) ? badge(turn) : null);
  }
  function renderSessions(snapshot) {
    const all = sortByTime(list(snapshot, 'sessions'), 'last_activity_at', 'updated_at', 'created_at');
    const f = filterState('sessions');
    const inGroup = all.filter(s => !f.group || (f.group === 'archived' ? isArchived(s) : !isArchived(s)));
    const options = { stateOf: s => s.state, kindOf: sessionKind };
    const archivedCount = all.filter(isArchived).length;
    return [title('sessions', 'sessionsDesc', [refreshButton(), capability(snapshot, 'create') ? button('newSession', openNewSession, 'primary', 'plus') : null]),
      tabs([['active', t('unarchived'), all.length - archivedCount], ['archived', t('archived'), archivedCount], ['', t('all'), all.length]], f.group, value => { f.group = value; f.state = ''; f.page = 1; renderPage(); }),
      filters(inGroup, options),
      dataTable([
        { key: 'sessions', render: s => cellTitle(display(s), [kindLabel(sessionKind(s)), idText(s.id)], () => navigate('session', sessionRef(s))) },
        { key: 'status', render: sessionStatus },
        { key: 'profile', render: s => e('div', {}, compactRef(field(s, 'agent_profile')), e('p', { class: 'cell-sub', text: field(s, 'agent_type', '') })) },
        { key: 'model', class: 'wide-only', render: s => modelText(field(s, 'effective_model')) },
        { key: 'target', class: 'nowrap', render: s => targetLabel(s) || dash() },
        { key: 'updated', class: 'nowrap', render: s => timeCell(field(s, 'last_activity_at', field(s, 'updated_at'))) }
      ], filtered(all, options), { rowLink: s => () => navigate('session', sessionRef(s)) })];
  }
  // A live Timeline is the freshest source; the snapshot replay covers reloads.
  function timelineTurn(timeline) {
    for (let i = timeline.items.length - 1; i >= 0; i--) {
      const item = timeline.items[i];
      if (item.kind === 'turn_finished') return { active: false, state: item.state || 'completed' };
      if (item.kind === 'turn_started') return { active: true, state: timeline.permissions.size ? 'waiting_permission' : 'running' };
    }
    return null;
  }
  function currentTurn(snapshot, timeline) {
    const live = timelineTurn(timeline);
    if (live) return live;
    const facts = snapshot.activity;
    return facts ? { active: Boolean(facts.active_turn), state: facts.turn_state || '' } : null;
  }
  function sessionActionButtons(caps, session, ref, turn) {
    const plan = {
      running: [turn && turn.active === false ? null : ['interrupt', ''], ['restart', ''], ['sleep', ''], ['archive', 'danger']],
      sleeping: [['resume', 'primary'], ['archive', 'danger']],
      archived: []
    }[session.state] || [['restart', ''], ['archive', 'danger']];
    return plan.filter(Boolean).map(([action, tone]) => actionButton(caps, 'sessions', action, ref, { key: action, item: session, tone, confirm: action !== 'resume' }));
  }
  function renderSession(snapshot) {
    const session = snapshot.session;
    if (!session) return [title('session'), empty('unavailable')];
    const ref = sessionRef(session); const caps = snapshot.actions ? snapshot : (state.cache.get('sessions?') || snapshot);
    const stream = timelineFor(ref);
    const turn = currentTurn(snapshot, stream);
    const perms = [...stream.permissions.values()];
    for (const permission of list(snapshot, 'permissions')) {
      const normalized = normalizePermission(permission, permission.id || permission.permission_id);
      if (!perms.some(p => p.id === normalized.id)) perms.push(normalized);
    }
    const queue = Array.isArray(snapshot.queue) ? snapshot.queue : list(snapshot.queue, 'items');
    const kind = sessionKind(session);
    const ownerTask = field(session, 'owner_task_id'); const instance = field(session, 'agent_instance_id');
    const idle = Number(field(session, 'idle_timeout_seconds', 0));
    return [literalTitle(display(session), e('span', { class: 'title-meta' }, chip(kindLabel(kind)), e('span', { class: 'mono', text: session.id || '' })), [refreshButton(), e('div', { id: 'session-actions', class: 'actions' }, sessionActionButtons(caps, session, ref, turn))]),
      e('div', { class: 'context-strip' }, [['profile', refText(field(session, 'agent_profile'))], ['agentType', field(session, 'agent_type')], ['model', refText(field(session, 'effective_model'))], ['target', targetLabel(session)]].map(([key, value]) => e('div', {}, e('label', { text: t(key) }), e('strong', {}, value && value.nodeType ? value : value || '—')))),
      e('div', { class: 'detail-grid' },
        e('div', { class: 'stack' }, section('timeline', e('div', { id: 'timeline', class: 'timeline', role: 'log', 'aria-label': t('timeline'), 'aria-live': 'off' }), e('span', { class: 'badge blue', id: 'stream-status' }, e('span', { class: 'dot', 'aria-hidden': true }), t('live'))),
          !isArchived(session) && capability(caps, 'submit', session) ? composer(ref) : null),
        e('aside', { class: 'stack', id: 'session-aside' }, perms.map(p => permissionPanel(p, ref, caps)),
          section('details', e('div', { class: 'panel-body' }, definition([
            ['lifecycle', badge(session.state)], ['turn', e('span', { id: 'turn-state' }, turn ? badge(turn.state) : dash())], ['source', kindLabel(kind)],
            ['createdAt', timeCell(field(session, 'created_at'))], ['updated', timeCell(field(session, 'last_activity_at', field(session, 'updated_at')))],
            ['idleTimeout', idle > 0 ? String(idle) : ''], ['sleepReason', field(session, 'last_sleep_reason', '')],
            ownerTask ? ['ownerTask', linkTo(shortID(ownerTask), 'task', { task_id: ownerTask })] : null,
            instance ? ['instances', linkTo(shortID(instance), 'instance', { agent_instance_id: instance })] : null,
            ['cwd', session.cwd ? e('span', { class: 'mono path', text: session.cwd }) : ''], ['id', e('span', { class: 'mono path', text: session.id || '' })]]))),
          section('queue', e('div', { class: 'panel-body stack' }, queue.length ? queue.map(item => e('div', { class: 'queue-item' }, code(item.text || item.prompt || ''), e('div', { class: 'actions' }, actionButton(caps, 'sessions', 'queue-cancel', Object.assign({}, ref, { queue_id: item.id }), { key: 'queue_cancel', item: session, tone: 'danger small' })))) : e('p', { class: 'muted small', text: t('queueEmpty') })),
            queue.length ? actionButton(caps, 'sessions', 'queue-clear', ref, { key: 'queue_clear', item: session, tone: 'danger small' }) : null)))];
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
    } }, textarea, e('div', { class: 'composer-foot' }, e('span', { class: 'help', text: t('sendHint') }), send));
    textarea.addEventListener('keydown', event => { if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); form.requestSubmit(); } });
    return form;
  }
  function permissionPanel(permission, ref, caps) {
    const tool = permission.tool || {};
    const controls = e('div', { class: 'actions' });
    const canAnswer = capability(caps, 'respond-permission');
    const respond = async data => {
      // Answering a permission lets or stops agent work, so keep the strong warning.
      if (!await confirmAction('respond', permission.question || tool.title || permission.id, true)) return;
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
        const tone = /reject|deny/.test(option.kind) ? 'danger' : /allow/.test(option.kind) && !/always/.test(option.kind) ? 'primary' : '';
        controls.appendChild(e('button', { type: 'button', class: 'ui-btn ' + tone, text: option.name, title: option.description || option.kind, disabled: !state.online, onclick: () => respond({ option_id: option.id }) }));
      }
    }
    return section('permission', e('div', { class: 'panel-body' }, permission.question ? e('p', { class: 'prose', text: permission.question }) : null,
      definition([['tool', tool.title || tool.name || t('unknown')], ['id', e('span', { class: 'mono', text: permission.id })]]), field(tool, 'raw_input') ? code(field(tool, 'raw_input')) : null,
      controls.childElementCount ? controls : e('p', { class: 'note', text: t('permissionUnavailable') }), e('p', { class: 'help', text: t('permissionHelp') })), icon('shield'), 'permission');
  }
  function timelineFor(ref) {
    const key = JSON.stringify(ref);
    if (!state.timeline.has(key)) state.timeline.set(key, { items: [], seen: new Set(), permissions: new Map(), cursor: '', scrollTop: null });
    return state.timeline.get(key);
  }
  function ingestEvent(ref, event) {
    const timeline = timelineFor(ref); const normalized = normalizeEvent(event);
    if (event.caught_up) timeline.caughtUp = true;
    if (normalized.id && timeline.seen.has(normalized.id)) return false;
    if (normalized.id) timeline.seen.add(normalized.id);
    if (event.cursor) timeline.cursor = event.cursor;
    if (normalized.permission && normalized.permission.id) timeline.permissions.set(normalized.permission.id, normalized.permission);
    if (normalized.resolved) timeline.permissions.delete(normalized.resolved);
    if (['heartbeat', 'session_metadata_changed'].includes(normalized.kind) || event.caught_up && !field(event, 'canonical_json')) return true;
    const last = timeline.items[timeline.items.length - 1];
    if (normalized.kind === 'assistant_delta' && last && last.kind === 'assistant_delta' && !last.decodeError && !normalized.decodeError && last.text.length + normalized.text.length < 100000) last.text += normalized.text;
    else if (normalized.kind === 'thinking_delta' && last && last.kind === 'thinking_delta' && last.text.length + normalized.text.length < 100000) last.text += normalized.text;
    else if (normalized.tool && normalized.tool.id) {
      const prior = timeline.items.findIndex(item => item.tool && item.tool.id === normalized.tool.id);
      if (prior >= 0) timeline.items[prior] = Object.assign({}, timeline.items[prior], normalized, { tool: Object.assign({}, timeline.items[prior].tool, normalized.tool) });
      else timeline.items.push(normalized);
    } else timeline.items.push(normalized);
    if (timeline.items.length > 1500) timeline.items.splice(0, timeline.items.length - 1500);
    if (timeline.seen.size > 10000) timeline.seen = new Set([...timeline.seen].slice(-5000));
    return true;
  }
  // The Timeline shows conversation content. Session metadata (command lists,
  // goal capability), empty messages, repeated usage snapshots and a result
  // that repeats the streamed reply of the same Turn add no content.
  const metadataKinds = ['commands_replaced', 'session_metadata_changed', 'heartbeat'];
  function hiddenEvent(item) {
    if (metadataKinds.includes(item.kind)) return true;
    if ((item.kind === 'goal_changed' || item.kind === 'structured') && item.body && !item.body.goal && Object.keys(item.body).every(key => key === 'supported')) return true;
    return ['assistant_delta', 'assistant_message', 'thinking_delta'].includes(item.kind) && !item.decodeError && !String(item.text || '').trim();
  }
  function visibleTimeline(items) {
    const out = []; let reply = ''; let usageIndex = -1;
    for (const item of items) {
      if (hiddenEvent(item)) continue;
      if (item.kind === 'turn_started' || item.kind === 'user_message') { reply = ''; usageIndex = -1; }
      if (item.kind === 'assistant_delta' || item.kind === 'assistant_message') reply += item.text || '';
      if (item.kind === 'result_observed' && item.text && reply.trim() && reply.trim().endsWith(item.text.trim())) continue;
      if (item.kind === 'usage' || item.kind === 'usage_observed') {
        if (usageIndex >= 0) out.splice(usageIndex, 1);
        usageIndex = out.length;
      }
      out.push(item);
    }
    return out;
  }
  function drawTimeline() {
    if (state.route.page !== 'session') return;
    const snapshot = state.cache.get(cacheKey()); if (!snapshot || !snapshot.session) return;
    const timeline = timelineFor(sessionRef(snapshot.session)); const host = document.getElementById('timeline'); if (!host) return;
    const atBottom = host.scrollHeight - host.scrollTop - host.clientHeight < 70;
    const y = host.scrollTop;
    const items = visibleTimeline(timeline.items);
    replace(host, items.length ? items.map(eventNode) : empty('noEvents', false, true));
    if (timeline.items.length >= 1500) host.prepend(e('p', { class: 'note', text: t('eventLimit', { count: 1500 }) }));
    host.scrollTop = timeline.scrollTop !== null ? timeline.scrollTop : atBottom ? host.scrollHeight : y;
    timeline.scrollTop = null;
  }
  const eventLabels = { assistant_delta: 'agent', assistant_message: 'agent', result_observed: 'result', user_message: 'user', turn_started: 'user', permission_requested: 'permission', permission_request: 'permission', permission_resolved: 'permissionResolved', usage: 'usage', usage_observed: 'usage', thinking_delta: 'thinking', plan_replaced: 'plan', diagnostic: 'diagnostic', session_diagnostic: 'diagnostic', state_change: 'sessionState', error: 'error' };
  function eventTime(item) { return item.at ? e('time', { datetime: item.at, title: date(item.at), text: clockTime(item.at) }) : null; }
  function planNode(body) {
    const entries = list(body, 'entries').length ? list(body, 'entries') : list(body && body.plan, 'entries');
    if (!entries.length) return code(body);
    return e('ul', { class: 'plan-list' }, entries.map(entry => {
      const status = entry.status || 'pending';
      return e('li', { class: 'plan-' + status }, icon(status === 'completed' ? 'check' : status === 'in_progress' ? 'half' : 'circle', 14), e('span', { text: entry.content || entry.title || '' }));
    }));
  }
  function usageText(body) {
    const usage = body && (body.usage || body);
    const input = usage && (usage.input_tokens ?? usage.inputTokens ?? usage.prompt_tokens);
    const output = usage && (usage.output_tokens ?? usage.outputTokens ?? usage.completion_tokens);
    return Number.isFinite(input) || Number.isFinite(output) ? t('usageLine', { input: Number(input || 0).toLocaleString(language()), output: Number(output || 0).toLocaleString(language()) }) : '';
  }
  function eventNode(item) {
    if (item.kind === 'turn_finished') return e('div', { class: 'turn-divider' }, e('span', {}, t('turnFinished'), ' · ', stateLabel(item.state || 'completed')), eventTime(item));
    if (item.kind === 'permission_resolved') return e('div', { class: 'turn-divider subtle' }, e('span', { text: t('permissionResolved') }), eventTime(item));
    const label = item.tool ? 'tool' : eventLabels[item.kind] || 'details';
    let content;
    let heading = [t(label)];
    let variant = '';
    if (item.tool) {
      variant = 'tool';
      heading = [item.tool.name || t('tool'), ' ', badge(item.tool.status)];
      content = item.tool.input || item.tool.output ? e('details', { open: item.tool.status === 'error' || item.tool.status === 'failed' }, e('summary', { text: t('toolDetails') }), item.tool.input ? code(item.tool.input) : null, item.tool.output ? code(item.tool.output) : null) : null;
    } else if (item.kind === 'turn_started' || item.kind === 'user_message') {
      variant = 'user';
      const channel = item.origin && item.origin.channel;
      if (channel) heading = [t('user'), ' · ', has('kind.' + channel) ? t('kind.' + channel) : channel];
      content = e('p', { class: 'content', text: item.text || '—' });
    } else if (item.kind === 'permission_requested' || item.kind === 'permission_request') {
      variant = 'permission';
      content = e('p', { class: 'content', text: item.permission && (item.permission.question || item.permission.tool.title || item.permission.id) || t('permission') });
    } else if (item.kind === 'thinking_delta') {
      variant = 'thinking';
      return e('article', { class: 'timeline-item thinking' }, e('div', { class: 'timeline-glyph' }, icon('thinking', 15)), e('details', {}, e('summary', {}, t('thinking'), eventTime(item)), e('p', { class: 'content', text: item.text })));
    } else if (item.kind === 'plan_replaced') {
      content = planNode(item.body);
    } else if (item.kind === 'usage' || item.kind === 'usage_observed') {
      const text = usageText(item.body);
      if (text) return e('div', { class: 'turn-divider subtle' }, e('span', { text }), eventTime(item));
      content = code(item.body || {});
    } else if (item.text || item.decodeError) {
      content = e('p', { class: 'content', text: item.decodeError ? t('invalidEncoding') : item.text });
      if (item.kind === 'result_observed') variant = 'result';
      if (item.kind === 'assistant_delta' || item.kind === 'assistant_message') variant = 'agent';
      if (['error', 'diagnostic', 'session_diagnostic'].includes(item.kind)) variant = 'bad';
    } else if (item.state) content = badge(item.state);
    else content = item.body ? code(item.body) : e('span', { class: 'muted', text: item.kind });
    const glyph = item.tool ? 'tool' : variant === 'permission' ? 'shield' : variant === 'user' ? 'user' : variant === 'bad' ? 'alert' : 'bots';
    return e('article', { class: 'timeline-item ' + variant }, e('div', { class: 'timeline-glyph' }, icon(glyph, 15)), e('div', { class: 'timeline-body' }, e('h3', {}, e('span', { class: 'event-title' }, heading), eventTime(item)), content));
  }
  function closeStream() {
    root.clearTimeout(state.lifecycleTimer); state.lifecycleTimer = null;
    if (state.streams) { state.streams.close(); state.streams = null; }
    if (state.drawTimer) { clearTimeout(state.drawTimer); state.drawTimer = null; }
    const timelineHost = typeof document !== 'undefined' ? document.getElementById('timeline') : null;
    if (timelineHost && state.activeStreamRef) timelineFor(state.activeStreamRef).scrollTop = timelineHost.scrollTop;
    state.activeStreamRef = null;
    // "Reconnecting" describes the live stream that just closed; reachability is
    // reported again by the requests that follow.
    if (state.connection === 'reconnecting' && state.online) { state.connection = 'connected'; updateConnection(); }
  }
  function setStreamStatus(key) {
    state.streamStatus = key;
    const status = document.getElementById('stream-status');
    if (status) { status.className = 'badge ' + (key === 'live' || key === 'loadingHistory' ? 'blue' : key === 'historyOnly' ? '' : 'warn'); replace(status, e('span', { class: 'dot', 'aria-hidden': true }), t(key)); }
  }
  // A sleeping or archived Session has no live events. Once its history replay is
  // complete and the server ends the stream, stop the browser's automatic
  // reconnects (each would replay the whole history) and say so, instead of
  // showing "reconnecting" for a Session that is simply not running.
  function settleStream(stream) {
    const cached = state.cache.get(cacheKey());
    const lifecycle = cached && cached.session && cached.session.state;
    if (state.activeStreamRef === null || !timelineFor(state.activeStreamRef).caughtUp || (lifecycle !== 'sleeping' && lifecycle !== 'archived')) return false;
    stream.close();
    if (state.connection === 'reconnecting') { state.connection = 'connected'; updateConnection(); }
    setStreamStatus('historyOnly');
    return true;
  }
  // The Hub sends no event when a Session sleeps, wakes or is archived elsewhere
  // (idle reclaim, another operator, Feishu), so lifecycle controls would keep the
  // state seen at page load. Re-read the lifecycle when the stream ends and every
  // 20 s while the tab is visible; the light read skips the Turn replay. Never once
  // archived, and a page rebuild only happens when the lifecycle actually changed.
  const lifecyclePollMs = 20000;
  async function syncSessionLifecycle(stream, polled) {
    const key = cacheKey(); const generation = state.generation;
    const cached = state.cache.get(key);
    if (!cached || !cached.session || isArchived(cached.session)) return;
    const now = Date.now();
    if (!polled && state.lifecycleSyncAt && now - state.lifecycleSyncAt < 10000) return;
    state.lifecycleSyncAt = now;
    try {
      const fresh = await get(state.route.page, Object.assign({}, state.route.params, { activity: '0' }));
      if (state.streams !== stream || generation !== state.generation || !fresh.session || fresh.session.state === cached.session.state) return;
      // Turn facts belong to the previous lifecycle; the live Timeline supplies the Turn.
      const merged = Object.assign({}, cached, fresh, { loaded_at: Date.now(), activity_status: 'unknown' });
      delete merged.activity;
      state.cache.set(key, merged);
      renderPage();
      if (!settleStream(stream)) setStreamStatus(state.streamStatus || 'live');
    } catch (_) { /* The connection indicator already reports reachability. */ }
  }
  function scheduleLifecyclePoll(stream) {
    root.clearTimeout(state.lifecycleTimer);
    state.lifecycleTimer = root.setTimeout(async () => {
      if (state.streams !== stream) return;
      if (!document.hidden && !state.modal) await syncSessionLifecycle(stream, true);
      if (state.streams === stream) scheduleLifecyclePoll(stream);
    }, lifecyclePollMs);
    if (state.lifecycleTimer && state.lifecycleTimer.unref) state.lifecycleTimer.unref();
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
    state.streams = stream; state.activeStreamRef = ref; state.lifecycleSyncAt = 0; timeline.caughtUp = false;
    state.streamEvents = 0; state.streamMark = 0; state.streamLostAt = 0;
    setStreamStatus('loadingHistory');
    scheduleLifecyclePoll(stream);
    const caps = snapshot.actions ? snapshot : state.cache.get('sessions?') || snapshot;
    stream.onopen = () => { if (state.streams !== stream) return; state.connection = 'connected'; updateConnection(); setStreamStatus(timeline.caughtUp ? 'live' : 'loadingHistory'); };
    stream.onmessage = message => {
      if (state.streams !== stream) return;
      let event;
      try { event = JSON.parse(message.data); } catch (_) { notify(t('invalidResponse'), true); return; }
      if (event.error && !own(event, 'kind')) { notify(String(event.error), true); return; }
      const fresh = ingestEvent(ref, event);
      if (fresh) state.streamEvents = (state.streamEvents || 0) + 1;
      if (timeline.caughtUp && state.streamStatus === 'loadingHistory') setStreamStatus('live');
      if (!fresh) return;
      state.lastSync = Date.now();
      if (!state.drawTimer) state.drawTimer = setTimeout(() => {
        state.drawTimer = null; drawTimeline();
        const aside = document.getElementById('session-aside');
        if (aside) {
          const previous = [...aside.querySelectorAll('.permission')]; previous.forEach(p => p.remove());
          for (const permission of timeline.permissions.values()) aside.prepend(permissionPanel(permission, ref, caps));
        }
        // Keep lifecycle controls and the Turn badge in step without
        // re-rendering the composer, which may hold focus and a draft.
        const current = state.cache.get(cacheKey()) || snapshot;
        const turn = currentTurn(current, timeline);
        const actions = document.getElementById('session-actions'); if (actions) replace(actions, sessionActionButtons(caps, current.session || session, ref, turn));
        const turnHost = document.getElementById('turn-state'); if (turnHost) replace(turnHost, turn ? badge(turn.state) : dash());
      }, 80);
    };
    // The server's "disconnected" event and the browser's own error both report one
    // interruption; handle it once. A replay that cut off after making progress is the
    // Hub limiting a long history, not a lost connection: the browser resumes after the
    // last event it saw, so say that instead of "reconnecting".
    const lost = () => {
      if (state.streams !== stream || settleStream(stream)) return;
      const now = Date.now();
      if (state.streamLostAt && now - state.streamLostAt < 500) return;
      state.streamLostAt = now;
      const progressed = (state.streamEvents || 0) !== (state.streamMark || 0);
      state.streamMark = state.streamEvents || 0;
      if (!timeline.caughtUp && progressed) setStreamStatus('loadingHistory');
      else { state.connection = state.online ? 'reconnecting' : 'offline'; updateConnection(); setStreamStatus('reconnecting'); }
      // Native EventSource retries only GET. An auth check redirects expired sessions.
      get('bootstrap').catch(() => {});
      syncSessionLifecycle(stream);
    };
    stream.addEventListener('disconnected', lost);
    stream.onerror = lost;
  }

  // ---------------------------------------------------------------------------
  // Tasks and persistent instances.
  // ---------------------------------------------------------------------------
  function modeLabel(mode) { return mode ? (has('mode.' + mode) ? t('mode.' + mode) : mode) : ''; }
  // Tasks name what started them; a Schedule's tasks come from its run history.
  function scheduleLink(row) {
    const id = row.schedule_id || row.schedule && row.schedule.id;
    if (!id) return null;
    const name = row.schedule_name || row.schedule && row.schedule.name || '#' + id;
    return e('a', { class: 'chip blue', href: '#schedule?' + new URLSearchParams({ schedule_id: id }), title: t('openSchedule'), text: name });
  }
  function renderTasks(snapshot) {
    const rows = sortByTime(list(snapshot, 'tasks'), 'requested_at', 'started_at');
    const options = { stateOf: r => r.status || r.state, kindOf: r => r.source };
    const schedules = list(snapshot, 'schedules');
    const selected = state.route.params.schedule_id || '';
    const scheduleFilter = schedules.length || selected ? selectOptions([{ value: '', label: t('allSchedules') }, ...schedules.map(row => ({ value: String(row.id), label: (row.name || '#' + row.id) + ' (' + Number(row.task_count || 0) + ')' }))], selected, value => navigate('tasks', value ? { schedule_id: value } : {}), { 'aria-label': t('schedules') }) : null;
    return [title('tasks', 'tasksDesc', refreshButton()), filters(rows, Object.assign({ extra: [scheduleFilter] }, options)),
      selected ? e('p', { class: 'help', text: t('scheduleTasksNote') }) : null,
      dataTable([
      { key: 'tasks', render: r => cellTitle(taskTitle(r), [idText(taskID(r)), modeLabel(r.mode)], () => navigate('task', { task_id: taskID(r) })) },
      { key: 'source', render: r => e('div', { class: 'badges' }, r.source ? chip(kindLabel(r.source), r.source === 'schedule' ? 'blue' : 'quiet') : null, scheduleLink(r)) },
      { key: 'result', render: r => badge(r.status || r.state) },
      { key: 'profile', render: r => e('div', {}, r.agent_profile || r.agent ? compactRef(r.agent_profile || r.agent) : dash(), e('p', { class: 'cell-sub', text: r.agent_type || '' })) },
      { key: 'target', class: 'wide-only nowrap', render: r => r.target_name || r.target_id || dash() },
      { key: 'requestedAt', class: 'nowrap', render: r => timeCell(r.requested_at) },
      { key: 'duration', class: 'nowrap', render: r => durationText(r.started_at, r.finished_at, !isTerminalTask(r.status)) || dash() },
      { key: 'actions', render: r => isTerminalTask(r.status) ? null : actionButton(snapshot, 'tasks', 'cancel', { task_id: taskID(r) }, { key: 'cancelTask', item: r, name: taskTitle(r), tone: 'danger small' }) }
    ], filtered(rows, options), { rowLink: r => () => navigate('task', { task_id: taskID(r) }), emptyKey: selected ? 'noScheduleTasks' : undefined, emptyHint: selected ? false : undefined })];
  }
  function valueList(values) {
    const items = values.map(value => typeof value === 'string' ? value : pretty(value)).filter(Boolean);
    return items.length ? e('ul', { class: 'value-list' }, items.map(text => e('li', { class: 'mono', text }))) : '';
  }
  function renderTask(snapshot) {
    const item = snapshot.task; if (!item) return [title('tasks'), empty('unavailable')];
    const id = taskID(item); const child = item.session_ref || {}; const terminal = isTerminalTask(item.status);
    const result = item.result && typeof item.result === 'object' ? item.result : null;
    return [literalTitle(taskTitle(item), e('span', { class: 'mono', text: id || '' }), [refreshButton(),
        child.child_session_id ? button('openSession', () => navigate('session', { name: child.child_session_id, target: child.target_id || '' })) : null,
        item.parent_session_id ? button('openParentSession', () => navigate('session', { name: item.parent_session_id })) : null,
        terminal ? null : actionButton(snapshot, 'tasks', 'cancel', { task_id: id }, { key: 'cancelTask', item, name: taskTitle(item), tone: 'danger' })]),
      item.error_code ? e('div', { class: 'banner bad', role: 'alert', text: has(item.error_code) ? t(item.error_code) : t('requestFailed') }) : null,
      e('div', { class: 'detail-grid' },
        e('div', { class: 'stack' },
          section('taskInstruction', e('div', { class: 'panel-body' }, item.task ? e('p', { class: 'prose', text: item.task }) : dash())),
          section('resultSummary', e('div', { class: 'panel-body stack' }, item.summary ? e('p', { class: 'prose', text: item.summary }) : e('p', { class: 'muted', text: t(terminal ? 'noSummary' : 'taskStillRunning') }),
            result ? definition([['result', badge(result.status)], ['changedFiles', valueList(list(result, 'changed_files'))], ['artifacts', valueList(list(result, 'artifacts'))], result.details_ref ? ['details', e('span', { class: 'mono', text: result.details_ref })] : null]) : null,
            result && result.diagnostics ? e('details', {}, e('summary', { text: t('diagnostics') }), code(result.diagnostics)) : null,
            result && result.full_transcript ? e('details', {}, e('summary', { text: t('fullTranscript') }), code(result.full_transcript)) : null,
            e('details', {}, e('summary', { text: t('rawRecord') }), code(item))))),
        e('aside', { class: 'stack' }, section('details', e('div', { class: 'panel-body' }, definition([
          ['status', badge(item.status)], ['mode', modeLabel(item.mode)], ['agentType', item.agent_type], ['profile', refText(item.agent_profile || item.agent)],
          ['permissionPolicy', item.permission_policy], ['target', item.target_name || item.target_id],
          ['requestedAt', timeCell(item.requested_at)], ['startedAt', timeCell(item.started_at)], ['finishedAt', timeCell(item.finished_at)],
          ['duration', durationText(item.started_at, item.finished_at, !terminal)],
          item.source ? ['source', kindLabel(item.source)] : null,
          item.schedule ? ['schedule', scheduleLink(item)] : null,
          item.agent_instance_id ? ['instances', linkTo(shortID(item.agent_instance_id), 'instance', { agent_instance_id: item.agent_instance_id })] : null,
          child.child_session_id ? ['childSession', linkTo(shortID(child.child_session_id), 'session', { name: child.child_session_id, target: child.target_id || '' })] : null,
          item.parent_session_id ? ['ownerSession', linkTo(shortID(item.parent_session_id), 'session', { name: item.parent_session_id })] : null])))))];
  }
  const instanceID = row => row.agent_instance_id || row.id;
  // A generated name ("agent-…") says nothing; the task that created the
  // instance says what it is for.
  const generatedInstanceName = row => !row.name || row.name === instanceID(row) || /^agent-[0-9a-f-]+$/i.test(row.name);
  function instanceName(row) { return !generatedInstanceName(row) ? row.name : truncate(row.purpose, 80) || row.name || shortID(instanceID(row)) || t('unknown'); }
  function instanceOwner(row) {
    const owner = row.owner;
    if (!owner) return dash();
    if (owner.missing) return e('div', {}, e('span', { class: 'muted', text: t('ownerSessionEnded') }), e('p', { class: 'cell-sub mono', text: shortID(owner.session_id) }));
    const kind = owner.workspace_source ? kindLabel(owner.workspace_source) : '';
    return e('div', {}, linkTo(owner.name || shortID(owner.session_id), 'session', { name: owner.name || owner.session_id, target: owner.target_id || '' }),
      e('p', { class: 'cell-sub' }, [kind, owner.agent_profile ? shortRef(owner.agent_profile) : '', owner.target_name || ''].filter(Boolean).join(' · ')));
  }
  function latestInstanceTask(row) {
    const latest = row.latest_task;
    if (!latest) return row.tasks_unavailable ? dash() : e('span', { class: 'muted', text: t('noInstanceTasks') });
    return e('div', {}, e('div', { class: 'badges' }, badge(latest.status), linkTo(shortID(latest.task_id), 'task', { task_id: latest.task_id })),
      e('p', { class: 'cell-sub' }, timeCell(latest.requested_at), ' · ', t('taskCount', { count: Number(row.task_count || 0) })));
  }
  function instanceActions(snapshot, row, small) {
    const id = instanceID(row); const archived = row.status === 'archived'; const active = row.status === 'active';
    const tone = small ? ' small' : '';
    return [
      !archived && capability(snapshot, 'followup', row) ? button('followup', () => openInstanceTask(row), tone) : null,
      !archived && capability(snapshot, 'send', row) ? button('sendMessage', () => openInstanceMessage(row, 'send'), tone) : null,
      active && capability(snapshot, 'steer', row) ? button('steer', () => openInstanceMessage(row, 'steer'), tone) : null,
      active ? actionButton(snapshot, 'instances', 'interrupt', { agent_instance_id: id }, { key: 'interrupt', item: row, name: instanceName(row), tone: 'danger' + tone }) : null];
  }
  function renderInstances(snapshot) {
    const rows = sortByTime(list(snapshot, 'instances'), 'last_used_at', 'created_at');
    const options = { stateOf: r => r.status };
    const open = r => () => navigate('instance', { agent_instance_id: instanceID(r) });
    return [title('instances', 'instancesDesc', [refreshButton(), capability(snapshot, 'spawn') ? button('spawn', () => openInstanceTask(), 'primary', 'plus') : null]), filters(rows, options), dataTable([
      { key: 'instances', render: r => cellTitle(instanceName(r), [idText(instanceID(r)), !generatedInstanceName(r) && r.purpose ? truncate(r.purpose, 60) : null, field(r, 'agent_profile') ? compactRef(field(r, 'agent_profile')) : null], open(r)) },
      { key: 'instanceOwner', render: instanceOwner },
      { key: 'status', render: r => e('div', { class: 'badges' }, badge(r.status), field(r, 'child_session_state') ? badge(field(r, 'child_session_state')) : null) },
      { key: 'latestTask', render: latestInstanceTask },
      { key: 'lastUsed', class: 'nowrap wide-only', render: r => timeCell(field(r, 'last_used_at', field(r, 'created_at'))) },
      { key: 'actions', render: r => e('div', { class: 'actions' }, r.status !== 'archived' && capability(snapshot, 'followup', r) ? button('followup', () => openInstanceTask(r), 'small') : null) }
    ], filtered(rows, options), { rowLink: open }), e('p', { class: 'note', text: t('instanceTitleNote') })];
  }
  function renderInstance(snapshot) {
    const item = snapshot.instance; if (!item) return [title('instances'), empty('unavailable')];
    const childSession = field(item, 'child_session_id');
    const tasks = list(item, 'tasks');
    return [literalTitle(instanceName(item), e('span', { class: 'title-meta' }, e('span', { class: 'mono', text: instanceID(item) || '' }), e('span', { text: t('instancesDesc') })), [refreshButton(), ...instanceActions(snapshot, item)]),
      e('div', { class: 'detail-grid' },
        e('div', { class: 'stack' },
          section('instancePurpose', e('div', { class: 'panel-body' }, item.purpose ? e('p', { class: 'prose', text: item.purpose }) : e('p', { class: 'muted', text: t('noPurpose') }))),
          section('instanceTasks', dataTable([
            { key: 'tasks', render: r => cellTitle(taskTitle(r), [idText(taskID(r))], () => navigate('task', { task_id: taskID(r) })) },
            { key: 'source', render: r => e('div', { class: 'badges' }, r.source ? chip(kindLabel(r.source), 'quiet') : null, scheduleLink(r)) },
            { key: 'result', render: r => badge(r.status) },
            { key: 'requestedAt', class: 'nowrap', render: r => timeCell(r.requested_at) }
          ], tasks, { pageKey: 'instance-tasks', pageSize: 20, emptyKey: 'noInstanceTasks', emptyHint: false, rowLink: r => () => navigate('task', { task_id: taskID(r) }) }), chip(String(Number(item.task_count || tasks.length))))),
        e('aside', { class: 'stack' }, section('details', e('div', { class: 'panel-body' }, definition([
          ['instanceOwner', instanceOwner(item)],
          ['status', badge(item.status)], ['sessionState', badge(field(item, 'child_session_state'))], ['profile', refText(field(item, 'agent_profile'))],
          ['agentType', field(item, 'agent_type')], ['model', refText(field(item, 'model_ref'))], ['permissionPolicy', field(item, 'permission_policy')],
          ['target', targetLabel(item)], ['createdAt', timeCell(field(item, 'created_at'))], ['lastUsed', timeCell(field(item, 'last_used_at'))],
          childSession ? ['childSession', linkTo(shortID(childSession), 'session', { name: childSession, target: target(item) })] : null]), e('details', {}, e('summary', { text: t('rawRecord') }), code(item))))))];
  }

  // ---------------------------------------------------------------------------
  // Schedules.
  // ---------------------------------------------------------------------------
  // last_status is "ok:task:<id>", "skipped:<why>", "error: <detail>" or a state.
  function parseRunStatus(raw) {
    const text = String(raw || '');
    const match = text.match(/^(ok|error|skipped|submitting|failed)(?::\s*([\s\S]*))?$/);
    if (!match) return { status: text, taskID: '', detail: '' };
    const rest = match[2] || '';
    const task = rest.match(/^task:(\S+)$/);
    return { status: match[1], taskID: task ? task[1] : '', detail: task ? '' : rest };
  }
  function scheduleTypeLabel(type) { return type === 'session-prompt' ? t('sessionPrompt') : type === 'subtask' ? t('subtask') : type || t('unknown'); }
  const scheduleID = row => row.id || row.schedule_id;
  function scheduleToggle(snapshot, row, small) {
    const action = row.enabled ? 'disable' : 'enable';
    return actionButton(snapshot, 'schedules', action, { schedule_id: scheduleID(row) }, { key: action, item: row, name: row.name || '#' + scheduleID(row), tone: small ? 'small' : '' });
  }
  function renderSchedules(snapshot) {
    const next = row => { const d = row.enabled && toDate(row.next_run_at); return d ? d.valueOf() : Infinity; };
    const rows = list(snapshot, 'schedules').slice().sort((a, b) => (b.enabled === true) - (a.enabled === true) || (next(a) === next(b) ? 0 : next(a) - next(b)) || scheduleID(a) - scheduleID(b));
    const options = { stateOf: r => r.enabled ? 'enabled' : 'paused' };
    const open = r => () => navigate('schedule', { schedule_id: scheduleID(r) });
    return [title('schedules', 'schedulesDesc', [refreshButton(), capability(snapshot, 'create') ? button('newSchedule', () => openScheduleForm(), 'primary', 'plus') : null]), filters(rows, options), dataTable([
      { key: 'schedules', render: r => cellTitle(r.name || '#' + scheduleID(r), ['#' + scheduleID(r), scheduleTypeLabel(r.schedule_type)], open(r)) },
      { key: 'spec', render: r => e('code', { class: 'spec', text: r.spec || '' }) },
      { key: 'status', render: r => badge(r.enabled ? 'enabled' : 'paused') },
      { key: 'nextRun', class: 'nowrap', render: r => r.enabled ? timeCell(r.next_run_at) : dash() },
      { key: 'lastRun', render: r => { const last = parseRunStatus(r.last_status); return e('div', { class: 'badges' }, timeCell(r.last_run_at), last.status ? badge(last.status) : null); } },
      { key: 'actions', render: r => e('div', { class: 'actions' }, capability(snapshot, 'update', r) ? button('edit', () => openScheduleForm(r), 'small') : null, scheduleToggle(snapshot, r, true)) }
    ], filtered(rows, options), { rowLink: open }), e('p', { class: 'note', text: t('schedulePauseNote') })];
  }
  function renderSchedule(snapshot) {
    const item = snapshot.schedule; if (!item) return [title('schedule'), empty('unavailable')];
    const id = scheduleID(item);
    const task = snapshot.task || item.task || null;
    const last = parseRunStatus(item.last_status);
    const runs = (list(snapshot, 'runs').length ? list(snapshot, 'runs') : list(item, 'runs')).slice().sort((a, b) => Number(b.fired_at || 0) - Number(a.fired_at || 0));
    const content = item.schedule_type === 'subtask' && task
      ? [definition([['taskProfile', refText(task.agent)], ['agentType', task.runtime], ['model', refText(task.model)], ['reasoningEffort', task.reasoning_effort]]), task.task ? e('p', { class: 'prose', text: task.task }) : null]
      : item.prompt ? e('p', { class: 'prose', text: item.prompt }) : dash();
    return [literalTitle(item.name || '#' + id, scheduleTypeLabel(item.schedule_type), [refreshButton(), button('scheduleTaskRecords', () => navigate('tasks', { schedule_id: id })), capability(snapshot, 'update', item) ? button('edit', () => openScheduleForm(item)) : null, scheduleToggle(snapshot, item), actionButton(snapshot, 'schedules', 'delete', { schedule_id: id }, { key: 'delete', item, name: item.name || '#' + id, tone: 'danger' })]),
      e('div', { class: 'detail-grid' },
        e('div', { class: 'stack' },
          section('scheduleContent', e('div', { class: 'panel-body stack' }, content)),
          section('history', dataTable([
            { key: 'firedAt', render: row => timeCell(row.fired_at) },
            { key: 'status', render: row => { const parsed = parseRunStatus(row.status); return e('div', {}, badge(parsed.status), parsed.detail ? e('p', { class: 'cell-sub', text: parsed.detail }) : null); } },
            { key: 'taskID', render: row => { const tid = row.task_id || parseRunStatus(row.status).taskID; return tid ? linkTo(shortID(tid), 'task', { task_id: tid }) : dash(); } }
          ], runs, { pageSize: 20, emptyKey: 'noRuns', emptyHint: false }))),
        e('aside', { class: 'stack' }, section('details', e('div', { class: 'panel-body' }, definition([
          ['id', '#' + id], ['status', badge(item.enabled ? 'enabled' : 'paused')], ['spec', e('code', { class: 'spec', text: item.spec || '' })],
          ['target', item.target_name || item.target_id], ['sessionName', item.session_name], ['runMode', item.run_mode],
          ['nextRun', item.enabled ? timeCell(item.next_run_at) : ''], ['lastRun', timeCell(item.last_run_at)],
          ['lastResult', last.status ? e('div', { class: 'stack tight' }, e('div', { class: 'badges' }, badge(last.status), last.taskID ? linkTo(shortID(last.taskID), 'task', { task_id: last.taskID }) : null), last.detail ? e('p', { class: 'cell-sub', text: last.detail }) : null) : '']]))),
          e('p', { class: 'note', text: t('schedulePauseNote') })))];
  }

  // ---------------------------------------------------------------------------
  // Bots, chats, profiles, packs, credentials and settings.
  // ---------------------------------------------------------------------------
  const botRestartCommand = snapshot => snapshot.restart_command || 'amux system restart';
  function restartBanner(snapshot, key) {
    return e('div', { class: 'banner warn', role: 'status' }, icon('alert'), e('div', {}, e('p', { text: t(key) }), e('code', { class: 'mono', text: botRestartCommand(snapshot) })));
  }
  function renderBots(snapshot) {
    const rows = list(snapshot, 'bots');
    const settings = row => row.settings || {};
    const running = row => row.status === 'running';
    const pending = rows.filter(row => row.pending_restart === true);
    return [title('bots', 'botsDesc', [refreshButton(), capability(snapshot, 'create') ? button('addBot', () => openAddBot(snapshot), 'primary', 'plus') : null]),
      pending.length ? restartBanner(snapshot, 'botRestartPending') : null,
      filters(rows, { noState: true }), dataTable([
      { key: 'bots', render: row => cellTitle(display(row), [e('span', { class: 'mono', text: row.profile || row.id || '' }), row.app_id ? e('span', { class: 'mono', text: row.app_id }) : null]) },
      { key: 'status', render: row => e('div', { class: 'badges' }, badge(row.status), row.pending_restart ? chip(t(row.configured === 'disabled' ? 'botStopsOnRestart' : 'botStartsOnRestart'), 'warn', botRestartCommand(snapshot)) : null, row.error ? chip(t('botCredentialsUnreadable'), 'bad') : null) },
      { key: 'profile', render: row => running(row) ? refText(settings(row).default_agent || settings(row).agent_profile || settings(row).profile) : dash() },
      { key: 'agentType', render: row => running(row) ? e('div', {}, settings(row).default_runtime_ref || dash(), e('p', { class: 'cell-sub' }, settings(row).default_model_ref ? refText(settings(row).default_model_ref) : null)) : dash() },
      { key: 'interactionMode', class: 'wide-only', render: row => settings(row).interaction_mode ? optionLabel(settings(row).interaction_mode) : dash() },
      { key: 'actions', render: row => e('div', { class: 'actions' },
        running(row) ? button('open', () => openBotForm(row, false), 'small') : null,
        capability(snapshot, 'save', row) ? button('edit', () => openBotForm(row, true), 'small') : null,
        running(row) ? button('groups', () => navigate('groups', { profile: row.profile || row.id }), 'small quiet') : null,
        capability(snapshot, 'update_credentials', row) ? button('updateBotSecret', () => openBotSecret(row), 'small') : null,
        actionButton(snapshot, 'bots', 'enable', { profile: row.profile }, { key: 'enableBot', item: row, name: display(row), tone: 'small', successKey: 'botSavedRestart' }),
        capability(snapshot, 'disable', row) ? button('disableBot', () => openBotRemoval(row, 'disable'), 'danger small') : null,
        capability(snapshot, 'delete', row) ? button('deleteBot', () => openBotRemoval(row, 'delete'), 'danger small') : null) }
    ], filtered(rows), { emptyKey: 'noBots', emptyHint: 'noBotsHint' }), e('p', { class: 'note', text: t('botLifecycleNote') })];
  }
  // Group names come from the Bridge's stored names; the chat ID is the fallback.
  function groupTitle(row) { return [row.name, row.title, row.chat_name].find(value => value && value !== (row.chat_id || row.id)) || row.chat_id || row.id || t('unknown'); }
  const groupNamed = row => groupTitle(row) !== (row.chat_id || row.id || t('unknown'));
  // Names are fetched from Feishu only by an explicit refresh, never by a read.
  // A visit with unnamed groups refreshes once per console session.
  async function refreshGroupNames(snapshot, automatic) {
    const profile = state.route.params.profile || '';
    const attempted = state.groupNameRefreshes || (state.groupNameRefreshes = new Set());
    if (automatic && attempted.has(profile)) return;
    attempted.add(profile);
    try {
      const result = await write('groups', 'refresh_names', profile ? { profile } : {});
      if (!automatic) notify(t('groupNamesRefreshed', { resolved: Number(result.names_resolved || 0), missing: Number(result.names_missing || 0) }));
      if (state.route.page === 'groups') await loadRoute();
    } catch (error) { if (!automatic) notify(errorMessage(error, true), true); }
  }
  function renderGroups(snapshot) {
    const rows = list(snapshot, 'groups');
    const unnamed = rows.filter(row => !groupNamed(row)).length;
    const canRefresh = capability(snapshot, 'refresh_names');
    if (unnamed && canRefresh && state.online) root.setTimeout(() => refreshGroupNames(snapshot, true), 0);
    const refresh = canRefresh ? button('refreshGroupNames', () => { refresh.disabled = true; refreshGroupNames(snapshot, false); }, '', 'refresh') : null;
    if (refresh) refresh.disabled = !state.online;
    return [title('groups', 'groupsDesc', [refreshButton(), refresh]), e('p', { class: 'rail group-note', text: t('knownBindings') }),
      unnamed ? e('p', { class: 'help', text: t('groupNamesMissing', { count: unnamed }) }) : null,
      filters(rows, { noState: true }), dataTable([
      { key: 'groups', render: row => { const name = groupTitle(row); const id = row.chat_id || row.id || ''; return cellTitle(name, [name !== id ? e('span', { class: 'mono', text: id }) : chip(t('groupNameUnknown'), 'quiet')], () => openGroupForm(row, snapshot)); } },
      { key: 'bots', render: row => row.profile || snapshot.profile || dash() },
      { key: 'source', render: row => stateLabel(row.source || snapshot.source) },
      { key: 'nameUpdated', class: 'wide-only nowrap', render: row => row.name_updated_at ? timeCell(row.name_updated_at) : dash() },
      { key: 'actions', render: row => button('open', () => openGroupForm(row, snapshot), 'small') }
    ], filtered(rows), { rowLink: row => () => openGroupForm(row, snapshot) }), snapshot.settings && state.route.params.chat_id ? section('settings', e('div', { class: 'panel-body' }, definition([['source', snapshot.source], ['profile', snapshot.profile]]), code(snapshot.settings), capability(snapshot, 'save') ? button('edit', () => editGroupSettings(snapshot, snapshot.settings)) : null)) : null];
  }
  const profileRef = row => row.ref || row.profile || row.name || '';
  function profileNamespace(row) { const ref = profileRef(row); return ref.includes('/') ? ref.split('/')[0] : row.namespace || row.source || ''; }
  // Namespaces are identifiers, so a value with parentheses cannot collide with
  // one; it keeps "no namespace" distinct from the empty "all" filter value.
  const noNamespace = '(none)';
  function skillNames(row) { return list(row, 'skills').map(skill => typeof skill === 'string' ? skill : skill.name || skill.ref || '').filter(Boolean); }
  // Bot and group defaults, Schedules, persistent instances and Sessions can
  // refer to an expert; a Session restart may still require Pack assets.
  const usageKinds = [['bots', 'usageBots'], ['groups', 'usageGroups'], ['schedules', 'schedules'], ['instances', 'instances'], ['sessions', 'sessions']];
  const usageTotal = counts => usageKinds.reduce((sum, [kind]) => sum + Number(counts && counts[kind] || 0), 0);
  const usageBreakdown = counts => usageKinds.filter(([kind]) => Number(counts && counts[kind] || 0)).map(([kind, label]) => t(label) + ' ' + counts[kind]).join(' · ');
  const usageSourceNames = sources => sources.map(source => t(usageKinds.some(([kind]) => kind === source) ? usageKinds.find(([kind]) => kind === source)[1] : source)).join(', ');
  function usageChip(counts, incomplete) {
    if (usageTotal(counts)) return chip(t('usedBy', { count: usageTotal(counts) }), 'blue', usageBreakdown(counts));
    return chip(t(incomplete ? 'usageUnknown' : 'unusedExpert'), incomplete ? 'warn' : 'quiet');
  }
  function usageItemLink(kind, item) {
    if (kind === 'sessions') return linkTo(item.name || item.id, 'session', { name: item.name, target: item.target });
    if (kind === 'schedules') return linkTo(item.name || item.id, 'schedule', { schedule_id: item.id });
    if (kind === 'instances') return linkTo(item.name || item.id, 'instance', { agent_instance_id: item.id });
    if (kind === 'groups') return linkTo(item.name || item.id, 'groups', {});
    return linkTo(item.name || item.id, 'bots', {});
  }
  function usageList(kind, items, count) {
    return e('ul', { class: 'usage-list' }, items.map(item => e('li', {}, usageItemLink(kind, item), item.state ? badge(item.state) : null)), count > items.length ? e('li', { class: 'muted', text: t('andMore', { count: count - items.length }) }) : null);
  }
  function usageSection(usage) {
    if (!usage || typeof usage !== 'object') return null;
    const counts = usage.counts || {}; const items = usage.items || {}; const missing = list(usage, 'unavailable');
    const total = usageTotal(counts);
    return section('usageTitle', e('div', { class: 'panel-body stack' },
      missing.length ? e('p', { class: 'banner', role: 'status', text: t('usageIncomplete', { sources: usageSourceNames(missing) }) }) : null,
      total ? usageKinds.filter(([kind]) => Number(counts[kind] || 0)).map(([kind, label]) => e('div', {}, e('h3', { class: 'subhead' }, t(label), ' ', chip(String(counts[kind]))), usageList(kind, list(items, kind), Number(counts[kind])))) : missing.length ? null : empty('noUsage', false, true),
      e('p', { class: 'help', text: t('usageCoverageNote') }), e('p', { class: 'help', text: t('usageSessionsNote') })), chip(String(total)));
  }
  function renderProfiles(snapshot) {
    const rows = list(snapshot, 'profiles').slice().sort((a, b) => profileRef(a).localeCompare(profileRef(b)));
    const f = filterState('profiles');
    const usage = snapshot.usage && typeof snapshot.usage === 'object' ? snapshot.usage : null;
    const unavailable = list(snapshot, 'usage_unavailable');
    const incomplete = unavailable.length > 0;
    if (incomplete && f.use === 'unused') f.use = '';
    const inUse = row => usageTotal(usage && usage[profileRef(row)]) > 0;
    const namespaces = [...rows.reduce((map, row) => map.set(profileNamespace(row), (map.get(profileNamespace(row)) || 0) + 1), new Map()).entries()];
    const wanted = f.namespace === noNamespace ? '' : f.namespace;
    const visible = filtered(rows).filter(row => !f.namespace || profileNamespace(row) === wanted).filter(row => !usage || !f.use || inUse(row) === (f.use === 'used'));
    const cards = visible.map(row => {
      const ref = profileRef(row); const open = () => navigate('profile', { ref });
      const name = row.name || row.display_name || ref.split('/').slice(1).join('/') || ref;
      const skills = skillNames(row);
      return e('section', { class: 'panel profile-card clickable', onclick: rowClick(open) },
        e('header', {}, e('h2', { text: name }), chip(profileNamespace(row) || t('unknown'), 'blue')),
        e('code', { class: 'muted ref', title: ref, text: shortRef(ref) }),
        row.description ? e('p', { class: 'clamp', text: row.description }) : e('p', { class: 'muted', text: t('noDescription') }),
        skills.length ? e('div', { class: 'chips' }, skills.slice(0, 6).map(skill => chip(skill)), skills.length > 6 ? chip('+' + (skills.length - 6), 'quiet') : null) : null,
        e('footer', {}, e('span', { class: 'muted', text: row.version || row.revision || t('skillCount', { count: skills.length }) }), usage ? usageChip(usage[ref], incomplete) : null, button('definition', open, 'small')));
    });
    return [title('profiles', 'profilesDesc', refreshButton()),
      namespaces.length > 1 || f.namespace ? tabs([['', t('all'), rows.length], ...namespaces.map(([ns, count]) => [ns || noNamespace, ns || t('unknown'), count])], f.namespace, value => { f.namespace = value; renderPage(); }) : null,
      usage && unavailable.length ? e('p', { class: 'banner', role: 'status', text: t('usageIncomplete', { sources: usageSourceNames(unavailable) }) }) : null,
      usage ? e('p', { class: 'help', text: t('usageCoverageNote') }) : null,
      filters(rows, { noState: true, extra: usage ? [selectOptions([{ value: '', label: t('allExperts') }, { value: 'used', label: t('inUse') + ' (' + rows.filter(inUse).length + ')' }, ...(!incomplete ? [{ value: 'unused', label: t('unusedExpert') + ' (' + rows.filter(row => !inUse(row)).length + ')' }] : [])], f.use, v => { f.use = v; f.page = 1; renderPage(); }, { 'aria-label': t('usageFilter') })] : [] }),
      visible.length ? e('div', { class: 'profile-grid' }, cards) : empty()];
  }
  function renderProfile(snapshot) {
    const item = snapshot.profile; if (!item) return [title('profiles'), empty('unavailable')];
    const skills = list(item, 'skills');
    const placement = item.placement || {}; const requires = placement.requires || {};
    const joined = values => values.filter(Boolean).join(', ');
    const pack = snapshot.pack && snapshot.pack.source_kind !== 'builtin' ? snapshot.pack : null;
    return [literalTitle(item.name || profileRef(item), e('span', { class: 'mono', text: profileRef(item) }), refreshButton()),
      e('div', { class: 'detail-grid' },
        e('div', { class: 'stack' },
          item.description ? section('description', e('div', { class: 'panel-body' }, e('p', { class: 'prose', text: item.description }))) : null,
          item.instructions || item.system_prompt || item.prompt ? section('instructions', e('div', { class: 'panel-body' }, code(item.instructions || item.system_prompt || item.prompt))) : null,
          section('skills', skills.length ? e('div', { class: 'skill-list' }, skills.map(skill => typeof skill === 'string' ? e('div', { class: 'skill' }, e('strong', { text: skill })) : e('div', { class: 'skill' }, e('strong', { text: skill.name || skill.ref || '' }), skill.description || skill.source ? e('p', { class: 'help', text: skill.description || skill.source }) : null))) : empty('noSkills', false, true), chip(String(skills.length))),
          usageSection(snapshot.usage)),
        e('aside', { class: 'stack' }, section('details', e('div', { class: 'panel-body' }, definition([
          ['profile', e('span', { class: 'mono path', text: profileRef(item) })], ['source', item.namespace || item.source || item.pack], ['version', item.version || item.revision],
          pack ? ['providedBy', linkTo(pack.namespace, 'packs', {})] : null,
          pack ? ['packSource', pack.source_host || pack.source_kind] : null,
          pack ? ['currentRevision', revisionText(pack.current_revision)] : null,
          pack ? ['lastSynced', timeCell(pack.last_synced_at)] : null,
          pack && (pack.auto_sync || pack.error_code || pack.managed_by) ? ['flags', packFlags(pack)] : null,
          ['instructionFiles', item.agents], ['preferredTargets', joined(list(placement, 'preferred_targets'))],
          ['requiredCredentials', joined(list(requires, 'credentials'))], ['requiredAgentTypes', joined(list(requires, 'agent_types'))]].filter(Boolean)),
          e('details', {}, e('summary', { text: t('rawRecord') }), code(item))))))];
  }
  function packFlags(row) {
    const development = row.development || null;
    return e('div', { class: 'chips' }, row.temporary ? chip(t('temporaryPack'), 'warn', development && development.expires_at ? t('expiresAt', { time: date(development.expires_at) }) : null) : null, row.development_of ? chip(t('developmentPack'), 'blue', row.development_of) : null,
      development && development.branch ? chip(development.branch, 'mono', t('developmentBranch')) : null,
      development && development.cleanup_error_code ? chip(t('cleanupBlocked'), 'bad', has('reason.' + development.cleanup_error_code) ? t('reason.' + development.cleanup_error_code) : null) : null,
      row.auto_sync ? chip(t('autoSync'), 'good', row.sync_interval || null) : null, row.error_code ? chip(t('syncFailed'), 'bad') : null, row.managed_by ? chip(t('managedPack'), '', row.managed_by) : null);
  }
  const revisionText = rev => rev ? e('span', { class: 'mono', title: String(rev), text: String(rev).slice(0, 12) }) : dash();
  function renderPacks(snapshot) {
    const rows = list(snapshot, 'packs').slice().sort((a, b) => String(a.namespace || '').localeCompare(String(b.namespace || '')));
    return [title('packs', 'packsDesc', [refreshButton(), capability(snapshot, 'install') ? button('install', openInstallPack, 'primary', 'plus') : null]), filters(rows, { noState: true }), dataTable([
      { key: 'packs', render: row => cellTitle(row.namespace || row.name || row.id || '', [row.display_name, row.source_host || row.source_kind]) },
      { key: 'experts', render: row => own(row, 'profile_count') ? e('button', { type: 'button', class: 'link-button', title: t('viewExperts'), 'aria-label': t('viewPackExperts', { count: row.profile_count, name: row.namespace }), text: String(row.profile_count), onclick: () => viewPackExperts(row.namespace) }) : dash() },
      { key: 'ref', class: 'wide-only', render: row => row.ref ? e('span', { class: 'mono', text: row.ref }) : dash() },
      { key: 'currentRevision', render: row => revisionText(row.current_revision || row.revision) },
      { key: 'lastSynced', render: row => timeCell(row.last_synced_at) },
      { key: 'flags', render: packFlags },
      { key: 'actions', render: row => e('div', { class: 'actions' }, button('details', () => showPack(row, snapshot), 'small'), ownedPack(row) ? actionButton(snapshot, 'packs', 'sync', { namespace: row.namespace || row.name }, { key: 'sync', item: row, name: row.namespace, tone: 'small' }) : null, ownedPack(row) && capability(snapshot, 'rollback', row) && list(row, 'versions').length > 1 ? button('rollback', () => openRollbackPack(row), 'danger small') : null, capability(snapshot, 'remove', row) && removablePack(row) ? button('removePack', () => openRemovePack(row), 'danger small') : null) }
    ], filtered(rows), { emptyKey: 'noPacks', emptyHint: 'noPacksHint' })];
  }
  // Managed and evaluation packs belong to amux: the console never syncs or rolls
  // them back. Temporary development and evaluation packs can be removed.
  const ownedPack = pack => !pack.managed_by && !pack.temporary && !pack.evaluation;
  const removablePack = pack => ownedPack(pack) || pack.temporary === true;
  function viewPackExperts(namespace) {
    const f = filterState('profiles'); f.namespace = namespace; f.use = ''; f.page = 1; navigate('profiles');
  }
  function showPack(pack, snapshot) {
    const canRollBack = ownedPack(pack) && capability(snapshot || {}, 'rollback', pack);
    const current = pack.current_revision || '';
    modal('details', body => body.append(definition([['namespace', pack.namespace], ['experts', own(pack, 'profile_count') ? String(pack.profile_count) : ''], ['source', pack.source_host || pack.source_kind], ['ref', pack.ref], ['currentRevision', e('span', { class: 'mono path', text: current })], ['lastSynced', timeCell(pack.last_synced_at)], ['flags', packFlags(pack)]]),
      e('h3', { class: 'subhead', text: t('versions') }),
      list(pack, 'versions').length ? e('table', { class: 'table compact' }, e('thead', {}, e('tr', {}, ['revision', 'ref', 'lastSynced', 'actions'].map(key => e('th', { text: t(key) })))), e('tbody', {}, list(pack, 'versions').map(v => e('tr', {}, e('td', {}, revisionText(v.revision), v.revision && v.revision === current ? chip(t('currentMark'), 'good') : null), e('td', { text: v.ref || '—' }), e('td', {}, timeCell(v.synced_at)), e('td', {}, canRollBack && v.revision && v.revision !== current ? button('rollbackTo', () => openRollbackPack(pack, v.revision), 'danger small') : null))))) : dash(),
      e('div', { class: 'form-actions' }, button('close', () => closeModal()))));
  }
  function nameChips(values) { return values.length ? e('div', { class: 'chips' }, values.map(name => chip(name, 'mono'))) : dash(); }
  // Credential values are write-only: the page lists names, and a value travels
  // only from a password field to the server, never back and never into a draft.
  function credentialNameChips(snapshot, row, names, action) {
    if (!names.length) return dash();
    const removable = capability(snapshot, action);
    return e('div', { class: 'chips' }, names.map(name => e('span', { class: 'chip mono' }, name,
      removable ? e('button', { type: 'button', class: 'chip-remove', 'aria-label': t(action === 'unset_env' ? 'removeEnv' : 'removeFile', { name }), title: t(action === 'unset_env' ? 'removeEnv' : 'removeFile', { name }), text: '×', onclick: () => removeCredentialEntry(row, action, name) }) : null)));
  }
  async function removeCredentialEntry(row, action, name) {
    if (!await confirmAction(action === 'unset_env' ? 'removeEnvAction' : 'removeFileAction', row.profile + ' / ' + name, true)) return;
    try { await write('credentials', action, { profile: row.profile, name }); notify(t('saved')); await loadRoute(); }
    catch (error) { notify(errorMessage(error, true), true); }
  }
  function renderCredentials(snapshot) {
    const rows = list(snapshot, 'profiles').length ? list(snapshot, 'profiles') : list(snapshot, 'credentials');
    const canWrite = capability(snapshot, 'set_env');
    return [title('credentials', 'credentialsDesc', [refreshButton(), canWrite ? button('newCredential', () => openCredentialValue(null, 'env'), 'primary', 'plus') : null]), e('div', { class: 'rail secret-note' }, icon('shield'), t('secretWriteOnly')), filters(rows, { noState: true }), dataTable([
      { key: 'credentialProfile', render: row => e('strong', { class: 'mono', text: row.profile || row.name || '' }) }, { key: 'kind', render: row => stateLabel(row.kind) },
      { key: 'envNames', render: row => credentialNameChips(snapshot, row, list(row, 'env_names'), 'unset_env') }, { key: 'fileNames', render: row => credentialNameChips(snapshot, row, list(row, 'file_names'), 'unset_file') },
      { key: 'actions', render: row => e('div', { class: 'actions' },
        canWrite ? button('setEnv', () => openCredentialValue(row, 'env'), 'small') : null,
        capability(snapshot, 'set_file') ? button('setFile', () => openCredentialValue(row, 'file'), 'small') : null,
        capability(snapshot, 'delete') ? button('deleteCredential', () => openDeleteCredential(row), 'danger small') : null) }
    ], filtered(rows), { emptyKey: 'noCredentials', emptyHint: 'noCredentialsHint' }), e('p', { class: 'note', text: t('credentialUseNote') })];
  }
  // Setting a name that exists replaces its value; any other name is added.
  function openCredentialValue(row, kind) {
    const file = kind === 'file';
    formDialog(file ? 'setFile' : 'setEnv', [
      formField('profile', 'credentialProfile', 'text', row ? row.profile : '', { required: true, readonly: Boolean(row), pattern: '[A-Za-z0-9_-]+', help: row ? null : 'credentialProfileHelp' }),
      formField('name', file ? 'credentialFileName' : 'credentialEnvName', 'text', '', { required: true, pattern: file ? null : '[A-Za-z_][A-Za-z0-9_]*', help: file ? 'credentialFileNameHelp' : 'credentialEnvNameHelp' }),
      formField('value', file ? 'credentialFileContent' : 'credentialValue', file ? 'textarea' : 'password', '', { required: true, help: 'credentialValueHelp' })
    ], data => write('credentials', file ? 'set_file' : 'set_env', { profile: data.profile.trim(), name: data.name.trim(), value: data.value }), { noDraft: true });
  }
  function openDeleteCredential(row) {
    formDialog('deleteCredential', [e('p', { class: 'prose', text: t('deleteCredentialLead', { profile: row.profile }) }), formField('confirm', 'typeProfileToConfirm', 'text', '', { required: true, help: 'typeProfileToConfirmHelp' })], data => {
      if (data.confirm.trim() !== row.profile) throw new APIError(t('confirmationMismatch'));
      return write('credentials', 'delete', { profile: row.profile, confirm: row.profile });
    }, { submitKey: 'deleteCredential', noDraft: true });
  }
  // Settings come from the configuration reference: the server sends each key's
  // section, bilingual description, default, constraints and control kind. A few
  // common keys also get a short label here; every other key shows its own name.
  const settingLabels = {
    'bridge.feishu.group_settings.enabled': 'setting.groupSettings',
    'bridge.feishu.presentation.thinking_process': 'setting.thinking',
    'bridge.feishu.presentation.completion_notification_enabled': 'setting.completion',
    'bridge.feishu.presentation.completion_notification_after_seconds': 'setting.completionAfter',
    'maintenance.daily_cleanup.enabled': 'setting.cleanup',
    'maintenance.daily_cleanup.at': 'setting.cleanupAt'
  };
  const localized = value => value && typeof value === 'object' ? value[language()] || value.en || '' : String(value || '');
  function settingControl(row, value, writable, onchange) {
    const attrs = { id: 'setting-' + row.key, name: row.key, disabled: !writable };
    if (row.kind === 'boolean' || row.kind === 'enum') {
      const options = row.kind === 'boolean' ? [{ value: 'true', label: t('booleanTrue') }, { value: 'false', label: t('booleanFalse') }] : list(row, 'choices').map(v => ({ value: v, label: optionLabel(v) }));
      if (value === '') options.unshift({ value: '', label: t('notSet') });
      else if (!options.some(o => o.value === value)) options.push({ value, label: value });
      return selectOptions(options, value, onchange, attrs);
    }
    const type = row.kind === 'integer' ? 'number' : row.kind === 'time' ? 'time' : 'text';
    return e('input', Object.assign(attrs, { value, type, step: type === 'number' ? '1' : null, placeholder: row.kind === 'list' ? t('listPlaceholder') : null, oninput: ev => onchange(ev.target.value) }));
  }
  function renderSettings(snapshot) {
    const rows = list(snapshot, 'settings');
    const key = 'settings-draft:' + snapshot.version;
    const draft = state.drafts.get(key) || { values: {}, reset: {} };
    const f = filterState('settings');
    const canSave = capability(snapshot, 'save');
    const byKey = new Map(rows.map(row => [row.key, row]));
    const changed = () => Object.keys(draft.values).filter(k => byKey.has(k) && String(byKey.get(k).value ?? '') !== draft.values[k]);
    const resets = () => Object.keys(draft.reset).filter(k => draft.reset[k] && byKey.has(k) && !byKey.get(k).inherited);
    const pending = () => changed().length + resets().length;
    const changeNote = e('span', { class: 'chip warn', hidden: !pending(), text: t('dirty') });
    const save = button('save', async () => {
      const values = Object.fromEntries(changed().filter(k => !draft.reset[k]).map(k => [k, draft.values[k]]));
      const reset = resets();
      if (!Object.keys(values).length && !reset.length) return;
      save.disabled = true;
      try { const response = await write('settings', 'save', { expected_version: snapshot.version, values, reset }); state.drafts.delete(key); notify(t(response.restart_required ? 'restartRequired' : 'saved')); await loadRoute(); }
      catch (error) { notify(errorMessage(error, true), true); save.disabled = !state.online; }
    }, 'primary');
    const refreshDirty = () => { const count = pending(); save.disabled = !state.online || !count; changeNote.hidden = !count; };
    save.disabled = !state.online || !pending();
    const remember = () => state.drafts.set(key, draft);
    const query = f.query.toLocaleLowerCase();
    const matches = row => (!query || [row.key, localized(row.description), settingLabels[row.key] ? t(settingLabels[row.key]) : '', localized(row.section_label)].join(' ').toLocaleLowerCase().includes(query))
      && (f.kind !== 'experimental' || row.experimental) && (f.kind !== 'customized' || !row.inherited) && (f.kind !== 'editable' || row.writable);
    const sections = new Map();
    for (const row of rows.filter(matches)) {
      const name = row.section || 'other';
      const editable = canSave && row.writable === true;
      const resetting = Boolean(draft.reset[row.key]);
      const value = own(draft.values, row.key) ? draft.values[row.key] : String(row.value ?? '');
      const control = settingControl(row, value, editable && !resetting, next => { draft.values[row.key] = next; remember(); refreshDirty(); });
      const description = localized(row.description); const fallback = localized(row.default); const constraints = localized(row.constraints);
      const resetButton = editable && !row.inherited ? e('button', { type: 'button', class: 'ui-btn small quiet', 'aria-pressed': String(resetting), text: t(resetting ? 'undoReset' : 'resetToDefault'), onclick: () => { draft.reset[row.key] = !resetting; delete draft.values[row.key]; remember(); renderPage(); } }) : null;
      const node = e('div', { class: 'setting-row' + (resetting ? ' resetting' : '') },
        e('div', { class: 'setting-label' }, e('label', { for: 'setting-' + row.key, text: settingLabels[row.key] ? t(settingLabels[row.key]) : row.key }), settingLabels[row.key] ? e('code', { class: 'muted', text: row.key }) : null,
          description ? e('p', { class: 'help', text: description }) : null,
          e('p', { class: 'help setting-meta' }, fallback ? t('settingDefault', { value: fallback }) : null, constraints ? [fallback ? ' · ' : '', t('settingConstraints', { value: constraints })] : null)),
        e('div', { class: 'setting-control' }, control, e('div', { class: 'chips' }, row.inherited ? chip(t('defaultValue'), 'quiet') : chip(t('customized'), 'blue'), row.experimental ? chip(t('experimental'), 'warn') : null, row.writable === false ? chip(t('readOnlySetting'), 'quiet', t('readOnlySettingHelp')) : null), resetButton));
      if (!sections.has(name)) sections.set(name, { label: localized(row.section_label) || row.section || t('settingsOther'), experimental: false, nodes: [] });
      const entry = sections.get(name);
      entry.experimental = entry.experimental || row.experimental === true;
      entry.nodes.push(node);
    }
    const kinds = [['', t('all'), rows.length], ['experimental', t('experimental'), rows.filter(r => r.experimental).length], ['customized', t('customized'), rows.filter(r => !r.inherited).length], ['editable', t('editableSettings'), rows.filter(r => r.writable).length]];
    const search = e('input', { type: 'search', value: f.query, placeholder: t('filterSettings'), 'aria-label': t('filterSettings'), oninput: ev => {
      f.query = ev.target.value; const cursor = ev.target.selectionStart; renderPage();
      const input = document.querySelector('.filterbar input'); if (input) { input.focus(); input.setSelectionRange(cursor, cursor); }
    } });
    const open = Boolean(query || f.kind);
    // Sections keep the operator's expand/collapse choice across re-renders.
    const expanded = f.sections || (f.sections = {});
    return [title('settings', 'settingsDesc', [refreshButton(), changeNote, canSave ? save : null]),
      e('div', { class: 'banner', role: 'note' }, icon('alert'), t('restartNotice')),
      tabs(kinds.map(([value, label, count]) => [value, label, count]), f.kind, value => { f.kind = value; renderPage(); }),
      e('div', { class: 'filterbar' }, search),
      sections.size ? e('div', { class: 'stack' }, [...sections.entries()].map(([name, entry]) => e('details', { class: 'panel setting-section', open: (open || (own(expanded, name) ? expanded[name] : entry.experimental || entry.nodes.length <= 3)) || null, ontoggle: ev => { expanded[name] = ev.currentTarget.open; } },
        e('summary', {}, e('span', { class: 'setting-section-title', text: entry.label }), entry.experimental ? chip(t('experimental'), 'warn') : null, chip(String(entry.nodes.length), 'quiet')),
        e('div', { class: 'setting-list' }, entry.nodes)))) : empty()];
  }

  // ---------------------------------------------------------------------------
  // Dialogs. Drafts stay in memory only: no messages, credentials or tokens
  // enter storage.
  // ---------------------------------------------------------------------------
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
    const backdrop = e('div', { class: 'backdrop', onclick: ev => { if (ev.target === backdrop) closeModal(); } }, panel);
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
    else input = e(type === 'textarea' ? 'textarea' : 'input', { id, name, type: type === 'textarea' ? null : type || 'text', value: value ?? '', required: opts.required, readonly: opts.readonly, autocomplete: type === 'password' ? 'new-password' : 'off', spellcheck: 'false', rows: type === 'textarea' ? opts.rows || '5' : null, pattern: opts.pattern || null, 'aria-describedby': opts.help ? id + '-help' : null });
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
        if (opts.confirm && !root.confirm(typeof opts.confirm === 'function' ? opts.confirm(data) : t('confirmDanger', { action: t(key), name: opts.confirm }))) return;
        current.pending = true; save.disabled = true; save.textContent = t('saving'); errorHost.replaceChildren();
        try {
          const result = await submit(data); state.drafts.delete(draftKey); if (state.modal === current) closeModal(true); notify(t(opts.successKey && opts.successKey(result) || 'saved')); await loadRoute();
        } catch (error) {
          if (state.modal === current) errorHost.textContent = errorMessage(error, true);
          else notify(errorMessage(error, true), true);
        } finally { current.pending = false; if (save.isConnected) { save.disabled = !state.online; save.textContent = t(opts.submitKey || 'save'); } }
      } }, errorHost, opts.help ? e('p', { class: 'help', text: t(opts.help) }) : null, fields, e('div', { class: 'form-actions' }, button('cancel', () => closeModal()), save));
      // Secret forms keep no draft: a value typed into them exists only in the field.
      const draft = opts.noDraft ? null : state.drafts.get(draftKey);
      if (draft) for (const input of form.querySelectorAll('[name]')) if (own(draft, input.name) && !input.readOnly) input.value = draft[input.name];
      const capture = () => state.drafts.set(draftKey, Object.fromEntries(new FormData(form)));
      if (!opts.noDraft) { form.addEventListener('input', capture); form.addEventListener('change', capture); }
      body.appendChild(form);
    }, { rebuild: opts.rebuild });
  }
  function showObject(key, object) { modal(key, body => body.append(code(object), e('div', { class: 'form-actions' }, button('close', () => closeModal())))); }
  function confirmAction(action, name, danger) {
    return new Promise(resolve => {
      modal('confirm', (body, current) => {
        body.appendChild(e('p', { class: 'prose', text: t(danger ? 'confirmDanger' : 'confirmAction', { action: t(action), name: name || t('unknown') }) }));
        const finish = value => { current.resolved = true; closeModal(true); resolve(value); };
        current.onClose = () => { if (!current.resolved) resolve(false); };
        body.appendChild(e('div', { class: 'form-actions' }, button('cancel', () => finish(false)), button('confirm', () => finish(true), danger ? 'danger solid' : 'primary')));
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
    formDialog(action === 'steer' ? 'steer' : 'sendMessage', [formField('text', 'text', 'textarea', '', { required: true })], data => write('instances', action, { agent_instance_id: instanceID(row), text: data.text }), { submitKey: 'send', help: action === 'steer' ? 'steerHelp' : 'sendMessageHelp' });
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
      if (item) payload.agent_instance_id = instanceID(item);
      await write('instances', item ? 'followup' : 'spawn', payload);
    }, { submitKey: 'create', draftKey: 'instance-task:' + (item ? instanceID(item) : 'new'), rebuild: saved => openInstanceTask(item, saved) });
  }
  async function openScheduleForm(item, values) {
    try {
      const edit = Boolean(item);
      if (edit && item.schedule_type === 'subtask' && !item.task) {
        const detail = await get('schedule', { schedule_id: scheduleID(item) });
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
        if (edit) payload.schedule_id = scheduleID(item);
        else payload.schedule_type = data.schedule_type;
        if ((!edit && data.schedule_type === 'subtask') || edit && item.schedule_type === 'subtask') {
          payload.task = taskData(data, item && item.task);
          if (!payload.task.agent || !payload.task.task) throw new APIError(t('required'));
        } else if (!edit) {
          for (const key of ['target_id', 'session_name', 'prompt', 'run_mode']) payload[key] = data[key];
          if (!payload.target_id || !payload.session_name || !payload.prompt) throw new APIError(t('required'));
        }
        await write('schedules', edit ? 'update' : 'create', payload);
      }, { submitKey: edit ? 'save' : 'create', draftKey: 'schedule:' + (item ? scheduleID(item) : 'new'), rebuild: saved => openScheduleForm(item, saved) });
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
      options: type === 'boolean' ? [{ value: 'true', label: t('booleanTrue') }, { value: 'false', label: t('booleanFalse') }] : Array.isArray(type) ? type.map(value => ({ value, label: optionLabel(value) })) : null
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
      const settings = bot.settings || {};
      modal('bots', body => body.append(definition([['bots', display(bot)], ['status', badge(bot.status)], ['version', String(bot.version ?? '')], ...botFieldDefinitions.map(([key, label, type]) => [label, typeof settings[key] === 'boolean' ? t(settings[key] ? 'booleanTrue' : 'booleanFalse') : Array.isArray(type) && settings[key] ? optionLabel(settings[key]) : settings[key]])]), e('div', { class: 'form-actions' }, button('close', () => closeModal()))));
      return;
    }
    const initial = Object.assign({}, bot.settings || {}, values || {});
    const requestID = root.crypto.randomUUID();
    formDialog('bots', [formField('profile', 'profile', 'text', bot.profile || bot.id, { readonly: true }), formField('expected_version', 'expectedVersion', 'text', bot.version ?? '', { readonly: true }), ...editableSettingFields(initial, botFieldDefinitions)], data => saveBotTransition({ profile: bot.profile || bot.id, expected_version: bot.version, settings: readSettingFields(data, botFieldDefinitions), request_id: requestID }), { help: 'appliesNew', draftKey: 'bot:' + bot.profile + ':' + bot.version, rebuild: saved => openBotForm(bot, editable, saved) });
  }
  // Bot credentials are write-only: the App Secret is sent once, checked with
  // Feishu by the server, and never shown again or kept in a draft.
  function openAddBot(snapshot) {
    formDialog('addBot', [
      formField('profile', 'botProfileName', 'text', '', { required: true, pattern: '[A-Za-z0-9_-]{1,64}', help: 'botProfileNameHelp' }),
      formField('app_id', 'appID', 'text', '', { required: true, pattern: 'cli_[A-Za-z0-9]{1,124}', help: 'appIDHelp' }),
      formField('app_secret', 'appSecret', 'password', '', { required: true }),
      formField('interaction_mode', 'interactionMode', 'text', '', { options: [{ value: '', label: t('inherit') }, { value: 'thread', label: optionLabel('thread') }, { value: 'singleton', label: optionLabel('singleton') }] })
    ], data => {
      const payload = { profile: data.profile.trim(), app_id: data.app_id.trim(), app_secret: data.app_secret };
      if (data.interaction_mode) payload.interaction_mode = data.interaction_mode;
      return write('bots', 'create', payload);
    }, { submitKey: 'addBot', help: 'addBotHelp', noDraft: true, successKey: () => 'botSavedRestart' });
  }
  function openBotSecret(bot) {
    formDialog('updateBotSecret', [formField('profile', 'botProfileName', 'text', bot.profile, { readonly: true }), formField('app_id', 'appID', 'text', bot.app_id || '', { readonly: true }), formField('app_secret', 'appSecret', 'password', '', { required: true })],
      data => write('bots', 'update_credentials', { profile: bot.profile, app_secret: data.app_secret }), { help: 'updateBotSecretHelp', noDraft: true, successKey: () => 'botSavedRestart' });
  }
  // Disabling and deleting name the Bot explicitly: the operator types its profile.
  function openBotRemoval(bot, action) {
    const key = action === 'delete' ? 'deleteBot' : 'disableBot';
    formDialog(key, [e('p', { class: 'prose', text: t(action === 'delete' ? 'deleteBotLead' : 'disableBotLead', { name: display(bot), profile: bot.profile }) }), formField('confirm', 'typeProfileToConfirm', 'text', '', { required: true, help: 'typeProfileToConfirmHelp' })], data => {
      if (data.confirm.trim() !== bot.profile) throw new APIError(t('confirmationMismatch'));
      return write('bots', action, { profile: bot.profile, confirm: bot.profile });
    }, { submitKey: key, noDraft: true, successKey: () => action === 'delete' ? 'saved' : 'botSavedRestart' });
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
    const groupDefinitions = [...botFieldDefinitions, ['mode', 'governance', ['owner_only', 'shared']], ['thinking_process', 'thinkingProcess', ['collapsed', 'expanded']], ['default_target', 'defaultTarget'], ['default_cwd', 'cwd'], ['idle_timeout_seconds', 'workspaceIdleTimeout', 'number'], ['thread_context_limit', 'threadContextLimit', 'number'], ['max_sessions_total', 'maxSessions', 'number'], ['max_sessions_per_user', 'maxSessionsPerUser', 'number']];
    const initial = Object.assign({}, settings, values || {});
    if (settings.idle_timeout_inherited && values?.idle_timeout_seconds == null) initial.idle_timeout_seconds = '';
    const advanced = e('details', {}, e('summary', { text: t('advanced') }), ...editableSettingFields(initial, groupDefinitions.slice(6)), formField('default_args', 'nativeArgs', 'textarea', values && values.default_args !== undefined ? values.default_args : list(settings, 'default_args').join('\n')));
    formDialog('groups', [formField('profile', 'profile', 'text', snapshot.profile, { readonly: true }), formField('chat_id', 'chatID', 'text', snapshot.chat_id || state.route.params.chat_id, { readonly: true }), e('p', { class: 'rail', text: t('source') + ': ' + t(snapshot.settings_source === 'bot_defaults' ? 'botDefaults' : 'savedSettings') }), ...editableSettingFields(initial, groupDefinitions.slice(0, 6)), advanced], data => {
      const next = Object.assign({}, settings, readSettingFields(data, groupDefinitions), { idle_timeout_inherited: String(data.idle_timeout_seconds ?? '').trim() === '', default_args: String(data.default_args || '').split('\n').filter(Boolean) });
      return write('groups', 'save', { profile: snapshot.profile, chat_id: snapshot.chat_id || state.route.params.chat_id, expected_version: settings.version || 0, settings: next });
    }, { draftKey: 'group:' + snapshot.profile + ':' + (snapshot.chat_id || state.route.params.chat_id) + ':' + settings.version, rebuild: saved => editGroupSettings(snapshot, settings, saved) });
  }
  function openInstallPack() {
    formDialog('install', [formField('namespace', 'namespace', 'text', '', { required: true }), formField('url', 'source', 'text', '', { required: true }), formField('ref', 'revision', 'text', '')], data => write('packs', 'install', Object.assign(data, { confirm: true })), { submitKey: 'install', confirm: t('packs') });
  }
  function openRollbackPack(pack, revision) {
    const revisions = list(pack, 'versions').length ? list(pack, 'versions') : list(pack, 'revisions');
    const namespace = pack.namespace || pack.name;
    // An explicit row selection must never be replaced by an older draft.
    const draftKey = 'pack-rollback:' + namespace + ':' + (revision || 'choose');
    if (revision) state.drafts.delete(draftKey);
    formDialog('rollback', [formField('namespace', 'namespace', 'text', namespace, { readonly: true }), formField('revision', 'revision', 'text', revision || '', { required: true, options: revisions.length ? [{ value: '', label: t('selectRevision') }, ...revisions.map(r => ({ value: typeof r === 'string' ? r : r.revision || r.id, label: typeof r === 'string' ? r : [String(r.revision || r.id).slice(0, 12), r.synced_at ? date(r.synced_at) : ''].filter(Boolean).join(' · ') }))] : null })], data => write('packs', 'rollback', Object.assign(data, { confirm: true })), { draftKey, submitKey: 'rollback', confirm: data => t('rollbackConfirm', { name: namespace, revision: data.revision }) });
  }

  // Removing a pack stops its experts being available for new work. Bots, Schedules
  // and persistent instances that name them would fail, so the dialog shows what
  // refers to the pack first and asks for an explicit "remove anyway" when anything does.
  function openRemovePack(pack) {
    const namespace = pack.namespace || pack.name;
    const development = pack.temporary && pack.development ? pack.development : null;
    modal('removePack', (body, current) => {
      const errorHost = e('div', { class: 'modal-error banner bad', role: 'alert' });
      const usageHost = e('div', { class: 'stack' }, e('div', { class: 'rail', role: 'status' }, e('span', { class: 'loading-mark' }), t('loading')));
      const keepFiles = e('input', { type: 'checkbox', id: 'remove-keep-files' });
      // A development Pack's worktree is removed only when it has no changes;
      // keeping it unregisters the Pack and leaves the worktree and branch.
      const keepWorktree = e('input', { type: 'checkbox', id: 'remove-keep-worktree' });
      const force = e('input', { type: 'checkbox', id: 'remove-force' });
      const forceRow = e('label', { class: 'checkbox-label', hidden: true }, force, e('span', { text: t('removeAnyway') }));
      const submit = e('button', { type: 'button', class: 'ui-btn danger solid', text: t('removePack'), disabled: true });
      let guarded = true; // usage still loading, or something refers to the pack
      const refresh = () => { submit.disabled = !state.online || current.pending || guarded && !force.checked; };
      const guard = () => { guarded = true; forceRow.hidden = false; refresh(); };
      force.addEventListener('change', refresh);
      get('usage', { namespace }).then(usage => {
        if (state.modal !== current) return;
        const counts = usage.counts || {}; const refs = usage.refs || {}; const items = usage.items || {}; const missing = list(usage, 'unavailable');
        const starting = Number(counts.bots || 0) + Number(counts.groups || 0) + Number(counts.schedules || 0) + Number(counts.instances || 0);
        const unreadable = missing.filter(source => source !== 'sessions');
        guarded = starting > 0 || unreadable.length > 0; forceRow.hidden = !guarded;
        replace(usageHost,
          Object.keys(refs).length ? e('div', {}, e('h3', { class: 'subhead' }, t('experts'), ' ', chip(String(Object.keys(refs).length))), e('ul', { class: 'usage-list' }, Object.keys(refs).sort().map(ref => e('li', {}, e('span', { class: 'mono', text: ref }), usageChip(refs[ref], missing.length > 0))))) : null,
          starting ? e('p', { class: 'banner bad', role: 'alert', text: t('packInUseWarning') }) : null,
          ['bots', 'groups', 'schedules', 'instances'].filter(kind => Number(counts[kind] || 0)).map(kind => e('div', {}, e('h3', { class: 'subhead' }, t(kind === 'bots' ? 'usageBots' : kind === 'groups' ? 'usageGroups' : kind), ' ', chip(String(counts[kind]))), usageList(kind, list(items, kind), Number(counts[kind])))),
          missing.length ? e('p', { class: 'banner', role: 'status', text: t('usageIncomplete', { sources: usageSourceNames(missing) }) }) : null,
          e('p', { class: 'help', text: t('usageCoverageNote') }),
          e('p', { class: 'help', text: t('usageSessionsNote') }));
        refresh();
      }, error => {
        if (state.modal !== current) return;
        replace(usageHost, e('p', { class: 'banner bad', role: 'alert', text: errorMessage(error) }));
        guard();
      });
      submit.addEventListener('click', async () => {
        if (current.pending || submit.disabled) return;
        current.pending = true; refresh(); errorHost.replaceChildren();
        try {
          const data = { namespace, confirm: true };
          if (keepFiles.checked && !development) data.keep_files = true;
          if (development && keepWorktree.checked) data.keep_worktree = true;
          if (force.checked) data.force = true;
          const result = await write('packs', 'remove', data);
          if (state.modal === current) closeModal(true);
          if (result && result.warning_code && has(result.warning_code)) notify(t(result.warning_code), true);
          else notify(t('saved'));
          await loadRoute();
        } catch (error) {
          const code = error.payload && (error.payload.code || error.payload.error);
          if (code === 'pack_in_use' || code === 'usage_unavailable') guard();
          if (state.modal === current) errorHost.textContent = errorMessage(error, true); else notify(errorMessage(error, true), true);
        } finally { current.pending = false; if (submit.isConnected) refresh(); }
      });
      // The decision stays in view while a long usage list scrolls beneath it.
      body.append(e('p', { class: 'prose', text: t('removePackLead', { name: namespace }) }),
        e('p', { class: 'help', hidden: !pack.temporary, text: pack.temporary ? t(development ? 'removeDevelopmentPackLead' : 'removeTemporaryPackLead', { branch: development && development.branch || '' }) : '' }), usageHost,
        e('div', { class: 'modal-foot' }, development ? e('label', { class: 'checkbox-label' }, keepWorktree, e('span', { text: t('keepWorktree') })) : e('label', { class: 'checkbox-label' }, keepFiles, e('span', { text: t('keepFiles') })), forceRow, errorHost,
          e('div', { class: 'form-actions' }, button('cancel', () => closeModal()), submit)));
    });
  }

  // ---------------------------------------------------------------------------
  // Global search ranks identity matches above incidental content matches.
  // ---------------------------------------------------------------------------
  const searchResources = ['sessions', 'tasks', 'instances', 'schedules', 'bots', 'groups', 'profiles', 'packs'];
  function searchKeys(resource, item) {
    if (resource === 'tasks') return [taskTitle(item), taskID(item), item.summary, item.schedule_name];
    if (resource === 'instances') return [item.name, instanceID(item), item.agent_profile, item.purpose, item.owner && item.owner.name];
    return [display(item), String(ident(item)), field(item, 'agent_profile', ''), item.chat_id, item.ref];
  }
  function searchScore(resource, item, query) {
    if (!query) return 1;
    const keys = searchKeys(resource, item).filter(Boolean).map(value => String(value).toLocaleLowerCase());
    if (keys.some(key => key === query)) return 100;
    if (keys.some(key => key.startsWith(query))) return 80;
    if (keys.some(key => key.split(/[\s/_.:-]+/).some(word => word.startsWith(query)))) return 70;
    if (keys.some(key => key.includes(query))) return 60;
    return JSON.stringify(item).toLocaleLowerCase().includes(query) ? 10 : 0;
  }
  // Equal scores prefer live objects, then the most recent activity.
  function searchRank(record) {
    const item = record.item;
    return [record.score, isArchived(item) || isTerminalTask(item.status) ? 0 : 1, timeValue(item, ['last_activity_at', 'requested_at', 'last_used_at', 'updated_at'])];
  }
  function compareSearch(a, b) {
    const left = searchRank(a); const right = searchRank(b);
    for (let i = 0; i < left.length; i++) if (left[i] !== right[i]) return right[i] - left[i];
    return 0;
  }
  function searchTarget(resource, item) {
    const map = { sessions: ['session', sessionRef(item)], tasks: ['task', { task_id: taskID(item) }], instances: ['instance', { agent_instance_id: instanceID(item) }], schedules: ['schedule', { schedule_id: scheduleID(item) }], profiles: ['profile', { ref: profileRef(item) }] };
    return map[resource] || null;
  }
  function searchLabel(resource, item) {
    if (resource === 'tasks') return taskTitle(item);
    if (resource === 'instances') return instanceName(item);
    if (resource === 'groups') return groupTitle(item);
    return display(item);
  }
  async function openSearch() {
    let records = []; let current = null; let query = '';
    modal('searchTitle', (body, modalState) => {
      current = modalState;
      const resultHost = e('div', { class: 'search-results', role: 'listbox', 'aria-label': t('searchTitle') });
      const status = e('div');
      const results = () => [...resultHost.querySelectorAll('.search-result')];
      const input = e('input', { type: 'search', placeholder: t('search'), 'aria-label': t('search'), class: 'full-width', oninput: event => { query = event.target.value; draw(); } });
      input.addEventListener('keydown', event => {
        if (event.key === 'ArrowDown') { event.preventDefault(); const first = results()[0]; if (first) first.focus(); }
        if (event.key === 'Enter') { event.preventDefault(); const first = results()[0]; if (first) first.click(); }
      });
      resultHost.addEventListener('keydown', event => {
        const all = results(); const index = all.indexOf(document.activeElement);
        if (event.key === 'ArrowDown' && index >= 0) { event.preventDefault(); (all[index + 1] || all[index]).focus(); }
        if (event.key === 'ArrowUp' && index >= 0) { event.preventDefault(); (all[index - 1] || input).focus(); }
      });
      function draw() {
        const normalized = query.trim().toLocaleLowerCase();
        const matches = records.map(record => Object.assign({ score: searchScore(record.resource, record.item, normalized) }, record)).filter(record => record.score > 0).sort(compareSearch).slice(0, 60);
        replace(resultHost, matches.length ? matches.map(({ resource, item }) => e('button', { type: 'button', class: 'search-result', role: 'option', onclick: () => {
          closeModal();
          const destination = searchTarget(resource, item);
          if (destination) navigate(...destination); else { filterState(resource).query = String(ident(item)); filterState(resource).page = 1; navigate(resource); }
        } }, icon(resource), e('div', { class: 'grow' }, e('strong', { text: searchLabel(resource, item) }), e('small', { text: t(resource) + ' · ' + shortID(ident(item)) })), resource === 'sessions' || resource === 'tasks' ? badge(item.state || item.status) : null)) : empty('noResults', false, true));
      }
      body.append(input, e('p', { class: 'note', text: t('searchHint') }), status, resultHost); resultHost.appendChild(e('p', { class: 'muted', text: t('loading') }));
      // Reuse snapshots loaded in the last minute instead of re-reading the fleet.
      Promise.allSettled(searchResources.map(async resource => {
        const cached = state.cache.get(resource + '?');
        return { resource, snapshot: cached && Date.now() - (cached.loaded_at || 0) < 60000 ? cached : await get(resource) };
      })).then(settled => {
        if (state.modal !== current || !state.authenticated) return;
        records = settled.filter(r => r.status === 'fulfilled').flatMap(r => list(r.value.snapshot, r.value.resource).map(item => ({ resource: r.value.resource, item })));
        const failures = settled.map((r, i) => r.status === 'rejected' ? t(searchResources[i]) : '').filter(Boolean);
        status.replaceChildren(failures.length ? e('div', { class: 'banner warn', text: t('searchPartial', { resources: failures.join(', ') }) }) : e('p', { class: 'note', text: t('searchScope', { count: settled.filter(r => r.status === 'fulfilled').length }) })); draw();
      });
    });
  }

  // ---------------------------------------------------------------------------
  // Authentication.
  // ---------------------------------------------------------------------------
  function authNotices() {
    const config = state.authConfig;
    if (!config) return [];
    return [config.expires_at > 0 ? e('div', { class: 'banner', role: 'status', text: expiryCopy() }) : null,
      config.insecure_transport ? e('div', { class: 'banner warn', role: 'note', text: t('insecureTransportWarning') }) : null];
  }
  function loginFailure(error) {
    const machineCode = error.payload && (error.payload.code || error.payload.error);
    state.loginErrorKey = windowExpired() ? 'console_expired' : typeof machineCode === 'string' && has(machineCode) ? machineCode : error.status === 401 ? 'consoleClosedOrExpired' : 'loginFailed';
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
  const api = { start, state, field, list, decodeBase64, decodeJSON, normalizeEvent, normalizePermission, deriveCounts, supports, routeFromHash, ingestEvent, request, write, navigate, loadRoute, render, renderPage, openNewSession, openScheduleForm, openBotForm, openSearch, closeModal, timelineFor, e, parseRunStatus, shortRef, shortID, durationText, visibleTimeline, sessionKind };
  root.ConsoleApp = api;
  if (typeof module !== 'undefined') module.exports = api;
  if (typeof document !== 'undefined' && !root.AMUX_CONSOLE_TEST) api.ready = start();
})(typeof window === 'undefined' ? globalThis : window);
