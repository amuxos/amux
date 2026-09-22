<h1 align="center">amux</h1>

<p align="center">
  <a href="https://github.com/amuxos/amux-releases/releases/latest"><img src="https://img.shields.io/github/v/release/amuxos/amux-releases?style=flat-square&amp;labelColor=151b28&amp;color=79e8c1&amp;label=release" alt="Latest release"></a>
  <a href="https://github.com/amuxos/amux-releases/releases"><img src="https://img.shields.io/github/downloads/amuxos/amux-releases/total?style=flat-square&amp;labelColor=151b28&amp;color=93c5fd" alt="Total release asset downloads"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-93c5fd?style=flat-square&amp;labelColor=151b28" alt="License: Apache 2.0"></a>
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat-square&amp;labelColor=151b28&amp;logo=go&amp;logoColor=white" alt="Source builds: Go 1.25 or newer"></a>
  <a href="https://github.com/amuxos/amux-releases/releases/latest"><img src="https://img.shields.io/badge/macOS_%26_Linux-arm64_%C2%B7_amd64-a7f3d0?style=flat-square&amp;labelColor=151b28" alt="macOS and Linux, arm64 and amd64"></a>
  <a href="docs/README.zh-CN.md"><img src="https://img.shields.io/badge/docs-English_%C2%B7_%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-c4b5fd?style=flat-square&amp;labelColor=151b28" alt="Documentation: English and Simplified Chinese"></a>
</p>

<p align="center"><a href="https://amuxos.com/">官网</a> · <a href="https://github.com/amuxos/amux">GitHub</a> · <a href="docs/README.zh-CN.md">文档</a></p>

<p align="center"><a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a></p>

amux 在本地和远端机器上运行、协调 Codex、Claude 和 Pi Session，提供 CLI、终端 UI、
Task 与 Schedule API，以及可选的飞书/Lark Bot Bridge。

这是 `https://github.com/amuxos/amux.git` 的源码候选版本，Go module 为
`github.com/amuxos/amux`。原创源码、文档和示例默认采用 [Apache-2.0](LICENSE)，
允许依协议商业使用和闭源集成；另有标注者除外。详见[许可说明](docs/licensing.zh-CN.md)。

贡献与许可规则见 [CONTRIBUTING.md](CONTRIBUTING.zh-CN.md)，工程说明见
[AGENTS.md](AGENTS.zh-CN.md)；Claude Code 使用 [CLAUDE.md](CLAUDE.md) 加载后者。

<a id="build-and-run"></a>

## 构建与运行

预编译版本无需 Go 工具链。先安装并登录原生 Agent，再下载安装器：

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex
```

独立安装器从 `amuxos/amux-releases` 下载预编译包，可加 `--version vX.Y.Z` 固定版本。
安装后打开新 shell，或执行安装器打印的 PATH 命令。

使用 `--runtime claude`（需要 Node.js 22+）或 `--runtime pi` 选择其他已安装 Agent。
添加 `--feishu` 可配置机器人。从源码构建：

```sh
git clone https://github.com/amuxos/amux.git
cd amux
```

源码构建需要 Go 1.25+，并已安装、登录原生 Agent（`codex`、`claude` 或 `pi`）。
在公共 module 目录运行：

```bash
# 自动检测已安装的 Agent，交互选择 Runtime。
bash scripts/install.sh --feishu

# 显式选择 Runtime，自动使用对应的 builtin Profile。
bash scripts/install.sh --runtime codex --feishu --interaction-mode singleton

# 仅使用本地 CLI，不配置飞书 Bot。
bash scripts/install.sh --runtime pi
```

每个 Runtime 自动映射到对应的 `builtin/codex`、`builtin/claude` 或 `builtin/pi` Profile：

- 仅一个已安装 Runtime 时自动选择；多个时按编号或名称选择。
- 非交互运行传入 `--runtime`（别名 `--provider`）；缺少 Runtime 会在安装前失败。
- 原生 Agent 管理认证和默认模型；`--model` 覆盖飞书 Bot 的模型，不附带端点或凭据。

脚本完成构建、安装、PATH 更新、本地系统配置和就绪检查。
启用 `--feishu` 后须在交互终端完成扫码；未完成时不创建 Session。

- 默认 **singleton** 模式创建命名的持久 Session 并绑定 Bot。
- `--feishu-profile` 修改 **Bot Profile** 名称（默认 `default`），不是 Agent Profile。
- `--session-name` 和 `--session-cwd` 指定 singleton Session 与工作区。

```bash
bash scripts/install.sh --runtime claude --feishu --interaction-mode thread \
  --feishu-profile assistant --model sonnet
