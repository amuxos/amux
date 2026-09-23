# Assistants, experts and Agents

[English](agents.md) | [简体中文](agents.zh-CN.md)

In Feishu you talk to an **assistant**, represented by a bot. In its settings you
select an **expert** and an execution engine. These choices have different identities:

| Concept | Meaning | Identity and lifetime |
| --- | --- | --- |
| Bot Profile | Feishu app/bot connection, owner and interaction settings | A bot configuration; can serve multiple conversations |
| Agent Profile (expert) | Instructions, resources and allowed launch configuration | A reference such as `builtin/codex`; reusable definition |
| Agent type / engine | Native implementation that executes work | Codex, Claude or Pi; not one specific conversation |
| Runtime target | Host process that launches and manages Agents | Target ID; a machine/execution location |
| Session | An ongoing managed conversation | Session ID on a target; contains multiple turns |
| TaskRun | One bounded delegated execution | Task ID; tracks the child Session/turn and final result |
| Agent Instance | Persistent identity for follow-up delegated work | Instance ID; spans multiple TaskRuns |

Selecting the same expert twice does not reuse a conversation. Two bots can use
the same expert without becoming one bot. A single bot can route to several
Sessions. An instance and its individual task IDs remain distinct.

## What is preserved when something changes

- A new turn in a Session keeps its conversation context, subject to the Agent's
  context limits and compaction.
- A new independent topic in thread mode starts its own Session; only explicitly
  supplied topic context, material, memory or workspace resources cross boundaries.
- A new TaskRun on a persistent instance can reuse its provider history and durable
  environment. Its result and execution status still belong to that new run.
- Changing the default expert affects new launches. It is not a migration of every
  existing Session, identity, workspace or credential.

Agent behavior also depends on the selected model and available tools. An expert
Profile is not a credential, a model subscription or an authorization grant.

Next: [conversation and work](execution.md), [delegation](../guides/delegation.md),
and the maintainer [Agent model](../current/agent-adapter-product-model.md).
