# Agent 记忆产品模型

[English](agent-memory-product-model.md) | [简体中文](agent-memory-product-model.zh-CN.md)

当前实现与 Profile 无关的记忆功能；已用 Codex 0.153.4 验证原生兼容契约，模型是否遵守协议仍取决于 Agent。

Hub 独立于 Agent Profile 和 Runtime 管理长期记忆。`memory.enabled` 是默认关闭的实验功能。
飞书管理的「实验功能」控制这个 Hub 级唯一开关；CLI 可执行
`amux system config set memory.enabled true`。保存并重启 Hub 后生效，远端 Runtime 无需另行启用。
记忆使用 Hub 的 `AMUX_HOME/memory` 下的 UTF-8 Markdown 文件，不增加记忆数据库或
持久搜索索引。非隔离使用 `global/`；Hub 用户隔离政策开启时，按认证后的有效
Principal 分到 `users/` 下的独立目录。Runtime 在线情况或 helper 就绪状态不能使
范围回退到全局。能访问 Session 不代表能访问其所有者的全部记忆；服务执行使用
有效服务 Principal 的记忆。

CLI 和 MCP 通过 Hub 搜索、读取、条件写入、遗忘和导出文档。路径相对于服务端确定的范围，
且仅允许安全的相对路径组件：拒绝绝对路径、路径穿越、符号链接逃逸、隐藏（点开头的）
或空路径组件，以及过长或非 UTF-8 的名称。搜索进行字面关键词匹配，返回有界片段；
达到扫描上限时报告结果未完整扫描。读取返回有界正文、续读信息和整个文件的 hash。
更新以该 hash 为前提，新建使用 `absent`。同范围写锁与同目录原子替换避免并发覆盖。
多个文件的修改不构成事务。删除文档不会抹除已经读取的会话历史或其它文档中的副本。

受支持的 Agent 启动时获得固定记忆协议和冻结的范围前提。新原生会话通过工具读取
小型 `MEMORY.md`（条目预算只约束这个精确文件名），其它内容按需读取；不逐轮自动召回或重写提示词。协议不依赖 Profile
投影。已有非受管原生会话不静默采用新的记忆政策。受管 Runtime 在会话内保持范围不变；冻结范围
不兼容时拒绝访问。绑定是非秘密的客户端前提，不是授权凭证；Hub 对每次请求独立鉴权。普通 CLI 用户可管理当前被授权的范围。

受管 Codex 会话通过仅本次启动生效的控制关闭原生自动记忆读取和生成。Claude 使用受支持的
环境开关关闭自动记忆及原生子 Agent 持久记忆。本次审阅的 Pi 核心没有内置自动长期记忆，
自定义记忆扩展需独立评估。其它 Provider 需要已验证的指令与原生记忆契约才启用 amux 记忆。不直接编辑或迁移用户全局配置和原生记忆文件。
隔离原生 home 不得继承共享的 `CODEX_HOME`、`CLAUDE_CONFIG_DIR` 或 `PI_CODING_AGENT_DIR` 覆盖。读取片段已包含在 Provider 输入用量中，
飞书标题不增加记忆计数。

记忆要求 Runtime 上有可执行的 amux CLI。Linux DAC 每次启动通过现有目标 UID
materializer，把 CLI 和最小 Hub 连接配置复制到当前 run 的 `agent-config/memory/`；
shell 和 MCP 使用该副本，由常规 run 清理回收。其它启动使用已安装的 CLI。
缺少工具时在启动 Agent 前明确报错。

关闭实验功能后，Hub 拒绝全部记忆数据 RPC 和新绑定，保留已保存文件。已有会话（包括恢复）
保留冻结的协议和原生自动记忆抑制，避免关闭访问后其历史被原生记忆重新提取；Hub 开关关闭期间
不能读写 amux 记忆。MCP 启动时查询一次 Hub 能力，各数据请求仍由 Hub 检查当前政策。重新开启
后，范围兼容的已有绑定和新会话可用；未绑定的旧会话仍需新建。只保存但未重启时，运行政策不变。

工具不可用时，固定协议要求 Agent 不使用记忆继续工作，不反复重试，也不回退到另一套自动
记忆。即使关闭访问，已有绑定的启动仍依赖 amux CLI。MCP 已缓存的记忆续页也在返回前
检查 Hub 开关。
