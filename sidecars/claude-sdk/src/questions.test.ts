import assert from 'node:assert/strict';
import {PassThrough} from 'node:stream';
import {test} from 'node:test';
import {RpcPeer} from './protocol.js';
import {ClaudeBridge, type QueryFn, type SdkUserMessage} from './claude.js';
const tick = () => new Promise(resolve => setTimeout(resolve, 10));
const questions = [
  {header: 'Format', question: 'Which format?', multiSelect: false, options: [{label: 'Brief', description: 'Short answer'}, {label: 'Detailed', description: 'Long answer'}]},
  {header: 'Targets', question: 'Which targets?', multiSelect: true, options: [{label: 'API', description: 'Public API'}, {label: 'CLI', description: 'Terminal'}]},
];
async function fixture(input: any = {questions}) {
  const output = new PassThrough();
  const messages: any[] = [];
  let buffer = '';
  output.on('data', chunk => {
    buffer += chunk.toString();
    let end: number;
    while ((end = buffer.indexOf('\n')) >= 0) {
      messages.push(JSON.parse(buffer.slice(0, end))); buffer = buffer.slice(end + 1);
    }
  });
  let result: any;
  const controller = new AbortController();
  const query: QueryFn = ({prompt, options}) => (async function*() {
    for await (const _ of prompt as AsyncIterable<SdkUserMessage>) {
      result = await (options as any).canUseTool('AskUserQuestion', input, {toolUseID: 'ask', signal: controller.signal});
      yield {type: 'result', subtype: 'success', stop_reason: 'end_turn'} as any;
      break;
    }
  })();
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), query);
  await bridge.startSession({cwd: '/tmp', extraArgs: ['--permission-mode', 'bypassPermissions']});
  await bridge.startTurn({text: 'ask', turnToken: 'turn'});
  await tick();
  return {bridge, messages, controller, result: () => result,
    requests: messages.filter(msg => msg.method === 'permission.request').map(msg => msg.params.permission)};
}

test('native questions aggregate out of order and accept custom text', async () => {
  const f = await fixture();
  assert.equal(f.requests.length, 2);
  assert.equal(f.requests[0].toolCall.allowOther, true);
  assert.equal(f.requests[0].options[0].description, 'Short answer');
  await f.bridge.respondPermission({permission_id: f.requests[1].id, option_ids: ['CLI', 'API', 'CLI']});
  assert.equal(f.result(), undefined, 'SDK must wait for every question');
  await assert.rejects(f.bridge.respondPermission({permission_id: f.requests[1].id, option_id: 'API'}));
  await assert.rejects(f.bridge.respondPermission({permission_id: f.requests[0].id, option_ids: ['Brief', 'Detailed']}));
  await f.bridge.respondPermission({permission_id: f.requests[0].id, option_id: 'A custom format'});
  await tick();
  assert.deepEqual(f.result(), {behavior: 'allow', updatedInput: {questions, answers: {'Which format?': 'A custom format', 'Which targets?': ['CLI', 'API']}}});
  assert.equal(f.messages.filter(msg => msg.params?.event?.type === 'permission_resolved').length, 2);
});
for (const mode of ['cancel', 'abort', 'interrupt', 'new turn']) {
  test(`questions release the SDK on ${mode} without an answer`, async () => {
    const f = await fixture();
    if (mode === 'cancel') await f.bridge.respondPermission({permission_id: f.requests[0].id, cancel: true});
    if (mode === 'abort') f.controller.abort();
    if (mode === 'interrupt') await f.bridge.interrupt();
    if (mode === 'new turn') await f.bridge.startTurn({text: 'next', turnToken: 'next'});
    await tick();
    assert.equal(f.result().behavior, 'deny');
    assert.equal(f.result().updatedInput, undefined);
    for (const request of f.requests) await assert.rejects(f.bridge.respondPermission({permission_id: request.id, option_id: 'Brief'}));
    assert.equal(f.messages.filter(msg => msg.params?.event?.type === 'permission_resolved').length, 2);
  });
}
test('malformed question schemas fail without hanging', async () => {
  for (const input of [{questions: []}, {questions: [questions[0], questions[0]]}, {questions: [{question: 'choose'}]}]) {
    const f = await fixture(input);
    assert.equal(f.requests.length, 0);
    assert.equal(f.result().behavior, 'deny');
  }
});
