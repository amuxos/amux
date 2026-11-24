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

A distribution can require personal credential admission for a new Feishu
Session when its conversation policy requires Linux user isolation. The Bridge
checks the selected Runtime using the resolved execution identity and the
actor identifiers provided by the incoming Feishu user event before starting
the Session. A pending admission replies to the original conversation
and retains the starting input in memory for a bounded time. Only the resolved
Feishu actor can operate its card, and any authorization URL is delivered in
that actor's exclusive card update. The callback acknowledges a click while
the Runtime works. Checking progress while a browser action is pending prompts
the current poll to run sooner. Credential tools run with temporary storage
inside the isolated execution's private run directory. Once verified, the Bridge
revalidates the original routing before continuing once. Existing Sessions and ordinary host
launches do not enter this flow. The public distribution has no credential
admission policy.

The [Feishu command reference](../reference/feishu-commands.md) is generated from
the command/help registry and its bilingual management catalogs. An advertised
command is not an authorization grant. Natural-language requests for delegation
or schedules require Agent tools and must be verified by their actual records;
`/schedule` and `/sched` are unsupported topic commands.

New Feishu topic and document-comment Sessions inherit the selected Runtime’s Feishu sleep policy when no workspace timeout was configured. An explicit zero disables idle sleep. Existing workspace and Session records keep their saved timeout. Group watch and Bot singleton Sessions keep their dedicated timeout policies.

## Bridge responsibilities

Agents can use `amux.feishu_send_message` to send plain text or Markdown through
their Session's bound Bot in group chats only. Direct messages are disabled. `format` defaults to `text`; `format=markdown` sends
the `text` body as a Feishu `post` with an `md` element for both new messages and
replies. The Markdown source is preserved; rendering follows Feishu's supported
syntax, not a promise of full CommonMark/GFM support. Images are not fetched or
uploaded. Both formats limit the encoded message content to 20 KiB, including the
rich-text envelope. The Hub authenticates the calling Session credential and
resolves exactly one active Bot binding (topic, group singleton, Bot singleton,
or document-comment Session). The tool cannot choose a Bot or another Session;
unbound, ambiguous, and foreign Sessions fail closed. Credentials stay on the Hub.
The destination must be explicit: a group `receive_id` with `receive_id_type=chat_id`,
or a group message ID to reply to. User recipient types are rejected. Before sending,
Bridge uses the bound Bot to resolve the destination chat (looking up the parent
message for replies), and requires Feishu `chat_mode` to be `group` or `topic`.
Private chats, unknown modes, missing metadata and failed lookups are rejected
without sending. This restriction applies to this MCP tool; normal Bridge input
and response routing is unchanged. Recipient permissions and Bot membership are
enforced by Feishu.
This is an external side effect; the Agent must have user authorization for the
destination and content. Successful calls return the platform message ID. An
optional `uuid` supports Feishu's time-bounded duplicate suppression; retries
must reuse it with identical format, content and destination. Delivery errors are surfaced
without falling back to user identity or a different Bot.

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
completion notifications, per-group settings, Agent memory, foreground steering,
background-task progress cards, and mentioning Turn initiators after final replies
complete. All are off by default. Agent memory uses the installation-wide
`memory.enabled` key.
Each enabled long-conversation completion, waiting or failure notification is
rendered separately for its private-message recipient and includes one native
mention of that recipient. Background child-Agent completion turns remain
excluded from these additional notification cards.
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

