<!-- Code generated from app/cli's MCP tool registry; DO NOT EDIT. -->

# amux MCP 工具参考

[English](mcp.md) | [简体中文](mcp.zh-CN.md)

本文档由 `amux mcp` 的实际 `tools/list` 注册表自动生成，覆盖正式工具、工具说明与完整输入 JSON Schema。修改 MCP 工具名称、可见性、说明或输入字段后，必须运行 `make mcp-doc`；仓库统一门禁会拒绝生成结果不一致的改动。 本参考中的 Schema 说明文字经过翻译；运行时 Schema 与校验字段保持不变。

MCP 的产品语义、权限与生命周期边界仍以 [`docs/current/cli-product-model.md`](../current/cli-product-model.zh-CN.md) 和 [`docs/distributed-task-model.md`](../distributed-task-model.zh-CN.md) 为准。运行时还会通过 [`amux.result_continue`](#amuxresult_continue) 对超过单页预算的成功结果续传；工具结果本身不是 `tools/list` 注册元数据，因此不在本文档中另维护一份可能漂移的输出 Schema。

`tools/list` 正式工具：**54**。

## 正式工具索引

| 工具 | 说明 |
| --- | --- |
| `amux.collaborator_list` | 仅在准备实际委派时按 kind 列出内部 Agent 或 external_bot；纯 Agent 能力发现请使用 amux.profile_list，不要调用本工具。external_bot 只返回当前 Session 所绑定飞书群内的 Bot；CLI、非飞书或无有效群 binding 时为空。 |
| `amux.delegation_cancel` | 请求取消支持 cancelable 的 Delegation；cancelled 是停止信号，不得自动创建替代委派。 |
| `amux.delegation_create` | 创建一次性 Delegation 并返回 ID。external_bot 仅在用户明确要求或确认后使用，且只能发送给当前 Session 所绑定飞书群内的 Bot；结果为 external_untrusted。创建预算默认最多 2 次，用户指定数量为硬上限。 |
| `amux.delegation_get` | 读取 Delegation 当前快照；增量事件使用 delegation_wait。 |
| `amux.delegation_intervene` | 回答支持 permission_response 的 Delegation 权限请求。 |
| `amux.delegation_wait` | 等待 Delegation；event 增量返回事件，actionable 避免普通进度反复唤醒主模型并保留权限/终态唤醒。wait_timed_out 不是 Delegation 终态；按返回状态维护实际创建的尝试账本。 |
| `amux.feishu_send_message` | 使用当前 Session 绑定的飞书 Bot 向群聊发送纯文本或 Markdown，禁用私聊。须已获得用户对目标与内容的授权。指定 receive_id + receive_id_type=chat_id，或群消息的 reply_to_message_id。返回 message_id；始终使用 Bot 身份。 |
| `amux.instance_followup` | 向持久 Agent 交付任务并立即触发新 turn，保留 provider 对话上下文；适用于后台任务完成后继续、或失败后由你决定重试。实例上次失败后遗留的子会话会被复用；若实例已无法使用，会返回错误并建议改用 instance_spawn 新建实例。 |
| `amux.instance_interrupt` | 中断持久 Agent 当前 turn。用于用户要求停止某个后台任务时；中断后简要告知用户，之后可用 instance_followup 继续。 |
| `amux.instance_list` | 列出持久 Agent Instance 状态、child Session 状态和 mailbox 深度。 |
| `amux.instance_send` | 向持久 Agent mailbox 入队消息但不唤醒，不启动新 turn。 |
| `amux.instance_spawn` | 非阻塞创建持久 Agent Instance（后台实例），返回稳定的 agent_instance_id。适用于你判断需要较长时间执行或大量工具调用的任务；短问题直接回答，不要委派。prompt 要自包含：写清目标、约束、交付物，并要求后台 Agent 结束时给出一段简洁的、面向用户的结论（放在最终回复末尾，可用「结论：」开头），不要把工作过程当作结论。调用后简短告诉用户已在后台开始，并继续响应用户的新消息；记住 agent_instance_id 与任务的对应关系，但对用户用自然语言描述任务，不要展示原始 id。任务结束时系统会自动向你送达一条「后台任务完成」通知，无需轮询或反复调用 task_get。 |
| `amux.instance_steer` | 向持久 Agent 正在执行的 turn 注入纠正或补充，不中断当前工作，适用于用户纠正或补充某个后台任务时。实例无可注入的 turn 时回落到 mailbox（delivery=mailbox），此时改用 instance_followup 继续该任务；mailbox 中的纠正会先送达，prompt 里不要重复。 |
| `amux.instance_wait` | 等待持久 Agent 当前 turn 结束或超时；结果仍用 task_get/task_stream 获取。前台会话一般不需要调用：后台任务结束时会自动送达完成通知，等待会阻塞你对用户新消息的响应。 |
| `amux.local_expert_change` | 预览、发布或清理 managed local 专家 patch。publish 仅限后续明确确认的 owner turn，并须原样提交相同 patch 与 receipt；评测确认不等于发布确认。 |
| `amux.local_expert_read` | 读取 managed local 专家的精确编辑基线；仅当前飞书 Bot owner turn 可用，不写入。 |
| `amux.memory_forget` | 按 revision 条件删除一份记忆文档；不会清除已进入会话的历史。纠正或忘记偏好时还需检查 MEMORY.md 和相关主题，修订重复内容及引用；部分失败须明确报告。 |
| `amux.memory_get` | 按需读取记忆文档或指定行段及 revision；新会话读取一次 MEMORY.md，同一上下文复用。truncated=true 时使用 next_offset 续读并核对 revision，路径不是本机文件。 |
| `amux.memory_search` | 按需搜索跨会话记忆；涉及以前的工作、决定或偏好时查询，返回有界命中片段。incomplete=true 表示扫描未完成；无需历史的任务不要检索。 |
| `amux.memory_upsert` | 按 revision 条件保存长期记忆；用于用户明确要求记住、稳定偏好和已验证经验。详细内容放主题文档，入口保持简短；不保存秘密、临时授权或推测。冲突须重读合并，成功后才能声称已保存。 |
| `amux.profile_get` | 按精确 ref 获取 Agent Profile 完整详情，支持开发版 namespace。 |
| `amux.profile_list` | 分页列出非开发版 Agent Profile；用 profiles[].ref 提交任务，详情用 profile_get。沿 next_cursor 读取至 has_more=false。 |
| `amux.result_continue` | 续读分页结果；按序拼接 content_chunk，直到 complete=true。游标仅当前 MCP 进程短期有效。 |
| `amux.schedule_create` | 创建 hub 定时规则。默认 schedule_type=subtask（推荐用于大多数定时任务：每次隔离运行临时子 Task，自动继承当前会话的 Agent 与模型，不允许 escalate-to-main；在飞书会话内省略 delivery_target 时自动投递回来源话题 origin，也可用 delivery_policy 控制 always/on_failure/on_non_empty）；仅需延续当前对话上下文时使用 schedule_type=session-prompt（默认 reuse-session，向目标 Session Submit 提示词并唤醒 Agent）。创建成功后请立即回复用户，不要在当前回合等待首次触发。 |
| `amux.schedule_delete` | 删除 Schedule；既有运行对应的 Task 结果仍可读取。 |
| `amux.schedule_get` | 获取 Schedule；include_runs=true 时包含运行历史及 task_id。 |
| `amux.schedule_list` | 分页列出 Schedule（含 created_at_iso 与投递设置；勿将更早或其他话题已完成的同名历史任务误当作本次新建）；沿 next_cursor 读取至 has_more=false。 |
| `amux.schedule_result_get` | 只读获取定时任务单次执行的结构化结果（覆盖 session-prompt 与 subtask，含执行状态、最终 Markdown 正文与飞书送达回执）。当需要查看定时任务最近一次或指定 execution_id 的最终产出与送达情况时调用。 |
| `amux.schedule_set_enabled` | 启用或停用 Schedule。 |
| `amux.schedule_trigger` | 立即手动触发一次 Schedule（不影响其启停状态与下次计划触发时间）。 |
| `amux.schedule_update` | 原地更新 Schedule 的名称、时间规则、时区、结果投递设置（delivery_target/delivery_policy）、session-prompt 的 prompt/run_mode 或 subtask 的局部 Task 字段；省略字段保持不变，不允许 escalate-to-main。 |
| `amux.session_interrupt` | 中断 Session 当前 turn。 |
| `amux.session_list` | 按 runtime 分组列出调用方可见的 Session；创建 Session 时不要预先调用，先省略 target 调用 session_run，仅在其报告多个 target 后用本工具选择。 |
| `amux.session_prune` | 破坏性操作：永久删除已归档 Session；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent；Task 历史使用 task_prune。 |
| `amux.session_queue_cancel` | 按 queue_id 丢弃 Session 队列中的一条输入。 |
| `amux.session_queue_clear` | 丢弃 Session 全部待处理输入。 |
| `amux.session_queue_list` | 按最早优先列出 Session 待处理输入。 |
| `amux.session_restart` | 原地重启 Session Agent 进程。 |
| `amux.session_resume` | 唤醒 sleeping Session。 |
| `amux.session_run` | 用 Agent Profile 创建 Session；profile 和 name 必填。未指定 target 时先直接调用：唯一连接 target 会自动选择，多个 target 会在创建前返回明确错误。 |
| `amux.session_send` | 向 Session 输入队列提交提示词；名称有歧义时用 target 消歧。 |
| `amux.session_set_model` | 切换 Session 模型并保留对话历史；支持原生模型与 provider-backed 模型 ref（如 custom/example-model），无歧义时也可用裸上游名（如 example-model）；满足热条件时不重启进程，否则原地重启并经 provider resume/handoff 保留对话，跨 provider 切换会开新对话并注入 handoff 历史；Task/Instance 持有的 Session 不支持。 |
| `amux.session_sleep` | 暂停 Session Agent 进程并保持可恢复。 |
| `amux.session_stop` | 归档 Session；Task 历史使用 task_prune 清理。 |
| `amux.session_stream` | 一次性 replay 并排空 Session 最近输出，返回有界聚合文本；不是实时 tail。 |
| `amux.task_cancel` | 取消 Agent Task，并中断或终止其 child Session。 |
| `amux.task_get` | 获取 Agent Task 状态、结构化结果和 session_ref。 |
| `amux.task_list` | 列出调用方可见的 Agent Task 及状态、session_ref 和可用结果。 |
| `amux.task_prune` | 清理终态 Task 历史；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent。 |
| `amux.task_respond_permission` | 应答 escalate-to-main Task 上抛的权限请求。 |
| `amux.task_run` | 阻塞运行短 Agent Task；仅支持 policy-allow 或 deny-all，escalate-to-main 会死锁。 |
| `amux.task_stream` | 从 from_seq 长轮询 Agent Task 事件，返回 next_seq 供无损续读。 |
| `amux.task_submit` | 异步提交 Agent Task，立即返回 task_id。agent 优先使用 profile_list 的 ref；开发版先经 profile_get 验证。 |
| `amux.turn_result_get` | 只读获取单轮对话的结构化结果（执行状态、最终 Markdown 正文与飞书卡片/附件送达回执）。当需要核实某轮回答是否真正执行完成、取回完整正文、或确认飞书是否送达及对应 message_id 时调用；支持按 session+turn_id（可用 latest）或 feishu_message_id 查询。 |

## `amux.collaborator_list`

仅在准备实际委派时按 kind 列出内部 Agent 或 external_bot；纯 Agent 能力发现请使用 amux.profile_list，不要调用本工具。external_bot 只返回当前 Session 所绑定飞书群内的 Bot；CLI、非飞书或无有效群 binding 时为空。

**输入 JSON Schema**

```json
{
  "properties": {
    "kind": {
      "description": "可选的协作者类型过滤；明确寻找当前群内的外部 Bot 时必须传 external_bot",
      "enum": [
        "internal_agent",
        "external_bot"
      ],
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.delegation_cancel`

请求取消支持 cancelable 的 Delegation；cancelled 是停止信号，不得自动创建替代委派。

**输入 JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "要请求取消的 Delegation ID",
      "type": "string"
    }
  },
  "required": [
    "delegation_id"
  ],
  "type": "object"
}
```

## `amux.delegation_create`

创建一次性 Delegation 并返回 ID。external_bot 仅在用户明确要求或确认后使用，且只能发送给当前 Session 所绑定飞书群内的 Bot；结果为 external_untrusted。创建预算默认最多 2 次，用户指定数量为硬上限。

**输入 JSON Schema**

```json
{
  "properties": {
    "collaborator_id": {
      "description": "amux.collaborator_list 返回的不透明协作者 ID，必须原样传入",
      "type": "string"
    },
    "idempotency_key": {
      "description": "可选的重试幂等键；仅同一逻辑委派可以复用",
      "type": "string"
    },
    "task": {
      "description": "交给协作者的完整、可独立执行的任务说明",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "整个 Delegation 的截止时间；0 或省略时使用后端默认值",
      "minimum": 0,
      "type": "integer"
    }
  },
  "required": [
    "collaborator_id",
    "task"
  ],
  "type": "object"
}
```

## `amux.delegation_get`

读取 Delegation 当前快照；增量事件使用 delegation_wait。

**输入 JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "要读取当前快照的 Delegation ID",
      "type": "string"
    }
  },
  "required": [
    "delegation_id"
  ],
  "type": "object"
}
```

