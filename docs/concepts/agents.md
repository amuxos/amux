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

## How experts, Skills and libraries fit together

Think of an expert as a person with particular Skills. The expert defines its
responsibility, working requirements and available resources. A Skill supplies a
reusable method, instructions, and supporting scripts or templates for a kind of work.
One Git **expert library (Agent Pack) can contain several experts and shared Skills**.
After installing a library, choose an assistant default in `/amux` settings, or
choose different experts for particular groups or tasks.

To create one, enter “Keep exploring” in the `/amux` guided setup and provide its
name, responsibility, remote Git URL and Skill requirements. Have the Agent inspect
the existing library, import complete version-pinned Skill packages or write a new
Skill, then preview both expert references and actual file changes. After confirmation,
follow the repository's normal publish, merge and sync workflow. Shared Skill updates
must identify every affected expert; adding an expert must preserve the others.

A Git URL identifies the library, rather than a local directory on an execution
host. Expert content does not grant account access; calendar and knowledge Skills
still need tools and resource permissions. Verify adopted revisions in a new Session;
existing work retains its launch snapshot. See the
[capability plaza](../guides/capability-plaza.md#libraries) and
[Agent Pack format](../reference/agent-profile-format.md).

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
