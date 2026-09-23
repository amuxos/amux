# Use memory across conversations

[English](memory.md) | [简体中文](memory.zh-CN.md)

amux memory stores verified preferences, agreements and reusable lessons as
Markdown documents on the Hub for authorized Agents to read on demand. This guide
covers enabling it, verifying persistence, recalling in a new conversation,
correcting/forgetting and understanding shared scope.

## Distinguish memory from other context

| Content | Location and use | Does a new conversation automatically have it? |
| --- | --- | --- |
| Native Session history | Current Agent conversation and resumable history | A new independent Session does not copy it all |
| Feishu topic context | Bounded relevant messages selected for an input | Depends on topic, selection and budgets; not complete group history |
| Workspace files | Selected Runtime's filesystem | Depends on cwd and access; not memory documents |
| amux memory | Hub scope selected by effective identity | Read through the memory protocol and tools, not copied wholesale |

Changing expert or Runtime does not automatically delete memory. A new topic is
not a delete operation either. Conversely, mentioning something in a previous
chat does not prove persistence: only a successful write result does.

## Enable it as the owner

1. In private `/amux` → experiments, the owner enables Agent memory.
2. Save, review the restart notice and confirm a system restart at a suitable time.
3. Start a new conversation or use the relevant management action to create a
   new continuous conversation.
4. Ask the assistant to read the memory entry and report availability; the switch's
   displayed value alone is not proof that access works.

Memory is off by default and uses one Hub-wide `memory.enabled` switch. Remote
Runtimes need no separate opt-in. Saving without restarting leaves the running
policy unchanged. Existing conversations without a memory binding do not silently
migrate: start a new conversation after first enablement instead of assuming a
restart of an old Session adds the protocol.

Supported managed launches cover Codex, Claude and Pi. The Runtime needs a runnable
amux CLI to access Hub memory; missing tools produce errors or unavailability
notices. Other Providers cannot be assumed compatible merely from a similar interface.

## Verify “save → read in a new conversation”

### Request a specific persistent fact

Start with a real, verified, nonsensitive agreement, for example:

> Save this confirmed agreement in amux memory: our project review reports must
> contain conclusion, evidence and unresolved questions. First check existing
> records, then update `topics/review-format.md` and, if useful, add a short pointer
> in `MEMORY.md`. Report the paths and saved content; explicitly say it was not
> saved if the write fails.

These are logical memory-tool paths, not instructions to create same-named files
in the code repository. Read existing documents first to retain other agreements
and obtain the current revision.

### Verify persistence, not just the reply

Ask the assistant to read back the path after writing. Then start an independent
conversation and ask:

> Find our agreed review-report format first, tell me the memory path you read,
> and apply that format to the material below.

This checks tools, scope and cross-conversation use. If recall fails, inspect
identity, binding and search terms instead of insisting it must remember. An
unfinished write or text present only in a previous chat is not stored memory.

Operators can inspect documents in their own authorized scope:

```sh
amux memory search "review report"
amux memory show topics/review-format.md
```

The CLI uses its authenticated identity's scope. Troubleshooting does not make it
automatically access a particular Feishu user's private memory.

## When the Agent reads and saves

Managed launches provide a fixed protocol: read the small `MEMORY.md` entry at the
start of a new native conversation; search relevant excerpts for prior work,
preferences, agreements or repeated issues; save explicit remember requests,
stable corrections and verified reusable lessons as needed.

This is a tool-use protocol for the Agent, not Hub-wide full-text retrieval or
prompt rewriting on every turn. Verify adherence through tool results; turns
that do not need historical information need not search all memory.

Memory grants no operation permissions and cannot override current instructions
or newly verified facts. Secrets, temporary authorizations and unverified guesses
must not be stored; the managed memory protocol explicitly states that boundary.

## Organize content and handle read limits

Keep a few general preferences and pointers in `MEMORY.md`; put details in topic
documents. For example:

```text
MEMORY.md
projects/example/release-checklist.md
topics/review-format.md
```

