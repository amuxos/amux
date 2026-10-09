# Manifest v2 Pack Example

[English](README.md) | [简体中文](README.zh-CN.md)

This Pack contains code review instructions and a v2 manifest. It declares no
engine, model, Provider, or permission settings. Choose those at execution time.

Copy this directory into a separate Git repository, commit it, and register it:

```sh
amux agent pack add example /absolute/path/to/example-pack-repo
amux agent show example/reviewer --json
amux session run example/reviewer --runtime codex
```

`pack add` must point to the Pack's own Git repository, not the parent repository.
Configure the selected engine's credentials on the Runtime. See the
[Pack format](../../docs/reference/agent-profile-format.md).
