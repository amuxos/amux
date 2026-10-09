# Feishu Bridge Product Model

[English](feishu-bridge-product-model.md) | [简体中文](feishu-bridge-product-model.zh-CN.md)

## Product entry and conversation contract

Feishu is the primary user interaction entry for the product: private requests,
group mentions, topic follow-ups, management cards and permission responses. CLI
and MCP remain alternative control and integration surfaces; their availability
does not make a terminal a prerequisite for an ordinary Feishu user.

A Feishu message can open management, invoke a control action, record human
context, or enqueue an Agent Turn. It does not always create a TaskRun. Bot-wide
singleton, per-topic bindings and experimental group singleton bindings have
different reuse scopes. The [Session model](session-product-model.md) defines
those identities and boundaries; [Task](../distributed-task-model.md) defines
delegation and persistent instances.

Group shared access, message handling (`thread|singleton`) and required Linux
user isolation are independent settings. Shared topic discussion without a bot
mention can become bounded context without starting another response. Explicit
commands still follow scope, Agent capability and binding-control checks.

The [Feishu command reference](../reference/feishu-commands.md) is generated from
the command/help registry and its bilingual management catalogs. An advertised
command is not an authorization grant. Natural-language requests for delegation
or schedules require Agent tools and must be verified by their actual records;
`/schedule` and `/sched` are unsupported topic commands.

## Bridge responsibilities

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

The owner-only Experiments page contains separate switches for long-conversation
completion notifications, per-group settings, and Agent memory. All three are
off by default. Agent memory uses the installation-wide `memory.enabled` key.
It works without an Agent Profile; memory is global across Runtimes unless user
isolation selects a separate store for each effective Principal. The page shows saved values and a
pending-restart indicator for each experiment whose running value differs.
Saving validates the whole form and updates submitted experiments atomically
while preserving unrelated configuration. Older cards that omit the memory or
per-group settings switch retain its latest saved value, resolved under the same
file lock. Saving does not change running behavior: the owner confirms a separate
system restart, and cancelling retains the saved settings.
After enabling and restarting, new conversations can use memory; old unmanaged
conversations do not adopt it. Disabling retains memory files and cannot erase
memory already present in a conversation's context. Card titles do not add a
separate memory usage counter.

Accepted messages map to a workspace and session. Session input enters the queue;
thread context is bounded and attached to the accepted submission. Agent progress,
tool activity, results and permission prompts are rendered from structured events.
The bridge does not infer provider semantics from display text.

A scheduled prompt submitted to a bound Session enters its normal input queue
and starts an Agent Turn when dispatched. The canonical `scheduler` input source
is preserved and permits the same progress, permission, error and final-result
presentation as other accepted input sources. Echoing the prompt is only one
part of that presentation. Provider startup output without an accepted input
remains suppressed. A schedule's `ok` status confirms successful submission;
the Turn's terminal event and result establish its execution outcome.

Each bound session launch has one live output stream. Concurrent binding,
input routing and session-added notifications reuse that stream, including its
readiness, turn state and card handles. A duplicate startup notification must
not freeze an existing progress card or put the same turn's result on a new
card. Session or launch identity changes, binding transfers and ended streams
still establish a new attachment. A newly created session may acquire its
identity from the first Hub snapshot without replacing its live stream.

Agent reply cards keep status, agent/model identity, timing and usage in the
collapsible execution summary, without a separate root title bar. This layout
also applies during streaming, after completion and in compact fallback rendering.
Model names use readable catalog labels instead of encoded service references.
Main and child card titles use the same structured usage summary. The latest
invocation's total is displayed as `22.8k tokens (8%)` when a context window is
known; otherwise the percentage is omitted. Native provider-thread cumulative
input is displayed as `累计输入 300k/343k（缓存/总量）` when cached input is reported,
or `累计输入 343k` without a cached-input value. Cumulative output is displayed as
`累计输出 12k`; both use tokens. Counts adapt their magnitude unit so a long-running
total stays readable: `k` below one million, `M` below one billion, then `B`
(`128.4M`, `12.3B`). The scaled value keeps one decimal digit and drops a trailing
`.0`, and a magnitude is never rounded up a step, so `999999` renders as `999.9k`
rather than `1M`. Explicit zero values are retained. Missing
cumulative fields are omitted, never inferred from the latest invocation or
summed across updates. Context occupancy still uses the latest invocation,
independently of cumulative consumption. Optional first-token latency and token
speed remain part of the summary. CardKit and legacy cards share these rules;
usage updates do not trigger a card refresh for every frame.