`projects/example` is an organizational example, not an authorization boundary.
Current limits are:

| Item | Limit or behavior |
| --- | --- |
| Root `MEMORY.md` entry | At most 2 KiB and 64 lines; short preferences and pointers |
| One document | At most 64 KiB; split details by topic |
| One scope | At most 1024 files and 16 MiB storage |
| Search | Literal keywords and bounded excerpts, not vector semantic search |
| Reads | Output budget can require continuation offsets; the first page is not necessarily the whole file |
| Paths | Relative to authorized scope; absolute paths, `..`, hidden components and unsafe links are rejected |

The entry budget applies only to the exact root filename `MEMORY.md`. Move details
and retain pointers when it grows too large instead of inserting an entire report.
Search can be incomplete due to scan budgets. Inspect notices and try known paths
or appropriate keywords before interpreting empty/truncated results as no record.

## Who shares the same memory

| Installation/execution identity | Scope |
| --- | --- |
| Hub user-isolation policy disabled | Global memory shared across Runtimes, not automatically divided by Profile or Feishu group |
| Hub user-isolation policy enabled | Separate scopes for authenticated effective Principals |
| Service execution | The service Principal's scope, not automatically the private memory of the person clicking a card |
| Same identity changes expert or Runtime | Hub identity policy still selects scope; names or machines do not create a new partition |

A new private chat, expert or group is not a substitute for privacy isolation.
An operator must configure identity policy for cross-user isolation. Session
access alone also does not authorize another Principal's memory.

Managed conversations freeze a memory-scope precondition at creation and retain
it across recovery or persistent-instance reuse. An incompatible identity/policy
change fails rather than falling back to global memory. Have the operator inspect
it and establish an appropriate new conversation.

## Correct or forget

A correction might say:

> Change “unresolved questions” in our review format to “risks and items to confirm”.
> Read the current document first, preserve other valid content, update related
> topic documents and entry pointers, and report the paths actually changed.

A forgetting request might say:

> Forget the obsolete release agreement. Check `MEMORY.md` and related topic
> documents for copies and pointers. Remove only that agreement, retain unrelated
> content and report which changes succeeded or failed.

Writes/deletes require the current revision; creation uses `absent`. If another
execution changed the file, an old revision conflicts. Re-read and merge before
writing instead of overwriting the latest agreement. Multi-document edits are
not a transaction, so partial success must identify remaining work.

Deleting a document does not erase content already read into the current
conversation or copies in other files/systems. Start fresh context if the Agent
must stop using a just-read obsolete fact. Conversely, `/clear` alone leaves Hub
memory documents intact.

## Disable and re-enable

Disabling the experiment and restarting rejects memory-tool access while retaining
files. Existing managed conversations retain their protocol and native automatic
memory suppression; they do not fall back to another automatic store.

Re-enabling restores compatible existing bindings; old unbound conversations
still need replacement. Managed Codex and Claude launches use supported local
controls to disable native automatic memory. The reviewed Pi core has no equivalent
automatic long-term memory; custom extensions need separate evaluation. amux does
not directly migrate users' native memory files.

## Troubleshooting

| Symptom | Check or action |
| --- | --- |
| Switch enabled but tools unavailable | Completed Hub restart, fresh conversation, runnable Runtime CLI |
| No recall in a new conversation | Successful write, same scope, actual search/read tool calls |
| CLI can see a document but Feishu cannot | Compare effective identity and managed binding; scopes may differ |
| Save conflicts | Re-read the revision and merge, not repeated submission with the old value |
| `MEMORY.md` is too large | Move details to topic documents and retain short pointers |
| Deleted agreement still appears in a response | It may already be in context; inspect copies and start fresh context |
| Agent claims persistence while disabled | Request successful tool evidence; unavailability means unsaved, not a switch to another store |

See the [memory model](../current/agent-memory-product-model.md) for the contract
and [CLI](../reference/cli.md) / [MCP](../reference/mcp.md) references for parameters.
