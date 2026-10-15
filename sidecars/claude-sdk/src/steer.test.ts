import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { test } from 'node:test';
import { RpcPeer } from './protocol.js';
import { ClaudeBridge, type QueryFn, type SdkUserMessage } from './claude.js';

function collect(output: PassThrough): any[] {
  const msgs: any[] = [];
  let buf = '';
  output.on('data', (chunk) => {
    buf += chunk.toString();
    let nl: number;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line) msgs.push(JSON.parse(line));
    }
  });
  return msgs;
}

const tick = () => new Promise((r) => setTimeout(r, 10));

// Scripted SDK: the test decides exactly which SDK messages are produced and
// when, and can inspect every user message the bridge pushed.
function scriptedQuery(interruptImpl: () => Promise<void> = async () => {}) {
  const prompts: SdkUserMessage[] = [];
  const outbox: any[] = [];
  let wake: (() => void) | undefined;
  let opts: Record<string, unknown> = {};
  const calls = { interrupts: 0 };
  const emit = (...msgs: any[]) => {
    outbox.push(...msgs);
    wake?.();
  };
  const fn: QueryFn = ({ prompt, options }) => {
    opts = options;
    void (async () => {
      for await (const m of prompt as AsyncIterable<SdkUserMessage>) prompts.push(m);
    })();
    async function* gen() {
      for (;;) {
        while (outbox.length) yield outbox.shift();
        await new Promise<void>((r) => (wake = r));
      }
    }
    const it = gen() as any;
    it.interrupt = async () => {
      calls.interrupts++;
      await interruptImpl();
    };
    return it;
  };
  return { fn, prompts, emit, calls, options: () => opts };
}

function events(msgs: any[]) {
  return msgs.filter((m) => m.method === 'event').map((m) => m.params.event);
}

async function setup(opts: { interrupt?: () => Promise<void>; stallMs?: number } = {}) {
  const output = new PassThrough();
  const peer = new RpcPeer(new PassThrough(), output);
  const msgs = collect(output);
  const sdk = scriptedQuery(opts.interrupt);
  const bridge = new ClaudeBridge(peer, sdk.fn, { stallMs: opts.stallMs });
  await bridge.startSession({ cwd: '/tmp' });
  return { msgs, sdk, bridge };
}

const echo = (msg: SdkUserMessage) => ({ type: 'user', isReplay: true, uuid: msg.uuid, message: msg.message });
const toolUse = (id: string) => ({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Bash', input: {} }] } });
const delta = (text: string) => ({ type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text } } });
const doneStates = (msgs: any[]) => events(msgs).filter((e) => e.type === 'turn_done').map((e) => e.state.state);

test('steer is rejected when no turn is in flight', async () => {
  const { bridge } = await setup();
  await assert.rejects(bridge.steerTurn({ text: 'hello' }), /no claude turn in flight/);
});

test('session enables replay-user-messages and advertises steer', async () => {
  const { sdk, bridge } = await setup();
  assert.deepEqual((sdk.options().extraArgs as Record<string, unknown>)['replay-user-messages'], null);
  const init: any = await bridge.initialize();
  assert.equal(init.capabilities.steer, true);
});

test('steer consumed mid-turn keeps a single amux turn', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'run three steps' });
  sdk.emit({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'tu1', name: 'Bash', input: {} }] } });
  await tick();
  const r: any = await bridge.steerTurn({ text: 'also add a step' });
  assert.equal(r.mode, 'tool_boundary');
  await tick();
  assert.equal(sdk.prompts.length, 2);
  const steer = sdk.prompts[1];
  assert.equal(steer.priority, 'now');
  assert.ok(steer.uuid);
  // CLI lets the tool finish, ends that request with an intermediate result,
  // then echoes the steer and continues.
  sdk.emit({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'tu1', content: 'step1' }] } });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '' });
  await tick();
  assert.equal(events(msgs).filter((e) => e.type === 'turn_done').length, 0);
  sdk.emit({ type: 'user', isReplay: true, uuid: steer.uuid, message: { role: 'user', content: 'also add a step' } });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'all done' });
  await tick();
  const done = events(msgs).filter((e) => e.type === 'turn_done');
  assert.equal(done.length, 1);
  assert.equal(done[0].state.state, 'turn_done');
});

