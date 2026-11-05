# Capability plaza and daily recommendations

[English](capability-plaza.md) | [简体中文](capability-plaza.zh-CN.md)

The capability plaza organizes existing amux workflows into 4 categories and 16
capabilities, each with two editable examples. This guide includes all 32 prompts
and their prerequisites. You need a connected Feishu bot. Getting started, the
plaza and recommendation preferences are for that bot's owner in its private chat.
If the bot is not connected yet, complete the
[first-bot tutorial](../tutorials/first-feishu-bot.md).

## Start with the guided setup

Send `/amux` in private chat and choose “Getting started”. The first confirmed
owner is also offered a welcome card.

1. Choose continuous conversation or independent topics. The former continues one
   conversation; the latter keeps separate chat histories for different work.
   Separate histories can still share workspace files; file and execution
   isolation require separate configuration.
2. Choose an available engine, model and reasoning effort. Reasoning effort is
   always expanded and controls how much reasoning the model does before answering.
   An unset preference initially uses the strongest level actually supported by
   the model; explicit saved choices are preserved. Stronger reasoning can take longer.
3. Preview an example task, edit its dates, links or materials, then send it.
   Calendar and similar work requests authorization through the required Skill
   during execution; the guide has no separate “connect calendar” step.
4. Under “Keep exploring”, select an existing expert, add a Git expert library,
   or create an expert by specifying its name, responsibility, Git URL and Skill
   requirements. One library can contain several experts. An authoring request
   still needs a change preview, publication confirmation and normal merge/sync.

Reenter from `/amux` at any time. Saved settings, expert libraries and the guide
draft are retained. A sent request is a submission; inspect actual results in
the task conversation in the same private chat. See the
[expert and Skill concepts](../concepts/agents.md).

## Choose a workflow in the plaza

1. Send `/amux` in the owner's private chat and choose “Capability plaza”.
2. Browse categories, with up to 5 entries per page, or choose “Show another capability”.
3. Open the examples and read the description and prerequisites. Where available,
   open the corresponding management page.
4. Choose example 1 or 2 and replace bracketed links, names and other placeholders.
5. Confirm with “Send to assistant”, then follow replies and authorization requests
   in the same private chat.

Browsing, switching examples, editing drafts and opening settings do not execute
an example. Submission uses ordinary conversation routing and authorization;
repeating the same submission does not execute it twice. Subpages return to their
parent or management overview. This flow does not overwrite first-run guide progress.
Examples are natural-language requests whose execution depends on the current
Agent, tools and authorization. A card does not install a Skill, connect an
external account or guarantee every engine supports a capability.


The assistant receives the selected capability's prerequisites and a small Hub
state snapshot. Saved preferences and running values are separate; unknown
health, model availability and authorization remain unknown. If the engine
does not expose registered amux tools, `amux mcp call <tool> --describe` shows
the real schema and `amux mcp call <tool>` accepts JSON arguments on stdin.
This uses the same actor, permissions and Hub as MCP; engine-native Cron or
Goal tools do not substitute for amux schedules or Feishu goals. Query receipts
and actual results establish success. Bracketed placeholders require user input.

## How daily recommendations work

- Newly configured bots enable recommendations by default, starting the next day
  at 11:00 in the **Hub host's timezone**. Online systems send around that time;
  a system returning online later that day can still send today's card.
- Upgrading an existing installation does not opt it in. The owner can enable or
  disable recommendations in the plaza, or disable them from a daily card.
  Reenabling starts the next day. Disabling leaves manual browsing and existing tasks available.
- Each bot recommends one capability privately to its current owner, never a group.
  Selection does not repeat until the full catalog round is exhausted. Manual
  browsing and random previews do not count as daily deliveries.
- Preferences and delivery records survive restarts. Each local date gets at most
  one attempt. Offline dates are not backfilled; failed or uncertain sends are not
  automatically retried that day. You can always open the plaza manually.

Daily recommendations describe workflows; they do not create schedules or start
other Agent work automatically.

## All capabilities and examples

These descriptions and prompts match the bilingual card catalog. External
integrations require installed Skills and account access.

### Automation and tasks

<a id="schedules"></a>

#### Run work on a schedule

Create schedules in natural language, inspect results and cancel them. Creation receipts and actual run results are separate.

