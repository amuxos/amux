# Group settings, access and approvals

[English](feishu-groups.md) | [简体中文](feishu-groups.zh-CN.md)

This guide is for bot owners enabling colleagues, selecting group-specific
experts and conversations, and controlling execution. Ordinary members address
the bot according to group policy; a management card does not make them owners.

## Separate three settings

| Setting | Question it answers | What it does not imply |
| --- | --- | --- |
| Owner-only / collaboration (`owner_only` / `shared`) | Who can start work in this group? | System maintenance, approval of every request, or access to all credentials |
| Continuous / independent topics (`singleton` / `thread`) | Which Session receives a new message? | Filesystem, identity or memory isolation |
| Topic user isolation | Must execution use the configured Linux UID/DAC boundary? | Host, identity and authorization prerequisites cannot be created merely by selecting a UI option |

An engineering group might allow collaboration with independent topics; a
project group might use one continuous conversation. System updates and restarts
remain owner/operator responsibilities in either case.

## Let colleagues participate

1. Ensure the bot is in the target group and you are its configured amux owner.
2. Mention the actual bot and send `开启协作模式`. This literal Chinese phrase is
   a recognized command, not a request for the Agent to infer a configuration edit.
3. Mention the bot with `/amux` and verify the group's access mode.
4. Have a colleague mention it with a small request in a new topic to verify
   both access and execution prerequisites.
5. To withdraw group collaboration, the owner sends `关闭协作模式` and checks status again.

Collaboration controls group admission. An installation with execution identity
management may also require identity enrollment or service membership. Enabling
collaboration does not guarantee every later authorization check will pass.
Group membership alone does not let a user interrupt another user's topic,
change its binding or approve their execution requests.

## Open this group's management view

The owner mentions the bot with `/amux` in a group root message. The group gets
a shareable status summary; the owner also receives a group-specific management
card in private chat. Check its group name, ID and assistant before editing.
Within an active bound topic, `/amux` shows that work instead; use the group-level
entry for group settings.

Without independent group settings, the group keeps the existing bot behavior
and management focuses on access and watch status. Opening a card does not create
a Session or opt the group into independent settings.

## Configure independent defaults

### Enable the experiment

Independent group settings are off by default. The owner enables them in private
`/amux` → experiments, saves, and confirms a system restart. Saving configuration
and applying it to the running system are separate steps. Check active work first,
since restarting can affect execution.

The switch enables the feature, not a migration of every group. A group opts in
only when its first complete settings snapshot is saved. Unsaved groups and
private chats retain the existing bot behavior.

### Save and verify

1. Mention the bot with `/amux` in the target group and open the current-group
   settings card received privately.
2. Choose the edit-group-settings action and inspect conversation mode, access,
   default expert and execution options.
3. Edit the provided fields; a large form may have several pages. Unsaved selections
   are only a draft.
4. Save, return to the summary and check the settings source and new-session defaults.
5. Start a new topic, or a new group continuous conversation when needed, and
   verify the actual expert and model.

Saving validates the complete settings set instead of accepting only valid pieces.
Cards carry a version: if another operation changed the configuration, an old
card reports a conflict or expiry. Reopen `/amux` in the target group and review
the latest values before editing again.

### Understand the fields

| Field | Current meaning and constraints |
| --- | --- |
| Group conversation mode | One conversation in this group, independent topics, or independent topics with user isolation |
| Default expert for new Sessions | An installed Profile; following the assistant's default inherits only the expert choice, not an opt-out from independent group policy |
| Execution engine | A supported engine or the expert's native engine; distinct from the execution machine |
| Model and reasoning effort | Must meet Profile, engine and model rules; saving does not purchase model access or grant credentials |
| Activity display | Collapse, expand or hide the process panel; does not change execution permissions |
| Who can start work | Owner only or group members, still subject to bot and execution-identity checks |
| Target machine | Runtime execution location; an explicit target must resolve uniquely |
| Working directory | An absolute path on that target; local paths are not copied to remote hosts |
| Extra arguments | A JSON string array such as `["--flag", "value"]`; actual arguments must be supported by the engine |
| Idle timeout | Seconds; zero disables automatic sleep, otherwise at least 60; sleeping does not delete history or workspace |
| Context message count | Shared-topic records selected for subsequent input, also bounded by context budgets |
| Group / per-user Session limits | Capacity for creating Sessions; zero means unlimited, not a token allowance |

