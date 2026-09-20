# AGENTS.md

[English](AGENTS.md) | [简体中文](AGENTS.zh-CN.md)

本目录是公共 Go module `github.com/amuxos/amux`，可作为独立仓库构建和发布。
仓库为 <https://github.com/amuxos/amux.git>，官网为 <https://amuxos.com/>。
本文中的路径相对于本 module 根目录；不要假设父目录存在另一个仓库。

`AGENTS.md` 和引用它的 `CLAUDE.md` 是版本化工程说明，不是个人工作区元数据。
先读 [CONTEXT.md](CONTEXT.zh-CN.md)、[文档导航](docs/README.zh-CN.md) 和相关 [ADR](docs/adr/README.zh-CN.md)。
使用既有领域词汇；涉及架构决定时指出与已有 ADR 的冲突并同步修订。

公共说明文档同时维护英文 `.md` 和简体中文 `.zh-CN.md`，页首互链，语义保持一致。
`make docs` 生成两种语言；更新生成参考时同步维护 `internal/doclang/translations.json`。
执行型示例 Prompt、Skill 和 `CLAUDE.md` 引用不作为成对文档。

## 构建与验证

```sh
make build          # amux、amuxd、amuxlet、amux-isolation-helper 到 bin/
make check-fast     # 当前 module 受影响的 Go packages，默认 -short
make check-fast FAST_ARGS="--package ./internal/bridge --run 'Skill|SlashCommand'"
make test           # 默认开发测试，不启用 CLI 长流程测试
make cli-doc        # 真实 Cobra 命令树生成 CLI 参考
make config-doc     # 真实配置元数据生成配置参考
make mcp-doc        # 真实工具注册表生成 MCP 参考
make proto          # buf generate
make lint
make fmt
make clean
```

常用聚焦测试：`go test ./pkg/acptui`、`go test ./internal/bridge`、
`go test ./internal/acp`、`go test -run TestName ./internal/session`。

除非用户明确限定或跳过，代码、脚本、配置、协议变更提交、push 或发布 tag 前运行：

```sh
GOMAXPROCS=4 GOFLAGS=-p=2 GOCACHE=/private/tmp/amux-go-cache scripts/check.sh
```

edit loop 优先使用聚焦测试或 `make check-fast`；冻结最终 diff 后再跑完整门禁。
纯文档改动可只运行 `git diff --check`。失败必须先修复，失败状态不得提交或推送。
相同且与本次改动无关的偶发失败连续出现两次时保留证据并报告，不重复碰运气。
不因 main 前进就重复 rebase，除非本次改动实际依赖、存在冲突或用户明确要求。

同一 worktree 的相同门禁不得并发，独立 worktree 可并行；成功回执绑定源码与环境，
`--force` 忽略成功回执。检查期间生成的源码变化不能被当作成功结果。

完整门禁覆盖 protobuf 生成与漂移、CLI/配置/MCP 参考、依赖校验、所有 Go packages、
CLI 长测试、四个二进制和 Claude sidecar。`scripts/list-long-tests.sh` 自动发现
`app/cli` 的长测试；这类测试函数以 `requireLongTest(t)` 为第一条语句，并由门禁
显式启用 `AMUX_RUN_LONG_TESTS=1`。不要维护另一份长测试函数名清单。

`go.mod` 的 `go` 指令是语言下限，不是工具链锁定；CI 在
`.github/workflows/ci.yml` 中另行固定已修复的工具链。因此贡献者本机 Go 版本可能
高于该下限，本机通过的门禁仍可能在 CI 失败。门禁必须与工具链版本无关：不要依赖
下限版本与当前版本行为不同的标准库行为；遇到疑似版本相关的失败，先按 CI 版本复现
再判断。例如 Go 1.25.0/1.25.1 会把 root 内的 `os.Root` `DirEntry` 按进程工作目录
解析，导致 `internal/memory` 的扫描在进程未运行于存储根目录时失败。

## 结构与依赖边界

- `cmd/{amux,amuxd,amuxlet,amux-isolation-helper}` 是可构建的薄入口。
- `app/cli`、`app/hub`、`app/runtime`、`app/isolationhelper` 拥有应用装配和相关测试。
- `internal/*` 拥有核心实现；`pkg/extensions` 是发行版注册扩展的公共边界。
- `pkg/acptui` 是可复用终端 UI，不得 import `internal/*`，不得知道 Session 管理、
  gRPC/tunnel、飞书或 source frame。特定行为通过 callbacks 和 AgentStream/AgentSender 注入。
- `agent-packs/builtin/` 是公共内置 Pack；`agent-packs` Go package 仅将资源嵌入为只读 fs.FS。
- `examples/` 是使用样例；`sidecars/claude-sdk/` 是 Claude SDK 源码，dist 为构建产物。

公共核心默认支持 Codex、Claude、Pi；其它发行版通过扩展注册额外 Adapter、Provider、
Profile、配置、Bridge 和品牌。公共代码不能依赖任何具体发行版，不能包含专有端点、
凭据或发行版私有资源。通用实现只维护一份，禁止用源文件覆盖或复制核心包实现定制。
详见 [独立发行版开发](docs/distribution-development.zh-CN.md)。

## 系统与包职责

```text
CLI / 飞书 -> amuxd Hub <- reverse gRPC tunnel <- amuxlet Runtime -> Agent process
```

普通 Session/Task/Schedule 数据面经过 Hub，再到本地或远端 Runtime。
本地 CLI-only 不要求飞书；普通数据面仍需要 Hub，Pack/credential 和本机恢复入口可独立工作。
`task.Runner` 始终在选中的 Runtime 执行并维护 child Session owner；Hub 只负责
registry、scheduler、task 路由、真相索引及 reconciler，不直接驱动 child Session RPC。

