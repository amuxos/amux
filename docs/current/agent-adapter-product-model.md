# Agent and Adapter Product Model

[English](agent-adapter-product-model.md) | [简体中文](agent-adapter-product-model.zh-CN.md)

## Product identities

Feishu is the primary conversational product entry. Its assistant is represented
by a Bot Profile; the selected expert is an Agent Profile. A Profile supplies reusable
expert content, an Agent type is an engine implementation, and a Runtime target
is an execution location. None is the identity of a running conversation.

Manifest v2 profiles require a caller-selected runtime and cannot declare engine,
model, provider, model allowlist or permission settings. Native execution arguments
and the runtime/model choice belong to the caller. New direct Sessions default to native `bypass` when permission arguments are
omitted, for both built-in and external experts. Explicit native permission
arguments remain authoritative. Task children derive their permissions from
`permission_policy` (default `policy-allow`). Only manifest v2 is accepted, including managed local
authoring. Execution settings are frozen in session snapshots. Refreshing v2 content
preserves those settings, including the provider binding and permission arguments.
The MCP `session_run`, `task_run`, `task_submit`, and `instance_spawn` inputs
expose runtime and model selection separately from the expert reference.
Task submission and instance creation expose native execution arguments as
`extra_args`; scheduled Task definitions preserve them for every trigger. These
arguments configure the native engine. For Task children, `permission_policy`
also selects native permissions and normalizes permission flags independently
of the selected expert. Runtime chooses the final native confinement from the
actual isolation backend and freezes it for resume.
CLI task submission and scheduled tasks also accept `--runtime`; scheduled
task definitions preserve that choice for each trigger.

A Session holds the conversation and can contain multiple Turns. A persistent
Agent Instance holds cross-task identity and durable execution references; each
assignment is a distinct TaskRun. Native Agent subagents do not automatically
create amux instances. See the [Session model](session-product-model.md) and
[Task model](../distributed-task-model.md). Public user terminology is explained
in [Assistants, experts and Agents](../concepts/agents.md).

Built-in Profiles also use manifest v2 and contain no execution fields. The amux
caller retains named built-in launch presets independently of Pack content, so
existing shortcut launches still select their configured runtime, model and
permission mode. Explicit caller settings take precedence. External v2 experts
have no Pack-owned execution preset. Direct session launches require a caller
runtime; Task submission (including scheduled Tasks and instance creation) uses
the distribution's default builtin launch preset when runtime is omitted. This
fallback supplies runtime/model selection without borrowing the default expert's
content or permissions. Profile discovery and
content projection never restore execution fields from the preset, and resolved
execution is frozen only in the launch snapshot.

## Runtime session and schedule capability

Every Runtime-launched Agent receives the `amux-session-schedule` Skill independently
of its Profile. The Runtime stages this resource outside the user workspace and
configures its short entrypoint when the native conversation is created or resumed.
Codex-compatible app-server providers merge it into `developerInstructions`;
hosts with an append-system-prompt surface merge it into that launch option.
App-server clear creates a new thread with the same Runtime entrypoint. Native
compaction retains the conversation's configured instructions. Ordinary turns
carry the original user input without a Runtime prefix or repeated instruction
messages. Profile and user instructions continue to load normally. Existing
brokers acquire this behavior after their next process restart.

Other hosts discover a small `amux-runtime-session-schedule` entry through their
adapter-declared native Skill catalog. That entry reads only the launch's
`AMUX_SESSION_SCHEDULE_CONTEXT` environment value to locate the shared guide and
its current transport and role. The Runtime publishes it at launch in its reserved
Skill directory; it does not edit workspace rules. Extensions must declare a
native system-prompt or Skill discovery surface to support this capability.

The context reports the launch's actual transport: configured Amux MCP for MCP
hosts, scoped CLI for Pi and isolated file-based extension hosts, or unavailable when automatic MCP injection is disabled
and no Amux server is configured. The Skill describes independent Sessions and
three schedule modes: `session-prompt` with `reuse-session` or
`new-session-per-run`, and bounded `subtask` execution. Availability is not an
authorization grant: Hub ownership checks and the current actor still apply.
Task-owned child Sessions receive a restricted role notice rather than automatic
session/schedule control tools. Linux DAC stages resources and control executables
through the selected principal's materializer, without copying the service user's
home. File-based extension hosts use CLI under DAC to preserve their existing
model, permission and MCP configuration rather than overwrite a shared file.

## Engines and provider behavior

