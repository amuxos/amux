# AGENTS.md

[English](AGENTS.md) | [简体中文](AGENTS.zh-CN.md)

This directory is the public Go module `github.com/amuxos/amux`, independently
buildable and releasable as a repository. Repository:
<https://github.com/amuxos/amux.git>; website: <https://amuxos.com/>.
Paths in this document are relative to this module root. Do not assume a parent repository.

`AGENTS.md` and its `CLAUDE.md` include are versioned engineering documentation,
not personal workspace metadata. Read [CONTEXT.md](CONTEXT.md), the
[documentation index](docs/README.md), and the relevant product models first.
Use established domain vocabulary. Identify conflicts with current product
contracts when making architectural decisions and update the affected models
and design records.

Maintain public documentation in English `.md` and Simplified Chinese `.zh-CN.md`
pairs with reciprocal language links and equivalent semantics. `make docs`
generates both languages; maintain `internal/doclang/translations.json` when
updating generated references. Executable example prompts, Skills, and the
`CLAUDE.md` include are not paired documentation pages.

## User-facing copy and i18n

Every addition or change to product-owned user-facing copy must include English
(`en`) and Simplified Chinese (`zh-CN`) in the same change. This applies to Feishu
cards and notifications, CLI help and human-readable output, terminal UI, and any
client surface: menus, dialogs, tooltips, accessibility labels, and success,
error, empty, loading, and permission states. Editing existing wording also
requires updating both languages. Do not defer one language to a follow-up.

Use the owning surface's i18n catalog and stable message keys; do not introduce
hardcoded product prose or translate arbitrary rendered strings. Preserve user
and Agent content, names, paths, IDs, command/flag names, machine-readable output,
protocol values, and raw third-party diagnostics; translate their product-owned
labels and wrappers. See the [language model](docs/current/i18n-product-model.md)
for selection, fallback, and isolation rules and migration status.

Before delivery, validate key coverage, placeholder/plural parity, both language
variants, and fallback for every affected flow, including error and empty states.
Cover changed rendering or language-selection behavior with focused tests; reuse
catalog checks for wording-only edits. A missing translation, raw key shown to a
user, or incomplete affected flow blocks completion even if English works. PRs
must state which surfaces and language variants were verified. Existing migration
gaps do not exempt new or modified copy from this requirement.

<a id="构建与验证"></a>

## Build and validation

```sh
make build          # amux, amuxd, amuxlet, amux-isolation-helper in bin/
make check-fast     # affected Go packages in this module; defaults to -short
make check-fast FAST_ARGS="--package ./internal/bridge --run 'Skill|SlashCommand'"
make test           # development tests, without long CLI workflow tests
make cli-doc        # CLI reference from the real Cobra command tree
make config-doc     # reference from real configuration metadata
make mcp-doc        # reference from the real MCP registry
make proto          # buf generate
make lint
make fmt
make clean
```

Useful focused tests: `go test ./pkg/acptui`, `go test ./internal/bridge`,
`go test ./internal/acp`, and `go test -run TestName ./internal/session`.

Unless the user explicitly limits or skips verification, run this before
committing, pushing, or tagging code, script, configuration, or protocol changes:

```sh
GOMAXPROCS=4 GOFLAGS=-p=2 GOCACHE=/private/tmp/amux-go-cache scripts/check.sh
```

During editing, prefer focused tests or `make check-fast`; run the full gate after
the final diff is frozen. Documentation-only changes may use `git diff --check`.
Fix failures before committing or pushing. After the same unrelated intermittent
failure occurs twice consecutively, retain evidence and report it; do not keep
retrying for luck. Do not repeatedly rebase just because main advances, unless
the change depends on it, conflicts, or the user explicitly requests it.

Do not run the same gate concurrently in one worktree. Separate worktrees may run
in parallel. Success receipts bind to source and environment; `--force` ignores
a receipt. Generated source changes during verification invalidate success.

The full gate covers protobuf generation and drift, CLI/configuration/MCP
references, dependency checks, all Go packages, long CLI tests, four binaries,
and the Claude sidecar. `scripts/list-long-tests.sh` discovers long tests in
`app/cli`; their first statement is `requireLongTest(t)` and the gate explicitly
sets `AMUX_RUN_LONG_TESTS=1`. Do not maintain a separate list of long test names.

