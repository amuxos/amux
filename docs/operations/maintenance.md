# Install, restart and update an installation

[English](maintenance.md) | [简体中文](maintenance.zh-CN.md)

Ordinary users work in Feishu. The operator owns the machine, native Agent login
and service lifecycle. Start with [installation](../tutorials/installation.md)
and [bot setup](../tutorials/first-feishu-bot.md).

## Check health before a change

In the owner's private `/amux` management, system status summarizes installation
health and provides restart/update-check actions. Group and topic controls do not
grant system maintenance authority. On the host use:

```sh
amux system status
amux system doctor
amux system restart --plan
```

A restart can interrupt active work. Review the plan and finish work before
`amux system restart`; the explicit `--force` option overrides the active-work
block and is not a routine requirement.

## Update binaries

`amux update --restart`

The default public release manifest verifies checksums. A custom direct archive
URL does not provide that verification; see [public releases](public-releases.md).
Updating an installation preserves state; it does not replace the native Agent's
login or automatically upgrade that Agent.

## Understand supervision and state

Supported hosts use launchd (macOS) or systemd (Linux). For unsupported hosts use
explicit foreground `amuxd` and `amuxlet` processes; inspect each binary’s
`--help` for its launch options. There is no automatic alternative supervisor. `~/.amux` contains installation state, configuration and secrets.
Back up the relevant state before destructive maintenance and keep source checkouts
outside it. Installer `--clean` removes amux data and repeats bot setup.

Remote workers execute on their own hosts. See [remote Runtime setup](../guides/remote-runtime.md)
for connection and target selection, and [troubleshooting](troubleshooting.md)
for symptom-based diagnosis.
