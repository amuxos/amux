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
  Default to an isolated `subtask` schedule (omitting `task.agent`/`task.model`
  inherits the current Session, and in Feishu omitting `delivery_target`
  defaults to `origin`); use `session-prompt` (`reuse-session`) only when
  continuing the current conversation history. Reply immediately after
  `schedule_create` instead of waiting for the first fire, and never confuse an
  older completed schedule from another thread with a new request. Read
  [schedule modes and management](references/schedules.md) when you need full
  details or CLI syntax.
- Confirm real IDs, target and time zone from tool responses. Discover candidates
  only when the requested target cannot be resolved. Follow tool schemas and
  pagination; never invent tool names, IDs or flags. Authorization does not
  expand because a tool or Skill is available.
- CLI hosts: inspect the relevant `amux session --help`, `amux schedule --help` or
  `amux task --help` once, then the command's help for exact syntax. Use
  `"$AMUX_SESSION_ID"` for the current session. Preserve the injected actor and
  connection environment; do not read service-user configuration or credentials.
