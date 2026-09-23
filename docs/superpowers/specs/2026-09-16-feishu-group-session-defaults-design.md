# Feishu group Agent Profile defaults and conversation modes

[English](2026-09-16-feishu-group-session-defaults-design.md) | [简体中文](2026-09-16-feishu-group-session-defaults-design.zh-CN.md)

Status: implementation specification, approved on 2026-09-16. Group settings cover
complete execution parameters. Validation results are recorded separately.

## 1. Design

Each `(bridge_id, chat_id)` independently selects a default Agent Profile,
Runtime, model, reasoning effort, conversation mode and workspace settings.
New Sessions that satisfy the existing trigger rules use this configuration.
Different groups using one Bot, and different Bots in one group, keep separate
settings and singleton Sessions.

| Conversation mode | Session routing | Environment |
| --- | --- | --- |
| Group singleton | New inputs reuse the group's current Session until the owner starts another | Ordinary host execution, with shared execution identity rules |
| Independent topics | Each new topic creates a Session; replies reuse it | Ordinary execution with execution authorization |
| Independent isolated topics | Each new topic creates a Session; replies reuse it | Identified user execution with required isolation |

Isolation does not split one topic into a Session per sender. Continuations keep
the original Session identity and pass execution authorization. Workspace access
(`owner_only|shared`), Bot governance, interaction mode and isolation remain
separate choices. Host isolation availability does not override conversation
policy: new ordinary and singleton Sessions select passthrough; isolated topics
pin linux-dac and fail closed when it is unavailable. Private chats and
unconfigured groups use Bot defaults. Existing Sessions retain their environment,
Principal and actor credentials; selecting passthrough does not bypass authorization.

Selecting both Profile and mode per group meets the requirement with one Bot.
Changing only the Profile cannot give groups distinct routing modes. Creating a
Bot Profile per group adds application and credential maintenance and is not the
normal workflow.

## 2. Usage

The installation-wide `bridge.feishu.group_settings.enabled` experiment defaults
to `false`. The owner enables **Group settings** from private `/amux` → Experiments
and chooses **Save and restart**. Saving only changes the file; the running Hub
keeps its startup value. The page indicates saved changes awaiting restart.

When disabled, group editing is hidden and old cards, natural-language commands,
workspace commands and the local operator CLI cannot bypass the gate.
Unconfigured groups and private chats keep existing Bot behavior. Configured
groups pause new inputs and automatic work without falling back to global
singleton routing or weakening isolation. Settings, bindings and accepted
output remain intact. Re-enabling and restarting restores the saved policy.
Experiment fields are saved atomically; older cards that omit the new field
preserve its saved value.

The owner sends `@Bot /amux` as a new message in the target group. The group sees
a status summary and the owner receives a private **Current group settings**
card showing the group name and ID. The complete editor includes Profile,
Runtime, model, effort, mode, thinking presentation and workspace parameters.
After saving, authorized members use `@Bot + task`. Ordinary members can view the
summary but cannot edit settings; Feishu group administration does not imply
amux ownership. System maintenance controls stay in private chat.

The searchable Profile selector offers **Follow Bot default Profile** and shows
the resolved Profile and its source. The first draft uses effective Bot settings;
saving makes the mode and execution fields independent. Subsequent Bot edits do
not overwrite them. A singleton card separately shows the current Session's
Profile and the default for the next Session. **New conversation** explains that
context starts empty; saving a Profile never silently replaces the current Session.

Owner commands include:

```text
@Bot /workspace default-agent team/reviewer
@Bot /workspace default-agent inherit
@Bot /workspace interaction singleton
@Bot /workspace interaction thread
@Bot /workspace interaction thread_isolated
@Bot /workspace status
```

The existing natural-language group default Profile request uses the same write
path. `/workspace mode owner_only|shared` still controls access.
`default-agent inherit` clears only the Profile override, keeping the group's
mode and Session scope. The first field update saves a full snapshot seeded from
current Bot/workspace defaults. Commands report transition effects; cards explain
them during confirmation. Bridge handles these management inputs before any Agent.