## `amux.delegation_intervene`

回答支持 permission_response 的 Delegation 权限请求。

**输入 JSON Schema**

```json
{
  "properties": {
    "decision": {
      "description": "允许或拒绝当前权限请求",
      "enum": [
        "allow",
        "deny"
      ],
      "type": "string"
    },
    "delegation_id": {
      "description": "需要处理权限请求的 Delegation ID",
      "type": "string"
    },
    "option_id": {
      "description": "后端权限请求提供的可选选项 ID",
      "type": "string"
    },
    "reason": {
      "description": "可选的决策说明",
      "type": "string"
    },
    "request_id": {
      "description": "permission_requested 事件中的请求 ID",
      "type": "string"
    }
  },
  "required": [
    "delegation_id",
    "request_id",
    "decision"
  ],
  "type": "object"
}
```

## `amux.delegation_wait`

等待 Delegation；event 增量返回事件，actionable 避免普通进度反复唤醒主模型并保留权限/终态唤醒。wait_timed_out 不是 Delegation 终态；按返回状态维护实际创建的尝试账本。

**输入 JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "要等待的 Delegation ID",
      "type": "string"
    },
    "from_seq": {
      "description": "上次响应返回的 next_seq；用于增量读取事件",
      "minimum": 0,
      "type": "integer"
    },
    "return_on": {
      "default": "event",
      "description": "event 保持兼容：下一事件、终态或窗口超时即返回；actionable 内部消费普通 output/status，只在权限请求、结果、错误、终态或 Delegation deadline 返回",
      "enum": [
        "event",
        "actionable"
      ],
      "type": "string"
    },
    "timeout_seconds": {
      "description": "底层有界长轮询窗口，默认 25 秒，不是整个 Delegation 的超时；actionable 模式会在 MCP 调用内跨窗口继续等待",
      "maximum": 30,
      "minimum": 0,
      "type": "integer"
    }
  },
  "required": [
    "delegation_id"
  ],
  "type": "object"
}
```

## `amux.feishu_send_message`

使用当前 Session 绑定的飞书 Bot 向群聊发送纯文本或 Markdown，禁用私聊。须已获得用户对目标与内容的授权。指定 receive_id + receive_id_type=chat_id，或群消息的 reply_to_message_id。返回 message_id；始终使用 Bot 身份。

**输入 JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "format": {
      "default": "text",
      "description": "正文格式：text（默认）或 markdown。Markdown 使用飞书富文本及其支持的语法；不下载或上传图片。",
      "enum": [
        "text",
        "markdown"
      ],
      "type": "string"
    },
    "receive_id": {
      "description": "群聊 ID；新消息必填，回复时省略。拒绝私聊会话。",
      "type": "string"
    },
    "receive_id_type": {
      "description": "接收者 ID 类型；仅支持 chat_id，与 receive_id 一起填写。",
      "enum": [
        "chat_id"
      ],
      "type": "string"
    },
    "reply_in_thread": {
      "description": "在话题内回复；仅可与 reply_to_message_id 一起使用，默认 false。",
      "type": "boolean"
    },
    "reply_to_message_id": {
      "description": "要回复的群消息 ID；与接收者字段互斥。禁用私聊回复。",
      "type": "string"
    },
    "text": {
      "description": "消息正文，原样保留。设置 format=markdown 发送富文本；编码后的内容（含富文本封装）不能超过 20 KiB。",
      "type": "string"
    },
    "uuid": {
      "description": "可选去重键，最多 50 字节。重试须保持相同目标、格式、内容与键；飞书在一小时内去重。",
      "type": "string"
    }
  },
  "required": [
    "text"
  ],
  "type": "object"
}
```

