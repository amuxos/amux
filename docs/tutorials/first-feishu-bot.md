# Connect your first Feishu bot

[English](first-feishu-bot.md) | [简体中文](first-feishu-bot.zh-CN.md)

By the end, a message to your bot will reach an Agent running on your machine and
its answer will return to Feishu. If your operator already provided a working
bot, go straight to [working in Feishu](../guides/feishu-conversations.md).
Operators need an installed, authenticated native Agent (Codex, Claude or Pi;
Claude also needs Node.js 22+), an interactive terminal and a Feishu account
that can complete the official app registration flow; organization policy may
require an administrator's approval.

## 1. Configure a bot and its conversation mode

For a first bot, use a persistent singleton conversation. Download the public installer:

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex --feishu --interaction-mode singleton   --feishu-profile assistant --session-name feishu-assistant
```

Choose the Agent installed on this host. This command also installs or refreshes
amux; it preserves existing data but can interrupt active work. Finish current
work first. Follow the official QR registration steps in the terminal, including
any app permissions or publishing steps it requests. Do not proceed past an
incomplete setup.

`assistant` names the **Bot Profile**; `builtin/codex` is the **Agent Profile**.
The installer records bot credentials, creates or reuses a compatible named
Session and binds it. It does not send a test chat message.

## 2. Check the system and open bot management

```sh
amux system status
amux session list
```

Confirm the system is healthy and `feishu-assistant` exists. Open the bot's private
chat in Feishu and send `/amux` to view management controls. Confirm the selected
Agent and interaction mode. Owner controls are available only to the configured
owner; do not use a group chat for owner-only system operations.

## 3. Send one small request

Send “Reply with one sentence explaining what you can help with.” in that private
chat. You should see progress followed by a final answer. If no card appears,
check bot setup and connection; if a card appears but Agent execution fails,
check the Agent on the Runtime. See [troubleshooting](../operations/troubleshooting.md).

## 4. Choose how future group work should behave

A singleton continues one bound conversation. Thread mode creates separate
conversations for topics. For a new bot intended for topic-based work, select
`--interaction-mode thread` in the installer instead. Changing modes does not
turn prior conversations into isolated users' workspaces.

Before adding colleagues, read [group settings and access](../guides/feishu-groups.md).
Who may use a bot, which conversation receives a message, and Linux user isolation
are three separate choices. See the [Bridge model](../current/feishu-bridge-product-model.md)
for the exact routing and permission rules.
