# 与 Agent Profile 无关的按需记忆

[English](agent-memory-design.md) | [简体中文](agent-memory-design.zh-CN.md)

状态：实施设计，修订于 2026-09-17。本次变更已实现存储、Hub RPC、CLI/MCP 和启动接入。
本文记录实现契约、已验证证据与待完成的验收，不代表真实模型行为和全部原生 Agent 生命周期已经验证。
产品行为以[当前记忆模型](current/agent-memory-product-model.zh-CN.md)为准，
调研依据见[记忆系统调研](reports/2026-09-16-agent-memory-research.zh-CN.md)。

## 1. 核心决定

有无 Agent Profile 都能使用记忆。一个 Hub 为整个 amux system 保存普通 UTF-8 Markdown
文件。非隔离执行使用共享全局记忆；用户隔离按认证后的有效 Principal 分区。同一 Principal
跨 Runtime、Agent 和工作区继续使用同一份记忆。不同 Hub 不自动同步。

默认关闭。飞书管理 → 实验功能 → Agent 记忆与
`amux system config set memory.enabled true|false` 使用同一个 Hub 配置。
保存并重启后系统级生效，远端 Runtime 和客户端无需另开本地开关。开启后新原生会话采用记忆；
关闭拒绝数据访问和新绑定，保留文件。已有绑定的会话恢复时保留固定协议和原生记忆抑制，
数据请求在关闭期间失败；重新开启且范围兼容后可继续使用。旧上下文无法撤回，关闭访问不应让
它被原生自动记忆再次提取；关闭期间新建会话使用 Agent 原有原生记忆政策。
MCP 启动时通过有超时限制的 Hub 请求检查能力，不逐轮查询。

Runtime 安装短而稳定的协议和工具。Agent 在新原生会话读取一次很小的逻辑 `MEMORY.md`
入口，之后仅在需要时搜索和读取主题笔记。Runtime 不逐轮检索、不插入变化的快照，也不跟踪
已投递记忆的版本。工具读取正文仍消耗模型输入 tokens；未读取的文件不进入模型上下文。

不引入记忆数据库、持久搜索索引、向量服务、后台反思模型、重复会话流水或逐轮检索。
现有 Timeline 仍保存执行历史。Agent 只将有价值的偏好、决定和已验证经验写入记忆。

## 2. 存储与内容

```text
<AMUX_HOME>/memory/                 # 位于 Hub 主机
  global/
    MEMORY.md
    preferences/communication.md
    projects/amux/decisions.md
  users/<sha256(principal-id)>/
    MEMORY.md
    projects/amux/decisions.md
```

安全目录键由服务推导，不能由 Agent 指定。全局和用户目录不会自动继承、合并或迁移。
所有 Runtime 调用同一 Hub，无需共享挂载或 Runtime 副本。一个记忆根目录只由一个 Hub
在线写入。离线检查、编辑和备份使用普通文件工具；在线修改通过 Hub API 或 CLI。

`MEMORY.md` 只保存少量普遍适用的偏好与入口，不列出所有文件。详细内容放主题文档。
项目路径表示适用范围，不是授权边界；不同机器上的目录 basename 相同不代表相同项目。
来源、日期和 Session/Turn 引用可以直接写进 Markdown，不维护额外元数据表。

`revision` 是整个文件的 SHA-256 hash，不是递增版本号。写入和删除必须通过
`expected_revision` 传入该 hash；新建使用 `absent`。Hub 在 scope 写锁内检查当前版本和
配额，在目标目录创建临时文件、同步文件、原子重命名并同步目录。删除同样同步父目录。
冲突返回当前观察到的版本，调用方重读后合并。

读取不存在的文档返回 `exists=false`、`revision="absent"`；删除已不存在文档返回
`already_absent=true`。写入持久化后才确认成功。RPC 结果不确定时需重读确认，不维护持久
幂等账本，也不承诺 exactly-once。多文件修改不是事务，搜索也不提供整个目录的快照。

## 3. 搜索与读取 API