Group watch accepts permitted root messages containing text or valid images,
files or merged forwards. Material-only messages use the shared parser's
description as session input. Deleted messages and messages without valid text
or materials do not start a session. Existing sender, group, deduplication,
capacity and loop-prevention rules still apply. Group watch is automation
authorized by the Bot owner: message bindings, audit records and context retain
the real sender, while Session execution identity and actor credentials use the
confirmed Bot owner so principal-scoped model-service login matches the
configuring operator. Without a configured owner, group watch refuses to start
the task rather than falling back to the alert sender's identity.

Each watched group has an owner-only settings entry in the private group-watch
list and its group settings card. The editor saves an independent, versioned
`group_watch.chat_settings.<chat_id>` record: enabled state, inherited/custom
execution, expert, Runtime, model, reasoning effort, activity presentation,
active-session limit (1–500), and idle timeout (60–604800 seconds). A save affects
only that Bot and group. Existing groups without a record retain the previous
Bot-wide watch defaults. Inherited execution uses the target group's ordinary
conversation defaults; a custom tuple replaces only execution selection.
Global pause remains a master switch. A disabled group is excluded from both
live intake and history polling. Execution and idle-timeout changes apply to
new Sessions; capacity checks and activity presentation use the group's current
policy. Saving validates the complete draft atomically, and rejects stale cards.
These watch settings do not require the separate group-conversation experiment;
that experiment's existing admission and isolation rules still apply.

The group-watch `sender_types` list is the default sender allowlist for watched
groups in the effective configuration. `chat_sender_types.<chat_id>` may
override that allowlist for one watched group, so one Bot profile can accept
App-authored alerts in one group and user-authored messages in another. The
override does not enroll a group by itself; the chat must still be present in
`chat_ids`. Groups without an override continue to use `sender_types`.

Optional alert deduplication applies only to app-authored group-watch root
messages and runs before Session creation or any reply card is sent. It is
disabled by default. When enabled, the Bridge derives a stable fingerprint from
the sending app and normalized visible card text, preferring a Rule ID plus the
card title when present, and suppresses another matching alert in the same chat
for the configured window measured from confirmed Session creation. Suppressed
duplicates do not extend the window. Atomic durable reservations prevent competing
roots from starting together. Capacity/policy rejection releases a reservation;
a failed Run is released only after the Runtime confirms that the named Session
is absent. An unavailable Runtime leaves the outcome pending. After at least one
minute, a later matching alert reconciles that reservation with the Runtime before
retrying. Only running, idle or sleeping Sessions are considered started; a
`created` Session remains unresolved. A recovered Session with a durable input
receipt suppresses the duplicate; otherwise the Bridge reuses its original topic
and Session to submit the new matching alert instead of creating another Session.
The window is committed after successful input submission, using the confirmed
creation time (or recovery confirmation time). Before dispatching Submit, the
Bridge persists that dispatch may have happened. If its response/receipt is lost,
matching alerts are suppressed for the configured window from dispatch, without
automatically replaying the uncertain submission. After that window, a new alert
may start normally. An archived/failed Session without a dispatched input releases
the launch reservation; it cannot block future alerts indefinitely. User-authored messages and different chats remain
independent. Message-ID deduplication still applies before semantic alert
deduplication. Event Bot Open IDs and history App IDs are resolved to the same
App ID using the complete group Bot catalog. Enabling alert deduplication requires
the chat Bot listing permission (`im:chat.members:read`); failed, incomplete, or
unmatched lookups return an error without consuming the message ID. Material-only
image, file, audio, video and forwarded-message placeholders are not fingerprints.

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

Agent-produced outbox artifacts follow Feishu IM upload limits independently of
inbound material limits. Each file may be at most 30 MB, with at most ten files
and 300 MB total per turn. Recognized images up to 10 MB use the image upload
API and retain image presentation; larger recognized images up to 30 MB use the
file upload API instead.

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

## Conversation controls and document comments

`/compact`, `/clear` and the native `/skill` picker currently require a
Codex-compatible Session. Compact/clear submit queued controls; interrupt routes
to the active Session instead of cancelling every related Task. The
[Session model](session-product-model.md) defines ordinary follow-up preemption
and text coalescing. See `router_control.go`, `router_submit.go`, `coalesce.go`
and `skill_picker.go` under `internal/bridge/` for these input paths.