Codex, Claude and Pi are public integrations. Adapters describe executable
discovery, launch configuration, protocol capabilities and workspace configuration
layout. Native agent authentication and model settings are used by default.
External model providers are configured by amux runtime/model bindings supplied
by distribution extensions; v2 Profile Packs cannot declare provider settings.
The public provider platform is `openai-compat`, with an explicit `base_url`:
Codex uses Responses and Pi uses Chat Completions. Additional provider platforms,
default endpoints and Pi compatibility settings belong to registered distribution
extensions; the shared Pi renderer consumes their configuration.
Runtime model suggestions are not an allowlist. An explicit native model name is
passed to the selected agent without inferring a provider, endpoint or credential.
Reasoning effort remains constrained by the runtime catalog's supported values
for the selected model.

Distributions may provide optional per-model display names in their runtime
catalog. Model pickers and session summaries use these labels, falling back to
the model ref when absent. Labels do not change stored refs, provider bindings,
upstream model names or existing sessions.

When a Codex-compatible provider explicitly reports that its saved conversation
is missing during restart or resume, the runtime starts one fresh provider
conversation with the available amux conversation handoff. The amux session,
execution settings and pending handoff remain intact. Other launch failures
remain errors and do not discard the saved provider conversation identity.

For Codex-compatible providers, `provider.structured.turn_hard_cap_seconds`
defaults to 21600 seconds (6 hours) per turn, allowing long-running work to
continue within that absolute limit. Explicit global and per-provider settings
take precedence; zero disables the limit, negative values use the provider
default, and configured values are capped at 7 days. Turn idle and tool idle
timeouts remain separate watchdog limits.

For Codex-compatible providers, `provider.structured.repeat_tool_call_limit`
defaults to 50 consecutive identical tool calls in one turn. The guard counts
unique started items with the same call-defining payload; duplicate frames do
not increment it, and a different call or a new turn resets the count. Changes
in returned output do not reset it. Reaching the limit fails the turn with
`code=repeat_tool_loop`. Zero disables the guard, negative values use the
provider default, and overrides are capped at 1000.

ACP v1 agents accept image input only when their `initialize` response explicitly
advertises `agentCapabilities.promptCapabilities.image = true`. Missing, false,
or malformed capabilities retain text-only input. The broker carries the
negotiated modalities to the Session on every connection, including Runtime
reconnects; older brokers without modality metadata remain text-only. Image-only
and mixed text/image submissions preserve content order and use ACP inline
base64 image blocks after the shared MIME, size and dimension checks.

ACP adapters may map the caller's model and reasoning selection to native
`configOptions` select values. New and resumed native conversations apply those
frozen settings before becoming available. Every requested value must be offered
by the agent and confirmed by its current selection or
`session/set_config_option` readback; missing or rejected settings fail instead
of silently choosing a default. Idle model changes use the same mapping without
restarting the process only when both the existing and requested model are
explicit and the provider is unchanged. Returning to the native default or
switching provider uses the existing relaunch path. A failed change
restores the previous native values; failure to restore closes the process.
A write without a confirmed native response retires the process instead of
attempting a rollback that a late write could overwrite.
Adapters without this mapping keep their existing launch-time configuration.

ACP `clear` creates a fresh native conversation in the same working directory
and reapplies its selected execution settings while retaining the amux Session.
ACP `compact` invokes `/compact` through `session/prompt` only when the agent's
current available-command snapshot advertises `compact`. Both operations use
the normal correlated Turn and terminal contract; no private compaction RPC is
guessed. The ACP broker transport remains compatible with existing sessions.

ACP usage retains the fields actually reported by the native agent. Final
`session/prompt` usage is projected to canonical snake_case token keys without
inferring cumulative totals or recomputing `total_tokens`. The latest
`usage_update` context `used`/`size` and typed `cost` coexist with those token
counts; missing values stay absent. A usage-only response still emits
`UsageObserved`. Interrupting a turn resolves pending and late native permission
requests with the ACP `cancelled` outcome before finishing cancellation. A
`session/cancel` successfully sent while a native prompt RPC is active records
cancellation for that request. Its completed response yields a cancelled turn
even if the native stop reason is `end_turn`. Idle interrupts and interrupts
after the response was read (including tail draining) do not rewrite that turn
or the next turn. Cancellation never emits an early terminal before the native
response or process retirement.

ACP watchdogs use the structured-provider idle, hard-cap and repeated-tool
settings: zero disables a layer, negative values select defaults, and values
are bounded before conversion. Completed native tool calls are considered identical only when their kind,
title and accumulated explicit call input match; pending placeholders and
updates to the same call ID are not new calls. ACP has no verified tool-progress heartbeat,
so tool execution and permission waits suspend the idle timer and remain bounded
by the hard cap. `tool_idle_timeout_seconds` has no separate effect on ACP.
On expiry, the driver cancels permissions and sends `session/cancel`, allows a
bounded cancellation grace, then retires the native process and drains its
reader before emitting the one error terminal. It retires the process even when
cancellation responds promptly because ACP updates lack native turn identity;
late frames cannot enter a later turn. Timers are scoped to one turn generation.


