# Local 专家自然语言编辑方案

[English](local-expert-natural-language-authoring-design.md) | [简体中文](local-expert-natural-language-authoring-design.zh-CN.md)

> 本文随公共核心维护，源码路径相对于公共仓库根目录。

状态：**自然语言编辑与显式触发的轻量评测已实现（仅 Feishu Bot owner）**。

本文描述 managed `local/*` 专家库的自然语言编辑入口。既有 local Git、Contract 校验、revision CAS、
不可变物化和 registry 原子切换保持不变；本期只增加 owner-only 的自然语言入口并复用既有发布路径。

## 1. 用户体验

用户直接在当前 Feishu 对话里创建或修改 local 专家：

```text
用户：把 local/reviewer 改得更关注并发安全
Agent：展示修改摘要和 diff

用户：锁相关内容少一点，增加幂等性检查
Agent：展示调整后的最新 diff

用户：评测一下修改效果
Agent：用 2 个代表性问题分别测试正式版和候选版，展示检查与盲比结论

用户：确认发布
Agent：发布并返回新 revision
```

同样支持：

- 从默认模板创建新的 `local/<name>`。
- 基于 `builtin/<name>` 创建一个独立的 local 专家。
- 修改已有 local 专家的 Profile、prompt 和 Skill 文件。

用户不需要进入单独的编辑任务，也不依赖卡片。private/group、thread/singleton 使用同一套自然语言
流程。只有用户明确确认后才产生正式 Git commit。

## 2. 最小状态模型

amux 不持久化可继续编辑的 Candidate、Draft 或候选版本。编辑中的 patch 只存在于当前对话的 Agent 上下文：

```text
正式 revision
  → Agent 读取必要文件
  → Agent 根据自然语言生成统一 Git patch
  → amux 临时应用并返回真实 diff + 签名 preview receipt
  → 用户明确要求且满足副作用确认规则时，可为同一 preview tree 生成短期隐藏 Profile，完成 A/B Task 后清理
  → Agent 可继续调整 patch 并重新预览
  → 用户在后续消息中确认
  → amux 用同一 patch + preview receipt 发布一个 commit
```

评测 Profile 只是不可编辑的运行时快照，不是 draft：没有名称选择、版本列表、恢复、合并或继续修改能力，
不更新 local 正式分支/revision，默认隐藏，并在显式清理或 30 分钟过期后删除。因此提交前仍没有
v1/v2/v3 或多候选同步问题。若对话上下文丢失，
重新读取正式版本并生成 patch 即可；正式库不会受到影响。

## 3. 支持范围

| 操作 | 基础内容 | 结果 |
| --- | --- | --- |
| `create` | 标准 local 默认模板 | 新建 `local/<name>` |
| `fork` | 只读 `builtin/<name>` snapshot | 新建独立的 `local/<name>` |
| `update` | 已有 `local/<name>` 的当前 revision | 更新该 local 专家 |

一次 patch 可以同时新增、修改或删除：

- Profile 与 manifest 配置。
- instructions、prompt 和其他文本资产。
- Skill 的 `SKILL.md`、`references/`、`scripts/` 以及 `assets/` 中的文本文件。

authoring patch 只支持文本文件；binary patch 会被拒绝。fork 时 builtin 中已有的二进制资产可以原样保留，
但本期不能通过自然语言 patch 新增、修改或删除二进制文件。

Skill 直接修改 Pack 中的目标文件，不做 copy-on-write，也不分析共享引用影响。同一个 Skill 被多个
local Profile 引用时，这些 Profile 的新 session/task 都会使用发布后的同一份 Skill。

本期不包含删除整个专家、自然语言 history/restore、自动生成下一轮优化或自动发布。隐藏诊断 CLI 继续用于机器侧
诊断和恢复，不作为普通用户编辑入口。

## 4. 两个工具

对 Agent 只暴露两个 MCP 工具：

### `amux.local_expert_read`

读取 builtin 或 local 的当前 revision 和必要文件，返回 bounded、可分页的结构化结果。它不修改状态。

