# `internal/bridge` Ownership Split — 施工图

[English](2026-09-10-bridge-ownership-split-plan.md) | [简体中文](2026-09-10-bridge-ownership-split-plan.zh-CN.md)

> 从原仓库主干保留的设计记录。文中的日期、阶段、源码行号和实现判断属于记录当时；当前行为以 [CLI 产品模型](../current/cli-product-model.zh-CN.md) 及相关领域产品模型为准。
> 源码路径相对于公共仓库根目录；原 `cmd/amux`、`cmd/amuxd`、`cmd/amuxlet` 的实现分别迁至 `app/cli`、`app/hub`、`app/runtime`，`cmd/*` 仅保留发行入口。

状态：**阶段一已实施**（状态所有权 + 端口拆分）；方法迁移待后续
范围：`internal/bridge` 的 `Bridge` 结构体所有权重构
前置事实：D1/D3（2026-08-16）已完成**文件拆分**，本文只讨论其推迟的**所有权拆分**。

## 0. 实施状态（2026-09-10）

已完成并验证：

| 项目 | 前 | 后 |
| --- | --- | --- |
| `Bridge` 直接声明字段 | 125 | 44（+17 个嵌入协作者 + 8 个嵌入端口） |
| `Bridge` 上的 mutex/RWMutex | 14 | **0** |
| `Bridge` 上的状态 map | 10 | **0** |
| `Bridge` 上的裸 `func` 字段 | 26 | **0**（收进 8 个端口结构） |

- **Wave 0 完成**：拆分 `b.mu`（stream/seenMsgs）；`managementMu` 按子域拆为 maintenance/pack/settings 三把锁；`groupWatchSettingsMu` 收归 `GroupWatch.applyGroupWatchExecutionSettings`，settings 不再直接加锁。
- **Wave 1 状态所有权完成**：17 个协作者结构体（`streamController`、`inputRouter`、`turnCoalescer`、`permissionGateway`、`capabilityPermissionAuditor`、`groupWatchState`、`documentCommentController`、`botCoordinationState`、`nativeSlashController`、`botIdentityResolver`、`skillPickerStore`、`larkLookupCache`、`materialDelivery`、`managementSettingsState`、`managementMaintenanceState`、`managementPackState`、`botSettingsTransitionState`）通过嵌入持有各自状态与锁。
- **端口拆分完成**：26 个注入 func 收进 8 个端口结构（`ports.go`），嵌入后所有 `b.<port>` 访问点不变。
- 验证：`go build ./...`、`go test ./internal/bridge`、`make check-fast` 全绿。

已完成（阶段二，2026-09-10）：

- **69 个按域方法**已迁到协作者 receiver（`Bridge` 方法 397 → 328，协作者方法 69）。迁移判定：方法引用的 `b.<field>` 全部属于同一协作者，且其调用的 `b.<method>` 也归同一协作者。
- 嵌入提升保证外部调用点 `b.Method()` 不变；纯依赖（nil 检查）同步改为接收者。
- 验证：`go build ./...`、`go test ./internal/bridge`、`scripts/check.sh` 全绿。

剩余 328 个方法是跨域编排（引用 `db`/`client`/`hub`/端口，或调用多个域的方法），保留在 `Bridge` 是合理 facade 职责；90 个无状态辅助方法被多域共享，不适合归属单一协作者。两者均不阻碍后续按需继续下沉。

> 迁移机械说明：`Bridge` 字段改为嵌入协作者后，`b.field` / `b.field = x`（字段提升）与 `b.method()` 调用点**无需修改**；只有 `&Bridge{field: ...}` 复合字面量会失效，已用脚本改写为 `&Bridge{collab: collab{field: ...}}`。

## 1. 背景与现状

### 1.1 已完成的（D 系列，2026-08-16）