The reply-mention experiment uses
`bridge.feishu.experimental.mention_turn_initiator` and applies live on save.
When enabled, the first final Agent reply for each ordinary group-topic or DM Turn
mentions the distinct human users whose accepted messages created that Turn with
native Feishu mentions. A coalesced Turn may therefore mention more than one user.
When a background child Agent completes, the foreground Agent's resulting final
reply resolves the child task's frozen parent Turn and notifies that Turn's exact
initiators instead of guessing from the conversation's latest speaker.
Waiting, progress, tool-activity and in-progress answer cards do not mention
anyone. A non-streamed final post may carry the mentions on its initial delivery.
For streamed CardKit or legacy-card replies, the answer remains in its existing
progress/answer card or cards while work runs. Only when the Turn completes
successfully and every card has finished does Bridge newly send the full final
answer with native mentions; it then patches the old progress/answer messages to
retain only their execution summary, tool calls and processing history in the
existing collapsed panels. The original status and model/usage summary stays
visible; there is no separate completed-answer text, answer divider, brand footer
or conversation-extension section on the old card. A card without activity keeps
a minimal execution status instead of an empty card. Failed, cancelled,
interrupted or superseded Turns, Turns that report a provider failure,
Bot-to-Bot handoffs and an output stream that ends before the Turn finishes keep
their existing cards and send no mentioned final reply.
Both messages retain the original fill-width turn-card styling. The old card is
not recalled. The new final card omits the activity section and renders the
complete answer. Its footer keeps the brand on the left, with native initiator
mentions on the right and any conversation feedback immediately after those
mentions. When a conversation observer is installed, the last final card is a
finalized CardKit card and owns the reserved conversation section and completion
reference; feedback never targets a compacted progress card. Without a split or
when final delivery fails, the existing completion-card binding is unchanged.
An answer longer than one card is sent as
consecutive final cards split at the same limits as the streamed answer; only
the last one carries the mentions. Old cards are compacted only after every final
card was sent. A failed send or compaction is retried once during completion with
stable Feishu request identities, so it does not duplicate final cards; if it
still fails, the old cards keep their last detailed state.
A singleton DM sends the final card as a new DM, so it also receives the normal
Feishu message notification.
Direct singleton group output, group-watch automation, App/Bot-authored input,
background completion turns without an exact parent-Turn link, submissions without
a human sender and Turns without an Agent reply do not receive this decoration. It is presentation-only: stored Agent
output, thread context and provider conversation history retain the undecorated
Agent answer.

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

Each bound Session has one live timeline output stream. Concurrent binding,
input routing and session-added notifications reuse that stream, including its
readiness, turn state and card handles. A duplicate startup notification or a
new process launch in the same Session (including wake from sleep) must not
freeze an existing progress card or put the same turn's result on a new card.
Launch metadata is refreshed without replacing a live Session subscription;
new-launch command discovery continues through timeline events. Session identity
changes, binding transfers, explicit engine/profile/model reconfiguration and
ended or cancelled streams still establish a new attachment. A newly created Session may acquire its identity from the first Hub
snapshot without replacing its live stream.

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

Artifact links use paths relative to the workspace's `amux-outbox/` directory.
The Bridge accepts both `amux-artifact:chart.png` and
`amux-artifact:amux-outbox/chart.png`, removes one optional `amux-outbox/`
prefix after URL decoding, and validates the remaining relative path before
reading it. Equivalent links share one delivery identity, including across
retries. Absolute paths and directory traversal remain invalid; file reads
stay confined to `amux-outbox/` under the Session's execution identity.

Linux DAC file exchange requires a helper that supports the session-file
operation. An older helper produces an explicit operator upgrade notice for
both inbound attachments and outbound artifacts; it must not be presented as
a misplaced or oversized file. Read/write failures retain their underlying
diagnostic in operator logs, with the target, Session, launch and file name.
The helper performs file exchange under the principal UID using its own fixed
worker entrypoint, independently of the profile materializer version.

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
Official registration pre-fills a new app's description with `Powered by amux`,
including when automatic capability configuration is skipped. This brand text
is identical in English and Simplified Chinese and remains editable on the
creation page.

A separately maintained distribution may register one Bridge extension before
the Bridge is first used. It can supply provider presentation, coordination
profile recognition, a transactional migration of its legacy defaults, and an
explicit console setup fallback. Registration is immutable after first use and
duplicate registration is an error. These hooks do not change Session execution,
canonical events, access checks, or the default official registration flow.
Distribution-specific implementations and session-cache paths belong to the
distribution module; the public Bridge operates independently without them.

Conversation extensions submit and read background Tasks through a binding-scoped
capability. The Bridge validates the Bot/topic binding; the Hub resolves the exact
target and Session and uses its frozen principal, authority and isolation backend.
Authority is revalidated on submission and reads, including after restart. Missing
or changed bindings and foreign task identities fail closed. A completed parent
Turn is sufficient: these independent Tasks do not claim an active parent Turn,
notify the parent, or accept caller-supplied execution identities. Host-mode
conversations remain supported when principal isolation is disabled.

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
[Session model](session-product-model.md) defines when ordinary follow-ups are
steered into the running Turn or queued, and text coalescing. See
`router_control.go`, `router_submit.go`, `coalesce.go`, `steer_target.go` and
`skill_picker.go` under `internal/bridge/` for these input paths.

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

