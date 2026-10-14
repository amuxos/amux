# Agent Profile Packs

[English](agent-profile-format.md) | [简体中文](agent-profile-format.zh-CN.md)

An Agent Pack is a Git repository containing a `dist/agent-pack.manifest.json`
manifest and its instruction and skill files. amux accepts **v2 only**. Other
schema versions are rejected without modifying the Pack. Runtime consumers do not
fall back to source TOML files.

## Identity and files

Registering a Pack as `team` makes its `reviewer` profile `team/reviewer`.
`team/reviewer@<revision>` pins a materialized revision. The Profile's `namespace`
and manifest's `namespaces` index organize entries without changing their refs.
All entries in `profiles` are consumed, including entries absent from that index.
`builtin` is reserved.

The manifest requires `schema_version: "v2"`, a nonempty `pack_namespace`, and a
nonempty `profiles` map. `namespaces` is optional. Instruction paths are relative
to the Pack root and are combined in listed order. Missing instruction files and
paths escaping the Pack fail validation.

## Content-only profiles

| Field | Meaning |
| --- | --- |
| `description` | Human-readable purpose, including built-in profiles. |
| `instructions` | Ordered paths to instruction files. |
| `skills` | Names of skills shipped by the Pack. |
| `envs` | Environment variable names requested from the credential store. |
| `env_refs` | Explicit references such as `credential:github:GITHUB_TOKEN`. |
| `namespace`, `extends`, `subagents` | Organization and authoring metadata. Publish resolved content; the consumer does not resolve source inheritance. |

Profiles cannot contain `type`, `model`, `providers`, `supported_models`, or
`permission`, including empty or null values. Top-level `providers` is forbidden,
even when empty. Provider source files are not part of a v2 Pack.

Execution chooses the runtime and model separately. Task `permission_policy`
also selects native permissions and normalizes native permission arguments;
direct Sessions keep caller/native permission settings.
A caller runtime setting or explicit `--runtime` is required. Built-in shortcuts
may have caller defaults; Tasks (including scheduled Tasks) also fall back to the
distribution's default builtin launch preset when runtime is omitted. These
defaults are outside the Pack. Model/provider
connections belong to Runtime registration. Credentials remain outside the Pack.
Resolved execution settings are frozen in Session/Task snapshots; refreshing
content does not replace those settings.

## Local authoring

amux creates the v2 source scaffold itself. Agent Pack Contract **0.8.0 or later**
compiles and checks it; its `init` defaults are not used. Installation obtains the
tool from the distribution's release source or `AGENT_PACK_CONTRACT_SCM_URL`.
An older release is rejected. `AMUX_AGENT_PACK_CONTRACT_BIN` can select an already
installed compatible compiler.

`agent local create` accepts content and description, without `--type`, `--model`,
or `--permission`. Create, fork, preview, evaluate, publish, and update all use
v2 sources and manifests. Existing older libraries must be converted explicitly
before use; amux does not silently rewrite user files.

Operators can explicitly convert a managed v1.1 Local Pack with
`amux agent local convert-v2 --expected-revision <full revision from agent local status>`.
Prepare the runtime, model, Provider connections and permissions before converting:
the command removes `type`, `model`, `providers`, `supported_models`, `permission`
and the Provider source directory; it does not migrate execution configuration.
Content values, list omission/empty-array semantics, IDs and assets are preserved.
TOML is re-encoded, so comments and formatting remain only in Git history.
A compatible installed Contract compiler must validate populated libraries before
a new commit and registry revision are published. Failures keep the formal branch
and registry unchanged. Empty unpublished libraries remain unregistered. Repeating
the command on v2 keeps the same revision. Historical snapshots remain available
as their original bytes; conversion does not make old v1.1 revisions valid v2 Packs.

## Discovery and projection

Default discovery includes built-in, local, and installed profiles. Development,
temporary, and evaluation Packs are excluded using registry metadata; the legacy
`-dev` namespace suffix remains recognized. `agent list --all` includes these
Packs and `--dev` lists only them. Explicit refs remain resolvable. An unavailable
Pack produces a diagnostic without hiding healthy Packs.

Only formal Git Packs can supply same-named skill overrides for built-ins. Local,
development, temporary, and evaluation Packs cannot override them. Other Profiles
retain their own skill sources.

Codex instructions use `AGENTS.md`, with skills under `.agents/skills`. Claude
uses `AGENTS.md` plus a `CLAUDE.md` import and `.claude/skills`. Pi uses its own
adapter projection. A task receives a frozen execution snapshot; updating a Pack
does not silently change a running task's identity.

See the [CLI reference](cli.md), [example Pack](../../examples/public-pack/README.md),
and [example validation tests](../../internal/agentprofile/public_pack_example_test.go).