Empty engine, model and reasoning selections use the selected Profile's defaults;
they do not continuously inherit the bot's whole execution configuration. Defaults
mainly apply to newly created Sessions. Existing topics, explicit `/run` selections
and `/bind` targets retain their own identity and execution settings.

## Move from bot-shared context to a group conversation

A bot singleton can cover private chat and several groups. After saving independent
continuous-conversation settings for one group, its future work uses its own
Session. Other groups and private chats are not migrated. Global bot history and
credentials are not copied; the first valid input starts work under the new policy.

The group card separately displays new-session defaults and the current Session's
expert/model. A difference can simply mean the defaults changed while the old
continuous conversation remains active.

To start with blank context, the owner selects the new continuous conversation
action. The old default input pointer is retired; accepted work can finish. The
next valid new input creates the new group conversation. Replies to old sources
neither drive the retired conversation nor silently enter the new one. Existing
ordinary topic bindings retain their own lifecycle.

Disable watch for this group before switching it to continuous conversation.
Group watch needs independent topics; see [automation](automation.md).

## Choose the isolation boundary

Independent topics with user isolation adds a mandatory isolation requirement
to `thread` routing; it is not a third routing enum. The operator must prepare
the [Linux isolation backend](../operations/linux-dac-isolation.md), identities
and workspace access. Unsupported hosts, missing identity or incompatible bound
Sessions fail instead of silently weakening isolation.

Enabling the host's isolation capability does not automatically isolate every
ordinary Feishu conversation. Existing Sessions retain their launch environment;
a group default change does not migrate running processes.

When the installation enables principal isolation policy, bot and group continuous
conversations require the bot's service identity and active service membership
for senders. Personal-topic enrollment and group collaboration do not replace
those prerequisites. Ask the operator to configure them instead of repeatedly
toggling collaboration after an identity failure.

## Handle execution permission requests

An execution may separately request approval for files, commands, tools or questions.

1. Check the Session, exact operation, paths, targets and meaning of its options.
2. An authorized owner or request-associated user responds; submit the complete
   selection when the card supports multiple choices.
3. Wait for the card to show it was handled, then check execution continues.
4. For an expired/already-handled request, inspect current status. If delivery
   failed, check connectivity; a button click alone does not prove authorization succeeded.

Denying a request, stopping a Session and cancelling a child Task are different
operations. Task policies `policy-allow`, `deny-all` and `escalate-to-main` control
permission requests; they neither create filesystem boundaries nor supply missing
credentials. See [delegation](delegation.md).

## Disable the experiment or diagnose problems

After disabling independent group settings and restarting, groups with saved
settings pause new inputs and automatic work while retaining configuration,
bindings and output for accepted work. They do not fall back to the bot's global
continuous conversation. Re-enable and restart to restore saved policy. Unsaved
groups and private chats retain existing behavior.

| Symptom | Action |
| --- | --- |
| Only a summary is visible in the group | Owner checks the private card, enabled experiment and completed restart |
| Opening settings did not change behavior | Check that settings were saved and no longer show inherited assistant settings |
| Old expert or model remains | Compare new-session defaults with the current Session; verify using a new conversation |
| Save says configuration changed | Reopen the group card and edit the latest values |
| Switching to continuous conversation is rejected | Check whether this group's watch is still enabled |
| A collaborator still cannot execute | Check bot admission, execution identity, service membership, isolation and capacity |
| Group pauses after disabling the experiment | Restore the experiment and restart; pausing preserves saved routing and isolation boundaries |

Formal contracts are in the [Bridge model](../current/feishu-bridge-product-model.md)
and [Session model](../current/session-product-model.md).