| Package | 职责 |
| --- | --- |
| `internal/config` | 配置加载、约束和参考元数据 |
| `internal/store`、`internal/replay` | SQLite 持久化、Session 输出重放 |
| `internal/session` | Session 生命周期、queue、dispatch 与 completion policy |
| `internal/agentstream` | canonical Turn identity、顺序 live events、唯一 TurnFinished 和 identity-free producer seam |
| `internal/adapter`、`internal/provider`、`internal/acp`、`internal/mux` | Agent 能力、协议解析、进程与 broker |
| `internal/task`、`internal/registry`、`internal/schedule` | Task runner、Runtime 调度和周期任务 |
| `internal/executionauth`、`internal/principal` | 控制面执行授权和底层 principal/delegation 校验 |
| `internal/agentprofile` | manifest 消费、registry、Profile projection 与 task-path 物化 |
| `internal/mcp` | MCP stdio 协议服务；实际工具注册和 CLI dispatch 在 app/cli |
| `internal/gateway` | HTTP/WebSocket Session Gateway，仅通过 transport pbgen 与 backend 交互 |
| `internal/sessionlaunch`、`internal/tunnel` | Session Run 请求、反向 tunnel 与路由 |
| `internal/bridge` | 飞书 WebSocket/API、会话路由、卡片、输出、上下文与 secrets |
| `internal/transport/pbgen` | 由 proto 生成的 protobuf/gRPC stubs |
| `internal/sourceframe`、`internal/workspace` | 输入来源边界与工作区路径 |

SQLite 保持 CGO-free，使用 `modernc.org/sqlite`。各组件保持 single-tenant 所有权。
应用入口负责 wiring；Bridge 渲染不泄漏到通用 TUI、ACP 或 Adapter。
Gateway backend 由 `app/cli` 注入，不得反向依赖 CLI。

## 产品模型先行

修改下列领域前，先读对应当前模型；如改变规则，先修订模型再改实现和行为测试。
不得让文档与实现长期不一致。历史设计只作为背景，目标规格需明确标注实现状态。

- CLI、参数、配置可见性、target/session 解析、生命周期、queue/restart、安装升级：
  [CLI 模型](docs/current/cli-product-model.zh-CN.md)。命令树位于 `app/cli`。
- Adapter/Provider/ACP、Session event、permission、resume/restart、attach：
  [Agent 模型](docs/current/agent-adapter-product-model.zh-CN.md)。
- Task、Runtime registry、MCP、权限、隔离、工作区、credential、Schedule：
  [Task 模型](docs/distributed-task-model.zh-CN.md) 和 [操作指南](docs/distributed-agent-usage.zh-CN.md)。
- Profile、Pack registry、projection 和 task-path 物化：
  [Pack 格式](docs/reference/agent-profile-format.zh-CN.md) 以及 CLI/Task 模型。
- 飞书路由、Bot commands、workspace/topic binding、permission、输出：
  [Bridge 模型](docs/current/feishu-bridge-product-model.zh-CN.md) 和
  [Thread context 模型](docs/current/feishu-thread-context-model.zh-CN.md)。
- HTTP/WebSocket Gateway：[桌面客户端 API](docs/reference/desktop-client.zh-CN.md)。

Timeline 拥有 amux Turn identity 和唯一 terminal；Provider/ACP 直接产出 typed emission，
Session 通过 correlated DispatchTurn 交给准确 Turn，并从 accepted canonical event
派生 policy view，拥有 queue 前进前的同步 completion barrier。不得另建 legacy event
adapter、完成事件推断或第二套 done gate。ResultObserved 携带结果内容，不代表 Turn 完成。

`internal/agentprofile` 单向依赖 `internal/task` 和 `internal/adapter`；后两者不得反向依赖。
使用结构化事件字段表达来源和能力；legacy source frame 解析留在应用边界。
Session 使用 `pkg/acptui` 的 line-mode TUI，输入通过 Submit 入队，不恢复 raw Send/Resize。
TUI 支持 `/detach` 和 Ctrl-]；只宣传已实际实现的交互命令。

## 生成文档与协议

配置行为改变时同步 `internal/config/reference.go` 的类型、默认值、约束、CLI 管理能力与语义；
动态路径同步 `internal/config/config_reference_test.go`。执行 `make config-doc`。
命令或 flag 的可见性、参数、默认值和文案改变时执行 `make cli-doc`。
MCP 注册、description、alias 或输入 Schema 改变时同步注册及 dispatch 测试，再执行 `make mcp-doc`。
生成器直接消费真实注册表；CLI 参考应覆盖所有平台并标出限制，不依赖生成主机平台。
三种生成参考必须与实现同次提交，禁止手改 `docs/reference/{cli,configuration,mcp}.md`。

协议来源是 `proto/amux/v1/amux.proto`，通过 `buf generate` 生成到 `internal/transport/pbgen/`。
gRPC 使用标准 protobuf codec，不恢复旧 JSON codec、手写 transport/pb 或 pbadapter 兼容层。

## 运行与调试

运行状态在 `~/.amux/`：`runtime.sock`、`runtime.db`、`bridge.db`、`schedule.db`、
`config.toml`、`secrets/`、`agent-packs/registry.json`、`sessions/`。
源码目录调整不改变这些用户数据路径。

`AMUX_DEBUG_ACP_CHUNKS=1` 记录 Runtime 上的 ACP raw chunks；`AMUX_DEBUG_JSONRPC=1`
记录 structured provider 的丢弃帧。设置在 `amuxlet` 上，由 broker 子进程继承。
手工 E2E 使用临时 HOME、独立 Hub 端口并清理测试 daemons/brokers，避免污染真实安装。
