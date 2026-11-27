# Session and conversation product model

[English](session-product-model.md) | [简体中文](session-product-model.zh-CN.md)

Status: current implementation. This model defines conversation identity,
Feishu bindings and input lifetime. It complements the [Agent contract](agent-adapter-product-model.md),
[Bridge contract](feishu-bridge-product-model.md) and [Task model](../distributed-task-model.md).

## Objects and identity

A Session is the Runtime-owned managed conversation, addressed by target and
Session identity. It retains launch settings, workspace, execution identity and
resumable provider history. An Agent Profile is its launch definition, not its
identity. The native provider's conversation ID is a resume handle, not the amux
Session ID. A restart can change the process without changing the Session.

Restart and automatic wake retain recorded execution settings. Missing profile or
engine identity is first recovered from the saved launch snapshot. If no profile
identity remains, recovery uses `builtin/default` when registered, otherwise the
distribution's default builtin profile. A missing engine uses the distribution's
default launch runtime. These fallbacks do not inherit shortcut permission bypass.
Saved model, reasoning, permission settings and workspace remain authoritative.
A deleted source Pack does not invalidate an existing execution snapshot; an
unavailable recorded engine is an error, never an automatic engine switch.
If no engine identity survives, native provider history cannot safely be resumed;
recovery starts a fresh provider conversation with the amux history handoff.

New Sessions default to native bypass unless native permission arguments were
explicitly supplied. This applies to CLI, MCP and Feishu launches, independently
of the expert. The resolved default is recorded for restart and resume; existing
Sessions retain their saved execution settings.

A Turn is one dispatched submission accepted by the Session queue. Several Feishu
messages may be coalesced into one submission; one message can also trigger a
control action or add context without starting a Turn. The canonical Timeline
owns TurnID/TurnSeq and exactly one TurnFinished. Result text alone is not a
completion signal. A Session can contain many Turns.

With the foreground follow-up experiment enabled, text-only inputs arriving during an active Turn are coalesced with adjacent inputs from the same sender and source for the same target Turn. The window starts at 400 milliseconds and can extend to two seconds. The resulting input is either steered or queued:

- **Steered** when its sender is the one person who sent the running Turn's input.
  The input is pinned to that TurnID and added to the running Turn; it does not
  create a new Turn, and the Turn still ends with exactly one TurnFinished, after
  the provider has handled its last steered input.
- **Queued** as ordinary input for the next Turn in every other case: input from
  another group member, group-watch alerts, Bot input or other input without a
  sender, no known running Turn, earlier input already waiting in the queue, a
  Turn that is being interrupted, differing Turn authorization, or a provider
  without steering. Queued follow-ups never preempt.
- **Not delivered** when the provider may already have taken the input but did not
  confirm it (a timeout or broken transport). The submission fails instead of
  queueing a second copy that could run twice.

Interrupting a Turn also cancels the input steered into it. Claude stops a steered
input that the CLI still has queued as soon as it starts running, and the next Turn
starts with a note naming the cancelled input. `AMUX_FEISHU_BUSY_INPUT=interrupt`
restores the previous contract: Bridge requests interruption immediately and
submits the coalesced input with preemption. Material-bearing inputs bypass text
coalescing. This is separate from queued `compact` / `clear` controls, which do
not preempt preceding input. Neither interruption nor a context reset rolls back
completed tool side effects.

A TaskRun is created through Task admission for a bounded child execution. An
ordinary Feishu request can drive a Session Turn without any TaskRun. The product
UI's use of “task” for ongoing work does not establish a TaskRun record. A TaskRun
records its child Session and exact accepted child Turn; an Agent Instance can
span multiple TaskRuns with distinct task IDs.

## Feishu binding scopes

| Binding | Reuse rule | Boundary |
| --- | --- | --- |
| Topic | Accepted input in an active bound topic reaches its Session | A new independent topic has a separate binding |
| Bot singleton | Eligible private/group input reuses the Bot Profile's bound Session | Not automatically per-chat or per-user |
| Group singleton | An opted-in group's eligible input reaches its current group Session | Scoped by Bot and group; distinct from the bot singleton |

