# Agent Profile Pack 样例

[English](README.md) | [简体中文](README.zh-CN.md)

把 `agent type + instructions + context(AGENTS.md) + skills + 默认启动参数/策略`
打包成 Git-backed Agent Pack，注册到 hub 主机后即可用
`amux session run <namespace/profile>` 或 `amux task submit --profile <namespace/profile>` 复用。

> 完整字段、校验规则、限制见 [Agent Profile Pack 格式](../../docs/reference/agent-profile-format.zh-CN.md)。
> 本目录是**可直接复制的 pack 样例**,不会被自动加载;需要先注册 pack。

## 本目录样例

| 样例 | type | profile ref | 用途 |
| --- | --- | --- | --- |
| [`code-reviewer/`](code-reviewer/) | claude | `review/code-reviewer` | 代码审查 subagent,带 instructions、AGENTS.md context 和 diff-review skill |
| [`codex-rules/`](codex-rules/) | codex | `rules/codex-rules` | 给 codex 注入 AGENTS.md 上下文 |

每个示例都包含当前运行时读取的 `dist/agent-pack.manifest.json`（v1.1），
同时保留 `profiles/*.toml` 作者源文件供参考。运行时不会自动编译或读取 TOML；
修改作者源文件后需同步生成或更新 manifest，并运行 `amux agent doctor` 验证。
示例 manifest 的 `instructions` 同时列出指令与 `AGENTS.md`，采用只读计划权限模式。
旧源文件中的 placement 与 task permission 字段不是 manifest v1.1 的运行时字段。

## 安装与使用

```bash
# Agent Pack 必须是 Git repo 且至少有一个 commit
cd code-reviewer
git init
git add .
git commit -m "add code-reviewer profile"

# 注册 pack namespace,查看 profile
amux agent pack add review "$(pwd)"
amux agent show review/code-reviewer
amux agent doctor review/code-reviewer
amux agent list

# 创建 session 或在 task-path 使用 profile
amux session run review/code-reviewer -n review-session
amux task submit "审查 ./src 的改动" --profile review/code-reviewer --wait
```

## 注意

- `code-reviewer` 使用 `claude`;运行前请确保 target 上已经配置好 Claude 凭证。task 需要单独注入启动身份时,按 [`../03-credential-profiles/`](../03-credential-profiles/README.zh-CN.md) 准备 credential profile,并在提交时传 `--credential-profile`。
- 普通 session/task 命令依赖 hub；应在 hub 主机注册 pack。控制面解析 profile，执行端按冻结快照物化指令和 skills。
- 想从零开始 authoring profile,参考 [Agent Profile Pack 格式](../../docs/reference/agent-profile-format.zh-CN.md) 的 pack layout。
