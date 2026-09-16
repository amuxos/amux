# 文档导航

[English](README.md) | [简体中文](README.zh-CN.md)

- [公共核心与独立发行版](distribution-development.zh-CN.md)
- [CLI 模型](current/cli-product-model.zh-CN.md)、[Agent 模型](current/agent-adapter-product-model.zh-CN.md)、[Bridge 模型](current/feishu-bridge-product-model.zh-CN.md)
- [Feishu Thread Context 模型](current/feishu-thread-context-model.zh-CN.md)
- [本地专家自然语言编辑](local-expert-natural-language-authoring-design.zh-CN.md)
- [架构决策记录](adr/README.zh-CN.md)
- [Task 模型](distributed-task-model.zh-CN.md) 与 [任务操作指南](distributed-agent-usage.zh-CN.md)
- [CLI](reference/cli.zh-CN.md)、[配置](reference/configuration.zh-CN.md)、[MCP](reference/mcp.zh-CN.md) 生成参考
- [Agent Profile Pack 格式](reference/agent-profile-format.zh-CN.md)
- [桌面 Gateway API](reference/desktop-client.zh-CN.md)
- [外部 MCP Host 接入](mcp-external-hosts.zh-CN.md)
- [Linux UID/DAC 隔离运维](operations/linux-dac-isolation.zh-CN.md)
- [源码示例](../examples/README.zh-CN.md)与 [Chat Template 机制示例](reference/chat-template-agent-examples.zh-CN.md)
- [Bridge 所有权拆分记录](reports/2026-09-10-bridge-ownership-split-plan.zh-CN.md)

当前规则在产品模型中维护，格式和生成参考在 `reference/`，可执行运维步骤在
`operations/`。保留的 ADR 和报告说明记录当时的决策与实现阶段，当前行为仍以产品模型为准。

英文默认页使用 `.md`，完整的简体中文对照页位于同目录，使用 `.zh-CN.md`。
通过页首链接切换语言。修改时同步维护两版，保持要求、示例与历史状态一致。
通用术语以项目 [词汇表](../CONTEXT.zh-CN.md) 为准。可执行示例的 Prompt、`SKILL.md` 和
`CLAUDE.md` 引用文件属于运行时资源，不重复生成文档页；用法见对应 README。

`make docs` 同时生成 CLI、配置和 MCP 的**中英文两版**参考，禁止手工编辑这六个文件。
更新源元数据和 `internal/doclang/translations.json` 后重新生成。
`make docs-check` 检查缺失翻译与生成文件漂移。运行时命令帮助和 MCP Schema
仍是真相源，文档渲染器仅翻译说明文字。

`.golangci.yml` 保留 golangci-lint v1 配置；`make lint` 需预先安装兼容版本。
统一交付检查仍为 `scripts/check.sh`。
