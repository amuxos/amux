<a id="internalbridge-ownership-split--施工图"></a>

# `internal/bridge` Ownership Split — Implementation Plan

[English](2026-09-10-bridge-ownership-split-plan.md) | [简体中文](2026-09-10-bridge-ownership-split-plan.zh-CN.md)

> Retained design record from the original repository's main branch. Dates, phases, source line numbers, and implementation assessments describe the state at the time. For current behavior, use the [CLI product model](../current/cli-product-model.md) and the relevant domain models.
> Source paths are relative to the public repository root. Implementations formerly in `cmd/amux`, `cmd/amuxd`, and `cmd/amuxlet` moved to `app/cli`, `app/hub`, and `app/runtime`; `cmd/*` now contains distribution entrypoints only.

Status: **Phase one implemented** (state ownership and port separation); method migration follows later.
Scope: ownership refactoring of the `Bridge` struct in `internal/bridge`.
Baseline: D1/D3 (2026-08-16) completed the **file split**. This record addresses the deferred **ownership split** only.

<a id="0-实施状态2026-09-10"></a>

## 0. Implementation status (2026-09-10)

Completed and verified:

| Item | Before | After |
| --- | --- | --- |
| Fields declared directly on `Bridge` | 125 | 44 (+17 embedded collaborators +8 embedded ports) |
| mutex/RWMutex fields on `Bridge` | 14 | **0** |
| State maps on `Bridge` | 10 | **0** |
| Bare `func` fields on `Bridge` | 26 | **0** (grouped into 8 port structs) |

- **Wave 0 complete**: split `b.mu` (stream/seenMsgs); split `managementMu` into maintenance/pack/settings locks by subdomain; moved `groupWatchSettingsMu` ownership into `GroupWatch.applyGroupWatchExecutionSettings`, so settings no longer acquires it directly.
- **Wave 1 state ownership complete**: 17 collaborator structs (`streamController`, `inputRouter`, `turnCoalescer`, `permissionGateway`, `capabilityPermissionAuditor`, `groupWatchState`, `documentCommentController`, `botCoordinationState`, `nativeSlashController`, `botIdentityResolver`, `skillPickerStore`, `larkLookupCache`, `materialDelivery`, `managementSettingsState`, `managementMaintenanceState`, `managementPackState`, `botSettingsTransitionState`) own their state and locks through embedding.
- **Port separation complete**: 26 injected functions were grouped into 8 port structs (`ports.go`). Embedding preserves all `b.<port>` access sites.
- Validation: `go build ./...`, `go test ./internal/bridge`, and `make check-fast` all passed.

Completed in phase two (2026-09-10):

- **69 domain methods** moved to collaborator receivers (`Bridge` methods: 397 → 328; collaborator methods: 69). A method qualifies when every referenced `b.<field>` belongs to one collaborator and every called `b.<method>` belongs to that same collaborator.
- Promotion through embedding preserves external `b.Method()` call sites. Checks that only test the receiver dependency for nil were updated to use the collaborator receiver.
- Validation: `go build ./...`, `go test ./internal/bridge`, and `scripts/check.sh` all passed.

The remaining 328 methods perform orchestration across domains (referencing `db`/`client`/`hub`/ports or calling methods from multiple domains), which is a reasonable facade responsibility for `Bridge`. The 90 stateless helpers are shared across domains and do not fit a single collaborator. Neither group prevents moving more methods down when needed.

> Mechanical migration: after embedding collaborators in `Bridge`, `b.field` / `b.field = x` (promoted fields) and `b.method()` call sites **need no changes**. Only composite literals such as `&Bridge{field: ...}` stop compiling; a script rewrote these as `&Bridge{collab: collab{field: ...}}`.

<a id="1-背景与现状"></a>

## 1. Background and baseline

<a id="11-已完成的d-系列2026-08-16"></a>

### 1.1 Completed work (D series, 2026-08-16)

| Commit | Change | Nature |
| --- | --- | --- |
| `5ca1e37e` (D1) | `management.go`: 6267 lines → 7 `management_*.go` files | Pure code move |
| `0dc62ae6` (D3) | `router.go`: 2573 lines → 5 `router_*.go` files; `db.go`: 2827 lines → 4 `db_*.go` files | Pure code move |
| `7b35aad9` (D2) | Split `codex/proc.go` by concern | Pure code move |

