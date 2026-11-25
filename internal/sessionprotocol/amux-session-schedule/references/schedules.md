# Schedule modes and management

`schedule_type` is `session-prompt` or `subtask`. `run_mode` applies only to
`session-prompt`. These combine into three execution modes:

| Mode | On each trigger | Results |
| --- | --- | --- |
| `session-prompt` + `reuse-session` | Submit a prompt to the existing Session, retaining conversation context | Session output; schedule has latest delivery status |
| `session-prompt` + `new-session-per-run` | Create an ordinary Session from the existing Session's launch template, without copying conversation history | `ok:new-session:<name>` identifies the Session; read its output |
| `subtask` | Submit independent bounded work from the saved Task specification | Task ID and run records; read Task terminal status and result |

## Choosing and creating

Use the current Session for follow-ups, progress checks and continuing a workflow.
For MCP, call `schedule_create(schedule_type="session-prompt", spec=...,
prompt=..., run_mode="reuse-session")`. Omit `session` for the current Session:
the MCP server resolves its injected `AMUX_SESSION_ID`. For another Session use
a confirmed Session ID or session-ref. Resolve targeting errors before retrying;
do not silently replace a follow-up with an independent Task.

For a fresh conversation each time, set `schedule_type="session-prompt"`,
`run_mode="new-session-per-run"`, confirmed template `session`, `spec` and a
self-contained `prompt`. The template must exist and remain unarchived when the
schedule fires. Its agent type, launch arguments, environment and workspace
settings are reused; its conversation is not. Runs may share the template's
working directory. This mode provides no extra isolation or guaranteed reply
into the originating chat. Use the template's target to inspect created Sessions.

For independent automation, set `schedule_type="subtask"`, `spec` and a complete
`task`. Configure isolation and workspace explicitly when required. Omission of
`schedule_type` defaults to `subtask`, not a current-session follow-up.

Put the object to check, success and stop conditions, next action, and schedule
cleanup instructions in recurring prompts. Record the returned `schedule_id` and
`next_run_at`; stop or delete the schedule when the workflow is finished.

Time specifications: `every <duration>` (minimum 1m), `daily HH:MM`,
`at <when>` (one-shot), and `cron <5-field expression>`. Resolve relative dates
and confirm the scheduler's time zone when it affects the requested time.

## Management and evidence

MCP: `schedule_list`, `schedule_get`, `schedule_set_enabled`, `schedule_update`
and `schedule_delete`. Follow `next_cursor` while `has_more=true`.
Update name, time, a session-prompt's `prompt` or `run_mode`, or partial
fields of a subtask's `task` specification in place with `schedule_update`;
omitted fields keep their current values and run history is preserved.

For session-prompt, `schedule_get` exposes only `last_run_at` and `last_status`,
not complete trigger history. `ok` and `ok:new-session:<name>` mean input was
submitted, not that the requested work succeeded. `skipped:offline` or a one-shot
`missed` means delivery did not occur. For subtask, use
`schedule_get(include_runs=true)` to obtain run records. `ok:task:<id>` means
Task submission; inspect that Task for the final outcome.

CLI equivalents: `amux schedule create "$AMUX_SESSION_ID" SPEC PROMPT
--run-mode reuse-session` (or a confirmed template ref and
`--run-mode new-session-per-run`); `amux schedule create-task` creates a subtask.
Use the command's current help for Task flags and management syntax. Use CLI
only when the Runtime context selects CLI, or the user explicitly requests an
operator CLI action.