test('steer queued behind a finishing turn swallows the first result', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'list fruits' });
  await tick();
  const r: any = await bridge.steerTurn({ text: 'and add BANANA42' });
  assert.equal(r.mode, 'held');
  await tick();
  assert.equal(sdk.prompts.length, 1, 'held while text streams');
  // Text-only turn: the original result arrives, then the held steer runs.
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'fruits' });
  await tick();
  assert.equal(events(msgs).filter((e) => e.type === 'turn_done').length, 0, 'first result must not end the amux turn');
  assert.equal(sdk.prompts.length, 2);
  const steer = sdk.prompts[1];
  assert.equal(steer.priority, undefined);
  sdk.emit({ type: 'user', isReplay: true, uuid: steer.uuid, message: { role: 'user', content: 'x' } });
  sdk.emit({ type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'BANANA42' } } });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'BANANA42' });
  await tick();
  const evs = events(msgs);
  const done = evs.filter((e) => e.type === 'turn_done');
  assert.equal(done.length, 1);
  const results = evs.filter((e) => e.type === 'result');
  assert.equal(results.length, 1);
  assert.equal(results[0].result.text, 'BANANA42');
  // A follow-up turn still works and is accounted separately.
  await bridge.startTurn({ text: 'next' });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'ok' });
  await tick();
  assert.equal(events(msgs).filter((e) => e.type === 'turn_done').length, 2);
});

test('interrupt with a pending steer orphans it so a later turn is not cut short', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'long work' });
  sdk.emit(toolUse('tu2'));
  await tick();
  await bridge.steerTurn({ text: 'correction' });
  await tick();
  const steer = sdk.prompts[1];
  await bridge.interrupt();
  await assert.rejects(bridge.steerTurn({ text: 'again' }), /interrupted/);
  sdk.emit({ type: 'result', subtype: 'error_during_execution' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted']);
  // The next turn waits until the CLI is done with the orphaned steer, so the
  // two never share a run.
  await bridge.startTurn({ text: 'new question' });
  await tick();
  assert.equal(sdk.prompts.length, 2, 'new turn is deferred behind the orphan');
  const interruptsBefore = sdk.calls.interrupts;
  sdk.emit(echo(steer), delta('orphan text'));
  await tick();
  assert.equal(sdk.calls.interrupts, interruptsBefore + 1, 'orphan run is interrupted on its first activity');
  sdk.emit({ type: 'result', subtype: 'error_during_execution' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted'], 'orphan result must be swallowed');
  assert.equal(sdk.prompts.length, 3, 'new turn starts once the orphan run is over');
  const next = String(sdk.prompts[2].message.content);
  assert.ok(next.endsWith('new question'));
  assert.match(next, /用户中断了上一个任务/, 'the interrupted steer is marked as cancelled for the model');
  sdk.emit(delta('answer'), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'answer' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted', 'turn_done']);
  const texts = events(msgs).filter((e) => e.type === 'assistant_text_delta').map((e) => e.text);
  assert.deepEqual(texts, ['answer'], 'orphan output never reaches a turn');
});

test('an interrupt between the tool boundary and the steer echo still ends the turn', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'long work' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'correction' });
  await tick();
  const steer = sdk.prompts[1];
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '' });
  await tick();
  await bridge.interrupt();
  const interruptsBefore = sdk.calls.interrupts;
  // The steer run starts after the interrupt; it is interrupted again so it
  // cannot run to completion, and its aborted result ends the amux turn.
  sdk.emit(echo(steer));
  await tick();
  assert.equal(sdk.calls.interrupts, interruptsBefore + 1);
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted']);
  // Nothing is left over: the next turn starts right away and gets its result.
  await bridge.startTurn({ text: 'next' });
  await tick();
  assert.equal(sdk.prompts.length, 3);
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'ok' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted', 'turn_done']);
});

test('an interrupt absorbed by a queued steer still ends the turn as interrupted', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'long work' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'first' });
  await bridge.steerTurn({ text: 'second' });
  await tick();
  const [s1, s2] = [sdk.prompts[1], sdk.prompts[2]];
  await bridge.interrupt();
  // CLI 2.1.296: with a 'now' message queued, the interrupt does not abort the
  // tool; the run ends at the tool boundary with a success result.
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted']);
  await bridge.startTurn({ text: 'T2' });
  await tick();
  assert.equal(sdk.prompts.length, 3, 'T2 waits for the orphaned steers');
  // Both orphaned steers are merged into one CLI run with a single result.
  sdk.emit(echo(s1), echo(s2), { type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  assert.equal(sdk.prompts.length, 4);
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'T2 answer' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted', 'turn_done']);
  assert.equal(events(msgs).filter((e) => e.type === 'result').at(-1).result.text, 'T2 answer');
});

