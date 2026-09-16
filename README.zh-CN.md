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

安装 Go 1.25 或更新版本，并按官方说明安装、登录至少一个原生 Agent
（`codex`、`claude` 或 `pi`）。在公共 module 目录运行安装脚本：

```bash
# 自动检测已安装的 Agent，交互选择 Runtime。
bash scripts/install.sh --feishu

# 显式选择 Runtime，自动使用对应的 builtin Profile。
bash scripts/install.sh --runtime codex --feishu --interaction-mode singleton

# 仅使用本地 CLI，不配置飞书 Bot。
bash scripts/install.sh --runtime pi
```

脚本将 `codex` 映射到 `builtin/codex`、`claude` 映射到 `builtin/claude`，
`pi` 映射到 `builtin/pi`。只检测到一个 Runtime 时自动选择；检测到多个时按编号
或名称选择。非交互运行应传入 `--runtime`（或别名 `--provider`）。缺少 Runtime
会在安装前失败。原生 Agent 管理自身认证和默认模型；`--model` 覆盖配置的飞书
Bot 使用的模型，不附带端点或凭据。

脚本完成构建、安装、更新 shell PATH、配置本地系统及就绪检查。`--feishu` 增加
二维码配置流程，默认使用 singleton 模式：创建命名的持久 Session 并绑定 Bot。
请在交互终端完成扫码；取消或未完成配置时，不会继续创建 Session。`default` 是
**飞书 Bot Profile 名称**，不是 Agent Profile。使用 `--feishu-profile` 修改它，
使用 `--session-name` 和 `--session-cwd` 指定 singleton Session 及工作区。

```bash
bash scripts/install.sh --runtime claude --feishu --interaction-mode thread \
  --feishu-profile assistant --model sonnet
```

thread 模式为新对话保存同样的 Profile、Runtime 和 Model。重复运行会复用 Bot
凭据和兼容的本地 singleton Session。Session 或绑定冲突时给出指引，不删除它们。
显式切换到 thread 模式会关闭 singleton 路由并保留旧 Session。复用 Session 时
显式传入 `--model` 会执行 `session set-model`。

替换已有安装前，脚本先暂存并验证新二进制，再停止 Pack autosync 和本地系统。
停止失败会中止替换。普通重复安装保留 amux 配置、凭据、Session 和 Pack，然后
启动新系统；仅在 autosync 原先运行时恢复它。该过程会中断活动工作。脚本不会
发送测试提示词或聊天消息。

需要全新安装时添加 `--clean`：

```bash
bash scripts/install.sh --clean --runtime codex --feishu --interaction-mode singleton
```

`--clean` 停止、卸载旧 amux，并删除 `AMUX_HOME`（默认 `~/.amux`）下全部数据，
包括配置、凭据、Session、日志和托管 Pack。需要重新配置飞书。原生 Agent 的安装
及其自身登录信息保留。二进制和 Claude SDK 暂存到该目录之外，因此 `--source`
和 `--claude-sidecar` 可以指向旧安装内部。源码 checkout 和 singleton 工作区必须
位于清理目录之外。卸载也会停止已登记的独立 MCP/GUI 服务，由外部宿主负责重新启动。

Claude 还需要锁定 SDK 支持的 Node/npm。源码安装会构建并复制完整 sidecar 到
`~/.amux/sidecars/claude-sdk/`，安装后的 Runtime 无需环境变量覆盖即可发现它。
Codex 和 Pi 不需要该 sidecar。脚本不安装或升级原生 Agent。

本地发行二进制可通过 `--source /path/to/binaries` 指定，目录中需要有 `amux`、
`amuxd` 和 `amuxlet`。Claude 还需要 `--claude-sidecar /path/to/claude-sdk`，
目录包含 `dist/`、`node_modules/` 和 `package.json`；若已处于相邻的
`../sidecars/claude-sdk/` 布局，可省略此参数。独立脚本可通过
`--source-root /path/to/public-amux` 或 `AMUX_INSTALL_SOURCE_ROOT` 选择要构建的
公共 checkout，内部发行版源码根目录会被拒绝。默认没有公共下载 URL。
全部参数见 `bash scripts/install.sh --help`；`--no-modify-path` 跳过添加 shell PATH
条目（清空卸载仍会移除旧的托管条目）。安装后打开新 shell，或在当前 shell 执行
脚本打印的 PATH 命令。

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