## 3. Defaults and transitions

New Sessions choose the explicit request Profile, then an explicit group Profile,
then the Bot default, then the distribution default. An unavailable explicit
choice fails; it never falls through to a lower priority. Public code does not
hard-code a distribution's private Profile. Projection and Pack updates retain
existing launch snapshot rules.

Existing topics, singleton continuations, wake and restart retain their Session
snapshot. `/bind` uses the bound Session's Profile, and `/run` does not change
group defaults. Both must satisfy required group isolation. An empty Runtime
selects the Profile's native runtime; empty model and effort select the engine's
defaults. Native model names remain subject to existing model/Runtime policies.
The complete saved tuple includes target, cwd, arguments, idle timeout, context
limit and Session quotas. Invalid fields reject the whole transaction. Profile
changes expose incompatible selections for correction instead of silently clearing
them. Launch rechecks Runtime availability, identity, credentials and isolation.

| Change | Existing work | Future inputs |
| --- | --- | --- |
| Change group default Profile | Existing Profile and context remain | New Sessions use the new default |
| Change Bot default Profile | Existing Sessions and explicit group overrides remain | Inheriting groups use the new Profile for new Sessions |
| Switch ordinary/isolated topics | Existing identity/environment remain | New Sessions and new bindings satisfy the new isolation policy |
| Topics → group singleton | Existing topics keep their Sessions | New unbound inputs use the group singleton; no history merge |
| Singleton → topics | Accepted work finishes; old singleton rejects new inputs | New topics create Sessions; old entries receive a new-topic notice |
| Start a new singleton | History and accepted work remain | New inputs start empty with current defaults |

Cards must not imply existing Sessions were migrated into isolation. Outputs
continue to their group during transitions. Switching back to singleton creates
a fresh generation rather than reviving an old one. Creation is lazy on the
first valid task. Failed launches do not leave an active pointer and can be
retried. A missing current Session prompts recovery or an explicit new conversation;
it must not disguise lost context as a normal continuation.

## 4. Routing and context

One consistent group policy snapshot supplies the version, Profile source, mode,
isolation requirement and execution fields throughout launch. Routing handles
Bridge management and authorization first, preserves ordinary existing topic
bindings, rejects retired singleton entries, then selects topic or singleton
routing for unbound inputs. Session projection, authorization, isolation checks
and persistence precede submission.

Settings transitions and singleton creation use a lock scoped by Bot and group.
Concurrent first inputs create one Session; version checks prevent an old launch
from binding against a newer policy. Deduplication retains each real input's
source. Coalescing, queues and streams use the first actual input as an internal
anchor, never the Bot-global `singleton:<bridge_id>` key.

Group singleton replies, activity, permission cards and artifacts are sent directly
to the group. The separate completion-notification experiment retains its private
notification rules. Input and output aliases point to its Session so replies continue
that conversation. The output destination does not drift to the last input or use
the Bot-global last-reply destination. Cross-topic inputs retain their speaker and
origin, but do not aggregate shared history from every topic, other group or DM.
The Session supplies conversation history; explicit quotes retain existing rules.
Topic mode keeps existing root-prompt and shared-thread-context boundaries.

Separate group conversations do not promise separate OS users, credentials or
filesystem security boundaries. Singleton execution retains shared service
Principal authorization. Execution isolation does not change Feishu message
visibility or prohibit quoting visible messages.

Group watch checks the target group's effective mode and supports both topic
variants. Another group's or the Bot's singleton mode does not disable it. Before
switching a watched group to singleton, the owner removes it from watch; conflicting
saves fail without implicitly changing another configuration store. Watch inherits
group execution defaults unless it has explicit capability overrides, which still
must satisfy group isolation. An automatic source without a valid isolated identity
fails rather than impersonating the owner. Document comments, DMs and other inputs
without a group scope retain their own defaults.

