<a id="chat-template-agent-渲染示例"></a>

# Chat Template Rendering Examples for Agents

[English](chat-template-agent-examples.md) | [简体中文](chat-template-agent-examples.zh-CN.md)

> These examples illustrate a general template mechanism. `chat_template.jinja` is an example template name, not a runtime file shipped by this repository.

This document uses a continuing agent conversation to explain `chat_template.jinja`.
It renders structured `messages`, `tools`, tool results, and related input into the
complete prompt received by the model. The surrounding agent runtime parses the
model output, executes tool calls, and returns the final visible text to the user.

A conversation has three layers:

- Original `messages`: data passed to the template by the API or agent runtime.
- Model input: the complete prompt rendered by `chat_template.jinja`.
- User output: text displayed after the runtime filters internal content such as `<think>` and `<tool_call>` from model output.

The examples assume:

```text
enable_thinking = true
add_generation_prompt = true
reasoning_effort is not provided
```

The template therefore renders this default:

```text
<|system|>Reasoning Effort: Max
```

<a id="例子-1普通第一轮"></a>

## Example 1: An ordinary first turn

Input `messages`:

```json
[
  {
    "role": "user",
    "content": "Write a Linux command to find the five largest files in the current directory"
  }
]
```

The model receives:

```text
[gMASK]<sop><|system|>Reasoning Effort: Max<|user|>Write a Linux command to find the five largest files in the current directory<|assistant|><think>
```

The model might output:

````text
I will use du, sort the results, and limit them to five entries.</think>
Use:

```bash
du -ah . | sort -rh | head -n 5
```

To include files only and exclude directories, use:

```bash
find . -type f -exec du -h {} + | sort -rh | head -n 5
```
````

The user receives:

````text
Use:

```bash
du -ah . | sort -rh | head -n 5
```

To include files only and exclude directories, use:

```bash
find . -type f -exec du -h {} + | sort -rh | head -n 5
```
````

`<think>...</think>` contains internal reasoning and should not be displayed to users in a normal product.

<a id="例子-2连续第二轮无工具"></a>

## Example 2: A second turn without tools

The user follows up in the second turn:

```text
Can you filter by modification time to include only the last seven days?
```

Input `messages` might be:

```json
[
  {
    "role": "user",
    "content": "Write a Linux command to find the five largest files in the current directory"
  },
  {
    "role": "assistant",
    "content": "Use:\n\n```bash\ndu -ah . | sort -rh | head -n 5\n```"
  },
  {
    "role": "user",
    "content": "Can you filter by modification time to include only the last seven days?"
  }
]
```

The model receives:

````text
[gMASK]<sop><|system|>Reasoning Effort: Max
<|user|>Write a Linux command to find the five largest files in the current directory
<|assistant|><think></think>
Use:

```bash
du -ah . | sort -rh | head -n 5
```
<|user|>Can you filter by modification time to include only the last seven days?
<|assistant|><think>
````

The historical assistant message now includes:

```text
<think></think>
```

The template clears historical reasoning so that old reasoning is not repeatedly fed back to the model.

The user receives:

````text
Yes:

```bash
find . -type f -mtime -7 -exec du -h {} + | sort -rh | head -n 5
```

`-mtime -7` selects files modified within the last seven days.
````

<a id="例子-3带工具调用的-agent-场景"></a>

## Example 3: An agent using a tool

Suppose this tool is available:

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

The user asks:

```text
Show me the five largest files in the current directory
```

The model receives:

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
<|user|>Show me the five largest files in the current directory
<|assistant|><think>
```

The model might first output a tool call:

```xml
I need to run du to inspect the results.</think>
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>du -ah . | sort -rh | head -n 5</arg_value></tool_call>
```

The user usually does not receive a final answer yet. The agent runtime intercepts `<tool_call>` and executes the tool.

After the tool returns, a `tool` message is appended to `messages`:

```json
{
  "role": "tool",
  "content": "120M\t./node_modules\n45M\t./dist\n12M\t./logs/app.log\n8M\t./assets\n4M\t./README.md"
}
```

On the next request, the model receives:

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

The model finally outputs:

````text
The five largest entries in the results are:

1. `./node_modules`: 120M
2. `./dist`: 45M
3. `./logs/app.log`: 12M
4. `./assets`: 8M
5. `./README.md`: 4M</think>

The five largest entries in the current directory are:

```text
120M  ./node_modules
45M   ./dist
12M   ./logs/app.log
8M    ./assets
4M    ./README.md
```
````

The user finally receives:

````text
The five largest entries in the current directory are:

```text
120M  ./node_modules
45M   ./dist
12M   ./logs/app.log
8M    ./assets
4M    ./README.md
```
````

<a id="例子-4多轮工具调用后的连续对话"></a>

## Example 4: Continuing a conversation after tool use

The user continues:

```text
Then delete dist for me
```

The conversation history received by the model includes:

- The initial user request
- The assistant tool call
- The tool result
- The final assistant answer
- The new user request

For example:

````text
[gMASK]<sop><|system|>Reasoning Effort: Max
<|system|>
# Tools
...
<|user|>Show me the five largest files in the current directory
<|assistant|><think></think>
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>du -ah . | sort -rh | head -n 5</arg_value></tool_call>
<|observation|><tool_response>120M	./node_modules
45M	./dist
...</tool_response>
<|assistant|><think></think>
The five largest entries in the current directory are:

```text
120M  ./node_modules
45M   ./dist
...
```
<|user|>Then delete dist for me
<|assistant|><think>
````

If the agent has permission to execute directly, it might call:

```xml
<tool_call>run_shell<arg_key>cmd</arg_key><arg_value>rm -rf dist</arg_value></tool_call>
```

An agent runtime with a permission check may request confirmation here.
The user receives a UI confirmation request instead of model text, for example:

```text
Request to execute command: rm -rf dist
Allow this command?
```

This is controlled by the agent runtime permission system rather than the template.

<a id="关键点"></a>

## Key points

The template determines what the model sees and how it should format tool calls.
What the user ultimately sees depends on how the surrounding runtime filters and
interprets model output.

Normally, the runtime should filter out:

```text
<think>...</think>
<tool_call>...</tool_call>
<tool_response>...</tool_response>
```

Only the final natural-language content should be shown to the user.
