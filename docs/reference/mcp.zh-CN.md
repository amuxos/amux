<!-- Code generated from app/cli's MCP tool registry; DO NOT EDIT. -->

# amux MCP 工具参考

[English](mcp.md) | [简体中文](mcp.zh-CN.md)

本文档由 `amux mcp` 的实际 `tools/list` 注册表自动生成，覆盖正式工具、工具说明与完整输入 JSON Schema。修改 MCP 工具名称、可见性、说明或输入字段后，必须运行 `make mcp-doc`；仓库统一门禁会拒绝生成结果不一致的改动。 本参考中的 Schema 说明文字经过翻译；运行时 Schema 与校验字段保持不变。

MCP 的产品语义、权限与生命周期边界仍以 [`docs/current/cli-product-model.md`](../current/cli-product-model.zh-CN.md) 和 [`docs/distributed-task-model.md`](../distributed-task-model.zh-CN.md) 为准。运行时还会通过 [`amux.result_continue`](#amuxresult_continue) 对超过单页预算的成功结果续传；工具结果本身不是 `tools/list` 注册元数据，因此不在本文档中另维护一份可能漂移的输出 Schema。

`tools/list` 正式工具：**49**。

## 正式工具索引

| 工具 | 说明 |
| --- | --- |
| `amux.collaborator_list` | 仅在准备实际委派时按 kind 列出内部 Agent 或 external_bot；纯 Agent 能力发现请使用 amux.profile_list，不要调用本工具。external_bot 只返回当前 Session 所绑定飞书群内的 Bot；CLI、非飞书或无有效群 binding 时为空。 |
| `amux.delegation_cancel` | 请求取消支持 cancelable 的 Delegation；cancelled 是停止信号，不得自动创建替代委派。 |
| `amux.delegation_create` | 创建一次性 Delegation 并返回 ID。external_bot 仅在用户明确要求或确认后使用，且只能发送给当前 Session 所绑定飞书群内的 Bot；结果为 external_untrusted。创建预算默认最多 2 次，用户指定数量为硬上限。 |
| `amux.delegation_get` | 读取 Delegation 当前快照；增量事件使用 delegation_wait。 |
| `amux.delegation_intervene` | 回答支持 permission_response 的 Delegation 权限请求。 |
| `amux.delegation_wait` | 等待 Delegation；event 增量返回事件，actionable 避免普通进度反复唤醒主模型并保留权限/终态唤醒。wait_timed_out 不是 Delegation 终态；按返回状态维护实际创建的尝试账本。 |
| `amux.instance_followup` | 向持久 Agent 交付任务并立即触发新 turn，保留 provider 对话上下文。 |
| `amux.instance_interrupt` | 中断持久 Agent 当前 turn。 |
| `amux.instance_list` | 列出持久 Agent Instance 状态、child Session 状态和 mailbox 深度。 |
| `amux.instance_send` | 向持久 Agent mailbox 入队消息但不唤醒，不启动新 turn。 |
| `amux.instance_spawn` | 非阻塞创建持久 Agent Instance，返回稳定的 agent_instance_id。 |
| `amux.instance_wait` | 等待持久 Agent 当前 turn 结束或超时；结果仍用 task_get/task_stream 获取。 |
| `amux.local_expert_change` | 预览、发布或清理 managed local 专家 patch。publish 仅限后续明确确认的 owner turn，并须原样提交相同 patch 与 receipt；评测确认不等于发布确认。 |
| `amux.local_expert_read` | 读取 managed local 专家的精确编辑基线；仅当前飞书 Bot owner turn 可用，不写入。 |
| `amux.memory_forget` | 按 revision 条件删除一份记忆文档；不会清除已进入会话的历史。纠正或忘记偏好时还需检查 MEMORY.md 和相关主题，修订重复内容及引用；部分失败须明确报告。 |
| `amux.memory_get` | 按需读取记忆文档或指定行段及 revision；新会话读取一次 MEMORY.md，同一上下文复用。truncated=true 时使用 next_offset 续读并核对 revision，路径不是本机文件。 |
| `amux.memory_search` | 按需搜索跨会话记忆；涉及以前的工作、决定或偏好时查询，返回有界命中片段。incomplete=true 表示扫描未完成；无需历史的任务不要检索。 |
| `amux.memory_upsert` | 按 revision 条件保存长期记忆；用于用户明确要求记住、稳定偏好和已验证经验。详细内容放主题文档，入口保持简短；不保存秘密、临时授权或推测。冲突须重读合并，成功后才能声称已保存。 |
| `amux.profile_get` | 按精确 ref 获取 Agent Profile 完整详情，支持开发版 namespace。 |
| `amux.profile_list` | 分页列出非开发版 Agent Profile；用 profiles[].ref 提交任务，详情用 profile_get。沿 next_cursor 读取至 has_more=false。 |
| `amux.result_continue` | 续读分页结果；按序拼接 content_chunk，直到 complete=true。游标仅当前 MCP 进程短期有效。 |
| `amux.schedule_create` | 创建 hub 定时 Agent Task；运行历史只记录 task_id，结果用 task_get/task_stream；不允许 escalate-to-main。 |
| `amux.schedule_delete` | 删除 Schedule；既有运行对应的 Task 结果仍可读取。 |
| `amux.schedule_get` | 获取 Schedule；include_runs=true 时包含运行历史及 task_id。 |
| `amux.schedule_list` | 分页列出 Schedule；沿 next_cursor 读取至 has_more=false。 |
| `amux.schedule_set_enabled` | 启用或停用 Schedule。 |
| `amux.schedule_update` | 原地更新 Schedule 的名称、时间规则或 Task；省略字段保持不变，不允许 escalate-to-main。 |
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

## `amux.instance_followup`

向持久 Agent 交付任务并立即触发新 turn，保留 provider 对话上下文。

**输入 JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "description": "要唤醒并接收 Task 的持久 Agent Instance，取自 instance_spawn 的返回值",
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

中断持久 Agent 当前 turn。

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

非阻塞创建持久 Agent Instance，返回稳定的 agent_instance_id。

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
    "isolation": {
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "model": {
      "description": "可选模型引用，必须来自所选 Profile 的 supported_models；该值会固定到持久 Agent Instance",
      "type": "string"
    },
    "permission_policy": {
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
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

## `amux.instance_wait`

等待持久 Agent 当前 turn 结束或超时；结果仍用 task_get/task_stream 获取。

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

创建 hub 定时 Agent Task；运行历史只记录 task_id，结果用 task_get/task_stream；不允许 escalate-to-main。

**输入 JSON Schema**

```json
{
  "properties": {
    "name": {
      "description": "Schedule 的可选显示名称",
      "type": "string"
    },
    "spec": {
      "description": "时间规则：every \u003cduration\u003e、daily \u003cHH:MM\u003e、at \u003cwhen\u003e 或 cron \u003cexpr\u003e",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref，例如 builtin/codex 或 team/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口",
          "type": "string"
        },
        "credential_profile": {
          "description": "控制面 credential profile 名称",
          "type": "string"
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
          "description": "每次定时运行面向用户的模型覆盖；留空时使用 Profile 或 Agent 默认模型",
          "type": "string"
        },
        "permission_policy": {
          "description": "留空或 default 时使用 Profile 或 Task 默认策略；允许 policy-allow 和 deny-all；定时 Agent Task 不允许 escalate-to-main",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "run_as": {
          "description": "可选的已登记 Principal alias；保存 Schedule 时解析并固化",
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
        "agent",
        "task"
      ],
      "type": "object"
    }
  },
  "required": [
    "spec",
    "task"
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

分页列出 Schedule；沿 next_cursor 读取至 has_more=false。

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

## `amux.schedule_update`

原地更新 Schedule 的名称、时间规则或 Task；省略字段保持不变，不允许 escalate-to-main。

**输入 JSON Schema**

```json
{
  "properties": {
    "name": {
      "description": "Schedule 的新显示名称；省略时保持不变",
      "type": "string"
    },
    "schedule_id": {
      "type": "integer"
    },
    "spec": {
      "description": "新的时间规则（every \u003cduration\u003e、daily \u003cHH:MM\u003e、at \u003cwhen\u003e 或 cron \u003cexpr\u003e）；省略时保持不变，修改后会重新计算 next_run_at",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref，例如 builtin/codex 或 team/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口",
          "type": "string"
        },
        "credential_profile": {
          "description": "控制面 credential profile 名称",
          "type": "string"
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
          "description": "每次定时运行面向用户的模型覆盖；留空时使用 Profile 或 Agent 默认模型",
          "type": "string"
        },
        "permission_policy": {
          "description": "留空或 default 时使用 Profile 或 Task 默认策略；允许 policy-allow 和 deny-all；定时 Agent Task 不允许 escalate-to-main",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "run_as": {
          "description": "可选的已登记 Principal alias；保存 Schedule 时解析并固化",
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
        "agent",
        "task"
      ],
      "type": "object"
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
    "cwd": {
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
      "description": "可选模型引用，必须来自所选 Profile 的 supported_models",
      "type": "string"
    },
    "permission_policy": {
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
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
      "description": "可选模型引用，必须来自所选 Profile 的 supported_models",
      "type": "string"
    },
    "permission_policy": {
      "enum": [
        "policy-allow",
        "escalate-to-main",
        "deny-all"
      ],
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
