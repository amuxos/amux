# Delegate work to other Agents

[English](delegation.md) | [简体中文](delegation.zh-CN.md)

When work divides into independently useful deliverables, ask the main Agent in
Feishu to delegate to experts and combine their results. This guide covers a
clear assignment, verification, follow-up with the same expert and failure handling.

## Choose the collaboration object

| Need | Object | Identity to retain |
| --- | --- | --- |
| Continue discussing with the assistant | Another turn in the current Session | Topic/conversation; no Task ID is required |
| Complete a bounded review, analysis or change | Ephemeral TaskRun | `task_id` |
| Work through several rounds in one expert environment | Persistent Agent Instance | `agent_instance_id` and each execution's `task_id` |
| Use the native Agent's own subagents | Native Agent capability | Does not automatically create amux Task or Instance identity |

A Profile is a reusable expert definition, not an execution. Selecting
`builtin/codex` twice can create independent work. Continuing a persistent expert
requires its instance ID.

## Specify the assignment

The main Agent needs amux delegation tools and an available Profile. A working
chat bot does not necessarily have these tools, and an installed Profile does
not prove credentials, workspace and target host are ready.

For a first attempt, ask the Agent to list available experts and explain their
roles. Then include:

| Element | Example |
| --- | --- |
| Input and scope | Repository, commit, change set or exact document excerpts |
| Division of work | One expert checks correctness, another checks test coverage |
| Allowed actions | Read-only review; no file edits or remote records yet |
| Deliverable | Each finding contains file location, evidence and recommendation |
| Environment | Repository/workspace on the chosen Runtime and required credentials |
| Deadline | Per-child time limit; report partial evidence and missing work on timeout |
| Synthesis | Main Agent deduplicates findings and identifies disagreements or unsupported claims |

For example, send this natural-language request, not a slash command:

> @assistant Review this repository change. First verify available experts and
> the repository location. Delegate correctness and test coverage to two experts
> through amux for read-only analysis. Supply each with the change scope and
> relevant material. Return their task IDs after creation, then combine findings
> with file evidence and clearly identify unfinished work.

Tool use still depends on the model, available tools and authorization. “I asked
another expert” in a reply is not proof of delegation; verify returned task or instance IDs.

## Follow a bounded delegation

A verifiable path is: choose a Profile → `amux.task_submit` returns a Task ID →
child preparation and execution → `amux.task_get` / `amux.task_stream` expose
status and results → main Agent synthesizes them. `amux.task_run` is the blocking
form. Permission escalation to the main Agent is incompatible with that blocking
form; use asynchronous submission and inspection when escalation is needed.

Tasks execute on the selected Runtime. Their default workspace is a temporary
directory, not an automatic copy of the parent's cwd, uncommitted changes,
complete Feishu topic or attachments. For repository work, provide an accessible
repository identity or target-local path. `git-worktree` needs a repository
directory or cache identity.

### Read child cards

For related delegations observed in a bound Feishu conversation, amux creates
separate child progress cards and updates them in place. A label uses an expert
short name plus instance or child Session identity; Task ID may be a temporary
label before child creation. Use the full Task ID to identify a record reliably.

- The main card summarizes parent activity; each child's process, model and
  result belong to that execution.
- A child succeeding does not mean the main Agent has finished its comparison.
- A parent response ending does not mean all children were cancelled; inspect
  their individual states.
- Child cumulative usage belongs to its Provider conversation and can include
  earlier TaskRuns for a persistent expert. Do not add parent/child cards as a
  bill for this request.
- If a child card is absent, inspect the ID and record first. Cards present work;
  records verify that work actually exists.

### Interpret status and results

| Status | Meaning |
| --- | --- |
| Nonterminal states such as `pending`, `preparing`, `running` | Work is still preparing resources or executing |
| `waiting_permission` | A permission request needs handling according to this Task's policy |
| `succeeded` | Execution succeeded; still verify the report, patch or files against acceptance criteria |
| `failed` | Inspect the error and partial result; do not present it as a complete delivery |
| `cancelled` | Cancellation does not undo already performed changes |
| `timed_out` | Deadline exceeded; inspect partial work before deciding what to retry |

