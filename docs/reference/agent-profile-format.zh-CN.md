<a id="agent-profile-packs"></a>

# Agent Profile Pack

[English](agent-profile-format.md) | [简体中文](agent-profile-format.zh-CN.md)

Agent Pack 是一个 Git 仓库，包含 `dist/agent-pack.manifest.json` manifest 及其引用的
指令和 Skill 文件。amux 消费 manifest `v1.1`，不会回退读取源码 Profile TOML。
Pack 可以使用自己的 authoring 工具生成 manifest；可选的 managed authoring runtime
没有默认下载源。

[示例 Pack](../../examples/public-pack/dist/agent-pack.manifest.json) 包含一个 Codex
审查 Profile，提供原生模型与显式 Provider 模型选项。其 Provider URL 仅供示例，
选择该 Provider 前必须替换为真实的 Responses API 端点。原生默认模型不需要 Provider。

<a id="identity-and-files"></a>

## 身份与文件

Pack 注册为 `team` 时，其 `reviewer` Profile 的引用为 `team/reviewer`。
`team/reviewer@<revision>` 固定物化版本。Profile 的 `namespace` 和 manifest 的
`namespaces` 索引用于组织条目，不会给引用增加一段路径。
`profiles` 中的所有条目都会被消费，包括不在 namespace 索引中的条目。
`builtin` 为保留命名空间。

Manifest 必须包含 `schema_version`、非空 `pack_namespace` 和非空 `profiles` map。
`providers` 和 `namespaces` 可以省略。指令路径相对于 Pack 根目录，按列表顺序合并。
缺失指令文件会使 Pack 校验失败；所有路径必须位于 Pack 内。

<a id="profiles"></a>

## Profile

内置专家和外部专家的描述均由 manifest 提供。发行版可以提供可选的兼容元数据，
仅在 manifest 缺少描述时回退使用；新增专家无需在 Go 中再次注册。

默认发现返回内置、本地及正式安装的专家。根据 registry 元数据排除开发、临时和
评估 Pack，同时兼容旧的 `-dev` 命名约定。`agent list --all` 包含这些 Pack，
`--dev` 只列出这些 Pack；显式 Profile 引用仍可解析。单个 Pack 不可用时返回诊断，
其他正常 Pack 仍然显示。只有正式 Git Pack 可以覆盖内置专家的同名 Skill；本地、
开发、临时和评估 Pack 不参与覆盖。非内置专家继续使用自身的 Skill 来源。

| 字段 | 含义 |
| --- | --- |
| `type` | 必填基础 Agent：`codex`、`claude` 或 `pi`。 |
| `description` | 面向读者的 Profile 描述。 |
| `model` | 默认原生模型名或限定的 Provider 模型；未声明 Provider 时，空值使用 Agent 默认模型。`providers` 非空时必填。 |
| `supported_models` | 可选的精确模型允许列表。省略时不限制原生选择；显式空列表只允许 Agent 默认模型。非空默认模型必须包含在列表中。 |
| `providers` | 如 `service/example-model` 的限定引用，每项须在顶层 `providers` map 中定义。 |
| `instructions` | 有序的指令文件路径。 |
| `skills` | Pack 提供的 Skill 名称。投影前，Runtime 上必须有相应 Skill 内容。 |
| `envs` | 从凭据存储请求的环境变量名。 |
| `env_refs` | 显式引用，例如 `credential:github:GITHUB_TOKEN`。 |
| `permission` | 可选的 `permission_mode`、`allowed_tools` 和 `ask_tools`。 |
| `namespace`、`extends`、`subagents` | 组织与 authoring 元数据。Manifest 应发布已解析的内容；消费者不解析源文件继承。 |

`permission_mode` 可为 `default`、`plan` 或 `bypass`。Codex 将其映射为审批与沙箱
设置，Claude 映射为原生权限模式。Pi 没有等价的原生权限模式参数。
Task 权限策略是独立的控制面规则，见 [Task 模型](../distributed-task-model.zh-CN.md)。

<a id="models-and-providers"></a>

## 模型与 Provider

没有显式 Runtime 覆盖时，遵循 Profile 模型策略。显式 `supported_models` 列表中的
每个限定 `binding/model` 选项都必须对应顶层 Provider 定义。为兼容 manifest v1.1，
Provider 默认模型还必须出现在 Profile 的 `providers` 数组中。
省略 `supported_models` 时，仅 `providers` 中列出的默认模型绑定 Provider；
其它请求的模型仍视为原生模型。单独存在的顶层 Provider 不会绑定未受限制的原生模型选择。

显式覆盖 Runtime 时，Runtime 建议不是允许列表。Operator 提供的原生模型名原样传给
所选 Agent，包括建议中没有的名称。不从名称推断 Provider、端点或凭据。
思考强度仍需受 Adapter 支持。要使用 Pack 的 Provider 绑定，应选择 Profile 模型并
省略 Runtime 覆盖。

| Agent | Provider 平台 | Wire API |
| --- | --- | --- |
| Codex | `openai-compat` | `responses` |
| Pi | `openai-compat` | `chat-completions` |
| Claude | 原生 Agent 配置 | 不支持 Pack Provider 绑定。 |

公共 Provider 必须提供 `base_url`、`upstream_model` 和不带前缀的 `api_key_env`
变量名。附加平台及其默认值属于发行版扩展。省略 `wire_api` 时选择 Agent 的原生默认值。
可选字段包括 `context_window`、`max_tokens`、`read_timeout`、`description`、
`prompt_hint` 和字符串值的 `thinking` map；具体投影依 Adapter 而定。
凭据在 Pack 外配置。示例特意不包含 API key 值。不提供 Responses-to-Chat 兼容网关。

<a id="workspace-projection"></a>

## 工作区投影

Codex 使用 `AGENTS.md` 指令和 `.agents/skills` 下的 Skill。
Claude 使用 `AGENTS.md` 加 `CLAUDE.md` 引用，Skill 位于 `.claude/skills`。
Pi 使用其 Adapter 特定的设置与扩展投影。Skill 来源元数据保留在 Pack 中。
Task 获取冻结的 Profile 快照；安装或更新 Pack 不会悄悄替换既有 Task 的身份。

Pack 注册、检查、验证与渲染命令见 [CLI 参考](cli.zh-CN.md)。
[示例验证测试](../../internal/agentprofile/public_pack_example_test.go) 校验随仓库提供的
manifest 及其原生/Provider 选择。
