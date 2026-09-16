# Chat Template Agent 渲染示例

[English](chat-template-agent-examples.md) | [简体中文](chat-template-agent-examples.zh-CN.md)

> 通用模板机制示例；文中的 `chat_template.jinja` 是示例模板名称，不是本仓库提供的运行时文件。

本文档用连续对话的 agent 场景说明 `chat_template.jinja` 的作用：
它把结构化的 `messages`、`tools`、tool results 等输入，渲染成模型真正接收的一整段
prompt。外层 agent runtime 再解析模型输出，执行工具调用，并把最终可见文本返回给用户。

可以把一次对话理解成三层：

- 原始 `messages`：API 或 agent runtime 传进模板的数据。
- 模型接收的数据：`chat_template.jinja` 渲染后的完整 prompt。
- 用户收到的数据：runtime 从模型输出里过滤掉 `<think>`、`<tool_call>` 等内部内容后展示的文本。

下面的例子假设：

```text
enable_thinking = true
add_generation_prompt = true
reasoning_effort 未传
```

因此模板会默认渲染：

```text
<|system|>Reasoning Effort: Max
```

## 例子 1：普通第一轮

输入 `messages`：

```json
[
  {
    "role": "user",
    "content": "帮我写一个 Linux 命令，查看当前目录下最大的 5 个文件"
  }
]
```

模型实际接收：

```text
[gMASK]<sop><|system|>Reasoning Effort: Max<|user|>帮我写一个 Linux 命令，查看当前目录下最大的 5 个文件<|assistant|><think>
```

模型可能输出：

````text
我会用 du 排序并限制前 5 项。</think>
可以用：

```bash
du -ah . | sort -rh | head -n 5
```

如果只想看文件、不包含目录，可以用：

```bash
find . -type f -exec du -h {} + | sort -rh | head -n 5
```
````

用户实际收到：

````text
可以用：

```bash
du -ah . | sort -rh | head -n 5
```

如果只想看文件、不包含目录，可以用：

```bash
find . -type f -exec du -h {} + | sort -rh | head -n 5
```
````

`<think>...</think>` 是内部思考区，正常产品里不应该展示给用户。

## 例子 2：连续第二轮，无工具

第二轮用户追问：

```text
能不能按修改时间过滤，只看最近 7 天？
```

输入 `messages` 可能是：

```json
[
  {
    "role": "user",
    "content": "帮我写一个 Linux 命令，查看当前目录下最大的 5 个文件"
  },
  {
    "role": "assistant",
    "content": "可以用：\n\n```bash\ndu -ah . | sort -rh | head -n 5\n```"
  },
  {
    "role": "user",
    "content": "能不能按修改时间过滤，只看最近 7 天？"
  }
]
```

模型接收：

````text
[gMASK]<sop><|system|>Reasoning Effort: Max
<|user|>帮我写一个 Linux 命令，查看当前目录下最大的 5 个文件
<|assistant|><think></think>
可以用：

```bash
du -ah . | sort -rh | head -n 5
```
<|user|>能不能按修改时间过滤，只看最近 7 天？
<|assistant|><think>
````

这里历史 assistant 消息被补了：

```text
<think></think>
```

这是模板在清理历史 thinking，避免把旧的思考内容反复喂给模型。

用户收到：

````text
可以：

```bash
find . -type f -mtime -7 -exec du -h {} + | sort -rh | head -n 5
```

`-mtime -7` 表示最近 7 天内修改过的文件。
````

## 例子 3：带工具调用的 agent 场景

假设有一个工具：

```json
{
  "name": "run_shell",
  "description": "Run a shell command",
  "parameters": {
    "type": "object",
    "properties": {
      "cmd": { "type": "string" }
    },
    "required": ["cmd"]
  }
}
```

用户问：

```text
帮我看下当前目录最大的 5 个文件
```

模型接收：

```text
[gMASK]<sop><|system|>Reasoning Effort: Max<|system|>
# Tools

You may call one or more functions to assist with the user query.

You are provided with function signatures within <tools></tools> XML tags:
<tools>
{"name": "run_shell", "description": "Run a shell command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}, "required": ["cmd"]}}
</tools>

For each function call, output the function name and arguments within the following XML format:
<tool_call>{function-name}<arg_key>{arg-key-1}</arg_key><arg_value>{arg-value-1}</arg_value>...</tool_call>
<|user|>帮我看下当前目录最大的 5 个文件
<|assistant|><think>
```

模型可能先输出工具调用：

```xml
需要执行 du 命令查看结果。</think>
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>du -ah . | sort -rh | head -n 5</arg_value></tool_call>
```

用户这时通常不会收到最终回答。agent runtime 会拦截 `<tool_call>`，执行工具。

工具返回后，`messages` 会追加一条 `tool`：

```json
{
  "role": "tool",
  "content": "120M\t./node_modules\n45M\t./dist\n12M\t./logs/app.log\n8M\t./assets\n4M\t./README.md"
}
```

下一次模型接收：

````text
...
<|assistant|><think></think>
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>du -ah . | sort -rh | head -n 5</arg_value></tool_call>
<|observation|><tool_response>120M	./node_modules
45M	./dist
12M	./logs/app.log
8M	./assets
4M	./README.md</tool_response>
<|assistant|><think>
````

模型最终输出：

````text
结果里最大的 5 项是：

1. `./node_modules`：120M
2. `./dist`：45M
3. `./logs/app.log`：12M
4. `./assets`：8M
5. `./README.md`：4M</think>

当前目录最大的 5 项是：

```text
120M  ./node_modules
45M   ./dist
12M   ./logs/app.log
8M    ./assets
4M    ./README.md
```
````

用户最终收到：

````text
当前目录最大的 5 项是：

```text
120M  ./node_modules
45M   ./dist
12M   ./logs/app.log
8M    ./assets
4M    ./README.md
```
````

## 例子 4：多轮工具调用后的连续对话

用户继续问：

```text
那帮我删掉 dist
```

模型接收的历史里会包括：

- 用户第一次请求
- assistant 的工具调用
- tool 的执行结果
- assistant 的最终回答
- 用户的新请求

大概是：

````text
[gMASK]<sop><|system|>Reasoning Effort: Max
<|system|>
# Tools
...
<|user|>帮我看下当前目录最大的 5 个文件
<|assistant|><think></think>
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>du -ah . | sort -rh | head -n 5</arg_value></tool_call>
<|observation|><tool_response>120M	./node_modules
45M	./dist
...</tool_response>
<|assistant|><think></think>
当前目录最大的 5 项是：

```text
120M  ./node_modules
45M   ./dist
...
```
<|user|>那帮我删掉 dist
<|assistant|><think>
````

如果 agent 有权限直接执行，它可能调用：

```xml
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>rm -rf dist</arg_value></tool_call>
```

更安全的 agent runtime 可能会在这里触发权限确认。用户收到的不是模型文本，
而是 UI 层的确认请求，例如：

```text
请求执行命令：rm -rf dist
是否允许？
```

这部分不是模板决定的，而是 agent runtime 的权限系统决定的。

## 关键点

这份模板决定的是模型看见什么，以及模型应该怎么输出工具调用。用户最终看到什么，
取决于外层 runtime 怎么过滤和解释模型输出。

通常应该过滤掉：

```text
<think>...</think>
<tool_call>...</tool_call>
<tool_response>...</tool_response>
```

只把最终自然语言内容展示给用户。