Group settings saves acknowledge the card callback immediately with a saving
status, then validate and commit in the background with a 15-second budget.
Duplicate saves for the same group are rejected while one is pending. Registry,
group-policy and routing lock waits and the database transaction honor cancellation;
a timed-out wait must not later commit the submitted settings. The result card
reports success, failure or a version conflict, and retains the submitted draft
for retry after a failed save. An optional daily-cleanup switch is saved after
the group transaction; failure of that separate write is reported as a partial
success, never as an unchanged group policy. Navigating away suppresses stale
card updates but does not revoke an already accepted save. Operator logs record
save-stage durations and errors without recording the form contents.

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
isolation requirement. Saving a watch policy checks the group’s version, updates
only that group, and preserves the other groups’ active policies. The result
card confirms the saved settings or shows a save failure for that group with a
link to reopen the editor. Full design and transition cases live in
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

Switching to a different runtime clears the prior runtime's model and reasoning
effort in the settings draft; the new runtime uses its default unless a model is
selected. Refreshing the same runtime preserves the draft. Switching also refreshes
optional model service controls. Each service action has a distinct card element name, so
install and recheck controls or multiple services can coexist. Saving submits
the selected runtime even when an optional service still requires setup.

Settings editors render their current draft before model discovery completes.
For up to 30 seconds, edits on the same card reuse a model snapshot scoped to
the acting Feishu user, resolved execution identity/environment, target, launcher
and Runtime catalog. Concurrent edits share discovery; explicit refresh,
login/install and Runtime changes bypass the snapshot. Cache data contains
choices and status, never provider credentials or executable callbacks.
Transient refresh failures retain choices for at most five minutes without
changing the draft; an authoritative signed-out result removes that source's
choices. Late results cannot replace a newer edit, saved settings or a different
page. Loading and failure states are available in English and Simplified Chinese.

## Turn model recovery actions

When a Feishu-started turn hits a provider rate limit or usage limit error, or
remains stuck in upstream retry or queue wait without visible output for at
least the configured threshold (default 60 seconds; adjustable live by the owner
in the Feishu Experiments card via
`bridge.feishu.experimental.model_recovery_queue_wait_seconds` in `0..3600`,
where `0` disables queued recovery controls while retaining rate-limit error
recovery), the turn card may present up to three same-engine alternative
model buttons (plus an optional "Other models" dropdown when additional
same-engine models exist) so the user can recover without starting a new topic or losing
session history. Candidate models come from the current engine's registered
model catalog excluding the active model; distributions may install an optional
`RankFallbackModels` Bridge hook to filter unavailable or high-load models and
rank healthier candidates first, falling back to the public catalog order on
timeout or error.

Clicking a recovery button is authorized only for the turn's initiator or the
Bot owner and is atomically claimed per turn so duplicate clicks cannot switch
or resubmit twice. If the turn is still running, Bridge interrupts it and waits
briefly for the session to become idle before invoking
`Restart(PreserveHistory: true)` on the existing session so engines that support
in-place hot reconfiguration apply the new model without restarting the process.
Bridge keeps a bounded in-memory recovery cache of recently submitted turn
inputs (evicted on normal turn completion or TTL expiry; large image payloads
are omitted). When the interrupted or failed turn had not executed any tool
calls and its cached input is still available, Bridge automatically resubmits
that turn on the new model; when tool calls had already run or the cached input
is unavailable, Bridge only switches the model and informs the user to continue
manually rather than replaying side effects. All recovery controls and status
notices are localized in English and Simplified Chinese.

## Distribution default migrations

Distributions declare exact old-to-new profile reference mappings for retired
defaults. Bridge owns the affected tables, parameterized updates and transaction.
Mappings apply simultaneously, preserve unrelated settings and historical session
identities, and are idempotent; targets cannot also be migration sources.

## Management card update ordering

Each management-card interaction invalidates pending results from the previous
view. Update checks and model discovery may finish in the background, but their
checking, success and failure cards must still belong to the current view before
writing. The version is checked after acquiring the card delivery lock and again
before sending a replacement DM after a failed PATCH. Already-issued requests
finish before the newer view is written through the same lock. Navigation keeps
the existing callback toast and asynchronous PATCH delivery.

## Management upgrade recovery

Management upgrades locate the current installed CLI even when a failed update
has moved the running Hub executable into a removed backup directory. The
installation metadata and default binary directory provide fallback paths.
Failures retain a bounded tail of CLI stderr in the operator log for diagnosis.

