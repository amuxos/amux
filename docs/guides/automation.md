# Schedules, group watch and document comments

[English](automation.md) | [简体中文](automation.zh-CN.md)

These entry points can all trigger Agents, but their conditions, context and
result destinations differ. Choose the right one and verify what the system
actually saved or received, instead of relying on a conversational promise.

| Entry | Trigger | Execution and result |
| --- | --- | --- |
| Schedule | A saved Hub time rule becomes due | Submit to a Session or create a child Task; inspect its records |
| Group watch | An enrolled group gets an eligible new root message | Independent topic work, presented in group topics/cards |
| Document comment | Capability enabled and the owner mentions the assistant in a comment | Execute in the comment thread's Session; reply in comments |

## Ask the assistant to schedule work

### Specify execution prerequisites

The Agent needs amux schedule tools, the Hub must be running, and the Runtime,
expert, credentials and workspace must be available when the rule fires. Saving
a Schedule does not reserve future permissions: authorization and execution
prerequisites are checked again at firing time.

Specify time, timezone, scope, expert, input location, permissions and how results
will be inspected. For example:

> @assistant Schedule a read-only repository check at 09:00 each weekday using
> the Hub's local timezone. First verify that timezone and the repository location
> on the Runtime. Return the saved rule ID, actual specification, expert,
> workspace and next firing time with timezone. Keep results in Task records;
> “scheduled” must not be reported as an already completed check.

If timezone or workspace is unknown, have the operator verify it first. This is
a natural-language request, not a new Feishu command. Topics do not support
`/schedule` or `/sched`.

### Verify after creation

1. Retain the returned `schedule_id` and `next_run_at`; “I will check every day”
   is insufficient evidence.
2. Ask the Agent to read it with `amux.schedule_get` and verify `enabled`, `spec`,
   Task input and execution settings. `amux.schedule_list` is paginated; reading
   only its first page cannot establish that no other rules exist.
3. Verify one execution using a short test interval or explicit future time.
   Do not repeatedly create same-named rules just to check whether one exists.
4. After it fires, read history, obtain that run's Task ID and retrieve its result.
5. Keep the production rule after successful verification; disable or delete
   unwanted test rules.

Operators can inspect:

```sh
amux schedule list
amux schedule runs <schedule-id>
amux task show <task-id>
amux task logs <task-id>
```

MCP creates scheduled subtasks. It does not automatically promise delivery back
to the original Feishu chat. History stores Task IDs; use `amux.task_get` /
`amux.task_stream` for results. Proactive notifications need an actual configured
messaging capability; saving a Schedule alone does not establish one.

## Timing, execution type and failure behavior

### Supported time specifications

| Rule | Example | Meaning |
| --- | --- | --- |
| Interval | `every 30m`, `every 1h30m` | Minimum one minute; schedule future interval firings |
| Daily | `daily 09:00` | 09:00 in the Hub's local timezone |
| Cron | `cron 0 9 * * 1-5` | Standard five fields, no seconds field, Hub local timezone |
| Once | `at 2027-01-15T09:00:00+08:00` | Explicit instant; replace the example with your desired future date |

An offset-free value such as `at 2027-01-15 09:00` also uses the Hub's local
timezone. The sender's phone, browser or creation terminal does not override it.
Have the Agent explain the next firing time with its timezone, not just “09:00”.

### Session schedules versus subtasks

| Type | Use | Boundary |
| --- | --- | --- |
| Session prompt | Operator uses CLI `schedule create` to periodically prompt a conversation | Reuse the Session, or launch a new Session per run from its settings; not a copy of its entire history |
| Scheduled subtask | Bounded work each time, through CLI `create-task` or MCP `schedule_create` | Each fire creates an ephemeral child Task with its own ID |

Scheduled subtasks support default policy, `policy-allow` or `deny-all`, not
`escalate-to-main`. Do not design unattended work around permission responses
from a main conversation that may have ended. Policy does not replace file access,
model login or workspace preparation.

### A successful fire is not a completed task

| Record | Meaning and next step |
| --- | --- |
| `ok` | Session prompt delivered; inspect the Session's execution result |
| `ok:task:<id>` | Child Task created; query its success, failure or timeout |
| `skipped:offline` | Recurring Session target was offline; wait for a future scheduled fire |
| `missed` | A one-shot delivery/submission could not happen and will not automatically repeat |
| `error: ...` | Read the error and check authorization, inputs, target or submission resources |

Recurring rules advance even after a failed delivery; they do not endlessly retry
the same occurrence. Do not expect every missed interval during downtime to be
backfilled. Consumed one-shot rules are not retried. Before arranging replacement
work, check whether a Task was already created to avoid duplication.

## Update, pause or delete a rule

The Agent can use `amux.schedule_update`, `amux.schedule_set_enabled` and
`amux.schedule_delete`. Read back the same ID after a change to verify its state.

