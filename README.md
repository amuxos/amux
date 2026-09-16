# amux

[English](README.md) | [简体中文](README.zh-CN.md)

[Website](https://amuxos.com/) · [GitHub](https://github.com/amuxos/amux)

amux runs and coordinates agent sessions across local and remote machines. It
provides a command-line interface, a terminal session UI, task and schedule APIs,
and an optional Feishu/Lark bot bridge. Codex, Claude and Pi are supported.

This is a source candidate for `https://github.com/amuxos/amux.git`, using the Go
module `github.com/amuxos/amux`. amux's original source code, documentation, and
examples are licensed under [Apache-2.0](LICENSE), unless otherwise marked.
Commercial use and integration into proprietary products are permitted subject
to the license. See [License and attribution](docs/licensing.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution and licensing rules, and
[AGENTS.md](AGENTS.md) for engineering instructions; Claude Code loads the latter
through [CLAUDE.md](CLAUDE.md).

## Build and Run

Clone the public repository:

```sh
git clone https://github.com/amuxos/amux.git
cd amux
```

Install Go 1.25 or newer, then build and install from the public module directory:

```sh
make build
bash scripts/install.sh --source ./bin
amux setup local
amux session run builtin/codex
```

Install and authenticate the selected agent separately using its official
instructions. The default profile is `builtin/codex`. The native agent owns its
model selection and authentication. No model endpoint or credential is bundled.
For a trusted local release archive, extract it and pass the directory containing
`amux`, `amuxd` and `amuxlet` to `scripts/install.sh --source`.

Claude additionally uses the Node sidecar:

```sh
make sidecar
export AMUX_CLAUDE_SIDECAR="$PWD/sidecars/claude-sdk/dist/index.js"
```

Set that environment variable on the runtime process before starting it. Use a
Node version supported by the pinned Claude SDK. `make sidecar` does not install
global npm links. Go-only builds do not need Node.

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
Go tests, checks third-party notices using Python 3, builds the commands, and tests
the sidecar when npm is available.
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

## License and attribution

When redistributing amux or a derivative work, provide the [LICENSE](LICENSE),
preserve applicable [NOTICE](NOTICE) attribution and other required notices, and
mark modified files as described in Apache-2.0 section 4. Attribution may appear
in accompanying third-party notices, documentation, or an appropriate component
list in an About view. A prominent logo or website credit is not required.

Dependencies and separately installed agents retain their own licenses and terms;
see [Third-party notices](THIRD_PARTY_NOTICES.md). Release archives place the
public core's license material under `licenses/amux/`. The [licensing guide](docs/licensing.md)
includes integration examples and explains the scope of these obligations.
