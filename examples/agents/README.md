# Agent Profile Pack Examples

[English](README.md) | [简体中文](README.zh-CN.md)

A v2 Pack holds reusable instructions and skills. Runtime, model, and permissions
are execution choices. These examples include ready-to-consume manifests; they
are not installed automatically.

| Example | Profile ref | Content |
| --- | --- | --- |
| [`code-reviewer/`](code-reviewer/) | `review/code-reviewer` | Review instructions and a diff-review skill |
| [`codex-rules/`](codex-rules/) | `rules/codex-rules` | Repository instructions |

Each example contains v2 authoring sources and `dist/agent-pack.manifest.json`.
Runtime consumers read the manifest. After changing sources, regenerate it with
Agent Pack Contract 0.8.0 or later.

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

Configure the selected engine's credentials on the target Runtime. Normal Session
and Task commands require the Hub; register the Pack on the Hub host. Execution
materializes content from a frozen snapshot. See the
[Pack format](../../docs/reference/agent-profile-format.md).
