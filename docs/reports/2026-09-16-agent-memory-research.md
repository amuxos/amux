# Agent memory research

[English](2026-09-16-agent-memory-research.md) | [简体中文](2026-09-16-agent-memory-research.zh-CN.md)

Research date: 2026-09-16. This supports the [amux on-demand memory design](../agent-memory-design.md),
not a description of current product behavior. Scope: locally installed
OpenWolf 1.0.4 and `<workspace>/.wolf`; OpenClaw official docs and a pinned source
revision; Claude Code official docs; Hermes official docs and a pinned source
revision; Codex official docs and schemas exported by local version 0.153.4.
Downloaded external project code was not executed. The installed Codex was used
only to export the protocol, without starting a model conversation or reading
or changing the user's existing memory content.

## 1. Findings

Memory storage and model context are distinct layers. Files or databases may
hold substantial content; only automatically loaded or tool-read content enters
model context. File read/search results also consume tokens, but there is no
requirement to prepend an entire library or changing snapshot to every user turn.

The common pattern suitable for amux is a short protocol, a small entry,
on-demand tool reads and timely writes. The Hub owns storage, isolation and
tools; the Agent decides when history is useful. Per-turn retrieval and
incremental injection are not prerequisites for memory.

It would be inaccurate to call every system injection-free: Claude loads a
small index, Hermes injects a small core snapshot, and OpenClaw has multiple
paths and curated/triggered recall exceptions. Adopt layering and on-demand
access while stating amux's chosen default explicitly.

## 2. Comparison

| System | Resident/startup information | Access to larger memory | Writes | Implication for amux |
| --- | --- | --- | --- | --- |
| OpenWolf 1.0.4 | AGENTS/CLAUDE entry loads a protocol that asks for scenario-specific cerebrum/anatomy reads | Agent file reads/Grep; hooks emit relevant reminders | Agent writes Markdown/JSON; hooks log some actions; daemon can consolidate | Protocol and tools can provide memory without projecting the entire store into the prompt. |
| OpenClaw native Codex | With memory tools, a small workspace-memory note replaces the MEMORY.md body | memory_search and memory_get excerpts | Workspace Markdown during work; lifecycle consolidation depends on the feature configuration | Closest to amux's native Agent integration through on-demand tools. |
| Other OpenClaw harnesses | Bounded USER/MEMORY bootstrap; runtime modes differ | Daily/topic files mainly retrieved on demand | During work, pre-compaction flush and background dreaming | Load only a small core; keep large history outside context; do not copy the entire runtime/background model. |
| Claude Code auto memory | Small MEMORY.md index loaded at startup | Standard file tools read topics on demand | Agent maintains topic files and index | One-line pointers, topic details, and an enforced index size limit. |
| Hermes built-in memory | Small MEMORY.md/USER.md snapshot frozen during ordinary turns | session_search queries SQLite history; skills loaded on demand | memory add/replace/remove; conditional background review | Separate universal preferences, history and procedures; preserve stable prefixes. |
| Codex local memories | When enabled, local memory can be injected into later conversations | This investigation did not establish precise loading sizes for every version | Background extraction from eligible idle historical threads | Govern native use, generation eligibility and CODEX_HOME to avoid two automatic authorities. |

## 3. OpenWolf: protocol, files and hooks

