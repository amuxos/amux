# Distributed Task Model

[English](distributed-task-model.md) | [简体中文](distributed-task-model.zh-CN.md)

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
the generated tool schemas.

Credential profiles identify launch credentials. A profile declares which
environment variables it needs; only those variables are materialized for the
effective principal. Credentials must not be persisted into session snapshots,
result payloads or public event text. Explicit execution overrides are validated
against profile policy and runtime capabilities.

Without an explicit runtime override, model selection follows the profile's
`supported_models` policy and declared provider bindings. With an explicit runtime
override, runtime model suggestions are not an allowlist: an unlisted native
model name passes unchanged to the selected public agent. The name does not infer
a provider, endpoint or credential. Adapter reasoning-effort validation still
applies. Profile provider selection requires omitting the runtime override.

Isolation is a launch policy and enforcement choice, not a UI label. Runtime
admission validates platform support and workspace access before launch. Strong
container/VM isolation and cross-machine high availability remain target
specifications, not capabilities implied by the distributed API.

Profile consumption uses manifest v1.1 and follows the one-way dependency from
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