## `amux.instance_followup`

向持久 Agent 交付任务并立即触发新 turn，保留 provider 对话上下文；适用于后台任务完成后继续、或失败后由你决定重试。实例上次失败后遗留的子会话会被复用；若实例已无法使用，会返回错误并建议改用 instance_spawn 新建实例。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "description": "要唤醒并接收 Task 的持久 Agent Instance，取自 instance_spawn 的返回值",
      "type": "string"
    },
    "notify_parent": {
      "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭",
      "type": "boolean"
    },
    "result_granularity": {
      "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，超限时截断并指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "task": {
      "description": "触发下一轮的新 Task 提示词",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "type": "boolean"
    }
  },
  "required": [
    "agent_instance_id",
    "task"
  ],
  "type": "object"
}
```

## `amux.instance_interrupt`

中断持久 Agent 当前 turn。用于用户要求停止某个后台任务时；中断后简要告知用户，之后可用 instance_followup 继续。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    }
  },
  "required": [
    "agent_instance_id"
  ],
  "type": "object"
}
```

## `amux.instance_list`

列出持久 Agent Instance 状态、child Session 状态和 mailbox 深度。

**输入 JSON Schema**

```json
{
  "properties": {
    "owner_session_id": {
      "description": "可选过滤条件：拥有这些 Instance 的 parent Session ID",
      "type": "string"
    },
    "status": {
      "enum": [
        "active",
        "idle",
        "archived"
      ],
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.instance_send`

向持久 Agent mailbox 入队消息但不唤醒，不启动新 turn。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    },
    "source": {
      "description": "队列输入的可选来源标签",
      "type": "string"
    },
    "text": {
      "description": "要排队的消息；Agent 唤醒后会在下一条 Task 提示词之前收到",
      "type": "string"
    }
  },
  "required": [
    "agent_instance_id",
    "text"
  ],
  "type": "object"
}
```

## `amux.instance_spawn`

非阻塞创建持久 Agent Instance（后台实例），返回稳定的 agent_instance_id。适用于你判断需要较长时间执行或大量工具调用的任务；短问题直接回答，不要委派。prompt 要自包含：写清目标、约束、交付物，并要求后台 Agent 结束时给出一段简洁的、面向用户的结论（放在最终回复末尾，可用「结论：」开头），不要把工作过程当作结论。调用后简短告诉用户已在后台开始，并继续响应用户的新消息；记住 agent_instance_id 与任务的对应关系，但对用户用自然语言描述任务，不要展示原始 id。任务结束时系统会自动向你送达一条「后台任务完成」通知，无需轮询或反复调用 task_get。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "可选的显式 Instance 句柄；省略时生成并返回新的 agent-\u003cuuid\u003e 句柄",
      "type": "string"
    },
    "credential_profile": {
      "type": "string"
    },
    "extra_args": {
      "description": "原生执行参数；权限参数按 permission_policy 归一化，其他参数保留顺序",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "isolation": {
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "model": {
      "description": "执行模型引用，固定到持久实例；省略时使用父会话的当前模型与思考强度（同引擎时），否则使用继承的启动预设模型或所选引擎默认模型",
      "type": "string"
    },
    "notify_parent": {
      "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭",
      "type": "boolean"
    },
    "permission_policy": {
      "description": "Task 与原生权限的统一策略：policy-allow（默认）、escalate-to-main 或 deny-all；覆盖原生权限参数，持久实例沿用该策略",
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
      "type": "string"
    },
    "reasoning_effort": {
      "description": "执行思考强度，固定到持久实例；会话内创建的实例在同引擎时沿用该会话的当前强度，否则使用所选模型声明的默认值",
      "type": "string"
    },
    "result_granularity": {
      "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，超限时截断并指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "可选 Principal alias 意图；省略时使用 parent Execution Principal 创建实例",
      "type": "string"
    },
    "runtime": {
      "description": "执行 Runtime 适配器；省略时使用 builtin 快捷入口预设，其他专家使用发行版默认执行配置",
      "type": "string"
    },
    "task": {
      "description": "交给 Agent 的初始 Task 提示词",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "type": "boolean"
    },
    "workspace": {
      "properties": {
        "local_dir": {
          "type": "string"
        },
        "policy": {
          "enum": [
            "temp-dir",
            "git-worktree"
          ],
          "type": "string"
        },
        "repo": {
          "properties": {
            "cache_key": {
              "type": "string"
            },
            "commit": {
              "type": "string"
            },
            "kind": {
              "type": "string"
            }
          },
          "type": "object"
        }
      },
      "type": "object"
    }
  },
  "required": [
    "agent",
    "task"
  ],
  "type": "object"
}
```

