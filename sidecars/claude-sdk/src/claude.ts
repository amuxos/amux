import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { RpcPeer } from './protocol.js';

// Minimal structural types for the Claude Agent SDK surface we use. The SDK's
// own types are richer; we keep these loose so the sidecar tolerates additive
// SDK changes and so tests can inject a fake `query` without the SDK installed.
export interface SdkUserMessage {
  type: 'user';
  message: { role: 'user'; content: unknown };
  parent_tool_use_id: null;
  uuid?: string;
  // 'next' lets Claude Code attach the message to the running turn at the next
  // tool boundary (or run it right after a text-only turn) without aborting
  // the in-flight request. Measured against SDK 0.3.158 and re-checked on
  // 0.3.296 (CLI 2.1.296); see turn.steer.
  priority?: 'now' | 'next' | 'later';
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
  // amux turn token; turn.steer must name the same token.
  turnToken?: string;
}

interface TurnSteerParams {
  text?: string;
  turnToken?: string;
}

// One amux turn as the sidecar sees it. Steering can make the CLI spend
// several runs (each ending in a `result`) on a single amux turn.
interface AmuxTurn {
  token: string;
  start: SdkUserMessage;
  // False while the start message waits for leftover CLI work of an earlier
  // turn (an orphaned steer run) to finish, so the two never share a run.
  startPushed: boolean;
  // Steers pushed to the CLI whose replay echo has not arrived yet.
  pendingSteers: Set<string>;
  // Steers accepted but not pushed yet (see steerTurn).
  held: SdkUserMessage[];
  // Steers that reached the CLI and so are part of the conversation history.
  sentSteers: string[];
  interruptRequested: boolean;
  ended: boolean;
  // Error tag of the latest synthetic assistant message (SDKAssistantMessage.error,
  // e.g. 'rate_limit'); cleared whenever a run of this turn ends.
  lastAssistantError?: string;
  // `usage` is per CLI run, so a steered turn sums the runs it spans.
  usage?: Record<string, unknown>;
  lastResult?: any;
}

interface PermissionRespondParams {
  permission_id?: string;
  option_id?: string;
  option_ids?: string[];
  cancel?: boolean;
}

interface UserQuestion {
  question: string;
  header: string;
  options: {label: string; description?: string}[];
  multiSelect: boolean;
}

interface UserQuestionBatch {
  turn: AmuxTurn;
  input: Record<string, unknown>;
  entries: Map<string, {question: UserQuestion; answer?: string[]}>;
  resolve: (result: unknown) => void;
  removeAbort?: () => void;
}

// Async FIFO that lets `turn.start` / `turn.steer` hand user messages to the
// long-lived query() input generator. Yields each pushed item; resolves
// cleanly once ended.
class MessageQueue<T> {
  private items: T[] = [];
  private waiters: Array<(v: IteratorResult<T>) => void> = [];
  private ended = false;

  push(text: T): void {
    const w = this.waiters.shift();
    if (w) w({ value: text, done: false });
    else this.items.push(text);
  }

  end(): void {
    this.ended = true;
    let w: ((v: IteratorResult<T>) => void) | undefined;
    while ((w = this.waiters.shift())) w({ value: undefined as unknown as T, done: true });
  }

