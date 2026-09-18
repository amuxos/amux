# Profile-independent, on-demand memory

[English](agent-memory-design.md) | [简体中文](agent-memory-design.zh-CN.md)

Status: implementation design, updated 2026-09-17. The storage, Hub RPC,
CLI/MCP and launch integration are implemented in this change. This page
records their contracts, verified evidence and remaining acceptance work; it is not evidence that
live model behavior or every native Agent lifecycle has been verified. The
[current memory model](current/agent-memory-product-model.md) defines product
behavior. Background evidence is in the [research report](reports/2026-09-16-agent-memory-research.md).

## 1. Decisions

Memory works with or without an Agent Profile. One Hub owns ordinary UTF-8
Markdown files for the whole amux system. Non-isolated execution uses shared
global memory; user isolation partitions memory by authenticated effective
Principal. The same Principal can use it across Runtimes, Agents and workspaces.
Different Hubs do not synchronize automatically.

The experiment defaults to off. Feishu management → Experiments → Agent memory
and `amux system config set memory.enabled true|false` edit the same Hub setting.
Save and restart to apply it across the system; remote clients and Runtimes do
not need a second local opt-in. Enabling affects new native conversations.
Disabling rejects memory data calls and new bindings while retaining files.
Existing bound conversations retain the fixed protocol and native-memory
suppression on resume; old context cannot be withdrawn or made safe for native
extraction by turning access off. Their data calls fail until re-enabled with
a compatible scope. MCP startup checks Hub capability with a bounded request,
without per-turn lookups. Newly created conversations while disabled use their
Agent's ordinary native-memory policy.

The Runtime installs a short, stable protocol and tools. The Agent reads a tiny
logical `MEMORY.md` entry once in a new native conversation, then searches and
reads topic notes only when useful. The Runtime does not retrieve memory on
every turn, insert changing snapshots, or track delivered memory revisions.
Reading a file through a tool still consumes model input tokens; unread files
do not enter the model context.

No memory database, persistent search index, vector service, background
reflection model, duplicate conversation journal, or per-turn retrieval is
introduced. Existing Timeline data remains execution history. The Agent writes
only useful preferences, decisions and verified lessons to memory.

## 2. Storage and content

```text
<AMUX_HOME>/memory/                 # on the Hub host
  global/
    MEMORY.md
    preferences/communication.md
    projects/amux/decisions.md
  users/<sha256(principal-id)>/
    MEMORY.md
    projects/amux/decisions.md
```

The safe directory key is derived by the service, not supplied by the Agent.
Global and user directories do not inherit, merge or migrate automatically.
All Runtimes call the same Hub; no shared mount or Runtime replica is required.
One Hub is the sole online writer of a memory root. Offline inspection, editing
and backup use normal text-file tools; online changes use the Hub APIs or CLI.

`MEMORY.md` contains a few broadly useful preferences and pointers. It is not a
catalog of every file. Details belong in topic documents. A project path
expresses relevance, not an authorization boundary; equal directory basenames
on different machines do not establish project identity. Sources, dates and
Session/Turn references can be ordinary Markdown text, without metadata tables.

`revision` is the SHA-256 hash of the whole file, not an incrementing counter.
Writes and deletes require that hash as `expected_revision`; creation requires
`absent`. The Hub locks each scope, checks the current revision and quotas, then
writes a temporary file in the destination directory, syncs it, atomically
renames it and syncs the directory. Deletes also sync their parent directory.
Conflicts return the observed revision so the caller can reread and merge.

An already missing document returns `exists=false` and `revision="absent"` on
read; forget reports `already_absent=true`. A successful write is acknowledged
only after persistence. An uncertain RPC result requires rereading; there is no
persistent idempotency ledger or exactly-once claim. Multi-file edits are not
transactions, and searches are not a whole-directory snapshot.

## 3. Search and read APIs

| Tool | Contract |
| --- | --- |
| `amux.memory_search(query, path_prefix?, limit?)` | Scan the authorized scope for literal keywords and return bounded matches. |
| `amux.memory_get(path, start_line?, end_line?, offset?)` | Read a bounded UTF-8 fragment, whole-file revision and continuation metadata. |
| `amux.memory_upsert(path, content, expected_revision)` | Conditionally replace the complete document, or create with `absent`. |
| `amux.memory_forget(path, expected_revision)` | Conditionally delete one document; does not erase conversation history. |

Search uses Go directory traversal and string matching, with no external `rg`
requirement. The query is split on whitespace into literal keywords; English
matching ignores case and Chinese matches contiguous text. It does not interpret
regular expressions, globs or shell syntax. A document matching any keyword is
a candidate. Ranking uses the number of distinct matched keywords, then
path/title matches, then stable path order. The result contains relative path,
snippet, line and revision; it is not an LLM-generated summary.

Search defaults to five matching documents, with a maximum request of twenty.
`incomplete` indicates the scan stopped before covering the scope;
`scanned_files` reports the scanned file count; response-level `truncated`
indicates matching results were omitted for result/output limits. Each match
also has its own snippet `truncated` flag. An incomplete empty result is not
proof that the complete library has no match. Narrow `path_prefix` or change
keywords when needed. Synonyms require the Agent to retry with other words;
there is no tokenizer, FTS5, BM25 or embedding index.

