# 桌面客户端 Gateway

[English](desktop-client.md) | [简体中文](desktop-client.zh-CN.md)

公共核心提供 `amux gui serve`，把现有 Session API 转成 HTTP JSON 和 WebSocket，供
桌面客户端使用。Electron 应用由独立发行版维护；本 module 不包含 Electron 源码或安装包。

## 组件与连接

| 组件 | 公共源码位置 | 职责 |
| --- | --- | --- |
| Gateway CLI | `app/cli/gui.go` | 启动监听、输出 ready 信息、适配现有 session client |
| HTTP/WebSocket server | `internal/gateway/` | bearer token 校验、REST 和 Attach 流 |
| 协议类型 | `internal/transport/pbgen/` | gRPC 请求、响应和事件结构 |

`internal/gateway` 通过 Backend interface 接收能力，不依赖 CLI wiring。`app/cli/gui.go`
负责注入 backend；公共 `cmd/amux/main.go` 只是调用 `app/cli.Main` 的薄入口。
其他发行版可在自己的命令入口注册扩展后调用公共应用。

```text
桌面 renderer -> HTTP/WebSocket gateway -> amuxd hub -> 选中的 amuxlet
```

本机 Session 同样经 hub 控制面。首次安装执行 `amux setup local`；已有安装执行
`amux system status` 检查 hub/runtime，不依靠 gateway 自动绕过 hub 直连本机 runtime。

## 启动与认证

```sh
amux gui serve --addr 127.0.0.1:0
```

客户端 main process 读取 stdout 的 ready 行：

```text
AMUX_GUI_READY {"addr":"127.0.0.1:<port>","token":"<per-process-token>"}
```

端口由操作系统分配，token 默认随机生成。客户端只向自己的 renderer 提供这两个值，
REST 请求携带 `Authorization: Bearer <token>`；Attach 使用 query token。应用退出时应
结束所启动的 gateway 进程。启动错误与日志写到 stderr。

默认地址是 loopback；`--addr` 可以显式覆盖，桌面发行版应固定传入 loopback 地址。
Gateway 允许跨源请求，访问控制依赖监听地址和 token，不依赖浏览器 Origin。

## API

| 路径 | 作用 |
| --- | --- |
| `/api/health` | 健康状态和版本 |
| `/api/sessions` | Session 列表，含 target 标识 |
| `/api/run` | 创建 Session |
| `/api/kill`、`/api/restart`、`/api/interrupt` | 终止、重启、中断 |
| `/api/submit` | 提交文本到 Session queue |
| `/api/respond-permission` | 回答权限请求 |
| `/api/queue/list`、`/api/queue/clear`、`/api/queue/cancel` | 管理待执行输入 |
| `/api/prune` | 删除指定 target 的已归档 Session |

`/api/health` 和 `/api/sessions` 使用 GET，其余操作使用 POST JSON；字段以 `internal/gateway/rest.go` 的
请求类型为准。Gateway 尚未暴露 sleep/resume 的 HTTP 路由。

`GET /attach?name=<session>&target_id=<target>&token=<token>` 升级为 WebSocket，
发送 JSON `AttachEvent`；Go `[]byte` 字段编码为 base64。该流用于读取事件，输入和控制
仍通过 REST。桌面提交使用 `source=desktop`。

Session 身份由 target 和 name 共同确定。对远端 Session 的操作必须携带 `target_id`，
批量 prune 应按 target 分组。所有 Session 使用 line view，输入通过 Submit 进入队列。

## 验证与诊断

在公共 module 运行 `go test ./internal/gateway ./app/cli`。客户端自己的构建、sidecar、
二进制捆绑及签名由发行版说明维护，不能从公共 `cmd` 构建产物推断已包含发行版扩展。

启动失败先看 gateway stderr；工具不可用先运行 `amux system status`。
权限响应需使用当前等待的 permission ID。Hub 模型见 [CLI 模型](../current/cli-product-model.zh-CN.md)，
命令参数见 [CLI 参考](cli.zh-CN.md)。