The D-series commit messages explicitly say "behavior unchanged / pure code move".

<a id="12-未完成的本文范围"></a>

### 1.2 Unfinished work (scope of this record)

The D series **did not touch `bridge.go`**. Ownership in the `Bridge` struct was unchanged:

```text
Bridge struct fields: before D1 130 → after D1 130 → current 125
(The struct declaration spans 144 lines including blanks and comments; it has 125 fields.)
*Bridge methods:       396, spread across approximately 60 non-test files
*Bridge sync primitives: 29 (mutex / RWMutex / WaitGroup / chan)
26 injected func fields
```

The file layout was tidied, but **cognitive complexity, lock ordering, and testability did not improve**. The complexity changed from "one 6267-line file" to "60 files sharing one receiver".

<a id="2-字段全量归属映射125125"></a>

## 2. Complete field ownership map (125/125)

<a id="21-注入依赖--端口40-个不拥有可变状态"></a>

### 2.1 Injected dependencies and ports (40 fields, no mutable state ownership)

`DEP` (14): read-only dependencies shared across domains, held and distributed by `Bridge`:

```text
hub  sessionStarter  db  client  secrets  profile  bridgeID
isolationSupport  botE2EConfig  subtasks
sessionViewerBaseURL  sessionViewerToken  threadContext  promptRenderer
```

`PORT` (26): currently bare `func` fields, to be extracted into 8 port interfaces (see §4).

<a id="22-collaborator85-个字段"></a>

### 2.2 Collaborators (85 fields)

| Collaborator | Field count | Fields | Current files | Owned locks | Cross-domain access |
| --- | --- | --- | --- | --- | --- |
| `ManagementConsole` | 22 | managementMu, managementActions, managementSystemInfo, managementPackActions, managementRestartDelay, onboardingCardSentByOpen, maintenanceActive, maintenanceConfirmations, managementRecoveryRunning, documentSettingsActive, botToBotSettingsActive, groupWatchExecutionActive, permissionRecheckActive/Message/Version, permissionApplyActive/Message/Version, packLibraryCards, nextPackLibraryView, botSettingsMu, pendingBotSettings | management.go, management_*.go, bot_settings_transition.go | `managementMu`, `botSettingsMu` | 🔴 `managementMu` spans 3 files |
| `StreamController` | 9 | mu, streams, streamTokens, streamReady, streamSessions, streamSessionID, streamFreeze, supersededTurns, nextStreamToken | stream.go | `mu` | 🔴 `mu` reused by router.go |
| `NativeSlashController` | 9 | nativeSlashMu/SyncOnce/Commands/TargetID/Session/Aliases/Skills/Syncing/SyncAgain | slash_commands.go | `nativeSlashMu` | ✅ None |
| `DocumentCommentController` | 9 | documentSourceMu, documentMu, documentStreams, documentStreamReady, documentTurns, documentTurnTimeout, documentCommentSemaphore, documentCommentsEnabled/Configured | document_comment.go | `documentSourceMu`, `documentMu` | ✅ None |
| `CapabilityPermissionAuditor` | 6 | capabilityPermissionMu, capabilityPermissions, permissionAuditChecked/Failed/Mu/Once | capability_permission.go | `capabilityPermissionMu`, `permissionAuditMu` | ✅ None |
| `TurnCoalescer` | 5 | turnMu, turnActive, coalesceMu, coalesced, coalesceDelay | coalesce.go | `turnMu`, `coalesceMu` | ✅ None |
| `BotCoordination` | 5 | botToBotSettingsMu, botToBotMu, botToBot, externalBotCatalogMu, externalBotCatalogSeen | bot_to_bot.go, external_bot_catalog.go | `botToBotSettingsMu`, `botToBotMu`, `externalBotCatalogMu` | ✅ None |
| `GroupWatch` | 4 | groupWatchMu, groupWatchSettingsMu, groupWatchConfig, groupWatchConfigChanged | group_watch.go | `groupWatchMu`, `groupWatchSettingsMu` | 🟡 settings changes config directly |
| `MaterialDelivery` | 4 | artifactMu, artifactInFlight, imageNoticeMu, imageNotices | artifact_output.go, material_input.go, image_input.go | `artifactMu`, `imageNoticeMu` | ✅ None (3 files in one domain) |
| `InputRouter` | 3 | routeMu, routeLocks, seenMsgs | router.go | `routeMu` | 🔴 Reuses `mu` |
| `PermissionGateway` | 3 | permissionMu, permissions, resolvedPermissions | permission.go | `permissionMu` | ✅ None |
| `SkillPicker` | 2 | skillPickerMu, skillPickerClaims | skill_picker.go | `skillPickerMu` | ✅ None |
| `LarkLookupCache` | 2 | chatBotCountMu, chatBotCountCache | lark_api.go | `chatBotCountMu` | ✅ None |
| `BotIdentityResolver` | 2 | botIdentityMu, botIdentityCheckedAt | bridge.go | `botIdentityMu` | ✅ None |

