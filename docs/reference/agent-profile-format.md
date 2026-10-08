# Agent Profile Packs

[English](agent-profile-format.md) | [简体中文](agent-profile-format.zh-CN.md)

An Agent Pack is a Git repository with a `dist/agent-pack.manifest.json` manifest
and the instruction and skill files it references. amux consumes manifests `v1.1` and `v2`;
it does not read source profile TOML files as a fallback. A pack may use its own
authoring tools to produce the manifest. The optional managed authoring runtime
has no default download source.

The [example pack](../../examples/public-pack/dist/agent-pack.manifest.json)
contains a Codex reviewer with native and explicit provider model choices.
Its provider URL is illustrative and must be replaced with a real Responses API
endpoint before selecting that provider. The native default needs no provider.

## Identity and Files

If the pack is registered as `team`, its `reviewer` profile is `team/reviewer`.
`team/reviewer@<revision>` pins the materialized revision. The profile's
`namespace` and the manifest's `namespaces` index organize entries; they do not
add a path segment to the reference. All entries in `profiles` are consumed,
including entries absent from the namespace index. `builtin` is reserved.

The manifest requires `schema_version`, a nonempty `pack_namespace`, and a
nonempty `profiles` map. `providers` and `namespaces` may be omitted. Instruction
paths are relative to the pack root and are combined in listed order. Missing
instruction files fail pack validation. Paths must remain inside the pack.

## Profiles

In `v2`, profiles contain reusable content and environment requirements. The
fields `type`, `model`, `providers`, `supported_models`, and `permission` are
forbidden, including empty values. Directory layout, namespaces, inheritance,
asset lists, and top-level provider definitions keep their existing meaning.
Execution selects the runtime and model, and supplies native permission arguments.
An explicit runtime or caller runtime setting is required; there is no implicit
engine fallback. The resolved execution settings remain in session snapshots.
Existing v1.1 libraries and their defaults remain supported; migration is explicit.
The managed local authoring workflow continues to produce v1.1 libraries.

Builtin profiles also use v2. Named shortcut defaults live in amux caller configuration, separate from the manifest. Discovery omits Profile execution fields; `runtime_required` is false for builtin shortcuts that have caller defaults.

The execution fields in the table below apply only to v1.1.

Profile descriptions belong to the manifest, including built-in profiles.
Distributions may supply optional legacy metadata as a fallback for an omitted
description; a profile does not require a second registration in Go.

Default discovery includes built-in, local and installed profiles. Development,
temporary and evaluation packs are excluded using registry metadata; the legacy
`-dev` namespace suffix remains recognized. `agent list --all` includes these
packs and `--dev` lists only them. Explicit profile refs remain resolvable.
An unavailable pack produces a diagnostic without hiding healthy packs.
Only formal Git packs can supply same-named skill overrides for built-in
profiles; local, development, temporary and evaluation packs cannot override
them. Non-built-in profiles retain their own skill sources.

| Field | Meaning |
| --- | --- |
| `type` | Required base agent: `codex`, `claude` or `pi`. |
| `description` | Human-readable profile description. |
| `model` | Default model name or qualified provider model; empty uses the agent default when no providers are declared. Required when `providers` is nonempty. |
| `supported_models` | Optional exact model allowlist for profile selection. Omitted leaves native selection unrestricted; an explicit empty list permits only the agent default. A nonempty default must be included. |
| `providers` | Qualified references such as `service/example-model`, each defined in the top-level `providers` map. |
| `instructions` | Ordered paths to instruction files. |
| `skills` | Names of skills shipped by the pack. Skill contents must be available on the runtime to project them. |
| `envs` | Environment variable names requested from the credential store. |
| `env_refs` | Explicit references such as `credential:github:GITHUB_TOKEN`. |
| `permission` | Optional `permission_mode`, `allowed_tools` and `ask_tools`. |
| `namespace`, `extends`, `subagents` | Organization and authoring metadata. Publish resolved content in the manifest; the consumer does not resolve source-file inheritance. |

`permission_mode` is `default`, `plan` or `bypass`. Codex maps these to its approval
and sandbox settings; Claude maps them to native permission modes. Pi has no
equivalent native permission-mode argument. Task permission policy is a separate
control-plane rule, described in the [task model](../distributed-task-model.md).

## Models and Providers

Without an explicit runtime override, the profile's model policy applies. In an
explicit `supported_models` list, each qualified `binding/model` choice must have
a matching top-level provider definition. A provider-backed default must also be
listed in the profile's `providers` array for manifest v1.1 compatibility. If
`supported_models` is omitted, only the default model listed in `providers` binds
to a provider; other requested models remain native. A top-level provider alone
does not bind an unrestricted native model selection.

With an explicit runtime override, runtime suggestions are not an allowlist.
An operator-supplied native model name is passed unchanged to the selected agent,
including names absent from suggestions. No provider, endpoint or credential is
inferred from that name. Reasoning effort must still be supported by the adapter.
To use this pack's provider binding, select the profile model without a runtime
override.

| Agent | Provider platform | Wire API |
| --- | --- | --- |
| Codex | `openai-compat` | `responses` |
| Pi | `openai-compat` | `chat-completions` |
| Claude | Native agent configuration | Pack provider bindings are unsupported. |

A public provider requires `base_url`, `upstream_model` and a bare `api_key_env`
variable name. Additional platforms and their defaults belong to distribution
extensions. Omitted `wire_api` selects the agent's native default. Optional fields
include `context_window`, `max_tokens`,
`read_timeout`, `description`, `prompt_hint` and a string-valued `thinking` map;
their projection depends on the adapter. Configure credentials outside the pack.
The example deliberately contains no API key value. A Responses-to-Chat
compatibility gateway is unavailable.

## Workspace Projection

Codex instructions use `AGENTS.md`, with skills under `.agents/skills`. Claude uses
`AGENTS.md` plus a `CLAUDE.md` import, with skills under `.claude/skills`. Pi uses
its adapter-specific settings and extension projection. Skill source metadata
stays in the pack. A task receives a frozen profile snapshot; installing or
updating a pack does not silently replace the identity of an existing task.

Use the [CLI reference](cli.md) for pack registration, inspection, validation and
rendering commands. The [example validation test](../../internal/agentprofile/public_pack_example_test.go)
checks the shipped manifest and its native/provider selections.