```

thread 模式为新对话保存 Profile/Runtime/Model。重复运行复用 Bot 凭据与兼容的
singleton Session；冲突时给出指引，不删除 Session 或绑定。切换到 thread 模式保留旧
Session，但关闭其 singleton 路由；显式 `--model` 通过 `session set-model` 更新复用的 Session。

重复安装先暂存、验证新二进制，再停止 Pack autosync 和本地系统；停止失败则中止替换。
配置、凭据、Session 和 Pack 均保留，随后启动新系统，并按原状态恢复 autosync。
**此过程会中断活动工作**，但不发送测试提示词或聊天消息。

需要全新安装时添加 `--clean`：

```bash
bash scripts/install.sh --clean --runtime codex --feishu --interaction-mode singleton
```

**`--clean` 会删除 `AMUX_HOME`（默认 `~/.amux`）下全部数据**：先停止、卸载 amux，
再清除配置、凭据、Session、日志和托管 Pack。须重新配置飞书；原生 Agent 及其登录信息保留。

- 源码 checkout 与 singleton 工作区须位于清理目录之外。
- 二进制和 Claude SDK 暂存到外部，因此 `--source`、`--claude-sidecar` 可指向旧安装内部。
- 卸载也会停止已登记的独立 MCP/GUI 服务，由外部宿主负责重新启动。

Claude 需要锁定 SDK 支持的 Node/npm。源码安装将完整 sidecar 构建到
`~/.amux/sidecars/claude-sdk/`，Runtime 自动发现，无需环境变量。
Codex 和 Pi 无需 sidecar；脚本不安装或升级原生 Agent。

<details>
<summary>高级安装选项</summary>

- **本地二进制：** `--source /path/to/binaries`，目录须包含 `amux`、`amuxd` 和 `amuxlet`。
- **Claude sidecar：** `--claude-sidecar /path/to/claude-sdk`，须包含 `dist/`、`node_modules/` 和 `package.json`；也可采用相邻的 `../sidecars/claude-sdk/` 布局。
- **源码目录：** 独立脚本接受 `--source-root /path/to/public-amux` 或 `AMUX_INSTALL_SOURCE_ROOT`，拒绝内部发行版根目录。
- **Shell PATH：** `--no-modify-path` 跳过添加条目；清空卸载仍会移除旧的托管条目。

全部参数见 `bash scripts/install.sh --help`。

</details>

<a id="operation"></a>

## 使用与运维

本地 Session、Task 和 Schedule 需要 Hub 与 Runtime，无需飞书：

| 操作 | 命令 |
| --- | --- |
| 配置本地系统 | `amux setup local` |
| 通过飞书官方注册流程添加 Bot | `amux setup feishu` |
| 重新构建后升级，无需重复 setup | `make upgrade` |
| 下载安装公共最新版本 | `amux update --restart` |

公共更新在替换二进制和配套 Claude SDK 前校验 SHA256。
用 `AMUX_RELEASE_URL` 或 `--release-url` 覆盖清单/归档来源。

Profile Pack 使用 manifest v1.1，支持外部 Git Pack，默认没有 Pack 市场。
可选专家编辑需单独配置兼容的 authoring runtime，源码构建不会隐式下载。
捆绑 Skill 和桌面应用不在本候选版本范围内。

<a id="development"></a>

## 开发

示例与操作指南见 [文档导航](docs/README.zh-CN.md) 和 [示例导航](examples/README.zh-CN.md)。

公共核心可独立构建。其它发行版依赖其 Go module，在调用应用入口前注册扩展。
详见[发行版开发指南](docs/distribution-development.zh-CN.md)。

`make check` 校验生成的协议与参考文档、用 Python 3 检查第三方声明、运行 Go 测试并
构建命令；npm 可用时测试 sidecar。不运行真实外部账号测试。部分 fixture 和资源仍待审核，
未纳入候选版本；当前套件不代表完整产品验收覆盖。

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

再分发须提供 [LICENSE](LICENSE)，保留适用的 [NOTICE](NOTICE) 与其它必需声明，
并按 Apache-2.0 第 4 条标明修改的文件。归属标识可放在第三方声明、文档或“关于”组件列表中，
无需显著展示 Logo 或官网署名。

依赖与原生 Agent 保留各自的许可条款，见[第三方声明](THIRD_PARTY_NOTICES.zh-CN.md)。
发行归档的许可材料位于 `licenses/amux/`；[许可说明](docs/licensing.zh-CN.md)提供义务解释和集成示例。