**Prerequisites:** An online Runtime is required. External data still requires the relevant Skill and authorization.

**Example 1**

> Create a scheduled task that tells me a joke in this conversation every minute. Return the saved task ID and next run time. After the first actual joke, I will say “Cancel this scheduled task.”

**Example 2**

> List my current schedules with their next run times and latest results. Do not change them yet.

Read more: [Schedules, group watch and comments](automation.md).

<a id="calendar"></a>

#### Check calendars and book rooms

Use an Agent conversation to inspect calendars, find times and book a room. Complete authorization within the task when requested.

**Prerequisites:** Requires a calendar/meeting-room Skill and work-account authorization. This card does not mean a calendar is connected.

**Example 1**

> Show my meetings for this week, grouped by date with time, subject and location.

**Example 2**

> Find a room for Saturday from 8:00 to 8:30 PM. Confirm the exact date, location and headcount before booking.

Read more: [Access and connection troubleshooting](../operations/troubleshooting.md).

<a id="background"></a>

#### Keep collaborating during long work

Experiments offer foreground follow-ups, background-task display and notifications for long work.

**Prerequisites:** Depends on the engine and enabled experiments. Settings indicate whether saving applies live or requires a restart.

**Example 1**

> Explain foreground follow-ups, background-task display and completion notifications. Inspect their current switches without changing them.

**Example 2**

> I have a long task: [task]. Suggest how to keep discussing other work while it runs using my current engine.

Read more: [Conversations, materials and results](feishu-conversations.md).

<a id="goals"></a>

#### Set a goal for ongoing work

Use a goal for work that needs several rounds, such as investigating failing tests or preparing an introductory guide. In the work conversation, send goal <objective> to start, goal for status, goal pause to pause, goal resume to continue, or goal clear to remove the goal while keeping the conversation. Do not prefix these commands with /.

**Prerequisites:** The current Session must actually support goals; Codex also requires a successful capability check. Claude SDK and Pi do not currently support them. Control the goal in the same work conversation. The examples below only prepare a draft and do not start a goal.

**Example 1**

> I want to keep investigating failing tests in a repository until I have an evidence-backed root cause, a fix and validation results. Draft a message I can send in the Feishu work conversation using goal <objective>, specifying scope, permitted actions and completion criteria. Leave placeholders for an unconfirmed repository, test scope or authorization, and list missing information outside the draft. Only prepare a draft; do not access files, call tools or start a goal.

**Example 2**

> I want to turn this knowledge base into an introductory guide with source links: [knowledge-base link]. First ask me to confirm the reading scope, intended readers and completion criteria, then draft a message I can send directly in the Feishu work conversation, using the format goal <objective>. Only prepare a draft; do not start a goal, read the knowledge base or publish documents.

The two examples only draft an objective and completion criteria; they do not
start a goal. When ready, manually send the following messages in the same
conversation where the work should run. Group topics still require a real bot
mention. There is currently no amux MCP goal-management tool for an Agent to
operate this feature; it should not infer support from the engine's name.

| Message | Effect |
| --- | --- |
| `goal <objective>` | Create and start ongoing work; define scope, permitted actions and completion criteria first |
| `goal` | Read actual goal state, usage and any existing budget |
| `goal pause` | Pause automatic goal progress |
| `goal resume` | Resume a paused goal, preserving usage accounting |
| `goal budget <positive integer>` | Explicitly set a token budget; do not invent an unspecified budget |
| `goal clear` | Remove the goal and keep conversation history; external actions already performed are not undone |
| `goal set <new objective>` | Explicitly replace the goal; an ordinary create message does not replace unfinished work |

These commands have no `/` prefix. Topic mode controls that topic's bound Session;
continuous conversation controls the shared Session. Querying elsewhere does not
move the goal's output there. Creation and replacement accept plain text;
attachments go in separate follow-ups. Handle unsupported, blocked or budget-limited
states according to actual receipts; resuming does not remove every limit.

Read more: [Feishu command reference](../reference/feishu-commands.md).

### Chats and collaboration

<a id="group_agent"></a>

#### Give each group its own Agent

Per-group settings choose an expert, Runtime, model, reasoning effort and conversation mode. New settings apply to new sessions.

