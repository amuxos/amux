# 02 · 分布式:一个 control + 多个 runtime

[English](README.md) | [简体中文](README.zh-CN.md)

把任务调度扩展到多台机器:**一个 amux control service** 负责调度/路由,**多个 amuxlet runtime**
反向连接到 hub 并执行 session/task。CLI 配置了 `hub.addr` 后,`amux session run` / `amux task`
就会经 hub 路由到合适的 runtime。

```
   amux CLI ──► amuxd (hub)  ◄── reverse tunnel ── amuxlet (runtime A)
                   │                          ◄────────────────── amuxlet (runtime B)
                   └─ 调度选址 + 路由 + task 索引
```

## A. control 主机(调度中心)

```bash
./hub-host.sh
# 等价于:
#   amux setup hub             # 写 hub.addr 默认本地，并启动本机 amux system
#   amux system target list    # 查看已注册的 target
```

> hub 监听地址默认本地 `127.0.0.1:7100`。要让远端连入,显式把 `hub.addr` 设成
> `0.0.0.0:<port>` 或具体网卡地址,并必须设置 `hub.token`。建议同时设置
> `hub.allowed_targets` 限制可注册的 runtime.id(见 `config.hub.toml`)。
> **task-path Agent Profile 解析发生在 hub 主机**:在 hub 主机用
> `amux agent pack add <namespace> <git-url-or-local-git-repo>` 注册 Agent Pack。
> `amux task submit --profile <namespace/profile>` 会由 amuxd 解析为 base agent 并路由。

## B. 每台 runtime 主机(执行机)

```bash
HUB=hub-host.example.com:7070 ./runtime-host.sh
# 等价于:
#   amux setup remote --hub hub-host.example.com:7070  # 写 hub.addr 并启动本机 amux system
#   # 给这台机起个可辨识的 target id 和容量/标签:
#   amux system config set runtime.id        gpu-box-1
#   amux system config set runtime.max_concurrency 4
#   amux system config set runtime.labels.gpu true
#   amux system restart
```

## C. 从任意配置了 hub 的机器提交

```bash
amux system target list                       # 确认 target 在线
amux session run builtin/codex -n work --on gpu-box-1 # 指定 target 起 session
amux task submit "跑测试"                        # 使用默认 Profile
amux task submit "审查改动" --profile team/reviewer
```

## 配置文件参考(透明对照)

- [`config.hub.toml`](config.hub.toml) —— hub 主机的 `~/.amux/config.toml` 关键片段。
- [`config.runtime.toml`](config.runtime.toml) —— runtime 主机的关键片段。

> `amux setup hub` / `amux setup remote` 会自动写好这些;手写配置时可对照本目录样例。

## 单机演练(隔离 HOME,不污染默认 ~/.amux)

没有第二台机器也能体验完整 hub 路径(hub + runtime + 经 hub 提交并解析 task-path profile):

```bash
./local-hub-demo.sh
```

脚本用临时 HOME + 随机端口起 hub 和 runtime,创建一个临时 Agent Pack,
提交一个 `--profile demo/reviewer` task,验证 hub 解析为 base 类型并路由执行,最后自动清理。
运行前需确保本机已安装并登录 `codex`。