## 5. Implementation boundaries

The public core owns this feature in `internal/bridge`; distributions supply
Profiles through existing registrations and Packs. A workspace extension table,
`feishu_group_settings`, is the authority for group configuration:

| Field | Meaning |
| --- | --- |
| `bridge_id, chat_id` | Composite key; row existence records independent configuration |
| `default_agent` | Empty means inherit Bot Profile at new launch |
| `interaction_mode` | Saved `singleton` or `thread` choice |
| `isolation_required` | May be true only for thread; UI `thread_isolated` maps to this pair |
| Execution/workspace fields | Runtime, model, effort, presentation, target, cwd, args, idle timeout, context and quotas |
| `state_version` | Optimistic concurrency; stale cards require refresh |
| `updated_by, updated_at` | Audit metadata |

`FeishuWorkspace.Mode` retains access semantics. Its legacy `default_agent` is
not reinterpreted as a new explicit override. Settings store one current
singleton anchor; the real topic binding retains Session, Profile and target.
Existing Session/Launch/Turn receipts retain identity. Origin records associate
each generation's inputs and outputs with its anchor and allow accurate output
recovery after restart. Old generations may finish accepted work.

Storage owns atomic saves; policy resolution owns inheritance; routing chooses
bindings; the existing starter creates Sessions; streams render to the retained
destination. Opening a card creates neither policy nor Session. Pack removal and
reference checks include Profiles referenced only by group defaults.

## 6. Compatibility

Database migration alone does not change routing. Groups opt in by saving their
first independent snapshot after enabling the experiment. That save explains the
separation from any Bot-global singleton. It copies no global singleton history,
credentials or bindings. Accepted old work finishes through its receipts; existing
topic Sessions remain bound. The Bot Session remains available to DMs and groups
without independent settings.

There is no implicit opt-out into a global singleton. Following the default
Profile only inherits that value. A full opt-out would need an explicit design
for the resulting shared-context scope change. This design preserves the Bridge model's separation:
governance, interaction and hosting topology remain distinct.

## 7. Acceptance

Validation covers two groups with different Profiles and modes; two Bots in one
group; concurrent first singleton inputs; two isolated topic initiators; authorized
continuations; changed defaults versus frozen Sessions; explicit run/bind; mode
transitions and retirement with pending work; identity and isolation failures;
stale/forged/non-owner cards; restart/deduplication; watched topics under a
singleton Bot; and read-only status without mutations. Also verify default-off,
save-before-restart, disabled callbacks/CLI/automation and accepted output recovery.

Update current models before behavior changes, regenerate affected references,
and run the repository gate on the frozen diff. Real L4 evidence must state
unsupported environmental cases explicitly and is retained separately.

## 8. Implementation references

- [Bridge model](../../current/feishu-bridge-product-model.md) and [thread context](../../current/feishu-thread-context-model.md)
- [Architecture overview](../../concepts/architecture.md): Bot access and system topology are independent.
- [Profile format](../../reference/agent-profile-format.md)
- [Group management](../../../internal/bridge/router_workspace.go), [topic launch](../../../internal/bridge/router_thread.go), and [legacy Bot singleton](../../../internal/bridge/router_botsession.go)
- [Group summary](../../../internal/bridge/management_groups.go) and [settings cards](../../../internal/bridge/management_group_settings.go)

## 9. Local operator interface

On the Hub host, use the same installation and Bot profile:

```sh
amux bridge feishu group-settings show <chat-id> --profile default > group.json
# Edit the complete JSON; retain bridge_id, chat_id and version.
amux bridge feishu group-settings apply <chat-id> --profile default --file group.json
amux bridge feishu group-settings new-conversation <chat-id> --profile default --expected-version <version>
```

These commands use the local Bridge DB regardless of a remote Hub address.
Missing fields, invalid values and stale versions fail. Target availability,
execution identity and isolation readiness are rechecked at actual launch.
