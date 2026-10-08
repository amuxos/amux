<!-- Code generated from app/cli's MCP tool registry; DO NOT EDIT. -->

# amux MCP Reference

[English](mcp.md) | [简体中文](mcp.zh-CN.md)

This reference is generated from the actual `amux mcp` tools/list registry and covers public tools, descriptions, and complete input JSON Schemas. Run `make mcp-doc` after changing tool names, visibility, descriptions, or input fields. The repository check rejects stale output. Schema descriptions are translated for this reference; runtime schemas and validation fields are unchanged.

MCP product semantics, permissions, and lifecycle boundaries are defined by the [CLI product model](../current/cli-product-model.md) and [Task model](../distributed-task-model.md). At runtime, [`amux.result_continue`](#amuxresult_continue) continues successful results that exceed a page budget. Tool results are not tools/list registration metadata, so this reference does not maintain a separate output Schema that could drift.

Official tools in `tools/list`: **49**.

## Public tool index

| Tool | Description |
| --- | --- |
| `amux.collaborator_list` | List internal Agents or external_bot by kind only when preparing an actual delegation. Use amux.profile_list for Agent capability discovery. external_bot returns only Bots in the Feishu group bound to the current Session; CLI, non-Feishu, or missing group bindings return an empty list. |
| `amux.delegation_cancel` | Request cancellation of a Delegation that supports cancelable. cancelled is a stop signal; do not automatically create a replacement delegation. |
| `amux.delegation_create` | Create a one-off Delegation and return its ID. Use external_bot only when the user explicitly requests or confirms it, and only for Bots in the Feishu group bound to the current Session. Results are external_untrusted. The default creation budget is two attempts; a user-specified count is a hard limit. |
| `amux.delegation_get` | Read the current Delegation snapshot. Use delegation_wait for incremental events. |
| `amux.delegation_intervene` | Answer a permission request for a Delegation that supports permission_response. |
| `amux.delegation_wait` | Wait for a Delegation. event returns incremental events; actionable avoids waking the main model repeatedly for ordinary progress while preserving permission and terminal-state wakeups. wait_timed_out is not a terminal Delegation state. Track actual created attempts using the returned status. |
| `amux.instance_followup` | Deliver a task to a persistent Agent and immediately start a new turn, retaining the provider conversation context. |
| `amux.instance_interrupt` | Interrupt the current turn of a persistent Agent. |
| `amux.instance_list` | List persistent Agent Instance status, child Session status, and mailbox depth. |
| `amux.instance_send` | Enqueue a message in a persistent Agent mailbox without waking it or starting a new turn. |
| `amux.instance_spawn` | Create a persistent Agent Instance without blocking and return a stable agent_instance_id. |
| `amux.instance_wait` | Wait until the persistent Agent's current turn ends or the wait times out. Retrieve results with task_get/task_stream. |
| `amux.local_expert_change` | Preview, publish, or clean up a managed local expert patch. Publishing requires a later, explicitly confirmed owner turn with the exact same patch and receipt. Evaluation approval does not authorize publication. |
| `amux.local_expert_read` | Read the exact editing baseline of a managed local expert. Available only in the current Feishu Bot owner's turn; performs no writes. |
| `amux.memory_forget` | Delete one memory document conditionally on revision; this does not erase existing conversation history. When correcting or forgetting a preference, also inspect MEMORY.md and related topics and revise duplicate content and references. Report partial failures clearly. |
| `amux.memory_get` | Read a memory document or line range and its revision on demand; read MEMORY.md once in a new conversation and reuse it in the same context. If truncated=true, continue with next_offset and verify revision. Paths are not local files. |
| `amux.memory_search` | Search cross-conversation memory on demand when earlier work, decisions or preferences are relevant; returns bounded snippets. incomplete=true means scanning did not finish. Do not search for tasks that need no history. |
| `amux.memory_upsert` | Save long-term memory conditionally on revision for explicit requests to remember, stable preferences and verified lessons. Put details in topic documents and keep the entry brief. Do not save secrets, temporary authorization or speculation. Re-read and merge conflicts; report saved only after success. |
| `amux.profile_get` | Get complete Agent Profile details by exact ref, including development namespaces. |
| `amux.profile_list` | List non-development Agent Profiles with pagination. Submit tasks using profiles[].ref and fetch details with profile_get. Follow next_cursor until has_more=false. |
| `amux.result_continue` | Continue reading a paginated result. Concatenate content_chunk values in order until complete=true. Cursors are short-lived and valid only in the current MCP process. |
| `amux.schedule_create` | Create a Hub schedule. Default schedule_type=subtask (ephemeral child Task; use task_get/task_stream for results; escalate-to-main is not allowed). With schedule_type=session-prompt, Submit a prompt into the target Session (explicit session session-ref parameter, or AMUX_SESSION_ID fallback) and wake the Agent. |
| `amux.schedule_delete` | Delete a Schedule. Task results from existing runs remain readable. |
| `amux.schedule_get` | Get a Schedule; include_runs=true includes run history and task_id values. |
| `amux.schedule_list` | List Schedules with pagination. Follow next_cursor until has_more=false. |
| `amux.schedule_set_enabled` | Enable or disable a Schedule. |
| `amux.schedule_update` | Update a Schedule's name, time rule, or Task in place. Omitted fields remain unchanged. escalate-to-main is not allowed. |
| `amux.session_interrupt` | Interrupt the current Session turn. |
| `amux.session_list` | List caller-visible Sessions grouped by Runtime. Do not call this before creating a Session: first call session_run without target, and use this tool to choose only if it reports multiple targets. |
| `amux.session_prune` | Destructive operation: permanently delete archived Sessions. Only the enrollment operator may use it in isolated clusters; delegated agents may use it in non-isolated clusters. Use task_prune for Task history. |
| `amux.session_queue_cancel` | Drop one Session queue input by queue_id. |
| `amux.session_queue_clear` | Drop all pending Session inputs. |
| `amux.session_queue_list` | List pending Session inputs, oldest first. |
| `amux.session_restart` | Restart the Session Agent process in place. |
| `amux.session_resume` | Wake a sleeping Session. |
| `amux.session_run` | Create a Session using an Agent Profile; profile and name are required. Call directly without target first: a sole connected target is selected automatically; multiple targets produce an explicit error before creation. |
| `amux.session_send` | Submit a Prompt to the Session input queue. Use target to disambiguate names. |
| `amux.session_set_model` | Switch the Session model and preserve conversation history. Supports native models and provider-backed refs such as custom/example-model; an unambiguous bare upstream name such as example-model also works. Reconfigure without restarting when hot-switch conditions hold; otherwise restart in place with provider resume/handoff. Switching providers starts a new conversation with handoff history. Sessions owned by Tasks or Instances are unsupported. |
| `amux.session_sleep` | Pause the Session Agent process while keeping it resumable. |
| `amux.session_stop` | Archive a Session. Use task_prune to clean up Task history. |
| `amux.session_stream` | Replay and drain recent Session output once, returning bounded aggregated text. This is not a live tail. |
| `amux.task_cancel` | Cancel an Agent Task and interrupt or terminate its child Session. |
| `amux.task_get` | Get Agent Task status, structured result, and session_ref. |
| `amux.task_list` | List caller-visible Agent Tasks with status, session_ref, and available results. |
| `amux.task_prune` | Clean up terminal Task history. Only the enrollment operator may use it in isolated clusters; delegated agents may use it in non-isolated clusters. |
| `amux.task_respond_permission` | Answer a permission request escalated by an escalate-to-main Task. |
| `amux.task_run` | Run a short Agent Task synchronously. Only policy-allow and deny-all are supported; escalate-to-main would deadlock. |
| `amux.task_stream` | Long-poll Agent Task events from from_seq and return next_seq for lossless continuation. |
| `amux.task_submit` | Submit an Agent Task asynchronously and immediately return task_id. Prefer a profile_list ref for agent; validate development Profiles with profile_get first. |

## `amux.collaborator_list`

List internal Agents or external_bot by kind only when preparing an actual delegation. Use amux.profile_list for Agent capability discovery. external_bot returns only Bots in the Feishu group bound to the current Session; CLI, non-Feishu, or missing group bindings return an empty list.

**Input JSON Schema**

```json
{
  "properties": {
    "kind": {
      "description": "Optional collaborator kind filter; external_bot is required when explicitly looking for an external Bot in the current group",
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

Request cancellation of a Delegation that supports cancelable. cancelled is a stop signal; do not automatically create a replacement delegation.

**Input JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "Delegation ID to request cancellation for",
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

Create a one-off Delegation and return its ID. Use external_bot only when the user explicitly requests or confirms it, and only for Bots in the Feishu group bound to the current Session. Results are external_untrusted. The default creation budget is two attempts; a user-specified count is a hard limit.

**Input JSON Schema**

```json
{
  "properties": {
    "collaborator_id": {
      "description": "Opaque collaborator ID returned by amux.collaborator_list; pass it unchanged",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional retry idempotency key; reuse only for the same logical delegation",
      "type": "string"
    },
    "task": {
      "description": "Complete, independently executable task instructions for the collaborator",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Deadline for the entire Delegation; 0 or omission uses the backend default",
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

Read the current Delegation snapshot. Use delegation_wait for incremental events.

**Input JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "Delegation ID whose current snapshot is requested",
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

Answer a permission request for a Delegation that supports permission_response.

**Input JSON Schema**

```json
{
  "properties": {
    "decision": {
      "description": "Allow or deny the current permission request",
      "enum": [
        "allow",
        "deny"
      ],
      "type": "string"
    },
    "delegation_id": {
      "description": "Delegation ID with the permission request to answer",
      "type": "string"
    },
    "option_id": {
      "description": "Optional option ID provided by the backend permission request",
      "type": "string"
    },
    "reason": {
      "description": "Optional explanation of the decision",
      "type": "string"
    },
    "request_id": {
      "description": "Request ID from the permission_requested event",
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

Wait for a Delegation. event returns incremental events; actionable avoids waking the main model repeatedly for ordinary progress while preserving permission and terminal-state wakeups. wait_timed_out is not a terminal Delegation state. Track actual created attempts using the returned status.

**Input JSON Schema**

```json
{
  "properties": {
    "delegation_id": {
      "description": "Delegation ID to wait for",
      "type": "string"
    },
    "from_seq": {
      "description": "next_seq from the previous response, for incremental event reading",
      "minimum": 0,
      "type": "integer"
    },
    "return_on": {
      "default": "event",
      "description": "In event mode, return on the next event, terminal state, or window timeout. actionable consumes ordinary output/status internally and returns only for permission requests, results, errors, terminal states, or the Delegation deadline",
      "enum": [
        "event",
        "actionable"
      ],
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Underlying bounded long-poll window, default 25 seconds; not the entire Delegation timeout. actionable continues waiting across windows within the MCP call",
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

Deliver a task to a persistent Agent and immediately start a new turn, retaining the provider conversation context.

**Input JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "description": "Persistent Agent Instance to wake and deliver the Task to, from instance_spawn",
      "type": "string"
    },
    "result_granularity": {
      "description": "summary (default) returns only the Agent summary; full inlines the complete redacted transcript in result.full_transcript, truncating oversized output and pointing to amux task_stream or amux task logs; structured-summary returns summary and result.details_ref (task_id) for retrieving the full transcript on demand",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "task": {
      "description": "New Task Prompt that starts the next turn",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Deadline for the entire Task in seconds from acceptance; 0 or omission uses the Runtime default (30 minutes)",
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

Interrupt the current turn of a persistent Agent.

**Input JSON Schema**

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

List persistent Agent Instance status, child Session status, and mailbox depth.

**Input JSON Schema**

```json
{
  "properties": {
    "owner_session_id": {
      "description": "Optional filter: parent Session ID that owns these Instances",
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

Enqueue a message in a persistent Agent mailbox without waking it or starting a new turn.

**Input JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    },
    "source": {
      "description": "Optional source label for the queued input",
      "type": "string"
    },
    "text": {
      "description": "Message to enqueue; after waking, the Agent receives it before the next Task Prompt",
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

Create a persistent Agent Instance without blocking and return a stable agent_instance_id.

**Input JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile ref from amux.profile_list profiles[].ref",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "Optional explicit Instance handle; omission generates and returns a new agent-\u003cuuid\u003e handle",
      "type": "string"
    },
    "credential_profile": {
      "type": "string"
    },
    "extra_args": {
      "description": "Native execution arguments, including permissions; separate from Task permission policy",
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
      "description": "Execution model ref, frozen on the instance; v2 profiles use the runtime default when omitted, while legacy profile constraints still apply",
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
      "description": "summary (default) returns only the Agent summary; full inlines the complete redacted transcript in result.full_transcript, truncating oversized output and pointing to amux task_stream or amux task logs; structured-summary returns summary and result.details_ref (task_id) for retrieving the full transcript on demand",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "Optional Principal alias intent; omission creates the Instance using the parent Execution Principal",
      "type": "string"
    },
    "runtime": {
      "description": "Execution runtime adapter; required for v2 profiles",
      "type": "string"
    },
    "task": {
      "description": "Initial Task Prompt for the Agent",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Deadline for the entire Task in seconds from acceptance; 0 or omission uses the Runtime default (30 minutes)",
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

Wait until the persistent Agent's current turn ends or the wait times out. Retrieve results with task_get/task_stream.

**Input JSON Schema**

```json
{
  "properties": {
    "agent_instance_id": {
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Maximum seconds to wait for the current turn to enter idle or archived; default 30 seconds",
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

Preview, publish, or clean up a managed local expert patch. Publishing requires a later, explicitly confirmed owner turn with the exact same patch and receipt. Evaluation approval does not authorize publication.

**Input JSON Schema**

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
      "description": "Required for preview/publish: base_revision from local_expert_read. Publishing must pass it unchanged; it cannot be omitted",
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
      "description": "Required for preview/publish; ignored by cleanup",
      "enum": [
        "create",
        "fork",
        "update"
      ],
      "type": "string"
    },
    "patch": {
      "description": "Required for preview/publish: unified text Git patch against the exact baseline returned by local_expert_read",
      "type": "string"
    },
    "prepare_evaluation": {
      "default": false,
      "description": "Preview only; default false. Set true only when the user explicitly requests evaluation of this candidate. If the Profile, Skill, or test case may write, notify, or cause other side effects, first present the test plan and obtain subsequent user confirmation, even if the case looks read-only. Prepares a hidden short-lived candidate Profile for one task_run A/B evaluation of the same preview tree. Re-evaluation after a patch change or completed evaluation requires a new explicit request and any necessary side-effect confirmation. Evaluation approval does not authorize publication",
      "type": "boolean"
    },
    "preview_receipt": {
      "description": "Required for publish/cleanup: opaque receipt returned by preview",
      "type": "string"
    },
    "source_ref": {
      "description": "Required only for fork: exact builtin/\u003cname\u003e ref",
      "type": "string"
    },
    "target_ref": {
      "description": "Exact local/\u003cname\u003e target ref",
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

Read the exact editing baseline of a managed local expert. Available only in the current Feishu Bot owner's turn; performs no writes.

**Input JSON Schema**

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
      "description": "Required only for fork: exact builtin/\u003cname\u003e ref",
      "type": "string"
    },
    "target_ref": {
      "description": "Exact local/\u003cname\u003e target ref",
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

Delete one memory document conditionally on revision; this does not erase existing conversation history. When correcting or forgetting a preference, also inspect MEMORY.md and related topics and revise duplicate content and references. Report partial failures clearly.

**Input JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "expected_revision": {
      "description": "SHA-256 revision returned by memory_get; use absent if the document is already missing",
      "type": "string"
    },
    "path": {
      "description": "Relative Markdown path within the current authorized scope",
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

Read a memory document or line range and its revision on demand; read MEMORY.md once in a new conversation and reuse it in the same context. If truncated=true, continue with next_offset and verify revision. Paths are not local files.

**Input JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "end_line": {
      "description": "Last line to read, inclusive; omit to read to the response limit",
      "minimum": 0,
      "type": "integer"
    },
    "offset": {
      "description": "Byte offset returned as next_offset by the previous memory_get; retain the same path and end_line and verify revision when continuing",
      "minimum": 0,
      "type": "integer"
    },
    "path": {
      "description": "Relative Markdown path within the current authorized scope; read MEMORY.md at the start of a new conversation",
      "type": "string"
    },
    "start_line": {
      "description": "First line to read (1-based); omit to start at the beginning; cannot be combined with offset",
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

Search cross-conversation memory on demand when earlier work, decisions or preferences are relevant; returns bounded snippets. incomplete=true means scanning did not finish. Do not search for tasks that need no history.

**Input JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "limit": {
      "description": "Maximum matching documents; defaults to 5, at most 20, still bounded by the response size limit",
      "minimum": 0,
      "type": "integer"
    },
    "path_prefix": {
      "description": "Relative path prefix within the current authorized scope, such as projects/amux/",
      "type": "string"
    },
    "query": {
      "description": "Whitespace-separated literal keywords; matches contiguous Chinese text and case-insensitive English, without regular expressions or semantic search",
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

Save long-term memory conditionally on revision for explicit requests to remember, stable preferences and verified lessons. Put details in topic documents and keep the entry brief. Do not save secrets, temporary authorization or speculation. Re-read and merge conflicts; report saved only after success.

**Input JSON Schema**

```json
{
  "additionalProperties": false,
  "properties": {
    "content": {
      "description": "Complete UTF-8 Markdown content; topic documents are limited to 64 KiB. Keep only brief preferences and pointers in MEMORY.md (at most 2 KiB and 64 lines)",
      "type": "string"
    },
    "expected_revision": {
      "description": "SHA-256 revision returned by memory_get; use absent to create a document, and re-read and merge after a conflict",
      "type": "string"
    },
    "path": {
      "description": "Relative Markdown path within the current authorized scope",
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

Get complete Agent Profile details by exact ref, including development namespaces.

**Input JSON Schema**

```json
{
  "properties": {
    "ref": {
      "description": "Exact Agent Profile ref, optionally including an explicit development namespace",
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

List non-development Agent Profiles with pagination. Submit tasks using profiles[].ref and fetch details with profile_get. Follow next_cursor until has_more=false.

**Input JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "Opaque continuation cursor from the previous profile_list page",
      "type": "string"
    },
    "namespace": {
      "description": "Optional exact Pack namespace filter, such as expert-agents; keep unchanged across pages",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.result_continue`

Continue reading a paginated result. Concatenate content_chunk values in order until complete=true. Cursors are short-lived and valid only in the current MCP process.

**Input JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "next_cursor returned by a paginated MCP result",
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

Create a Hub schedule. Default schedule_type=subtask (ephemeral child Task; use task_get/task_stream for results; escalate-to-main is not allowed). With schedule_type=session-prompt, Submit a prompt into the target Session (explicit session session-ref parameter, or AMUX_SESSION_ID fallback) and wake the Agent.

**Input JSON Schema**

```json
{
  "properties": {
    "name": {
      "description": "Optional Schedule display name",
      "type": "string"
    },
    "prompt": {
      "description": "Prompt delivered into the target Session when schedule_type=session-prompt fires",
      "type": "string"
    },
    "run_mode": {
      "description": "session-prompt only: reuse-session (default) reuses the target Session; new-session-per-run starts a new Session from the template each fire",
      "enum": [
        "reuse-session",
        "new-session-per-run"
      ],
      "type": "string"
    },
    "schedule_type": {
      "description": "subtask (default) creates an ephemeral child Task; session-prompt Submit-s a prompt into the target Session on fire and wakes the Agent",
      "enum": [
        "subtask",
        "session-prompt"
      ],
      "type": "string"
    },
    "session": {
      "description": "Target session-ref for schedule_type=session-prompt (same forms as CLI amux schedule create \u003csession-ref\u003e: target/name, Session ID, or name); omit to fall back to AMUX_SESSION_ID",
      "type": "string"
    },
    "spec": {
      "description": "Time rule: every \u003cduration\u003e, daily \u003cHH:MM\u003e, at \u003cwhen\u003e, or cron \u003cexpr\u003e",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile ref from amux.profile_list profiles[].ref, such as builtin/codex or team/push_sre; raw Agent Type strings are supported only for legacy callers",
          "type": "string"
        },
        "credential_profile": {
          "description": "Control-plane credential profile name",
          "type": "string"
        },
        "extra_args": {
          "description": "Native execution arguments, including permissions; separate from Task permission policy",
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "isolation": {
          "description": "Sandbox isolation mode; Phase 1 is best-effort trusted mode",
          "enum": [
            "process",
            "private-home"
          ],
          "type": "string"
        },
        "model": {
          "description": "Execution model ref; v2 profiles use the runtime default when omitted, while legacy profile constraints still apply",
          "type": "string"
        },
        "permission_policy": {
          "description": "Empty or default uses the Profile or Task default policy. policy-allow and deny-all are allowed; scheduled Agent Tasks do not allow escalate-to-main",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "run_as": {
          "description": "Optional enrolled Principal alias; resolved and frozen when the Schedule is saved",
          "type": "string"
        },
        "runtime": {
          "description": "Execution runtime adapter; required for v2 profiles",
          "type": "string"
        },
        "task": {
          "description": "Task Prompt submitted on each Schedule trigger",
          "type": "string"
        },
        "timeout_seconds": {
          "description": "Deadline for the entire Task in seconds from acceptance; 0 or omission uses the Runtime default (30 minutes)",
          "minimum": 0,
          "type": "integer"
        },
        "trusted_mode_ack": {
          "description": "Legacy compatibility no-op (v0.38): long-lived secret injection is allowed under weak isolation by default; retained only for backward compatibility",
          "type": "boolean"
        },
        "workspace": {
          "properties": {
            "local_dir": {
              "description": "Repo cache directory local to the target Runtime; policy=git-worktree requires local_dir or repo.cache_key",
              "type": "string"
            },
            "policy": {
              "description": "Use temp-dir for a disposable Workspace per run; git-worktree checks out a worktree from the repo cache",
              "enum": [
                "temp-dir",
                "git-worktree"
              ],
              "type": "string"
            },
            "repo": {
              "properties": {
                "cache_key": {
                  "description": "Repo cache key resolved under the Runtime's amux repos directory; policy=git-worktree requires local_dir or repo.cache_key",
                  "type": "string"
                },
                "commit": {
                  "description": "Workspace repository commit or ref",
                  "type": "string"
                },
                "kind": {
                  "description": "Workspace repository kind, such as git",
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
    "spec"
  ],
  "type": "object"
}
```

## `amux.schedule_delete`

Delete a Schedule. Task results from existing runs remain readable.

**Input JSON Schema**

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

Get a Schedule; include_runs=true includes run history and task_id values.

**Input JSON Schema**

```json
{
  "properties": {
    "include_runs": {
      "description": "Whether to include Schedule run history; use task_id from runs with task_get or task_stream to retrieve results",
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

List Schedules with pagination. Follow next_cursor until has_more=false.

**Input JSON Schema**

```json
{
  "properties": {
    "cursor": {
      "description": "Opaque continuation cursor from the previous schedule_list page",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.schedule_set_enabled`

Enable or disable a Schedule.

**Input JSON Schema**

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

Update a Schedule's name, time rule, or Task in place. Omitted fields remain unchanged. escalate-to-main is not allowed.

**Input JSON Schema**

```json
{
  "properties": {
    "name": {
      "description": "New Schedule display name; omitted means unchanged",
      "type": "string"
    },
    "schedule_id": {
      "type": "integer"
    },
    "spec": {
      "description": "New time rule (every \u003cduration\u003e, daily \u003cHH:MM\u003e, at \u003cwhen\u003e, or cron \u003cexpr\u003e); omitted means unchanged. Updates recompute next_run_at",
      "type": "string"
    },
    "task": {
      "properties": {
        "agent": {
          "description": "Agent Profile ref from amux.profile_list profiles[].ref, such as builtin/codex or team/push_sre; raw Agent Type strings are supported only for legacy callers",
          "type": "string"
        },
        "credential_profile": {
          "description": "Control-plane credential profile name",
          "type": "string"
        },
        "extra_args": {
          "description": "Native execution arguments, including permissions; separate from Task permission policy",
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "isolation": {
          "description": "Sandbox isolation mode; Phase 1 is best-effort trusted mode",
          "enum": [
            "process",
            "private-home"
          ],
          "type": "string"
        },
        "model": {
          "description": "Execution model ref; v2 profiles use the runtime default when omitted, while legacy profile constraints still apply",
          "type": "string"
        },
        "permission_policy": {
          "description": "Empty or default uses the Profile or Task default policy. policy-allow and deny-all are allowed; scheduled Agent Tasks do not allow escalate-to-main",
          "enum": [
            "default",
            "policy-allow",
            "deny-all"
          ],
          "type": "string"
        },
        "run_as": {
          "description": "Optional enrolled Principal alias; resolved and frozen when the Schedule is saved",
          "type": "string"
        },
        "runtime": {
          "description": "Execution runtime adapter; required for v2 profiles",
          "type": "string"
        },
        "task": {
          "description": "Task Prompt submitted on each Schedule trigger",
          "type": "string"
        },
        "timeout_seconds": {
          "description": "Deadline for the entire Task in seconds from acceptance; 0 or omission uses the Runtime default (30 minutes)",
          "minimum": 0,
          "type": "integer"
        },
        "trusted_mode_ack": {
          "description": "Legacy compatibility no-op (v0.38): long-lived secret injection is allowed under weak isolation by default; retained only for backward compatibility",
          "type": "boolean"
        },
        "workspace": {
          "properties": {
            "local_dir": {
              "description": "Repo cache directory local to the target Runtime; policy=git-worktree requires local_dir or repo.cache_key",
              "type": "string"
            },
            "policy": {
              "description": "Use temp-dir for a disposable Workspace per run; git-worktree checks out a worktree from the repo cache",
              "enum": [
                "temp-dir",
                "git-worktree"
              ],
              "type": "string"
            },
            "repo": {
              "properties": {
                "cache_key": {
                  "description": "Repo cache key resolved under the Runtime's amux repos directory; policy=git-worktree requires local_dir or repo.cache_key",
                  "type": "string"
                },
                "commit": {
                  "description": "Workspace repository commit or ref",
                  "type": "string"
                },
                "kind": {
                  "description": "Workspace repository kind, such as git",
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

Interrupt the current Session turn.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

List caller-visible Sessions grouped by Runtime. Do not call this before creating a Session: first call session_run without target, and use this tool to choose only if it reports multiple targets.

**Input JSON Schema**

```json
{
  "properties": {},
  "type": "object"
}
```

## `amux.session_prune`

Destructive operation: permanently delete archived Sessions. Only the enrollment operator may use it in isolated clusters; delegated agents may use it in non-isolated clusters. Use task_prune for Task history.

**Input JSON Schema**

```json
{
  "properties": {
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
      "type": "string"
    }
  },
  "type": "object"
}
```

## `amux.session_queue_cancel`

Drop one Session queue input by queue_id.

**Input JSON Schema**

```json
{
  "properties": {
    "queue_id": {
      "description": "Queue input ID to cancel, returned by amux.session_queue_list",
      "type": "string"
    },
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Drop all pending Session inputs.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

List pending Session inputs, oldest first.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Restart the Session Agent process in place.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Wake a sleeping Session.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Create a Session using an Agent Profile; profile and name are required. Call directly without target first: a sole connected target is selected automatically; multiple targets produce an explicit error before creation.

**Input JSON Schema**

```json
{
  "properties": {
    "args": {
      "description": "Native execution arguments, including permission settings",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "cwd": {
      "type": "string"
    },
    "model": {
      "description": "Execution model ref; omitted uses the selected runtime default",
      "type": "string"
    },
    "name": {
      "description": "Session name (required)",
      "type": "string"
    },
    "profile": {
      "description": "Agent Profile ref (such as builtin/codex) or legacy Agent Type",
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
      "description": "Execution runtime adapter; required for v2 profiles",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty selects automatically only when exactly one target is connected",
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

Submit a Prompt to the Session input queue. Use target to disambiguate names.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
      "type": "string"
    },
    "text": {
      "description": "Prompt text to enqueue for the Session",
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

Switch the Session model and preserve conversation history. Supports native models and provider-backed refs such as custom/example-model; an unambiguous bare upstream name such as example-model also works. Reconfigure without restarting when hot-switch conditions hold; otherwise restart in place with provider resume/handoff. Switching providers starts a new conversation with handoff history. Sessions owned by Tasks or Instances are unsupported.

**Input JSON Schema**

```json
{
  "properties": {
    "model": {
      "description": "Target model: native model name or provider-backed ref such as custom/example-model; an unambiguous bare upstream name such as example-model is also supported",
      "type": "string"
    },
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Pause the Session Agent process while keeping it resumable.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Archive a Session. Use task_prune to clean up Task history.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Replay and drain recent Session output once, returning bounded aggregated text. This is not a live tail.

**Input JSON Schema**

```json
{
  "properties": {
    "session": {
      "description": "Session name or ID",
      "type": "string"
    },
    "target": {
      "description": "Target Runtime ID; empty uses the Hub default target",
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

Cancel an Agent Task and interrupt or terminate its child Session.

**Input JSON Schema**

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

Get Agent Task status, structured result, and session_ref.

**Input JSON Schema**

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

List caller-visible Agent Tasks with status, session_ref, and available results.

**Input JSON Schema**

```json
{
  "properties": {},
  "type": "object"
}
```

## `amux.task_prune`

Clean up terminal Task history. Only the enrollment operator may use it in isolated clusters; delegated agents may use it in non-isolated clusters.

**Input JSON Schema**

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

Answer a permission request escalated by an escalate-to-main Task.

**Input JSON Schema**

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

Run a short Agent Task synchronously. Only policy-allow and deny-all are supported; escalate-to-main would deadlock.

**Input JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile ref from amux.profile_list profiles[].ref; an exact development Profile ref validated with amux.profile_get is also accepted, such as builtin/codex, team/push_sre, or team-task-dev/push_sre. Raw Agent Type strings are supported only for legacy callers",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "Persistent Agent Instance handle (mode=persistent); reuse the instance_spawn value for instance_followup, instance_send, or instance_interrupt",
      "type": "string"
    },
    "credential_profile": {
      "description": "Control-plane credential profile name",
      "type": "string"
    },
    "extra_args": {
      "description": "Native execution arguments, including permissions; separate from Task permission policy",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "isolation": {
      "description": "Sandbox isolation mode; Phase 1 is best-effort trusted mode",
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "mode": {
      "description": "Ephemeral (default) runs once and is destroyed; persistent keeps the Agent Instance alive across Tasks for multiple turns",
      "enum": [
        "ephemeral",
        "persistent"
      ],
      "type": "string"
    },
    "model": {
      "description": "Execution model ref; v2 profiles use the runtime default when omitted, while legacy profile constraints still apply",
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
      "description": "summary (default) returns only the Agent summary; full inlines the complete redacted transcript in result.full_transcript up to 64KB, with truncation pointing to amux task_stream or amux task logs; structured-summary returns summary and result.details_ref (task_id) for fetching the full transcript on demand",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "Optional Principal alias intent, resolved and authorized by amuxd; omission makes the child inherit the parent's Execution Principal",
      "type": "string"
    },
    "runtime": {
      "description": "Execution runtime adapter; required for v2 profiles",
      "type": "string"
    },
    "task": {
      "description": "Prompt for the Agent Task",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Deadline in seconds for the entire Task, measured from acceptance and including preparation and Agent startup; 0 or omission uses the Runtime default (30 minutes)",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "description": "Legacy compatibility no-op (v0.38): long-lived secret injection is allowed under weak isolation by default; retained only for backward compatibility",
      "type": "boolean"
    },
    "workspace": {
      "properties": {
        "local_dir": {
          "description": "Repo cache directory local to the target Runtime; policy=git-worktree requires local_dir or repo.cache_key",
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
              "description": "Repo cache key resolved under the Runtime's amux repos directory; policy=git-worktree requires local_dir or repo.cache_key",
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

Long-poll Agent Task events from from_seq and return next_seq for lossless continuation.

**Input JSON Schema**

```json
{
  "properties": {
    "from_seq": {
      "description": "Resume reading from this event sequence number; pass next_seq from the previous call",
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

Submit an Agent Task asynchronously and immediately return task_id. Prefer a profile_list ref for agent; validate development Profiles with profile_get first.

**Input JSON Schema**

```json
{
  "properties": {
    "agent": {
      "description": "Agent Profile ref from amux.profile_list profiles[].ref; an exact development Profile ref validated with amux.profile_get is also accepted, such as builtin/codex, team/push_sre, or team-task-dev/push_sre. Raw Agent Type strings are supported only for legacy callers",
      "type": "string"
    },
    "agent_instance_id": {
      "description": "Persistent Agent Instance handle (mode=persistent); reuse the instance_spawn value for instance_followup, instance_send, or instance_interrupt",
      "type": "string"
    },
    "credential_profile": {
      "description": "Control-plane credential profile name",
      "type": "string"
    },
    "extra_args": {
      "description": "Native execution arguments, including permissions; separate from Task permission policy",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "isolation": {
      "description": "Sandbox isolation mode; Phase 1 is best-effort trusted mode",
      "enum": [
        "process",
        "private-home"
      ],
      "type": "string"
    },
    "mode": {
      "description": "Ephemeral (default) runs once and is destroyed; persistent keeps the Agent Instance alive across Tasks for multiple turns",
      "enum": [
        "ephemeral",
        "persistent"
      ],
      "type": "string"
    },
    "model": {
      "description": "Execution model ref; v2 profiles use the runtime default when omitted, while legacy profile constraints still apply",
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
      "description": "summary (default) returns only the Agent summary; full inlines the complete redacted transcript in result.full_transcript up to 64KB, with truncation pointing to amux task_stream or amux task logs; structured-summary returns summary and result.details_ref (task_id) for fetching the full transcript on demand",
      "enum": [
        "summary",
        "full",
        "structured-summary"
      ],
      "type": "string"
    },
    "run_as": {
      "description": "Optional Principal alias intent, resolved and authorized by amuxd; omission makes the child inherit the parent's Execution Principal",
      "type": "string"
    },
    "runtime": {
      "description": "Execution runtime adapter; required for v2 profiles",
      "type": "string"
    },
    "task": {
      "description": "Prompt for the Agent Task",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Deadline in seconds for the entire Task, measured from acceptance and including preparation and Agent startup; 0 or omission uses the Runtime default (30 minutes)",
      "minimum": 0,
      "type": "integer"
    },
    "trusted_mode_ack": {
      "description": "Legacy compatibility no-op (v0.38): long-lived secret injection is allowed under weak isolation by default; retained only for backward compatibility",
      "type": "boolean"
    },
    "workspace": {
      "properties": {
        "local_dir": {
          "description": "Repo cache directory local to the target Runtime; policy=git-worktree requires local_dir or repo.cache_key",
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
              "description": "Repo cache key resolved under the Runtime's amux repos directory; policy=git-worktree requires local_dir or repo.cache_key",
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
