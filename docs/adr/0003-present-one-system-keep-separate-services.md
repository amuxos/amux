# Present one system while keeping services separate

[English](0003-present-one-system-keep-separate-services.md) | [简体中文](0003-present-one-system-keep-separate-services.zh-CN.md)

> Retained design record from the original repository's main branch. Dates, phases, source line numbers, and implementation assessments describe the state at the time. For current behavior, use the [CLI product model](../current/cli-product-model.md) and the relevant domain models.
> Source paths are relative to the public repository root. Implementations formerly in `cmd/amux`, `cmd/amuxd`, and `cmd/amuxlet` moved to `app/cli`, `app/hub`, and `app/runtime`; `cmd/*` now contains distribution entrypoints only.

Owners interact with one amux system, but amuxd and amuxlet remain independent
native service units. A single-host deployment installs both and aggregates
their status and lifecycle actions. The same system may host one dedicated Bot
Profile or multiple independently configured Bot Profiles, while distributed
deployments may place the hub and runtimes on different hosts without changing
the user-facing system model. This avoids exposing topology while preserving
the distributed runtime boundary.
