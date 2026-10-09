# 示例导航

[English](README.md) | [简体中文](README.zh-CN.md)

- [分布式 Hub 与 Runtime](02-distributed-hub/README.zh-CN.md)：本地演示、双主机配置和启动脚本。
- [Credential Profiles](03-credential-profiles/README.zh-CN.md)：凭据管理命令及不含真实密钥的 JSON 模板。
- [Agent Profile Pack](public-pack/README.zh-CN.md)：可供当前消费者解析的 manifest v2 与代码审查指令。

- [Claude 审查与 Codex 规则 Pack](agents/README.zh-CN.md)：保留作者源文件，并附可直接消费的 v2 manifest。

当前运行时只消费 `dist/agent-pack.manifest.json`，不会从旧 Profile TOML 自动生成或回退读取。
构建和使用说明见 [项目 README](../README.zh-CN.md) 与 [文档导航](../docs/README.zh-CN.md)。