### `amux.local_expert_change`

统一处理预览和发布，主要参数为：

```text
mode          preview | publish | cleanup
operation     create | fork | update
target_ref    local/<name>
source_ref    fork 时的 builtin/<name>
base_revision
patch         unified Git patch
preview_receipt  publish 时必填
prepare_evaluation  preview 时可选
```

输入 Schema 按 mode 强制要求完整字段：preview 必须携带 `operation`、`target_ref`、`base_revision`、`patch`；
publish 还必须携带 `preview_receipt`；cleanup 必须携带 `preview_receipt`；fork 额外必须携带 `source_ref`。

`preview`：

1. 从默认模板、builtin snapshot 或 local `base_revision` 创建临时 worktree。
2. 应用 Agent 提交的统一 Git patch。
3. 执行 Agent Pack Contract 校验。
4. 返回规范化的真实 `git diff`、结果 tree hash 和签名 `preview_receipt`；receipt 绑定 session、
   preview turn、operation、target/source、base revision、patch digest 和 tree，默认 30 分钟失效。
5. 默认删除临时 worktree，不保存运行时候选，也不产生正式 commit。
6. `prepare_evaluation` 默认 false；普通创建、修改或优化请求不得把它设为 true。只有 owner 对当前候选明确要求
   评测，且目标 Profile/Skill 本身或 case 可能产生写入、通知或其它外部副作用时已经另行确认测试计划，才额外
   把同一 tree 物化为隐藏的 `local-eval-*-dev/<name>` 临时 Profile；即使 case 看起来只读，只要目标本身涉及
   这些能力也需要确认。它不更新 local 正式 ref/revision，30 分钟过期。

`publish`：

1. 在新的临时 worktree 中重新应用同一 patch。
2. 重新执行 Contract 校验并计算 tree hash。
3. 校验 `preview_receipt`，并要求请求中的 operation、target/source、base revision、patch digest，以及重新应用后的
   tree 与 receipt 完全一致；任一字段不匹配时返回具体字段原因。
4. 要求正式库仍处于 `base_revision`。
5. 创建一个 commit，并复用既有 CAS、不可变物化、registry 原子切换和 last-known-good 流程。

`cleanup` 校验当前 owner turn、session 与同一 preview receipt 后，删除 receipt 绑定的评测 Profile；它不会
撤销 receipt。publish 成功也会 best-effort 清理。遗留候选由 daemon 定期按 TTL 回收。receipt 有效期内的中间
普通对话也不会撤销确认；任意后续 owner turn 都可携带完全相同的参数发布，但 preview 所在 turn 不能立即发布。

这是一个小接口、深实现：Agent 负责理解自然语言和维护当前 patch；amux 只负责读取、验证和原子发布。
不需要 `LocalExpertCandidate`、`LocalExpertDraft` 或专门的候选数据库；临时 Profile 只复用现有 Pack registry。

## 5. 为什么不让 Agent 直接写正式 Git

Agent 可以使用 Git patch 语义，但不能直接更新正式
`$AMUX_HOME/managed-agent-packs/local/origin.git`：

- 当前 session 可能运行在远端 amuxlet，Agent shell 不一定在安装 local 库的主机上。
- 直接写 Git 会绕过用户确认、Contract、revision CAS 和 registry 原子切换。
- 工具可以限制 clean Pack 相对路径，并拒绝绝对路径、`..`、`.git`、symlink 和越界访问。

因此 Git 仍是底层事实来源，但所有正式写入通过 `local_expert_change(mode=publish)` 完成。

## 6. 自然语言编排规则

`amux-skill` 负责以下简单流程：

1. 调用 `local_expert_read` 获取必要上下文。
2. 根据用户要求生成 patch。默认调用 `local_expert_change(mode=preview)`，只展示真实 diff；“优化”本身不代表
   用户要求评测。