<a id="3-必须先修的跨域违规p0"></a>

## 3. Coupling across domains that must be fixed first (P0)

These three cases share locks across unrelated state. Fix them before splitting ownership, or the coupling merely moves into child structs.

<a id="31-bmu-同时保护-stream-状态与-seenmsgs"></a>

### 3.1 `b.mu` protects both stream state and `seenMsgs`

```text
stream.go:225/242/342/373/386/1173   b.mu  protects streams/streamTokens/streamReady/...
router.go:420                      b.mu  protects seenMsgs
```

One mutex covers two unrelated sets of maps, creating unnecessary contention and implicit lock ordering.
**Fix**: streams use `streamMu`; `seenMsgs` belongs to `InputRouter.mu`.

<a id="32-managementmu-跨-maintenance--pack--settings-三个子域"></a>

### 3.2 `managementMu` spans the `maintenance`, `pack`, and `settings` subdomains

```text
management_maintenance.go   9 sites
management_pack.go          9 sites
management_settings.go     18 sites
```

**Fix**: split locks by subdomain (`maintenanceMu` / `packMu` / `settingsMu`), or first move `managementMu` into a `ManagementState` struct that exclusively owns the lock and provides atomic access methods.

<a id="33-groupwatchsettingsmu-被-management_settingsgo-直接加锁改配置"></a>

### 3.3 `management_settings.go` directly acquires `groupWatchSettingsMu` to change configuration

```text
group_watch.go:111              b.groupWatchSettingsMu.Lock()
management_settings.go:594      b.groupWatchSettingsMu.Lock()
```

**Fix**: `GroupWatch` provides `ApplySettings(cfg)`. Settings calls it without accessing the internal lock.

<a id="4-26-个-func-字段--8-个端口"></a>

## 4. 26 function fields → 8 ports

| Port interface | Methods (current fields) | Consumers |
| --- | --- | --- |
| `IdentityPort` | resolveExecutionIdentity | bridge (Session startup) |
| `MessagingPort` | threadNotice, chatNotice, dmNotice, dmInteractive, managementCardPatch, reactionAdd | management_settings, router_submit, capability_permission, completion_notification, document_comment, management_maintenance, router_workspace, bridge |
| `LookupPort` | topicRootLookup, quotedMessageLookup, chatNameLookup, chatBotCountLookup, botIdentityLookup | completion_notification, router_workspace, material_input, quoted_message, management_groups, lark_api, bridge |
| `DocumentCommentAPI` | documentCommentFetch, documentCommentReply, documentCommentReact, documentSubscribe | document_comment |
| `ExternalBotPort` | botHandoff, botHandoffPrompt, externalBotDelegation, externalBotCatalogFetch | bot_to_bot, router_workspace, external_bot_coordination, external_bot_catalog |
| `ArtifactPort` | artifactUpload, artifactSend, fileResourceFetch, imageResourceFetch | artifact_output, material_input, image_input |
| `GroupWatchAPI` | groupWatchListMessages | group_watch |
| `AccessScopePort` | permissionScopeList | capability_permission |

<a id="5-分波施工计划"></a>

## 5. Migration waves

