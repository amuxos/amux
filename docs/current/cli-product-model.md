# CLI Product Model

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

The primary command groups are `setup`, `session`, `task`, `agent`, `schedule`,
`memory`, `isolation`, `console` and `system`.

The owner's private `/amux` → Web management console page opens and closes one
installation-wide temporary browser console. It is off by default. Opening binds
`0.0.0.0:<console.port>` (or `[::]:<console.port>` when the target origin is an
IPv6 literal; port 7892 by default) for 30 minutes; reopening an active
window does not extend it. Expiry, explicit close, or Hub shutdown stops the
listener and invalidates all browser sessions. All Bots share this same manager.
The card displays a reachable server URL, remaining time and a newly generated
32-character temporary password only in the confirmed owner's private card.
Passwords are passed to the child over stdin, never command arguments, URL,
environment, logs or application persistence. Each new window rotates the password.
`console.port` and `console.public_url` are read from current configuration when
opening a window without restarting the Hub. `console.public_url` overrides the
advertised origin; when unset, an active distribution-registered dynamic origin
resolver may resolve the browser-facing origin after the listener binds, and
otherwise the Hub host IP and global port are used. Non-loopback HTTP is
supported for trusted networks; it is not encrypted and public deployments
should use HTTPS.

`amux console` remains available for operator diagnostics. It defaults to a local
listener and a 30-minute lifetime. `--password-stdin` and a non-loopback
`--public-url` (or an active post-listen public-origin resolver) are required to
expose a non-loopback listener. `--duration` can
shorten, but not extend beyond, 30 minutes. A local invocation without a password
retains five-minute single-use fragment pairing. Password login exchanges the
password for an HttpOnly, SameSite=Strict cookie whose lifetime cannot exceed the
open window. Logout revokes one browser session without stopping the console.
All business reads and event streams require authentication; writes also require
the exact Host/Origin and a session CSRF token. Browser secrets are not stored in
localStorage. Agent and enrollment actor credentials cannot start this operator
surface. System daemon launches and maintenance restarts strip Agent/session
context environment variables (`AMUX_ACTOR_TOKEN`, `AMUX_ACTOR_TOKEN_FILE`,
`AMUX_SESSION_ID`, `AMUX_TASK_ID`, `AMUX_PRINCIPAL_ID`) so restarting or
upgrading from inside an Agent session does not contaminate the Hub daemon.
A managed child verifies its direct parent against the recorded Hub PID
and installed sibling Hub executable before using a loopback operator connection.
An enrollment file left in the host HOME does not change that managed child's
identity; explicit Agent/session identity still rejects startup. The private
managed flag alone grants no authority. This does not establish multi-user browser RBAC.
The managed console manages the host credential store; it does not impersonate an
enrolled Principal. Credential values are write-only: the operator can create a
profile, set or replace an environment variable or a credential file (a clean
relative path inside the execution home), remove one entry, or delete a whole
profile after typing its name. Responses and lists contain only profile, variable
and file names; values and file contents are never returned and never kept in a
browser draft. A secrets-store file that is not a credential profile is never
overwritten or deleted.

The console covers overview, Sessions and Timeline, TaskRuns and persistent
instances, Schedules, Bots and groups, expert content/Packs, credentials and local
settings. There is no Runtime-node management page, host shell or arbitrary
command endpoint. Backend capability lists determine available actions. Bot and
group settings changes use the live Hub Bridge transition and version checks,
including confirmation and recovery choices.

Bots can be added, disabled, re-enabled and deleted. Adding needs a profile name,
App ID and App Secret; the server checks them with Feishu before saving and refuses
an existing profile name or an App another profile uses. The App Secret is never
returned. A running Bot can rotate its App Secret for the same App; using another
App means adding a new Bot. Disabling keeps the credentials aside; only a
disabled Bot can be deleted, which removes its credentials but keeps its chat
bindings and settings. Bridges start with the Hub, so these changes take effect
only after `amux system restart`; the list marks Bots that start or stop on the
next restart and the console never restarts the Hub itself. Live settings stay
editable only for running Bots.