The `go` directive in `go.mod` is a language floor, not a toolchain pin; CI pins
a patched toolchain separately in `.github/workflows/ci.yml`. A contributor's
local Go may therefore be newer than the floor, and a gate that passes locally
can still fail in CI. Keep the gate toolchain-agnostic: do not rely on standard
library behavior that differs between the floor and current releases, and verify
a suspicious toolchain-dependent failure against the CI version before dismissing
it. Go 1.25.0 and 1.25.1, for example, resolved a rooted `os.Root` `DirEntry`
against the process working directory, so `internal/memory` scans failed unless
the process happened to run in the store root.

<a id="结构与依赖边界"></a>

## Structure and dependency boundaries

- `cmd/{amux,amuxd,amuxlet,amux-isolation-helper}` are thin, buildable entrypoints.
- `app/cli`, `app/hub`, `app/runtime`, and `app/isolationhelper` own application assembly and its tests.
- `internal/*` owns core implementation; `pkg/extensions` is the public distribution registration boundary.
- `pkg/acptui` is a reusable terminal UI. It must not import `internal/*` or know about Session management, gRPC/tunnels, Feishu, or source frames. Inject behavior through callbacks and AgentStream/AgentSender.
- `agent-packs/builtin/` is the public built-in Pack; the `agent-packs` Go package embeds it as a read-only fs.FS.
- `examples/` contains usage examples. `sidecars/claude-sdk/` contains the Claude SDK source; its dist is build output.

The public core supports Codex, Claude, and Pi by default. Other distributions
register additional Adapters, Providers, Profiles, configuration, Bridge, and
branding through extensions. Public code must not depend on a specific
distribution or contain proprietary endpoints, credentials, or private resources.
Maintain one common implementation; do not customize by source overlays or copied
core packages. See [distribution development](docs/distribution-development.md).

<a id="系统与包职责"></a>

## System and package responsibilities

```text
CLI / Feishu -> amuxd Hub <- reverse gRPC tunnel <- amuxlet Runtime -> Agent process
```

Normal Session/Task/Schedule data paths pass through the Hub to the local or
remote Runtime. Local CLI-only use does not require Feishu, but ordinary data
paths still require the Hub. Pack/credential operations and local recovery can
work independently. `task.Runner` always executes on the selected Runtime and
owns child Sessions. The Hub owns registry, scheduler, task routing, truth
indexes, and reconcilers; it does not drive child Session RPCs directly.

| Package | Responsibility |
| --- | --- |
| `internal/config` | Configuration loading, constraints, and reference metadata |
| `internal/store`, `internal/replay` | SQLite persistence and Session output replay |
| `internal/session` | Session lifecycle, queue, dispatch, and completion policy |
| `internal/agentstream` | Canonical Turn identity, ordered live events, one TurnFinished, and an identity-free producer seam |
| `internal/adapter`, `internal/provider`, `internal/acp`, `internal/mux` | Agent capabilities, protocol parsing, processes, and brokers |
| `internal/task`, `internal/registry`, `internal/schedule` | Task runner, Runtime scheduling, and periodic tasks |
| `internal/executionauth`, `internal/principal` | Control-plane authorization and principal/delegation validation |
| `internal/agentprofile` | Manifest consumption, registry, Profile projection, and task-path materialization |
| `internal/mcp` | MCP stdio protocol; tool registration and CLI dispatch live in app/cli |
| `internal/gateway` | HTTP/WebSocket Session Gateway; depends on transport pbgen and backend interfaces |
| `internal/sessionlaunch`, `internal/tunnel` | Session Run requests, reverse tunnels, and routing |
| `internal/bridge` | Feishu WebSocket/API, routing, cards, output, context, and secrets |
| `internal/transport/pbgen` | Protobuf/gRPC stubs generated from proto/ |
| `internal/sourceframe`, `internal/workspace` | Input source boundaries and workspaces |

SQLite stays CGO-free with `modernc.org/sqlite`. Components retain single-tenant
ownership. Application entrypoints own wiring; Bridge rendering must not leak
into generic TUI, ACP, or Adapters. `app/cli` injects the Gateway backend; the
Gateway must not depend back on CLI.

