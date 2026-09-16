# 许可与归属标识

[English](licensing.md) | [简体中文](licensing.zh-CN.md)

除文件另有标注外，amux 的原创源码、文档和示例采用未经修改的
[Apache License, Version 2.0](../LICENSE)（`Apache-2.0`）。项目归属声明位于
[NOTICE](../NOTICE)。已有版权声明中的权利仍属于各自权利人。项目版权声明署名为
amuxos 组织，许可证不转让版权归属。

## 商业使用与集成

Apache-2.0 允许商业使用、修改、分发和闭源产品集成。独立发行版可以为自身代码选择
其它协议，同时履行 amux 及其它依赖对应的许可义务。Apache-2.0 包含受其条款约束的
贡献者专利授权，但不授予项目商标的一般使用权，也不代表项目为集成产品背书。

再分发 amux 或其衍生作品时，应遵守 LICENSE 第 4 条：

- 向接收者提供完整许可证。
- 在修改过的文件中显著声明已经修改这些文件。
- 在分发的衍生源码中保留适用的版权、专利、商标和归属声明。
- 以可读形式保留 NOTICE 中适用的归属声明。分发衍生作品时，第 4(d) 条允许将声明
  放在 NOTICE 文件、随附源码或文档中，或产品通常展示第三方声明的位置。

集成方可在完整许可证和适用的 NOTICE 内容旁使用以下说明：

> 本产品包含 amux（https://github.com/amuxos/amux），依据 Apache License 2.0 使用。

第三方声明文档或适当的“关于”组件列表可以作为归属标识的位置，不额外要求显著展示
Logo、首页徽标或广告署名。仅提供托管服务不会触发 Apache-2.0 的再分发义务；
仅调用独立运行的 amux 服务，本身也不构成对 amux 的再分发。

NOTICE 仅提供信息，不增加许可条件。本指南解释协议，不修改协议；英文 LICENSE
原文是正式许可文本。中文指南与英文指南说明相同规则。

## 发行与第三方组件

`scripts/package-release.sh` 在每个发行归档的 `licenses/amux/` 中附带 LICENSE、
NOTICE、两种语言的第三方声明指南、go.mod 和 Go 依赖声明集合。其它发行版设置
`AMUX_PACKAGE_BUILD_ROOT` 时也使用相同位置；其中的许可材料适用于公共核心及所列依赖，
不适用于该发行版自己的代码或额外依赖。发行方还需补充实际产物要求的其它声明。

公共发行打包器包含 Go 二进制及这些声明，不捆绑可选的 Claude SDK sidecar 或单独
安装的 Agent；它们的条款独立于 amux 的 Apache-2.0 授权。添加额外组件前，应阅读
[第三方声明](../THIRD_PARTY_NOTICES.zh-CN.md)，包括 MPL 源码获取地址和 Anthropic SDK 条款。

贡献的许可规则见 [CONTRIBUTING.md](../CONTRIBUTING.zh-CN.md)。
