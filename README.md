# amux

[English](README.md) | [简体中文](README.zh-CN.md)

[Website](https://amuxos.com/) · [GitHub](https://github.com/amuxos/amux)

amux runs and coordinates agent sessions across local and remote machines. It
provides a command-line interface, a terminal session UI, task and schedule APIs,
and an optional Feishu/Lark bot bridge. Codex, Claude and Pi are supported.

This is a source candidate for `https://github.com/amuxos/amux.git`, using the Go
module `github.com/amuxos/amux`. License and redistribution notices must be
finalized before publication. No license grant is implied by
generating a candidate.

See [AGENTS.md](AGENTS.md) for contribution rules; Claude Code loads the same instructions through [CLAUDE.md](CLAUDE.md).

## Build and Run

Clone the public repository:

```sh
git clone https://github.com/amuxos/amux.git
cd amux
```

Install Go 1.25 or newer and install/authenticate at least one native agent
(`codex`, `claude` or `pi`). From the public module directory, run the installer:

```bash
# Detect installed agents and choose the runtime interactively.
bash scripts/install.sh --feishu

# Select the runtime explicitly; its matching builtin Profile is automatic.
bash scripts/install.sh --runtime codex --feishu --interaction-mode singleton

# Local CLI only; do not configure a Feishu bot.
bash scripts/install.sh --runtime pi
```

The installer maps `codex` → `builtin/codex`, `claude` → `builtin/claude`, and
`pi` → `builtin/pi`. One detected runtime is selected automatically; with
several installed, choose by number or name. Noninteractive runs should pass
`--runtime` (or its `--provider` alias). Missing runtimes fail before installation.
The native agent owns its authentication and default model. `--model` overrides
the model used by the configured Feishu bot; no endpoint or credential is bundled.

The script builds and installs amux, updates shell PATH, configures the local
system and checks readiness. `--feishu` adds QR provisioning and defaults to
singleton mode: a named persistent Session is created and bound to the bot.
Complete the QR flow in an interactive terminal; cancelled/incomplete setup
stops before Session creation. `default` is the **Feishu Bot Profile name**,
not an Agent Profile. Change it with `--feishu-profile`; use `--session-name`
and `--session-cwd` to choose the singleton Session and its workspace.

```bash
bash scripts/install.sh --runtime claude --feishu --interaction-mode thread \
  --feishu-profile assistant --model sonnet
```

Thread mode stores the same Profile/Runtime/Model for new conversations. Reruns
reuse bot credentials and compatible local singleton Sessions. A conflicting
Session or binding fails with guidance; it is not deleted. An explicit switch
to thread mode disables singleton routing and preserves the old Session. An
explicit `--model` on a reused Session applies `session set-model`.
Before replacing an existing installation, the script stages and validates the
new binaries and stops Pack autosync and the local system. A stop failure
aborts replacement. Ordinary reruns preserve amux configuration, credentials,
Sessions and Packs, then start the new system and restore autosync if it was
running. This interrupts active work. The script does not send test prompts or
chat messages.

For a fresh installation, add `--clean`:

```bash
bash scripts/install.sh --clean --runtime codex --feishu --interaction-mode singleton
```

`--clean` stops and uninstalls the old amux, then deletes all data in
`AMUX_HOME` (default `~/.amux`), including configuration, credentials, Sessions,
logs and managed Packs. Feishu must be provisioned again. Native agent
installations and their own logins are retained. Binaries and the Claude SDK
are staged outside that directory, so `--source` and `--claude-sidecar` can
point into the old installation. Keep the source checkout and singleton
workspace outside the directory being purged. Registered standalone MCP/GUI
servers are also stopped by uninstall; their external hosts own relaunch.

Claude also needs Node/npm supported by the pinned SDK. Source installation
builds and copies its complete sidecar to `~/.amux/sidecars/claude-sdk/`, which
the installed runtime discovers without an environment override. Codex and Pi
do not require that sidecar. Native agents are never installed or upgraded by
this script.

For local release binaries, use `--source /path/to/binaries` (containing `amux`,
`amuxd` and `amuxlet`). Claude additionally needs `--claude-sidecar /path/to/claude-sdk`
containing `dist/`, `node_modules/` and `package.json`, unless it is already in
the sibling `../sidecars/claude-sdk/` layout. A standalone copy of the script
can build a checkout selected with `--source-root /path/to/public-amux` or
`AMUX_INSTALL_SOURCE_ROOT`. It rejects an internal distribution source root.
There is no default public download URL. See `bash scripts/install.sh --help`
for all flags; `--no-modify-path` skips adding the shell PATH entry (clean
uninstall still removes the old managed entry). Open a
new shell after installation or run the printed PATH command in the current one.

## Operation

Local sessions, tasks and schedules require the local hub and runtime, but do not
require Feishu. `amux setup local` configures them. `amux setup feishu` adds the
optional bot integration through Feishu's official application registration flow.
For an existing installation, rebuild and run `make upgrade`; setup need not be
repeated. Updates use explicit local binaries or an operator-configured release
source. There is no default download host or pack marketplace.

Profile packs use manifest v1.1. Runtime consumption of external Git packs is
available. Optional expert authoring needs a separately configured compatible
authoring runtime; source builds do not download one implicitly. Bundled skills
and the desktop application are outside this candidate.

## Development

Examples and operating guides are listed in the [documentation index](docs/README.md)
and [examples index](examples/README.md).

This module is the independently buildable public core. Additional
distributions depend on it as a Go module and register extensions before calling
the importable application entrypoints. See the [distribution development guide](docs/distribution-development.md).

`make check` verifies generated transport and reference files, runs the included
Go tests, builds the commands, and tests the sidecar when npm is available.
Tests requiring live external accounts are not run. Some existing test fixtures
and resources remain outside this candidate pending review; the included suite
is not a claim of complete product acceptance coverage.

- [CLI model](docs/current/cli-product-model.md)
- [Agent model](docs/current/agent-adapter-product-model.md)
- [Bridge model](docs/current/feishu-bridge-product-model.md)
- [Task model](docs/distributed-task-model.md)
- [CLI reference](docs/reference/cli.md)
- [Configuration reference](docs/reference/configuration.md)
- [MCP reference](docs/reference/mcp.md)
- [Agent Profile Packs](docs/reference/agent-profile-format.md)