Principle: **each wave moves code, compiles, and passes all existing tests**, without changing behavior.

<a id="wave-0--跨域解耦消-p0不动结构"></a>

### Wave 0 · Remove coupling across domains (resolve P0 without structural changes)

1. Split `b.mu` into `streamMu` and `InputRouter.mu`.
2. Add `ApplySettings(cfg)` to `GroupWatch` and remove direct access to `groupWatchSettingsMu` from `management_settings.go`.
3. First move `managementMu` unchanged into `ManagementState{ mu; ... }` (establish ownership without splitting semantics, enabling a later split).

Risk: low. Changes concentrate on field renaming and two method boundaries.

<a id="wave-1--零跨域-collaborator约-45-字段"></a>

### Wave 1 · Collaborators without cross-domain access (approximately 45 fields)

In descending order of independence, each as a separate PR:

1. `NativeSlashController` (9): one file, no cross-domain access.
2. `DocumentCommentController` (9): one file and `DocumentCommentAPI`.
3. `TurnCoalescer` (5): one file.
4. `CapabilityPermissionAuditor` (6): one file and `AccessScopePort`.
5. `BotCoordination` (5): two files in one domain and `ExternalBotPort`.
6. `MaterialDelivery` (4): three files in one domain and `ArtifactPort`.
7. `PermissionGateway` (3).
8. `SkillPicker` (2), `LarkLookupCache` (2), and `BotIdentityResolver` (2).

Use the same pattern for each:

```go
// Current
type Bridge struct { /* 9 nativeSlash* fields */ }

// Target
type nativeSlashController struct {
    mu        sync.Mutex
    once      sync.Once
    commands  map[string]command.NativeSlashCommand
    // ...
    api MessagingPort   // Depends only on ports, not on *Bridge.
}
```

`Bridge` retains one field, `nativeSlash *nativeSlashController`. Move the original method bodies; call sites change from `b.nativeSlashCommands` to `b.nativeSlash.commands` or use methods.

<a id="wave-2--streamcontroller--inputrouter--groupwatch约-16-字段"></a>

### Wave 2 · StreamController / InputRouter / GroupWatch (approximately 16 fields)

Depends on Wave 0. `StreamController` owns the stream lifecycle and subscription boundary. Invariants across methods such as `Subscribe`/`Freeze` must remain fully owned by the child struct.

<a id="wave-3--managementconsole22-字段"></a>

### Wave 3 · ManagementConsole (22 fields)

The largest and hardest part. Split it into three first:

- `ManagementSession`: `*Active` / version / maintenance state machines.
- `PackLibraryConsole`: packLibraryCards / nextPackLibraryView / managementPackActions.
- `BotSettingsTransition`: botSettingsMu / pendingBotSettings.

Keep `managementActions` / `managementSystemInfo` / `managementRestartDelay` / `managementCardPatch` as injected ports on `Bridge` or `ManagementSession`.

<a id="6-验收标准"></a>

## 6. Acceptance criteria

- [ ] `Bridge` has ≤45 fields (target ≈36: 14 DEP +8 ports +14 collaborator pointers).
- [ ] `Bridge` has no `sync.Mutex` / `sync.RWMutex` / `map` fields; collaborators exclusively own all mutable state.
- [ ] Each collaborator has exactly one lock (or an explicit set of locks), with no locking across file boundaries.
- [ ] All 26 bare `func` fields are replaced by 8 port interfaces.
- [ ] Each collaborator can be constructed and tested independently of `Bridge`; new `*_test.go` files do not need to assemble an entire Bridge.
- [ ] `go vet ./...` has no new warnings; `scripts/check.sh` passes.
- [ ] Existing `internal/bridge` tests pass **unchanged**, except for necessary construction/initialization adjustments.

<a id="7-非目标"></a>

## 7. Non-goals

- No changes to visible Feishu/Lark behavior or to the rules in [docs/current/feishu-bridge-product-model.md](../current/feishu-bridge-product-model.md).
- No rewrite of `bridgeDB`; splitting the 115 methods and migrations in `db_*.go` is a separate topic.
- No changes to string routing in `Hub.Route`; see the separate table-driven tunnel refactoring.
