# Sessions, Tasks and Schedules

[English](execution.md) | [简体中文](execution.zh-CN.md)

Feishu is the main place to ask amux for work. For example, a review request in a
topic starts a Session turn; a follow-up adds another turn to the same conversation.
Only an actual delegation creates a child TaskRun. Asking the same persistent
expert to continue can create another TaskRun on its Agent Instance. A Schedule
stores a rule for future work. The word “task” in a chat is broader than the Task API.

Choose the execution object by how you intend to continue the work:

| Need | Use | What you keep |
| --- | --- | --- |
| Ongoing conversation you can return to | Session | Conversation identity, workspace and history |
| A bounded assignment with an observable result | Task | Task ID, status, events and result |
| Work started at a time or repeatedly | Schedule | A firing rule targeting a Session or creating a subtask |
| A reusable expert through MCP | Persistent Agent Instance | Instance identity across separate TaskRuns |

A Profile such as `builtin/codex` is a launch definition, not one of these running
objects. A target selects a Runtime location. Choosing a Profile does not create
an execution until a Session or Task is started.

## A conversation can accept several turns

Each accepted Session submission enters a queue. One turn is dispatched at a
time according to Session policy. You can watch, detach, send more input or
interrupt a turn without discarding the conversation. Sleeping stops the process
while retaining resumable state. See [Session operations](../guides/cli-sessions.md).

## A task has a completion boundary

A Task supplies a prompt and execution settings. Its runner creates child work
on the chosen Runtime and reports state and result. `--wait` blocks the calling
CLI until completion; omitting it gives you a task ID for later inspection.
Permission policy and workspace isolation are separate settings. See the
[Task guide](../distributed-agent-usage.md).

## A schedule creates future work

A Session schedule submits prompts to an existing conversation, or creates a
new Session for each run when configured. A subtask schedule creates bounded
work at each fire. Authorization is checked again when firing; a saved schedule
does not bypass revoked access. Timing is evaluated by the Hub, not the terminal
that created it. See [schedules](../guides/automation.md).

The [Task model](../distributed-task-model.md) and
[MCP reference](../reference/mcp.md) define persistent Instance and TaskRun details.