| commit | 内容 | 性质 |
| --- | --- | --- |
| `5ca1e37e` (D1) | `management.go` 6267 行 → 7 个 `management_*.go` | 纯代码移动 |
| `0dc62ae6` (D3) | `router.go` 2573 → 5 个 `router_*.go`；`db.go` 2827 → 4 个 `db_*.go` | 纯代码移动 |
| `7b35aad9` (D2) | `codex/proc.go` 按 concern 拆分 | 纯代码移动 |

D 系列 commit message 明确为 "behavior unchanged / pure code move"。

### 1.2 未完成的（本文范围）

D 系列**没有触碰 `bridge.go`**，`Bridge` 结构体所有权完全未动：

```
Bridge struct 字段数：  D1 前 130  →  D1 后 130  →  当前 125
（注：struct 声明占 144 行，含空行与注释；实际字段 125 个）
*Bridge 方法数：        396，分散在约 60 个非测试文件
*Bridge 同步原语：      29 个（mutex / RWMutex / WaitGroup / chan）
26 个注入用 func 字段
```

结论：文件布局已整理，但**认知复杂度、锁顺序、可测性未改善**；复杂度从"一个 6267 行文件"变成"60 个文件共享同一 receiver"。

## 2. 字段全量归属映射（125/125）

### 2.1 注入依赖 / 端口（40 个，不拥有可变状态）

`DEP`（14）——跨域共享的只读依赖，由 `Bridge` 持有并分发：

```
hub  sessionStarter  db  client  secrets  profile  bridgeID
isolationSupport  botE2EConfig  subtasks
sessionViewerBaseURL  sessionViewerToken  threadContext  promptRenderer
```

`PORT`（26）——当前是裸 `func` 字段，应抽成 8 个端口接口（见 §4）。

### 2.2 Collaborator（85 个字段）

| Collaborator | 字段数 | 字段 | 当前所属文件 | 独立锁 | 跨域污染 |
| --- | --- | --- | --- | --- | --- |
| `ManagementConsole` | 22 | managementMu, managementActions, managementSystemInfo, managementPackActions, managementRestartDelay, onboardingCardSentByOpen, maintenanceActive, maintenanceConfirmations, managementRecoveryRunning, documentSettingsActive, botToBotSettingsActive, groupWatchExecutionActive, permissionRecheckActive/Message/Version, permissionApplyActive/Message/Version, packLibraryCards, nextPackLibraryView, botSettingsMu, pendingBotSettings | management.go, management_*.go, bot_settings_transition.go | `managementMu`, `botSettingsMu` | 🔴 `managementMu` 跨 3 文件 |
| `StreamController` | 9 | mu, streams, streamTokens, streamReady, streamSessions, streamSessionID, streamFreeze, supersededTurns, nextStreamToken | stream.go | `mu` | 🔴 `mu` 被 router.go 复用 |
| `NativeSlashController` | 9 | nativeSlashMu/SyncOnce/Commands/TargetID/Session/Aliases/Skills/Syncing/SyncAgain | slash_commands.go | `nativeSlashMu` | ✅ 无 |
| `DocumentCommentController` | 9 | documentSourceMu, documentMu, documentStreams, documentStreamReady, documentTurns, documentTurnTimeout, documentCommentSemaphore, documentCommentsEnabled/Configured | document_comment.go | `documentSourceMu`, `documentMu` | ✅ 无 |
| `CapabilityPermissionAuditor` | 6 | capabilityPermissionMu, capabilityPermissions, permissionAuditChecked/Failed/Mu/Once | capability_permission.go | `capabilityPermissionMu`, `permissionAuditMu` | ✅ 无 |
| `TurnCoalescer` | 5 | turnMu, turnActive, coalesceMu, coalesced, coalesceDelay | coalesce.go | `turnMu`, `coalesceMu` | ✅ 无 |
| `BotCoordination` | 5 | botToBotSettingsMu, botToBotMu, botToBot, externalBotCatalogMu, externalBotCatalogSeen | bot_to_bot.go, external_bot_catalog.go | `botToBotSettingsMu`, `botToBotMu`, `externalBotCatalogMu` | ✅ 无 |
| `GroupWatch` | 4 | groupWatchMu, groupWatchSettingsMu, groupWatchConfig, groupWatchConfigChanged | group_watch.go | `groupWatchMu`, `groupWatchSettingsMu` | 🟡 settings 直接改 config |
| `MaterialDelivery` | 4 | artifactMu, artifactInFlight, imageNoticeMu, imageNotices | artifact_output.go, material_input.go, image_input.go | `artifactMu`, `imageNoticeMu` | ✅ 无（同域 3 文件） |
| `InputRouter` | 3 | routeMu, routeLocks, seenMsgs | router.go | `routeMu` | 🔴 复用 `mu` |
| `PermissionGateway` | 3 | permissionMu, permissions, resolvedPermissions | permission.go | `permissionMu` | ✅ 无 |
| `SkillPicker` | 2 | skillPickerMu, skillPickerClaims | skill_picker.go | `skillPickerMu` | ✅ 无 |
| `LarkLookupCache` | 2 | chatBotCountMu, chatBotCountCache | lark_api.go | `chatBotCountMu` | ✅ 无 |
| `BotIdentityResolver` | 2 | botIdentityMu, botIdentityCheckedAt | bridge.go | `botIdentityMu` | ✅ 无 |

