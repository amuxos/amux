# 排查飞书与 Agent 执行问题

[English](troubleshooting.md) | [简体中文](troubleshooting.zh-CN.md)

先从飞书中看到的现象定位，再请运维者检查对应组件。重复发送可能追加工作；结果不确定时，
先查状态，不要直接重发。

| 现象 | 先检查什么 | 下一步 |
| --- | --- | --- |
| 机器人完全不回复 | 是否选对机器人、群里是否真正 @、访问权限与接入状态 | 私聊试 `/amux`，运维者检查 Bridge 连接和应用权限 |
| 群里讨论没有回答 | 协作话题需要提及才触发执行 | 在原话题 @机器人并给出具体补充需求 |
| 其他成员被拒绝 | 负责人或协作访问策略 | 负责人检查群内 `/amux` 设置，进群本身不等于授权 |
| 出现卡片后启动失败 | 所选专家、Runtime 和原生 Agent 登录 | 在真正执行的主机上诊断 Agent |
| 工作似乎卡住 | 待处理权限、当前轮次或排队输入 | 先处理当前请求，用 `/status` 或场景帮助查看，再决定是否中断 |
| 旧话题拒绝输入 | 绑定已结束或群连续会话已退役 | 新发群消息或话题，不要期待旧话题自动变成新会话 |
| 关闭实验后群不工作 | 已保存的独立群配置被暂停 | 负责人检查实验开关并按需重启生效 |
| 记忆不可用 | 保存值与运行值、旧对话未绑定、Hub 作用域 | 应用设置并新建对话，不回退到其他用户的记忆 |
| 定时工作没有聊天回复 | 实际规则、运行、Task 记录及结果投递约定 | 读取 Task 结果，创建规则不保证向飞书推送 |

## 运维诊断

在受影响的安装环境运行：

```sh
amux system status
amux system doctor
amux system target list
amux system logs
```

`system logs` 打印日志路径，按失败时间查看对应 Hub 或 Runtime 日志。
`system doctor` 只诊断，不修改安装。通过 Session 或 Task 的 target 确认执行机器；
本机 worker 健康，不能说明远端 worker 也健康。

在执行主机检查所选 Profile，按实际选择替换：

```sh
amux agent doctor builtin/codex
```

默认 doctor 是静态检查；`--smoke` 会明确执行一次真实模型请求，可能消耗账号额度。
找不到程序或登录过期时，先修复 Runtime 服务环境中的原生 Agent，再重试。

## 有目的地恢复

服务停止时，`amux system start` 启动已有配置。重启前查看计划，并完成正在运行的工作：

```sh
amux system restart --plan
```

准备好后执行 `amux system restart`。不要用安装器 `--clean` 做一般修复，它会删除 amux
数据。强制隔离不可用时，修复[helper 就绪条件](linux-dac-isolation.zh-CN.md)，不要降低策略。

报问题时提供版本、时间、消息模式、Profile、target、Session 或 Task ID、精确错误和去除
凭据的相关日志。最终回答截图本身不能证明是哪一次 TaskRun 产出的。