**Prerequisites:** Enable and apply per-group settings in Experiments, then mention the assistant with /amux in the target group. Existing sessions keep their configuration.

**Example 1**

> I want a code-review expert for my engineering group and a calendar expert for my office group. List available experts and explain the setup steps without changing other groups.

**Example 2**

> Explain how to choose a model and reasoning effort for one group and apply them to new conversations.

Read more: [Group settings and access](feishu-groups.md).

<a id="group_watch"></a>

#### Watch selected group conversations

Group watch follows selected group messages and handles work according to configured rules. Its settings can pause, resume and choose an expert.

**Prerequisites:** The target group must use topic mode and include the bot with message access. Configure the watch scope explicitly.

**Example 1**

> Explain how to choose watched groups, set the execution expert and pause watching. Do not expand the scope yet.

**Example 2**

> I want the assistant to watch [group name], using an expert skilled in [work domain] to handle eligible new topics. Check prerequisites and propose the settings, then wait for my confirmation.

Read more: [Schedules, group watch and comments](automation.md).

<a id="document_comments"></a>

#### Start work from document comments

The owner can mention the assistant in a document comment to start work with the selected expert and receive a comment reply. Choose execution options in settings.

**Prerequisites:** Requires the document-comment feature, document access and event subscription. Comments do not automatically grant execution authorization.

**Example 1**

> Explain how to request work in a document comment, what permissions are needed and how to choose the expert.

**Example 2**

> I want to request [work description] through a document comment. Inspect the setup and explain the trigger without publishing changes.

Read more: [Schedules, group watch and comments](automation.md).

<a id="context"></a>

#### Organize conversations by topic

Continuous conversation supports ongoing discussion; topic mode separates the histories of different work. Topic history and execution isolation are separate settings.

**Prerequisites:** Mode changes follow managed confirmation. Separate histories do not guarantee isolated files.

**Example 1**

> Explain when to use continuous conversation or topic mode and how each differs from file isolation. Do not change settings yet.

**Example 2**

> I am working on [work one] and [work two]. Suggest how to organize topics so their conversation contexts remain separate.

Read more: [Conversations, materials and results](feishu-conversations.md).

<a id="bot_collaboration"></a>

#### Collaborate across assistants

Supported assistants can delegate and respond to one another. The owner manages collaboration settings.

**Prerequisites:** Requires assistant collaboration, a discoverable target bot and permitted execution access.

**Example 1**

> Explain assistant-to-assistant collaboration, the current collaboration switch and how to inspect eligible assistants in a target group. Do not send messages or delegate work.

**Example 2**

> I want [target assistant] to help with [work]. Check availability and authorization, then propose a plan before proceeding.

Read more: [Delegation and collaboration](delegation.md).

### Experts and Skills

<a id="experts"></a>

#### Choose an expert for the work

An expert is like a person with particular Skills: responsibilities and reusable ways of working. Switch the everyday expert in assistant settings; existing tasks keep their snapshot.

**Prerequisites:** Experts must be available in the current libraries. Review responsibilities and execution settings before switching.

**Example 1**

> List the configured experts with their responsibilities and Skills. Recommend one for [work description].

**Example 2**

> I want to change my everyday expert. Show the current expert, available choices and the impact, then wait for my selection.

Read more: [Experts and Agents](../concepts/agents.md).

<a id="libraries"></a>

#### Keep several experts in one library

Manage multiple experts and shared Skills in a remote Git repository. Adding, syncing and managing libraries use the normal Agent Pack workflow.

**Prerequisites:** Requires an accessible Git URL and the Agent Pack format. Adoption depends on normal merge and sync.

**Example 1**

> Inspect this Git expert library and list its experts and Skills. Start with a read-only preview: [Git URL].

**Example 2**

> Add a second expert named [name] for [responsibility] to this existing library. Inspect it and preview the diff, retaining all existing experts and Skills: [Git URL].

Read more: [Expert-library format and resources](../reference/agent-profile-format.md).

<a id="skills"></a>

#### Teach an expert a Skill

A Skill is a reusable method with instructions, scripts or references. Import or author Skills and inspect the effects of updates.

**Prerequisites:** Requires a readable Skill source and library access. Import complete packages, pin versions and confirm publication.