## Temporary Web management console

The owner's private `/amux` overview includes Web management console. Opening the
control page does not start a server. Enable starts one system-wide listener on
`0.0.0.0:<console.port>` for 30 minutes (default port 7892); Close, expiry or Hub
shutdown stops it and invalidates browser sessions. All Bots share the same
window, and repeated Enable does not renew an active window. Only the confirmed
owner acting on a server-recorded private management card can view credentials or
change the listener. Group/shared/unknown cards and non-owner callbacks are denied.

The private control page shows the reachable URL, remaining time, a random
32-character temporary password, refresh and close. Overview, callback values,
URLs and application logs/persistence do not contain that password. A new window
rotates it. Browser authentication and CSRF remain mandatory. The origin defaults
to the Hub host IP/global port or `console.public_url`; HTTP is explicitly marked
as unencrypted and suitable only for trusted networks. Use HTTPS for public access.
Lifecycle work is asynchronous so callbacks acknowledge without waiting for process
startup, and stale completion cannot replace newer navigation.

## Management interface language

Every subpage reached from `/amux`, including editors, confirmations, loading,
error and onboarding pages, retains a top-level Back to overview action. Nested
pages also name their parent (assistant settings, integrations, document comments,
expert library, or schedule details). Returning is navigation only: it never
submits an unsaved form or repeats a task. Surface metadata survives every
callback and asynchronous refresh so later pages keep the same navigation.

The overview shows eight management actions, including the capability plaza and
Web management console,
with at most three actions per row. The language
selector keeps the bilingual label `界面语言 (Language)` in both languages so a
viewer can find it after choosing an unfamiliar interface language.

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

Goal permission requests use the same frozen binding as the turn's replies.
Approval controls use Card 2.0 callback buttons in a `column_set`, including when
compacting oversized requests; the removed `action` container is not emitted. An
accepted ordinary submission cancels a pending goal admission offer atomically;
ordinary input in progress rejects goal offers without waiting for that input
to finish. TAIL Attach includes the active canonical turn's start snapshot from
the atomic read head. Bridge restores only a goal turn matching its persisted
destination and current binding, authorizing subsequent replies and permission
requests without replaying earlier output.

## Native question cards

Native questions use Card 2.0 forms with single or multiple selection and text
input. When custom answers are allowed, a separate optional text field overrides
the selected options; the server validates that at least one answer is present.
Each answerable question card includes one native mention of its originating user
in the footer so Feishu can notify the person who is authorized to answer it.
Option descriptions and product labels are shown without changing answer values.
Product text supports English and Simplified Chinese; model content stays literal.
Secret questions are not collected in Feishu chat cards: they are cancelled with
a localized explanation. Oversized questions are rejected rather than losing choices.

Only the originating user can answer a question card; callback message/chat identity
when supplied must match the saved request. A question is bound to its originating
turn and expires after 15 minutes. Cancellation returns no selected answer to the
native request. Submission is claimed once, and duplicate or stale cards cannot
affect a later turn. Synchronous cards retire on turn completion or stream teardown.
The pending store remains in memory: restarting Bridge invalidates old cards and
does not promise durable question recovery. A scheduled prompt targeting a bound
Session uses this same interaction path; child Tasks follow the Task question rule.

Async follow-up question cards remain answerable for 15 minutes after creation,
including after their originating turn succeeds. Answers queue without interrupting
active work and are pinned to the originating Session, launch and native conversation.
The native conversation is checked again at dispatch if an answer waits in the queue.
The original question accompanies the answer. Cancellation and expiry are local;
there is no pending provider RPC to resolve. Failed/interrupted turns and stream
teardown retire these cards. If answer delivery cannot be confirmed, the card is
retired with an explanation rather than allowing a duplicate submission. Bridge
restart invalidates these in-memory cards too.

## Distribution conversation extensions

A statically registered distribution may observe accepted, correlated conversation
submissions and canonical completions, and handle its own card callback namespace.
The public composition registers no observer or extra experiment entries. The
observer receives immutable binding snapshots and explicit automation/supersession
provenance; it owns its authorization, state, bounded background work and cleanup.
Host services provide scoped card delivery, owner/locale lookup, configuration
transactions and trusted background task submission through the Hub task router.
Extensions do not receive direct access to Bridge database tables. Their independent
experiment controls do not participate in the public save-and-restart form.

