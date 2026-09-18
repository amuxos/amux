# Agent 记忆机制调研

[English](2026-09-16-agent-memory-research.md) | [简体中文](2026-09-16-agent-memory-research.zh-CN.md)

调研日期：2026-09-16。用于 [amux 按需记忆设计](../agent-memory-design.zh-CN.md)，不是当前产品行为说明。范围包括本机 OpenWolf 1.0.4 / `<workspace>/.wolf`、OpenClaw 官方文档与固定版本源码、Claude Code 官方文档、Hermes 官方文档与固定版本源码，以及 Codex 官方文档和本机 0.153.4 导出的协议 Schema。未运行下载的外部项目代码；仅调用已安装 Codex 导出协议，没有启动模型会话，也未读取或修改用户原有记忆正文。

## 1. 结论

记忆存储与模型上下文是两个不同层次。文件或数据库可以保存很多内容；只有被自动加载或工具读取的部分才进入模型上下文。使用文件 Read/search 也会消耗 tokens，但不要求每轮把整个库或相关快照附在用户消息前。

更适合 amux 的共同模式是：短使用说明、小入口、按需工具读取、及时写回。Hub 管存储、隔离和工具；Agent 判断什么时候需要历史。每轮自动查询和增量注入不是记忆成立的必要条件。

不能把所有系统描述为“完全不注入”：Claude 自动加载小索引，Hermes 注入小型核心快照，OpenClaw 按 harness/能力有多条路径，也有精选与触发式召回的例外。应借鉴分层与按需原则，明确 amux 自己选择的默认行为。

## 2. 对比

| 系统 | 常驻或启动信息 | 大量记忆怎么读 | 怎么写 | 对 amux 的启示 |
| --- | --- | --- | --- | --- |
| OpenWolf 1.0.4 | 通过 AGENTS/CLAUDE 入口加载操作协议；协议要求按场景读 cerebrum/anatomy | Agent 文件读取、Grep；hooks 会给相关提醒 | Agent 写 Markdown/JSON；hooks 记部分操作，daemon 可整理 | 能力可以来自协议与工具，不必把全部记忆做 prompt projection。 |
| OpenClaw 当前原生 Codex | memory 工具可用时，以小段 workspace-memory 指引代替 MEMORY.md 正文 | memory_search + memory_get 指定片段 | 工作中写 workspace Markdown；可选/默认启用的生命周期整理依具体功能配置 | 最贴近 amux 原生 Agent 的按需工具接入。 |
| OpenClaw 其它 harness | 有界 USER/MEMORY 等 bootstrap，存在运行模式差异 | daily/topic 文件主要按需检索 | 工作中写入、压缩前 flush、后台 dreaming | 小核心可加载，大历史保持在库外；不要照搬整个运行时和后台模型。 |
| Claude Code auto memory | 启动加载 MEMORY.md 小索引 | 主题文件用标准文件工具按需读 | Agent 自主创建/修改主题文件与索引 | 索引只存一句概述，详细内容留专题；索引需要真实容量限制。 |
| Hermes 内置记忆 | 小型 MEMORY.md/USER.md 快照，正常轮次冻结 | session_search 查 SQLite 历史，skills 按需加载 | memory add/replace/remove；满足条件可后台 review | 分清普遍偏好、历史和操作方法；保持固定前缀。 |
| Codex 原生 local memories | 启用后可向后续会话注入本地记忆 | 官方文档未在本次核查中确立所有版本的精确加载体积 | 后台从符合条件的闲置历史线程提炼 | 需同时治理原生读取、生成资格及 CODEX_HOME，避免与 Hub 成为两个自动来源。 |

## 3. OpenWolf：协议、文件与 hooks