## 3. 必须先修的跨域违规（P0）

这三处是"共享锁保护不相关状态"的典型，拆分前必须处理，否则只是把耦合搬进子结构体。

### 3.1 `b.mu` 同时保护 stream 状态与 `seenMsgs`

```
stream.go:225/242/342/373/386/1173   b.mu  保护 streams/streamTokens/streamReady/...
router.go:420                        b.mu  保护 seenMsgs
```

一个 mutex 覆盖两套无关 map，造成虚假竞争与隐式锁顺序。
**修法**：stream 用 `streamMu`，`seenMsgs` 归 `InputRouter.mu`。

### 3.2 `managementMu` 跨 `maintenance` / `pack` / `settings` 三个子域

```
management_maintenance.go  9 处
management_pack.go         9 处
management_settings.go    18 处
```

**修法**：按子域拆锁（`maintenanceMu` / `packMu` / `settingsMu`），或先把 `managementMu` 收进一个 `ManagementState` 结构体，由它独占该锁并提供原子访问方法。

### 3.3 `groupWatchSettingsMu` 被 `management_settings.go` 直接加锁改配置

```
group_watch.go:111              b.groupWatchSettingsMu.Lock()
management_settings.go:594      b.groupWatchSettingsMu.Lock()
```

**修法**：`GroupWatch` 提供 `ApplySettings(cfg)` 方法，settings 只调用它，不碰内部锁。

## 4. 26 个 func 字段 → 8 个端口

| 端口接口 | 方法（当前字段） | 使用方 |
| --- | --- | --- |
| `IdentityPort` | resolveExecutionIdentity | bridge（session 启动） |
| `MessagingPort` | threadNotice, chatNotice, dmNotice, dmInteractive, managementCardPatch, reactionAdd | management_settings, router_submit, capability_permission, completion_notification, document_comment, management_maintenance, router_workspace, bridge |
| `LookupPort` | topicRootLookup, quotedMessageLookup, chatNameLookup, chatBotCountLookup, botIdentityLookup | completion_notification, router_workspace, material_input, quoted_message, management_groups, lark_api, bridge |
| `DocumentCommentAPI` | documentCommentFetch, documentCommentReply, documentCommentReact, documentSubscribe | document_comment |
| `ExternalBotPort` | botHandoff, botHandoffPrompt, externalBotDelegation, externalBotCatalogFetch | bot_to_bot, router_workspace, external_bot_coordination, external_bot_catalog |
| `ArtifactPort` | artifactUpload, artifactSend, fileResourceFetch, imageResourceFetch | artifact_output, material_input, image_input |
| `GroupWatchAPI` | groupWatchListMessages | group_watch |
| `AccessScopePort` | permissionScopeList | capability_permission |

## 5. 分波施工计划

