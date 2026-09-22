<h1 align="center">amux</h1>

<p align="center">
  <a href="https://github.com/amuxos/amux-releases/releases/latest"><img src="https://img.shields.io/github/v/release/amuxos/amux-releases?style=flat-square&amp;labelColor=151b28&amp;color=79e8c1&amp;label=release" alt="Latest release"></a>
  <a href="https://github.com/amuxos/amux-releases/releases"><img src="https://img.shields.io/github/downloads/amuxos/amux-releases/total?style=flat-square&amp;labelColor=151b28&amp;color=93c5fd" alt="Total release asset downloads"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-93c5fd?style=flat-square&amp;labelColor=151b28" alt="License: Apache 2.0"></a>
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat-square&amp;labelColor=151b28&amp;logo=go&amp;logoColor=white" alt="Source builds: Go 1.25 or newer"></a>
  <a href="https://github.com/amuxos/amux-releases/releases/latest"><img src="https://img.shields.io/badge/macOS_%26_Linux-arm64_%C2%B7_amd64-a7f3d0?style=flat-square&amp;labelColor=151b28" alt="macOS and Linux, arm64 and amd64"></a>
  <a href="docs/README.md"><img src="https://img.shields.io/badge/docs-English_%C2%B7_%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-c4b5fd?style=flat-square&amp;labelColor=151b28" alt="Documentation: English and Simplified Chinese"></a>
</p>

<p align="center"><a href="https://amuxos.com/">Website</a> · <a href="https://github.com/amuxos/amux">GitHub</a> · <a href="docs/README.md">Documentation</a></p>

<p align="center"><a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a></p>

amux runs and coordinates Codex, Claude and Pi sessions across local and remote
machines, with a CLI, terminal UI, task and schedule APIs, and an optional
Feishu/Lark bot bridge.