<a id="产品模型先行"></a>

## Product models first

Before changing a domain below, read its current model. If rules change, update
the model before implementation and behavior tests. Do not let docs and code
diverge. Historical designs are background; target specifications must state
implementation status explicitly.

- CLI, arguments, configuration visibility, target/Session resolution, lifecycle, queue/restart, installation/upgrades: [CLI model](docs/current/cli-product-model.md). The command tree lives in `app/cli`.
- Adapter/Provider/ACP, Session events, permissions, resume/restart, attach: [Agent model](docs/current/agent-adapter-product-model.md).
- Task, Runtime registry, MCP, permissions, isolation, credentials, and Schedule: [Task model](docs/distributed-task-model.md) and [task guide](docs/distributed-agent-usage.md).
- Profile, Pack registry, projection, task-path materialization: [Pack format](docs/reference/agent-profile-format.md), CLI model, and Task model.
- Feishu routing, Bot commands, workspace/topic binding, permissions, output: [Bridge model](docs/current/feishu-bridge-product-model.md) and [thread context model](docs/current/feishu-thread-context-model.md).
- HTTP/WebSocket Gateway: [desktop client API](docs/reference/desktop-client.md).

Timeline owns amux Turn identity and the single terminal event. Provider/ACP
produce typed emissions directly. Sessions hand off accepted submissions through
correlated DispatchTurn and derive policy views from accepted canonical events,
including a synchronous completion barrier before advancing the queue. Do not
introduce legacy event adapters, inferred completion, or a second completion gate.
ResultObserved carries result content; it does not mean the Turn has finished.

`internal/agentprofile` depends on `internal/task` and `internal/adapter`, never
in reverse. Express source and capability in structured event fields; parse
legacy source frames only at application boundaries. Sessions use `pkg/acptui`
line mode, with input queued through Submit; do not restore raw Send/Resize.
The TUI supports `/detach` and Ctrl-]. Document only implemented interaction commands.

<a id="生成文档与协议"></a>

## Generated documentation and protocols

When configuration behavior changes, update types, defaults, constraints, CLI
management, and visibility in `internal/config/reference.go`, plus dynamic paths
in `config_reference_test.go`. Run `make config-doc`.
When command/flag visibility, arguments, defaults, or wording change, run `make cli-doc`.
When MCP registration, description, alias, or Schema changes, update registration
and dispatch tests and run `make mcp-doc`. Generators consume actual registries.
The CLI reference covers all platforms and marks restrictions independently of
the generating host. Commit both languages of all affected generated references
with the implementation; do not edit them by hand.

The protocol source is `proto/amux/v1/amux.proto`; `buf generate` writes
`internal/transport/pbgen/`. `buf.gen.yaml` pins generators invoked through
`go run`, reusing Go's module/build caches without remote code-generation requests
or preinstalled plugin binaries. gRPC uses the standard protobuf codec. Do not restore
old JSON codecs, handwritten transport/pb, or pbadapter compatibility layers.

<a id="运行与调试"></a>

## Running and debugging

Runtime state lives under `~/.amux/`: `runtime.sock`, `runtime.db`, `bridge.db`,
`schedule.db`, `config.toml`, `secrets/`, `agent-packs/registry.json`, and `sessions/`.
Source directory changes do not move these user data paths.

`AMUX_DEBUG_ACP_CHUNKS=1` records raw Runtime ACP chunks;
`AMUX_DEBUG_JSONRPC=1` records structured provider discarded frames. Set them on
the Runtime process; brokers inherit its environment. Debug output may include
protocol content. Manual E2E tests use a temporary HOME, a separate Hub port,
and cleaned-up daemons/brokers so they do not alter a real installation.

## Documentation publishing

Feishu user journeys lead the public documentation. Read the
[Session and conversation model](docs/current/session-product-model.md) when
changing conversation identity, bindings or Turn/Task boundaries. Maintain the
explicit bilingual `docs/navigation.json` catalog when adding pages.
`make docs` now also generates Feishu command references from the live help registry
and the repository indexes; `make docs-check` checks these and the catalog.
The [documentation workflow](docs/maintainers/documentation-system.md) defines
reader layers, status labels, verification scope and website regeneration.
