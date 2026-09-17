# Agent and Adapter Product Model

[English](agent-adapter-product-model.md) | [简体中文](agent-adapter-product-model.zh-CN.md)

Codex, Claude and Pi are public integrations. Adapters describe executable
discovery, launch configuration, protocol capabilities and workspace configuration
layout. Native agent authentication and model settings are used by default.
External model providers may be configured explicitly through a profile pack.
The public provider platform is `openai-compat`, with an explicit `base_url`:
Codex uses Responses and Pi uses Chat Completions. Additional provider platforms,
default endpoints and Pi compatibility settings belong to registered distribution
extensions; the shared Pi renderer consumes their configuration.
Runtime model suggestions are not an allowlist. An explicit native model name is
passed to the selected agent without inferring a provider, endpoint or credential.
Reasoning effort remains constrained by the adapter's supported values.

When a Codex-compatible provider explicitly reports that its saved conversation
is missing during restart or resume, the runtime starts one fresh provider
conversation with the available amux conversation handoff. The amux session,
execution settings and pending handoff remain intact. Other launch failures
remain errors and do not discard the saved provider conversation identity.

For Codex-compatible providers, `provider.structured.repeat_tool_call_limit`
defaults to 50 consecutive identical tool calls in one turn. The guard counts
unique started items with the same call-defining payload; duplicate frames do
not increment it, and a different call or a new turn resets the count. Changes
in returned output do not reset it. Reaching the limit fails the turn with
`code=repeat_tool_loop`. Zero disables the guard, negative values use the
provider default, and overrides are capped at 1000.

Codex uses its app-server protocol; Claude uses the Claude Agent SDK sidecar.
Provider and ACP implementations emit typed payloads. The session-owned canonical
timeline assigns turn identity and order and accepts exactly one terminal event
per turn. Provider message identifiers remain provenance. Observing a result does
not itself complete a turn. The session owns completion policy and queue advancement.

When an ACP agent's output closes, the broker stops accepting requests and drains
requests already being handled before closing client connections. Pending prompt
results, terminal emissions and acknowledgements retain their order. Connection
writes have a bounded shutdown deadline so an unresponsive client cannot prevent
broker exit; a transport failure remains an error.

Codex-compatible providers normalize `thread/tokenUsage/updated` into
`UsageObserved`. `tokenUsage.last` describes the latest invocation and supplies
token counts and context-window usage. Optional `tokenUsage.total` supplies the
provider thread's native cumulative snapshot as `cumulative_input_tokens`,
`cumulative_output_tokens`, `cumulative_total_tokens`,
`cumulative_cached_input_tokens`, `cumulative_cache_creation_input_tokens` and
`cumulative_reasoning_output_tokens`. Missing totals are omitted; snapshots are
neither added together nor inferred from `last`. The latest usage is also carried
by `ResultObserved.Usage`. Cumulative consumption is separate from context-window
occupancy and is never combined across provider threads or child agents.

Provider adapters must not interpret bridge presentation text. The reusable
terminal UI depends on callbacks and interfaces, never on internal session,
transport or Feishu packages. CLI and bridge consumers present the same accepted
canonical events. Permissions are structured requests with correlated responses.

The public builtin pack contains exactly three native-agent profiles. It embeds
no endpoint, credential, external skill or model-service binding. Native Responses
providers can be configured; a Responses-to-Chat compatibility gateway is not
available. Unsupported agents and wire modes fail before process launch or
credential materialization.

Additional distributions compose statically registered adapters and provider
factories through `pkg/extensions` before starting any application. Registered
providers use the same session timeline and isolation contracts as the builtin
providers. Unknown providers and duplicate registrations fail before launch.
The public module must build and test without any distribution module.
