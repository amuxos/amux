import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { test } from 'node:test';
import { RpcPeer } from './protocol.js';
import { applyExtraArgs, ClaudeBridge, type QueryFn, type SdkUserMessage } from './claude.js';

// Collect the NDJSON notifications the bridge writes to its output stream.
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

test('startTurn drives the SDK and streams real deltas, not an echo', async () => {
  const output = new PassThrough();
  const peer = new RpcPeer(new PassThrough(), output);
  const msgs = collect(output);

  // Fake SDK: for each user turn, yield a system init, a text delta of "4",
  // and a result — proving the bridge maps SDK output, not the input text.
  const fakeQuery: QueryFn = ({ prompt }) => {
    async function* gen() {
      for await (const user of prompt as AsyncIterable<SdkUserMessage>) {
        void user; // input consumed but NOT echoed
        yield { type: 'system', subtype: 'init', session_id: 'sess_1' } as any;
        yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: '4' } } } as any;
        yield { type: 'result', subtype: 'success', stop_reason: 'end_turn' } as any;
      }
    }
    return gen();
  };

  const bridge = new ClaudeBridge(peer, fakeQuery);
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'what is 2+2? reply with only the number' });
  await tick();

  const events = msgs.filter((m) => m.method === 'event').map((m) => m.params.event);
  const deltas = events.filter((e) => e.type === 'assistant_text_delta');
  assert.equal(deltas.length, 1);
  assert.equal(deltas[0].text, '4', 'assistant text must be the model output "4"');
  assert.ok(!deltas.some((e) => String(e.text).includes('2+2')), 'must not echo the prompt');

  const done = events.find((e) => e.type === 'turn_done');
  assert.ok(done, 'turn_done emitted');
  assert.equal(done.state.stop_reason, 'end_turn');

});

test('result message emits a native result event before turn_done', async () => {
  const output = new PassThrough();
  const peer = new RpcPeer(new PassThrough(), output);
  const msgs = collect(output);

  // SDK result message carries the final text + usage + cost; the bridge must
  // surface it as a native `result` event so the task control plane can record a
  // summary without an injected text envelope.
  const fakeQuery: QueryFn = ({ prompt }) => {
    async function* gen() {
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        void _user;
        yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'done' } } } as any;
        yield {
          type: 'result',
          subtype: 'success',
          stop_reason: 'end_turn',
          result: 'the final answer',
          usage: { input_tokens: 3 },
          total_cost_usd: 0.002,
        } as any;
      }
    }
    return gen();
  };

  const bridge = new ClaudeBridge(peer, fakeQuery);
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'go' });
  await tick();

  const events = msgs.filter((m) => m.method === 'event').map((m) => m.params.event);
  const resultIdx = events.findIndex((e) => e.type === 'result');
  const doneIdx = events.findIndex((e) => e.type === 'turn_done');
  assert.ok(resultIdx >= 0, 'result event emitted');
  assert.equal(events[resultIdx].result.text, 'the final answer');
  assert.equal(events[resultIdx].result.cost_usd, 0.002);
  assert.ok(doneIdx > resultIdx, 'result event precedes turn_done');
});

test('subagent messages do not leak into the parent presentation stream', async () => {
  const output = new PassThrough();
  const peer = new RpcPeer(new PassThrough(), output);
  const msgs = collect(output);

  const fakeQuery: QueryFn = ({ prompt }) => {
    async function* gen() {
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        void _user;
        yield {
          type: 'stream_event',
          parent_tool_use_id: 'parent-task',
          event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'child answer' } },
        } as any;
        yield {
          type: 'assistant',
          parent_tool_use_id: 'parent-task',
          message: { content: [{ type: 'tool_use', id: 'child-tool', name: 'Read', input: { path: 'child.txt' } }] },
        } as any;
        yield {
          type: 'user',
          parent_tool_use_id: 'parent-task',
          message: { content: [{ type: 'tool_result', tool_use_id: 'child-tool', content: 'child result' }] },
        } as any;
        yield {
          type: 'stream_event',
          parent_tool_use_id: null,
          event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'parent answer' } },
        } as any;
        yield {
          type: 'assistant',
          parent_tool_use_id: null,
          message: { content: [{ type: 'tool_use', id: 'parent-tool', name: 'Read', input: { path: 'parent.txt' } }] },
        } as any;
        yield { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'parent answer' } as any;
      }
    }
    return gen();
  };

  const bridge = new ClaudeBridge(peer, fakeQuery);
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'delegate' });
  await tick();

  const events = msgs.filter((m) => m.method === 'event').map((m) => m.params.event);
  assert.deepEqual(events.filter((e) => e.type === 'assistant_text_delta').map((e) => e.text), ['parent answer']);
  assert.deepEqual(events.filter((e) => e.type === 'tool_started').map((e) => e.tool.id), ['parent-tool']);
  assert.ok(events.some((e) => e.type === 'result' && e.result.text === 'parent answer'));
  assert.ok(events.some((e) => e.type === 'turn_done'));
});