Codex uses its app-server protocol; Claude uses the Claude Agent SDK sidecar.
Provider and ACP implementations emit typed payloads. The session-owned canonical
timeline assigns turn identity and order and accepts exactly one terminal event
per turn. Provider message identifiers remain provenance. Observing a result does
not itself complete a turn. The session owns completion policy and queue advancement.

Sessions have an explicit lifecycle: `created`, `running`, `idle`, `sleeping` and
`archived`. A running session becomes eligible for automatic sleep only when its
idle timeout (never below 60 seconds) has elapsed with an empty queue, no turn in
flight and no pending permission prompts; sleep is recorded as manual, idle
timeout or restart failure. Sleeping sessions keep their identity and workspace
and wake on new input; archiving ends routability. Restart, resume and hot
model/effort reconfiguration (`set-model` without a process restart) follow the
selected agent's capabilities. Codex and ACP agents that provide an execution
configuration mapping support in-process reconfiguration when their eligibility
conditions are met; other changes restart with handoff.

When an ACP or structured agent's output closes, the broker stops accepting requests and drains
requests already being handled before closing client connections. Pending prompt
results, terminal emissions and acknowledgements retain their order. Connection
writes have a bounded shutdown deadline so an unresponsive client cannot prevent
broker exit; a transport failure remains an error. A shutdown request retains its
connection until its acknowledgement is written, even when stopping the provider
closes its output before the request handler returns.

Codex-compatible providers normalize `thread/tokenUsage/updated` into
`UsageObserved`. `tokenUsage.last` describes the latest invocation and supplies
token counts and context-window usage. Optional `tokenUsage.total` supplies the
provider thread's native cumulative snapshot as `cumulative_input_tokens`,
`cumulative_output_tokens`, `cumulative_total_tokens`,
`cumulative_cached_input_tokens`, `cumulative_cache_creation_input_tokens` and
`cumulative_reasoning_output_tokens`. Missing totals are omitted; snapshots are
neither added together nor inferred from `last`. The latest usage is also carried
by `ResultObserved.Usage`. Cumulative consumption is separate from context-window
occupancy and is never combined across provider threads or child agents.

Provider adapters must not interpret bridge presentation text. The reusable
terminal UI depends on callbacks and interfaces, never on internal session,
transport or Feishu packages. CLI and bridge consumers present the same accepted
canonical events. Permissions are structured requests with correlated responses.

The public builtin pack contains exactly three native-agent profiles. It embeds
no endpoint, credential, external skill or model-service binding. Native Responses
providers can be configured; a Responses-to-Chat compatibility gateway is not
available. Unsupported agents and wire modes fail before process launch or
credential materialization.

Additional distributions compose statically registered adapters and provider
factories through `pkg/extensions` before starting any application. Registered
providers use the same session timeline and isolation contracts as the builtin
providers. Unknown providers and duplicate registrations fail before launch.
The public module must build and test without any distribution module.

Codex native model choices are discovered on the selected Runtime through its
`codex app-server` account and paginated `model/list` APIs, using the execution
identity's native environment. Opening or refreshing a settings editor obtains
a fresh catalog, including model names and reasoning capabilities. The runtime picker does not
use an embedded GPT list. Explicit provider bindings remain separate.
A signed-out account, unavailable Runtime, ambiguous target or failed discovery
does not fall back to guessed models; the editor shows the status and preserves
existing selections. Native catalogs are suggestions, not authorization: Codex
validates explicit or previously saved native model/effort values at execution
time when no live capability snapshot is available. A live snapshot constrains
the editor's reasoning choices. Optional model-service authentication is
independent of native Codex authentication.

## On-demand system memory

When Hub memory is enabled, fresh Codex, Claude, and Pi sessions receive a fixed,
short memory protocol independently of Agent Profile. The Runtime freezes the
Hub-derived binding in non-secret launch metadata; memory documents remain on
the Hub and enter the conversation only through explicit tools. Ordinary turns
perform no Runtime memory lookup. Restarts and persistent instance reuse retain
the original binding; old sessions without one require a fresh session to opt in.
A private binding must match the effective execution principal. Hub authorization
still validates each tool request against current policy.

Codex uses additive developer instructions and per-process/thread overrides to
disable native memory reading and generation, plus a persisted disabled thread
memory mode. Unsupported native contracts fail visibly before any turn. Claude
merges append-system-prompt arguments and sets `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`
for each managed launch, including resumes. This also disables Claude subagent
persistent memory; ordinary CLAUDE.md instructions still load. Pi launches use its
JSONL RPC mode: its reviewed core has no built-in automatic long-term memory to
disable.
User extensions can implement their own memory and are not covered by that claim;
amux does not disable unrelated extensions. Other providers report memory
unavailability without assuming that a compatible transport supports Codex's
memory controls. No global agent configuration or native memory files are modified.