原则：**每一波都是纯搬迁 + 编译通过 + 现有测试全绿**，不夹带行为变更。

### Wave 0 · 跨域解耦（消 P0，不动结构）
1. 拆 `b.mu` → `streamMu` + `InputRouter.mu`。
2. `GroupWatch` 增加 `ApplySettings(cfg)`，移除 `management_settings.go` 对 `groupWatchSettingsMu` 的直接访问。
3. `managementMu` 先原样收进 `ManagementState{ mu; ... }`（不拆语义，仅建立所有权，便于后续再分）。

风险：低。改动集中在字段改名与 2 个方法边界。

### Wave 1 · 零跨域 Collaborator（约 45 字段）
按独立性从高到低，每个单独成 PR：

1. `NativeSlashController`（9）— 单文件，零跨域
2. `DocumentCommentController`（9）— 单文件 + `DocumentCommentAPI`
3. `TurnCoalescer`（5）— 单文件
4. `CapabilityPermissionAuditor`（6）— 单文件 + `AccessScopePort`
5. `BotCoordination`（5）— 同域 2 文件 + `ExternalBotPort`
6. `MaterialDelivery`（4）— 同域 3 文件 + `ArtifactPort`
7. `PermissionGateway`（3）
8. `SkillPicker`（2）、`LarkLookupCache`（2）、`BotIdentityResolver`（2）

模式统一：

```go
// 现在
type Bridge struct { /* 9 个 nativeSlash* 字段 */ }

// 目标
type nativeSlashController struct {
    mu        sync.Mutex
    once      sync.Once
    commands  map[string]command.NativeSlashCommand
    // ...
    api MessagingPort   // 只依赖端口，不依赖 *Bridge
}
```

`Bridge` 保留一个字段 `nativeSlash *nativeSlashController`，原方法体搬迁，调用点从 `b.nativeSlashCommands` 改为 `b.nativeSlash.commands` 或经方法访问。

### Wave 2 · StreamController / InputRouter / GroupWatch（约 16 字段）
依赖 Wave 0 完成。`StreamController` 持有 stream 生命周期与 subscribe 边界，需保证 `Subscribe`/`Freeze` 等跨方法不变量在子结构体内闭合。

### Wave 3 · ManagementConsole（22 字段）
最大、最难。建议先拆成三个：
- `ManagementSession`：`*Active` / version / maintenance 状态机
- `PackLibraryConsole`：packLibraryCards / nextPackLibraryView / managementPackActions
- `BotSettingsTransition`：botSettingsMu / pendingBotSettings

`managementActions` / `managementSystemInfo` / `managementRestartDelay` / `managementCardPatch` 作为注入端口留在 `Bridge` 或 `ManagementSession`。

## 6. 验收标准

- [ ] `Bridge` 字段数 ≤ 45（目标 ≈ 36：DEP 14 + 端口 8 + Collaborator 指针 14）。
- [ ] `Bridge` 上不再有任何 `sync.Mutex` / `sync.RWMutex` / `map` 字段；所有可变状态由 Collaborator 独占。
- [ ] 每个 Collaborator 恰好一个锁（或明确的锁集合），且不再跨文件加锁。
- [ ] 26 个裸 `func` 字段全部消失，替换为 8 个端口接口。
- [ ] 每个 Collaborator 可脱离 `Bridge` 单独构造并单测（新增 `*_test.go` 不再需要组装整个 Bridge）。
- [ ] `go vet ./...` 无新增告警；`scripts/check.sh` 全绿。
- [ ] `internal/bridge` 现有测试**零修改**通过（除必要的构造/初始化适配）。

## 7. 非目标

- 不改 Feishu/Lark 可见行为；不触碰 [docs/current/feishu-bridge-product-model.md](../current/feishu-bridge-product-model.zh-CN.md) 描述的规则。
- 不重写 `bridgeDB`（`db_*.go` 的 115 方法与 migration 拆分是独立课题）。
- 不改 `Hub.Route` 字符串路由（独立课题，见 tunnel 表驱动改造）。
