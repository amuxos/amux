# Canonical Agent Event Timeline Owns Agent Turn Lifecycle

[English](2026-08-27-canonical-agent-event-timeline.md) | [简体中文](2026-08-27-canonical-agent-event-timeline.zh-CN.md)

> Retained design record from the original repository's main branch. Dates, phases, source line numbers, and implementation assessments describe the state at the time. For current behavior, use the [CLI product model](../current/cli-product-model.md) and the relevant domain models.
> Source paths are relative to the public repository root. Implementations formerly in `cmd/amux`, `cmd/amuxd`, and `cmd/amuxlet` moved to `app/cli`, `app/hub`, and `app/runtime`; `cmd/*` now contains distribution entrypoints only.

Status: accepted; durable Timeline, first-party consumer migration, legacy lifecycle cleanup and direct typed producer emission are implemented

Date: 2026-08-27

## Context

amux supports ACP agents and multiple structured providers. Their native
protocols disagree about event names, terminal signals, result placement,
permission requests, retry notifications and provider-native turn IDs.
Normalizing those differences is unavoidable business complexity. Repeating
the normalization at every internal layer is accidental complexity.

Before this decision, one logical turn could be represented successively as a
`provider.Event`, `session.Event`, broker frame, flat protobuf `AttachEvent`,
`agentui.Event` and `TaskEvent`. Session code also assigned Turn identity and
inferred terminal state from combinations of event kind, provider event type,
state and stop reason. Task and presentation consumers then interpreted parts
of the same information again.

This is not only duplication. A structured-broker terminal once lost its typed
provider event while crossing the process seam. Session could not recognize
the frame as terminal, so its turn gate did not close and the next queued input
could wait forever. The workaround added provider event type to a state frame
and reconstructed the event on the runtime side. That patch was necessary, but
it showed that lifecycle truth did not have a single owner.

This ADR supersedes only the terminal-ownership and propagation mechanism
described by
the earlier structured-provider watchdog design (current provider rules are maintained in the
[Agent / Adapter product model](../current/agent-adapter-product-model.md)).
Its timeout, activity, user-wait and tool-wait policies remain valid; their
expiry paths still clean up producer-owned watchdog/turn state and emit an
explicit typed terminal through the exact per-turn capability. Session maps it
to the exact canonical Turn. Watchdog cleanup is not part of
the Session completion barrier.

The former shared `provider.Event` / `session.Event` chain was an optional-field
compatibility envelope and did not own canonical Turn identity, ordering or
terminal idempotency. It has been removed from the producer-to-Session path;
Claude's native sidecar JSON DTO remains local to its provider decoder.

At the same time, amux has three deliberately different histories:

- The canonical Session Timeline is a durable, provider-neutral record for
  structured Agent turn review and cursor-based delivery.
- Session visible-text replay remains a derived conversation-handoff view; it
  is not a live event stream or lifecycle authority.
- `TaskEvent` is an append-only, redacted, durable TaskRun stream with its own
  sequence, cursor and replay contract.

They are not three competing sources of current state. Session and TaskRun
snapshots remain authoritative for current control-plane state; the Timeline
records observable Agent behavior. Replacing all state with one event-sourced
store would expand the change far beyond the review requirement and force
processes, sockets, authorization leases and Task control state into an
unnatural replay model.

## Decision

### 1. Add a session-owned Agent Event Timeline

Each Session composes one `internal/agentstream` Timeline. The Timeline is the
canonical, provider-neutral live truth for Agent turn events. It owns:

- amux Turn identity and event ordering;
- an exact per-turn capability used by Session to accept typed producer emissions;
- typed event validation;
- exactly one canonical `TurnFinished` per accepted turn;
- terminal idempotency and rejection of events arriving after terminal;
- live subscription and bounded Session-policy projection capabilities.

Provider and ACP implementations directly emit identity-free
`agentstream.ProducerEmission`: one typed turn payload, explicit terminal or
session payload plus native provenance. Session passes turn payloads and
terminals to the captured exact Turn; Timeline alone assigns canonical
identity/order. Task subscribes directly to typed Timeline events;
CLI/MCP/web/Feishu use Follow/Tail and `agentui` maps canonical events. Session
derives metadata, permission, activity and visible conversation views from the
already accepted canonical value, without constructing a reverse compatibility
event or a second live stream.

The Timeline does not own Session authorization, permission policy, watchdog
configuration, proc control, Task status, presentation policy or visible text replay
storage. Those concerns stay in their existing modules. It is the durable
source of structured Agent history, not the source of every Session behavior.

The external seam is a concrete Timeline with a small interface centered on
starting a turn, publishing session-scoped state and reading by cursor. Durable
storage is behind the narrow internal `Journal` seam; Session composes the
concrete Timeline with the runtime SQLite store. We do not introduce a global
event bus or make consumers depend on the database schema.

