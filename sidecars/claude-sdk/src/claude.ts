import { RpcPeer } from './protocol.js';

// Minimal structural types for the Claude Agent SDK surface we use. The SDK's
// own types are richer; we keep these loose so the sidecar tolerates additive
// SDK changes and so tests can inject a fake `query` without the SDK installed.
export interface SdkUserMessage {
  type: 'user';
  message: { role: 'user'; content: unknown };
  parent_tool_use_id: null;
}

export interface SdkQuery extends AsyncIterable<any> {
  interrupt?: () => Promise<void>;
  close?: () => void;
  applyFlagSettings?: (settings: { env: Record<string, string> }) => Promise<void>;
}

export type QueryFn = (args: { prompt: AsyncIterable<SdkUserMessage>; options: Record<string, unknown> }) => SdkQuery;

export interface SessionStartParams {
  amuxSessionId?: string;
  cwd?: string;
  env?: string[];
  extraArgs?: string[];
  claudeExecutable?: string;
  resume?: string;
}

interface TurnStartParams {
  text?: string;
}

interface PermissionRespondParams {
  permission_id?: string;
  option_id?: string;
}

// Async FIFO that lets `turn.start` hand user text to the long-lived query()
// input generator. Yields each pushed item; resolves cleanly once ended.
class TextQueue {
  private items: string[] = [];
  private waiters: Array<(v: IteratorResult<string>) => void> = [];
  private ended = false;

  push(text: string): void {
    const w = this.waiters.shift();
    if (w) w({ value: text, done: false });
    else this.items.push(text);
  }

  end(): void {
    this.ended = true;
    let w: ((v: IteratorResult<string>) => void) | undefined;
    while ((w = this.waiters.shift())) w({ value: undefined as unknown as string, done: true });
  }

  async *[Symbol.asyncIterator](): AsyncGenerator<string> {
    for (;;) {
      if (this.items.length > 0) {
        yield this.items.shift() as string;
        continue;
      }
      if (this.ended) return;
      const res = await new Promise<IteratorResult<string>>((resolve) => this.waiters.push(resolve));
      if (res.done) return;
      yield res.value;
    }
  }
}

export type PermissionMode = 'default' | 'acceptEdits' | 'bypassPermissions' | 'plan';
export type EffortLevel = 'low' | 'medium' | 'high' | 'xhigh' | 'max';

const PERMISSION_MODES: ReadonlySet<PermissionMode> = new Set([
  'default',
  'acceptEdits',
  'bypassPermissions',
  'plan',
]);

const EFFORT_LEVELS: ReadonlySet<EffortLevel> = new Set([
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
]);

// applyExtraArgs walks extraArgs as CLI-style flags and maps the well-known
// ones onto Claude Agent SDK options. The amux product model says adapters
// must not silently drop user-supplied args after `--`; for the SDK sidecar we
// fold the flags we know into options so `amux run claude -- --model opus
// --permission-mode bypassPermissions` and the desktop's structured Model /
// Permission Mode pickers reach the SDK. Unknown flags are ignored so future
// CLI additions don't crash old sidecars.
export function applyExtraArgs(options: Record<string, unknown>, extraArgs: string[] | undefined): void {
  if (!extraArgs || extraArgs.length === 0) return;
  const args = extraArgs;
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (typeof arg !== 'string') continue;
    const { flag, inlineValue } = splitFlag(arg);
    if (!flag) continue;
    const consumeValue = (): string | undefined => {
      if (inlineValue !== undefined) return inlineValue;
      const next = args[i + 1];
      if (typeof next === 'string' && !next.startsWith('-')) {
        i++;
        return next;
      }
      return undefined;
    };
    switch (flag) {
      case '--model': {
        const v = consumeValue();
        if (v) options.model = v;
        break;
      }
      case '--fallback-model': {
        const v = consumeValue();
        if (v) options.fallbackModel = v;
        break;
      }
      case '--permission-mode': {
        const v = consumeValue();
        if (v && (PERMISSION_MODES as ReadonlySet<string>).has(v)) {
          options.permissionMode = v as PermissionMode;
          if (v === 'bypassPermissions') options.allowDangerouslySkipPermissions = true;
        }
        break;
      }
      case '--dangerously-skip-permissions':
      case '--bypass-permissions':
        options.permissionMode = 'bypassPermissions';
        options.allowDangerouslySkipPermissions = true;
        break;
      case '--append-system-prompt': {
        const v = consumeValue();
        // query() accepts the public preset.append option. A top-level
        // appendSystemPrompt is an internal transport field and is ignored.
        if (v) options.systemPrompt = { type: 'preset', preset: 'claude_code', append: v };
        break;
      }
      case '--effort': {
        const v = consumeValue();
        if (v && (EFFORT_LEVELS as ReadonlySet<string>).has(v)) {
          options.effort = v as EffortLevel;
        }
        break;
      }
      case '--max-thinking-tokens': {
        const v = consumeValue();
        const n = v !== undefined ? Number(v) : NaN;
        if (Number.isFinite(n) && n > 0) options.maxThinkingTokens = n;
        break;
      }
      default:
        // Unknown flag: don't try to consume a value — leaving `next` for the
        // next iteration keeps it safe to add new SDK options in callers.
        break;
    }
  }
}