Document-comment intake is independently enabled. It requires an actual bot
mention, a confirmed owner and owner access before reading document content or
starting execution. Group collaboration does not grant this capability to other
members. The binding is scoped to Bot, document and root comment; subsequent
mentions in that comment thread reuse it. Intake reads the concrete request and
bounded available document context, deduplicates events and replies in comments.
It does not itself edit the document body. Permission cards go to the initiating
owner's private chat, with a comment directing them there. Read/launch/submit
failures produce explicit failure handling; enabling the setting alone does not
prove platform access or a working subscription. Implementation: `internal/bridge/document_comment.go`.

## Group-scoped settings

Group-scoped settings are an installation-wide experiment, disabled by default
(`bridge.feishu.group_settings.enabled = false`). The owner enables it from the
private `/amux` experiments page using Save and restart. Saving changes only the
configuration file; the running Hub keeps its startup setting until restart.
When disabled, group editing, old card callbacks and local operator commands
are unavailable. Groups with saved independent settings pause new inputs and
automatic work without falling back to Bot defaults or weakening isolation.
Their settings, Session bindings and accepted output are retained; enabling the
experiment and restarting resumes their saved policy. Groups without saved
settings and private chats continue to use the existing Bot behavior.

Each `(bridge_id, chat_id)` may opt
into independent interaction, Profile, Runtime, model, reasoning effort, thinking
process presentation, target, cwd, arguments, idle timeout, context limit and
session quotas. The owner edits a complete draft seeded from effective Bot and
workspace settings; saves are atomic and version checked. Existing installations
retain Bot-wide behavior until a group saves its own settings. Group access mode
remains separate from thread/singleton interaction and execution isolation.

When the group editor uses a builtin expert's caller-selected runtime, model
choices come from that runtime catalog, including discovered native and model-service
choices. Its default option retains the caller preset model. Explicit runtime
selection uses the selected runtime catalog; legacy Profile model restrictions remain
in force when using a legacy Profile's own runtime.

New topic sessions consume the saved group execution tuple. An explicit reasoning effort
is validated and materialized even when Runtime uses the Profile default; the Profile
model/provider selection and its model policy remain in force. Explicit `/run`
selections and bound Sessions retain their own identity. Existing Sessions never
change Profile or execution settings merely because a default changed. Required
isolation cannot silently degrade or be bypassed by binding a legacy execution.

Linux isolation availability and a Feishu conversation's isolation choice are
separate. New ordinary topic and singleton Sessions explicitly select passthrough;
the isolated-topic choice selects linux-dac and fails closed when unavailable.
The effective group setting controls this choice, with Bot defaults used for
private chats and groups without independent settings. Enabling the host's
isolation capability must not silently isolate every Feishu conversation. Execution
principals and actor credentials remain identified when the host requires them;
selecting passthrough changes process isolation, not authorization. Existing or
explicitly bound Sessions retain their frozen execution environment.

Group singleton uses one Session per Bot and group. Replies, progress, permission
requests and artifacts are sent directly to that group, like Bot singleton output.
New inputs and explicitly addressed replies to its output messages enter the same queue. The first input
remains the internal binding anchor, not a Feishu thread output destination. It does
not share a Session with other groups or private chats. Mode changes and the owner’s new-conversation action
retire the previous default binding without interrupting accepted work. Replies
to retired group-singleton entries receive a new-topic notice. Ordinary existing
thread bindings remain usable.