  async *[Symbol.asyncIterator](): AsyncGenerator<T> {
    for (;;) {
      if (this.items.length > 0) {
        yield this.items.shift() as T;
        continue;
      }
      if (this.ended) return;
      const res = await new Promise<IteratorResult<T>>((resolve) => this.waiters.push(resolve));
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
// mergeMcpConfig loads a Claude CLI style MCP config (a file path or inline
// JSON with a top-level mcpServers map) into options.mcpServers. A bad config
// is ignored rather than crashing the session.
export function mergeMcpConfig(options: Record<string, unknown>, value: string): void {
  let parsed: unknown;
  try {
    const text = value.trim().startsWith('{') ? value : readFileSync(value, 'utf8');
    parsed = JSON.parse(text);
  } catch {
    return;
  }
  const servers = (parsed as { mcpServers?: unknown } | null)?.mcpServers;
  if (!servers || typeof servers !== 'object' || Array.isArray(servers)) return;
  const existing = (options.mcpServers && typeof options.mcpServers === 'object') ? options.mcpServers as Record<string, unknown> : {};
  options.mcpServers = { ...existing, ...(servers as Record<string, unknown>) };
}

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
      case '--mcp-config': {
        // amux injects its own MCP server (amux.instance_spawn & co.) as
        // `--mcp-config <file>`; the SDK takes servers via options.mcpServers,
        // so dropping this flag silently strips every amux tool.
        const v = consumeValue();
        if (v) mergeMcpConfig(options, v);
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
  private input = new MessageQueue<SdkUserMessage>();
  private q?: SdkQuery;
  private sessionId?: string;
  private pendingPermissions = new Map<string, (result: unknown) => void>();
  private pendingQuestions = new Map<string, UserQuestionBatch>();
  private suggestions = new Map<string, unknown>();
  private permSeq = 0;
  private queryFn?: QueryFn;
  // The amux turn in flight (or waiting to start). The runtime dispatches one
  // turn at a time; the SDK ends an interrupted turn with an
  // `error_during_execution` result that looks identical to a real failure, so
  // interrupts are tracked per turn to report them as interruptions.
  private turn?: AmuxTurn;
  // Steer messages of an already ended (interrupted / abandoned) turn that the
  // CLI still has queued. They still run once dequeued; that run is foreign.
  private orphanSteers = new Set<string>();
  // The CLI is executing work that belongs to no live amux turn. Its output is
  // suppressed, it is interrupted as soon as it shows activity, and its result
  // is swallowed; the next turn's start waits until it is over.
  private foreignRun = false;
  // A CLI run is in progress: activity was seen since the last result.
  private runOpen = false;
  // An interrupt was already sent while the current run was open.
  private runInterrupted = false;
  // Steer messages are held back while the model is generating without a tool
  // in flight. Measured with SDK 0.3.158 (unchanged on 0.3.296): a message the
  // CLI attaches mid-turn ('next' / no priority) is rendered next to the tool
  // result and models sometimes dismiss it as tool-output injection, while
  // priority 'now' aborts in-flight text streaming. So a steer is pushed with
  // 'now' only while a tool runs (the CLI lets the tool finish, ends the
  // request at the tool boundary and continues with a real user message), and
  // otherwise is held until the next tool starts or the current run's result
  // arrives.
  private toolsInFlight = new Set<string>();
  // Watchdog for CLI work the sidecar waits on but that is not guaranteed to
  // show up (a steer echo after a result, or leftover work before a deferred
  // start): after this long without CLI activity the wait is abandoned.
  private stallTimer?: ReturnType<typeof setTimeout>;
  private stallMs: number;
  // Steers of an interrupted turn stay in the CLI conversation and read like
  // pending instructions, so the next turn starts with a note naming them as
  // cancelled (measured with CLI 2.1.296: otherwise the model tends to carry
  // them out in the next turn).
  private cancelledSteers: string[] = [];

  // queryFn is injectable for tests; in production it is lazily imported from
  // the Claude Agent SDK so the protocol layer can be unit-tested without it.
  constructor(private peer: RpcPeer, queryFn?: QueryFn, opts: { stallMs?: number } = {}) {
    this.queryFn = queryFn;
    this.stallMs = opts.stallMs ?? 30_000;
  }

  async initialize(): Promise<object> {
    return {
      protocolVersion: 1,
      provider: 'claude',
      capabilities: { permissions: true, interrupt: true, resume: true, steer: true },
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
    const previous = this.turn;
    // The runtime only dispatches after the previous terminal, so a live turn
    // here was given up on (e.g. a timed-out turn.start). Its remaining CLI
    // work must not be credited to the new turn.
    if (previous && !previous.ended) this.abandonTurn(previous);
    this.turn = {
      token: params.turnToken ?? '',
      start: userMessage(params.text ?? ''),
      startPushed: false,
      pendingSteers: new Set(),
      held: [],
      sentSteers: [],
      interruptRequested: false,
      ended: false,
    };
    this.releaseDeferredStart();
    this.refreshStallTimer();
    return { ok: true };
  }

  // turn.steer injects text into the turn that is running now. It is rejected
  // when no turn is in flight, the named turn is not the one in flight, or the
  // turn is being interrupted, so the caller can queue the text as an ordinary
  // next turn instead. An accepted steer is always handed to the CLI unless
  // the turn is interrupted.
  async steerTurn(params: TurnSteerParams): Promise<object> {
    const text = params.text ?? '';
    if (!text.trim()) throw new Error('steer text is required');
    if (!this.q) throw new Error('claude session is not started');
    const turn = this.liveTurn();
    if (!turn) throw new Error('no claude turn in flight');
    if (params.turnToken && params.turnToken !== turn.token) throw new Error('steer target turn is not active');
    if (turn.interruptRequested) throw new Error('claude turn is being interrupted');
    const msg = userMessage(text);
    if (turn.startPushed && this.toolsInFlight.size > 0) {
      msg.priority = 'now';
      turn.pendingSteers.add(msg.uuid as string);
      turn.sentSteers.push(text);
      this.input.push(msg);
      return { ok: true, uuid: msg.uuid, mode: 'tool_boundary' };
    }
    turn.held.push(msg);
    return { ok: true, uuid: msg.uuid, mode: 'held' };
  }

  async interrupt(): Promise<object> {
    // An interrupt with no turn in flight must not mark the next, unrelated
    // turn's failure as an interruption.
    const turn = this.liveTurn();
    if (turn && !turn.startPushed) {
      // The CLI never saw this turn; it was still waiting behind leftover work.
      this.endTurn(turn);
      this.emitEvent('turn_done', { state: { state: 'interrupted', stop_reason: 'interrupted' } });
      this.refreshStallTimer();
      return { ok: true };
    }
    if (turn) {
      turn.interruptRequested = true;
      this.cancelQuestions(turn);
    }
    // An interrupt sent between runs may not reach the run that follows, so
    // only an open run counts as interrupted; otherwise the next run's first
    // activity is interrupted again (see noteRunActivity).
    const marked = this.runOpen && !this.runInterrupted;
    if (marked) this.runInterrupted = true;
    try {
      await this.q?.interrupt?.();
    } catch (err) {
      // The CLI did not get the interrupt: the turn keeps running with all of
      // its steers.
      if (turn && !turn.ended) turn.interruptRequested = false;
      if (marked) this.runInterrupted = false;
      this.emitError(err);
      return { ok: true };
    }
    // Held steers never reached the CLI; they are cancelled with the turn.
    if (turn) turn.held = [];
    return { ok: true };
  }

  async respondPermission(params: PermissionRespondParams): Promise<object> {
    const id = params.permission_id ?? '';
    const batch = this.pendingQuestions.get(id);
    if (batch) {
      if (batch.turn !== this.liveTurn() || batch.turn.interruptRequested) throw new Error('user_input_expired');
      if (params.cancel) {
        if (params.option_id || params.option_ids?.length) throw new Error('user_input_cancel_with_answers');
        this.finishQuestions(batch, {behavior: 'deny', message: 'User cancelled the question. No answer was selected.'});
        return {ok: true};
      }
      const entry = batch.entries.get(id)!;
      if (entry.answer) throw new Error('user_input_already_answered');
      const values = params.option_ids ?? [params.option_id ?? ''];
      if (!Array.isArray(values) || values.some(value => typeof value !== 'string')) throw new Error('user_input_invalid_answer');
      const answers = [...new Set(values.map(value => value.trim()).filter(Boolean))];
      if (answers.length === 0 || (!entry.question.multiSelect && answers.length !== 1)) throw new Error('user_input_invalid_answer');
      entry.answer = answers;
      this.pendingQuestions.delete(id);
      this.emitEvent('permission_resolved', {permission: {id}});
      if ([...batch.entries.values()].every(value => value.answer)) {
        const mapped = Object.fromEntries([...batch.entries.values()].map(value => [value.question.question,
          value.question.multiSelect ? value.answer : value.answer![0]]));
        this.finishQuestions(batch, {behavior: 'allow', updatedInput: {...batch.input, answers: mapped}});
      }
      return {ok: true};
    }
    if (params.cancel || params.option_ids?.length) throw new Error('user_input_not_pending');
    const resolve = this.pendingPermissions.get(id);
    if (resolve) {
      this.pendingPermissions.delete(id);
      resolve(this.permissionResult(id, params.option_id));
    } else { throw new Error('permission_not_pending'); }
    this.suggestions.delete(id);
    return { ok: true };
  }

  async shutdown(): Promise<object> {
    this.cancelQuestions();
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
    // Echo consumed stdin user messages so turn.steer can observe when the CLI
    // actually picked a steer message up (see onMessage 'user').
    options.extraArgs = { 'replay-user-messages': null };
    applyExtraArgs(options, params.extraArgs);
    return options;
  }

  private async *inputGen(): AsyncGenerator<SdkUserMessage> {
    for await (const msg of this.input) {
      yield msg;
    }
  }

  // The amux turn in flight or waiting to start, if any.
  private liveTurn(): AmuxTurn | undefined {
    const turn = this.turn;
    return turn && !turn.ended ? turn : undefined;
  }

  // CLI output and permission requests belong to the live turn only while the
  // CLI works on it; leftover work of an ended turn is never surfaced, since
  // the runtime would attribute it to whichever turn is current.
  private outputOpen(): boolean {
    const turn = this.liveTurn();
    return !!turn && turn.startPushed && !this.foreignRun;
  }

  private endTurn(turn: AmuxTurn): void {
    this.cancelQuestions(turn);
    turn.ended = true;
    turn.held = [];
    // Steers the CLI still has queued run anyway once dequeued.
    for (const id of turn.pendingSteers) this.orphanSteers.add(id);
    turn.pendingSteers.clear();
    if (this.turn === turn) this.turn = undefined;
  }

  private abandonTurn(turn: AmuxTurn): void {
    // Mid-run, or before its first result, the CLI is still busy with it.
    if (turn.startPushed && (this.runOpen || !turn.lastResult)) this.foreignRun = true;
    this.endTurn(turn);
  }

  // Hands the live turn's start message to the CLI once no leftover work can
  // share its run.
  private releaseDeferredStart(): void {
    const turn = this.liveTurn();
    if (!turn || turn.startPushed || this.foreignRun || this.orphanSteers.size > 0) return;
    turn.startPushed = true;
    this.runInterrupted = false;
    if (this.cancelledSteers.length > 0) {
      turn.start.message.content = `${cancelledSteerNote(this.cancelledSteers)}\n\n${String(turn.start.message.content)}`;
      this.cancelledSteers = [];
    }
    this.input.push(turn.start);
  }

  private flushHeld(turn: AmuxTurn, priority?: 'now'): void {
    const held = turn.held;
    turn.held = [];
    for (const msg of held) {
      if (priority) msg.priority = priority;
      turn.pendingSteers.add(msg.uuid as string);
      turn.sentSteers.push(String(msg.message.content));
      this.input.push(msg);
    }
  }

  private onEcho(uuid: unknown): void {
    const id = typeof uuid === 'string' ? uuid : '';
    if (!id) return;
    if (this.orphanSteers.delete(id)) this.foreignRun = true;
    else this.turn?.pendingSteers.delete(id);
  }

  // Runs for every CLI message that belongs to a run. Leftover work is
  // interrupted as soon as it shows activity (so an interrupted user's queued
  // correction does not execute), and so is a run that starts after its turn
  // was asked to stop: an interrupt sent while a 'now' steer is queued, or
  // between runs, does not reliably stop the next run.
  private noteRunActivity(): void {
    this.runOpen = true;
    if (!this.outputOpen()) {
      if (this.foreignRun || this.orphanSteers.size > 0) {
        this.foreignRun = true;
        this.interruptRun();
      }
    } else if (this.turn?.interruptRequested) {
      this.interruptRun();
    }
    this.refreshStallTimer();
  }

  private interruptRun(): void {
    if (this.runInterrupted) return;
    this.runInterrupted = true;
    Promise.resolve()
      .then(() => this.q?.interrupt?.())
      .catch((err) => console.error('claude sidecar: interrupt failed:', err instanceof Error ? err.message : String(err)));
  }

  // CLI work the sidecar waits on but that is not guaranteed to show up.
  private waitingOnCli(): boolean {
    const turn = this.liveTurn();
    if (!turn) return false;
    if (!turn.startPushed) return true;
    return !this.runOpen && turn.pendingSteers.size > 0;
  }

  private refreshStallTimer(): void {
    if (this.stallTimer) clearTimeout(this.stallTimer);
    this.stallTimer = undefined;
    if (!this.waitingOnCli()) return;
    this.stallTimer = setTimeout(() => this.onStall(), this.stallMs);
    this.stallTimer.unref?.();
  }

  private onStall(): void {
    this.stallTimer = undefined;
    const turn = this.liveTurn();
    if (!turn || !this.waitingOnCli()) return;
    console.error('claude sidecar: expected CLI work did not arrive; continuing without it');
    if (!turn.startPushed) {
      this.orphanSteers.clear();
      this.foreignRun = false;
      this.releaseDeferredStart();
      return;
    }
    // The pushed steers never started a run: end the turn with its last result.
    // endTurn turns them into orphans, so if the CLI runs one late anyway the
    // next turn's start waits behind it and its output is never attributed to
    // that turn.
    this.finishTurn(turn, turn.lastResult ?? { subtype: 'success' }, false);
  }

  private async consume(): Promise<void> {
    if (!this.q) return;
    try {
      for await (const msg of this.q) {
        this.onMessage(msg);
      }
    } catch (err) {
      const turn = this.liveTurn();
      if (turn) this.endTurn(turn);
      this.orphanSteers.clear();
      this.foreignRun = false;
      this.refreshStallTimer();
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
        this.noteRunActivity();
        if (isSubagentMessage || !this.outputOpen()) break;
        this.onStreamEvent(msg.event);
        break;
      case 'assistant': {
        this.noteRunActivity();
        const turn = this.liveTurn();
        if (isSubagentMessage || !turn || !this.outputOpen()) break;
        if (typeof msg.error === 'string' && msg.error) turn.lastAssistantError = msg.error;
        this.onAssistant(msg.message);
        break;
      }
      case 'user':
        // replay-user-messages echoes every consumed stdin user message; the
        // echo tells which steer the CLI has picked up.
        if (!isSubagentMessage && msg.isReplay === true) this.onEcho(msg.uuid);
        this.noteRunActivity();
        if (isSubagentMessage || msg.isReplay === true || !this.outputOpen()) break;
        this.onToolResults(msg.message);
        break;
      case 'result':
        this.onResult(msg);
        break;
      default:
        break;
    }
  }

  // A result ends one CLI run. A steered amux turn spans several runs and
  // ends with the run that follows its last steer.
  private onResult(msg: any): void {
    const foreign = !this.outputOpen();
    this.runOpen = false;
    this.runInterrupted = false;
    this.toolsInFlight.clear();
    if (foreign) {
      // Leftover work of an ended turn (orphaned steer, abandoned turn).
      this.foreignRun = false;
      this.releaseDeferredStart();
      this.refreshStallTimer();
      return;
    }
    const turn = this.turn as AmuxTurn;
    turn.usage = addUsage(turn.usage, msg.usage);
    turn.lastResult = msg;
    if (turn.interruptRequested) {
      // A queued 'now' steer can absorb the interrupt, ending the run at the
      // tool boundary (stop_reason 'tool_use') instead; the user still asked
      // to stop. Steers the CLI still holds become orphans.
      const interrupted = msg.subtype === 'error_during_execution' || msg.stop_reason === 'tool_use';
      this.finishTurn(turn, msg, interrupted);
      return;
    }
    if (turn.held.length > 0 || turn.pendingSteers.size > 0) {
      // Accepted steers still have to run, even after a failed run (they get
      // a fresh run, e.g. after error_max_turns): the turn continues with
      // them. A held steer starts that run as an ordinary user message.
      turn.lastAssistantError = undefined;
      this.flushHeld(turn);
      this.refreshStallTimer();
      return;
    }
    this.finishTurn(turn, msg, false);
  }

  private finishTurn(turn: AmuxTurn, msg: any, interrupted: boolean): void {
    const assistantError = turn.lastAssistantError;
    const usage = turn.usage;
    this.endTurn(turn);
    this.refreshStallTimer();
    if (interrupted) {
      this.cancelledSteers.push(...turn.sentSteers);
      this.emitEvent('turn_done', { state: { state: 'interrupted', stop_reason: 'interrupted' } });
      return;
    }
    if (msg.is_error === true || (msg.subtype && msg.subtype !== 'success')) {
      const message = (typeof msg.result === 'string' && msg.result)
        || (Array.isArray(msg.errors) && msg.errors.map(String).join('\n'))
        || String(msg.subtype ?? 'Claude request failed');
      // A synthetic assistant message tagged 'rate_limit' (e.g. "You've hit
      // your session limit") precedes the failed result; keep that
      // classification so amux can explain the limit instead of a generic error.
      const code = assistantError === 'rate_limit' ? 'rate_limit' : 'result_error';
      const error = { message, code };
      this.emitEvent('error', { error });
      this.emitEvent('turn_done', { state: { state: 'failed', stop_reason: 'error' }, error });
      return;
    }
    if (typeof msg.result === 'string' || usage || typeof msg.total_cost_usd === 'number') {
      // Native turn result: the final text + usage + cost, surfaced so the
      // task control plane can record a summary without an injected envelope.
      // Emitted before turn_done so the runner has it at the terminal signal.
      // total_cost_usd is already cumulative; usage is summed over the runs.
      this.emitEvent('result', {
        result: {
          text: typeof msg.result === 'string' ? msg.result : '',
          usage,
          cost_usd: typeof msg.total_cost_usd === 'number' ? msg.total_cost_usd : undefined,
        },
      });
    }
    const stopReason = msg.stop_reason ?? msg.subtype ?? 'end_turn';
    this.emitEvent('turn_done', { state: { state: 'turn_done', stop_reason: String(stopReason) } });
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
        this.toolsInFlight.add(String(block.id ?? ''));
        const turn = this.liveTurn();
        if (turn && turn.held.length > 0 && !turn.interruptRequested) this.flushHeld(turn, 'now');
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
        this.toolsInFlight.delete(String(block.tool_use_id ?? ''));
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
    // Leftover work of an ended turn, or a turn being interrupted, must not
    // ask the user (the request would surface on an unrelated turn).
    if (!this.outputOpen() || this.turn?.interruptRequested) {
      return Promise.resolve({ behavior: 'deny', message: 'Interrupted by user' });
    }
    if (toolName === 'AskUserQuestion') return this.askUserQuestions(input, ctx);
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

  private askUserQuestions(input: Record<string, unknown>, ctx: any): Promise<unknown> {
    const questions = input.questions;
    if (!Array.isArray(questions) || questions.length === 0 || questions.length > 4 ||
        questions.some(q => !q || typeof q.question !== 'string' || !q.question.trim() ||
          !Array.isArray(q.options) || q.options.some((o: any) => !o || typeof o.label !== 'string' || !o.label.trim())) ||
        new Set(questions.map(q => q.question)).size !== questions.length) {
      return Promise.resolve({behavior: 'deny', message: 'Invalid user question schema.'});
    }
    if (ctx?.signal?.aborted) return Promise.resolve({behavior: 'deny', message: 'Question interrupted.'});
    return new Promise(resolve => {
      const batch: UserQuestionBatch = {turn: this.liveTurn()!, input, entries: new Map(), resolve};
      for (const question of questions as UserQuestion[]) {
        const id = `question_${++this.permSeq}`;
        batch.entries.set(id, {question});
        this.pendingQuestions.set(id, batch);
      }
      if (ctx?.signal) {
        const abort = () => this.finishQuestions(batch, {behavior: 'deny', message: 'Question interrupted.'});
        ctx.signal.addEventListener('abort', abort, {once: true});
        batch.removeAbort = () => ctx.signal.removeEventListener('abort', abort);
      }
      for (const [id, {question}] of batch.entries) {
        this.peer.notify('permission.request', {permission: {
          id, provider: 'claude',
          toolCall: {id: ctx?.toolUseID ?? '', name: 'AskUserQuestion', title: question.header ?? '',
            question: question.question, multiSelect: !!question.multiSelect, allowOther: true, blocking: true},
          options: question.options.map(option => ({optionId: option.label, name: option.label, description: option.description ?? ''})),
        }});
      }
    });
  }

  private finishQuestions(batch: UserQuestionBatch, result: unknown): void {
    batch.removeAbort?.();
    for (const id of batch.entries.keys()) {
      if (this.pendingQuestions.delete(id)) this.emitEvent('permission_resolved', {permission: {id}});
    }
    batch.resolve(result);
  }

  private cancelQuestions(turn?: AmuxTurn): void {
    for (const batch of new Set(this.pendingQuestions.values())) {
      if (!turn || batch.turn === turn) this.finishQuestions(batch, {behavior: 'deny', message: 'Question interrupted.'});
    }
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

function cancelledSteerNote(steers: string[]): string {
  // Quote what the user typed, without the amux framing note around it.
  const quoted = steers.map((text) => {
    const body = text.replace(/^\[amux 提示：[\s\S]*?\]\n\n/, '').trim();
    return `「${body.length > 300 ? `${body.slice(0, 300)}…` : body}」`;
  });
  return '[amux 提示：用户中断了上一个任务。用户在该任务执行期间追加的下列消息已随中断取消，不要执行其中的操作，' +
    `也不要继续上一个任务，除非用户在下面再次提出：\n${quoted.join('\n')}]`;
}

function userMessage(text: string, priority?: 'now' | 'next' | 'later'): SdkUserMessage {
  const msg: SdkUserMessage = {
    type: 'user',
    message: { role: 'user', content: text },
    parent_tool_use_id: null,
    uuid: randomUUID(),
  };
  if (priority) msg.priority = priority;
  return msg;
}

// Sums per-run usage objects field by field (numbers add, nested objects
// recurse, anything else keeps the latest value).
function addUsage(total: Record<string, unknown> | undefined, next: unknown): Record<string, unknown> | undefined {
  if (!isPlainObject(next)) return total;
  if (!total) return structuredClone(next);
  const out: Record<string, unknown> = { ...total };
  for (const [key, value] of Object.entries(next)) {
    const current = out[key];
    if (typeof value === 'number' && typeof current === 'number') out[key] = current + value;
    else if (isPlainObject(value) && isPlainObject(current)) out[key] = addUsage(current, value);
    else out[key] = value;
  }
  return out;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