Groups are titled by their Feishu group name. Names are stored by the Bridge
whenever it resolves one (group watch, cards, external Bot references) and when
the operator refreshes names; a read never calls Feishu. Opening the page with
unnamed groups triggers one refresh per browser session, and a refresh button is
always available. A group whose name cannot be read (for example after the Bot
left it) shows its chat ID.

Settings are generated from the configuration reference, the same metadata as
`amux system config` and the generated reference. Each key appears under its
reference section with a bilingual description, default and allowed values,
including system functions such as task cleanup, broker reaping, staged Session
reclaim and structured-provider watchdogs, and every experimental feature. Keys
can be filtered by experiment, customization and editability; a customized key
can be reset to its default. Secrets, executable and file paths, map-valued keys
and per-Bot or per-group keys are not shown; keys that change Hub identity,
connectivity or admission are read-only. Saving uses the same validation as
`amux system config set`, including cross-field Session reclaim checks, and
applies all changes atomically against the version read. Most keys are read at
startup, so the console asks for a restart after saving.

Task records show what started each task: a Schedule, a persistent instance, an
Agent Session (a subtask) or a direct request. Scheduled tasks link to their
Schedule and can be filtered by it; the link comes from each Schedule's retained
run history (its most recent 50 runs), and session-prompt Schedules create no
task records. A Schedule page opens its task records. Persistent instances show
their owner Session (name, source and expert), their purpose (the task that
created the instance), their latest task and task count; an instance without its
own name is titled by that purpose. An instance page lists its recent tasks.
The browser cannot replace a Profile's content-only contract with model or
permission fields. Existing Sessions keep their execution snapshots. Pausing a
Schedule does not cancel admitted work; admission and final execution outcomes
remain distinct. The existing desktop Gateway and read-only viewer retain their
separate authentication and sharing contracts.

The expert library page lists Profiles read-only; the browser never edits an
expert's content, model or permission fields. Content authoring stays in the
natural-language flow that previews a diff and asks for confirmation before
publication. Each expert shows references from Bot and group defaults, Schedules,
persistent instances, and unarchived Sessions. Missing or unreadable reference
sources are reported as unknown, not as evidence that an expert is unused. This
view does not inventory every possible reference: watch or document-comment
configuration, external integrations, and offline Runtimes may also depend on a
Pack. Operators must check those separately before removing it. The Packs page
shows available expert counts, recorded revisions and per-revision
rollback. A removal needs explicit confirmation and rejects managed, builtin, and
managed local Packs. A temporary development or evaluation Pack can only be
removed, never synced or rolled back. When a development lease backs it, removal
also removes the development worktree if it has no uncommitted changes; otherwise
nothing is removed and the console explains why, and the operator may remove the
Pack while keeping the worktree and branch. The branch is always kept. It can keep downloaded files; if
references or reference availability require attention, the operator must also
explicitly confirm removal despite the risk. A Session's launch settings remain
frozen, but after removal a restarted or resumed Session may no longer be able to
re-project Pack assets or refresh its Profile. Removal checks are point-in-time:
independent settings can change concurrently. A failure to remove downloaded
files after unregistering the Pack is reported separately without exposing host
paths. Rollback prompts identify the exact revision. Host diagnostics such as
`agent doctor` stay in the CLI: they inspect the console host rather than the
Runtime that runs Sessions.

