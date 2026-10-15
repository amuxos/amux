# Work with your assistant in Feishu

[English](feishu-conversations.md) | [简体中文](feishu-conversations.zh-CN.md)

This guide covers a first request, supporting material, follow-ups, progress,
permissions, and starting over. You need a connected bot and permission to use
it. Operators can start with [bot setup](../tutorials/first-feishu-bot.md).

## Identify the current surface

`/amux` opens different views depending on where you send it. In a group, select
the actual bot from Feishu's mention picker and address the command to it.
Typing text that merely looks like `@name` may not create a platform mention.

| Location | What `/amux` provides | Next step |
| --- | --- | --- |
| Private chat with the bot | Assistant status, settings and system maintenance, subject to identity checks | Inspect the current expert and message handling; owners can manage settings |
| A new root message in a group | Group access, message handling and watch status | Check access; owners should also inspect the group management card sent privately |
| A new, unbound topic | Instructions for starting work | Mention the bot with a request, or use available `/run` and `/bind` actions |
| An active bound topic | Current conversation status and controls | Continue, inspect or interrupt that work |

`/help` lists commands for the current surface. A listed command still depends
on Agent capability and the caller's permissions. Ordinary users do not need a terminal.

## Start work and follow up

This example uses independent topics in a group. Continuous-conversation scopes
are explained below.

1. Mention the bot with a goal, material, expected deliverable and allowed actions.
2. Wait for its reply card, check what it is doing, and handle any permission request.
3. Reply inside the same topic and mention the bot to continue the work.
4. Verify the deliverable and completion status. Use a new root message for a
   separate request that needs independent context.

For example:

> @assistant Review this proposal. Identify three implementation risks and cite
> the relevant paragraphs. Reply here with your review; do not edit the document yet.

Then reply in that topic:

> @assistant Expand the second risk into reproduction conditions, affected scope
> and two possible fixes, using the same material.

In a shared topic, ordinary discussion without mentioning the bot is recorded
as bounded context and does not itself trigger a response. Mention the bot when
you want it to act on that discussion; silence on every human message is expected.

### What happens if you follow up during a response

By default a plain-text follow-up you send during your own request's execution
is **steered into the running execution** instead of interrupting it: closely
spaced inputs are merged in a window of about two seconds and handed to the
working Agent as a message the user sent mid-execution. The Agent decides whether it corrects the current
work or appends a new task, and keeps completed progress. Claude reads it at the
boundary after the current tool call; a follow-up sent while a text-only answer
streams is handled right after that answer, still in the same execution. Codex
uses its native `turn/steer`.

Only the person who sent the running request can steer it. Messages from other
group members, group-watch alerts and messages from other Bots are queued and run
as the next execution, without disturbing the current one. A follow-up is also
queued when the running execution cannot accept it: it just finished, is being
interrupted, earlier messages are already waiting, the Agent does not support
steering, or the sender's authorization differs from the running execution.
Images and attachments use the material path, so do not rely on the same
text-batching window for them.

If amux cannot confirm that the running execution received a follow-up (for
example a timeout), it does not queue another copy, because the follow-up may
already be running. Check the reply and resend the message only if it was not
handled.

Send `/interrupt` to stop right away; in topic mode `/stop` ends the task.
Interrupting also cancels the follow-ups already steered into that execution;
send them again if you still want them. Interruption does not undo file changes
or external tool operations that already happened.
Operators can set `AMUX_FEISHU_BUSY_INPUT=interrupt` to restore the previous
interrupt-on-follow-up behavior.

In singleton mode the foreground session converses and dispatches: long work is
delegated to background Agent Instances, and the foreground Agent relays
corrections to the right instance with `amux.instance_steer`.

## Know which conversation receives the message

| Message handling | Reuse scope | Starting independent context |
| --- | --- | --- |
| Bot continuous conversation (`singleton`) | One bound Session for the bot, potentially spanning permitted private chats and groups | The owner uses conversation management in private `/amux` |
| Independent topics (`thread`) | One bound Session per active topic | Start a new root message; a reply in an existing topic continues that conversation |
| Independent group continuous conversation | One Session for this bot in this group, separate from other groups and private chat | The owner selects the new continuous conversation action in this group's settings |
| Independent topics with user isolation | Topic routing plus the installation's Linux user isolation | Start a new topic; identity and access prerequisites still apply |

The same expert name does not identify the same conversation. Separate topics
also do not imply separate filesystems: they may use the same execution host and
configured workspace. Before adding a bot to multiple groups, check whether its
continuous conversation shares context. Use [group settings](feishu-groups.md)
when each group needs its own conversation.