## `amux.instance_steer`

向持久 Agent 正在执行的 turn 注入纠正或补充，不中断当前工作，适用于用户纠正或补充某个后台任务时。实例无可注入的 turn 时回落到 mailbox（delivery=mailbox），此时改用 instance_followup 继续该任务；mailbox 中的纠正会先送达，prompt 里不要重复。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    },
    "text": {
      "description": "要注入的纠正或补充；Agent 会判断是调整当前工作还是追加要求，并保留已完成进度",
      "type": "string"
    }
  },
  "required": [
    "agent_instance_id",
    "text"
  ],
  "type": "object"
}
```

## `amux.instance_wait`

等待持久 Agent 当前 turn 结束或超时；结果仍用 task_get/task_stream 获取。前台会话一般不需要调用：后台任务结束时会自动送达完成通知，等待会阻塞你对用户新消息的响应。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    },
    "timeout_seconds": {
      "description": "等待当前 turn 进入 idle 或 archived 的最长秒数；默认 30 秒",
      "type": "integer"
    }
  },
  "required": [
    "agent_instance_id"
  ],
  "type": "object"
}
```

## `amux.local_expert_change`

预览、发布或清理 managed local 专家 patch。publish 仅限后续明确确认的 owner turn，并须原样提交相同 patch 与 receipt；评测确认不等于发布确认。

**输入 JSON Schema**

```json
{
  "allOf": [
    {
      "if": {
        "properties": {
          "mode": {
            "const": "preview"
          }
        },
        "required": [
          "mode"
        ]
      },
      "then": {
        "required": [
          "operation",
          "target_ref",
          "base_revision",
          "patch"
        ]
      }
    },
    {
      "if": {
        "properties": {
          "mode": {
            "const": "publish"
          }
        },
        "required": [
          "mode"
        ]
      },
      "then": {
        "required": [
          "operation",
          "target_ref",
          "base_revision",
          "patch",
          "preview_receipt"
        ]
      }
    },
    {
      "if": {
        "properties": {
          "mode": {
            "const": "cleanup"
          }
        },
        "required": [
          "mode"
        ]
      },
      "then": {
        "required": [
          "preview_receipt"
        ]
      }
    },
    {
      "if": {
        "properties": {
          "mode": {
            "enum": [
              "preview",
              "publish"
            ]
          },
          "operation": {
            "const": "fork"
          }
        },
        "required": [
          "mode",
          "operation"
        ]
      },
      "then": {
        "required": [
          "source_ref"
        ]
      }
    }
  ],
  "properties": {
    "base_revision": {
      "description": "preview/publish 必填；local_expert_read 返回的 base_revision，publish 必须原样携带，不能省略",
      "type": "string"
    },
    "mode": {
      "enum": [
        "preview",
        "publish",
        "cleanup"
      ],
      "type": "string"
    },
    "operation": {
      "description": "preview/publish 必填；cleanup 忽略",
      "enum": [
        "create",
        "fork",
        "update"
      ],
      "type": "string"
    },
    "patch": {
      "description": "preview/publish 必填；基于 local_expert_read 返回精确基线的 unified text Git patch",
      "type": "string"
    },
    "prepare_evaluation": {
      "default": false,
      "description": "仅 preview 使用，默认 false。只有用户对本次候选明确要求评测时才可设为 true；只要 Profile/Skill 本身或测试 case 可能产生写入、通知或其它副作用，即使 case 看起来只读，也必须先展示测试计划并取得用户后续确认。用于为相同 preview tree 准备隐藏的短期候选 Profile，供 task_run 做一次 A/B 评测；patch 变化或评测完成后重新评测需要新的明确要求和必要的副作用确认；评测确认不授权 publish",
      "type": "boolean"
    },
    "preview_receipt": {
      "description": "publish/cleanup 模式必填；preview 返回的不透明 receipt",
      "type": "string"
    },
    "source_ref": {
      "description": "仅 fork 操作必填；精确的 builtin/\u003cname\u003e 引用",
      "type": "string"
    },
    "target_ref": {
      "description": "精确的 local/\u003cname\u003e 目标引用",
      "type": "string"
    }
  },
  "required": [
    "mode"
  ],
  "type": "object"
}
```

## `amux.local_expert_read`

读取 managed local 专家的精确编辑基线；仅当前飞书 Bot owner turn 可用，不写入。

**输入 JSON Schema**

```json
{
  "properties": {
    "operation": {
      "enum": [
        "create",
        "fork",
        "update"
      ],
      "type": "string"
    },
    "source_ref": {
      "description": "仅 fork 操作必填；精确的 builtin/\u003cname\u003e 引用",
      "type": "string"
    },
    "target_ref": {
      "description": "精确的 local/\u003cname\u003e 目标引用",
      "type": "string"
    }
  },
  "required": [
    "operation",
    "target_ref"
  ],
  "type": "object"
}
```

## `amux.memory_forget`

按 revision 条件删除一份记忆文档；不会清除已进入会话的历史。纠正或忘记偏好时还需检查 MEMORY.md 和相关主题，修订重复内容及引用；部分失败须明确报告。

**输入 JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "expected_revision": {
      "description": "memory_get 返回的 SHA-256 revision；文档已不存在时可填 absent",
      "type": "string"
    },
    "path": {
      "description": "当前授权范围内的 Markdown 相对路径",
      "type": "string"
    }
  },
  "required": [
    "path",
    "expected_revision"
  ],
  "type": "object"
}
```

## `amux.memory_get`

按需读取记忆文档或指定行段及 revision；新会话读取一次 MEMORY.md，同一上下文复用。truncated=true 时使用 next_offset 续读并核对 revision，路径不是本机文件。

**输入 JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "end_line": {
      "description": "读取末行（包含）；省略时读取到响应上限",
      "minimum": 0,
      "type": "integer"
    },
    "offset": {
      "description": "上次 memory_get 返回的 next_offset 字节位置；续读时保留相同 path 和 end_line，并核对 revision",
      "minimum": 0,
      "type": "integer"
    },
    "path": {
      "description": "当前授权范围内的 Markdown 相对路径；新会话先读取 MEMORY.md",
      "type": "string"
    },
    "start_line": {
      "description": "读取起始行（从 1 开始）；省略时从开头读取，不能与 offset 同时使用",
      "minimum": 0,
      "type": "integer"
    }
  },
  "required": [
    "path"
  ],
  "type": "object"
}
```

## `amux.memory_search`

