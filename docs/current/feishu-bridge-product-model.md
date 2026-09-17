# Feishu Bridge Product Model

[English](feishu-bridge-product-model.md) | [简体中文](feishu-bridge-product-model.zh-CN.md)

The optional bridge owns Feishu/Lark API behavior, message routing, bot commands,
thread and workspace bindings, permissions and output presentation. It does not
own agent execution or change local CLI requirements.

Group-watch and document-comment execution overrides follow the selected
runtime's model policy. Without a runtime override, they follow the Profile's
model policy: omitted `supported_models` permits native model names, an explicit
list restricts choices to its entries, and an explicit empty list permits only
the agent default. CLI validation and Bridge admission apply the same rule.

An operator configures a bot profile using the official application registration
flow. Setup requests the scopes/events required by implemented capabilities.
Personal and team governance control access independently of system hosting
topology. Owner operations are distinct from ordinary conversation actions.

Accepted messages map to a workspace and session. Session input enters the queue;
thread context is bounded and attached to the accepted submission. Agent progress,
tool activity, results and permission prompts are rendered from structured events.
The bridge does not infer provider semantics from display text.

Each bound session launch has one live output stream. Concurrent binding,
input routing and session-added notifications reuse that stream, including its
readiness, turn state and card handles. A duplicate startup notification must
not freeze an existing progress card or put the same turn's result on a new
card. Session or launch identity changes, binding transfers and ended streams
still establish a new attachment. A newly created session may acquire its
identity from the first Hub snapshot without replacing its live stream.

Main and child card titles use the same structured usage summary. The latest
invocation's total is displayed as `22.8k tokens (8%)` when a context window is
known; otherwise the percentage is omitted. Native provider-thread cumulative
input is displayed as `累计输入 300k/343k（缓存/总量）` when cached input is reported,
or `累计输入 343k` without a cached-input value. Cumulative output is displayed as
`累计输出 12k`; both use tokens. Explicit zero values are retained. Missing
cumulative fields are omitted, never inferred from the latest invocation or
summed across updates. Context occupancy still uses the latest invocation,
independently of cumulative consumption. Optional first-token latency and token
speed remain part of the summary. CardKit and legacy cards share these rules;
usage updates do not trigger a card refresh for every frame.

Group watch accepts permitted root messages containing text or valid images,
files or merged forwards. Material-only messages use the shared parser's
description as session input. Deleted messages and messages without valid text
or materials do not start a session. Existing sender, group, deduplication,
capacity and loop-prevention rules still apply.

App-authored root alerts that mention only people remain eligible for group
watch. Mentions of the current bot use ordinary routing; mentions of other bots
exclude an App message from automatic handling, including mixed human/bot
mentions. Event and history inputs enforce the same distinction. Explicit
event target types are authoritative; when the platform omits the type (as in
history), Bridge resolves target IDs against a complete, current chat bot list.
This lookup uses `im:chat.members:read`, independently of whether Bot-to-Bot
collaboration is enabled. Each poll shares one lookup per chat. An unavailable
or incomplete list leaves ambiguous messages unsubmitted and reports the error;
they remain eligible for a later poll within the lookback window. Messages
without ambiguous mentions do not require the lookup.

Ordinary conversations, live group-watch events and history polling collect
materials through the same path. Each message has its own collection context;
attachments cannot carry over to another message in a polling batch. Material
retrieval uses the current Bridge application's identity and the owning chat,
with the same session capabilities, size limits, file materialization and error
notices in each entry path. Only descendants returned by a merged-forward
container are expanded. A descendant's original message ID may retrieve its
resources only when Feishu explicitly reports the same chat as the container;
otherwise retrieval keeps the top-level container ID.

Bridge state is grouped by responsibility into controllers, stores and API
ports. Routing and application wiring remain in Bridge; the ownership changes
preserve event ordering, stream lifecycle, permissions and distribution hooks.

New workspaces default to `builtin/codex`. The bot may select available profiles
and runtimes. Public branding links to the amux homepage at `https://amuxos.com/`.
The delegation protocol retains the stable identifier
`urn:amux:protocol:feishu-external-bot-delegation:v1`, independently of the homepage.
External bot discovery and new delegation require an active binding to the
current group; cross-group discovery is not allowed. Existing delegated work can
finish using its recorded peer identity.

The public distribution has no legacy console automation and does not read
another tool's web-session cache. Operators can use the official registration
flow or configure an application manually.

A separately maintained distribution may register one Bridge extension before
the Bridge is first used. It can supply provider presentation, coordination
profile recognition, a transactional migration of its legacy defaults, and an
explicit console setup fallback. Registration is immutable after first use and
duplicate registration is an error. These hooks do not change Session execution,
canonical events, access checks, or the default official registration flow.
Distribution-specific implementations and session-cache paths belong to the
distribution module; the public Bridge operates independently without them.

The parent activity card summarizes collaboration tasks without mixing their
output into the parent Agent process panel. Each newly discovered task gets one
child progress card per Bridge process, updated in place through its terminal
result. Its label is the short profile name plus the first eight characters of
the persistent agent instance ID, or the exact child session ID for an ordinary
task; TaskRun ID is a temporary fallback before child creation. Profile labels
drop the namespace and revision and fall back to Agent Type, then `agent`;
identity suffixes omit the `agent-` or `task-` prefix. Cards show status, profile,
elapsed time and the effective model when known. Identity and
effective model come from the exact child session on the same target, never
from the parent or output text, and remain visible after terminal cleanup.

Child cards aggregate only their own redacted structured tool activity by tool
ID and show TaskEvent output in a separate process panel. They may include the
child session's own read-only viewer link. CardKit creation failure falls back
to one legacy interactive card that subsequent updates patch in place.

Child usage comes only from that TaskRun's structured `usage` events and final
`TaskResult.usage_json`. Sequence-based deduplication preserves the latest usage
through replay and terminal cleanup. Each card shows its own child provider
thread's cumulative snapshot, which may include earlier TaskRuns when a
persistent child reuses the thread. Parent and other child usage is not included.

During a parent turn, discovery runs about every two seconds, plus one final
discovery when that turn ends. Each watcher replays TaskEvent from sequence zero
and resumes after disconnection from its last sequence without duplicating
events. Status, permission and result update the child card immediately; output
updates are coalesced over two seconds. Watchers survive the parent turn until
the child task terminates, its timeout window expires or the process exits.
Parent summaries retain their roughly thirty-second refresh cadence.