3. 只有用户对当前候选明确要求“评测 / 测试 / 验证修改效果 / 对比新旧版本”时才准备评测。先结合目标 Profile、
   Skill 本身、可用工具和拟用 case 判断副作用：只有目标本身不涉及也不会自然触发外部动作，并且 case 是可确认
   的纯文本推理或只读分析时才可继续。只要目标本身涉及写文件/配置/数据库、创建或修改外部对象、发布/部署、
   发送消息/邮件/通知、调用改变外部状态的 API，无论 case 看起来是否只读，都先展示 case、目标环境、可能动作
   和影响，并等待用户在后续消息明确确认测试计划；case 本身有副作用或无法确认只读时同样处理。未确认前不得
   创建候选或启动 Task。
4. 评测授权满足后，用同一 patch 重新 preview 并传 `prepare_evaluation=true`，再选择 1–3 个能代表用户目标的
   case；用相同 prompt、model、permission、isolation 和结果粒度，分别通过
   `task_run` 执行正式 Profile 与工具返回的候选 Profile。先做格式/关键字等确定性检查，再隐藏 A/B 身份
   判断哪个结果更符合目标；不把“必须调用某 Skill”等编排提示写进业务 case。两边没有共同 model 时
   标记不可比；主观判断交给一个只看到随机 A/B 输出、不看到 ref/diff/调用顺序的独立 ephemeral judge Task。
5. 调用 `local_expert_change(mode=cleanup)` 清理临时候选；失败如实说明，TTL 继续兜底。
6. 向用户展示精简评测报告和服务端真实 diff。评测失败或超时不能包装为通过，也不能自动发布。
7. 用户继续修改时，Agent 更新当前 patch 并再次 preview，不创建候选版本。一次评测完成或 patch 变化后，之前
   的明确评测要求与测试计划确认都视为已消费；每次重新评测都需要用户对新候选提出新的明确要求，涉及副作用
   时还要重新展示计划并取得后续确认。
8. 测试计划确认只授权当前候选的本次评测，不能替代发布确认。只有用户在后续消息中明确确认同一 diff，才用
   同一 patch 和 `preview_receipt` 调用 publish。

若用户切换目标、对话丢失 patch、正式 revision 已变化或 tree hash 不一致，就重新读取并预览；不能猜测
或静默覆盖。

## 7. 当前 turn 授权

local 专家库属于一个 amux installation，并由其中多个 Feishu Bot 共享；发布权限属于当前 Bot Profile
的 owner。owner 在 private/group、thread/singleton 中都可以使用，非 owner、外部 Bot 和自动输入拒绝。

授权绑定当前输入 turn，而不是永久绑定 session：

```text
Bridge 验证当前消息 sender 是当前 Bot owner
  → 通过可信 hub route 附带结构化 turn grant
  → reverse tunnel 附加不可由直接 gRPC 请求伪造的进程内 provenance
  → runtime 将 grant 绑定到本次 TurnID
  → MCP 发布时回查当前授权 turn
  → turn_done / error / interrupt 后失效
```

`read`、`preview` 和 `publish` 都使用同一 owner turn 校验。模型不能自行填写 owner 身份或生成授权。
用户的预览请求和后续确认必须属于不同 turn，避免 Agent 在一次请求中自行生成并立即发布。

发布同时校验：

1. MCP 调用必须携带属于当前 session 的 delegated Agent credential；Feishu 会话即使使用 legacy-host
   execution，也会在启动时 best-effort 获得、在 restart 时轮换 session-scoped credential，不会退化为
   只信任 `AMUX_SESSION_ID`。签发失败时普通会话仍可运行，但该 session 的 local authoring fail closed。
2. 当前活跃 turn 拥有 local authoring grant。
3. 签名 receipt 中的 session、preview turn、operation、target/source、base revision、patch digest 和 tree 与发布请求完全一致，
   且 publish 发生在后续 owner turn。
4. 当前正式 revision 仍等于 `base_revision`。

多个 owner 或 session 可以并行预览；revision CAS 保证它们不能静默覆盖彼此的正式提交。

## 8. 发布与生效