按需搜索跨会话记忆；涉及以前的工作、决定或偏好时查询，返回有界命中片段。incomplete=true 表示扫描未完成；无需历史的任务不要检索。

**输入 JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "limit": {
      "description": "最多返回的命中文档数；默认 5，最大 20，仍受响应大小限制",
      "minimum": 0,
      "type": "integer"
    },
    "path_prefix": {
      "description": "当前授权范围内的相对路径前缀，例如 projects/amux/",
      "type": "string"
    },
    "query": {
      "description": "空白分隔的字面关键词；匹配中文连续文本和忽略大小写的英文，不使用正则或语义检索",
      "type": "string"
    }
  },
  "required": [
    "query"
  ],
  "type": "object"
}
```

## `amux.memory_upsert`

按 revision 条件保存长期记忆；用于用户明确要求记住、稳定偏好和已验证经验。详细内容放主题文档，入口保持简短；不保存秘密、临时授权或推测。冲突须重读合并，成功后才能声称已保存。

**输入 JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "content": {
      "description": "完整 UTF-8 Markdown 正文；主题文档最多 64 KiB，MEMORY.md 只保留简短偏好和入口（最多 2 KiB、64 行）",
      "type": "string"
    },
    "expected_revision": {
      "description": "memory_get 返回的 SHA-256 revision；新建文档必须填 absent，冲突后重读合并",
      "type": "string"
    },
    "path": {
      "description": "当前授权范围内的 Markdown 相对路径",
      "type": "string"
    }
  },
  "required": [
    "path",
    "content",
    "expected_revision"
  ],
  "type": "object"
}
```

## `amux.profile_get`

按精确 ref 获取 Agent Profile 完整详情，支持开发版 namespace。

**输入 JSON Schema**

```json
{
  "properties": {
    "ref": {
      "description": "精确的 Agent Profile 引用；需要时可包含显式开发版 namespace",
      "type": "string"
    }
  },
  "required": [
    "ref"
  ],
  "type": "object"
}
```

## `amux.profile_list`

分页列出非开发版 Agent Profile；用 profiles[].ref 提交任务，详情用 profile_get。沿 next_cursor 读取至 has_more=false。