**Example 1**

> Teach [expert name] to do [capability]. Inspect existing Skills and propose an import or authoring plan with sources before preparing library changes.

**Example 2**

> Review an update to [Skill name] in [Git URL]. List every affected expert and preview the changes first.

Read more: [Expert-library format and resources](../reference/agent-profile-format.md).

<a id="delegation"></a>

#### Delegate work to specialists

Split work into tasks for suitable experts, then combine their results.

**Prerequisites:** Requires available experts, a Runtime and execution authorization. Confirm scope and deliverables first.

**Example 1**

> Break [task description] into independent pieces and recommend experts. Show the plan before executing and combining results.

**Example 2**

> Ask suitable experts to review implementation, tests and risks for this proposal, then combine evidence-backed feedback: [proposal or link].

Read more: [Delegation and collaboration](delegation.md).

### Settings and maintenance

<a id="experiments"></a>

#### Explore experimental features

Experiments include per-group settings, Agent memory, completion notifications, foreground follow-ups and background-task display. Settings distinguish saved and active values.

**Prerequisites:** Only the owner can change these settings. Follow the page for saving or confirming a restart.

**Example 1**

> Explain the current amux experiments, which are active and which need a restart. Do not change settings yet.

**Example 2**

> I want notifications when long tasks finish. Explain thresholds and impact, then show me how to enable them in Experiments.

Read more: [Group settings and access](feishu-groups.md).

<a id="runtimes"></a>

#### Choose an execution environment and model

A Runtime supplies the actual execution environment. Model and reasoning choices configure new work, with real selections shown in assistant settings.

**Prerequisites:** Requires an available Runtime and model. Changing defaults does not migrate running work.

**Example 1**

> Explain what the execution host, engine, model and reasoning effort each control. Use my assistant defaults to show where I can inspect and choose actually available options without changing anything.

**Example 2**

> Recommend an available model and reasoning level for [work description]. Explain quality and latency trade-offs before making changes.

Read more: [Runtimes and execution locations](remote-runtime.md).

<a id="maintenance"></a>

#### Inspect and maintain amux

The management page summarizes health and version, with update checks and confirmed restart operations.

**Prerequisites:** Only the owner can perform maintenance. Updates and restarts follow management-page confirmation.

**Example 1**

> Explain the current amux version and connection observations, distinguish known facts from unchecked items, and show how to view system status in management. Do not restart or update.

**Example 2**

> Explain the amux upgrade process and its effects on running work. Show how to check for updates, verify the target version and confirm an upgrade in management. Do not install or restart.

Read more: [System maintenance](../operations/maintenance.md).

## Verify the outcome

- **Schedules:** retain the real schedule ID and next firing time. Wait for the
  first actual joke in the current conversation, then ask to cancel that schedule
  and verify the same rule is disabled. A creation receipt is not an execution result.
- **Settings and experts:** check the saved summary and use a new Session to verify
  the expert, model and reasoning level. Existing work keeps its original settings.
  For library changes, verify the official version was merged and synced; an Agent
  draft is not an installed revision.
- **Goals and delegation:** inspect the goal state card or real task/instance IDs.
  Distinguish a draft goal, an accepted request and completed work.

## If you cannot continue

| Symptom | Next step |
| --- | --- |
| Plaza asks for private chat or denies access | Use this bot's owner account and send `/amux` again in its private chat |
| Card says a capability changed or cannot be saved | Return to the plaza and choose again; retry transient failures later |
| Delivery of a request is uncertain | Inspect task replies in the same private chat first; the submission is not automatically replayed. Confirm it did not run before retrying |
| External links or calendar access fail | Check the required Skill, resource scope and account authorization; follow the Agent's authorization instructions |
| Changing an expert or model has no effect | Compare new-Session defaults with the current Session's actual settings and verify in a new Session |
| Goals are unsupported | Use actual Session capability results; choose a supported engine or run ordinary work instead |
| No daily recommendation arrived | Check the preference, activation date, Hub timezone and today's connectivity; a failed send is not retried that day |

See [troubleshooting](../operations/troubleshooting.md), the
[Feishu command reference](../reference/feishu-commands.md) and the
[Bridge model](../current/feishu-bridge-product-model.md).