The overview reports Session lifecycle counts, Turn state, the most recent
TaskRuns (with a 24-hour success and failure tally) and live Bot connections.
Turn state comes from the canonical replay of at most 16 unarchived Sessions,
those with a live agent process first. A sleeping Session has no process, so it is
never counted as working or waiting and never makes the totals unknown. A live
Session whose Turn cannot be confirmed is shown as unknown, on the overview and on
its Session page, and a confirmed subset is never presented as a total. Each
Session is counted once: a Turn waiting on the owner counts as needing input, not
as in progress. Task summaries omit result payloads and bound
task text. A task index that is unavailable or slower than five seconds, or an
unconfigured Bridge, omits only its own section. Lifecycle controls follow the
Session state and the observed Turn. The Hub sends no event when a Session sleeps,
wakes or is archived elsewhere, so an open Session page re-reads the lifecycle
(without the Turn replay) when the live stream ends and every 20 seconds while the
tab is visible.
A reconnecting Timeline stream resumes after the last event the browser received,
so a long history that the Hub cuts off still completes; while it loads, the
Timeline says so instead of reporting a lost connection.
Once its history is complete, the Timeline of a sleeping or archived Session is
labelled history only and is not reconnected.
The unencrypted-HTTP notice applies to non-loopback origins; a loopback origin
(local use or an SSH tunnel) is not flagged.

Sessions, tasks and schedules use the hub
control plane. Each runtime executes work locally and connects to the hub
through an outbound tunnel.
Local operation requires a hub, but does not require the Feishu bridge.
The `local` target alias identifies the configured local runtime, including
when filtering `session list`; it does not require a runtime named `local`.
The `isolation` group manages principal accounts, aliases, delegations and the
isolation helper service; access rules follow the task model.

`bridge feishu group-settings` manages the local installation's group defaults.
These commands require the default-off `bridge.feishu.group_settings.enabled`
experiment. Enable it on the private `/amux` experiments page and restart amux
before configuring groups. Disabling it retains saved settings and bindings but
pauses new work in independently configured groups until it is re-enabled.
`show <chat-id>` exports the complete JSON settings snapshot (version zero means
the group has not opted in). `apply <chat-id> --file <json|->` validates and saves
that snapshot using its version as a compare-and-swap token. `new-conversation
<chat-id> --expected-version <n>` retires a group singleton's default pointer;
already accepted work continues. These operator commands run on the Hub host
and operate on its local Bridge database; they do not follow a remote Hub address.
The selected `--profile` determines the Bot scope and must match the JSON scope.
Execution availability is checked again when a Session launches.

A target identifies a runtime location. A session is an ongoing conversation
with one agent; a task is a bounded execution with a result. Profile references
select reusable expert content. Supported builtin profiles are `builtin/codex`,
`builtin/claude` and `builtin/pi`; the default is `builtin/codex`.
Built-in Pack manifests use v2. The CLI supplies the named built-in launch preset
when using a built-in shortcut; explicit runtime/model/native permission choices
override it. External v2 experts continue to require `--runtime`. The default
expert remains `builtin/codex`; its execution defaults are caller policy, not
Profile fields. New Sessions use native bypass when permission arguments are
omitted, including external experts and direct engine launches. Explicit
permission arguments override this default.

With an explicit runtime, a model name is passed to the native agent even when it
is absent from runtime suggestions. Packs do not define model allowlists.

`session set-model` and MCP `amux.session_set_model` retain an explicitly selected
runtime from the session's launch snapshot. They resolve the target model using
that runtime's model policy, preserving the frozen Profile identity and reasoning
effort without reapplying the source Profile's model allowlist. Unsupported
reasoning effort fails before changing the running session.

Every session uses the same line-oriented terminal UI. Submitted text enters the
session queue; `session queue` lists pending inputs, and `session queue cancel`
/ `session queue clear` drop one or all entries. Detach ends the UI connection
without ending the session. Raw byte
input and terminal resize are not transport operations. Restart/resume and model
changes follow the selected agent's capabilities.
If a Codex-compatible provider's native history is missing, restart, resume and
automatic wake recover using the available amux conversation transcript while
retaining the session identity, workspace and execution settings.