**输入 JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "上一页 profile_list 返回的不透明续页游标",
      "type": "string"
    },
    "namespace": {
      "description": "可选的精确 Pack namespace 过滤条件，例如 expert-agents；翻页时必须保持不变",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.result_continue`

续读分页结果；按序拼接 content_chunk，直到 complete=true。游标仅当前 MCP 进程短期有效。

**输入 JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "分页 MCP 结果返回的 next_cursor",
      "type": "string"
    }
  },
  "required": [
    "cursor"
  ],
  "type": "object"
}
```

## `amux.schedule_create`

创建 hub 定时规则。默认 schedule_type=subtask（推荐用于大多数定时任务：每次隔离运行临时子 Task，自动继承当前会话的 Agent 与模型，不允许 escalate-to-main；在飞书会话内省略 delivery_target 时自动投递回来源话题 origin，也可用 delivery_policy 控制 always/on_failure/on_non_empty）；仅需延续当前对话上下文时使用 schedule_type=session-prompt（默认 reuse-session，向目标 Session Submit 提示词并唤醒 Agent）。创建成功后请立即回复用户，不要在当前回合等待首次触发。

**输入 JSON Schema**

```json
{
  "properties": {
    "delivery_policy": {
      "description": "结果主动投递策略（delivery_target 非 none 时生效）：always（默认，每次终态都推送）、on_failure（仅失败或超时推送）、on_non_empty（仅失败或产出非静默内容时推送，忽略空内容/NO_ALERT/SILENT/HEARTBEAT_OK）",
      "enum": [
        "always",
        "on_failure",
        "on_non_empty"
      ],
      "type": "string"
    },
    "delivery_target": {
      "description": "结果主动投递目标：origin（发回创建时的来源飞书话题/会话；在飞书会话中创建 subtask 且省略时自动默认 origin）、none（仅落库不主动推送）。注意 session-prompt + reuse-session 不允许 origin",
      "enum": [
        "none",
        "origin"
      ],
      "type": "string"
    },
    "name": {
      "description": "Schedule 的可选显示名称",
      "type": "string"
    },
    "prompt": {
      "description": "schedule_type=session-prompt 时到期投递到目标 Session 的提示词",
      "type": "string"
    },
    "run_mode": {
      "description": "仅 session-prompt：reuse-session（默认）复用目标 Session 延续对话上下文；new-session-per-run（旧版兼容）每次按模板新开 Session",
      "enum": [
        "reuse-session",
        "new-session-per-run"
      ],
      "type": "string"
    },
    "schedule_type": {
      "description": "subtask（默认，推荐用于大多数定时任务）：每次隔离执行临时子 Task，自动继承当前会话的 Agent/模型（未显式指定时），结果按 delivery_target/delivery_policy 主动送达；session-prompt：向目标 Session Submit 提示词并唤醒 Agent（适合依赖当前会话上下文的后续跟进）",
      "enum": [
        "subtask",
        "session-prompt"
      ],
      "type": "string"
    },
    "session": {
      "description": "schedule_type=session-prompt 时的目标 session-ref（与 CLI amux schedule create \u003csession-ref\u003e 相同：target/name、Session ID 或名称）；省略时回落到 AMUX_SESSION_ID",
      "type": "string"
    },
    "spec": {
      "description": "时间规则：every \u003cduration\u003e（支持 d/w）、daily \u003cHH:MM\u003e、at \u003cwhen\u003e、in \u003cduration\u003e（相对一次性）或 cron \u003cexpr\u003e（支持 @daily 等描述符）",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile 引用（取自 amux.profile_list 的 profiles[].ref）；在当前会话内创建时可省略，默认自动继承当前会话的 Agent Profile 与模型",
          "type": "string"
        },
        "credential_profile": {
          "description": "控制面 credential profile 名称",
          "type": "string"
        },
        "extra_args": {
          "description": "原生执行参数；权限参数按 permission_policy 归一化，其他参数保留顺序",
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "isolation": {
          "description": "Sandbox 隔离模式；Phase 1 为 best-effort trusted mode",
          "enum": [
            "process",
            "private-home"
          ],
          "type": "string"
        },
        "model": {
          "description": "每次定时运行面向用户的模型覆盖；默认应传入当前会话模型，留空时使用 Profile 或 Agent 默认模型",
          "type": "string"
        },
        "permission_policy": {
          "description": "Task 与原生权限的统一策略：default 或 policy-allow（默认），或 deny-all；定时 Task 不支持 escalate-to-main；覆盖原生权限参数",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "reasoning_effort": {
          "description": "每次定时运行面向用户的思考强度覆盖；默认应传入当前会话思考强度，留空时使用所选模型声明的默认值",
          "type": "string"
        },
        "run_as": {
          "description": "可选的已登记 Principal alias；保存 Schedule 时解析并固化",
          "type": "string"
        },
        "runtime": {
          "description": "执行 Runtime 适配器；省略时使用 builtin 快捷入口预设，其他专家使用发行版默认执行配置",
          "type": "string"
        },
        "task": {
          "description": "Schedule 每次触发时提交的 Task 提示词",
          "type": "string"
        },
        "timeout_seconds": {
          "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）",
          "minimum": 0,
          "type": "integer"
        },
        "trusted_mode_ack": {
          "description": "旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留",
          "type": "boolean"
        },
        "workspace": {
          "properties": {
            "local_dir": {
              "description": "目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
              "type": "string"
            },
            "policy": {
              "description": "temp-dir 为每次运行创建一次性 Workspace；git-worktree 从 repo cache 检出 worktree",
              "enum": [
                "temp-dir",
                "git-worktree"
              ],
              "type": "string"
            },
            "repo": {
              "properties": {
                "cache_key": {
                  "description": "在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
                  "type": "string"
                },
                "commit": {
                  "description": "Workspace repo 的 commit 或 ref",
                  "type": "string"
                },
                "kind": {
                  "description": "Workspace repo 类型，例如 git",
                  "type": "string"
                }
              },
              "type": "object"
            }
          },
          "type": "object"
        }
      },
      "required": [
        "task"
      ],
      "type": "object"
    },
    "timezone": {
      "description": "可选 IANA 时区名称（例如 Asia/Shanghai）；省略时使用 Hub 本地时区",
      "type": "string"
    }
  },
  "required": [
    "spec"
  ],
  "type": "object"
}
```

## `amux.schedule_delete`

删除 Schedule；既有运行对应的 Task 结果仍可读取。

**输入 JSON Schema**

```json
{
  "properties": {
    "schedule_id": {
      "type": "integer"
    }
  },
  "required": [
    "schedule_id"
  ],
  "type": "object"
}
```

## `amux.schedule_get`

获取 Schedule；include_runs=true 时包含运行历史及 task_id。

**输入 JSON Schema**

```json
{
  "properties": {
    "include_runs": {
      "description": "是否包含 Schedule 运行历史；使用 runs 中的 task_id 调用 task_get 或 task_stream 获取结果",
      "type": "boolean"
    },
    "schedule_id": {
      "type": "integer"
    }
  },
  "required": [
    "schedule_id"
  ],
  "type": "object"
}
```

## `amux.schedule_list`

分页列出 Schedule（含 created_at_iso 与投递设置；勿将更早或其他话题已完成的同名历史任务误当作本次新建）；沿 next_cursor 读取至 has_more=false。

**输入 JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "上一页 schedule_list 返回的不透明续页游标",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.schedule_result_get`

只读获取定时任务单次执行的结构化结果（覆盖 session-prompt 与 subtask，含执行状态、最终 Markdown 正文与飞书送达回执）。当需要查看定时任务最近一次或指定 execution_id 的最终产出与送达情况时调用。

**输入 JSON Schema**

```json
{
  "properties": {
    "execution_id": {
      "description": "可选执行 ID（来自 amux.schedule_get 的 runs[].execution_id）；省略或 0 返回最近一次执行结果",
      "type": "integer"
    },
    "schedule_id": {
      "description": "定时任务 ID",
      "type": "integer"
    }
  },
  "required": [
    "schedule_id"
  ],
  "type": "object"
}
```

## `amux.schedule_set_enabled`

启用或停用 Schedule。

**输入 JSON Schema**

```json
{
  "properties": {
    "enabled": {
      "type": "boolean"
    },
    "schedule_id": {
      "type": "integer"
    }
  },
  "required": [
    "schedule_id",
    "enabled"
  ],
  "type": "object"
}
```

## `amux.schedule_trigger`

立即手动触发一次 Schedule（不影响其启停状态与下次计划触发时间）。

**输入 JSON Schema**

```json
{
  "properties": {
    "schedule_id": {
      "type": "integer"
    }
  },
  "required": [
    "schedule_id"
  ],
  "type": "object"
}
```

## `amux.schedule_update`

原地更新 Schedule 的名称、时间规则、时区、结果投递设置（delivery_target/delivery_policy）、session-prompt 的 prompt/run_mode 或 subtask 的局部 Task 字段；省略字段保持不变，不允许 escalate-to-main。

**输入 JSON Schema**

```json
{
  "properties": {
    "delivery_policy": {
      "description": "更新结果主动投递策略（always、on_failure 或 on_non_empty）；省略时保持不变",
      "enum": [
        "always",
        "on_failure",
        "on_non_empty"
      ],
      "type": "string"
    },
    "delivery_target": {
      "description": "更新结果主动投递目标（none 或 origin）；省略时保持不变",
      "enum": [
        "none",
        "origin"
      ],
      "type": "string"
    },
    "name": {
      "description": "Schedule 的新显示名称；省略时保持不变",
      "type": "string"
    },
    "prompt": {
      "description": "仅 session-prompt：更新到期投递到目标 Session 的提示词；省略时保持不变",
      "type": "string"
    },
    "run_mode": {
      "description": "仅 session-prompt：更新执行模式（reuse-session 或 new-session-per-run）；省略时保持不变",
      "enum": [
        "reuse-session",
        "new-session-per-run"
      ],
      "type": "string"
    },
    "schedule_id": {
      "type": "integer"
    },
    "spec": {
      "description": "新的时间规则（every \u003cduration\u003e、daily \u003cHH:MM\u003e、at \u003cwhen\u003e、in \u003cduration\u003e 或 cron \u003cexpr\u003e）；省略时保持不变，修改后会重新计算 next_run_at",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile 引用（取自 amux.profile_list 的 profiles[].ref）；在当前会话内创建时可省略，默认自动继承当前会话的 Agent Profile 与模型",
          "type": "string"
        },
        "credential_profile": {
          "description": "控制面 credential profile 名称",
          "type": "string"
        },
        "extra_args": {
          "description": "原生执行参数；权限参数按 permission_policy 归一化，其他参数保留顺序",
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "isolation": {
          "description": "Sandbox 隔离模式；Phase 1 为 best-effort trusted mode",
          "enum": [
            "process",
            "private-home"
          ],
          "type": "string"
        },
        "model": {
          "description": "每次定时运行面向用户的模型覆盖；默认应传入当前会话模型，留空时使用 Profile 或 Agent 默认模型",
          "type": "string"
        },
        "permission_policy": {
          "description": "Task 与原生权限的统一策略：default 或 policy-allow（默认），或 deny-all；定时 Task 不支持 escalate-to-main；覆盖原生权限参数",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "reasoning_effort": {
          "description": "每次定时运行面向用户的思考强度覆盖；默认应传入当前会话思考强度，留空时使用所选模型声明的默认值",
          "type": "string"
        },
        "run_as": {
          "description": "可选的已登记 Principal alias；保存 Schedule 时解析并固化",
          "type": "string"
        },
        "runtime": {
          "description": "执行 Runtime 适配器；省略时使用 builtin 快捷入口预设，其他专家使用发行版默认执行配置",
          "type": "string"
        },
        "task": {
          "description": "Schedule 每次触发时提交的 Task 提示词",
          "type": "string"
        },
        "timeout_seconds": {
          "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）",
          "minimum": 0,
          "type": "integer"
        },
        "trusted_mode_ack": {
          "description": "旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留",
          "type": "boolean"
        },
        "workspace": {
          "properties": {
            "local_dir": {
              "description": "目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
              "type": "string"
            },
            "policy": {
              "description": "temp-dir 为每次运行创建一次性 Workspace；git-worktree 从 repo cache 检出 worktree",
              "enum": [
                "temp-dir",
                "git-worktree"
              ],
              "type": "string"
            },
            "repo": {
              "properties": {
                "cache_key": {
                  "description": "在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
                  "type": "string"
                },
                "commit": {
                  "description": "Workspace repo 的 commit 或 ref",
                  "type": "string"
                },
                "kind": {
                  "description": "Workspace repo 类型，例如 git",
                  "type": "string"
                }
              },
              "type": "object"
            }
          },
          "type": "object"
        }
      },
      "type": "object"
    },
    "timezone": {
      "description": "新的 IANA 时区名称（例如 Asia/Shanghai，传空字符串回落 Hub 本地时区）；省略时保持不变",
      "type": "string"
    }
  },
  "required": [
    "schedule_id"
  ],
  "type": "object"
}
```

## `amux.session_interrupt`

中断 Session 当前 turn。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_list`

按 runtime 分组列出调用方可见的 Session；创建 Session 时不要预先调用，先省略 target 调用 session_run，仅在其报告多个 target 后用本工具选择。

**输入 JSON Schema**

```json
{
  "properties": {},
  "type": "object"
}
```

## `amux.session_prune`

破坏性操作：永久删除已归档 Session；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent；Task 历史使用 task_prune。

**输入 JSON Schema**

```json
{
  "properties": {
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.session_queue_cancel`

按 queue_id 丢弃 Session 队列中的一条输入。

**输入 JSON Schema**

```json
{
  "properties": {
    "queue_id": {
      "description": "要取消的队列输入 ID，由 amux.session_queue_list 返回",
      "type": "string"
    },
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session",
    "queue_id"
  ],
  "type": "object"
}
```

## `amux.session_queue_clear`

丢弃 Session 全部待处理输入。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_queue_list`

按最早优先列出 Session 待处理输入。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_restart`

原地重启 Session Agent 进程。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_resume`

唤醒 sleeping Session。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_run`

用 Agent Profile 创建 Session；profile 和 name 必填。未指定 target 时先直接调用：唯一连接 target 会自动选择，多个 target 会在创建前返回明确错误。

**输入 JSON Schema**

```json
{
  "properties": {
    "args": {
      "description": "原生执行参数，包含权限设置",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "cwd": {
      "type": "string"
    },
    "model": {
      "description": "执行模型引用；省略时使用所选 Runtime 默认模型",
      "type": "string"
    },
    "name": {
      "description": "Session 名称（必填）",
      "type": "string"
    },
    "profile": {
      "description": "Agent Profile 引用（例如 builtin/codex）或旧版 Agent Type",
      "type": "string"
    },
    "ref": {
      "type": "string"
    },
    "repo": {
      "type": "string"
    },
    "run_as": {
      "type": "string"
    },
    "runtime": {
      "description": "执行 Runtime 适配器；外部 v2 专家必填，具有 amux 调用方默认值的 builtin 快捷入口可省略",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时仅在唯一连接 target 时自动选择",
      "type": "string"
    }
  },
  "required": [
    "profile",
    "name"
  ],
  "type": "object"
}
```

## `amux.session_send`

向 Session 输入队列提交提示词；名称有歧义时用 target 消歧。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    },
    "text": {
      "description": "要排入 Session 输入队列的提示词文本",
      "type": "string"
    }
  },
  "required": [
    "session",
    "text"
  ],
  "type": "object"
}
```

## `amux.session_set_model`

切换 Session 模型并保留对话历史；支持原生模型与 provider-backed 模型 ref（如 custom/example-model），无歧义时也可用裸上游名（如 example-model）；满足热条件时不重启进程，否则原地重启并经 provider resume/handoff 保留对话，跨 provider 切换会开新对话并注入 handoff 历史；Task/Instance 持有的 Session 不支持。

**输入 JSON Schema**

```json
{
  "properties": {
    "model": {
      "description": "要切换到的模型：原生模型名或 provider-backed 模型 ref（如 custom/example-model），无歧义时可用裸上游名（如 example-model）",
      "type": "string"
    },
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session",
    "model"
  ],
  "type": "object"
}
```

## `amux.session_sleep`

暂停 Session Agent 进程并保持可恢复。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_stop`

