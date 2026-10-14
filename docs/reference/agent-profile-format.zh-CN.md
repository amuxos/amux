# Agent Profile Pack

[English](agent-profile-format.md) | [简体中文](agent-profile-format.zh-CN.md)

Agent Pack 是包含 `dist/agent-pack.manifest.json` manifest 及指令、Skill 文件的
Git 仓库。amux **仅接受 v2**。其他版本会报错，不修改 Pack；运行时不会回退读取源码 TOML。

## 身份和文件

以 `team` 注册 Pack 后，其 `reviewer` 专家引用为 `team/reviewer`。
`team/reviewer@<revision>` 固定物化版本。Profile 的 `namespace` 与 manifest 的
`namespaces` 索引只用于组织内容，不改变引用。消费者读取全部 `profiles`，包括没有
列入索引的条目。`builtin` 是保留命名空间。

manifest 必须包含 `schema_version: "v2"`、非空 `pack_namespace` 和非空 `profiles`。
`namespaces` 可省略。指令路径相对于 Pack 根目录，按顺序拼接；缺失文件或越界路径校验失败。

## 内容型专家

| 字段 | 含义 |
| --- | --- |
| `description` | 专家用途说明，内置专家也从这里读取。 |
| `instructions` | 有序指令文件路径。 |
| `skills` | Pack 提供的 Skill 名称。 |
| `envs` | 向凭据存储请求的环境变量名称。 |
| `env_refs` | 如 `credential:github:GITHUB_TOKEN` 的显式引用。 |
| `namespace`、`extends`、`subagents` | 组织与编写元数据。发布展开后的内容；消费者不解析源码继承。 |

Profile 禁止包含 `type`、`model`、`providers`、`supported_models`、`permission`，
包括空值和 null。顶层 `providers` 也禁止出现，包括空对象。v2 Pack 不包含 Provider 源文件。

执行时单独选择 Runtime 和模型。Task 的 `permission_policy` 同时选择原生权限并归一化
原生权限参数；直接 Session 保留调用方/原生权限设置。必须由调用方设置 Runtime 或显式传入
`--runtime`。内置快捷入口可以有调用方默认值；Task（含定时 Task）省略 Runtime 时，
还会采用发行版默认 builtin 的启动预设。这些默认值位于 Pack 外部。
模型与 Provider 连接配置属于 Runtime 注册；凭据保存在 Pack 外部。
执行设置解析后冻结到 Session/Task 快照，刷新内容不替换执行设置。

## 本地编辑

amux 自己创建 v2 源码骨架，由 **Agent Pack Contract 0.8.0 及以上**编译校验，
不调用工具的默认 `init` 模板。安装过程从发行版发布源或 `AGENT_PACK_CONTRACT_SCM_URL`
获取工具，拒绝旧版发行包。可用 `AMUX_AGENT_PACK_CONTRACT_BIN` 指定已安装的兼容编译器。

`agent local create` 接受内容和用途说明，不接受 `--type`、`--model`、`--permission`。
创建、复制、预览、评估、发布和更新全部使用 v2 源码和 manifest。已有旧版库必须显式
转换后再使用，amux 不自动改写用户文件。

维护者可通过
`amux agent local convert-v2 --expected-revision <agent local status 返回的完整 revision>`
显式转换托管的 v1.1 Local Pack。转换前先准备 Runtime、模型、Provider 连接和权限：
命令会移除 `type`、`model`、`providers`、`supported_models`、`permission` 和 Provider
源码目录，不迁移执行配置。内容值、列表省略与空数组的语义、ID 和资产保持不变。
TOML 会重新编码，注释与排版仅保留在 Git 历史中。有专家的库必须先通过已安装的兼容
Contract 编译器校验，再发布新 commit 和 registry revision。失败保留正式分支和 registry；
未发布的空库保持未注册状态。对 v2 重复执行不改变 revision。历史快照保留原始字节；
转换不会让旧 v1.1 revision 成为有效的 v2 Pack。

## 发现和投影

默认发现范围包含内置、本地及已安装专家。开发、临时和评估 Pack 按注册元数据排除，
仍识别旧的 `-dev` 命名空间后缀。`agent list --all` 包含这些 Pack，`--dev` 只列出它们。
显式引用仍可解析。某个 Pack 不可用时单独报告诊断，不隐藏健康 Pack。

仅正式 Git Pack 可以向内置专家提供同名 Skill 覆盖；本地、开发、临时和评估 Pack
不能覆盖。其他专家继续使用自身 Skill 来源。

Codex 使用 `AGENTS.md` 和 `.agents/skills`；Claude 使用 `AGENTS.md`、导入它的
`CLAUDE.md` 和 `.claude/skills`；Pi 使用所属 Adapter 的投影。任务接收冻结的执行快照，
更新 Pack 不会静默改变运行中任务的身份。

参见 [CLI 参考](cli.zh-CN.md)、[Pack 示例](../../examples/public-pack/README.zh-CN.md)
及[示例校验测试](../../internal/agentprofile/public_pack_example_test.go)。
