# How amux works

[English](architecture.md) | [简体中文](architecture.zh-CN.md)

amux keeps Agent conversations running independently of the terminal or chat
window used to access them. One system can run locally or distribute execution
across several machines.

```text
Feishu / CLI / terminal UI / external MCP host
                    |
                amuxd (Hub)
                    ^
          outbound connection from Runtime
                    |
             amuxlet (Runtime)
                    |
         Codex / Claude / Pi process
```

## The Hub coordinates; the Runtime executes

The Hub tracks available Runtimes, routes requests, schedules work and enforces
control-plane authorization. A Runtime launches Agent processes and manages
their Sessions and workspaces. Task runners live on the selected Runtime.
The Runtime connects outward to the Hub through a reverse gRPC tunnel.

On one machine you still need both Hub and Runtime. `amux setup local` configures
that topology. You do not need Feishu for CLI, Task or Schedule use. Pack and
credential management and local recovery have operations that work without the Hub.

Operators manage one system through `amux system`, while Hub and Runtime remain
separate services. One installation can host multiple Bot Profiles; each Bot's
access policy is independent of where the system runs. Bot access does not grant
system maintenance authority. Separate installations remain an option when
process, data or failure isolation is required.

## The interface is not the conversation

Attaching a terminal creates a viewer and input surface for an existing Session.
Detaching does not terminate it. The optional Feishu Bridge maps messages to
Sessions and renders structured progress, results and permission requests.
`amux mcp` lets an external host access amux tools over stdio; it is not an HTTP
endpoint on the Hub.

## Know where your data and credentials live

Installation state defaults to `~/.amux/` (`AMUX_HOME` can change it). Execution
workspaces and Agent processes belong to the selected Runtime, so a path on your
laptop is not automatically a path on a remote worker. Native Agent login must
be usable where that Agent executes. Optional amux memory is owned by the Hub;
it is disabled by default and scoped by the Hub's identity policy.

## What amux adds

Agent Profiles describe launch policy; Packs distribute Profiles and resources.
amux adds persistent Sessions, bounded Tasks, schedules, routing, authorization
and shared interfaces. The underlying Agent still determines model access and
many execution capabilities. Public built-ins are `builtin/codex`,
`builtin/claude` and `builtin/pi`.

Continue with [Sessions, Tasks and Schedules](execution.md),
[remote Runtimes](../guides/remote-runtime.md), or the maintainer
[Agent contract](../current/agent-adapter-product-model.md).
