# Your first Session

[English](cli-sessions.md) | [简体中文](cli-sessions.zh-CN.md)

Start an Agent, send it a small request, and reconnect to the same conversation.
Complete [installation](../tutorials/installation.md) first. This tutorial uses `builtin/codex`;
substitute your installed Agent's built-in Profile if necessary.

## 1. Start a named conversation

```sh
amux session run builtin/codex --name hello-amux
```

The command prints `Session started` and returns. The Agent runs on the Runtime;
closing this terminal does not stop it. If the name already exists, attach to it
or choose a different name instead of creating it again.

## 2. Send a request and watch the answer

```sh
amux session send hello-amux "Reply with one sentence explaining what you can help with."
amux session attach hello-amux
```

You should see the accepted input and the Agent's response in the timeline. The
send command submits work; it does not wait for the response. This step uses the
native Agent's model account. If a permission request appears, resolve it in the
terminal UI. Authentication or launch failures are covered in
[troubleshooting](../operations/troubleshooting.md).

## 3. Detach and return

Type `/detach` or press Ctrl-] in the attached UI, then run:

```sh
amux session list
amux session attach hello-amux
```

Detaching closes your viewer while keeping the conversation. You can submit the
next message from the attached UI or with `session send`.

## 4. Stop the process while retaining the conversation

After the response has finished, detach and run:

```sh
amux session sleep hello-amux
amux session list
```

The Session should be sleeping. Resume when needed with
`amux session resume hello-amux`, then attach again. Resuming uses the Agent's
supported history mechanism; it is different from starting a new conversation.

Next: [choose between Sessions and Tasks](../concepts/execution.md).
