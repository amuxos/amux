# CLI Product Model

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

The primary command groups are `setup`, `session`, `task`, `agent`, `schedule`
and `system`. Sessions, tasks and schedules use the hub control plane. Each
runtime executes work locally and connects to the hub through an outbound tunnel.
Local operation requires a hub, but does not require the Feishu bridge.

A target identifies a runtime location. A session is an ongoing conversation
with one agent; a task is a bounded execution with a result. Profile references
select an agent and launch policy. Supported builtin profiles are `builtin/codex`,
`builtin/claude` and `builtin/pi`; the default is `builtin/codex`.
With an explicit runtime, a model name is passed to the native agent even when it
is absent from runtime suggestions. Without a runtime override, a profile's
explicit `supported_models` remains its model allowlist.

Every session uses the same line-oriented terminal UI. Submitted text enters the
session queue. Detach ends the UI connection without ending the session. Raw byte
input and terminal resize are not transport operations. Restart/resume and model
changes follow the selected agent's capabilities.
If a Codex-compatible provider's native history is missing, restart, resume and
automatic wake recover using the available amux conversation transcript while
retaining the session identity, workspace and execution settings.

`amux setup local` configures the local hub and runtime using native service
supervision where supported. `amux install --source <directory>` installs local
binaries. `amux update --source <directory> --restart` updates an existing
installation without repeating setup. No release or authoring-runtime download
source is configured by default. Remote sources must be explicitly supplied by
the operator. Core installation can proceed when optional authoring is unavailable.

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

The generated CLI reference is the complete command/flag contract, including
platform restrictions. Regenerate it after changing command metadata.

Application implementations live in importable `app/cli`, `app/hub` and
`app/runtime` packages. The `cmd` entrypoints select the public defaults. A
separate distribution module may register extensions before calling the same
application entrypoints. It depends on this module and owns its configuration,
release sources and additional command behavior.