Completed CardKit replies expose a serializable card reference for an optional extension section. The stream owns all mutations until completion; after that, the extension persists monotonically increasing mutation sequences and updates only its section. Answer and activity elements remain owned by the reply renderer. No additional message is sent for a section update; unsupported or undelivered cards have no reference.

On ordinary replies, the optional conversation section appears after reply content and its footer divider, before the final brand footer, including when an activity card receives its answer later. On a successfully split mentioned final reply, the reserved section owns the footer row: brand, recipients, then a single feedback action. Rich extension states use the full width below that row. The serialized reference preserves the mentioned recipients so section updates and restart recovery keep the original footer and native mentions. Section updates do not add another divider or change the Agent answer.

## Scheduled task visibility

The Hub persists each firing before dispatch and correlates its Task or exact canonical Turn. Admission is not execution completion. An unavailable observation remains unknown; it does not imply failure or authorize replay. Older uncorrelated history is not used for proactive alerts.

Feishu personal schedule views and every action require the frozen creator, application scope, active identity binding, and execution access. Creator provenance comes from the authenticated request and exact Feishu Turn receipts, never the latest speaker, an arbitrary recipient, or the Bot owner. Unattributable schedules have no proactive notifications. Existing operator/CLI authorization remains unchanged. Status notifications are sent privately by the Bot to its configured administrator (owner), with creator attribution. The administrator alert center exposes task names, execution status, and notification controls (acknowledge, snooze, recheck), but grants no access to another creator's prompts, results, or execution controls. Personal views remain creator-scoped.

On upgrade, existing subtask schedules without a creator binding are recovered only from a valid frozen schedule identity with an authorization receipt, its human origin actor, a matching owner (if recorded), and exactly one active Feishu alias across applications. Ambiguous or missing provenance is left unbound; later-created schedules are never included in this recovery. Reads still check current execution access. Recovery hydrates persisted execution state without replaying tasks or historical notifications. Older submission records remain visible as legacy history with an unconfirmed outcome, deduplicated against correlated executions.

The two schedule entries in the `/amux` overview share one button row. Personal schedules and the administrator alert center provide a back button; returning refreshes the overview summary and respects the viewer’s existing management permissions.

Recurring success is quiet. Failures and missed runs are aggregated for 60 seconds; unknown outcomes wait five minutes. Ongoing incidents update their existing card, with at most one unacknowledged reminder per day. Recovery closes the card after ten stable minutes. One-shot terminal outcomes receive a receipt. Acknowledge stops reminders; snooze lasts 24 hours; pause affects future dispatch only. Default quiet hours are 22:00–08:00 in the scheduling timezone. New status messages are limited to three per administrator/application in 30 minutes.

Delivery state is separate from execution. A persistent outbox retries transient failures with a stable request UUID (Feishu deduplicates for one hour). Availability/blocking errors 230013/230053 suspend delivery until an explicit recheck; no fallback recipient is used. Missing administrator configuration retains pending notices and exposes a delivery diagnostic. Changing the administrator or migrating creator delivery retires old outbox requests and card IDs, clears recipient-specific errors/acknowledgements/snoozes, and preserves execution state, creator identity, and task notification preferences. Active incidents can then notify the new administrator; completed historical receipts are not replayed. `/amux` exposes personal summaries, paginated history, results, and these controls in English and Simplified Chinese.

## Daily cleanup choice

The owner can choose daily automatic cleanup when selecting ordinary or isolated topics in
onboarding, Bot defaults or per-group settings. The choice is installation-wide,
defaults to enabled, and is applied live. Older forms that omit the choice retain
the saved value. Disabling preserves data and existing lifecycle reclaim rules.
Each daily run sends the configured Bot owner a private bilingual report with
Runtime, time, counts, reclaimed space, skipped work and failures, including
empty results. Reports contain no conversation content, credentials or private
workspace paths. Delivery failure is retained for retry and logged.

The owner-only experiments page includes the installation-wide Codex executable. Empty restores `codex`; the value is resolved on the execution Runtime, including remote targets. Saving preserves fields omitted by older cards and uses the existing save/restart confirmation. Existing Sessions keep their launch executable.

Registered app-server integrations share the executable setting, owner-only experiments form, execution-node resolution and frozen Session launch state. Native model discovery uses that same executable, without static native fallback; provider/service entries stay separate.

## Capability discovery

