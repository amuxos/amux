<a id="外部-mcp-host-接入-amux-mcp"></a>

# Connect External MCP Hosts to `amux mcp`

[English](mcp-external-hosts.md) | [简体中文](mcp-external-hosts.zh-CN.md)

`amux mcp` is a local, on-demand stdio MCP server. Codex CLI, Gemini CLI, Claude Code,
or any standard MCP host can launch it as a subprocess with `command + args` to
use the complete `amux.*` tool surface.

It is not a network service: it has no listening port or remote access endpoint.
The subprocess runs as the same OS user as the host and exits when the host exits
or closes stdin. `amuxd` is the Hub backend for tasks and schedules; it does not
host an MCP endpoint.

<a id="前置条件"></a>

## Prerequisites

1. `amux` is installed and available in the MCP host's `PATH`.
2. `amux system status` reports a reachable Hub and an available Runtime. For the
   default local topology, run `amux setup local` first.
3. The built-in MCP switch is enabled. It is on by default; to re-enable it:

```bash
amux system config set mcp.enabled true
```

The host can start `amux mcp` before the Hub is running because backend connections
are established on tool calls. The Hub must be reachable when task, agent, or
schedule tools are actually called.

## Codex CLI

Use the CLI to configure it:

```bash
codex mcp add amux -- amux mcp
codex mcp list
```

Equivalent `~/.codex/config.toml`:

```toml
[mcp_servers.amux]
command = "amux"
args = ["mcp"]
```

## Gemini CLI

Add user-level configuration:

```bash
gemini mcp add --scope user amux amux mcp
gemini mcp list
```

Equivalent `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "amux": {
      "command": "amux",
      "args": ["mcp"]
    }
  }
}
```

## Claude Code

Add user-level configuration:

```bash
claude mcp add --scope user amux -- amux mcp
claude mcp list
```

<a id="其他标准-mcp-host"></a>

## Other standard MCP hosts

Select the stdio transport with this configuration:

```json
{
  "command": "amux",
  "args": ["mcp"]
}
```

If the host cannot see the shell's `PATH`, use the absolute path to the `amux`
binary. If it isolates `HOME`, explicitly set `AMUX_HOME` in its MCP server
environment to the actual amux control-plane directory (normally `.amux` in the
user's home). Agent Profile discovery uses Hub RPC and does not require the MCP
process to read that directory directly. Do not set `AMUX_SESSION_ID` manually in
global host configuration; it is reserved for parent provenance and lifecycle
binding of amux-managed Sessions.

<a id="接入后的行为"></a>

## Behavior after connection

- `tools/list` returns the main profile discovery, task, persistent agent, session, schedule, memory and local-expert tools.
- Call only names exposed by `tools/list`; retired or unlisted names return unknown-tool errors.
- External hosts usually have no `AMUX_SESSION_ID`, so tasks without an explicit `parent_session_id` are independent top-level tasks.
- Tools use the current user's amux configuration, Hub permissions, and credential policy. stdio adds no separate authentication.
- Host-side MCP tool confirmation and amux task `permission_policy` are independent controls.

First call `amux.profile_list` to verify discovery, then pass a returned
`profiles[].ref` to `amux.task_submit`. For task semantics and asynchronous
workflows, see the [task guide](distributed-agent-usage.md).

<a id="排障"></a>

## Troubleshooting

| Symptom | Check |
| --- | --- |
| The host reports `command not found` | Find the absolute path with `command -v amux` and put it in host configuration. |
| `amux mcp` reports that it is disabled | Run `amux system config set mcp.enabled true`. |
| The server connects but tools report an unreachable Hub | Run `amux system status`, repair the Hub/Runtime, and retry. |
| New tools remain invisible after changing host configuration | Restart the host completely so it restarts `amux mcp` and performs initialize/tools/list. |
| An isolated host reads the wrong configuration directory | Explicitly pass the correct `AMUX_HOME`; do not rely only on the isolated `HOME`. |
