# Install amux

[English](installation.md) | [简体中文](installation.zh-CN.md)

By the end of this tutorial, amux's local Hub and Runtime will be ready to run an
Agent. Feishu is optional. The public release supports macOS and Linux on arm64
and amd64.

## Before you start

Install and authenticate one native Agent: Codex, Claude Code, or Pi. Confirm it
works in its own terminal first. amux uses that Agent's login; it does not provide
model access or install the Agent for you. Claude also requires Node.js 22+.

Use a normal working directory outside `~/.amux`. A prebuilt amux release does
not require Go. Managed services use launchd on macOS or systemd on Linux; see
[operations](../operations/maintenance.md) for hosts without a supported service manager.

## Install a release

Download the installer, then choose the Agent already installed on this machine:

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex
```

Replace `codex` with `claude` or `pi` as appropriate. Add `--version vX.Y.Z` with
an actual release version to pin it. The installer sets up the local system and
prints the PATH change; open a new terminal or apply that printed change.

## Check readiness

```sh
amux system status
amux system target list
amux agent doctor builtin/codex
```

The system should report healthy services, and the target list should include
the local Runtime. Use the matching `builtin/claude` or `builtin/pi` profile for
the doctor command. Static diagnosis does not send a model prompt. A real turn
is the next tutorial's verification step.

If `amux` is missing, check PATH. If the Hub cannot be reached, follow
[troubleshooting](../operations/troubleshooting.md) before running setup repeatedly.

## Alternative: build from source

Source builds require Go 1.25+; Claude builds also require Node/npm:

```sh
git clone https://github.com/amuxos/amux.git
cd amux
bash scripts/install.sh --runtime codex
```

Inside a checkout the script builds from source. For local binary inputs,
sidecars, and installer options, see the [repository installation reference](../../README.md).

Ordinary reinstallation preserves amux data but interrupts active work. `--clean`
deletes amux configuration, credentials, Sessions and managed Packs; it is not a
repair step for routine startup problems.

Next: [run your first Session](../guides/cli-sessions.md), then optionally
[connect a Feishu bot](first-feishu-bot.md).
