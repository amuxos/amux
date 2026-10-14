---
name: amux-session-schedule
description: Manage Amux sessions and scheduled work. Use for creating or controlling agent conversations, delayed follow-ups, periodic checks, and schedule management.
---

# Amux sessions and schedules

The Runtime instruction configured for this conversation states your transport and execution role.
Use that transport only: MCP hosts use the available Amux MCP tools; CLI hosts
use the scoped `amux` CLI. If unavailable, explain the missing capability; do not bypass
configuration or authorization through another transport. Task-owned children
must return control to their parent rather than create sessions or schedules.

- A Session is a continuing conversation. Use it when the user requests an
  independently interactive agent. Discover Profiles with `profile_list` /
  `profile_get`; create with `session_run`. Pass the returned target to subsequent
  calls. Use `session_send`, `session_stream` and session lifecycle tools to
  interact. Never infer completion from successful input submission.
- A Task is bounded work with its own terminal status and result. Creating an
  ordinary Session does not provide a separate workspace or extra isolation.
- For delayed work, use a schedule rather than waiting in a long-running shell.
  Read [schedule modes and management](references/schedules.md) before creating
  or modifying one. Continue an existing workflow with a `session-prompt` schedule
  in `reuse-session` mode; use a `subtask` for independent bounded work. Use
  `new-session-per-run` only when a fresh ordinary conversation is requested.
- Confirm real IDs, target and time zone from tool responses. Discover candidates
  only when the requested target cannot be resolved. Follow tool schemas and
  pagination; never invent tool names, IDs or flags. Authorization does not
  expand because a tool or Skill is available.
- CLI hosts: inspect the relevant `amux session --help`, `amux schedule --help` or
  `amux task --help` once, then the command's help for exact syntax. Use
  `"$AMUX_SESSION_ID"` for the current session. Preserve the injected actor and
  connection environment; do not read service-user configuration or credentials.