归档 Session；Task 历史使用 task_prune 清理。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.session_stream`

一次性 replay 并排空 Session 最近输出，返回有界聚合文本；不是实时 tail。

**输入 JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session 名称或 ID",
      "type": "string"
    },
    "target": {
      "description": "目标 runtime ID；留空时使用 hub 默认目标",
      "type": "string"
    }
  },
  "required": [
    "session"
  ],
  "type": "object"
}
```

## `amux.task_cancel`

取消 Agent Task，并中断或终止其 child Session。

**输入 JSON Schema**

```json
{
  "properties": {
    "task_id": {
      "type": "string"
    }
  },
  "required": [
    "task_id"
  ],
  "type": "object"
}
```

## `amux.task_get`

获取 Agent Task 状态、结构化结果和 session_ref。

**输入 JSON Schema**

```json
{
  "properties": {
    "task_id": {
      "type": "string"
    }
  },
  "required": [
    "task_id"
  ],
  "type": "object"
}
```

## `amux.task_list`

列出调用方可见的 Agent Task 及状态、session_ref 和可用结果。

**输入 JSON Schema**

```json
{
  "properties": {},
  "type": "object"
}
```

## `amux.task_prune`

清理终态 Task 历史；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent。

**输入 JSON Schema**

```json
{
  "properties": {
    "dry_run": {
      "type": "boolean"
    },
    "older_than_seconds": {
      "minimum": 0,
      "type": "integer"
    },
    "statuses": {
      "items": {
        "type": "string"
      },
      "type": "array"
    }
  },
  "type": "object"
}
```

## `amux.task_respond_permission`

应答 escalate-to-main Task 上抛的权限请求。

**输入 JSON Schema**

```json
{
  "properties": {
    "allow": {
      "type": "boolean"
    },
    "option_id": {
      "type": "string"
    },
    "permission_id": {
      "type": "string"
    },
    "reason": {
      "type": "string"
    },
    "task_id": {
      "type": "string"
    }
  },
  "required": [
    "task_id",
    "allow"
  ],
  "type": "object"
}
```

## `amux.task_run`

