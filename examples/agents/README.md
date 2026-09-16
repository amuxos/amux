<a id="agent-profile-pack-样例"></a>

# Agent Profile Pack Examples

[English](README.md) | [简体中文](README.zh-CN.md)

Package `agent type + instructions + context (AGENTS.md) + skills + default launch arguments/policy`
as a Git-backed Agent Pack. Register it on the Hub host, then reuse it through
`amux session run <namespace/profile>` or `amux task submit --profile <namespace/profile>`.

> For complete fields, validation, and limits, see the [Agent Profile Pack format](../../docs/reference/agent-profile-format.md).
> These are **copyable Pack examples**, not automatically loaded Packs. Register them first.

<a id="本目录样例"></a>

## Included examples

| Example | type | Profile ref | Purpose |
| --- | --- | --- | --- |
| [`code-reviewer/`](code-reviewer/) | claude | `review/code-reviewer` | Code review subagent with instructions, AGENTS.md context, and a diff-review skill |
| [`codex-rules/`](codex-rules/) | codex | `rules/codex-rules` | Inject AGENTS.md context into Codex |

Each example includes `dist/agent-pack.manifest.json` (v1.1), which the current
Runtime reads, and retains `profiles/*.toml` authoring sources for reference.
The Runtime does not compile or read TOML automatically. After editing authoring
sources, generate or update the manifest and validate it with `amux agent doctor`.
The example manifests list both instructions and `AGENTS.md` under `instructions`
and use read-only plan permission mode. Placement and task permission fields in
the legacy sources are not manifest v1.1 runtime fields.

<a id="安装与使用"></a>

## Install and use

```bash
# An Agent Pack must be a Git repository with at least one commit.
cd code-reviewer
git init
git add .
git commit -m "add code-reviewer profile"

# Register the Pack namespace and inspect its Profile.
amux agent pack add review "$(pwd)"
amux agent show review/code-reviewer
amux agent doctor review/code-reviewer
amux agent list

# Create a Session or use the Profile on the task path.
amux session run review/code-reviewer -n review-session
amux task submit "Review changes in ./src" --profile review/code-reviewer --wait
```

<a id="注意"></a>

## Notes

- `code-reviewer` uses `claude`; configure Claude credentials on the target before running it. If a task needs a separate launch identity, prepare a credential profile using the [credential example](../03-credential-profiles/README.md) and pass `--credential-profile` at submission.
- Normal Session/Task commands require the Hub. Register the Pack on the Hub host. The control plane resolves the Profile; execution materializes instructions and skills from the frozen snapshot.
- For authoring a Profile from scratch, follow the layout in the [Agent Profile Pack format](../../docs/reference/agent-profile-format.md).
