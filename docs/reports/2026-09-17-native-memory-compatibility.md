# Native Memory Compatibility Evidence

[English](2026-09-17-native-memory-compatibility.md) | [简体中文](2026-09-17-native-memory-compatibility.zh-CN.md)

Evidence reviewed on 2026-09-17. This report describes compatibility boundaries;
the [Agent model](../current/agent-adapter-product-model.md) defines current behavior.

## Claude

Claude's [memory documentation](https://code.claude.com/docs/en/memory#enable-or-disable-auto-memory)
documents `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` as a process environment switch.
Its [subagent documentation](https://code.claude.com/docs/en/sub-agents#enable-persistent-memory)
explicitly says that disabling auto memory also makes a subagent's `memory`
field ineffective: memory instructions and additional memory tool access are
not added. The locally installed Claude Code 2.1.274 executable also contains
the environment check before the `autoMemoryEnabled` settings lookup. This is
documentation and local executable evidence, not a live-model validation.

amux applies that switch on every managed launch and resume, and sends the
memory protocol through the SDK's additive system prompt. User-authored
CLAUDE.md instructions remain active. Previously read content cannot be removed
from a native conversation by changing an environment variable; unmanaged
native conversations therefore require a new conversation to adopt amux memory.

`CLAUDE_CONFIG_DIR` overrides the native directory. Isolated launches explicitly
clear it so Claude uses the isolated HOME's default `.claude/` and `.claude.json`
layout, preserving existing isolated native paths. The SDK inherits the
sidecar's effective environment after backend isolation. Replaying the
Runtime's pre-isolation environment in `session.start` would reintroduce shared
native directories, so the provider no longer sends that redundant overlay.
Linux DAC launch and cleanup also skip service-user workspace trust writes for
built-in agents; those files do not belong to the effective principal.

## Pi

Reviewed upstream Pi 0.85.1 at
[`e4c75a7`](https://github.com/badlogic/pi-mono/tree/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent).
Its core resource loader loads project instructions, SYSTEM/APPEND_SYSTEM files,
skills and extensions; its system prompt and settings have no built-in automatic
long-term-memory feature equivalent to Codex or Claude. Session persistence and
compaction are conversation history and remain enabled.

The [configuration path implementation](https://github.com/badlogic/pi-mono/blob/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent/src/config.ts)
uses `PI_CODING_AGENT_DIR`, defaulting to `~/.pi/agent`. Isolated launches pin this
directory beneath their own HOME. amux adds its stable CLI memory protocol.

Pi's [documented project-trust override](https://github.com/badlogic/pi-mono/blob/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent/README.md#project-trust)
is `--approve` (`-a`). Linux DAC launches use it to load the managed workspace's
project resources without editing the service user's trust store. Explicit
`--approve`/`--no-approve` or short forms take precedence. Private-home launches
retain their default trust-file behavior under the isolated HOME.

Pi extensions can add independent memory behavior. There is no verified universal
memory-only switch for arbitrary extensions. `--no-extensions` disables general
extension discovery and still permits explicitly selected extensions; amux does
not apply it automatically. Compatibility with a custom memory extension must be
assessed for that extension; the core review does not establish such compatibility.

## Additional providers

An app-server-compatible or ACP transport does not establish native memory
capability. Providers without a verified instruction and native-memory contract
remain unavailable for amux memory and keep their ordinary launch behavior.
They do not receive Codex-specific configuration or protocol calls by analogy.

## Disabling the amux experiment

The Hub is authoritative for the experiment switch. Disabling it rejects memory
data operations and prevents new conversations from adopting memory. Existing
bound conversations keep their original binding, protocol and native-memory
suppression on resume, preventing an implicit switch to a second memory store.
The protocol tells the Agent to continue ordinary work when memory tools are
unavailable and to report that remembering/forgetting did not complete. No
per-turn lookup, prompt rewrite, or extra frozen lifecycle state is introduced.
MCP startup may query Hub capability to hide unavailable tools; already-running
clients still encounter the Hub's data-operation gate.

Focused tests cover Claude's disable setting on fresh and resumed launches,
preserving unmanaged configuration, native-directory override removal in
private-home/Linux DAC environments, and the Claude sidecar environment boundary.
