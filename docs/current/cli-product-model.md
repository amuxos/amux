# CLI Product Model

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

The primary command groups are `setup`, `session`, `task`, `agent`, `schedule`,
`memory`, `isolation` and `system`. Sessions, tasks and schedules use the hub
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
select an agent and launch policy. Supported builtin profiles are `builtin/codex`,
`builtin/claude` and `builtin/pi`; the default is `builtin/codex`.
With an explicit runtime, a model name is passed to the native agent even when it
is absent from runtime suggestions. Without a runtime override, a profile's
explicit `supported_models` remains its model allowlist.

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
installation without repeating setup. Public updates use the platform manifest
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
