<a id="task-操作指南"></a>

# Task Guide

[English](distributed-agent-usage.md) | [简体中文](distributed-agent-usage.zh-CN.md)

For first-time local use, install and authenticate the chosen native Agent, then
run `amux setup local`. Check the control plane and execution location:

```sh
amux system status
amux system target list
amux agent list
```

Submit a task with an explicit completion condition and wait for it:

```sh
amux task submit "Review the working directory and report defects with evidence" --profile builtin/codex --wait
```

`--wait` makes the task blocking; blocking tasks reject `--permission
escalate-to-main`.

Omit `--wait` to receive a task ID, then inspect status, follow events, or cancel:

```sh
amux task show <task-id>
amux task logs <task-id>
amux task cancel <task-id>
```

The default workspace is a temporary directory. For a repository, use
`--workspace git-worktree` and `--repo-dir /absolute/path/to/repository`; optionally
set the starting revision with `--commit <ref>`. `--timeout` is in seconds.
`--permission` accepts `policy-allow`, `escalate-to-main`, or `deny-all`. This policy
selects native launch permissions and approval replies. It overrides native permission
flags in `extra_args`; other arguments retain their order. `policy-allow` is
noninteractive for Codex-compatible engines and bypasses the inner sandbox only
with enforced Linux DAC isolation. `deny-all` selects native read-only/deny mode;
engines without that capability reject it before launch (Pi supports only
`policy-allow`). Workspace, credential and OS isolation remain separate boundaries.
Persistent instances retain their policy; changing it or using an old child
snapshot without the unified policy requires a new instance.

After a permission escalation event, respond explicitly using its permission ID:

```sh
amux task respond <task-id> --permission <permission-id> --allow
```

Use `--deny` to reject. Reference credentials with `--credential-profile <name>`;
see the [credential example](../examples/03-credential-profiles/README.md).
Identified callers use their Principal's credentials. An authorized `--run-as`
alias switches service identity; the Hub resolves and freezes that identity.

Recurring or one-shot work uses schedules. Session prompts use
`amux schedule create <session-ref> <spec> <prompt>`; standalone subtasks use
`amux schedule create-task <spec> --agent <profile> --task "<text>"`. The spec is a
five-field cron expression, `every <interval>`, `daily HH:MM`, or `at <time>` for a
single fire. Authorization is re-checked on every fire, and a consumed one-shot
schedule is not retried. Scheduled subtasks accept
`--permission-policy default|policy-allow|deny-all`; escalation to the main agent
is not available for them. Manage schedules with `amux schedule list`,
`enable`/`disable`, `update` and `remove`, and inspect past fires with
`amux schedule runs`.

Results remain in task history. Preview cleanup candidates first:

```sh
amux task prune --older-than 7d --dry-run
```

After review, removing `--dry-run` deletes terminal history. Persistent Instance
MCP tools differ from ordinary tasks. See [external MCP hosts](mcp-external-hosts.md)
for stdio integration with an external main agent and the [MCP reference](reference/mcp.md)
for complete inputs. The [Task model](distributed-task-model.md) defines these
boundaries; the [generated CLI reference](reference/cli.md) defines all CLI options.