本机 `/opt/homebrew/lib/node_modules/openwolf/package.json` 确认为 `openwolf@1.0.4`，项目地址为 [cytostack/openwolf](https://github.com/cytostack/openwolf)。以下以已安装代码和用户实际 `.wolf` 为准，不推断远端最新版本与本机完全相同。

- `<workspace>/AGENTS.md` 只引用 `.wolf/OPENWOLF.md`，并提醒读 cerebrum/anatomy；没有将 memory、buglog 等全部文件嵌入入口。
- `.wolf/OPENWOLF.md` 要求生成代码前检查 cerebrum、修 bug 前读 buglog、优先使用 anatomy 描述、同会话不要重复读取未修改文件；并明确建议定向 Grep 和追加时不全量读日志。
- `hooks/session-start.js` 在本地读 cerebrum 来检查条目数与文件新鲜度，给 Agent 的是提醒；并非把读到的整份内容输出到模型。
- `hooks/pre-write.js` 用规则匹配 Do-Not-Repeat 和同文件的相关历史 bug，输出少量提醒，不是每轮全库检索与全量注入。
- `post-write.js` 更新文件索引、追加日志并启发式记录修复；语义偏好、重要决定的提炼主要依赖 Agent 主动写文件。

安装包还包含 daemon 整理能力：`src/templates/cron-manifest.json` 提供日常 consolidation 和每周 cerebrum reflection。`dist/src/daemon/cron-engine.js` 的普通 consolidation 主要把旧 Session 日志压成动作数，不应宣传为智能总结；AI reflection 才会显式读取指定文件并调用 `claude -p`。存在这段代码不等于本机该 daemon 或任务正在运行，本次没有进行运行状态验证。

因此用户认为“大部分记忆无需直接注入提示词”有代码依据，但实际 Read 返回和 hook 提醒仍会成为模型输入；也不能把 OpenWolf 说成所有记忆自动提炼且零 token。

## 4. OpenClaw：必须区分 harness 和版本

源码核查固定为 [67adae4b098f5bd84f9ccf069a2076d073c7b79b](https://github.com/openclaw/openclaw/tree/67adae4b098f5bd84f9ccf069a2076d073c7b79b)。在线文档在调研日读取，后续可能更新。

官方 [Memory overview](https://docs.openclaw.ai/concepts/memory) 把 `USER.md` 定位为小型用户模型，`MEMORY.md` 定位为精选长期信息，`memory/*.md` 定位为详细笔记；详细笔记通过 search/get 查询。文档还明确磁盘文件过长时仅裁剪注入副本，不删除磁盘正文。

更具体的 [System prompt / Workspace bootstrap injection](https://docs.openclaw.ai/concepts/system-prompt#workspace-bootstrap-injection) 说明：原生 Codex 在 memory 工具可用时不反复粘贴 MEMORY.md，而给一个小的 workspace-memory note，引导调用 `memory_search` / `memory_get`。工具不可用时有有界正文 fallback。非 Codex harness 仍有正常 bootstrap 注入。因此只读 overview 的“启动加载 MEMORY.md”会漏掉当前 Codex 的区别。

源码可定位到 [Codex 的实际分流](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/codex/src/app-server/attempt-context.ts#L222) 和 [workspace-memory 短提示](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/codex/src/app-server/attempt-context.ts#L885)。提示明确将 MEMORY.md 视为记忆文件，相关时经工具读取。独立的 [Memory Recall 工具协议](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/memory-core/src/memory-tool-contract.ts#L88) 要求回答历史工作、决定、偏好等内容前先 search、再 get needed lines，没有要求普通消息每轮检索。

`memory/*.md` 不属于普通 turn 的 bootstrap Project Context；裸 `/new` 或 `/reset` 可把最近日记作为一次性启动上下文。导入自其它编码助手的记忆也作为单独可检索文件保存，不默认并入 bootstrap MEMORY.md。

[Memory search](https://docs.openclaw.ai/concepts/memory-search) 描述关键词与语义混合检索。当前版本还存在范围较窄的 deterministic trigger recall，不能据此声称所有正文永远只经显式工具读取；这是 amux 可选增强，非本次默认方案前提。

写入与整理方面：

- 平时由 Agent 把有价值内容保存到 Markdown。
- [Automatic memory flush](https://docs.openclaw.ai/concepts/memory#automatic-memory-flush) 在支持的运行路径上，于压缩前运行专门的提醒/保存 turn，使用私有会话副本；需要可写 workspace，实际会有模型调用成本。[当前跳过条件](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/src/auto-reply/reply/agent-runner-memory.ts#L1294) 包括 CLI runtime 和自行拥有原生压缩的 runtime，因此不能声称原生 Codex 普遍享有该 hook。
- [session-memory hook](https://docs.openclaw.ai/automation/hooks/bundled-hooks#session-memory) 在 new/reset 等边界保存近期用户/助手文本摘录；默认不调用模型生成文件 slug。它保存的是摘录，不应与语义反思混称。
- 当前 [Dreaming](https://docs.openclaw.ai/concepts/dreaming) 已提供后台筛选、合并和提升，不应沿用旧文章“完全没有后台整理”的描述。它有自己的来源筛选、调度与额外计算，amux 一期无需一并引入。

OpenClaw 的 agent/workspace/session/tool 可见性与 amux 的 EffectivePrincipalID 模型不同。[bootstrap 过滤](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/src/agents/workspace.ts#L1379) 会在群聊等场景排除根 MEMORY.md；但不向 prompt 注入某文件不等于文件系统具备多租户 ACL。不能把共享 workspace 目录直接视为 amux 的“同用户跨 Runtime”授权实现。

## 5. Claude Code：小索引，主题正文按需读取

依据 [How Claude remembers your project / Auto memory](https://code.claude.com/docs/en/memory#auto-memory)：

- 每个 repo 的 auto-memory 目录包含 MEMORY.md 索引及主题文件；同 repo worktree 共享，原生能力是 machine-local。
- 启动加载索引的前 200 行或 25 KB（先达到者）；主题文件不在启动时自动加载，需要时用标准文件工具读取。
- 文档建议每条索引一行，将详细内容移动到主题文件；写入接近/超过索引读取上限时提醒整理。
- Agent 判断哪些信息未来有用，不要求每个 Session 都写记忆。偏好、反馈、难以从代码推导的项目背景与外部参考是典型内容。
- `@path` import 会在启动时展开，被 import 的文本仍占上下文；把大文件拆成多个 import 不等于按需加载。

amux 应借鉴小入口和显式读取，保留自己的 Hub 存储来满足跨 Runtime 共享；直接使用 Claude 本机 auto-memory 目录不能满足用户需求。

## 6. Hermes：小核心冻结，历史与技能按需读取

核查 [官方 Persistent Memory 文档](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory) 及固定源码 [2e320e6d1a81bae96ac9b7ccf07d10c92120ca94](https://github.com/NousResearch/hermes-agent/tree/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94)。

- 内置 MEMORY.md 默认 2,200 字符、USER.md 默认 1,375 字符；官方的 tokens 是粗估，不是精确上限。
- [MemoryStore](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool_store.py#L67) 分 live entries 与 system-prompt snapshot；[格式化输出读取冻结快照](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool_store.py#L363)，中途写文件不改正常轮次的前缀。
- [system prompt 构建](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/system_prompt.py#L632) 区分稳定/context/volatile 段；[压缩后重建](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/system_prompt.py#L707) 会重读记忆。文档“session 内冻结”需结合源码理解为正常轮次冻结、自然重建边界刷新。
- [session_search](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/session_search_tool.py#L1) 按需读 SQLite FTS5 历史，支持检索与有界展开；检索本身不需要另一轮 LLM，但返回正文仍占上下文。
- [memory 工具说明](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool.py#L262) 将普遍适用事实放 memory，任务内 procedure/pitfall 放 skill，任务进度与完成日志留历史。
- [后台 review](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/background_review.py#L1) 可以在条件满足时从主对话分叉提炼，但有额外模型成本，不是每轮无条件动作。
- [context breakdown](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/context_breakdown.py#L121) 单独展示 memory 分类的本地估算，与 Provider 实际 usage 区分。

Hermes 内置文件以 home/profile 为单位，文档不建议多个 Agent 进程共享同一 home；外部 provider 还有其它能力。因此 amux 应借鉴分层和前缀稳定性，由 Hub 串行校验写入并按 Principal 分目录，不直接同步或并发写同一个原生 home。

## 7. Codex 原生记忆：独立存储不等于没有冲突

依据 OpenAI Docs 的 [Memories](https://learn.chatgpt.com/docs/customization/memories)、[Configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) 和 [App-server](https://learn.chatgpt.com/docs/app-server)，并对照本机 `codex-cli 0.153.4` 的协议生成结果。

### 已核实的原生行为

- local Codex memories 与 ChatGPT 网页记忆分开。目录默认是 `~/.codex/memories/`，由 CODEX_HOME 决定实际位置；文件是生成状态，官方不建议把手动改文件作为主要管理方式。
- 后台会从符合条件的旧会话提炼；跳过活跃或过短会话，并等待足够闲置时间。不能把“主 Agent 没有主动写 memory 文件”当作没有记忆写入。
- 当前官方默认 `features.memories=false`。启用后，`memories.use_memories`、`memories.generate_memories` 默认 true；不能据默认关闭推断所有用户都未开启。
- `use_memories=false` 跳过向未来会话注入既有记忆；`generate_memories=false` 让**新线程**不被保存为未来记忆生成的输入。后者的文档承诺没有覆盖所有旧线程迁移。
- `memories.disable_on_external_context` 默认 false。开启时排除用过 MCP、web search、tool search 等外部上下文的线程；默认不能据此断定 amux 返回的内容不会被提炼。它也不代替全体受管会话的禁生成策略。
- `/memories` 提供当前 chat 的使用/贡献控制，不改变全局设置。但交互命令存在不等于 app-server 中同名开关已具有可直接替代的协议语义。

因此冲突主要是两套来源给出矛盾偏好、工具结果被再次提炼、Hub 已删除的信息被本地副本重新带回，以及本地目录的身份边界不等于 Hub scope；不是两个工具使用相同名称导致的简单覆盖。

### 调研起点与后续实现的接点

按照官方建议运行 `codex app-server generate-json-schema --experimental --out <临时目录>`，本机 0.153.4 的 start/resume/fork 参数均含 `developerInstructions`、`baseInstructions` 和 `config`。使用追加 developer 指令来装配短协议，不能覆盖原生 base 指令。Schema 另含 `thread/memoryMode/set` 和 `enabled|disabled` 枚举，但未描述生成/读取/历史产物各自的语义；本次没有把接口存在当作运行时行为已验证。

调研起点的 [startOrResumeThread](../../internal/provider/codex/proc.go) 尚未传上述 instruction/config 字段；进程启动已保留额外 `-c` 参数，MCP 配置使用了该承载。后续本次实现增加了 [Codex 记忆接入](../../internal/provider/codex/memory.go)：启动覆盖、有效配置检查、合并 developer 指令及 `thread/memoryMode/set`。协议/单元测试不能替代对配置优先级、受管子 Agent、恢复及线程禁生成状态持久化的真实验收，也不能只写 prompt 要求“优先用 amux”就宣布兼容。

身份方面，调研起点的 [普通 Runtime 启动](../../app/runtime/server.go) 继承服务进程环境；[Linux DAC 环境处理](../../internal/isolation/backend.go) 改写 HOME/XDG，却未显式处理 CODEX_HOME。[Task private-home](../../internal/task/sandbox.go) 也存在这一边界，且它本身不是按用户的强隔离。如果服务环境显式设置了共享 CODEX_HOME，仅改 HOME 不足以证明 native 数据分区；是否可访问还受实际文件权限影响，不能据此断言发生了泄漏。本次实现随后为隔离启动重设 CODEX_HOME 并清理重复环境键；这修复启动边界，不替代文件权限和凭据 provisioning 的验收。

### 方案选择

启用 amux 记忆的受管 Codex 会话以 Hub 为唯一自动长期记忆来源，在创建线程前使用受支持的 launch 配置关闭 native 自动读取和本会话的生成资格。用户独立启动 Codex 时保持原来的策略；amux 不直接编辑、同步或删除用户已有原生记忆正文及全局配置，Codex 仍可持久化受管线程自身状态。来源切换用新原生会话；恢复旧线程必须验证其来源策略及持久禁生成状态，无法保证时不能把其当作已清空的受管会话。

固定短协议明确 `amux.memory_get("MEMORY.md")` 是工具中的逻辑文档，而非本机 MEMORY.md；记住/纠正/忘记均作用于 Hub。原生文件读权限仍由执行隔离保证，关闭自动记忆不是文件系统 ACL。这一策略也需要为其它 Agent 核查对应控制，不能推断所有 Provider 都已实现。

## 8. 对 amux 方案的取舍

推荐默认只安装稳定短协议与工具。Agent 在新原生会话读一次有严格容量限制的入口，后续遇到历史相关任务再 search/get；用户纠正或明确要求记住时及时写入。

按用户最新要求，amux 记忆直接使用 Hub 主机上的 Markdown 文件：非隔离存于 `memory/global/`，用户隔离存于 `memory/users/<principal-key>/`。search 在当前 scope 内进行有界的字面关键词扫描，get 按路径、行段或字节 offset 读取；不引入 SQLite、FTS5、向量服务或持久索引。中文按连续文本匹配，同义词依赖 Agent 换词查询。文件内容 hash 用于检测冲突，Hub 写锁加同目录原子替换保证单文件更新，不承诺多文件事务。绑定是 `global` 或 `principal:<effective-principal-id>` 的相等性前置条件，不含 `policy_revision`，不用于选择权限范围。CLI export 只导出一份文档并在分页时核对 revision。上文 Hermes 的 SQLite 属于调研事实，不是 amux 的选型。

这样无需逐轮相关性查询、已投递条目版本表、memory context epoch、累计 8k 后强制停读等机制；普通 turn 不因记忆库大小或 Hub 预检增加成本。它的代价是主动召回与沉淀依赖 Agent 遵守协议，需通过行为测试评估，后续才能决定是否添加有限的自动提醒。

按用户最新要求，不增加飞书记忆数量或 token 标签，也不引入对应独立计量链路。工具读回的正文属于 Provider 正常输入用量，现有 token 展示继续适用。

同一受管会话只启用一个自动长期记忆来源，并在接入层控制原生读写行为。仅把 amux 与 native 文件放在不同目录，或声明 amux 优先，不能避免后台重复收录和遗忘失效；旧会话及 CODEX_HOME 的实际身份边界必须纳入验收。