阻塞运行短 Agent Task；仅支持 policy-allow 或 deny-all，escalate-to-main 会死锁。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref；也可传入先经 amux.profile_get 验证的精确开发版 Profile 引用，例如 builtin/codex、team/push_sre 或 team-task-dev/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "持久 Agent Instance 句柄（mode=persistent）；复用 instance_spawn 返回的值调用 instance_followup、instance_send 或 instance_interrupt",
      "type": "string"
    },
    "credential_profile": {
      "description": "控制面 credential profile 名称",
      "type": "string"
    },
    "extra_args": {
      "description": "原生执行参数；权限参数按 permission_policy 归一化，其他参数保留顺序",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "isolation": {
      "description": "Sandbox 隔离模式；Phase 1 为 best-effort trusted mode",
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "mode": {
      "description": "ephemeral（默认）只运行一次并销毁；persistent 让 Agent Instance 跨 Task 保持存活，以支持多轮交互",
      "enum": [
        "ephemeral",
        "persistent"
      ],
      "type": "string"
    },
    "model": {
      "description": "执行模型引用；省略时使用父会话的当前模型与思考强度（同引擎时），否则使用继承的启动预设模型或所选引擎默认模型",
      "type": "string"
    },
    "notify_parent": {
      "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭",
      "type": "boolean"
    },
    "permission_policy": {
      "description": "Task 与原生权限的统一策略：policy-allow（默认）、escalate-to-main 或 deny-all；覆盖原生权限参数，持久实例沿用该策略",
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
      "type": "string"
    },
    "reasoning_effort": {
      "description": "执行思考强度；会话内创建的子任务在同引擎时沿用该会话的当前强度，否则使用所选模型声明的默认值",
      "type": "string"
    },
    "result_granularity": {
      "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，最大 64KB，截断时指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "可选 Principal alias 意图；由 amuxd 解析并授权，未指定时 child 继承 parent 的 Execution Principal",
      "type": "string"
    },
    "runtime": {
      "description": "执行 Runtime 适配器；省略时使用 builtin 快捷入口预设，其他专家使用发行版默认执行配置",
      "type": "string"
    },
    "task": {
      "description": "交给 Agent Task 的提示词",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "整个 Task 的截止时间（秒），从 Task 被接受时开始计算，并包含准备与 Agent 启动时间；0 或省略时使用 runtime 默认值（30 分钟）",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "description": "旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留",
      "type": "boolean"
    },
    "workspace": {
      "properties": {
        "local_dir": {
          "description": "目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
          "type": "string"
        },
        "policy": {
          "enum": [
            "temp-dir",
            "git-worktree"
          ],
          "type": "string"
        },
        "repo": {
          "properties": {
            "cache_key": {
              "description": "在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
              "type": "string"
            },
            "commit": {
              "type": "string"
            },
            "kind": {
              "type": "string"
            }
          },
          "type": "object"
        }
      },
      "type": "object"
    }
  },
  "required": [
    "agent",
    "task"
  ],
  "type": "object"
}
```

## `amux.task_stream`

从 from_seq 长轮询 Agent Task 事件，返回 next_seq 供无损续读。

**输入 JSON Schema**

```json
{
  "properties": {
    "from_seq": {
      "description": "从该事件序号继续读取；传入上一次调用返回的 next_seq",
      "type": "integer"
    },
    "task_id": {
      "type": "string"
    }
  },
  "required": [
    "task_id"
  ],
  "type": "object"
}
```

## `amux.task_submit`

异步提交 Agent Task，立即返回 task_id。agent 优先使用 profile_list 的 ref；开发版先经 profile_get 验证。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref；也可传入先经 amux.profile_get 验证的精确开发版 Profile 引用，例如 builtin/codex、team/push_sre 或 team-task-dev/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "持久 Agent Instance 句柄（mode=persistent）；复用 instance_spawn 返回的值调用 instance_followup、instance_send 或 instance_interrupt",
      "type": "string"
    },
    "credential_profile": {
      "description": "控制面 credential profile 名称",
      "type": "string"
    },
    "extra_args": {
      "description": "原生执行参数；权限参数按 permission_policy 归一化，其他参数保留顺序",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "isolation": {
      "description": "Sandbox 隔离模式；Phase 1 为 best-effort trusted mode",
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "mode": {
      "description": "ephemeral（默认）只运行一次并销毁；persistent 让 Agent Instance 跨 Task 保持存活，以支持多轮交互",
      "enum": [
        "ephemeral",
        "persistent"
      ],
      "type": "string"
    },
    "model": {
      "description": "执行模型引用；省略时使用父会话的当前模型与思考强度（同引擎时），否则使用继承的启动预设模型或所选引擎默认模型",
      "type": "string"
    },
    "notify_parent": {
      "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭",
      "type": "boolean"
    },
    "permission_policy": {
      "description": "Task 与原生权限的统一策略：policy-allow（默认）、escalate-to-main 或 deny-all；覆盖原生权限参数，持久实例沿用该策略",
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
      "type": "string"
    },
    "reasoning_effort": {
      "description": "执行思考强度；会话内创建的子任务在同引擎时沿用该会话的当前强度，否则使用所选模型声明的默认值",
      "type": "string"
    },
    "result_granularity": {
      "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，最大 64KB，截断时指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "可选 Principal alias 意图；由 amuxd 解析并授权，未指定时 child 继承 parent 的 Execution Principal",
      "type": "string"
    },
    "runtime": {
      "description": "执行 Runtime 适配器；省略时使用 builtin 快捷入口预设，其他专家使用发行版默认执行配置",
      "type": "string"
    },
    "task": {
      "description": "交给 Agent Task 的提示词",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "整个 Task 的截止时间（秒），从 Task 被接受时开始计算，并包含准备与 Agent 启动时间；0 或省略时使用 runtime 默认值（30 分钟）",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "description": "旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留",
      "type": "boolean"
    },
    "workspace": {
      "properties": {
        "local_dir": {
          "description": "目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
          "type": "string"
        },
        "policy": {
          "enum": [
            "temp-dir",
            "git-worktree"
          ],
          "type": "string"
        },
        "repo": {
          "properties": {
            "cache_key": {
              "description": "在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key",
              "type": "string"
            },
            "commit": {
              "type": "string"
            },
            "kind": {
              "type": "string"
            }
          },
          "type": "object"
        }
      },
      "type": "object"
    }
  },
  "required": [
    "agent",
    "task"
  ],
  "type": "object"
}
```

## `amux.turn_result_get`

只读获取单轮对话的结构化结果（执行状态、最终 Markdown 正文与飞书卡片/附件送达回执）。当需要核实某轮回答是否真正执行完成、取回完整正文、或确认飞书是否送达及对应 message_id 时调用；支持按 session+turn_id（可用 latest）或 feishu_message_id 查询。

**输入 JSON Schema**

```json
{
  "properties": {
    "bridge_id": {
      "description": "可选飞书机器人/bridge ID，配合 feishu_message_id 消歧",
      "type": "string"
    },
    "feishu_message_id": {
      "description": "触发该轮的飞书消息 ID（om_...）；与 session+turn_id 二选一",
      "type": "string"
    },
    "session": {
      "description": "Session 引用（如 target/name 或 session_id）；省略时若未传 feishu_message_id 则回落 AMUX_SESSION_ID",
      "type": "string"
    },
    "turn_id": {
      "description": "轮次 ID，或传 latest 查该会话最新一轮",
      "type": "string"
    }
  },
  "type": "object"
}
```
