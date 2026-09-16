<a id="manifest-v11-pack-示例"></a>

# Manifest v1.1 Pack Example

[English](README.md) | [简体中文](README.zh-CN.md)

This directory includes `dist/agent-pack.manifest.json` and `instructions/reviewer.md`,
showing instructions, model selection, and Provider binding for a code review
Profile. The Runtime reads the manifest; Profile TOML is not part of runtime parsing.

`https://provider.example/v1`, the model names, and `EXAMPLE_API_KEY` are examples
and cannot call a real model. Replace the manifest's Provider with your own before
use, or remove the example binding and use native Agent configuration. Manage
credentials through a [Credential Profile](../03-credential-profiles/README.md), not in the Pack.

Copy this directory's contents into the root of a separate Git repository,
commit them, then register it:

```sh
amux agent pack add example /absolute/path/to/example-pack-repo
amux agent show example/reviewer --json
```

`pack add` must point to the Pack's own Git repository. Do not use the current
parent repository as its root. See the [Agent Profile Pack format](../../docs/reference/agent-profile-format.md).