`/amux` includes a Capability plaza. A shared bilingual catalog describes
implemented product workflows, prerequisites, and editable example prompts.
It covers schedules, experiments, per-group execution settings, experts and Skills, collaboration, context and operations.
Integration examples identify required Skills/access rather than promising a
connected account. Cards link to existing settings where applicable.

New Bot setup enables one daily discovery card, starting the following day at
11:00 in the Hub host's timezone. Existing installations are not opted in on
upgrade. The owner can enable or disable recommendations in the plaza or daily
card. Delivery and preferences are scoped to Bot and current owner, persist
across restart, and never target a group. Random selection avoids repeats until
the available catalog has been exhausted. At most one delivery is claimed per
local date, including concurrent workers and reconnects; offline dates are not
backfilled. A failed or uncertain send is not automatically replayed that day.

Browsing, changing examples and editing prompts never starts Agent work.
Explicit submission uses ordinary authorization and conversation routing, with
persistent duplicate suppression. This flow keeps first-run onboarding progress
independent. A sent request is not reported as completed work. Every plaza,
detail, prompt and result card retains navigation back to its parent and `/amux`.

Each natural-language example includes its capability description, prerequisites,
specific tool/management guidance and an allowlisted Hub-side state snapshot.
Saved experiments and running values are distinct; unavailable observations stay
unknown. The snapshot contains no credentials, full config, provider settings,
owner identity or watched-group IDs. An Agent's local filesystem is not evidence
of the Hub's settings. Management changes use the existing confirmed card flow;
missing tools do not authorize invented commands or direct database/config edits.
Formal `goal` and slash commands are routed unchanged so appended context cannot
turn a status query into creation. Goal examples draft concrete objectives and
completion criteria; the card itself explains the actual bare `goal` controls.

The [capability plaza guide](../guides/capability-plaza.md) documents all 16
capabilities, 32 examples, prerequisites, setup reentry and daily delivery rules.

## Owner onboarding

Model discovery uses the selected engine and the draft conversation environment,
including engines registered by a distribution through the app-server integration.

The owner can open the guided setup at any time from `/amux`. First owner
capture offers the same welcome card. Reentry preserves settings, expert
libraries and the saved draft. Guide progress is persisted per Bot and owner;
versioned callbacks reject stale cards and never replay an example on resume.

The guide separates message handling, runtime/model selection and first tasks.
Reasoning effort is always visible and a previously unset preference defaults
to the strongest capability actually advertised by the selected model. Saved
preferences and isolation requirements are preserved. Separate topic histories
can still share workspace files; execution isolation is configured separately.

Examples are submitted only after review through ordinary authorized Agent
routing. An accepted submission is not a completed task. Calendar authorization
is requested by the installed Skill during execution. The repeating joke
example uses a session-prompt schedule every minute, asks the owner to wait for
one actual result, then cancel that same schedule by natural language.

Continue exploring offers selecting an expert, adding a Git library, or creating
an expert with Skills. An expert is like a person with particular Skills; a Skill
describes a repeatable way of working. One Git library contains multiple experts
and shared Skills. Creation collects a remote Git address, responsibility and
Skills, then submits an authoring request to the Agent. The Agent must inspect
the existing repository and show a concrete preview before publication, retain
other experts, and wait for normal merge/sync before claiming adoption. The
wizard never reports repository publication merely from task submission.

The optional Skill catalog action submits the expert form to a review, preserving
its name, responsibility, repository and Skill requirements. After confirmation,
the Agent searches the catalog, presents real candidates and fixed versions for
selection, then downloads the selected package into the expert-library change
preview. It does not open a catalog website as the import action. Task progress
offers refresh and exploration without a message deep link; replies and any
authorization instructions appear in the same chat's task conversation.

Distribution hooks supply personal repository templates, Skill catalog sources
and execution guidance; proprietary endpoints remain outside the public core.

Operators can send the same card to the saved owner with
`amux bridge feishu onboarding --profile <name>`. This sends one private card
and does not start another event consumer, reset settings, or replay tasks.

A newly configured Bot with an already identified owner queues its welcome card
until the Bridge connects. Ordinary upgrades do not create that marker. Delivery
is claimed before sending so an uncertain outcome is not replayed on reconnect;
the permanent management entry remains available for explicit reentry.

Onboarding navigation and draft edits return the replacement card directly in
the callback response. Success navigation does not emit a generic update toast.
Examples use separate full-width actions with short labels and visible request
text. Each exploratory page offers Back, retaining saved drafts.
