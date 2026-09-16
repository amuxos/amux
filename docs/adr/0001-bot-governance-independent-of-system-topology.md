# Make Bot governance explicit and independent of system topology

[English](0001-bot-governance-independent-of-system-topology.md) | [简体中文](0001-bot-governance-independent-of-system-topology.zh-CN.md)

> Retained design record from the original repository's main branch. Dates, phases, source line numbers, and implementation assessments describe the state at the time. For current behavior, use the [CLI product model](../current/cli-product-model.md) and the relevant domain models.
> Source paths are relative to the public repository root. Implementations formerly in `cmd/amux`, `cmd/amuxd`, and `cmd/amuxlet` moved to `app/cli`, `app/hub`, and `app/runtime`; `cmd/*` now contains distribution entrypoints only.

Each Feishu Bot Profile explicitly selects `personal` or `team` governance.
Personal limits that Bot's capabilities to its owner; Team allows authorized
members to use the capabilities that Bot exposes. Feishu activity never changes
governance implicitly, and changing an active singleton Bot from Personal to
Team requires confirmation because existing context becomes shared.

Governance is not an installation-wide deployment mode. One amux system may
host one dedicated Bot Profile or multiple independently governed Bot Profiles;
an operator may instead deploy a separate system when stronger process, data,
or failure-domain isolation is required. System lifecycle authority remains
with the system owner/operator in either topology.