| 工具 | 契约 |
| --- | --- |
| `amux.memory_search(query, path_prefix?, limit?)` | 在授权范围内扫描字面关键词并返回有界命中。 |
| `amux.memory_get(path, start_line?, end_line?, offset?)` | 返回有界 UTF-8 片段、整份文件的版本和续读信息。 |
| `amux.memory_upsert(path, content, expected_revision)` | 按条件替换完整文档，或以 `absent` 新建。 |
| `amux.memory_forget(path, expected_revision)` | 按条件删除单份文档，不清除会话历史。 |

搜索使用 Go 目录遍历和字符串匹配，不依赖外部 `rg`。查询按空白拆成字面关键词，英文忽略
大小写、中文匹配连续文本，不解释正则、glob 或 shell 语法。任一词命中的文档成为候选。
先按命中不同关键词的数量排序，再比较路径/标题命中，最后按路径稳定排序。结果包含相对
路径、片段、行号和版本，不调用 LLM 生成摘要。

默认返回五份命中文档，请求上限为二十份。`incomplete` 表示尚未扫描完整作用域即停止；
`scanned_files` 返回已扫描文件数；响应级 `truncated` 表示因条数或输出上限省略了命中。
每条命中还具有独立的片段 `truncated` 标记。扫描未完成时的空结果不能证明整个库无命中。
需要时缩小 `path_prefix` 或换关键词。同义词由 Agent 换词重试；不提供分词器、FTS5、
BM25 或 embedding 索引。

get 使用从 1 开始、包含末行的行范围。起始行填 0 或省略表示从开头读，末行填 0 或省略
表示读取到响应上限。被截断时，将 `next_offset` 作为 `offset` 续读，保持相同 path/end_line
并核对 revision。非零 start_line 和 offset 不能同时使用，不提供 section-name 参数。
逻辑路径必须是相对 Markdown 路径，拒绝绝对路径、穿越和符号链接；搜索不跟随符号链接。
Agent 不能选择别的作用域或 Principal。

等价 CLI 为 `amux memory search|show|upsert|forget|export`。search/show 返回有界 JSON。
upsert 从 `--file` 或 stdin 读取原始 UTF-8 Markdown，必须指定 `--expected-revision`；
forget 同样要求该选项。export 将单份完整文档读到标准输出，校验各页版本相同，发生冲突
则不输出正文；它不是整个库的归档接口。没有 MCP 的 Agent 使用 CLI。MCP 工具响应超过
单页预算时，继续使用现有通用 `amux.result_continue` 包装续传。

## 4. 固定协议与日常行为

实际协议源文件为 [memoryprotocol](../internal/memoryprotocol/protocol.go)，要求：

- 新会话读取一次很小的逻辑 `MEMORY.md` 入口，仍在上下文时复用；路径属于 amux 工具，
  不是本地文件。
- 回答以前的工作、决定、偏好、约定或重复问题前先搜索，仅读取相关片段。无需历史的普通
  任务不必搜索。
- 保存明确的“记住”请求、稳定偏好纠正和可复用的已验证经验；详细内容放主题，入口保持小。
- 更新前先读，携带版本；冲突后重读合并。只有工具成功返回才能声称已保存。
- 纠正或遗忘时同时更新入口和相关主题，清理重复的旧内容与引用；部分失败必须说明。
- 不保存秘密、临时授权和未经验证的猜测。当前指令与已验证事实优先，记忆不授予权限。

协议根据可用能力列出 MCP 工具或等价 CLI，不携带记忆正文、时间戳和逐轮检索状态。
它不改写仓库 AGENTS.md/CLAUDE.md，不依赖 `.wolf`，也不要求内置 Pack。
用户维护的项目说明仍属于工程指导。

一次 forget RPC 只删除一个文件。自然语言“忘记这个偏好”可能需要多次按条件修改。
删除文件无法抹除原生会话已读过的文本；需要离开旧上下文时新建会话。其它活跃会话下一次
读取时看到变更，服务不会把更新后的偏好主动推送给所有模型。

## 5. Runtime 接入与前缀复用

