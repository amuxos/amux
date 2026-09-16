# Use native service supervision

[English](0002-use-native-service-supervision.md) | [简体中文](0002-use-native-service-supervision.zh-CN.md)

> Retained design record from the original repository's main branch. Dates, phases, source line numbers, and implementation assessments describe the state at the time. For current behavior, use the [CLI product model](../current/cli-product-model.md) and the relevant domain models.
> Source paths are relative to the public repository root. Implementations formerly in `cmd/amux`, `cmd/amuxd`, and `cmd/amuxlet` moved to `app/cli`, `app/hub`, and `app/runtime`; `cmd/*` now contains distribution entrypoints only.

New amux installations use launchd on macOS and systemd on Linux to keep the
amux system running and to execute lifecycle actions. Hosts without a supported
service manager use an explicit foreground mode; the existing PID-managed
daemon path remains only for migration compatibility and is not an automatic
fallback. This avoids competing supervisors and makes restart and recovery
state authoritative.