test('canUseTool emits a permission.request resolved by permission.respond', async () => {
  const output = new PassThrough();
  const peer = new RpcPeer(new PassThrough(), output);
  const msgs = collect(output);

  let approved: unknown;
  const fakeQuery: QueryFn = ({ prompt, options }) => {
    async function* gen() {
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        const canUseTool = (options as any).canUseTool as (n: string, i: any, c: any) => Promise<unknown>;
        approved = await canUseTool('Bash', { command: 'ls' }, { toolUseID: 'tu_1' });
        yield { type: 'result', subtype: 'success', stop_reason: 'end_turn' } as any;
      }
    }
    return gen();
  };

  const bridge = new ClaudeBridge(peer, fakeQuery);
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'list files' });
  await tick();

  const req = msgs.find((m) => m.method === 'permission.request');
  assert.ok(req, 'permission.request emitted');
  const permId = req.params.permission.id;
  assert.equal(req.params.permission.toolCall.name, 'Bash');

  await bridge.respondPermission({ permission_id: permId, option_id: 'allow' });
  await tick();
  assert.deepEqual(approved, { behavior: 'allow' });

});

test('applyExtraArgs maps --model and --permission-mode onto SDK options', () => {
  const options: Record<string, unknown> = { permissionMode: 'default' };
  applyExtraArgs(options, ['--model', 'opus', '--permission-mode', 'acceptEdits']);
  assert.equal(options.model, 'opus');
  assert.equal(options.permissionMode, 'acceptEdits');
});

test('applyExtraArgs maps --effort onto SDK options', () => {
  const options: Record<string, unknown> = {};
  applyExtraArgs(options, ['--effort', 'high']);
  assert.equal(options.effort, 'high');

  applyExtraArgs(options, ['--effort=xhigh']);
  assert.equal(options.effort, 'xhigh');
});

test('append instructions reach the SDK through its public systemPrompt option', async () => {
  let received: Record<string, unknown> | undefined;
  const fakeQuery: QueryFn = ({ options }) => {
    received = options;
    return (async function* () {})();
  };
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), new PassThrough()), fakeQuery);
  const instruction = 'Profile instructions.\n\nUse amux for cross-session memory.';
  await bridge.startSession({
    cwd: '/tmp',
    extraArgs: ['--append-system-prompt', instruction],
    env: ['CLAUDE_CODE_DISABLE_AUTO_MEMORY=1'],
  });
  assert.deepEqual(received?.systemPrompt, { type: 'preset', preset: 'claude_code', append: instruction });
  assert.equal(received?.appendSystemPrompt, undefined);
  assert.equal((received?.env as Record<string, string>).CLAUDE_CODE_DISABLE_AUTO_MEMORY, '1');
});

test('SDK and child use native HOME defaults for an empty config override', async () => {
  const previous = process.env.CLAUDE_CONFIG_DIR;
  process.env.CLAUDE_CONFIG_DIR = '';
  try {
    let received: Record<string, unknown> | undefined;
    const fakeQuery: QueryFn = ({ options }) => {
      assert.equal(process.env.CLAUDE_CONFIG_DIR, undefined, 'SDK must not resolve config relative to cwd');
      received = options;
      return (async function* () {})();
    };
    const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), new PassThrough()), fakeQuery);
    await bridge.startSession({ env: ['CLAUDE_CONFIG_DIR=', 'CLAUDE_CODE_DISABLE_AUTO_MEMORY=1'] });
    const env = received?.env as Record<string, string>;
    assert.equal(Object.hasOwn(env, 'CLAUDE_CONFIG_DIR'), false);
    assert.equal(env.CLAUDE_CODE_DISABLE_AUTO_MEMORY, '1');
  } finally {
    if (previous === undefined) delete process.env.CLAUDE_CONFIG_DIR;
    else process.env.CLAUDE_CONFIG_DIR = previous;
  }
});