Get accepts 1-based inclusive line ranges. A zero/omitted start begins at the
start; a zero/omitted end reads until the response limit. Continue a truncated
fragment using `next_offset` as `offset`, retaining the same path/end line and
checking the revision. Do not combine a nonzero start line and offset. There is
no section-name parameter. Logical paths must be relative Markdown paths;
absolute paths, traversal and symlinks are rejected. Searches do not follow
symlinks. The Agent cannot choose another scope or Principal.

The equivalent CLI is `amux memory search|show|upsert|forget|export`. Search/show
return bounded JSON. Upsert reads raw UTF-8 Markdown from `--file` or stdin,
with mandatory `--expected-revision`; forget also requires that flag. Export
reads one complete document to stdout, checks that revisions remain identical
across pages, and emits nothing if a conflict interrupts export. It is not a
whole-library archive. Agents without MCP use the CLI. Configured MCP tools
retain the existing generic `amux.result_continue` envelope when a response
exceeds the MCP page budget.

## 4. Fixed protocol and normal behavior

The executable source is [memoryprotocol](../internal/memoryprotocol/protocol.go).
Its instructions say to:

- Read the small logical `MEMORY.md` entry once in a new conversation and reuse
  it while still in context; these paths belong to amux tools, not local files.
- Search before answering questions about earlier work, decisions, preferences,
  agreements or repeated problems; read only relevant excerpts. Ordinary tasks
  that need no history do not need a search.
- Save explicit requests to remember, stable corrections and reusable verified
  lessons. Keep details in topics and keep the entry small.
- Read before updating, pass the revision, and resolve conflicts by rereading
  and merging. Report persistence only after a successful tool response.
- When correcting or forgetting, update both the entry and related topics so
  duplicate stale content and references do not survive. Report partial failure.
- Never save secrets, temporary authorization or unverified guesses. Current
  instructions and verified facts take priority; memory grants no authority.

The protocol names MCP tools or their CLI equivalents according to available
capabilities. It contains no memory body, timestamp or per-turn retrieval state.
It does not rewrite repository AGENTS.md/CLAUDE.md, depend on `.wolf`, or require
a builtin Pack. User-maintained project instructions remain project guidance.

One forget RPC only removes one file. A natural-language request to forget a
preference may need several conditional edits. Removing a file cannot erase
text already read by a native conversation; use a new conversation when that
old context must be left behind. Other live conversations see edits on their
next read; the service does not push changed preferences into every model.

## 5. Runtime integration and prefix reuse

[Runtime integration](../app/runtime/memory.go) resolves a binding once for a
fresh execution and stores it in Session launch state. Resumes retain it. An
unbound legacy conversation does not silently adopt the current memory policy.
Discovery failure produces a launch warning and leaves that conversation
without memory; it does not block ordinary Agent work or fall back to global.
The failure does not create a temporary Runtime memory store.

| Agent | Integration in this change |
| --- | --- |
| Codex | Merge the short protocol with effective developer instructions and send `developerInstructions`; retain native base instructions. Require supported config and thread-memory-mode calls. |
| Claude | Merge existing append instructions into one `--append-system-prompt`; set launch-local `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`. |
| Pi | Use `--append-system-prompt` with the equivalent CLI protocol. |
| Other Providers | Report the missing memory capability; do not add a per-turn prompt fallback. |

The protocol and tool schemas remain stable within a native conversation.
Memory bodies arrive as tool results appended to the conversation. Editing the
store does not rewrite earlier messages or the instruction prefix. This avoids
memory-induced prefix changes, but actual cache hits still depend on Provider
serialization, routing, TTL and compaction.

Native Agents own compaction. After compaction, missing relevant context can be
read again. There is no memory context epoch, per-entry delivery ledger, or
cumulative 8k-token reading cutoff. No hidden flush turn, shadow conversation
or second completion gate is added. Instructions cannot guarantee that an
Agent writes every useful fact before a crash.

Linux DAC tool access uses the existing target-UID materializer to copy the CLI
and minimal connection configuration once per isolated launch into
`<run>/agent-config/memory/`. This keeps the service account's private installation
and config inaccessible. Launch-local PATH and MCP settings select that copy;
ordinary run cleanup removes it. This costs one binary copy per isolated launch.
Other launches, including private-home Tasks, resolve the installed CLI. A
missing executable is an explicit launch error. The Claude sidecar maps the
merged append instructions to the SDK's supported `systemPrompt.append` field.

## 6. Bounds

| Resource | Current limit |
| --- | --- |
| `MEMORY.md` | 2 KiB UTF-8 and 64 lines |
| Topic document | 64 KiB |
| Scope | 1,024 documents and 16 MiB |
| Search query | 512 bytes and 1–16 whitespace-separated terms |
| Search scan | 1,024 files, 8 MiB, 250 ms; directory-entry limits also bound traversal |
| Store response | 8 KiB serialized output budget |
| MCP response page | Existing 6 KiB budget, with generic continuation |

