# Agent Profile Pack 示例

[English](README.md) | [简体中文](README.zh-CN.md)

v2 Pack 保存可复用指令和 Skill。Runtime、模型和权限在执行时选择。
这些示例附带可直接消费的 manifest，不会自动安装。

| 示例 | 专家引用 | 内容 |
| --- | --- | --- |
| [`code-reviewer/`](code-reviewer/) | `review/code-reviewer` | 审查指令与 diff-review Skill |
| [`codex-rules/`](codex-rules/) | `rules/codex-rules` | 仓库指令 |

每个示例包含 v2 作者源码及 `dist/agent-pack.manifest.json`。运行时消费 manifest。
修改源码后，使用 Agent Pack Contract 0.8.0 及以上重新生成。

```sh
cd code-reviewer
git init
git add .
git commit -m "add code-reviewer profile"
amux agent pack add review "$(pwd)"
amux agent show review/code-reviewer
amux agent doctor review/code-reviewer
amux session run review/code-reviewer --runtime claude -n review-session
amux task submit "Review changes in ./src" --profile review/code-reviewer --runtime claude --wait
```

在目标 Runtime 配置所选引擎的凭据。普通 Session 和 Task 命令需要 Hub，Pack 在 Hub
所在主机注册，执行时从冻结快照物化内容。参见 [Pack 格式](../../docs/reference/agent-profile-format.zh-CN.md)。