test('orphaned work cannot ask for permission', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'long work' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'rm -rf build' });
  await tick();
  const steer = sdk.prompts[1];
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  sdk.emit(echo(steer), toolUse('tu-orphan'));
  await tick();
  const canUseTool = sdk.options().canUseTool as (name: string, input: unknown, ctx: unknown) => Promise<any>;
  const decision = await canUseTool('Bash', { command: 'rm -rf build' }, { toolUseID: 'tu-orphan' });
  assert.equal(decision.behavior, 'deny');
  assert.equal(msgs.filter((m) => m.method === 'permission.request').length, 0);
  assert.equal(events(msgs).filter((e) => e.type === 'tool_started').length, 1, 'only the live turn tool is surfaced');
});

test('a failed interrupt keeps the turn and its steers running', async () => {
  const { msgs, sdk, bridge } = await setup({ interrupt: async () => { throw new Error('control channel closed'); } });
  await bridge.startTurn({ text: 'long work' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'correction' });
  await tick();
  const steer = sdk.prompts[1];
  await bridge.interrupt();
  // Steering works again: the turn is not being interrupted.
  const r: any = await bridge.steerTurn({ text: 'more' });
  assert.equal(r.mode, 'tool_boundary');
  await tick();
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '' });
  await tick();
  assert.deepEqual(doneStates(msgs), [], 'the tool-boundary result is not the end of the turn');
  sdk.emit(echo(steer), echo(sdk.prompts[2]), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'final answer' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['turn_done']);
  assert.equal(events(msgs).filter((e) => e.type === 'result')[0].result.text, 'final answer');
});

test('steer naming another turn is rejected', async () => {
  const { sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'work', turnToken: 'turn-a' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await assert.rejects(bridge.steerTurn({ text: 'x', turnToken: 'turn-b' }), /not active/);
  const r: any = await bridge.steerTurn({ text: 'y', turnToken: 'turn-a' });
  assert.equal(r.mode, 'tool_boundary');
});

test('usage is summed over the runs of a steered turn', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'work' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'more' });
  await tick();
  const steer = sdk.prompts[1];
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '', usage: { input_tokens: 10, output_tokens: 5, server_tool_use: { web_search_requests: 1 } }, total_cost_usd: 0.1 });
  sdk.emit(echo(steer), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'ok', usage: { input_tokens: 7, output_tokens: 3, server_tool_use: { web_search_requests: 2 } }, total_cost_usd: 0.25 });
  await tick();
  const result = events(msgs).find((e) => e.type === 'result').result;
  assert.deepEqual(result.usage, { input_tokens: 17, output_tokens: 8, server_tool_use: { web_search_requests: 3 } });
  assert.equal(result.cost_usd, 0.25, 'total_cost_usd is already cumulative');
});

test('interrupting a turn that waits behind leftover work ends it without the CLI', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'one' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'c' });
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  await bridge.startTurn({ text: 'two' });
  await bridge.interrupt();
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted', 'interrupted']);
  assert.equal(sdk.prompts.length, 2, 'the deferred start is never sent');
});

test('a deferred start is released when the orphaned steer never shows up', async () => {
  const { msgs, sdk, bridge } = await setup({ stallMs: 30 });
  await bridge.startTurn({ text: 'one' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'c' });
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  await bridge.startTurn({ text: 'two' });
  await tick();
  assert.equal(sdk.prompts.length, 2);
  await new Promise((r) => setTimeout(r, 60));
  assert.equal(sdk.prompts.length, 3, 'start released by the watchdog');
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'two' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['interrupted', 'turn_done']);
});

test('a turn waiting for a steer that never runs ends with its last result', async () => {
  const { msgs, sdk, bridge } = await setup({ stallMs: 30 });
  await bridge.startTurn({ text: 'one' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'c' });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'done anyway' });
  await tick();
  assert.deepEqual(doneStates(msgs), []);
  await new Promise((r) => setTimeout(r, 60));
  assert.deepEqual(doneStates(msgs), ['turn_done']);
});