The current default deadline is 30 minutes. `timeout_seconds` starts at admission
and includes preparation and Agent startup. Allow preparation time when choosing
a shorter deadline. Results default to a summary. Read events or detailed results
for evidence; even full text can be bounded, with pointers for further retrieval.

## Permissions, stopping and failures

Child permission policies govern permission requests: the default `policy-allow`
automatically handles requests that meet its rules, without requiring a human
prompt for each one; `deny-all` denies them, and `escalate-to-main` reports them
for a response through task tools. These policies do not grant filesystem access,
bypass isolation, supply credentials or establish a read-only sandbox.

Sending `/interrupt` to the parent in Feishu does not establish that every child
was cancelled. To stop a particular child, ask the main Agent to call
`amux.task_cancel` for that Task ID and report the final state. Already written
files or external changes still need separate inspection.

After a child failure, retrieve its error and evidence and retry only the missing
part. Check which Runtime and workspace contain the output. A child finishing
with a patch does not automatically merge it into your main branch, and a claim
of editing files does not prove the parent's workspace changed.

Operators can verify with read-only commands:

```sh
amux task show <task-id>
amux task logs <task-id>
```

## Continue with the same expert

For several rounds in the same environment, explicitly request a persistent
Agent Instance and retain its ID:

> Use a persistent expert for the first review. Return the instance ID, this
> task's ID and the main findings. After I add constraints, continue with that
> same instance instead of creating another expert with the same name.

| Tool | Purpose | Execution effect |
| --- | --- | --- |
| `amux.instance_spawn` | Create a persistent expert | Starts initial work and returns a stable instance identity |
| `amux.instance_followup` | Give an existing instance a new assignment | Starts a new TaskRun while retaining native conversation history |
| `amux.instance_send` | Hold supplemental input for a later wake | Mailbox only; delivered before a subsequent Task prompt |
| `amux.instance_wait` | Wait for current execution to end or the wait to time out | Does not start work; retrieve results through Task tools |
| `amux.instance_interrupt` | Cancel the instance's current owned execution | Keeps the instance and resumable environment |

A follow-up can say: “Continue instance `<agent_instance_id>` with the new
constraints, reassess the second risk, and return the new Task ID.” A successful
`instance_send` does not mean the expert started answering; use an execution
entry such as `instance_followup` to wake it.

A wait timeout from `instance_wait` is not a Task timeout or cancellation. Inspect
the corresponding Task ID with `amux.task_get` or `amux.task_stream` for results.

The instance returns to its recorded Runtime with resumable Provider history and
environment. This is not automatic cross-host failover. Instance states
`active` / `idle` / `archived` are distinct from a particular Task's success or failure.

## Troubleshooting

| Symptom | Check or remedy |
| --- | --- |
| Delegation claimed but no task exists | Request actual tool results and IDs; distinguish native subagents |
| Child cannot find repository/files | Provide target-accessible paths, repository identity and inputs; inspect workspace policy |
| Child waits for authorization | Inspect `waiting_permission` and permission ID; check the parent can respond |
| “Same expert” forgot previous work | Was its instance ID reused, or only the same Profile selected? |
| Instance message got no response | `instance_send` only queues; `instance_followup` starts work |
| Parent stopped while children run | Cancel the specific Task and verify its terminal state |
| Every child succeeded without a final answer | Ask the parent to retrieve results, resolve differences and synthesize |

Other Feishu bots are a separate integration, not installed local Profiles.
External-bot discovery and new delegation require a valid current-group binding;
this does not permit cross-group discovery. See [Agent concepts](../concepts/agents.md),
the [Task model](../distributed-task-model.md), [Task operations](../distributed-agent-usage.md)
and [MCP reference](../reference/mcp.md) for full contracts and parameters.