This repository is the public source of amux; Go module:
`github.com/amuxos/amux`. Original code, docs and examples use [Apache-2.0](LICENSE)
unless marked otherwise, permitting commercial use and proprietary integration
under its terms. See [License and attribution](docs/licensing.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution and licensing rules, and
[AGENTS.md](AGENTS.md) for engineering instructions; Claude Code loads the latter
through [CLAUDE.md](CLAUDE.md).

## Build and Run

Prebuilt releases require no Go toolchain. Install and authenticate a native
agent first, then download the installer:

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex
```

The standalone installer downloads from `amuxos/amux-releases`; add
`--version vX.Y.Z` to pin a version. After installation, open a new shell or
run the PATH command printed by the installer.

Use `--runtime claude` (Node.js 22+) or `--runtime pi` for another installed
agent. Add `--feishu` for bot setup. To build from source:

```sh
git clone https://github.com/amuxos/amux.git
cd amux
```

Source builds require Go 1.25+ and an installed, authenticated native agent
(`codex`, `claude` or `pi`). Run from the public module directory:

```bash
# Detect installed agents and choose the runtime interactively.
bash scripts/install.sh --feishu

# Select the runtime explicitly; its matching builtin Profile is automatic.
bash scripts/install.sh --runtime codex --feishu --interaction-mode singleton

# Local CLI only; do not configure a Feishu bot.
bash scripts/install.sh --runtime pi
```

The installer maps each runtime to its matching `builtin/codex`, `builtin/claude`
or `builtin/pi` Profile:

- One installed runtime is selected automatically; otherwise choose by number or name.
- For noninteractive runs, pass `--runtime` (alias `--provider`). Missing runtimes fail before installation.
- Native agents own authentication and default models. `--model` overrides the Feishu bot's model; no endpoints or credentials are bundled.

The script builds and installs amux, updates PATH, configures the local system
and checks readiness. With `--feishu`, complete the QR flow in an interactive
terminal; incomplete setup stops before Session creation.

- Default **singleton** mode creates a named persistent Session and binds the bot.
- `--feishu-profile` changes the **Bot Profile** name (`default`), not the Agent Profile.
- `--session-name` and `--session-cwd` select the singleton Session and workspace.

```bash
bash scripts/install.sh --runtime claude --feishu --interaction-mode thread \
  --feishu-profile assistant --model sonnet
```

Thread mode saves Profile/Runtime/Model for new conversations. Reruns reuse bot
credentials and compatible singleton Sessions; conflicts fail with guidance
without deleting either Session or binding. Switching to thread mode preserves
the old Session but disables its singleton routing. An explicit `--model` updates
a reused Session via `session set-model`.

Reinstallation validates staged binaries before stopping Pack autosync and the
local system; stop failures abort replacement. It preserves configuration,
credentials, Sessions and Packs, then starts the new system and restores autosync
if previously running. **Active work is interrupted.** No test prompts or chat
messages are sent.

For a fresh installation, add `--clean`:

```bash
bash scripts/install.sh --clean --runtime codex --feishu --interaction-mode singleton
```

**`--clean` deletes all data in `AMUX_HOME` (default `~/.amux`)** after stopping
and uninstalling amux: configuration, credentials, Sessions, logs and managed
Packs. Feishu setup must be repeated; native agents and their logins are retained.

- Keep the source checkout and singleton workspace outside the purged directory.
- Binaries and the Claude SDK are staged outside it, so `--source` and `--claude-sidecar` may point into the old installation.
- Uninstall also stops registered standalone MCP/GUI servers; their external hosts own relaunch.

Claude requires Node/npm compatible with the pinned SDK. Source installs build
its complete sidecar into `~/.amux/sidecars/claude-sdk/`, discovered automatically.
Codex and Pi need no sidecar. The script never installs or upgrades native agents.

<details>
<summary>Advanced installation options</summary>

- **Local binaries:** `--source /path/to/binaries` must contain `amux`, `amuxd` and `amuxlet`.
- **Agent profile:** `--agent-profile <ref>` selects a builtin profile explicitly and derives the runtime from it.
- **Force source build:** `--from-source` builds from the checkout even outside one.
- **Claude sidecar:** pass `--claude-sidecar /path/to/claude-sdk` with `dist/`, `node_modules/` and `package.json`, or use the sibling `../sidecars/claude-sdk/` layout.
- **Source checkout:** a standalone script accepts `--source-root /path/to/public-amux` or `AMUX_INSTALL_SOURCE_ROOT`; internal distribution roots are rejected.
- **Shell PATH:** `--no-modify-path` skips adding an entry; clean uninstall still removes the old managed entry.

See `bash scripts/install.sh --help` for all flags.

</details>

## Quickstart

With the CLI installed, configure the local system once, then start a session
and hand it work:

```sh
amux setup local
amux session run builtin/codex --name demo
amux session send demo "Summarize this repository's layout"
amux session attach demo   # live timeline; /detach or Ctrl-] to leave
```

One installation runs a Hub (`amuxd`) that owns identity, routing and
scheduling, plus local or remote Runtimes (`amuxlet`) that execute agent
sessions. Sessions hold ongoing conversations; tasks are bounded executions with
results; schedules fire prompts or subtasks on a cron-like spec. The optional
Feishu bridge and external MCP hosts plug into the same control plane.

## Operation

Local sessions, tasks and schedules need the Hub and Runtime, not Feishu:

| Action | Command |
| --- | --- |
| Configure the local system | `amux setup local` |
| Add a bot through Feishu's official registration flow | `amux setup feishu` |
| Upgrade after rebuilding, without repeating setup | `make upgrade` |
| Download and install the latest public release | `amux update --restart` |

Public updates from the default release manifest verify SHA256 before replacing
binaries and the bundled Claude SDK. Override the manifest/archive source with
`AMUX_RELEASE_URL` or `--release-url`; custom URLs pointing directly at an
archive skip checksum verification.

Profile Packs use manifest v1.1 and support external Git Packs; there is no default
marketplace. Optional expert authoring needs a separately configured compatible
runtime, never downloaded implicitly by source builds. Bundled skills and the
desktop application are not part of this repository.

## Development

Examples and operating guides are listed in the [documentation index](docs/README.md)
and [examples index](examples/README.md).

This module builds independently. Other distributions depend on its Go
module and register extensions before calling its application entrypoints.
See [distribution development](docs/distribution-development.md).

`make check` verifies generated transport and references, runs Go tests, checks
third-party notices with Python 3, builds commands, and tests the sidecar when
npm is available. Live-account tests are excluded. Some fixtures and resources
remain outside this repository pending review; the suite does not establish
complete product acceptance coverage.

- [CLI model](docs/current/cli-product-model.md)
- [Agent model](docs/current/agent-adapter-product-model.md)
- [Bridge model](docs/current/feishu-bridge-product-model.md)
- [Task model](docs/distributed-task-model.md)
- [CLI reference](docs/reference/cli.md)
- [Configuration reference](docs/reference/configuration.md)
- [MCP reference](docs/reference/mcp.md)
- [Agent Profile Packs](docs/reference/agent-profile-format.md)

## License and attribution

Redistributions must include [LICENSE](LICENSE), preserve applicable [NOTICE](NOTICE)
and other required notices, and mark modified files per Apache-2.0 section 4.
Attribution may appear in third-party notices, docs or an About component list;
a prominent logo or website credit is not required.

Dependencies and native agents retain their own terms; see
[Third-party notices](THIRD_PARTY_NOTICES.md). Releases include core license material
under `licenses/amux/`. The [licensing guide](docs/licensing.md) explains obligations
and integration examples.
