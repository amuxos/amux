# Public builtin profiles

[English](README.md) | [简体中文](README.zh-CN.md)

`dist/agent-pack.manifest.json` is the authored runtime manifest for this pack.
It uses the same v1.1 consumer contract as external Git packs, without requiring
a separate authoring tool to build amux. Its profiles use the user's native agent
authentication and model defaults; no hosted model service or credential is
bundled. Provider permission modes match the existing builtin profiles.

Public skills and instructions are still under review. Do not copy resources
from the private pack into this directory without checking their full contents,
references and redistribution rights. The public embed directive lists its
runtime input explicitly so adding an unrelated file here cannot silently embed
it into a binary.