[Runtime 接入](../app/runtime/memory.go)只在新执行时解析一次绑定，保存到 Session 启动
状态，恢复时保留。不带绑定的旧会话不会静默采用当前记忆策略。发现失败会产生启动警告，
该会话不启用记忆，不阻断普通 Agent 工作，也不回退全局范围或创建 Runtime 临时记忆库。

| Agent | 本次变更的接入 |
| --- | --- |
| Codex | 将短协议与有效 developer 指令合并后传入 `developerInstructions`，保留原生 base 指令；要求支持 config 和 thread-memory-mode 调用。 |
| Claude | 将已有追加指令合并成一个 `--append-system-prompt`，仅在本次启动设置 `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`。 |
| Pi | 通过 `--append-system-prompt` 提供等价 CLI 协议。 |
| 其它 Provider | 明确报告缺少记忆能力，不增加逐轮 prompt 兜底。 |

同一原生会话的协议和工具 Schema 保持稳定。记忆正文以工具结果追加到会话中，修改存储
不会重写之前的消息或指令前缀。这避免由记忆变化引起前缀变化；实际缓存命中仍受 Provider
序列化、路由、TTL 和压缩影响。

压缩由原生 Agent 负责，压缩后丢失的相关上下文可以重新读取。不维护 memory context epoch、
逐条投递账本或累计读取 8k tokens 后停用的阈值。不增加隐藏 flush turn、影子会话或第二个
completion gate。提示协议不能保证 Agent 在崩溃前保存所有有用事实。

Linux DAC 每次隔离启动通过现有目标 UID materializer，将 CLI 与最小连接配置复制一次到
`<run>/agent-config/memory/`，无需开放服务账号的私有安装和配置目录。启动专属 PATH
和 MCP 配置指向该副本，常规 run 清理负责删除；成本是每次隔离启动复制一个二进制。
其它启动（包括 private-home Task）解析已安装的 CLI。找不到可执行文件时明确启动失败。
Claude sidecar 把合并后的追加指令映射到 SDK 支持的 `systemPrompt.append` 字段。

## 6. 容量边界

| 资源 | 当前限制 |
| --- | --- |
| `MEMORY.md` | UTF-8 2 KiB 且不超过 64 行 |
| 主题文档 | 64 KiB |
| scope | 1,024 份文档和 16 MiB |
| 搜索查询 | 512 字节、1–16 个空白分隔的词 |
| 搜索扫描 | 1,024 文件、8 MiB、250 ms；另有限制目录项数量的遍历预算 |
| 存储响应 | 8 KiB 序列化输出预算 |
| MCP 响应页 | 现有 6 KiB 预算，使用通用续传 |

这些是字节/行数限制，不是精确 token 计数。入口超限要求迁移详细内容到主题文档，不静默
丢弃重要条件。多次有价值读取仍会累积上下文，由 Provider 正常管理。飞书标题沿用现有
Provider 总用量，本次不增加记忆数量、token 标签或独立计量事件。

## 7. 身份与冻结的作用域

Hub 按配置的隔离政策和认证 actor 推导作用域。非隔离记忆为 `global`；隔离记忆为
`principal:<effective-principal-id>`。服务执行使用其有效 service Principal。
同一个人的 CLI/飞书 alias 需映射到同一 Principal 才能共享隔离记忆。
Task、Instance、Schedule 和 child 执行使用授权后的有效身份，而非任意 parent ID。

`AMUX_MEMORY_BINDING` 以非秘密字符串携带这一生命周期/相等性前置条件，不是认证信息
或作用域选择器，不包含 `policy_revision` 字段。授权边界仍由 Hub actor 凭据与隔离政策
共同决定。Hub 每次认证调用，拒绝不匹配的绑定。
冻结是受管 Runtime 的生命周期保证；该标记不是线程来源的密码学证明，不能阻止
有权限的自定义客户端主动请求当前被授权的范围。
委托 Agent 凭据必须携带受管绑定；普通已注册 CLI 调用方可省略，在其当前授权范围操作。
能够访问 Session 不代表能访问其所有者的全部记忆。