Per-group settings are default-off. When enabled for a group they override bot
message-handling defaults. Existing ordinary topic bindings remain addressable;
an explicit launch/bind does not replace the group's current continuous conversation.
Retiring a group singleton stops new input to that conversation but lets accepted
work complete, including output recovery after a Bridge restart.

In shared active topics, ordinary unmentioned human discussion is recorded as
bounded context; an explicit mention triggers work. Owner-only admission and
binding-control authorization are independent checks. A visible card or topic
does not authorize all readers to drive its Session.

A Feishu queue receipt can offer the sender an explicit action to move their pending text into the running Turn. The receipt pins the Session, launch and destination Turn. Only the original sender can act, and the Runtime requires the exact queue head and matching execution authorization. Success consumes the pending input. A definite rejection keeps its queue position. An uncertain provider outcome consumes the entry and reports uncertainty to prevent duplicate execution. A completed or replaced destination is never retargeted.

## Lifecycle and user actions

Session state uses `created`, `running`, `idle`, `sleeping`, `archived`. Turn state
and TaskRun status must not be inferred from these labels. Process liveness alone
does not say whether a turn is awaiting a tool, permission or completion.

| Action | Meaning |
| --- | --- |
| Submit/reply | Queue new input for the existing conversation |
| Detach | Close a viewer; preserve Session and execution |
| Interrupt | Request interruption of the active work; preserve Session identity |
| Sleep/resume | Stop/relaunch the process with resumable state |
| Restart | Relaunch using the existing Session's settings and available history |
| Compact | Ask a supporting Agent to summarize its current context |
| Clear | Start fresh native context while retaining the Session binding, where supported |
| Stop a topic | Kill its Session and terminate the topic binding |
| New group continuous conversation | Retire the old input pointer; do not cancel already accepted work |

If the broker for a Feishu Session with saved launch settings disconnects
unexpectedly, the Runtime records the active Turn failure and puts the Session
to sleep. The Feishu binding stays usable, and new input wakes the same Session
with its available history. The interrupted input is not replayed. A stop
requested by the user still ends the binding. Task-owned and persistent Agent
Instance Sessions follow their own lifecycle.

Bot-wide singleton mode rejects topic-style run/bind/stop operations and directs
the user to management. Agent-specific controls may be unavailable. Clearing
context or changing a binding is not deletion of workspace files, stored Hub
memory or all historical records. An ended topic is not silently reused as a new
conversation. Default expert changes apply to new launches; existing topic
Sessions keep their selected expert. Singleton expert replacement follows its
confirmed management transition.

## Context boundaries

Native conversation history, Feishu context packages, workspaces, explicit Task
inputs and Hub memory are separate resources. They do not merge merely because
a bot, Profile or human user is shared. Topic context is bounded, not an export
of the whole chat. Children receive their explicit task inputs and allowed
resources, not automatic copies of the parent's complete conversation.

See [thread context](feishu-thread-context-model.md) for selection and truncation,
and [memory](agent-memory-product-model.md) for principal-derived scope.

## Implementation evidence

- [Event timeline design record](../maintainers/agent-event-timeline.md) preserves the historical rationale and alternatives behind Turn ownership and replay.
- [Session state and queue](../../internal/session/session.go), [Timeline](../../internal/agentstream/timeline.go).
- [Topic decisions](../../internal/bridge/inputrouter/router.go), [routing](../../internal/bridge/router.go), [bot singleton](../../internal/bridge/router_botsession.go), [group singleton](../../internal/bridge/router_group_session.go).
- [Control dispatch](../../internal/bridge/router_control.go), [command policies](../../internal/bridge/command/catalog.go).
- [TaskRun fields](../../internal/task/types.go), [persistent instance store](../../internal/agentinstance/store.go).

## Native goals

Source implementation; live Feishu end-to-end acceptance is pending.

Only a leading bare `goal` is a formal command; `/goal` is not an alias. The
commands are `goal <objective>` (create/start), `goal` (query), `goal pause`,
`goal resume`, `goal clear`, `goal set <objective>` (explicit replacement), and
`goal budget <positive tokens>`. An unfinished goal rejects implicit replacement.
No budget is invented. Native state, token usage and runtime seconds remain
authoritative. Resume preserves native accounting; changing an objective follows
the provider's accounting semantics. Clearing a goal preserves the conversation.