function splitFlag(arg: string): { flag: string | null; inlineValue: string | undefined } {
  if (!arg.startsWith('--')) return { flag: null, inlineValue: undefined };
  const eq = arg.indexOf('=');
  if (eq < 0) return { flag: arg, inlineValue: undefined };
  return { flag: arg.slice(0, eq), inlineValue: arg.slice(eq + 1) };
}

export class ClaudeBridge {
  private seq = 0;
  private started = false;
  private input = new TextQueue();
  private q?: SdkQuery;
  private sessionId?: string;
  private pendingPermissions = new Map<string, (result: unknown) => void>();
  private suggestions = new Map<string, unknown>();
  private permSeq = 0;
  private queryFn?: QueryFn;
  // The SDK ends an interrupted turn with an `error_during_execution` result
  // that looks identical to a real failure. Track our own interrupt requests so
  // that result can be reported as an interruption instead of a provider error.
  private pendingTurns = 0;
  private interruptRequested = false;

  // queryFn is injectable for tests; in production it is lazily imported from
  // the Claude Agent SDK so the protocol layer can be unit-tested without it.
  constructor(private peer: RpcPeer, queryFn?: QueryFn) {
    this.queryFn = queryFn;
  }

  async initialize(): Promise<object> {
    return {
      protocolVersion: 1,
      provider: 'claude',
      capabilities: { permissions: true, interrupt: true, resume: true },
    };
  }

  async startSession(params: SessionStartParams = {}): Promise<object> {
    if (this.started) return { provider: 'claude' };
    this.started = true;

    // Isolation uses an empty override to mask the service user's setting.
    // The SDK treats it as a relative path; unset it before importing the SDK
    // so its own settings/debug files follow the isolated HOME as well.
    if (process.env.CLAUDE_CONFIG_DIR === '') delete process.env.CLAUDE_CONFIG_DIR;
    const queryFn = this.queryFn ?? (await this.loadQuery());
    const options = this.buildOptions(params);
    this.q = queryFn({ prompt: this.inputGen(), options });

    const env = options.env as Record<string, string>;
    if (env.AMUX_MODEL_SERVICE) {
      // Native settings.env overrides inherited process.env. Use the SDK
      // control channel to pin this explicitly selected route without putting
      // credentials in --settings argv or changing the user's settings files.
      const overrides: Record<string, string> = {};
      for (const name of [
        'ANTHROPIC_BASE_URL', 'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_API_KEY',
        'ANTHROPIC_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL',
        'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_HAIKU_MODEL',
        'CLAUDE_CODE_SUBAGENT_MODEL', 'CLAUDE_CODE_MAX_CONTEXT_TOKENS',
        'CLAUDE_CODE_MAX_OUTPUT_TOKENS', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC',
      ]) {
        if (env[name] !== undefined) overrides[name] = env[name];
      }
      try {
        if (!this.q.applyFlagSettings) throw new Error('Claude SDK cannot apply model-service settings');
        await this.q.applyFlagSettings({ env: overrides });
      } catch (err) {
        this.q.close?.();
        this.q = undefined;
        this.started = false;
        throw err;
      }
    }

    // runtime.pid lets the Go side fall back to signalling the process.
    this.peer.notify('runtime.pid', { pid: process.pid });

    // Drive the unified message stream in the background; events are delivered
    // to amux as notifications, decoupled from the turn.start request/response.
    void this.consume();
    return { provider: 'claude' };
  }

  async startTurn(params: TurnStartParams): Promise<object> {
    this.pendingTurns++;
    this.input.push(params.text ?? '');
    return { ok: true };
  }