Limits are bytes/lines, not exact token counts. Entry overflow requires moving
details into a topic; the service does not silently drop important conditions.
Repeated useful reads still accumulate context and use the Provider's normal
context management. Feishu titles keep existing Provider usage totals; this
change adds no memory count, token label or separate accounting event.

## 7. Identity and frozen scope

The Hub derives scope from its configured isolation policy and authenticated
actor. Non-isolated memory is `global`; isolated memory is
`principal:<effective-principal-id>`. A service execution uses its effective
service Principal. CLI/Feishu aliases for one person must resolve to the same
Principal to share isolated memory. Task, Instance, Schedule and child execution
use their authorized effective identities rather than an arbitrary parent ID.

`AMUX_MEMORY_BINDING` carries that non-secret string as a lifecycle/equality
precondition, not as authentication or a scope selector. There is no
`policy_revision` field. Hub actor credentials and isolation policy remain the
authorization boundary. The Hub authenticates every call and rejects a
mismatching binding. Freezing is
a managed Runtime lifecycle guarantee; the marker is not cryptographic proof of
thread provenance and cannot stop an authorized custom client from deliberately
requesting its current authorized scope. Delegated
Agent credentials require a managed binding; ordinary enrolled CLI callers
may omit it and operate in their current authorized scope. Session visibility
alone does not grant access to the owner's entire memory.

Helper health, connected targets and a passthrough fallback never select global
memory when isolation policy is enabled. Missing/invalid identity is rejected.
Changing isolation policy does not copy files or make old context public; an
incompatible binding fails and requires a new conversation. File permissions
and actual execution isolation must also prevent direct filesystem access to
another Principal's data. The short protocol is not an ACL.

## 8. Native memory compatibility

[Codex documentation](https://learn.chatgpt.com/docs/customization/memories)
describes background extraction from eligible old conversations and loading
into later conversations. Separate file paths alone do not prevent contradictory
preferences, duplicated MCP results or deleted facts returning from a native
copy. Managed amux memory therefore chooses one automatic long-term source.

The [Codex integration](../internal/provider/codex/memory.go) sets these values
for the managed launch and thread, without editing global config:

```toml
[features]
memories = false
[memories]
use_memories = false
generate_memories = false
```

It reads effective config before installing merged developer instructions and
requires the returned values to confirm native memory is disabled. It also
calls `thread/memoryMode/set` with `disabled`; an unsupported API fails clearly.
An isolated offline probe with Codex 0.153.4 confirmed that `config/read` returned
all three memory settings as false, `thread/start` accepted `developerInstructions`,
and disabled thread mode persisted in native state, without a model turn.
Its [native extraction selector](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/state/src/runtime/memories.rs#L234)
requires enabled mode. Protocol tests cover request/failure paths. This evidence
does not establish all background-worker behavior, native subagent inheritance
or compatibility with other versions.

The official configuration promise for `generate_memories=false` concerns new
threads. An old unbound conversation is not automatically made clean by a
launch flag or fork. amux does not migrate, import, delete or rewrite existing
native memory files, and independent Codex launches retain their own policy.
Memory-source changes need a new native conversation. Replaying old text must
not be represented as erasing previously read memory.

Linux DAC and Task private-home launch code reset inherited `CODEX_HOME` and
`PI_CODING_AGENT_DIR` to the corresponding home, clear `CLAUDE_CONFIG_DIR` to
retain its default isolated-home layout, and remove duplicate environment values. Private-home remains
weak isolation, not a per-user filesystem ACL. Credential provisioning must
stay explicit; copying the service user's entire `.codex` or falling back to a
shared home is not a memory compatibility solution. Disabling automatic memory
does not prevent readable-file access or custom hooks/extensions from writing
other memories. Claude disables native auto memory, including persistent
subagent memory, with `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`. The SDK inherits the
sidecar's environment after backend isolation without a pre-isolation override.
Pi has no built-in automatic long-term memory in the reviewed core version;
ordinary instructions, session history and extensions remain enabled. Custom
memory extensions need their own compatibility review. See the dated
[native compatibility evidence](reports/2026-09-17-native-memory-compatibility.md).

## 9. Acceptance and remaining evidence

Automated acceptance covers the file store, scopes, path confinement, revision
conflicts, quotas, bounded search/read, CLI/MCP forwarding, launch binding,
instruction merging and native-control request/failure paths. The existing
process E2E exercises memory CRUD, search, paged export and stale-revision
rejection through separate CLI invocations against both public and internal
binaries in the final gate. The offline Codex 0.153.4 probe establishes the
configuration, instruction and persisted-mode contracts described above.
Required live model evidence remains separate: explicit remember/correct/forget
across fresh conversations, no unnecessary recall on unrelated turns, native
clear/resume/compaction behavior and native child-agent policy inheritance.
The offline probe did not run model turns; mocks and schema fields alone are
not evidence of those behaviors.

Validate the public/internal integration with the repository's final gate after
the diff is frozen. No delivery or runtime compatibility claim follows merely
from this design page. Future semantic search or limited automatic reminders
require observed scale/recall problems and a separate decision; Markdown files
remain authoritative.
