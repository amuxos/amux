# Distributed Task Model

[English](distributed-task-model.md) | [简体中文](distributed-task-model.zh-CN.md)

## Task, TaskRun and Agent Instance

“Task” in this API means a bounded execution admitted by the Hub. Its concrete
record is a TaskRun with a task ID, child Session/Turn correlation, status and
result. Ordinary Feishu conversation submissions are Session Turns; they need not
create TaskRuns. The UI word “task” alone does not imply a record in Task history.

An ephemeral Task uses a child execution for that assignment. A persistent Agent
Instance can own successive TaskRuns while retaining a stable `agent_instance_id`,
sticky Runtime, workspace/home references and provider resume handle. Reusing a
Profile reference alone creates no instance reuse. Instance status (`active`,
`idle`, `archived`), Session state and TaskRun status are separate dimensions.
Terminal TaskRun outcomes are `succeeded`, `failed`, `cancelled`, `timed_out`.

`instance_followup` submits work and triggers execution; `instance_send` only
queues mailbox input, delivered on a later wake. Interrupting the instance
cancels its current owner TaskRun while retaining the instance. Tasks receive
explicit inputs and authorized resources, not the parent's full chat history
by default. Parent/child identity records preserve provenance without merging
contexts. See the [Session model](current/session-product-model.md).

Follow-up work with omitted launch identity restores the instance's saved profile,
engine and model before resolving defaults. It does not select a new distribution
default; explicit identity changes remain subject to the instance's no-switch contract.

## Scheduling and execution

The hub authenticates requests, freezes execution identity, selects an eligible
runtime and reserves capacity before dispatch. The selected runtime's task runner
executes a child session locally, enforcing ownership in process. The hub routes
requests and maintains task-to-target truth; it does not drive child sessions
directly. Terminal tasks release reservations through reconciliation, and
uncommitted reservations additionally expire by TTL as a backstop.

A task is a bounded execution with normalized terminal outcome, structured result,
event log and changed-file evidence. An agent instance is persistent and accepts
follow-up work. Cancellation and interruption preserve correlation with the
running execution. Permission decisions are explicit and tied to the requesting
task or instance. Instance interruption enters cancellation through the child
owner TaskRun: the runner records cancellation intent, finishes that run as
`cancelled`, and preserves the resumable instance. It must not depend on a direct
child interrupt producing a provider terminal event. MCP tools expose profiles, tasks and persistent instances using
the generated tool schemas. Task submission, instance creation and scheduled
Task definitions accept `extra_args` for native engine settings, independently
of `permission_policy`. Schedules preserve the ordered argument array verbatim
and expose the saved runtime, model and native arguments on MCP readback.

Credential profiles identify launch credentials. A profile declares which
environment variables it needs; only those variables are materialized for the
effective principal. Credentials must not be persisted into session snapshots,
result payloads or public event text. Explicit execution overrides are validated
against profile policy and runtime capabilities.

Task execution choices belong to amux, not v2 Profile content. An explicit runtime
and model take precedence. Without a runtime, a builtin shortcut uses its own
launch preset; other profiles use the distribution's default builtin launch
preset. An omitted model uses that preset's model when its runtime is inherited,
or the selected engine's native default when no model is configured. These
defaults do not replace expert content, permission policy or native arguments.
Runtime model suggestions are not an allowlist in the public distribution: an
unlisted native model name passes unchanged to the selected agent. The name does
not infer a provider, endpoint or credential. Adapter validation still applies.

Isolation is a launch policy and enforcement choice, not a UI label. Runtime
admission validates platform support and workspace access before launch. Strong
container/VM isolation and cross-machine high availability remain target
specifications, not capabilities implied by the distributed API.

Profile consumption supports manifest v2 only and follows the one-way dependency from
profile materialization to task/adapter contracts. Optional local expert authoring
requires an explicitly configured compatible authoring runtime and fails closed
when unavailable; native builtin profiles continue to run.

Canonical `ToolChanged` events also project into durable task `tool` events.
They contain only redacted tool ID, name, title and status; raw input, content,
diff, locations and output remain on the Agent Timeline. Tool activity neither
changes TaskResult nor decides task completion.

Canonical `UsageObserved` and `ResultObserved.Usage` also project into durable
task `usage` events with a JSON object named `usage`. Only known nonnegative
integer token, context and speed metrics are retained. Equal snapshots are
deduplicated, events are filtered by the exact TurnID, and the final snapshot is
published before terminal status. `TaskResult.usage_json` retains the final usage.
Cumulative fields belong to the child provider thread; a persistent instance that
reuses a thread may include consumption from earlier TaskRuns. They are not
exclusive to the current TaskRun, and parent and child usage is not added together.

Coordination delegations initiated by `amux-session:<session_id>` preserve that
parent session through existing Task admission and authorization, which resolves
the active ParentTurnID. Other initiators do not infer a parent. Coordination
wait advances its TaskEvent cursor over `tool` and `usage` events without
manufacturing status or text events or waking the parent Agent for pure tool or
usage updates.

Child Sessions use the same [Hub memory model](current/agent-memory-product-model.md)
as interactive Sessions. A fresh child resolves memory with its effective launch
credential and stores a scope precondition independently of its Profile. With
Hub user isolation enabled, a service execution uses the service Principal's
memory; otherwise all Runtimes share global memory. Existing child threads keep
their binding on resume, and a persistent instance cannot change its effective
Principal. Task children use the memory CLI without adding recursive task MCP
tools. Failed capability discovery emits a warning and leaves that conversation
without amux memory; it never selects a global fallback.

## Schedule and permission boundaries

Creating or updating a scheduled Task validates omitted execution choices using
the same defaults as Task submission. Definitions retain the caller's Profile
reference, runtime, model and native arguments; content and omitted defaults are
resolved at each trigger, including for existing schedules. Once a Task is
admitted, its resolved execution snapshot is frozen for retries.

Hub schedules either deliver a prompt to a Session or submit an ephemeral child
Task. MCP `schedule_create` defaults to the child-Task path (`schedule_type=subtask`
or omitted); its history records Task IDs, not the completed result or a promise to
send it to the originating Feishu chat. `ok:task:<id>` means submission succeeded;
final outcome comes from the Task record. With `schedule_type=session-prompt`, the
rule targets an explicit `session` session-ref (or falls back to `AMUX_SESSION_ID`)
resolved like CLI `amux schedule create <session-ref>`, then Submit-s into that
Session on fire (`Scheduler.deliverReuseSession`), which can wake the Agent;
inspect Schedule history statuses such as `ok` or `skipped:offline`.
Recurring rules advance after each attempted fire, including delivery failure;
consumed one-shot rules are not retried. Disabling or deleting a Schedule does
not cancel an already admitted Task. See `internal/schedule/spec.go`,
`internal/schedule/scheduler.go` and `app/cli/mcp_schedule.go`.

The default Task permission policy is `policy-allow`: the resolver automatically
handles requests within its rules. `deny-all` rejects requests;
`escalate-to-main` exposes them for an explicit response. Escalation is unavailable
for blocking `task_run` and scheduled subtasks. Permission resolution is separate
from workspace/OS isolation and credential authorization. An instance wait timeout
also does not cancel or time out its Task; inspect that Task for the final result.