`amux setup` accepts `local`, `hub`, `remote`, `feishu` and `feishu-local`
targets; `amux setup local` configures the local hub and runtime using native
service supervision where supported. `amux install --source <directory>` installs local
binaries. `amux update --source <directory> --restart` updates an existing
installation without repeating setup. On Linux, updates also refresh an already
installed isolation helper and its materializer from the same bundle. Helper
installation and managed updates set the dedicated executable directory to `0755`
so isolated users can run the helper. This uses root or noninteractive sudo,
preserves the installed systemd unit and isolation
settings, and refuses replacement while isolated launches are active. An idle
helper holds an admission guard until the upgrade exits, preventing concurrent
launches from racing the restart. Older helpers without this capability require
a one-time maintenance stop before updating; stopped helpers stay stopped. A failed
helper refresh returns a failed update before ordinary service restarts; retry
`amux update` after the cause is resolved. It never installs or enables isolation
on a machine without a helper service. Linux release bundles must include the
matching helper. When an older installer hands off to the new CLI, post-install
completion can recover a skipped helper from its still-present source bundle
only after verifying that its CLI and runtime match the installed binaries.

Linux updates with an installed helper coordinate through the local Runtime before
installing binaries. Maintenance rejects new launches, inputs and TaskRuns while
allowing permission replies and interrupts. Already accepted queues and TaskRuns
drain without cancellation. The default wait is five minutes (`--drain-timeout`);
on timeout the update aborts before replacement and restores admission. Once
quiescent, running Sessions are slept and durably recorded, the helper is updated,
and requested service restarts complete before those Sessions resume. Sessions
already sleeping are not awakened. A renewable lease and durable recovery list
survive CLI handoff and Runtime restart; abandonment triggers recovery. Resume
failures are reported and retried, without silently discarding the recovery list.
Recovery attempts yield between Sessions. When an attempt reaches its deadline,
the next pass starts with Sessions it had no time to try. Retry backoff increases
only when a pass makes no progress, and resets when any pending Session is restored.
The updater waits as long as the Runtime continues reducing the pending count.
After a requested service restart, the updater allows up to five minutes for the local Runtime to become reachable. During session recovery, two minutes without observable progress ends the CLI wait. The Runtime keeps retrying.
Native goal continuations pause admission during maintenance; the existing goal
grant is retained and resumed afterward. Maintenance is a local administration RPC,
not a Hub or tunnel operation. An older Runtime without this protocol still uses
the helper's safe guard; active launches require a one-time maintenance upgrade.
The helper's final guard remains authoritative, including for orphan processes.

Public updates use the platform manifest
from `amuxos/amux-releases` by default. `AMUX_RELEASE_URL` overrides the source;
`AMUX_SCM_LATEST_URL` and the existing archive format remain supported for
distributions. Manifest downloads require SHA256 verification before extraction.
No authoring-runtime source is configured by default. Core installation can
proceed when optional authoring is unavailable.
Distributions may register explicit optional-component subcommands under
`amux setup` through the CLI `SetupCommands` hook. Building the command tree or
running core install/update never executes these installers.

`scripts/install.sh` provides one onboarding entrypoint for public source builds and local release
binaries. A standalone copy also downloads the latest public release, or the
version selected with `--version vX.Y.Z`. A checkout retains the source-build
default. Downloads resolve the version once, then fetch its immutable platform
package and checksum. Release packages include the matching Claude SDK sidecar;
Claude still requires Node.js 22 or newer. It detects installed `codex`, `claude`
and `pi` executables, prompts
when multiple runtimes are available, and accepts `--runtime` (`--provider` is
an alias) for unattended selection. The chosen runtime maps to the matching
`builtin/codex`, `builtin/claude` or `builtin/pi` Profile; missing runtimes and
invalid Profiles fail before installation. Native agent login remains owned by
the agent. Claude source installs also prepare and install its SDK sidecar.
The installer configures the local system and shell PATH. Optional `--feishu`
performs bot provisioning before inspecting the Bot Profile or binding a
Session. `--interaction-mode singleton` (the installer default) creates and
binds a named Session; `thread` saves the selected Profile/Runtime/Model for
new conversations. Installation does not send a smoke prompt or chat message.
Repeated runs reuse compatible singleton bindings and reject conflicting
ones instead of replacing a user's Session.