当隔离政策开启时，helper 健康、已连接 target 或 passthrough 降级都不能选择全局记忆。
身份缺失或无效时拒绝访问。切换隔离政策不复制文件，也不让旧上下文公开；不兼容绑定会
失败，必须新建会话。文件权限和实际执行隔离也必须阻止直接访问其它 Principal 的数据，
短协议不是 ACL。

## 8. 原生记忆兼容

[Codex 文档](https://learn.chatgpt.com/docs/customization/memories)说明了从符合条件的
旧会话后台提炼、向后续会话加载的机制。仅分开文件路径无法防止矛盾偏好、重复收录 MCP
结果或被删除事实从原生副本复现。因此受管 amux 记忆选择单一自动长期来源。

[Codex 接入](../internal/provider/codex/memory.go)只对受管启动和线程设置以下值，
不编辑全局配置：

```toml
[features]
memories = false
[memories]
use_memories = false
generate_memories = false
```

安装合并后的 developer 指令前先读取有效配置，要求返回值确认原生记忆已关闭；同时以
`disabled` 调用 `thread/memoryMode/set`，不支持该 API 时明确失败。已用 Codex 0.153.4 在
隔离目录执行离线探针，确认 `config/read` 返回三个记忆设置均为 false，`thread/start`
接受 `developerInstructions`，禁用线程状态持久写入原生状态；全程没有模型 turn。
该版本的[原生提炼选择器](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/state/src/runtime/memories.rs#L234)
要求线程处于 enabled 模式。协议测试覆盖请求与失败处理；这些证据不代表全部后台 worker
行为、原生子 Agent 继承或其它版本兼容性均已验证。

官方对 `generate_memories=false` 的配置承诺针对新线程。启动 flag 或 fork 不能自动将
不带绑定的旧会话变成干净上下文。amux 不迁移、导入、删除或改写已有原生记忆文件；
独立启动的 Codex 保持原有策略。切换记忆来源需要新原生会话，重放旧文本不能宣称已清除
之前读过的记忆。

Linux DAC 和 Task private-home 启动代码将继承的 `CODEX_HOME` 和 `PI_CODING_AGENT_DIR`
重设为对应 home 下的目录，清空 `CLAUDE_CONFIG_DIR` 保留隔离 home 的默认布局，并去除重复环境值。private-home 仍是弱隔离，不是按用户的文件系统 ACL。
凭据 provisioning 必须明确处理；复制服务用户整个 `.codex` 或回退共享 home 不是记忆
兼容方案。关闭自动记忆不能阻止读取仍可读的文件，也不能阻止自定义 hook/extension 写入
其它记忆。Claude 通过 `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` 关闭自动记忆及子 Agent
持久记忆；SDK 继承隔离后的 sidecar 环境，不再由隔离前的环境覆盖。
本次审阅的 Pi 核心没有内置自动长期记忆，保留普通项目指令、会话历史和扩展；自定义记忆扩展
仍需独立兼容评估。证据见[原生记忆兼容报告](reports/2026-09-17-native-memory-compatibility.zh-CN.md)。

## 9. 验收与待补证据

自动化验收覆盖文件存储、作用域、路径约束、版本冲突、配额、有界搜索/读取、CLI/MCP
转发、启动绑定、指令合并及原生控制请求/失败路径。现有进程 E2E 在完整门禁中分别使用
公共和内部二进制，通过独立 CLI 调用验证记忆 CRUD、搜索、分页导出和旧版本冲突拒绝。
Codex 0.153.4 离线探针验证了上文的配置、指令和模式持久化契约。真实模型行为仍需
单独验证：跨新会话明确记住/纠正/遗忘、无关 turn 不检索、原生 clear/resume/压缩，以及
原生子 Agent 的政策继承。离线探针没有执行模型 turn，mock 和 Schema 字段本身也不是
这些行为的证据。

冻结 diff 后通过仓库完整门禁验证公共与内部集成。本文本身不构成交付或运行时兼容声明。
未来增加语义搜索或有限自动提醒，需要实际规模/召回问题作为依据并单独决定；Markdown
文件继续保持权威来源。
