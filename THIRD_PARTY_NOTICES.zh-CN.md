# 第三方声明

[English](THIRD_PARTY_NOTICES.md) | [简体中文](THIRD_PARTY_NOTICES.zh-CN.md)

amux 的 [Apache-2.0 许可证](LICENSE) 适用于其原创材料。依赖、第三方材料、单独安装的
Agent 和外部服务保留各自的许可证及使用条款；amux 的归属标识不能替代它们的声明。

## Go 依赖

[third_party/go-notices.txt](third_party/go-notices.txt) 保留了 [go.mod](go.mod) 中固定
版本依赖的上游许可证、版权、归属和专利声明文件，以及 Go Runtime 和标准库许可证。
集合包含所有 require 的 module，单个二进制可能仅包含其中一部分。每个 module 条目
提供对应版本的源码归档链接。集合还包含生产源码子目录中的额外声明；依赖的测试
fixture、fuzz harness 和 vendor 目录不在此清单范围内。

运行 `python3 scripts/update-go-notices.py` 从下载的上游 module 生成集合，
`make check` 校验其时效。生成器不判断协议兼容性，也不能替代对复制源码中声明的
审核。新增或更新依赖时，应保留实际再分发文件适用的其它声明；go.mod 变化后重新生成集合。

以下固定版本依赖采用 MPL-2.0：

| Module | 版本 | 对应源码 |
| --- | --- | --- |
| `github.com/hashicorp/hcl` | `v1.0.0` | [源码归档](https://proxy.golang.org/github.com/hashicorp/hcl/@v/v1.0.0.zip) |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | [源码归档](https://proxy.golang.org/github.com/hashicorp/golang-lru/v2/@v/v2.0.7.zip) |

本项目使用这些依赖未经修改的上游版本，协议全文已包含在声明集合中。分发包含 MPL
代码的可执行文件时，应按第 3.2 条以 MPL-2.0 提供对应源码，并告知接收者获取方式。
如果修改了受 MPL 覆盖的文件，还应提供修改后的对应源码，仅提供上游链接不足以提供
你的修改。这项义务不会改变 amux 自身文件的许可证。

## 可选 Claude sidecar

`sidecars/claude-sdk/` 中 amux 自身的 Adapter 源码采用 Apache-2.0。锁文件固定的
`@anthropic-ai/claude-agent-sdk` `0.3.296` 及同版本的八个平台包使用 Anthropic 自有条款。
其许可证注明“All rights reserved”，并指向
[Claude 法律与合规文档](https://code.claude.com/docs/en/legal-and-compliance)；
SDK README 还指向 [Anthropic 商业条款](https://www.anthropic.com/legal/commercial-terms)。

该版本的 [SDK 许可证](https://unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.296/LICENSE.md)
和 [SDK README](https://unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.296/README.md)
可从上游获取。可以从 npm 下载不等于获得再分发授权，amux 的许可证也不授予 Anthropic
软件的使用权。将其捆绑到发行版之前，应核实适用条款及自身权限。

`make sidecar` 从锁文件安装依赖以完成本地构建，其它 npm 依赖保留包内附带的许可证和
声明。公共 Go 发行归档不包含此 sidecar 或 `node_modules`。如果分发 sidecar，应为
实际生产包中的 npm 依赖（包括原生平台包）附带适用的声明与许可证全文；Go 声明集合
不覆盖这些组件。

## 其它 Agent 与发行版

Codex、Claude、Pi 及其服务需要单独安装或访问，仍受各自条款约束。下游发行版应处理
额外依赖、资源和扩展的许可义务。发行归档中 `licenses/amux/` 下的文件仅覆盖公共核心
及所列 Go 依赖。
