# amux

[English](README.md) | [简体中文](README.zh-CN.md)

[官网](https://amuxos.com/) · [GitHub](https://github.com/amuxos/amux)

amux 在本地和远端机器上运行、协调智能体 Session，提供命令行界面、终端 Session UI、
Task 与 Schedule API，以及可选的飞书/Lark Bot Bridge。支持 Codex、Claude 和 Pi。

这是 `https://github.com/amuxos/amux.git` 的源码候选版本，Go module 为
`github.com/amuxos/amux`。正式发布前仍需确定许可证和再分发声明；生成候选版本本身
不构成许可证授权。

工程协作规则见 [AGENTS.md](AGENTS.zh-CN.md)，Claude Code 使用 [CLAUDE.md](CLAUDE.md) 加载相同说明。

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

`make check` 校验生成的协议与参考文档，运行已包含的 Go 测试、构建命令，并在 npm 可用时
测试 sidecar。不运行依赖真实外部账号的测试。部分既有测试 fixture 和资源仍待审核，
未纳入本候选版本；当前测试套件不代表已经覆盖完整产品验收。

- [CLI 模型](docs/current/cli-product-model.zh-CN.md)
- [Agent 模型](docs/current/agent-adapter-product-model.zh-CN.md)
- [Bridge 模型](docs/current/feishu-bridge-product-model.zh-CN.md)
- [Task 模型](docs/distributed-task-model.zh-CN.md)
- [CLI 参考](docs/reference/cli.zh-CN.md)
- [配置参考](docs/reference/configuration.zh-CN.md)
- [MCP 参考](docs/reference/mcp.zh-CN.md)
- [Agent Profile Pack](docs/reference/agent-profile-format.zh-CN.md)
