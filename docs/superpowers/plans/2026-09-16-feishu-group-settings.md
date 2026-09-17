# Feishu group settings implementation plan

[English](2026-09-16-feishu-group-settings.md) | [简体中文](2026-09-16-feishu-group-settings.zh-CN.md)

This records the implementation and validation scope for
[the group settings specification](../specs/2026-09-16-feishu-group-session-defaults-design.md).
The implementation uses Go, modernc SQLite, existing Feishu cards and the Session
launch pipeline. Distribution-specific L4 fixtures and environment evidence remain
in the distribution repository.

## Implementation sequence

1. Add versioned group settings, atomic workspace updates, singleton anchors and
   input/output origin associations. Reads must not opt a group in. Test complete
   round trips, Bot/group independence, stale updates, creation races and restart.
   Include group references in Pack removal checks.
2. Resolve one complete launch policy with Profile, Runtime, model, effort,
   workspace and isolation requirements. Preserve explicit selections and existing
   Session snapshots. Serialize first singleton creation and transitions. Send
   singleton output directly to its group and recover accepted output after
   retirement or restart. Use passthrough for ordinary execution and pin required
   isolation to linux-dac without changing authorization.
3. Add owner-only, scope-authenticated, versioned management cards, natural
   language and workspace commands, and local operator CLI commands. Reuse the
   existing execution selectors and distinguish current Session settings from
   future defaults. Preserve legacy access and group-watch management.
4. Add the default-off installation experiment to the shared Experiments form.
   Save atomically, show pending restart, preserve omitted fields from old cards,
   and load persisted configuration in the local CLI. Gate editing and new work
   in configured groups while retaining accepted output and bindings. Verify
   legacy Bot behavior for unconfigured groups and DMs.
5. Update product models and bilingual generated references; run focused tests
   during editing, then the full repository gate on the frozen diff. Exercise
   real Feishu messages and Runtime state in L4, restoring test configuration and
   cleaning only test-owned Sessions.

## Acceptance evidence to retain

Record two-group routing and singleton reuse/reset, full model/Runtime/effort
selection, ordinary versus isolated execution UIDs, two isolated identities with
reciprocal DAC denial, unavailable isolation and helper-outage rejection, and
correct direct group output across Hub restart. Test default-off rejection,
saved-but-not-restarted behavior, enabled execution and restored disabled state.

Record build identities, actual checks and environmental limitations separately.
A selected model that starts but produces no final answer is not a passing reply
path; repeat that path with a supported control model and report both outcomes.
Do not include credentials, installation databases or private environment details
in public artifacts. Project AGENTS.md/CLAUDE.md remain tracked documentation.