test('a steer that runs after the stall watchdog does not corrupt the next turn', async () => {
  const { msgs, sdk, bridge } = await setup({ stallMs: 30 });
  await bridge.startTurn({ text: 'one' });
  sdk.emit(toolUse('tu1'));
  await tick();
  await bridge.steerTurn({ text: 'late correction' });
  await tick();
  const steer = sdk.prompts[1];
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'one done' });
  await tick();
  await new Promise((r) => setTimeout(r, 60));
  assert.deepEqual(doneStates(msgs), ['turn_done'], 'watchdog ends the first turn');
  await bridge.startTurn({ text: 'two' });
  await tick();
  assert.equal(sdk.prompts.length, 2, 'the new turn waits behind the stalled steer');
  // The CLI finally runs the stale steer: its output and result are swallowed.
  sdk.emit(echo(steer), delta('stale text'), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'stale' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['turn_done'], 'stale result must not end turn two');
  assert.equal(sdk.prompts.length, 3, 'turn two starts once the stale run is over');
  assert.ok(String(sdk.prompts[2].message.content).endsWith('two'));
  sdk.emit(delta('answer two'), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'answer two' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['turn_done', 'turn_done']);
  const texts = events(msgs).filter((e) => e.type === 'assistant_text_delta').map((e) => e.text);
  assert.ok(!texts.includes('stale text'), 'stale output never reaches a turn');
  assert.equal(events(msgs).filter((e) => e.type === 'result').at(-1).result.text, 'answer two');
});

test('a held steer is released with priority now when the next tool starts', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'plan then run' });
  await tick();
  await bridge.steerTurn({ text: 'use dry-run' });
  await tick();
  assert.equal(sdk.prompts.length, 1);
  sdk.emit({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'tu9', name: 'Bash', input: {} }] } });
  await tick();
  assert.equal(sdk.prompts.length, 2);
  assert.equal(sdk.prompts[1].priority, 'now');
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'tool_use', result: '' });
  sdk.emit({ type: 'user', isReplay: true, uuid: sdk.prompts[1].uuid, message: { role: 'user', content: 'use dry-run' } });
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'ok' });
  await tick();
  assert.equal(events(msgs).filter((e) => e.type === 'turn_done').length, 1);
});

test('the cancelled-steer note is only added after an interrupted steered turn', async () => {
  const { sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'plain' });
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  await bridge.startTurn({ text: 'after a plain interrupt' });
  await tick();
  assert.equal(sdk.prompts[1].message.content, 'after a plain interrupt');
  sdk.emit({ type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'ok' });
  await tick();
  // A held steer never reached the CLI: nothing to cancel in its history.
  await bridge.startTurn({ text: 'third' });
  await tick();
  assert.equal((await bridge.steerTurn({ text: 'held only' }) as any).mode, 'held');
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution', is_error: true });
  await tick();
  await bridge.startTurn({ text: 'x' });
  await tick();
  assert.equal(sdk.prompts.at(-1)?.message.content, 'x');
});

test('an interrupt drops held steers that never reached the CLI', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'write' });
  await tick();
  await bridge.steerTurn({ text: 'held' });
  await bridge.interrupt();
  sdk.emit({ type: 'result', subtype: 'error_during_execution' });
  await tick();
  assert.equal(sdk.prompts.length, 1, 'held steer must not be sent after interrupt');
  const done = events(msgs).filter((e) => e.type === 'turn_done');
  assert.equal(done.length, 1);
  assert.equal(done[0].state.state, 'interrupted');
});

test('a held steer still runs after a hard failure', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'work' });
  await tick();
  const r: any = await bridge.steerTurn({ text: 'more' });
  assert.equal(r.mode, 'held');
  sdk.emit({ type: 'result', subtype: 'error_max_turns', is_error: true, errors: ['max turns'] });
  await tick();
  assert.deepEqual(doneStates(msgs), [], 'the accepted steer is not dropped');
  assert.equal(sdk.prompts.length, 2);
  assert.equal(sdk.prompts[1].message.content, 'more');
  sdk.emit(echo(sdk.prompts[1]), { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'handled' });
  await tick();
  assert.deepEqual(doneStates(msgs), ['turn_done']);
  assert.equal(events(msgs).filter((e) => e.type === 'error').length, 0);
});

test('a hard failure with no steer left ends the turn', async () => {
  const { msgs, sdk, bridge } = await setup();
  await bridge.startTurn({ text: 'work' });
  await tick();
  sdk.emit({ type: 'result', subtype: 'error_max_turns', is_error: true, errors: ['max turns'] });
  await tick();
  assert.deepEqual(doneStates(msgs), ['failed']);
});
