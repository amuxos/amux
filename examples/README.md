<a id="示例导航"></a>

# Examples

[English](README.md) | [简体中文](README.zh-CN.md)

- [Distributed Hub and Runtime](02-distributed-hub/README.md): local demo, two-host configuration, and startup scripts.
- [Credential Profiles](03-credential-profiles/README.md): credential commands and JSON templates without real secrets.
- [Agent Profile Pack](public-pack/README.md): manifest v1.1 accepted by the current consumer, with code review instructions.
- [Claude review and Codex rules Packs](agents/README.md): authoring sources plus directly consumable v1.1 manifests.

The Runtime consumes only `dist/agent-pack.manifest.json`. It does not generate
manifests from legacy Profile TOML or fall back to reading TOML. For build and
usage instructions, see the [project README](../README.md) and
[documentation index](../docs/README.md).
