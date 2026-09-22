<a id="amux-context"></a>

# amux 术语

[English](CONTEXT.md) | [简体中文](CONTEXT.zh-CN.md)

本词汇表为 amux 的产品概念统一命名，确保架构工作使用稳定术语。

<a id="language"></a>

## 术语

**Feishu user（飞书用户）**：
向 amux Bot 提出工作请求，并在飞书中查看进度或结果的人。
这是一种使用角色；自托管安装中，同一个人也可以是 amux operator。
_避免使用_：Consumer（消费者）

**amux operator（amux 运维者）**：
负责 amux 安装健康状态和生命周期的角色，包括启动、重启、升级和诊断服务。
该角色可以与 Feishu user 由同一人担任。
_避免使用_：Administrator persona（管理员画像）

**Bot governance mode（Bot 治理模式）**：
每个飞书 Bot Profile 独立选择 `owner_only`（个人）或 `shared`（团队）。owner_only 将该
Bot 的能力限制给 owner；shared 允许授权成员使用该 Bot 暴露的能力。治理模式不决定系统
拓扑、工作区策略或 `thread|singleton` 交互模式。
_避免使用_：Deployment mode、edition、audience mode（部署模式、版本、受众模式）

**System hosting topology（系统托管拓扑）**：
一个 amux system 托管一个专属 Bot Profile，还是多个独立配置的 Bot Profile 的部署
事实。选择依据是资源、进程、数据和故障域隔离要求，不从 Bot 治理模式推导。
_避免使用_：Personal deployment、Team deployment（个人部署、团队部署）

**amux system（amux 系统）**：
面向 owner 的完整 amux 安装健康与生命周期对象，将 Hub、Runtime、Bridge 及相关进程
作为一个整体呈现；各组件只在诊断中出现。
_避免使用_：amux service、component（amux 服务、组件）

**Managed amux system（托管 amux 系统）**：
由宿主操作系统原生服务管理器管理生命周期的 amux system。
不支持的宿主显式前台运行 amux，不会悄悄选择第二种 supervisor。
_避免使用_：PID daemon、fallback service（PID 守护进程、回退服务）

**System status card（系统状态卡片）**：
仅 owner 可在飞书私聊看到的摘要，展示 amux system 健康、版本、当前 Bot 交互模式、
Hub 主机 IP，以及重启和检查更新操作。主机 IP 是系统级连接元数据，不表示 Hub
监听该网卡；卡片不显示组件级健康、PID、日志和诊断，也不在群聊或任务话题中暴露。
_避免使用_：Component dashboard、session status（组件面板、Session 状态）

**Feishu input routing（飞书输入路由）**：
将规范化的飞书用户输入分类为应驱动的 amux 动作，例如工作区管理、Session 入口、
活动任务对话、人类上下文采集或无操作。
_避免使用_：Command routing、session routing（命令路由、Session 路由）

**Agent turn（智能体轮次）**：
amux 接受一条队列提交并准备派发时创建的、与 provider 无关的单位。
其 canonical 表示拥有由 amux 管理的 TurnID 和 TurnSeq，且恰好有一个 `TurnFinished`。
Provider message ID 是来源信息，不是 turn 身份；兼容表示不创建另一条领域 turn。
`ResultObserved` 携带结果内容，但不结束 turn。

**Agent presentation action（智能体呈现动作）**：
从 canonical Agent event 推导出的、与交互表面无关的指令，例如追加、替换、分段回复，
更新活动，请求权限或结束 turn。飞书负责渲染这些动作，不解释 provider 协议。

**Execution authorization（执行授权）**：
控制 actor 能否访问智能体执行，以及能否使用创建或驱动执行的自动化的控制面决策。
它覆盖 Session、TaskRun、AgentInstance 和 Schedule，同时保留这些概念的区别。
_避免使用_：Object ownership check、RPC authorization（对象所有权检查、RPC 授权）
