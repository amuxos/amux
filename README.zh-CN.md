# amux

[English](README.md) | [简体中文](README.zh-CN.md)

[官网](https://amuxos.com/) · [GitHub](https://github.com/amuxos/amux)

amux 在本地和远端机器上运行、协调智能体 Session，提供命令行界面、终端 Session UI、
Task 与 Schedule API，以及可选的飞书/Lark Bot Bridge。支持 Codex、Claude 和 Pi。

这是 `https://github.com/amuxos/amux.git` 的源码候选版本，Go module 为
`github.com/amuxos/amux`。除另有标注外，amux 的原创源码、文档和示例采用
[Apache-2.0](LICENSE)，允许遵守该协议进行商业使用和闭源产品集成。
详见[许可与归属标识说明](docs/licensing.zh-CN.md)。

贡献与许可规则见 [CONTRIBUTING.md](CONTRIBUTING.zh-CN.md)，工程说明见
[AGENTS.md](AGENTS.zh-CN.md)；Claude Code 使用 [CLAUDE.md](CLAUDE.md) 加载后者。

<a id="build-and-run"></a>

## 构建与运行

通过以下命令克隆公共仓库：

```sh
git clone https://github.com/amuxos/amux.git
cd amux
```

安装 Go 1.25 或更新版本，然后在公共 module 目录构建并安装：

```sh
make build
bash scripts/install.sh --source ./bin
amux setup local
amux session run builtin/codex
```

按官方说明单独安装并认证所选 Agent。默认 Profile 为 `builtin/codex`，原生 Agent
负责模型选择和认证；仓库不附带模型端点或凭据。使用可信的本地发行归档时，先解压，
再通过 `scripts/install.sh --source` 指定包含 `amux`、`amuxd` 和 `amuxlet` 的目录。

Claude 还需要 Node sidecar：

```sh
make sidecar
export AMUX_CLAUDE_SIDECAR="$PWD/sidecars/claude-sdk/dist/index.js"
```

启动 Runtime 前，在其进程环境中设置该变量。使用锁定的 Claude SDK 支持的 Node
版本。`make sidecar` 不安装全局 npm 链接。仅构建 Go 二进制不需要 Node。

<a id="operation"></a>

## 使用与运维

本地 Session、Task 和 Schedule 需要本地 Hub 和 Runtime，但不需要飞书。
`amux setup local` 负责配置它们；`amux setup feishu` 通过飞书官方应用注册流程添加
可选 Bot 集成。已有安装重新构建后执行 `make upgrade`，无需重复 setup。
更新使用显式指定的本地二进制或 operator 配置的发行源；默认没有下载服务器或 Pack 市场。

Profile Pack 使用 manifest v1.1，支持在运行时消费外部 Git Pack。可选的专家编辑能力
需要单独配置兼容的 authoring runtime；源码构建不会隐式下载它。
捆绑 Skill 和桌面应用不在本候选版本范围内。

<a id="development"></a>

## 开发

示例与操作指南见 [文档导航](docs/README.zh-CN.md) 和 [示例导航](examples/README.zh-CN.md)。

本 module 是可独立构建的公共核心。其它发行版将其作为 Go module 依赖，在调用可导入的
应用入口前注册扩展，详见 [发行版开发指南](docs/distribution-development.zh-CN.md)。

`make check` 校验生成的协议与参考文档、使用 Python 3 校验第三方声明，运行已包含的
Go 测试、构建命令，并在 npm 可用时测试 sidecar。不运行依赖真实外部账号的测试。
部分既有测试 fixture 和资源仍待审核，未纳入本候选版本；当前测试套件不代表已经
覆盖完整产品验收。

- [CLI 模型](docs/current/cli-product-model.zh-CN.md)
- [Agent 模型](docs/current/agent-adapter-product-model.zh-CN.md)
- [Bridge 模型](docs/current/feishu-bridge-product-model.zh-CN.md)
- [Task 模型](docs/distributed-task-model.zh-CN.md)
- [CLI 参考](docs/reference/cli.zh-CN.md)
- [配置参考](docs/reference/configuration.zh-CN.md)
- [MCP 参考](docs/reference/mcp.zh-CN.md)
- [Agent Profile Pack](docs/reference/agent-profile-format.zh-CN.md)

<a id="license-and-attribution"></a>

## 许可与归属标识

再分发 amux 或其衍生作品时，应提供 [LICENSE](LICENSE)，保留适用的 [NOTICE](NOTICE)
归属声明及其它必需声明，并按 Apache-2.0 第 4 条标明修改过的文件。归属标识可以放在
随附的第三方声明、文档，或“关于”页面的组件列表中，不要求显著展示 Logo 或官网署名。

依赖和单独安装的 Agent 保留各自的许可证与使用条款，见[第三方声明](THIRD_PARTY_NOTICES.zh-CN.md)。
发行归档将公共核心的许可材料放在 `licenses/amux/`。[许可说明](docs/licensing.zh-CN.md)
提供集成标识示例，并解释这些义务的适用范围。