For example: “Disable rule `<schedule_id>` and retain it for inspection. Tell me
whether Tasks it previously created are still running; do not report those Tasks
as cancelled merely because the rule is disabled.”

Operators can pause future triggers with:

```sh
amux schedule disable <schedule-id>
amux schedule runs <schedule-id>
```

Disabling or deleting a rule does not cancel existing Tasks. Cancel the individual
Task ID and check its terminal state. Re-enabling does not automatically retry a
previously failed Task.

## Automatically handle new group topics

### Enable and verify watch

Group watch suits alert analysis or initial handling of new questions. The target
group must use topic mode, the bot must receive/read eligible messages, and an
expert and execution resources must be available.

1. The owner mentions the bot with the literal command `开启群值守`.
2. Verify the group and assistant on the confirmation card, then enroll the group.
3. Check group `/amux` or the private management card to confirm enrollment and
   enabled watch status.
4. Send an eligible **new root message**. To test automatic handling, do not
   mention the current bot: a mention uses ordinary routing instead.
5. Inspect the new topic work/card and verify its expert and input materials.

One bot can use different sender types for different groups, such as app alerts
in one group and user questions in another. A per-group sender override only
changes filtering; it does not enroll an otherwise unwatched group.

### Which messages are eligible

| Condition | Current behavior |
| --- | --- |
| Enrolled group with watch enabled | Still subject to sender, content, capacity and access checks |
| User/app new root message | Effective sender allowlist applies; not every reply starts new work |
| Text or valid images, files, merged forwards | May become input; material-only messages use a material description |
| Mention of the current bot | Ordinary mention routing avoids a second automatic execution |
| App alert mentioning only people | Can remain eligible |
| App message mentioning other bots | Excluded from automatic handling to avoid bot loops |
| Management command, empty/deleted message or the bot's own output | Does not start ordinary new alert work |
| Duplicate event or active Session capacity reached | Deduplication or capacity may prevent a new execution |

In addition to live events, bounded history polling finds recent messages. It is
not a replay of all group history. An old message is not a useful test of newly
enabled watch.

Optional app-alert content deduplication is off by default. When enabled, matching
alerts from the same app in the same group may be suppressed within the configured
window without a new Session. User messages and different groups are independent.
Ambiguous bot mentions and alert deduplication can require permission to list
group bots. Check capability notices after a permission failure instead of using
repeated identical alerts as a liveness test.

### Stop watch and understand group overrides

The owner sends `关闭群值守` and confirms the card to remove this group. This stops
future automatic intake; it does not cancel topic work already created. Handle
the specific topic or Task to stop existing work.

Independent group policy determines the group's routing, so a bot using global
singleton mode can still watch an independently configured thread-mode group.
Disable watch before switching that group to a continuous conversation. Disabling
the independent-group-settings experiment pauses automatic work in opted-in groups
while retaining saved settings.

## Start work from document comments

Document comments are a separate capability. The current implementation requires
a confirmed bot owner as the caller. Enabling group collaboration does not grant
every member access to this entry point.

1. The owner enables document-comment intake in private `/amux` assistant settings
   and satisfies the capability checks for platform permissions and document access.
2. In an accessible document comment, use Feishu's actual mention feature and
   state a request, for example: “Check this paragraph's edge cases and reply here
   with suggestions; do not edit the body yet.”
3. The system reads the comment and available related document context. If it
   cannot read the comment or specific request, it reports failure rather than
   pretending analysis started.
4. Mention the assistant again in the same comment thread to continue. Different
   root comments bind independent Sessions.
5. Read the comment reply. If permission is needed, handle the card in the bot's
   private chat, then return to the comment for the result.

Input centers on the comment and selected relevant context; it is not an unlimited
copy of the entire document. Output goes to comments. Enabling intake does not
itself edit the document body; writing depends on Agent tools, instructions and
authorization. Duplicate delivery of the same event is deduplicated, not new work.

## When automation does not happen

| Symptom | Checks |
| --- | --- |
| Agent promised a schedule but no rule exists | Request `schedule_id`, read it back, verify tool availability |
| No Feishu notification at the expected time | Inspect fire history and Task; separate execution from unconfigured notification delivery |
| Rule succeeded but Task failed | `ok:task` only confirms creation; read Task error, workspace and credentials |
| Watch ignored a test message | Enrollment, thread mode, new root, sender types, mention targets and capacity |
| Repeated app alert has no new card | Message deduplication and optional content-dedup window |
| Document comment did not start work | Owner identity, capability/access, actual mention and readable request |
| Document work is waiting | Private permission cards and failure messages in the comment |

Parameters are in the [CLI](../reference/cli.md) and [MCP](../reference/mcp.md)
references; intake contracts are in the [Bridge model](../current/feishu-bridge-product-model.md).
