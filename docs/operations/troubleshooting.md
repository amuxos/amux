# Troubleshoot Feishu and Agent execution

[English](troubleshooting.md) | [简体中文](troubleshooting.zh-CN.md)

Start with what you can see in Feishu, then ask the operator to inspect the
relevant component. A repeated message can enqueue more work, so check status
before resending a request whose outcome is uncertain.

| Symptom | First check | Next action |
| --- | --- | --- |
| Bot never responds | Correct bot, real @mention in a group, bot access and setup | Try `/amux` in private chat; operator checks Bridge connection and app permissions |
| Group discussion gets no answer | Shared topics require a mention to trigger work | @mention the bot in the existing topic with a specific follow-up |
| Other members are denied | Owner-only/shared access policy | Owner checks the group's `/amux` settings; sharing the group is insufficient |
| A card appears, then launch fails | Selected expert, Runtime and native Agent login | Operator runs Agent diagnosis on the executing host |
| Work appears stuck | Pending permission card, active turn or queued input | Resolve the current request; use `/status` or context help before interrupting |
| Old topic refuses input | Ended binding or retired group conversation | Start a new group message/topic; do not expect an old topic to become a new conversation |
| Group stopped after experiment was disabled | Saved independent group settings are paused | Owner checks the experiment and applies any required restart |
| Memory unavailable | Saved vs running switch, old unbound conversation, Hub scope | Apply settings, start a new conversation; do not fall back to another user's memory |
| Schedule has no visible chat reply | Actual schedule/run/task records and result delivery arrangement | Read the Task result; creation does not guarantee a Feishu push |

## Operator diagnostics

On the affected installation:

```sh
amux system status
amux system doctor
amux system target list
amux system logs
```

`system logs` prints paths; inspect the indicated Hub/Runtime logs around the
failure time. `system doctor` diagnoses without changing the installation. Match
the Runtime to the Session/Task's target; a healthy local worker says nothing
about a failing remote one.

Check the selected Profile on the executing host, replacing it as needed:

```sh
amux agent doctor builtin/codex
```

The normal doctor is static. `--smoke` deliberately runs a real model turn and
may use account quota. For a missing executable or expired login, repair the
native Agent in that Runtime's service environment before retrying.

## Recover deliberately

If services are stopped, `amux system start` starts the configured installation.
Before restarting, inspect the plan and finish active work:

```sh
amux system restart --plan
```

Then run `amux system restart` when ready. Do not use installer `--clean` as a
routine repair; it deletes amux data. If required isolation is unavailable, fix
[helper readiness](linux-dac-isolation.md) instead of weakening policy.

For a useful issue, include version, time, message mode, Profile, target, Session
or Task ID, the exact error and relevant log excerpt with credentials removed.
Do not assume a screenshot of a final answer proves which TaskRun produced it.