test('SDK preserves an explicit nonempty config directory', async () => {
  const previous = process.env.CLAUDE_CONFIG_DIR;
  process.env.CLAUDE_CONFIG_DIR = '/custom/config';
  try {
    const fakeQuery: QueryFn = ({ options }) => {
      assert.equal(process.env.CLAUDE_CONFIG_DIR, '/custom/config');
      assert.equal((options.env as Record<string, string>).CLAUDE_CONFIG_DIR, '/custom/config');
      return (async function* () {})();
    };
    const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), new PassThrough()), fakeQuery);
    await bridge.startSession({});
  } finally {
    if (previous === undefined) delete process.env.CLAUDE_CONFIG_DIR;
    else process.env.CLAUDE_CONFIG_DIR = previous;
  }
});

test('applyExtraArgs treats --dangerously-skip-permissions as bypass + allow flag', () => {
  const options: Record<string, unknown> = { permissionMode: 'default' };
  applyExtraArgs(options, ['--dangerously-skip-permissions']);
  assert.equal(options.permissionMode, 'bypassPermissions');
  assert.equal(options.allowDangerouslySkipPermissions, true);
});

test('applyExtraArgs supports inline --flag=value form and ignores unknown flags', () => {
  const options: Record<string, unknown> = {};
  applyExtraArgs(options, ['--model=claude-sonnet-4-5', '--fallback-model=haiku', '--unknown', 'leftover']);
  assert.equal(options.model, 'claude-sonnet-4-5');
  assert.equal(options.fallbackModel, 'haiku');
  assert.equal(options.unknown, undefined);
});

test('applyExtraArgs rejects unknown permission modes so we never feed bad enums into the SDK', () => {
  const options: Record<string, unknown> = { permissionMode: 'default' };
  applyExtraArgs(options, ['--permission-mode', 'notARealMode']);
  assert.equal(options.permissionMode, 'default');
});

test('applyExtraArgs rejects unknown effort levels so we never feed bad enums into the SDK', () => {
  const options: Record<string, unknown> = { effort: 'medium' };
  applyExtraArgs(options, ['--effort', 'notARealEffort']);
  assert.equal(options.effort, 'medium');
});

test('applyExtraArgs setting --permission-mode bypassPermissions also sets the dangerously-skip flag', () => {
  const options: Record<string, unknown> = {};
  applyExtraArgs(options, ['--permission-mode', 'bypassPermissions']);
  assert.equal(options.permissionMode, 'bypassPermissions');
  assert.equal(options.allowDangerouslySkipPermissions, true);
});

// The real SDK ends an interrupted turn with the same error_during_execution
// result it uses for genuine failures. Each scripted turn can hold until
// interrupt() is called, then emit its result.
const executionError = {
  subtype: 'error_during_execution',
  is_error: true,
  errors: ['[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null'],
};

function scriptedQuery(turns: Array<{ waitForInterrupt: boolean; result: Record<string, unknown> }>): QueryFn {
  return ({ prompt }) => {
    let release: (() => void) | undefined;
    async function* gen() {
      let i = 0;
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        void _user;
        const turn = turns[i++];
        yield { type: 'system', subtype: 'init', session_id: 'sess_1' } as any;
        if (turn.waitForInterrupt) await new Promise<void>((resolve) => { release = resolve; });
        yield { type: 'result', ...turn.result } as any;
      }
    }
    return Object.assign(gen(), { interrupt: async () => { release?.(); } });
  };
}

function eventsOf(msgs: any[]): any[] {
  return msgs.filter((m) => m.method === 'event').map((m) => m.params.event);
}

test('an interrupted turn is reported as interrupted, not as a provider error', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), scriptedQuery([
    { waitForInterrupt: true, result: executionError },
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'long task' });
  await tick();
  await bridge.interrupt();
  await tick();

  const events = eventsOf(msgs);
  assert.equal(events.filter((e) => e.type === 'error').length, 0, 'no error event for an interruption');
  const done = events.filter((e) => e.type === 'turn_done');
  assert.equal(done.length, 1);
  assert.equal(done[0].state.state, 'interrupted');
  assert.equal(done[0].error, undefined);
});

test('error_during_execution without an interrupt is still a provider error', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), scriptedQuery([
    { waitForInterrupt: false, result: executionError },
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'go' });
  await tick();

  const events = eventsOf(msgs);
  assert.equal(events.find((e) => e.type === 'error')?.error.code, 'result_error');
  assert.equal(events.find((e) => e.type === 'turn_done').state.state, 'failed');
});

