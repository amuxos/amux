<a id="public-builtin-profiles"></a>

# 公共内置 Profile

[English](README.md) | [简体中文](README.zh-CN.md)

`dist/agent-pack.manifest.json` 是本 Pack 直接维护的运行时 manifest。
它与外部 Git Pack 使用相同的 v1.1 消费契约，构建 amux 不需要独立的 authoring 工具。
这些 Profile 使用用户原生 Agent 的认证和默认模型，不包含托管模型服务或凭据。
Provider 权限模式与现有内置 Profile 一致。

公共 Skill 和指令仍在审核中。没有检查完整内容、引用和再分发权利之前，不要从私有
Pack 复制资源到本目录。公共 embed 指令显式列出运行时输入，因此向本目录添加无关
文件不会悄悄将它嵌入二进制。