```text
patch + confirmed preview_receipt
  → turn owner 校验
  → 临时 worktree 重放
  → Contract + tree hash + base revision 校验
  → 一个 Git commit
  → revision CAS
  → 不可变 revision 物化
  → registry 原子切换
```

任何一步失败都继续使用 last-known-good revision。fork 和 create 也只产生一个正式 commit。

新 revision 只影响新 session/task 或显式 `restart --refresh-profile`。发起修改的当前 session 继续使用
启动时冻结的旧 revision，避免专家在执行中修改自己后立刻改变行为。

## 9. 实现落点

| 位置 | 改动 |
| --- | --- |
| `docs/current/*.md` | 实现前同步 Agent Profile、Distributed Task 和 Feishu 产品规则 |
| `internal/agentprofile` | read、临时 worktree、patch、Contract、tree hash 与现有发布流程适配 |
| `proto/amux/v1/amux.proto` | current-turn grant 和 read/change RPC |
| `internal/bridge/router.go` | 从已认证 Feishu 消息生成 turn grant |
| `internal/session`、`app/runtime` | 保存、查询并在 turn 终态清除 grant |
| `app/hub` | 鉴权并在 installation 主机执行 local read/change |
| `app/cli/mcp.go` | 两个 MCP 工具、候选引用及 bounded structured result |
| 发行版提供的自然语言 authoring Skill | 自然语言预览、A/B Task、调整、确认发布流程 |

不新增公开 CLI 命令。实现修改 proto 后执行 `make proto`；代码改动提交前执行完整
`GOCACHE=/private/tmp/amux-go-cache scripts/check.sh`。

## 10. 验收

- create、fork、update 都能 preview，preview 不落盘、不产生 commit。
- 一个 patch 可以联合修改 Profile、prompt 和多个 Skill 文本文件；binary patch 必须拒绝。
- 多轮自然语言调整只重复 preview，不产生服务端候选版本。
- 普通创建、修改或优化请求默认不评测；只有用户明确要求时才准备候选。
- 只要目标 Profile/Skill 本身涉及写操作、通知或其它外部副作用，无论 case 看起来是否只读，都必须先展示计划
  与影响并取得用户后续确认；case 有副作用或无法确认只读时同样处理。
- 明确评测要求与计划确认只授权当前候选 patch 的本次评测；每次重新评测都需要新的明确要求，涉及副作用时还要
  重新确认计划。评测确认不等于发布确认。
- 获得授权的评测使用与 preview receipt 完全相同的 tree；正式/候选使用相同 case 与执行参数。
- Local 正式版与候选版的 Skill 文件随 Task snapshot 传输，远端 runtime 也必须投影同一内容。
- 没有共同 model 时不做 A/B；主观比较必须由身份剥离的独立 judge Task 完成。
- 临时候选不进入普通 discovery/飞书专家库，cleanup 或 TTL 后删除；不会改变 local 正式 revision。
- 评测失败、超时或候选启动失败不发布，且报告真实状态。
- 未确认、receipt 失效、patch/tree 不一致或 base revision 变化时不能发布。
- owner 在 private/group、thread/singleton 中都能发布；其他输入不能继承授权。
- publish 只产生一个正式 commit，失败时保留 last-known-good。
- 当前 session 保持旧 snapshot，新 session 使用新 revision。
- `amux update` 更新 builtin，但不覆盖 local。

## 11. 安全边界

本方案在控制面同时校验 trusted tunnel provenance、session-scoped Agent credential、当前 owner turn grant
和 preview receipt。普通 amuxd Submit 没有 grant 字段，直接调用 amuxlet Submit 即使伪造内部布尔字段也会
被拒绝；local authoring RPC 缺少或错用 Agent credential 时同样 fail closed。

该边界仍不承诺隔离同一 Unix 账号下能够读取其它进程环境或窃取 bearer credential 的恶意进程。若未来
要求 hostile-agent 安全，需要将 Agent 放入独立系统身份/强隔离 backend，并把正式 local Git 写权限收口到
独立 daemon；这不改变本文的两工具交互和发布事务。