When principal isolation is enabled, group singleton and Bot singleton require
the Bot-scoped `service:<bot-profile>` principal alias and active sender service
membership. Personal topic enrollment does not provision this shared execution
identity, and a group's `shared` access mode does not grant service membership.
Operators must provision these prerequisites when enabling isolation; see the
[Linux isolation guide](../operations/linux-dac-isolation.md#飞书连续会话的共享身份).

Group watch eligibility and inherited execution settings use the target group’s
policy. Explicit capability execution overrides still apply within the group’s
isolation requirement. Full design and transition cases live in
[the implementation specification](../superpowers/specs/2026-09-16-feishu-group-session-defaults-design.md).

## Model service selection

Distribution model services appear in the model picker for supported engines,
with signed-in, signed-out, pending or expired state for the acting principal.
Missing optional service dependencies appear as setup required, with guidance
from the distribution and a recheck button. Services that declare installation
support also show an explicit install button. Viewing, selecting or rechecking
a service never installs software. Only the install action starts dependency
setup on the Hub, with progress polled in the existing card. Completion returns
to the login controls; failures offer retry without changing the editor draft.
Selecting a signed-out service starts its SDK login; a valid login is reused.
The card carries only a login URL, never credentials or challenge tokens.
After login, the picker includes all enabled upstream model/configuration pairs
and their declared reasoning options. Login does not save or switch an engine.
Changing defaults still affects only new sessions. Another sender or a shared
execution principal never silently inherits the editor's personal credentials.
Login controls appear beside the model picker and reflect the current login
state. Selecting a service or refreshing login preserves the unsaved model and
reasoning selection. Signed-in service models appear together immediately after
their service entry, with concise model/configuration labels. Existing saved
model references remain usable.

Switching the runtime refreshes the settings draft, including optional model
service controls. Each service action has a distinct card element name, so
install and recheck controls or multiple services can coexist. Saving submits
the selected runtime even when an optional service still requires setup.

## Distribution default migrations

Distributions declare exact old-to-new profile reference mappings for retired
defaults. Bridge owns the affected tables, parameterized updates and transaction.
Mappings apply simultaneously, preserve unrelated settings and historical session
identities, and are idempotent; targets cannot also be migration sources.

## Management interface language

`/amux` product copy is owned by stable keys in a shared English/Simplified Chinese
catalog. Templates interpolate runtime values without translating user-provided
names, paths, identifiers, diagnostics, or Agent output. Catalog validation checks
key and argument parity and the shared formatter owns plural selection and fallback.

Management cards use JSON 2.0 **local** `i18n_content` fields and callback toasts
use `i18n`, following the viewer's Feishu/Lark language. JSON 2.0 does not support
global `i18n_elements`. English is the fallback for unsupported client languages.
The `/amux` overview offers automatic, English and Simplified Chinese display.
The preference is persisted per Bot/user. Explicit selection applies to
server-recorded private management cards and private conversation cards, including
per-topic DMs and the continuous Bot session. Shared and pre-upgrade cards retain
per-viewer automatic display; reopen `/amux` to apply a preference to a new card.
The preference does not change Agent reply language. If a permission card fails
to send, its plain-text fallback uses the saved language; automatic mode includes
both languages because plain messages cannot select language per viewer.
Initial delivery, in-place updates, confirmation dialogs, asynchronous results and
restart recovery preserve the same language policy. Callback values and
execution authorization remain language-independent.

Sources: [Feishu card localization](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/configure-multi-language-content)
and the SDK callback `Toast.I18nContent` contract.

The platform-independent formatter lives in `internal/i18n`; bridge-owned copy is
in `internal/bridge/locales/{en,zh-CN}.json`. Add semantic keys in both catalogs
and pass dynamic values as template arguments. Rendering accepts explicit product
messages or literal strings; it never looks up a translation using user content.
English count messages provide singular forms where needed. Tests compare rendered
languages, preserve callback payloads, check card budgets and exercise persisted
preferences, shared-card isolation and failed-update fallback. CLI and desktop
coverage is tracked separately in the [product language model](i18n-product-model.md).
Agent-generated content retains its original language.

Conversation and subtask cards also use native per-viewer translations for
status, timing and usage labels, question/follow-up headings, plans, execution
records, progress and final-result headings, viewer controls and size-limit
fallbacks. Private conversation cards use the recipient's saved preference, falling back
to the viewer's client language in automatic mode. Shared cards always retain
per-viewer translations. Each CardKit delivery snapshots its verified recipient
and preference so retries and subsequent updates cannot apply a private override
to a group. Preference changes apply to new cards; historical cards are retained. Product headings are separate
from the streamed Agent body, so CardKit text updates preserve both translations
and literal Agent content. Activity replacement and legacy activity cards carry
the same translations. Truncated progress uses a language-neutral ellipsis.
Already-delivered historical cards are not rewritten retroactively.

Codex editors refresh native model choices from the actual execution Runtime,
independently of optional model services. A refresh control retries discovery;
signed-out and unavailable states do not populate a static GPT fallback. Saved
model and effort selections survive refresh failures.
