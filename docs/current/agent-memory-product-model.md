# Agent Memory Product Model

[English](agent-memory-product-model.md) | [简体中文](agent-memory-product-model.zh-CN.md)

Current implementation of profile-independent memory. Native compatibility was
checked with Codex 0.153.4; model adherence remains dependent on the Agent.

The Hub owns long-term memory independently of Agent Profiles and Runtimes.
`memory.enabled` is an experimental feature, disabled by default. The Feishu
management experiments page controls this single Hub-wide switch; CLI users can
set it with `amux system config set memory.enabled true`. Save and restart the
Hub to apply it; remote Runtimes need no separate opt-in. Memory uses UTF-8 Markdown files under the
Hub's `AMUX_HOME/memory`, with no memory database or persistent search index.
Non-isolated installations use `global/`; when the Hub's user-isolation policy
is enabled, the authenticated effective principal owns a separate `users/`
directory. Runtime availability and helper readiness never select a global
fallback. Session access alone does not authorize access to another principal's
memory. Service executions use their effective service principal.

The CLI and MCP tools search, read, conditionally write and forget documents
through the Hub. Paths are relative to the server-derived scope. Absolute paths,
traversal and symlink escapes are rejected. Search matches literal keywords and
returns bounded snippets; scanning limits are reported as incomplete results.
Reads return bounded text, continuation information and a whole-file hash.
Writes use that hash as a precondition, or `absent` for creation. A per-scope
write lock and same-directory atomic replacement prevent lost concurrent
updates. Multiple-file edits are not a transaction. Deleting a document does
not erase already-read conversation history or duplicates in other documents.

Supported Agent launches receive one stable memory protocol and a frozen scope
precondition. New native conversations read a small `MEMORY.md` through tools;
other material is read on demand. There is no per-turn automatic retrieval or
prompt rewriting. The protocol is independent of profile projection. Existing
unmanaged native conversations do not silently adopt a new memory policy.
The managed Runtime keeps the scope fixed for the conversation; requests with
an incompatible frozen scope fail. The binding is a non-secret client precondition,
not an authorization token: every request is independently authorized by the Hub. Normal CLI users may manage their current authorized scope.

Managed Codex conversations disable native automatic memory use and generation
with launch-local controls. Claude disables automatic memory, including native
subagent persistent memory, through its supported environment switch. The reviewed
Pi core has no built-in automatic long-term memory; custom memory extensions
require separate review. Other providers need a verified instruction and
native-memory contract before adopting amux memory. User global configurations and native memory files
are not directly edited or migrated. Isolated native home paths must not inherit
shared `CODEX_HOME`, `CLAUDE_CONFIG_DIR` or `PI_CODING_AGENT_DIR` overrides. Provider input usage already includes returned memory;
Feishu titles do not add a memory counter.

Memory requires a runnable amux CLI on the Runtime. Linux DAC launches copy the
CLI and minimal Hub connection config into the current run's
`agent-config/memory/` through the existing target-UID materializer; shell and
MCP clients use that copy. The ordinary run cleanup reclaims it. Other launches
use the installed CLI. Missing tools fail visibly before starting the Agent.

Disabling the experiment rejects all memory data RPCs and new bindings while
retaining stored files. Existing conversations keep their frozen protocol and
native automatic-memory suppression, including on resume: disabling access must
not make their history eligible for native extraction. They cannot read or write
amux memory while the Hub switch is off. MCP discovery checks Hub capability
once at startup; every data call still enforces Hub policy. Re-enabling permits
existing compatible bindings and new conversations; legacy unbound conversations
need a new conversation. Saving settings without restarting leaves the running
policy unchanged.

If tools report memory unavailable, the fixed protocol directs the Agent to
continue without recall or repeated retries, and never fall back to another
automatic-memory store. Existing bound launches retain the CLI dependency even
while access is disabled. Cached MCP memory continuations also check the Hub
switch before returning another page.