### 2. amux owns Turn identity and dispatch correlation

The queue and Timeline assign the canonical TurnID and TurnSeq. Provider
message IDs, thread IDs, native turn IDs and native sequence numbers are
provenance only; they cannot replace or overwrite amux identity.

Starting a turn is one operation: Timeline creates the Turn and
emits `TurnStarted`; Session installs a producer capability bound to that Turn and
then dispatches with an opaque amux token. In-process drivers return typed
emissions through the callback. Both ACP and Structured broker v3 use a versioned
envelope and return the opaque token plus a broker-assigned, monotonically
increasing per-turn sequence with every correlated emission. Session maps each
typed emission to the captured Turn's Emit or Finish operation. The broker
sequence exists only for reconnect replay; it is not a provider sequence and
does not replace canonical `SessionID + Epoch + Seq` identity. A provider-native
turn ID is insufficient because amux, not the provider, owns queue admission and
lifecycle identity. Replay-capable brokers advertise a versioned
`turn_replay_version`; for each token they assign the sequence and place the
immutable emission in the process-local replay buffer before attempting socket
delivery. The broker frame carries the immutable producer emission and never
carries canonical SessionID, TurnID, Cursor or timestamp.

Every in-process driver must provide this capability, which is valid only when its protocol is
strictly single-turn and its terminal is the final frame, or when it can route
multiplexed events by a native turn identity. The shared `EventSinks` helper does
not infer correlation for an ambiguous stream. Brokers must advertise and
successfully negotiate the correlated-turn protocol before the connection is
accepted. Older surviving brokers fail closed; they cannot drive the provider
through a session-wide prompt frame.

### 3. Dispatch failure is an observable terminal

Once `StartTurn` has created a canonical Turn, a driver dispatch failure must
finish that same Turn with:

```text
TurnFinished{outcome=error, code=submit_failed}
```

Closing an internal gate without emitting a terminal would create a half-turn:
the queue might move forward while Task, Attach or Feishu never observes why
the accepted submission ended.

If the per-turn capability accepts a producer terminal before dispatch
returns an error, the already accepted terminal wins. Timeline does not create
a second terminal.

### 4. `TurnFinished` is the only terminal

Canonical lifecycle rules are:

```text
TurnStarted is the first event of an accepted turn.
TurnFinished is the last accepted event of that turn.
ResultObserved carries result content and never closes the turn.
Diagnostic, provider error notification, tool failure and usage are not
terminal by themselves.
Provider completion, watchdog and interrupt paths must emit an explicit terminal
through the exact correlated sink; Session maps it to `Finish`.
The Session-owned process-exit path may `Finish` the exact Turn directly.
```

The first accepted terminal is authoritative. Repeating the same terminal is
idempotent and returns the original event without allocating another sequence.
A conflicting second outcome returns `ErrTerminalConflict` and preserves the
first terminal; the current Timeline does not automatically append a
diagnostic. Any later emission on the finished Turn is rejected rather than
being rebound to a new Turn.

The stable canonical outcomes are `success`, `error`, `cancelled` and
`interrupted`. Provider-specific reasons are normalized into stable amux codes
and diagnostic text at the producer protocol boundary. Task directly maps the
canonical terminal to `TerminalOutcome`; it does not inspect provider stop
reasons or native provider DTOs.

### 5. Session performs a synchronous completion barrier

Timeline acceptance of `TurnFinished` completes the Turn's observable event
lifecycle. It does not by itself make Session ready to dispatch another input.

Before queue advancement, Session synchronously runs a completion barrier for
the exact Turn identity. In MR1-3 the barrier clears the canonical turn's
authorization and pending permission state. Only after that cleanup completes
may the queue start the next item. Provider watchdogs stop and clear their own
state on the producer terminal path; Session metadata persistence remains on
its existing path and is not claimed as part of this barrier.

This barrier is not an asynchronous Timeline subscriber. Slow or failed
Attach, Task or Feishu consumers cannot prevent lifecycle cleanup, and an old
Turn completion cannot clean state belonging to a new Turn. Interrupt may
still revoke authorization immediately when accepted, before the provider
reports terminal, because that is a safety policy owned by Session.

### 6. Persist the Timeline before delivery; preserve derived conversation replay

Each Session has one immutable Timeline epoch, created with the Session and
preserved across amuxlet restart. SQLite stores the closed canonical event
union in strict sequence order. A canonical event is appended and committed
before it enters in-memory history or wakes subscribers. If append fails, the
Timeline faults, does not consume the sequence, rejects later writes, closes
subscribers and asks Session to stop the producer. amux must never continue
with an unrecorded suffix.

The cursor is opaque at transport boundaries and identifies one exact
`SessionID + Epoch + Seq`. Reads have three explicit meanings:

- **History** returns committed events after the cursor and reaches EOF.
- **Follow** returns committed history after the cursor, crosses one no-gap
  caught-up boundary, then waits for future committed events.
- **Tail** ignores history present when the read starts and waits only for
  future committed events.

Attach requires one of these explicit modes and exposes a caught-up watermark;
the old `replay` field is removed and its wire number reserved. History is a read-only delivery
operation: replaying old canonical events must never redispatch provider input,
answer permissions, invoke tools or repeat any other side effect.

On runtime recovery, Timeline validates the complete journal before exposing
the Session, restores exact event/turn sequence and sticky metadata, and keeps
an unfinished Turn as the active Turn. If its producer process is not alive,
Session appends one `TurnFinished{outcome=interrupted, code=runtime_restart}`;
it never redispatches the old input. Unknown event versions/kinds, envelope
mismatches, gaps or invalid turn lifecycle fail closed.

If a broker survives the runtime restart and advertises sequenced replay, it
keeps disconnect-window emissions in a process-local buffer keyed by opaque turn
token and broker sequence. Losing the delivery connection clears only its
current owner, not this buffer. The recovered runtime derives `after_sequence`
from the greatest broker sequence carried by a committed canonical event for
that active Turn in the durable Timeline journal, then calls
`rebind(turn_token, after_sequence)`. The broker replays buffered emissions with
`sequence > after_sequence` in order and crosses to live delivery without
sending another dispatch. A producer frame that was intentionally ignored
and therefore has no canonical journal event may be replayed and ignored again;
replay itself must not execute provider or tool side effects. This process-local
replay seam is not a second durable log and does not define a per-frame durable
ACK protocol. The first version retains the active or most recently completed
Turn buffer until a different Turn dispatch supersedes it; it does not silently
evict a reconnect suffix.

If an older surviving broker does not advertise replay capability, recovery
fails closed rather than rebind with a possible gap or redispatch the input.

Retention follows Session ownership in the first durable version: pruning or
deleting a Session atomically deletes its canonical Timeline and derived
conversation messages. There is no independent event TTL, compaction or cross-Session
global ordering. The runtime database is owner-only (`0600`). Only the stable,
policy-filtered canonical union is durable; raw provider protocol frames,
`Tool.RawOutput`, raw permission details and permission `ToolCall.RawInput` are excluded.

Session metadata/permission bookkeeping, activity and visible text handoff are
bounded synchronous projections of the already accepted `agentstream.Event`.
They do not construct another Agent event value, expose a live subscriber or
become another source of terminal truth.

### 7. Keep `TaskEvent` independent

`agentstream.Event` describes one Session's Agent turn. `TaskEvent` describes
one TaskRun's durable control-plane history. They have different identity,
retention, redaction and delivery requirements and remain separate models.

Task keeps its append-only event log, TaskRun snapshot, redaction,
output coalescing, task deadline and cursor semantics. Timeline does not replace
those responsibilities. Task now consumes `agentstream` directly and maps the
canonical Terminal without provider stop-reason helpers. Producer values remain
an adapter seam, but the legacy v1 lifecycle and unspecified Attach path are removed.

## Initial Implementation Sequence

The accepted first delivery is deliberately limited to:

1. **MR1 — canonical contract and shadow parity.** Add typed Timeline events,
   validation and tests while legacy lifecycle remains authoritative.
2. **MR2 — per-turn correlation.** Bind a Session emission capability to the
   exact Turn for in-process drivers, and use versioned opaque-token envelopes
   with per-turn broker sequence for both ACP and Structured brokers; keep
   authority in the legacy path until correlation is observable and stable.
3. **MR3 — lifecycle authority and completion barrier.** Let correlated
   Sessions use Timeline for terminal truth and queue advancement, retaining
   canonical-to-legacy projections for existing consumers and replay.
4. **Durable Session Timeline.** Append the closed canonical union to the
   Session SQLite journal before delivery; restore it on startup; add opaque
   cursor reads with History, Follow and Tail semantics while preserving the
   unspecified legacy Attach mode.
5. **First-party consumer migration.** Task subscribes directly to typed events;
   CLI/MCP/web use Follow, Feishu uses Tail, and `agentui` consumes canonical
   events. The unspecified Attach mode remains an isolated compatibility seam.
6. **Legacy lifecycle cleanup.** Make correlated `DispatchTurn` mandatory,
   reject old brokers and unspecified Attach requests, then delete the
   session-wide live subscriber, structured replay cache, legacy turn gate and
   terminal inference.
7. **Direct typed producer emission.** Make provider/ACP Proc APIs and broker
   v3 carry identity-free typed payloads and explicit terminals; project
   accepted canonical events directly to Session policy and delete the shared
   producer/session compatibility adapter.

The following remain explicitly outside this implementation:

