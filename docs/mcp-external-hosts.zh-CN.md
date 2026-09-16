# 外部 MCP Host 接入 `amux mcp`

[English](mcp-external-hosts.md) | [简体中文](mcp-external-hosts.zh-CN.md)

`amux mcp` 是本机按需启动的 stdio MCP server。Codex CLI、Gemini CLI、Claude Code
或其他标准 MCP host 通过 `command + args` 拉起一个 `amux mcp` 子进程，即可使用完整的
`amux.*` 工具面。

它不是网络服务：没有监听端口，也不提供跨机器访问。MCP 子进程与 host 使用同一操作系统用户
身份；host 退出或关闭 stdin 后，MCP 子进程随连接结束。`amuxd` 只作为任务和 schedule 的 hub
后端，不托管 MCP endpoint。

## 前置条件

1. `amux` 已安装并位于 MCP host 可见的 `PATH` 中。
2. `amux system status` 显示目标 hub 可达且有可用 runtime。首次使用默认本机拓扑时，先执行
   `amux setup local`。
3. 内置 MCP 总开关已启用。它默认开启；如果此前关闭过，可重新启用：

```bash
amux system config set mcp.enabled true
```

`amux mcp` 可以在 hub 尚未启动时被 host 拉起，因为后端连接在工具调用时才建立；但真正调用
task、agent 或 schedule 工具时 hub 必须可达。

## Codex CLI

推荐用 CLI 写入配置：

```bash
codex mcp add amux -- amux mcp
codex mcp list
```

等价的 `~/.codex/config.toml` 配置：

```toml
[mcp_servers.amux]
command = "amux"
args = ["mcp"]
```

## Gemini CLI

写入用户级配置：

```bash
gemini mcp add --scope user amux amux mcp
gemini mcp list
```

等价的 `~/.gemini/settings.json` 配置：

```json
{
  "mcpServers": {
    "amux": {
      "command": "amux",
      "args": ["mcp"]
    }
  }
}
```

## Claude Code

写入用户级配置：

```bash
claude mcp add --scope user amux -- amux mcp
claude mcp list
```

## 其他标准 MCP host

选择 stdio transport，并使用以下通用配置：

```json
{
  "command": "amux",
  "args": ["mcp"]
}
```

如果 host 看不到 shell 的 `PATH`，把 `command` 改为 `amux` 二进制的绝对路径。若 host 使用了
隔离的 `HOME`，可在其 MCP server 环境配置中显式传入 `AMUX_HOME`，指向真实的 amux 控制面
目录（默认是用户主目录下的 `.amux`）；Agent Profile discovery 本身通过 hub RPC 获取，不依赖
MCP 进程直接读取该目录。不要把 `AMUX_SESSION_ID` 人工写进全局 host 配置；
该变量只用于 amux 托管 session 的 parent provenance 和生命周期绑定。

## 接入后的行为

- `tools/list` 返回 agent profile discovery、task、persistent agent 和 schedule 等全部主工具。
- 只调用 `tools/list` 展示的工具名；已淘汰或未展示的名称会返回未知工具错误。
- 外部 host 通常没有 `AMUX_SESSION_ID`，因此未显式传 `parent_session_id` 的 task 是独立顶层任务。
- 所有工具以当前用户的 amux 配置、hub 权限和 credential policy 执行；stdio 层没有额外认证。
- host 侧的 MCP tool confirmation 与 amux task 的 `permission_policy` 是两层独立控制。

建议先让 host 调用 `amux.profile_list` 验证 discovery，再用返回的
`profiles[].ref` 调用 `amux.task_submit`。完整工具语义和 async 工作流见
[`distributed-agent-usage.md`](distributed-agent-usage.zh-CN.md)。

## 排障

| 现象 | 检查 |
| --- | --- |
| host 报 `command not found` | 使用 `command -v amux` 找到绝对路径，并写入 host 配置。 |
| `amux mcp` 提示已禁用 | 运行 `amux system config set mcp.enabled true`。 |
| server 能连接，但工具调用报 hub 不可达 | 运行 `amux system status`，修复 hub/runtime 后重试。 |
| host 更新配置后仍看不到新工具 | 完全重启 host，让它重新拉起 `amux mcp` 并执行 initialize/tools/list。 |
| 隔离 host 读到了错误配置目录 | 显式传正确的 `AMUX_HOME`；不要仅依赖被隔离后的 `HOME`。 |
