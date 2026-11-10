'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
// Exercise the real transition coordinator with an inert document; no server.
function setup(responses, confirmation = true) {
  const calls = [], prompts = [];
  const window = {
    AMUX_CONSOLE_TEST: true, navigator: { language: 'zh-CN', onLine: true }, location: { hash: '' },
    confirm(message) { prompts.push(message); return confirmation; },
    fetch: async (url, options) => { calls.push({ url, data: JSON.parse(options.body).data }); const payload = responses.shift(); return { ok: true, status: 200, json: async () => payload }; },
    atob, crypto: require('node:crypto').webcrypto
  };
  const document = { getElementById() { return null; } };
  const context = vm.createContext({ window, document, console, URLSearchParams, TextDecoder, Uint8Array });
  const base = path.join(__dirname, '..');
  vm.runInContext(fs.readFileSync(path.join(base, 'i18n.js'), 'utf8'), context);
  vm.runInContext(fs.readFileSync(path.join(base, 'app.js'), 'utf8').replace('  const api = { start,', '  const api = { saveBotTransition, errorMessage, start,'), context);
  window.ConsoleApp.state.bootstrap = { csrf_token: 'c', operator: true };
  return { app: window.ConsoleApp, calls, prompts };
}

test('bot confirmation and recovery preserve original version, settings and request id', async () => {
  const env = setup([{ status: 'needs_confirmation', confirmation_token: 'first', impact: { en: 'Switch', 'zh-CN': '切换' } }, { status: 'needs_recovery_choice', confirmation_token: 'second', impact: { en: 'New', 'zh-CN': '新建' } }, { status: 'success' }]);
  const payload = { profile: 'bot', expected_version: 4, settings: { isolation_required: false, default_agent: 'p' }, request_id: 'request' };
  await env.app.saveBotTransition(payload);
  assert.equal(env.calls.length, 3);
  for (const call of env.calls) { assert.deepEqual(call.data.settings, payload.settings); assert.equal(call.data.expected_version, 4); assert.equal(call.data.request_id, 'request'); }
  assert.equal(env.calls[1].data.confirmed, true); assert.equal(env.calls[1].data.confirmation_token, 'first');
  assert.equal(env.calls[2].data.recovery_action, 'create_blank'); assert.equal(env.calls[2].data.confirmation_token, 'second');
  assert.ok(env.prompts[0].includes('切换')); assert.ok(env.prompts[1].includes('新建'));
});

test('declining transition never sends a confirmation and state_changed never succeeds', async () => {
  const declined = setup([{ status: 'needs_confirmation', confirmation_token: 'token' }], false);
  await assert.rejects(() => declined.app.saveBotTransition({ profile: 'bot' }));
  assert.equal(declined.calls.length, 1);
  const conflict = setup([{ status: 'state_changed' }]);
  await assert.rejects(() => conflict.app.saveBotTransition({ profile: 'bot' }), error => error.status === 409);
  assert.equal(conflict.calls.length, 1);
});

test('known product error codes localize and unknown server codes use safe fallback', () => {
  const env = setup([]);
  const known = env.app.errorMessage({ status: 400, message: 'console_invalid_bot_settings', payload: { code: 'console_invalid_bot_settings' } });
  assert.ok(known.includes('助手配置无效'));
  const unknown = env.app.errorMessage({ status: 400, message: 'unexpected_internal_key', payload: { code: 'unexpected_internal_key' } });
  assert.equal(unknown.includes('unexpected_internal_key'), false);
});
