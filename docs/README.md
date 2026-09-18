<a id="文档导航"></a>

# Documentation

[English](README.md) | [简体中文](README.zh-CN.md)

- [Public core and independent distributions](distribution-development.md)
- [License and attribution](licensing.md), [third-party notices](../THIRD_PARTY_NOTICES.md), and [contributing](../CONTRIBUTING.md)
- [CLI model](current/cli-product-model.md), [Agent model](current/agent-adapter-product-model.md), and [Bridge model](current/feishu-bridge-product-model.md)
- [Feishu thread context model](current/feishu-thread-context-model.md)
- [Agent memory model](current/agent-memory-product-model.md) and [design](agent-memory-design.md)
- [Local expert editing in natural language](local-expert-natural-language-authoring-design.md)
- [Architecture decision records](adr/README.md)
- [Task model](distributed-task-model.md) and [task guide](distributed-agent-usage.md)
- Generated [CLI](reference/cli.md), [configuration](reference/configuration.md), and [MCP](reference/mcp.md) references
- [Agent Profile Pack format](reference/agent-profile-format.md)
- [Desktop Gateway API](reference/desktop-client.md)
- [External MCP hosts](mcp-external-hosts.md)
- [Linux UID/DAC isolation operations](operations/linux-dac-isolation.md)
- [Source examples](../examples/README.md) and [chat template examples](reference/chat-template-agent-examples.md)
- [Bridge ownership split record](reports/2026-09-10-bridge-ownership-split-plan.md)

Product models define current behavior. Formats and generated references live in
`reference/`; executable operating procedures live in `operations/`. Retained ADRs
and reports describe decisions and implementation stages at the time they were
written; use the product models for current behavior.

English pages use `.md`; their complete Simplified Chinese counterparts use
`.zh-CN.md` in the same directory. Use the language links at the top of each page.
Update both versions together, retaining the same requirements, examples, and
historical status. The project [glossary](../CONTEXT.md) defines shared terminology.
Executable example prompts, `SKILL.md`, and the `CLAUDE.md` include are runtime
resources rather than duplicate documentation pages; their readmes explain their use.

`make docs` generates **both languages** of the CLI, configuration, and MCP
references. Do not edit these six files by hand. Update source metadata and
`internal/doclang/translations.json`, then regenerate. `make docs-check` rejects
missing translations and stale output. Runtime command help and MCP schemas remain
the source of truth; the documentation renderer translates descriptions only.

`.golangci.yml` retains the golangci-lint v1 configuration; `make lint` requires a
compatible version. The complete delivery check remains `scripts/check.sh`.
