# Credential Profiles

[English](README.md) | [简体中文](README.zh-CN.md)

Credential Profile 引用 task 启动所需的身份；Agent Profile 的 `envs`/`env_refs`
声明需要哪些变量。密钥不放进任务 prompt 或 Pack。格式与行为见
[Pack 格式](../../docs/reference/agent-profile-format.zh-CN.md) 和 [Task 模型](../../docs/distributed-task-model.zh-CN.md)。

优先使用当前 CLI，在提示中输入密钥：

```sh
amux agent credential set anthropic ANTHROPIC_API_KEY
amux agent credential show anthropic
amux agent credential ls
```

已有 enrollment 或 `AMUX_ACTOR_TOKEN` 时，命令通过 hub 使用当前 Principal 的凭据存储；
hub 不可达会失败，不回退服务账号的全局存储。Operator 为指定 Principal 初始化时使用：

```sh
amux agent credential setup anthropic ANTHROPIC_API_KEY --principal <principal-id>
```

未识别的 legacy-host 使用 `$AMUX_HOME/secrets/<profile>.json`（默认 `~/.amux/secrets/`）。
本目录保留 [Anthropic](anthropic.json.example) 与 [Codex](codex.json.example) 的 JSON
格式模板，均为占位值；手工导入模板只适用于该 legacy-host 存储，不能用来替代 Principal
凭据管理。真实文件放在源码仓库之外，并设置权限 `0600`。

提交时引用凭据名称：

```sh
amux task submit "审查工作目录中的代码" --profile <profile-ref> --credential-profile anthropic --wait
```

将 `<profile-ref>` 替换为声明了 `ANTHROPIC_API_KEY` 的 Profile。原生内置 Profile 也可以使用
对应 Agent 支持的原生登录方式；创建 Credential Profile 本身不会给 Pack 添加变量声明。`files` 键是隔离
HOME 内的相对路径；`kind` 为 `long-lived` 或 `short-lived`。空凭据名称不注入凭据。
完整选项见 [CLI 参考](../../docs/reference/cli.zh-CN.md)。