- deleting Claude sidecar native JSON DTO types before the sidecar protocol is
  versioned directly around canonical semantics;
- deleting the small non-Agent `session.Event` value still used only to append
  local system notices to visible replay;
- replacing derived conversation replay/handoff with event sourcing;
- replacing Session or TaskRun control-plane snapshots with event sourcing;
- persisting raw provider frames or opaque tool/permission dumps;
- merging TaskEvent or presentation policy into Timeline;
- introducing Kafka, a standalone event service or full Session event sourcing.

## Alternatives Considered

### Shared event value object only

Moving `provider.Event` to a neutral package and making it a typed union is a
useful migration step but a shallow final module. Turn assignment, terminal
inference, queue gates and duplicate completion remain spread across callers.
The deletion test fails: removing that module would remove types, but little
behavior would reappear elsewhere because the behavior never moved behind its
interface.

### Full durable event-sourced Session

Writing every event to SQLite before applying Session, Attach, Task and Feishu
projectors can offer strong replay and cursor semantics, but reconstructing all
Session state from that log would make processes, sockets, queue admission,
authorization leases and Task control state depend on replay. The accepted
design persists only the canonical Agent Timeline and keeps Session/Task
snapshots authoritative. Full Session event sourcing remains rejected.

### Keep provider/session terminal inference and improve tests

More tests around each translation would reduce individual regressions but
would preserve multiple sources of truth. Every new provider or surface would
still need to understand which combinations of error, state and stop reason
are terminal. This option does not address the architectural cause.

## Consequences

Positive consequences:

- queue progress depends on one explicit typed terminal instead of a
  distributed inference convention;
- an old turn's delayed emission cannot contaminate the next turn;
- dispatch failure, watchdog, process exit and interrupt share one observable
  completion model;
- result content and lifecycle completion can evolve independently;
- provider-specific changes remain behind the producer protocol seam;
- Session completion cleanup becomes deterministic and testable at one seam;
- committed history can be reviewed or resumed by opaque cursor across runtime
  restart without replaying provider side effects;
- append-before-notify prevents consumers from observing an event that was not
  recorded;
- future Task, Attach and presentation consumers can delete repeated mapping
  and terminal branches incrementally.

Costs and risks:

- per-turn correlation changes broker and driver ordering and is the highest
  migration risk;
- queue advancement must not move to Timeline before the completion barrier is
  correct for canonical authorization and pending-permission cleanup; producer
  watchdog cleanup must remain ordered before its terminal emission;
- canonical history is loaded into memory per Session in this first version;
  very long Sessions may later need bounded paging or compaction without
  changing the cursor/read contract;
- append-before-notify adds one SQLite write per canonical event;
- native provider/ACP decoders remain protocol-specific, but their output seam
  is the shared typed producer emission contract;
- an old broker surviving deployment is rejected and its running Session may
  be ended or archived instead of being migrated in place.

## Verification Invariants

Tests for the initial delivery must prove through the Timeline/Session seam:

- amux, never a provider ID, assigns canonical Turn identity;
- dispatch failure produces exactly one observable `submit_failed` terminal;
- `ResultObserved` precedes terminal and does not close the gate;
- retryable diagnostics and tool failure do not implicitly finish a Turn;
- duplicate same-outcome finish is idempotent and conflicting finish is
  rejected;
- late events on an old Turn are rejected and never assigned to the next Turn;
- producer watchdog/interrupt terminal and Session process exit converge on one
  `TurnFinished` through their respective adapter/direct paths;
- the Session completion barrier for Turn N finishes before Turn N+1 dispatch;
- slow live consumers cannot block provider progress or lifecycle cleanup;
- append happens before notification; an append failure faults the Timeline,
  preserves the last committed sequence and stops further producer progress;
- History reaches EOF, Follow crosses history to live without a gap, and Tail
  observes only events committed after subscription;
- a durable cursor survives runtime restart for the same Session epoch, while
  a malformed, future or wrong-epoch cursor is rejected;
- recovery validates the complete lifecycle and does not redispatch an
  unfinished turn; a replay-capable surviving broker resumes from the greatest
  broker sequence present in committed canonical history without dispatch, an
  older broker without replay capability fails closed, and a dead producer gets
  one `runtime_restart` terminal;
- pruning a Session atomically removes its Timeline, and raw provider dumps are
  absent from durable JSON;
- unspecified Attach mode and old broker prompt frames fail closed;
- TaskEvent remains independently durable, ordered, redacted and replayable.

The structural completion criterion is deletion, not the existence of a new
package. Session terminal inference, broker terminal reconstruction and
consumer-specific provider stop-reason branches and the producer/session
compatibility adapter have been removed from the turn lifecycle. Native wire
DTOs remain local to their protocol decoders and are not lifecycle authorities
or consumer streams.
