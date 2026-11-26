# Schedule modes and management

Schedules separate **execution** from **result delivery**.

## Two primary execution modes

| Mode | When to use | On each trigger | Result delivery |
| --- | --- | --- | --- |
| **Isolated** (`schedule_type="subtask"`, default) | Most recurring or one-shot jobs (reports, monitoring, reminders, checks, batch tasks) that should not pollute the current chat context | Runs an independent bounded Task; inside a live Session (`AMUX_SESSION_ID` set), omitting `task.agent` automatically inherits the current Session's Agent Profile and model | Controlled by `delivery_target` (`origin` / `none`) and `delivery_policy` (`always` / `on_failure` / `on_non_empty`). In Feishu chats, omitting `delivery_target` defaults to `origin` (`always`) |
| **In-session** (`schedule_type="session-prompt"`, `run_mode="reuse-session"`) | Follow-ups and progress checks that explicitly need the current Session's conversation history | Submits the prompt into the existing Session and wakes the Agent | Output appears directly in that Session (`delivery_target="origin"` is disallowed; omit or use `none`) |

Legacy compatibility: `schedule_type="session-prompt"` with
`run_mode="new-session-per-run"` creates a fresh Session from an existing
template Session on each fire without copying conversation history. Prefer
`schedule_type="subtask"` with `delivery_target="origin"` for new independent
schedules because it provides durable execution history, isolation, and direct
result delivery back to the originating chat.

## Choosing and creating

For independent recurring or one-shot work, call
`schedule_create(spec=..., task={"task": "..."})` (default
`schedule_type="subtask"`). Inside a live Session, you may omit `task.agent`
and `task.model` to inherit the current Session's Agent Profile and model, or
specify a dedicated specialist profile explicitly. Set `delivery_target` and
`delivery_policy` when needed:

- `delivery_target`:
  - `origin`: post the run result card back to the originating chat/thread (auto-default for Feishu-originated `subtask` schedules when omitted).
  - `none`: record execution history without proactive chat delivery.
- `delivery_policy`:
  - `always` (default): deliver on every terminal run.
  - `on_failure`: deliver only on failure or timeout.
  - `on_non_empty`: deliver on failure or non-silent output (suppresses empty output and silent tokens such as `NO_ALERT`, `SILENT`, `HEARTBEAT_OK`, `NONE`).

For continuing the current conversation, call
`schedule_create(schedule_type="session-prompt", spec=..., prompt=...,
run_mode="reuse-session")`. Omit `session` for the current Session so the MCP
server resolves `AMUX_SESSION_ID`. For another Session use a confirmed Session
ID or session-ref. Resolve targeting errors before retrying; do not silently
replace an in-session follow-up with an isolated Task.

Put the object to check, success and stop conditions, next action, and schedule
cleanup instructions in recurring prompts. Record the returned `schedule_id` and
`next_run_at`; **reply to the user immediately after `schedule_create` instead
of waiting or polling for the first fire** (results are delivered automatically
when `delivery_target` is enabled). When checking `schedule_list`, check
`created_at_iso` and state so you never mistake an older completed schedule from
another thread for the user's new request. Stop or delete the schedule when the
workflow is finished.

Time specifications: `every <duration>` (minimum 1m; supports `d`/`w`),
`daily HH:MM`, `at <when>` (one-shot), `in <duration>` (relative one-shot
normalized to `at <RFC3339>`), and `cron <5-field expression or @descriptor>`.
Pass an explicit IANA `timezone` (for example `Asia/Shanghai`) when the
schedule should not depend on the Hub's local time zone.

## Management and evidence

MCP: `schedule_list`, `schedule_get`, `schedule_result_get`, `schedule_set_enabled`, `schedule_update`,
`schedule_trigger` and `schedule_delete`. Follow `next_cursor` while `has_more=true`.
Use `schedule_trigger` (CLI `amux schedule run <id>`) to verify a rule immediately
once without changing its `next_run_at`.
Update name, time, `timezone`, `delivery_target`, `delivery_policy`, a
session-prompt's `prompt` or `run_mode`, or partial fields of a subtask's
`task` specification in place with `schedule_update`; omitted fields keep their
current values and run history is preserved.

For structured execution outcomes (canonical execution status, final Markdown
answer, and Feishu delivery receipts) across both `subtask` and `session-prompt`
schedules, call `schedule_result_get(schedule_id=...)` (optionally passing
`execution_id` from `schedule_get(include_runs=true)`). For any single Session
turn or Feishu message, use `turn_result_get`. For subtask schedules,
`schedule_get(include_runs=true)` also exposes run records, `task_id`,
`result_summary`, and `delivery_status`. For session-prompt schedules, `ok` on
`last_status` means input was submitted, not that the requested work succeeded.

CLI equivalents: `amux schedule create-task SPEC --agent ... --task ...
[--delivery-target origin|none] [--delivery-policy always|on_failure|on_non_empty]`;
`amux schedule create "$AMUX_SESSION_ID" SPEC PROMPT --run-mode reuse-session`.
Use CLI only when the Runtime context selects CLI, or the user explicitly
requests an operator CLI action.