  async interrupt(): Promise<object> {
    // An interrupt with no turn in flight must not mark the next, unrelated
    // turn's failure as an interruption.
    if (this.pendingTurns > 0) this.interruptRequested = true;
    try {
      await this.q?.interrupt?.();
    } catch (err) {
      this.interruptRequested = false;
      this.emitError(err);
    }
    return { ok: true };
  }

  async respondPermission(params: PermissionRespondParams): Promise<object> {
    const id = params.permission_id ?? '';
    const resolve = this.pendingPermissions.get(id);
    if (resolve) {
      this.pendingPermissions.delete(id);
      resolve(this.permissionResult(id, params.option_id));
    }
    this.suggestions.delete(id);
    return { ok: true };
  }

  async shutdown(): Promise<object> {
    this.input.end();
    try {
      this.q?.close?.();
    } catch {
      // best effort
    }
    queueMicrotask(() => process.exit(0));
    return { ok: true };
  }

  // ---- internals ----------------------------------------------------------

  private async loadQuery(): Promise<QueryFn> {
    const mod = (await import('@anthropic-ai/claude-agent-sdk')) as { query: QueryFn };
    return mod.query;
  }

  private buildOptions(params: SessionStartParams): Record<string, unknown> {
    // env replaces (not merges) the SDK subprocess env, so start from our own.
    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(process.env)) {
      if (v !== undefined) env[k] = v;
    }
    for (const entry of params.env ?? []) {
      const eq = entry.indexOf('=');
      if (eq > 0) env[entry.slice(0, eq)] = entry.slice(eq + 1);
    }
    if (env.CLAUDE_CONFIG_DIR === '') delete env.CLAUDE_CONFIG_DIR;
    const options: Record<string, unknown> = {
      cwd: params.cwd,
      env,
      permissionMode: 'default',
      includePartialMessages: true,
      systemPrompt: { type: 'preset', preset: 'claude_code' },
      settingSources: ['user', 'project', 'local'],
      canUseTool: this.canUseTool.bind(this),
    };
    if (params.claudeExecutable) options.pathToClaudeCodeExecutable = params.claudeExecutable;
    if (params.resume) options.resume = params.resume;
    applyExtraArgs(options, params.extraArgs);
    return options;
  }

  private async *inputGen(): AsyncGenerator<SdkUserMessage> {
    for await (const text of this.input) {
      yield { type: 'user', message: { role: 'user', content: text }, parent_tool_use_id: null };
    }
  }

  private async consume(): Promise<void> {
    if (!this.q) return;
    try {
      for await (const msg of this.q) {
        this.onMessage(msg);
      }
    } catch (err) {
      this.emitError(err);
      this.emitEvent('turn_done', { state: { state: 'turn_done', stop_reason: 'error' } });
    }
  }

  private onMessage(msg: any): void {
    const isSubagentMessage = typeof msg?.parent_tool_use_id === 'string' && msg.parent_tool_use_id !== '';
    switch (msg?.type) {
      case 'system':
        if (msg.subtype === 'init' && typeof msg.session_id === 'string') {
          if (this.sessionId !== msg.session_id) {
            this.sessionId = msg.session_id;
            // Surface to amux so it can persist for /restart resume.
            this.peer.notify('session.id', { sessionId: this.sessionId });
          }
        }
        break;
      case 'stream_event':
        if (isSubagentMessage) break;
        this.onStreamEvent(msg.event);
        break;
      case 'assistant':
        if (isSubagentMessage) break;
        this.onAssistant(msg.message);
        break;
      case 'user':
        if (isSubagentMessage) break;
        this.onToolResults(msg.message);
        break;
      case 'result': {
        const interrupted = this.interruptRequested && msg.subtype === 'error_during_execution';
        this.interruptRequested = false;
        this.pendingTurns = Math.max(0, this.pendingTurns - 1);
        if (interrupted) {
          this.emitEvent('turn_done', { state: { state: 'interrupted', stop_reason: 'interrupted' } });
          break;
        }
        if (msg.is_error === true || (msg.subtype && msg.subtype !== 'success')) {
          const message = (typeof msg.result === 'string' && msg.result)
            || (Array.isArray(msg.errors) && msg.errors.map(String).join('\n'))
            || String(msg.subtype ?? 'Claude request failed');
          const error = { message, code: 'result_error' };
          this.emitEvent('error', { error });
          this.emitEvent('turn_done', { state: { state: 'failed', stop_reason: 'error' }, error });
          break;
        }
        if (typeof msg.result === 'string' || msg.usage || typeof msg.total_cost_usd === 'number') {
          // Native turn result: the final text + usage + cost, surfaced so the
          // task control plane can record a summary without an injected envelope.
          // Emitted before turn_done so the runner has it at the terminal signal.
          this.emitEvent('result', {
            result: {
              text: typeof msg.result === 'string' ? msg.result : '',
              usage: msg.usage,
              cost_usd: typeof msg.total_cost_usd === 'number' ? msg.total_cost_usd : undefined,
            },
          });
        }
        const stopReason = msg.stop_reason ?? msg.subtype ?? 'end_turn';
        this.emitEvent('turn_done', { state: { state: 'turn_done', stop_reason: String(stopReason) } });
        break;
      }
      default:
        break;
    }
  }

  // Incremental assistant text/thinking from raw Anthropic stream events.
  private onStreamEvent(event: any): void {
    if (event?.type !== 'content_block_delta') return;
    const delta = event.delta;
    if (delta?.type === 'text_delta' && typeof delta.text === 'string') {
      this.emitEvent('assistant_text_delta', { text: delta.text });
    } else if (delta?.type === 'thinking_delta' && typeof delta.thinking === 'string') {
      this.emitEvent('thinking_delta', { text: delta.thinking });
    }
  }

  // Full assistant message: surface tool_use blocks as tool activity. Text is
  // intentionally skipped here because it already streamed via stream_event
  // deltas — emitting it again would double the output.
  private onAssistant(message: any): void {
    const content = Array.isArray(message?.content) ? message.content : [];
    for (const block of content) {
      if (block?.type === 'tool_use') {
        this.emitEvent('tool_started', {
          tool: { id: block.id ?? '', name: block.name ?? '', status: 'running', input: block.input ?? {} },
        });
      }
    }
  }

  private onToolResults(message: any): void {
    const content = Array.isArray(message?.content) ? message.content : [];
    for (const block of content) {
      if (block?.type === 'tool_result') {
        const preview = this.previewToolResult(block.content);
        this.emitEvent('tool_completed', {
          tool: {
            id: block.tool_use_id ?? '',
            status: block.is_error ? 'failed' : 'completed',
            output_preview: preview,
            error: block.is_error ? preview : undefined,
          },
        });
      }
    }
  }

  private previewToolResult(content: unknown): string {
    if (typeof content === 'string') return content.slice(0, 2000);
    if (Array.isArray(content)) {
      return content
        .map((c: any) => (typeof c?.text === 'string' ? c.text : ''))
        .join('')
        .slice(0, 2000);
    }
    return '';
  }

  // Bridge the SDK's tool-approval callback to amux's permission RPC: emit a
  // permission.request notification and await the user's permission.respond.
  private canUseTool(toolName: string, input: Record<string, unknown>, ctx: any): Promise<unknown> {
    const id = `perm_${++this.permSeq}`;
    if (ctx?.suggestions) this.suggestions.set(id, ctx.suggestions);
    this.peer.notify('permission.request', {
      permission: {
        id,
        provider: 'claude',
        toolCall: {
          id: ctx?.toolUseID ?? '',
          name: toolName,
          rawInput: JSON.stringify(input ?? {}),
        },
        options: [
          { optionId: 'allow', name: 'Allow' },
          { optionId: 'allow_always', name: 'Allow always' },
          { optionId: 'deny', name: 'Deny' },
        ],
      },
    });
    return new Promise<unknown>((resolve) => this.pendingPermissions.set(id, resolve));
  }

  private permissionResult(id: string, optionId?: string): unknown {
    switch (optionId) {
      case 'allow':
        return { behavior: 'allow' };
      case 'allow_always':
        return { behavior: 'allow', updatedPermissions: this.suggestions.get(id) };
      default:
        return { behavior: 'deny', message: 'Denied by user' };
    }
  }

  private emitError(err: unknown): void {
    this.emitEvent('error', { error: { message: err instanceof Error ? err.message : String(err) } });
  }

  private emitEvent(type: string, fields: Record<string, unknown>): void {
    this.peer.notify('event', {
      event: { id: `claude_${++this.seq}`, seq: this.seq, provider: 'claude', type, ...fields },
    });
  }
}
