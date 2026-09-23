# 在另一台机器上执行 Agent

[English](remote-runtime.md) | [简体中文](remote-runtime.zh-CN.md)

这是运维操作。飞书用户继续使用同一个机器人，Hub 将工作路由到选定 Runtime。
先在 worker 安装 amux 并登录原生 Agent；笔记本的登录和文件不会自动复制过去。

## 建立到 Hub 的连接

worker 主动向 Hub 连接。若 Hub 只监听 loopback，可在 worker 的另一个终端保持 SSH 转发：

```sh
ssh -N -L 17100:127.0.0.1:7100 operator@hub-host
```

将 SSH 目标和端口替换为真实 Hub 主机配置，然后在 worker 执行：

```sh
amux system config set runtime.id worker-a
amux setup remote --hub 127.0.0.1:17100
amux system status
```

选择唯一且稳定的 Runtime ID。该操作会改变 worker 的配置与服务拓扑，适用于专门的
worker 安装。SSH 连接需要保持可用。若配置了 Hub token 或 target 白名单，需配置匹配
的 `hub.token` 并允许此 Runtime ID。Hub 直接监听非 loopback 地址时要求 token，应使用
可信且受保护的网络。完整设置见[配置参考](../reference/configuration.zh-CN.md)。

## 确认注册并路由会话

在已连接 Hub 的 CLI 上执行：

```sh
amux system target list
amux session run builtin/codex --name remote-demo --on worker-a
amux session attach worker-a/remote-demo
```

目标列表必须出现 worker。Agent 程序、凭据和工作区路径必须在 worker 上存在。
`local` 别名指向 CLI 配置的本机 Runtime，不是任意名叫 local 的 worker。
在飞书支持的助手或群启动设置中，为新工作选择可用 Runtime；这不会迁移已有对话。

持久 Agent Instance 回到已记录的 Runtime。分布式路由不意味着自动迁移或跨机高可用。