Private-home and Linux DAC launches pin `CODEX_HOME` and `PI_CODING_AGENT_DIR`
beneath their own HOME and clear inherited `CLAUDE_CONFIG_DIR`, preserving
Claude's default `.claude/` and `.claude.json` layout within that HOME.
Before loading the SDK, the Claude sidecar removes an empty `CLAUDE_CONFIG_DIR`
override from its own environment and the SDK child environment; an empty string
is a relative directory in the SDK, not a request for its default directory.
Linux DAC launches also set Claude's `IS_SANDBOX=1` compatibility flag: the helper
validates a dedicated non-root host UID even when the process sees UID 0 inside
its user namespace. This does not enable a permission mode or change hardening;
private-home and ordinary launches do not acquire this flag from amux.
Linux DAC materialization creates `.codex` as the target user even without native
credential files, preserves existing contents and rejects symlink destinations.
Claude's SDK inherits the sidecar's effective launch environment; the provider
must not reapply the Runtime's original environment after backend isolation.
Built-in Linux DAC launches do not modify the service user's workspace trust;
Pi uses its per-launch `--approve` override unless the caller selected an explicit
trust flag. Private-home launches continue using their own native trust files.
See the [native compatibility evidence](../reports/2026-09-17-native-memory-compatibility.md).

## Authenticated model services

A distribution may add model services independently of the execution engine.
Selecting a service model keeps the selected engine and its tool execution.
The Hub owns per-principal login state and upstream credentials. Each model
request authenticates with the existing AMUX actor credential, including remote
Runtimes. The broker owns a loopback gateway for the lifetime of its agent;
credentials and login challenges are not stored in model refs or launch specs.
Catalog entries preserve the upstream model/configuration pair and declared
reasoning capabilities. Unknown or unavailable selections fail explicitly.
No service is registered by the public distribution.

Distribution model services classify failures before writing a response. The Hub
and broker gateway preserve the classified HTTP status and safe diagnostic,
including login (401), permission (403), rate limit (429) and timeout (504).
Unknown failures expose only a generic availability diagnostic. Classified
diagnostics use English and Simplified Chinese together because this protocol
has no viewer locale; host language settings do not change its JSON output.
Errors after streaming starts abort the stream rather than reporting success.

For Claude launches marked with `AMUX_MODEL_SERVICE`, the sidecar applies the
selected gateway, credentials, model aliases and limits through the SDK's
in-memory flag-settings control before accepting prompts. These values take
precedence over user/project/local settings while preserving other settings and
managed policy. Credentials are not passed in command-line arguments or written
to settings files. Failure to apply the overrides fails startup explicitly.
SDK results marked `is_error`, or carrying any subtype other than `success`,
terminate the turn as an error and preserve the reported failure message.

## Distribution customization contracts

Provider validation always applies shared required-field and wire-protocol rules.
Distributions validate only additional agent/platform combinations; native
Codex and Pi OpenAI-compatible rules remain owned by core.

Workspace rules and skills use the adapter's declared configuration layout.
Special renderers such as Claude's additional instructions file remain in core;
distributions do not separately redeclare ordinary projection paths.

An optional Responses-to-Chat distribution registers its runtime command builder.
Core owns credential transport, isolation and process lifecycle; the distribution
owns the supervisor command and arguments. The public distribution still does
not provide a compatibility gateway.

Adapters whose configuration layout supports workspace skills (including Codex)
advertise that support through `ConfigLayout`; MCP exposes it as the
`supports_skills` capability.

Runtime model/provider connection bindings are registered by the amux distribution,
independently of builtin Pack content. Runtime catalog selection does not read provider
definitions from the builtin manifest. Packs cannot define providers. Existing execution snapshots retain their runtime bindings.

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

## Native user questions

Native user questions retain their question text, option descriptions, multi-select,
custom-answer and secret flags across the canonical event stream. Their legacy
permission transport envelope does not make them execution approvals. Codex-compatible
app servers whose read-only native `features list` advertises
`default_mode_request_user_input` receive a per-thread
configuration override enabling it, including resume and clear; explicit launch
feature overrides remain authoritative. Hosts without the capability keep their
native behavior. The integration does not switch collaboration mode or mutate
the user's configuration files.

Capability probes retain the agent's execution identity, isolation backend,
environment and working directory, but each probe owns a separate process launch
ID. Probe completion, failure or timeout must not close the agent's streams or
terminate its process.

Claude SDK `AskUserQuestion` is mapped to one correlated request per question.
The sidecar returns the original questions with `updatedInput.answers` only after
all answers arrive; multi-select preserves selected labels. Cancelling a question
cancels its whole native question batch, without choosing any business answer.
Late, duplicate or invalid replies are rejected. Interruption, shutdown and native
turn completion retire pending questions. Nonblocking native requests retain their
blocking flag; answers are accepted only while their originating turn is active.