Capability is discovered on the live provider. Codex-compatible app servers are
probed with `thread/goal/get`; unsupported methods, disabled features and invalid
responses fail closed. Other registered structured providers opt in through
`GoalController` and `NativeGoalTurnProvider`, exported by `pkg/extensions`.
Claude SDK and Pi currently have no native goal adapter and expose no goal help
or actions. Agent names alone never establish support. No native `/goal` slash
menu is registered.

In topic mode, controls resolve the bound Session. Only creation can launch and
bind an unbound topic through normal admission. In bot singleton mode, the goal
belongs to the shared bot Session across chats. Output stays pinned to the
creation destination; querying elsewhere does not relocate it. Replacing the
goal establishes a new destination and invalidates earlier card buttons.
Creation/replacement accepts a text objective; send attachments and explicit
quoted material as separate ordinary follow-ups. Existing native conversation
history remains available; goal controls do not hydrate Feishu topic history.
Existing mention, ownership and group-access rules still apply.

Each native continuation needs an enabled durable grant matching its native
conversation and objective, and a single runtime owner. It receives a fresh
canonical Turn and ordinary terminal, transport replay and watchdog handling,
without local-expert authoring privileges. Pending user inputs take priority.
Only matching persisted destinations authorize Feishu goal output; unrelated
native turns retain quarantine. Startup notifications wait for owner handover.
Pause revokes admission before native cancellation; clear also removes grants
and destinations. Ordinary input preemption preserves the grant and can resume
an otherwise paused goal after the input queue drains. Explicit interrupts and
`goal pause` revoke it. Native blocked, usage-limited, budget-limited and complete
states do not auto-resume. A disconnected owner cannot admit new continuations;
if quarantine pauses the native goal, use `goal resume` after reconnecting.

Goal observations are sticky session-scoped canonical events and are restored
at Attach catch-up. State cards include native usage, budget and time, with
English and Simplified Chinese copy. Mutating card callbacks recheck permissions,
current Session identity, destination generation and native goal identity;
stale cards cannot control a replacement Session or goal.

Goal permission requests use the same frozen binding as the turn's replies. An
accepted ordinary submission cancels a pending goal admission offer atomically;
ordinary input in progress rejects goal offers without waiting for that input
to finish. TAIL Attach includes the active canonical turn's start snapshot from
the atomic read head. Bridge restores only a goal turn matching its persisted
destination and current binding, authorizing subsequent replies and permission
requests without replaying earlier output.

## Single-turn results

Single-turn result collection is enabled by default (`turn_results.enabled=true`) with
bounded retention and can be disabled with `turn_results.enabled=false`. When disabled,
read queries return `unavailable` (`reason=disabled`) and any previously cached snapshots
in Hub storage are cleared on startup; Runtime canonical timelines remain unaffected.

When enabled, Hub records read-only structured snapshots for turns and scheduled
executions started after enabling, separating three independent facts:

- **Execution** (`execution.status`): derived strictly from canonical `TurnFinished`,
  `TaskRun` terminal state, or schedule admission failure (`missed`), never inferred
  from answer text.
- **Output** (`output.status`, `output.content`): the final accepted Markdown answer
  without thinking or intermediate tool progress. Non-completed executions do not return
  partial answers; outputs exceeding `turn_results.max_content_bytes` (default 1 MiB)
  return `unavailable` (`reason=result_too_large`) without silent truncation.
- **Deliveries** (`deliveries[]`): confirmed external delivery receipts (for example,
  Feishu primary/continuation cards and `amux-artifact` attachments with confirmed
  `message_id` values, or `suppressed`/`partial`/`failed`/`unknown`). Turns without an
  external IM binding (such as CLI-only sessions, `new-session-per-run` schedules, or
  `subtask` schedules) settle with an empty `deliveries` list rather than waiting.

Snapshots are bounded by `turn_results.ttl_seconds` (default 7 days, `0` disables time
expiry), `turn_results.max_entries`, `turn_results.max_total_bytes`, and
`turn_results.pending_timeout_seconds` (unsettled records expire to
`unavailable`/`result_not_settled`). Reads never trigger model retries, resend IM
messages, or extend retention, and enforce the same Principal session and schedule
authorization boundaries as ordinary reads.
