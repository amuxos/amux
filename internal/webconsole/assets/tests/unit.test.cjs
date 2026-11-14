'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
global.ConsoleI18n = require('../i18n.js');
const app = require('../app.js');
const base = path.join(__dirname, '..');

test('all copy is bilingual with identical placeholders and English fallback', () => {
  const placeholders = text => [...text.matchAll(/\{(\w+)\}/g)].map(m => m[1]).sort();
  for (const [key, pair] of Object.entries(ConsoleI18n.messages)) {
    assert.equal(pair.length, 2, key);
    assert.ok(pair.every(s => typeof s === 'string' && s.length), key);
    assert.deepEqual(placeholders(pair[0]), placeholders(pair[1]), key);
    assert.equal(ConsoleI18n.t(key, 'fr-FR'), pair[0].replace(/\{\w+\}/g, ''));
  }
  assert.equal(ConsoleI18n.normalize('zh-Hans-CN'), 'zh-CN');
  assert.equal(ConsoleI18n.normalize('ZH-cn'), 'zh-CN');
  assert.equal(ConsoleI18n.t('count', 'en', { count: 8 }), '8 records');
});

test('assets are self-contained and never render user-controlled HTML', () => {
  const js = fs.readFileSync(path.join(base, 'app.js'), 'utf8');
  const html = fs.readFileSync(path.join(base, 'index.html'), 'utf8');
  const css = fs.readFileSync(path.join(base, 'styles.css'), 'utf8');
  assert.doesNotMatch(js, /innerHTML|outerHTML|insertAdjacentHTML|eval\(|new Function/);
  assert.doesNotMatch(html, /https?:\/\//);
  assert.doesNotMatch(css, /@import|url\(https?:/);
  assert.doesNotMatch(js, /localStorage\.setItem\([^)]*(?:token|draft|code|message)/i);
  // Touch targets stay at least 44px; a precise pointer may use denser controls.
  assert.match(css, /button, input, select, \.ui-btn \{ min-height: 44px; \}/);
  assert.match(css, /@media \(max-width: 540px\)/);
  assert.ok(css.includes('data-theme=dark'));
  assert.ok(css.includes('prefers-reduced-motion'));
  // The content column must start at the top instead of centering vertically.
  assert.match(css, /\.main \{[^}]*margin: 0 auto;/);
});

test('safe unicode base64 decoding and malformed payload handling', () => {
  const text = '你好 🌍 <img onerror=alert(1)>';
  assert.equal(app.decodeBase64(Buffer.from(text).toString('base64')), text);
  assert.equal(app.decodeBase64('%%%'), null);
  assert.equal(app.decodeJSON('%%%'), null);
  assert.deepEqual(app.decodeJSON(Buffer.from('{"x":1}').toString('base64')), { x: 1 });
});

test('transport fields and capabilities are explicit and fail closed', () => {
  assert.equal(app.field({ targetId: 'r' }, 'target_id'), 'r');
  assert.deepEqual(app.list({ profiles: [] }, 'profiles'), []);
  assert.equal(app.supports({}, 'archive'), false);
  assert.equal(app.supports({ actions: ['archive'] }, 'archive'), true);
  assert.equal(app.supports({ actions: ['archive'] }, 'archive', { actions: [] }), false);
  assert.equal(app.supports({ capabilities: { install: true } }, 'install'), true);
});

test('overview does not infer active turns from running lifecycle', () => {
  const counts = app.deriveCounts([{ state: 'running' }, { state: 'running', turn_state: 'running' }, { state: 'sleeping' }, { state: 'archived', turn_state: 'failed' }]);
  assert.deepEqual(counts, { active: 3, running: 2, sleeping: 1, archived: 1, working: 1, attention: 0 });
  // A Turn waiting on the owner is one bucket: needs attention, not also working.
  assert.deepEqual(app.deriveCounts([{ state: 'running', active_turn: true, pending_permissions: 1 }, { state: 'running', active_turn: true }]), { active: 2, running: 2, sleeping: 0, archived: 0, working: 1, attention: 1 });
});

test('route parser supports safe known routes only', () => {
  assert.deepEqual(app.routeFromHash('#session?name=hello%20world&target=r'), { page: 'session', params: { name: 'hello world', target: 'r' } });
  assert.equal(app.routeFromHash('#runtimes').page, 'overview');
  assert.equal(app.routeFromHash('#<script>').page, 'overview');
});

test('permissions preserve actual option identifiers without invented grants', () => {
  const p = app.normalizePermission({ toolCall: { title: 'Run tests', multiSelect: true }, options: [{ optionId: 'real_once', name: 'Allow once' }] }, 'p1');
  assert.equal(p.id, 'p1'); assert.equal(p.multi, true);
  assert.deepEqual(p.options.map(o => o.id), ['real_once']);
  assert.deepEqual(app.normalizePermission({}).options, []);
});

test('canonical and raw attach event variants decode safely', () => {
  const canonical = { kind: 'tool_changed', cursor: { epoch: 'e', seq: 1 }, body: { phase: 'completed', tool: { id: 't', title: 'Bash', output_preview: 'ok' } } };
  const event = app.normalizeEvent({ canonical_json: Buffer.from(JSON.stringify(canonical)).toString('base64'), cursor: 'c1' });
  assert.equal(event.kind, 'tool_changed'); assert.equal(event.tool.output, 'ok');
  assert.equal(app.normalizeEvent({ kind: 0, data: Buffer.from('你好').toString('base64') }).text, '你好');
  assert.equal(app.normalizeEvent({ kind: 'PERMISSION_REQUEST', permission_id: 'p', permission_payload: Buffer.from('{"options":[]}').toString('base64') }).permission.id, 'p');
});

test('SSE replay deduplicates per session, retains bounded history and resolves permissions', () => {
  const ref = { name: 'one', target: 'r' };
  assert.equal(app.ingestEvent(ref, { kind: 0, event_id: '1', data: Buffer.from('a').toString('base64') }), true);
  assert.equal(app.ingestEvent(ref, { kind: 0, event_id: '1', data: Buffer.from('a').toString('base64') }), false);
  assert.equal(app.ingestEvent({ name: 'two', target: 'r' }, { kind: 0, event_id: '1', data: Buffer.from('b').toString('base64') }), true);
  const emit = body => app.ingestEvent(ref, { cursor: body.kind, canonical_json: Buffer.from(JSON.stringify(body)).toString('base64') });
  emit({ kind: 'permission_requested', body: { request: { id: 'p', options: [{ id: 'allow' }] } } });
  assert.equal(app.timelineFor(ref).permissions.size, 1);
  emit({ kind: 'permission_resolved', body: { request_id: 'p' } });
  assert.equal(app.timelineFor(ref).permissions.size, 0);
  for (let i = 0; i < 1700; i++) app.ingestEvent(ref, { kind: 1, event_id: 's' + i, state: 'idle' });
  assert.equal(app.timelineFor(ref).items.length, 1500);
});

test('schedule run status, refs and durations are rendered from exact wire values', () => {
  assert.deepEqual(app.parseRunStatus('ok:task:task-1'), { status: 'ok', taskID: 'task-1', detail: '' });
  assert.deepEqual(app.parseRunStatus('error: queue full'), { status: 'error', taskID: '', detail: 'queue full' });
  assert.deepEqual(app.parseRunStatus('skipped:offline'), { status: 'skipped', taskID: '', detail: 'offline' });
  assert.deepEqual(app.parseRunStatus('submitting'), { status: 'submitting', taskID: '', detail: '' });
  assert.equal(app.shortRef('pack/agent@ea7ff4ebe870d9fdfbb9fc291f4bb1f034635b78'), 'pack/agent@ea7ff4e');
  assert.equal(app.shortRef('builtin/claude'), 'builtin/claude');
  assert.equal(app.shortID('task-30c274c1-2217-4725-8101-9ad35ebe6bbf'), 'task-30c274c1');
  assert.equal(app.shortID('plain-name'), 'plain-name');
  assert.equal(app.sessionKind({ owner_task_id: 't' }), 'task');
  assert.equal(app.sessionKind({ agent_instance_id: 'i' }), 'instance');
  assert.equal(app.sessionKind({ workspace_source: 'feishu' }), 'feishu');
});

test('timeline view hides metadata noise and repeated usage without dropping content', () => {
  const items = [
    { kind: 'commands_replaced', body: { commands: [] } },
    { kind: 'goal_changed', body: { supported: true } },
    { kind: 'turn_started', text: 'Hi' },
    { kind: 'assistant_message', text: '' },
    { kind: 'assistant_delta', text: 'Final answer' },
    { kind: 'usage_observed', body: { usage: { input_tokens: 1 } } },
    { kind: 'tool_changed', tool: { id: 't' } },
    { kind: 'usage_observed', body: { usage: { input_tokens: 2 } } },
    { kind: 'result_observed', text: 'Final answer' },
    { kind: 'turn_finished', state: 'success' },
    { kind: 'goal_changed', body: { supported: true, goal: { objective: 'ship' } } }
  ];
  const visible = app.visibleTimeline(items).map(item => item.kind + (item.body && item.body.usage ? ':' + item.body.usage.input_tokens : ''));
  assert.deepEqual(visible, ['turn_started', 'assistant_delta', 'tool_changed', 'usage_observed:2', 'turn_finished', 'goal_changed']);
});