The local `/opt/homebrew/lib/node_modules/openwolf/package.json` identifies
`openwolf@1.0.4` and [cytostack/openwolf](https://github.com/cytostack/openwolf).
The findings below concern installed code and the user's actual `.wolf` files;
they do not assume that the latest remote release is identical.

- `<workspace>/AGENTS.md` references `.wolf/OPENWOLF.md` and reminds the Agent to
  read cerebrum/anatomy. It does not embed every memory/buglog file.
- `.wolf/OPENWOLF.md` requires cerebrum before coding, buglog before fixes and
  anatomy-first navigation. It discourages rereading unchanged files within a
  session and recommends targeted Grep and avoiding full-log reads for appends.
- `hooks/session-start.js` reads cerebrum locally to check entry counts and
  freshness, then emits reminders rather than the entire content to the Agent.
- `hooks/pre-write.js` matches Do-Not-Repeat rules and relevant historical bugs
  in the same file. It emits a few reminders, not a per-turn full-library search.
- `post-write.js` updates the file index, appends logs and heuristically records
  fixes. Semantic preferences and important decisions mostly rely on Agent writes.

The installed package also contains daemon consolidation.
`src/templates/cron-manifest.json` defines daily consolidation and weekly
cerebrum reflection. Ordinary consolidation in `dist/src/daemon/cron-engine.js`
mainly reduces old Session logs to action counts; this is not intelligent
summarization. AI reflection explicitly reads selected files and invokes
`claude -p`. Code presence does not prove that the local daemon or jobs are
running; their running state was not checked.

There is therefore code evidence for keeping most memory outside the prompt.
Actual read results and hook reminders still become model input; OpenWolf does
not imply fully automatic extraction at zero token cost.

## 4. OpenClaw: harness and version matter

Source inspection is pinned to
[67adae4b098f5bd84f9ccf069a2076d073c7b79b](https://github.com/openclaw/openclaw/tree/67adae4b098f5bd84f9ccf069a2076d073c7b79b).
Online docs were read on the research date and may change later.

The official [Memory overview](https://docs.openclaw.ai/concepts/memory) defines
`USER.md` as a small user model, `MEMORY.md` as curated long-term information and
`memory/*.md` as detailed notes retrieved through search/get. It also states
that oversize files are truncated in their injected copy, not deleted on disk.

The more specific [Workspace bootstrap injection](https://docs.openclaw.ai/concepts/system-prompt#workspace-bootstrap-injection)
explains that native Codex with memory tools receives a small workspace-memory
note directing it to `memory_search`/`memory_get`, instead of repeatedly pasted
MEMORY.md. There is a bounded-body fallback when tools are unavailable; other
harnesses still use ordinary bootstrap injection. The overview alone misses
this native-Codex distinction.

Source evidence includes [Codex branching](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/codex/src/app-server/attempt-context.ts#L222)
and the [workspace-memory note](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/codex/src/app-server/attempt-context.ts#L885).
The note treats MEMORY.md as a memory file to read through tools when relevant.
The separate [Memory Recall tool contract](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/extensions/memory-core/src/memory-tool-contract.ts#L88)
requires search followed by needed lines before answering about prior work,
decisions or preferences; it does not require a search for every ordinary turn.

`memory/*.md` is not ordinary-turn bootstrap Project Context. A bare `/new` or
`/reset` may add recent daily notes as one-time startup context. Memory imported
from other coding assistants is separately searchable rather than automatically
merged into bootstrap MEMORY.md.

[Memory search](https://docs.openclaw.ai/concepts/memory-search) describes hybrid
keyword/semantic retrieval. The current version also includes narrower
deterministic trigger recall, so not all memory bodies always require explicit
tool reads. Such recall is an optional future enhancement for amux, not a
prerequisite for this default design.

Writing and consolidation include:

- The Agent saves useful content to Markdown while working.
- [Automatic memory flush](https://docs.openclaw.ai/concepts/memory#automatic-memory-flush)
  runs a dedicated reminder/save turn before compaction on supported paths,
  using a private conversation copy. It needs a writable workspace and incurs
  model cost. [Skip conditions](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/src/auto-reply/reply/agent-runner-memory.ts#L1294)
  include CLI runtimes and runtimes with native compaction; native Codex does
  not universally receive this hook.
- The [session-memory hook](https://docs.openclaw.ai/automation/hooks/bundled-hooks#session-memory)
  saves excerpts of recent user/assistant text at new/reset boundaries. It does
  not call a model to generate a file slug by default. Excerpts are not semantic
  reflection.
- Current [Dreaming](https://docs.openclaw.ai/concepts/dreaming) provides background
  selection, consolidation and promotion. Older claims of no background
  consolidation are outdated. It has its own source filtering, scheduling and
  compute costs; amux need not adopt it in the first release.

OpenClaw agent/workspace/session/tool visibility differs from amux's
EffectivePrincipalID model. [Bootstrap filtering](https://github.com/openclaw/openclaw/blob/67adae4b098f5bd84f9ccf069a2076d073c7b79b/src/agents/workspace.ts#L1379)
excludes root MEMORY.md in contexts such as group chats, but withholding a file
from a prompt is not a filesystem multi-user ACL. A shared workspace directory
is not directly an implementation of same-user access across amux Runtimes.

## 5. Claude Code: small index and on-demand topics

According to [How Claude remembers your project / Auto memory](https://code.claude.com/docs/en/memory#auto-memory):

- Each repository's auto-memory directory contains a MEMORY.md index and topic
  files. Worktrees of that repository share memory; the native feature is local
  to one machine.
- Startup loads the first 200 lines or 25 KB of the index, whichever limit is
  reached first. Topic files are read on demand through standard file tools.
- The docs recommend a one-line index entry for each topic, moving detail into
  topic files and organizing the index as it approaches/exceeds the read limit.
- The Agent decides which information is useful later; not every Session must
  write memory. Typical content includes preferences, feedback, project context
  not evident from code, and external references.
- `@path` imports expand at startup and consume context. Splitting a large file
  into multiple imports does not make it load on demand.

amux should adopt the small entry and explicit reads while retaining Hub storage
for cross-Runtime sharing. Claude's local auto-memory directory alone does not
meet that requirement.

## 6. Hermes: frozen core, on-demand history and skills

Sources: [Persistent Memory documentation](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory)
and pinned revision [2e320e6d1a81bae96ac9b7ccf07d10c92120ca94](https://github.com/NousResearch/hermes-agent/tree/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94).

- Built-in MEMORY.md defaults to 2,200 characters and USER.md to 1,375 characters.
  Token figures in documentation are estimates rather than exact limits.
- [MemoryStore](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool_store.py#L67)
  distinguishes live entries from a system-prompt snapshot; [formatting reads
  the frozen snapshot](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool_store.py#L363).
  Writes during a conversation do not change ordinary-turn prefixes.
- [System prompt construction](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/system_prompt.py#L632)
  separates stable/context/volatile sections. [Post-compaction reconstruction](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/system_prompt.py#L707)
  rereads memory. Thus “frozen within the session” means ordinary turns remain
  stable, with refresh at natural reconstruction boundaries.
- [session_search](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/session_search_tool.py#L1)
  reads SQLite FTS5 history on demand with bounded expansion. Retrieval itself
  needs no additional LLM call, but returned text still consumes context.
- [Memory tool guidance](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/tools/memory_tool.py#L262)
  places broadly applicable facts in memory, procedures/pitfalls in skills and
  task progress/completion logs in history.
- [Background review](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/background_review.py#L1)
  can fork from the main conversation under qualifying conditions, at additional
  model cost; it is not unconditional on every turn.
- [Context breakdown](https://github.com/NousResearch/hermes-agent/blob/2e320e6d1a81bae96ac9b7ccf07d10c92120ca94/agent/context_breakdown.py#L121)
  displays a local memory-category estimate separately from actual Provider usage.

Hermes built-in files belong to a home/profile, and its docs discourage multiple
Agent processes sharing one home. External providers offer other capabilities.
amux should borrow layering and prefix stability while the Hub serializes
conditional writes and partitions directories by Principal; it should not sync
or concurrently write one native home.

## 7. Codex: separate storage can still conflict

Sources: OpenAI [Memories](https://learn.chatgpt.com/docs/customization/memories),
[Configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
and [App-server](https://learn.chatgpt.com/docs/app-server), compared with schema
export from local `codex-cli 0.153.4`.

### Verified documentation

- Local Codex memories are separate from ChatGPT web memory. They default to
  `~/.codex/memories/`, with the actual location determined by CODEX_HOME. Files
  are generated state; manually editing them is not the recommended primary
  control surface.
- Background extraction selects eligible old conversations, excluding active or
  too-short ones and waiting for sufficient idle time. The main Agent not
  explicitly writing memory files does not prove no memory is being generated.
- Current official defaults have `features.memories=false`. When enabled,
  `memories.use_memories` and `memories.generate_memories` default to true. The
  off-by-default feature does not mean every user's configuration disables it.
- `use_memories=false` skips injecting existing memory into future conversations;
  `generate_memories=false` keeps **new threads** from being stored as future
  generation inputs. That promise does not establish every old-thread migration.
- `memories.disable_on_external_context` defaults to false. When enabled, it
  excludes threads that used MCP, web search, tool search and similar external
  context. Its default cannot establish that amux results will not be extracted,
  and it is not a replacement for disabling generation for all managed threads.
- `/memories` controls the current chat's use/contribution without changing
  global settings. An interactive command does not prove an app-server switch
  has identical protocol semantics.

The conflicts concern contradictory preferences, re-extracted tool results,
Hub-deleted facts returning from a local copy and differing identity boundaries;
they are not simply two tools overwriting an identical tool name.

### Research baseline and subsequent implementation

Following official guidance, `codex app-server generate-json-schema
--experimental --out <temporary-directory>` exported start/resume/fork parameters
with `developerInstructions`, `baseInstructions` and `config` in version 0.153.4.
The short protocol should augment developer instructions, not replace native
base instructions. The schema also includes `thread/memoryMode/set` with
`enabled|disabled`, but does not explain use/generation/existing artifacts
separately. API presence was not treated as runtime behavioral verification.

At the research baseline, [startOrResumeThread](../../internal/provider/codex/proc.go)
did not pass those instruction/config fields. Process launch already preserved
extra `-c` arguments for MCP configuration. The subsequent implementation adds
[Codex memory integration](../../internal/provider/codex/memory.go): launch
controls, effective config checks, merged developer instructions and
`thread/memoryMode/set`. Protocol/unit tests do not replace live acceptance of
config precedence, managed subagents, resume or persisted generation exclusion.
A prompt saying “prefer amux” is not proof of compatibility.

At that baseline, [ordinary Runtime launch](../../app/runtime/server.go)
inherited service-process environment; [Linux DAC setup](../../internal/isolation/backend.go)
rewrote HOME/XDG without explicitly handling CODEX_HOME. [Task private-home](../../internal/task/sandbox.go)
had the same boundary and is not strong user isolation. If the service defines
a shared CODEX_HOME, changing HOME alone does not prove native-data separation;
actual access also depends on file permissions, so this did not establish an
observed leak. The implementation subsequently resets CODEX_HOME and removes
duplicate environment keys for isolated launches. That fixes the launch
boundary, not the need to verify permissions and credential provisioning.

### Chosen policy

Managed Codex conversations with amux memory use the Hub as the single automatic
long-term source. Supported launch controls disable native use and the thread's
generation eligibility before creation. Independently launched Codex retains
its existing policy. amux does not directly edit, sync or delete existing native
memory bodies or global config, although Codex may persist managed thread state.
A source-policy change uses a new native conversation; old-thread resume needs
verified provenance and persistent exclusion and cannot be described as clean
without that evidence.

The fixed protocol identifies `amux.memory_get("MEMORY.md")` as a logical tool
document, not a local MEMORY.md. Remember/correct/forget operate on the Hub.
Execution isolation still governs native-file access; disabling automatic
memory is not a filesystem ACL. Each other Agent needs its own verified controls;
this research does not establish universal Provider support.

## 8. Decisions for amux

The recommended default installs only stable instructions and tools. The Agent
reads a strictly bounded entry once in a new native conversation, searches/reads
when historical information is relevant and promptly writes explicit remember
requests and corrections.

Following the user's requirement, amux uses Markdown files on the Hub host:
`memory/global/` when non-isolated and `memory/users/<principal-key>/` under user
isolation. Search is a bounded literal keyword scan within the scope; get reads
by path, lines or byte offset. No SQLite, FTS5, vector service or persistent
index is added. Chinese matches contiguous text; synonyms need Agent query
reformulation. Content hashes detect conflicts; a Hub lock and same-directory
atomic replacement protect single-file changes without multi-file transactions.
Bindings are equality preconditions of `global` or
`principal:<effective-principal-id>`, without `policy_revision`; they do not
select authorization scope. CLI export returns one document and verifies
revisions across pages. Hermes's SQLite above is a research fact, not amux's
storage choice.

This avoids per-turn relevance queries, delivered-entry version tables, memory
context epochs and forced stop-reading after a cumulative 8k threshold. Ordinary
turns incur no memory-library-size or Hub-preflight cost. The tradeoff is that
recall and capture depend on the Agent following the protocol. Behavioral tests
must assess that before deciding whether limited automatic reminders are needed.

No Feishu memory count or token label, and no separate accounting pipeline, is
added. Tool-read content belongs to normal Provider input usage and the existing
token display remains applicable.

Each managed conversation uses one automatic long-term source, with native
behavior controlled at the integration layer. Separate directories or “amux
first” instructions cannot prevent background duplication and failed forgetting.
Old conversations and the actual CODEX_HOME identity boundary remain acceptance
requirements.