test('an interrupt with no turn in flight does not mask a later failure', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), scriptedQuery([
    { waitForInterrupt: false, result: executionError },
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.interrupt();
  await bridge.startTurn({ text: 'go' });
  await tick();

  const events = eventsOf(msgs);
  assert.equal(events.find((e) => e.type === 'turn_done').state.state, 'failed');
  assert.equal(events.filter((e) => e.type === 'error').length, 1);
});

test('an interrupt only covers the turn it interrupted', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), scriptedQuery([
    { waitForInterrupt: true, result: executionError },
    { waitForInterrupt: false, result: executionError },
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'first' });
  await tick();
  await bridge.interrupt();
  await tick();
  await bridge.startTurn({ text: 'second' });
  await tick();

  const states = eventsOf(msgs).filter((e) => e.type === 'turn_done').map((e) => e.state.state);
  assert.deepEqual(states, ['interrupted', 'failed']);
});

test('a failed interrupt call is not treated as an interruption of the turn', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const fakeQuery: QueryFn = ({ prompt }) => {
    async function* gen() {
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        void _user;
        await new Promise((r) => setTimeout(r, 30));
        yield { type: 'result', ...executionError } as any;
      }
    }
    return Object.assign(gen(), { interrupt: async () => { throw new Error('control channel closed'); } });
  };
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), fakeQuery);
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'go' });
  await bridge.interrupt();
  await new Promise((r) => setTimeout(r, 60));

  const states = eventsOf(msgs).filter((e) => e.type === 'turn_done').map((e) => e.state.state);
  assert.deepEqual(states, ['failed']);
});

// When the account hits a usage limit the SDK yields a synthetic assistant
// message tagged error: 'rate_limit', then a failed result carrying the same text.
function messagesQuery(turns: any[][]): QueryFn {
  return ({ prompt }) => {
    async function* gen() {
      let i = 0;
      for await (const _user of prompt as AsyncIterable<SdkUserMessage>) {
        void _user;
        yield { type: 'system', subtype: 'init', session_id: 'sess_1' } as any;
        for (const m of turns[i++] ?? []) yield m as any;
      }
    }
    return Object.assign(gen(), { interrupt: async () => {} });
  };
}

const limitText = "You've hit your session limit · resets 1:50am (Asia/Shanghai)";
const limitAssistant = {
  type: 'assistant',
  parent_tool_use_id: null,
  error: 'rate_limit',
  message: { role: 'assistant', content: [{ type: 'text', text: limitText }] },
};
const limitResult = { type: 'result', subtype: 'success', is_error: true, result: limitText };

test('a usage-limit result is reported with the rate_limit code', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), messagesQuery([[limitAssistant, limitResult]]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'go' });
  await tick();

  const events = eventsOf(msgs);
  const error = events.find((e) => e.type === 'error')?.error;
  assert.deepEqual(error, { message: limitText, code: 'rate_limit' });
  const done = events.filter((e) => e.type === 'turn_done');
  assert.equal(done.length, 1);
  assert.equal(done[0].state.state, 'failed');
  assert.equal(done[0].error.code, 'rate_limit');
  assert.equal(events.filter((e) => e.type === 'assistant_text_delta').length, 0, 'synthetic text is not streamed');
});

test('the rate_limit tag does not leak into a later turn', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), messagesQuery([
    [limitAssistant, limitResult],
    [{ type: 'result', ...executionError }],
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'one' });
  await tick();
  await bridge.startTurn({ text: 'two' });
  await tick();

  const codes = eventsOf(msgs).filter((e) => e.type === 'error').map((e) => e.error.code);
  assert.deepEqual(codes, ['rate_limit', 'result_error']);
});

test('a non-rate-limit assistant error keeps the result_error code', async () => {
  const output = new PassThrough();
  const msgs = collect(output);
  const bridge = new ClaudeBridge(new RpcPeer(new PassThrough(), output), messagesQuery([
    [{ ...limitAssistant, error: 'server_error' }, { ...limitResult, result: 'API Error: 500' }],
  ]));
  await bridge.startSession({ cwd: '/tmp' });
  await bridge.startTurn({ text: 'go' });
  await tick();

  assert.equal(eventsOf(msgs).find((e) => e.type === 'error')?.error.code, 'result_error');
});
