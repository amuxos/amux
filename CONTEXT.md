# amux Context

[English](CONTEXT.md) | [简体中文](CONTEXT.zh-CN.md)

This glossary names product concepts in amux so architecture work uses stable language.

## Language

**Feishu user**:
A person who asks an amux bot to do work and consumes its progress or results in
Feishu. This is a usage role; the same person may also be the amux operator for
a self-hosted installation.
_Avoid_: Consumer

**amux operator**:
The role responsible for the health and lifecycle of an amux installation,
including starting, restarting, upgrading, and diagnosing its services. It may
be held by the same person as a Feishu user.
_Avoid_: Administrator persona

**Bot governance mode**:
The per-Feishu-Bot-Profile choice between `owner_only` (personal) and `shared`
(team). Owner_only limits that Bot's capabilities to its owner; shared allows
authorized members to use the capabilities that Bot exposes. Governance does not
choose system topology, workspace policy, or `thread|singleton` interaction mode.
_Avoid_: Deployment mode, edition, audience mode

**System hosting topology**:
The deployment fact that one amux system hosts either one dedicated Bot Profile
or multiple independently configured Bot Profiles. It is chosen for resource,
process, data, and failure-domain isolation, not inferred from Bot governance.
_Avoid_: Personal deployment, Team deployment

**amux system**:
The owner-facing health and lifecycle object for one complete amux
installation. It presents hub, runtime, bridge, and related processes as one
whole; individual components appear only in diagnostics.
_Avoid_: amux service, component

**Managed amux system**:
An amux system whose lifecycle is owned by the host operating system's native
service manager. Unsupported hosts run amux explicitly in the foreground
instead of silently selecting a second supervisor.
_Avoid_: PID daemon, fallback service

**System status card**:
The owner-only Feishu private-chat summary of amux system health, version,
current Bot interaction mode, Hub host IP, and the restart and update-check
actions. The host IP is system-level connection metadata and does not imply the
Hub listens on that interface;
per-component health, PID, logs, and diagnostics are not shown on this card. It
is not exposed in group chats or task threads.
_Avoid_: Component dashboard, session status

**Feishu input routing**:
The classification of a normalized Feishu user input into the amux action it should drive, such as workspace management, session entry, active task conversation, human context capture, or a no-op.
_Avoid_: Command routing, session routing

**Agent turn**:
The provider-independent unit created when amux accepts one queued submission
for dispatch. Its canonical representation has amux-owned TurnID and TurnSeq
and exactly one `TurnFinished`; provider message IDs are provenance, not turn
identities, and compatibility representations do not create another domain
turn. `ResultObserved` carries result content but does not complete a turn.

**Agent presentation action**:
A surface-neutral instruction derived from canonical Agent events, such as
append/replace/segment reply, update activity, request permission, or finish a
turn. Feishu renders these actions but does not interpret provider protocols.

**Execution authorization**:
The control-plane decision governing an actor's access to agent executions and
automation that can create or drive them. It applies across Sessions, TaskRuns,
AgentInstances, and Schedules without collapsing those distinct concepts.
_Avoid_: Object ownership check, RPC authorization