A group continuous conversation posts directly to the group. After it is retired,
a reply to its old source receives a start-again notice instead of silently going
to the new conversation. Previously accepted work can still finish.

## Supply materials and quotations

Send text, images, files or merged forwards, and explain what to extract from
them. A quoted reply can identify a particular message. Shared topic context
selects bounded relevant records; it is not unrestricted access to chat history.

- Material retrieval uses the bot application's access. Your ability to open a
  file does not prove the bot can read it.
- Image understanding also depends on the Session and Agent. Check material
  notices for access failures, limits or unsupported capabilities; try a smaller
  file, a text excerpt, or ask the operator to check access.
- Merged forwards expand only descendants returned by Feishu; they do not grant
  arbitrary access to attachments from other chats.
- Editing a previously sent message does not automatically resubmit it to the
  Agent. Send an important correction as a new reply and mention the bot.

Returned files have separate limits from inbound materials. Agent artifacts are
limited to ten files per turn, 30 MB per file and 300 MB total. Recognized images
up to 10 MB use image presentation; larger images within the permitted limit
are sent as files. A claim that a file was generated does not prove upload
succeeded: check the actual attachment.

## Read progress and handle requests

The main card updates progress and results. Its expandable execution summary
contains expert, model, timing and Provider-reported usage when available. amux
does not invent missing metrics. Cumulative tokens may include earlier turns
in the native conversation; they are neither this request's isolated bill nor
the sum of the parent and all children.

Work delegated through amux gets separate child progress cards. Each card tracks
its own execution; the main Agent may still be summarizing after a child finishes.
See [delegation](delegation.md) for how to read these records.

When a permission or question card appears:

1. Check its conversation and requested operation or question, not just its allow button.
2. Choose from the supplied options; the response applies to that exact request.
3. Confirm the card is marked handled, then watch execution continue. Expired,
   repeated or unauthorized responses are rejected.

The owner and request-associated user respond subject to authorization; group
membership alone is insufficient. Some operations are already allowed by policy
and do not produce a card. Group access in `/amux` is separate from tool and file
access. See [access and approvals](feishu-groups.md).

## Interrupt, start fresh or end work

| Action | Implemented effect | Boundary |
| --- | --- | --- |
| `/status` | Show the currently bound Session's status | Does not start another analysis |
| `/interrupt` | Request interruption of the active Agent execution; keep the conversation | Does not roll back effects or mean all child Tasks are cancelled |
| `/restart` | Restart this Session using available history according to Agent capability | Neither an amux system restart nor a context reset |
| `/compact` | Queue context compaction and retain a summary | Currently requires a Codex-compatible Session; runs after preceding inputs |
| `/clear` | Queue fresh native context while preserving the Session binding | Also requires Codex-compatible support; keeps workspace files and Hub memory |
| `/stop` | End the topic Session and binding | Bot singleton mode rejects this topic lifecycle action and directs you to management |

Use interruption to pause a response. Use a supported clear action or a new
conversation for fresh context. To remove cross-conversation memory, explicitly
[correct or forget memory](memory.md).

### Use an already loaded Skill

The `/skill` picker currently supports Codex-compatible Sessions. Establish the
conversation first, send `/skill`, then select an actually loaded skill and give
it a task. `/skill <name> <task>` is also supported; use a name from the current
Session's available list. This does not install a missing Skill or switch experts.

## Verify a complete conversation

Check that the response appeared in the intended topic/group, follow-ups used the
right material, required permissions were handled, and the result or attachment
is inspectable. An existing Session keeping its old expert after a default change
is expected: defaults apply to new launches.

| Symptom | First check |
| --- | --- |
| No group response | Real bot mention, access permission, or unmentioned shared discussion |
| An old topic asks you to start again | Ended topic or retired group continuous conversation |
| A response stops or several follow-ups are combined | Plain-text revisions sent during execution |
| A quote or attachment is unavailable | Bot access, material notices, Agent capability and size limits |
| `/clear` or `/skill` is unavailable | Compatible Agent and an active binding |
| Execution keeps waiting for authorization | Outstanding cards; document-comment requests also use private chat |

Continue with [troubleshooting](../operations/troubleshooting.md). Formal behavior
and commands are in the [Session model](../current/session-product-model.md),
[Bridge model](../current/feishu-bridge-product-model.md) and
[Feishu command reference](../reference/feishu-commands.md).
