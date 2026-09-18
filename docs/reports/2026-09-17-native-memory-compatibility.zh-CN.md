# 原生记忆兼容性证据

[English](2026-09-17-native-memory-compatibility.md) | [简体中文](2026-09-17-native-memory-compatibility.zh-CN.md)

2026-09-17 核对的证据。本报告说明兼容边界；当前行为以
[Agent 模型](../current/agent-adapter-product-model.zh-CN.md)为准。

## Claude

Claude 的[记忆文档](https://code.claude.com/docs/en/memory#enable-or-disable-auto-memory)
提供进程环境开关 `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`。
[子 Agent 文档](https://code.claude.com/docs/en/sub-agents#enable-persistent-memory)
明确说明关闭自动记忆也会让子 Agent 的 `memory` 字段失效：不再添加记忆指令和
额外的记忆工具访问能力。本机 Claude Code 2.1.274 可执行文件也包含此环境变量
检查，且优先于 `autoMemoryEnabled` 配置。这是文档和本地可执行文件证据，
没有进行真实模型验证。

amux 在每次受管启动和恢复时设置该开关，并通过 SDK 的追加 system prompt
传入记忆协议。用户编写的 CLAUDE.md 指令仍然生效。改变环境变量不能从原生会话
中抹除已经读取的内容，因此未受管的原生会话必须新建会话才能接入 amux 记忆。

`CLAUDE_CONFIG_DIR` 可以覆盖原生目录。隔离启动显式清空它，使 Claude 使用
隔离 HOME 的默认 `.claude/` 和 `.claude.json` 布局，保留已有隔离原生路径。
SDK 继承 backend 完成隔离后的 sidecar 有效环境。若在 `session.start` 中重放
Runtime 隔离前的环境，会重新引入共享原生目录，因此 Provider 不再发送这一
重复的环境覆盖。
Linux DAC 启动和清理也跳过内置 Agent 的服务用户工作区信任写入，因为这些文件
不属于有效 Principal。

## Pi

审查了上游 Pi 0.85.1 的
[`e4c75a7`](https://github.com/badlogic/pi-mono/tree/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent)
版本。其核心资源加载器读取项目指令、SYSTEM/APPEND_SYSTEM 文件、Skill 和扩展；
system prompt 和配置中没有相当于 Codex/Claude 的内置自动长期记忆功能。
Session 持久化和上下文压缩属于会话历史，继续保留。

[配置路径实现](https://github.com/badlogic/pi-mono/blob/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent/src/config.ts)
使用 `PI_CODING_AGENT_DIR`，默认是 `~/.pi/agent`。隔离启动将该目录固定在自身
HOME 下。amux 添加固定的 CLI 记忆协议。

Pi 的[文档化项目信任覆盖参数](https://github.com/badlogic/pi-mono/blob/e4c75a73222ae2c72abb5f5314fa35ee8effc508/packages/coding-agent/README.md#project-trust)
是 `--approve`（`-a`）。Linux DAC 启动使用该参数加载受管工作区的项目资源，
避免修改服务用户的信任库。显式 `--approve` / `--no-approve` 及短参数优先。
private-home 启动保留隔离 HOME 下的默认信任文件行为。

Pi 扩展可以添加独立记忆行为。任意扩展没有已确认的通用“只关闭记忆”开关。
`--no-extensions` 关闭通用扩展发现，且仍允许显式指定的扩展；amux 不会自动添加
该参数。自定义记忆扩展需要分别评估兼容性，核心审查结论不能证明这些扩展兼容。

## 其它 Provider

app-server 兼容或 ACP 传输不代表具备相同的原生记忆能力。没有经过确认的指令与
原生记忆契约的 Provider 继续提示 amux 记忆不可用，保持普通启动行为；不会因为
协议相似就发送 Codex 专用配置或调用。

## 关闭 amux 实验开关

Hub 是实验开关的权威。关闭后拒绝记忆数据操作，并阻止新会话接入记忆。已有绑定
的会话在恢复时保留原有绑定、协议和原生记忆抑制，避免隐式切换到第二套记忆库。
协议要求记忆工具不可用时继续普通工作，并说明记住 / 遗忘没有完成。不引入每轮
查询、提示词改写或额外冻结生命周期状态。MCP 启动可以查询 Hub 能力以隐藏不可用
工具；已经运行的客户端仍受 Hub 的数据操作开关约束。

聚焦测试覆盖 Claude 新建和恢复时关闭自动记忆、保留未受管配置、private-home /
Linux DAC 环境中的原生目录覆盖清理，以及 Claude sidecar 的环境隔离边界。