Before replacing an existing installation, the installer stages validated binaries and any
Claude sidecar outside the amux data directory, checks the selected agent,
and stops Pack autosync and the local system. A stop failure aborts replacement.
Ordinary reruns preserve configuration, credentials, Sessions and Pack data,
then start the new binaries and restore autosync only if it was running.
`--clean` explicitly uninstalls with data purge before a fresh local setup;
it removes amux configuration, credentials, logs, Sessions and managed Pack
data and requires Feishu provisioning again. It does not remove native agent
installations or their own login data. Staging permits reinstalling from
binaries or a sidecar already inside the directory being purged. The installer
rejects unsafe purge roots and a singleton workspace inside the purge root.
Source checkouts must also remain outside the purge root. Uninstall stops
Pack autosync, the local Runtime/Hub and registered standalone MCP/GUI servers;
it returns a stop failure before removing binaries, PATH entries or data.
An external host owns relaunch of its MCP/GUI processes.

Configuration discovery and generated documentation describe the available
keys. Generic configuration commands reject unknown keys before writing.
Agent pack and credential operations and local system recovery can work while
the hub is unavailable. Isolation and credential access follow the task model.
Credential command examples use the public provider platform. Distributions may
append examples for their own provider integrations during startup registration.
Persistent Instance interruption also follows the Task model: it cancels the
current owner TaskRun and keeps the instance resumable, rather than relying on
a provider interrupt acknowledgement to classify the run's final state.

Task event JSON streams expose `kind=usage` with a numeric `usage` object,
including the child's latest normalized snapshot and optional `cumulative_*`
provider-thread totals. `task logs --json` preserves the object; plain-text logs
do not print each usage update. The Task model defines the cumulative scope.

`amux memory search|show|upsert|forget|export` manages Markdown memory through the
Hub, independently of Agent Profiles and Runtimes. Search and show return bounded
JSON including revisions and continuation metadata. Upsert reads UTF-8 content
from `--file` or standard input; upsert and forget require `--expected-revision`
(a previously read hash, or `absent` for creation). Export writes one complete
document to standard output and fails if its revision changes during paging.
Export is plain text only; search, show, upsert and forget accept `--json`.
The commands and `amux.memory_search|get|upsert|forget` MCP tools have no scope or
principal override: the Hub authenticates the caller and selects the scope.
Managed processes pass their frozen `AMUX_MEMORY_BINDING` as a precondition;
legacy managed processes without it must start a new conversation. Disabled
memory tools are omitted from runtime MCP discovery and reject calls; disabling
memory does not delete files. See the [memory model](agent-memory-product-model.md).

The generated CLI reference is the complete command/flag contract, including
platform restrictions. Regenerate it after changing command metadata.

Application implementations live in importable `app/cli`, `app/hub` and
`app/runtime` packages. The `cmd` entrypoints select the public defaults. A
separate distribution module may register extensions before calling the same
application entrypoints. It depends on this module and owns its configuration,
release sources and additional command behavior.

`agent list` and Hub profile discovery share the registry's development-pack
classification (development metadata, temporary/evaluation flags, or the legacy
`-dev` suffix). `--all` includes these profiles; `--dev` selects only them.
Healthy profiles remain available when another pack fails to load; CLI JSON and
Hub responses report `profiles_error`, and human-readable CLI output warns on
stderr. `agent local create --description` records a purpose summary in the Pack.
Local creation and authoring produce content-only v2 Packs. Creation does not accept
`--type`, `--model`, or `--permission`; choose execution settings when launching.
Older Pack schemas are rejected without rewriting user files.

The hidden operator command `agent local convert-v2 --expected-revision <full revision>`
explicitly converts a managed v1.1 Local Pack to v2. It removes execution fields
and Provider sources, recompiles and validates content, then publishes a new
commit through the library lock and revision CAS. Old commits and materialized
snapshots remain unchanged. Empty unpublished libraries convert only their source
scaffold; already-v2 sources are a no-op. Installation and ordinary authoring do
not invoke conversion. The operator supplies runtime execution settings separately.

## Offline Profile rendering

`amux agent render` accepts `--runtime` and `--model` separately from the expert
reference. An external v2 Profile requires an explicit runtime before any output is written. Builtin shortcuts can use amux caller defaults.
Rendering projects content for that engine without launching it; execution
permissions remain native launch arguments supplied by the caller.

## Daily cleanup

The installation-wide `maintenance.daily_cleanup.enabled` switch defaults to true.
The Hub reads it live and schedules cleanup for ordinary and isolated topics at
06:17 in the Hub's local timezone once per calendar day and Runtime. There is no
cron dependency and no Agent turn. A durable result prevents duplicate cleanup
after restart; notification failures retry delivery without repeating deletion.
`amux system cleanup` previews the same Runtime cleanup; `--apply` executes it
and sends the result to configured Bot owners. Only host operators can invoke
this system operation; Agent credentials and enrolled users cannot.

Cleanup uses existing archived Session and seven-day terminal Task lifecycle
APIs. The report counts Task child Sessions removed through Task pruning.
An expired Task whose child Session can still run or resume is deferred. The
Session artifact sweep can remove broker PID and log files at least three hours
old for archived or sleeping Sessions whose brokers have stopped, while retaining
their Session records, replay and resume data. It retains referenced workspaces
and provider threads, recent orphan data, explicitly protected paths and
credentials. It never
sweeps global `/tmp`. Filesystem cleanup fails closed on unreadable authoritative
state and does not follow symlinks outside managed roots. Provider-specific
layouts are registered by distributions. Principal files are processed through
the existing privileged helper, never by granting the Runtime broad sudo.
Large reference snapshots travel as a sealed file descriptor so the helper
can evaluate the complete snapshot within the socket's message size limit.
Logs are bounded and SQLite compaction runs only above 300 MiB of free pages.

## Configured Codex executable

`adapter.codex.executable` selects a Codex-compatible executable (a PATH name, absolute path, or `~/` path); empty uses `codex`. It is a program path, not a shell command. The Hub forwards the configured program to new Codex Sessions; model discovery uses the same program on the selected Runtime. The Runtime resolves `~/` against the execution HOME. Each Session freezes its program for restart, resume and model changes; changing the setting affects new Sessions. Existing Sessions without this field retain `codex`. The owner-only `/amux` experiments form edits this installation-wide setting; save and restart activates it. The path must exist on each selected Runtime. It does not edit remote configuration files.

Registered app-server integrations share the executable setting, owner-only experiments form, execution-node resolution and frozen Session launch state. Native model discovery uses that same executable, without static native fallback; provider/service entries stay separate.

Operators can send a getting-started card to the saved owner with
`amux bridge feishu onboarding --profile <name>`. This sends one private card
to the saved owner. Add `--capabilities` to send the capability plaza instead;
`--capabilities --enable-daily` also enables daily discovery starting tomorrow.
Without `--enable-daily`, sending a plaza leaves delivery preferences unchanged.
Neither form starts another event consumer, resets settings, or replays tasks.

## Terminal access to MCP tools

`amux mcp call <tool>` accepts a JSON object on stdin (or `--input-file`) and
prints the MCP tool-result envelope as JSON. This is a terminal fallback when
an engine does not expose its registered amux tools to the model. It uses the
same enabled-tool registry, SDK schema validation, actor credentials and Hub
authorization as `amux mcp`; it does not bypass permissions or use a local
profile registry. It joins transport result chunks within one invocation;
domain pagination such as `profile_list` still requires following `next_cursor`.
Tool errors retain `isError` and return a nonzero exit code. Unknown tools,
disabled MCP and invalid input fail without running a handler.

MCP `session_stream` resolves an omitted target from the caller-visible session
listing. An ambiguous session name fails and requires a target; it never picks
the first matching machine.

MCP schedule creation, detail and list results retain numeric Unix timestamps
and also provide `next_run_at_iso` / `last_run_at_iso` in UTC RFC3339 when set.
Agents can quote these values without guessing a timezone or converting epochs mentally.
