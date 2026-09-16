<a id="local-专家自然语言编辑方案"></a>

# Natural-Language Editing of Local Experts

[English](local-expert-natural-language-authoring-design.md) | [简体中文](local-expert-natural-language-authoring-design.zh-CN.md)

> Maintained with the public core. Source paths are relative to the public repository root.

Status: **natural-language editing and explicitly requested lightweight evaluation
are implemented (Feishu Bot owner only)**.

This document describes natural-language editing of managed `local/*` experts.
Existing local Git, Contract validation, revision CAS, immutable materialization,
and atomic registry switching remain unchanged. This phase adds an owner-only
natural-language entrypoint and reuses the existing publication path.

<a id="1-用户体验"></a>

## 1. User experience

Create or modify a local expert directly in the current Feishu conversation:

```text
User: Make local/reviewer focus more on concurrency safety.
Agent: Show a change summary and diff.

User: Less emphasis on locks; add idempotency checks.
Agent: Show the updated diff.

User: Evaluate the changes.
Agent: Test the published and candidate versions on two representative questions; show checks and blind comparison results.

User: Confirm publication.
Agent: Publish and return the new revision.
```

Also supported:

- Create a new `local/<name>` from the default template.
- Fork an independent local expert from `builtin/<name>`.
- Edit an existing local expert's Profile, Prompt, and Skill files.

No separate editing task or card is required. Private/group and thread/singleton
conversations share the same flow. Only explicit user confirmation produces an
official Git commit.

<a id="2-最小状态模型"></a>

## 2. Minimal state model

amux does not persist an editable Candidate, Draft, or candidate version. The
patch being edited exists only in the current conversation's Agent context:

```text
Published revision
  -> Agent reads the necessary files
  -> Agent creates a unified Git patch from the natural-language request
  -> amux applies it temporarily and returns a real diff plus a signed preview receipt
  -> On explicit user evaluation request and any required side-effect confirmation, materialize a short-lived hidden Profile for the same preview tree; clean up after A/B Tasks
  -> Agent may revise the patch and preview again
  -> User confirms in a later message
  -> amux publishes one commit with the same patch and preview receipt
```

An evaluation Profile is an immutable runtime snapshot, not a draft. It has no
name selection, version list, restore, merge, or further editing capabilities.
It does not update the published local branch/revision, is hidden by default,
and is deleted on explicit cleanup or after 30 minutes. There is still no
pre-publication v1/v2/v3 or multiple-candidate synchronization problem. If the
conversation context is lost, reread the published version and regenerate the
patch; the published repository is unaffected.

<a id="3-支持范围"></a>

## 3. Supported scope

| Operation | Baseline | Result |
| --- | --- | --- |
| `create` | Standard local default template | New `local/<name>` |
| `fork` | Read-only `builtin/<name>` snapshot | New independent `local/<name>` |
| `update` | Current revision of an existing `local/<name>` | Updated local expert |

One patch can add, modify, or delete:

- Profile and manifest configuration.
- Instructions, Prompts, and other text assets.
- A Skill's `SKILL.md` and text files under `references/`, `scripts/`, and `assets/`.

Authoring patches support text files only; binary patches are rejected. A fork
can retain existing binary assets from builtin unchanged, but this phase cannot
add, modify, or delete binary files through a natural-language patch.

Skill edits modify the Pack's target files directly, without copy-on-write or
shared-reference impact analysis. When multiple local Profiles reference one
Skill, their new Sessions/Tasks use the same published Skill.

Deleting a whole expert, natural-language history/restore, automatic next-round
optimization, and automatic publication are out of scope. Hidden diagnostic CLI
commands remain for machine-side diagnosis and recovery, not normal user editing.

<a id="4-两个工具"></a>

## 4. Two tools

Only two MCP tools are exposed to the Agent:

### `amux.local_expert_read`

Read the current builtin/local revision and necessary files, returning bounded,
paginated structured results. No state is changed.

### `amux.local_expert_change`

Handle preview and publication through one interface. Main arguments:

```text
mode                preview | publish | cleanup
operation           create | fork | update
target_ref          local/<name>
source_ref          builtin/<name> for fork
base_revision
patch               unified Git patch
preview_receipt     required for publish
prepare_evaluation  optional for preview
```

The input Schema requires complete fields per mode: preview requires `operation`,
`target_ref`, `base_revision`, and `patch`; publish also requires `preview_receipt`;
cleanup requires `preview_receipt`; fork additionally requires `source_ref`.

`preview`:

1. Create a temporary worktree from the default template, builtin snapshot, or local `base_revision`.
2. Apply the Agent's unified Git patch.
3. Run Agent Pack Contract validation.
4. Return the normalized real `git diff`, result tree hash, and signed `preview_receipt`. The receipt binds the Session, preview turn, operation, target/source, base revision, patch digest, and tree. It expires after 30 minutes by default.
5. Delete the temporary worktree by default, without saving a runtime candidate or creating an official commit.
6. `prepare_evaluation` defaults to false. Ordinary creation, editing, or optimization requests must not set it true. Only an explicit owner request to evaluate this candidate, plus separate confirmation of the test plan if the target Profile/Skill or case may write, notify, or cause other external effects, permits materializing the same tree as a hidden `local-eval-*-dev/<name>` temporary Profile. Even a read-only-looking case requires confirmation when the target has those capabilities. The Profile does not update the published local ref/revision and expires after 30 minutes.

`publish`:

1. Reapply the same patch in a new temporary worktree.
2. Rerun Contract validation and compute the tree hash.
3. Verify `preview_receipt`: operation, target/source, base revision, patch digest, and reapplied tree must match exactly. A mismatch reports the specific field.
4. Require the published repository to remain at `base_revision`.
5. Create one commit through the existing CAS, immutable materialization, atomic registry switch, and last-known-good flow.

`cleanup` validates the current owner turn, Session, and same preview receipt,
then deletes the evaluation Profile bound to that receipt without revoking the
receipt. Successful publication also attempts cleanup. A daemon periodically
reaps leftover candidates by TTL. Ordinary conversation between preview and
confirmation does not revoke a still-valid receipt. Any later owner turn can
publish with identical arguments, but the preview turn cannot publish immediately.

The interface is small while implementation enforces the details. The Agent
understands natural language and maintains the current patch; amux reads,
validates, and publishes atomically. No `LocalExpertCandidate`, `LocalExpertDraft`,
or dedicated candidate database is needed; temporary Profiles reuse the Pack registry.

<a id="5-为什么不让-agent-直接写正式-git"></a>

## 5. Why the Agent does not write published Git directly

The Agent may use Git patch semantics but cannot directly update
`$AMUX_HOME/managed-agent-packs/local/origin.git`:

- The current Session may run on a remote amuxlet; the Agent shell may not be on the host holding the local repository.
- Direct Git writes bypass user confirmation, Contract validation, revision CAS, and atomic registry switching.
- Tools can restrict access to clean Pack-relative paths and reject absolute paths, `..`, `.git`, symlinks, and escapes.

Git remains the underlying source of truth, but all official writes go through
`local_expert_change(mode=publish)`.

<a id="6-自然语言编排规则"></a>

## 6. Natural-language orchestration rules

`amux-skill` follows this flow:

1. Call `local_expert_read` for the necessary context.
2. Generate a patch from the request. By default call `local_expert_change(mode=preview)` and show only the real diff. “Optimize” alone is not an evaluation request.
3. Prepare evaluation only when the user explicitly asks to “evaluate,” “test,” “verify the changes,” or “compare old and new versions” for this candidate. Assess side effects from the target Profile, Skill, available tools, and proposed case. Proceed without a separate test-plan confirmation only when the target neither involves nor naturally triggers external actions and the case is verifiably pure text reasoning or read-only analysis. If the target can write files/configuration/databases, create or modify external objects, publish/deploy, send messages/mail/notifications, or call state-changing APIs, first show the case, environment, possible actions, and impact, then wait for explicit confirmation in a later user message, even if the case looks read-only. Apply the same rule when the case itself has side effects or cannot be verified as read-only. Do not create a candidate or start a Task before confirmation.
4. After evaluation is authorized, preview the same patch with `prepare_evaluation=true`. Choose 1–3 cases representative of the goal and use `task_run` for the published Profile and returned candidate Profile with identical Prompt, model, permissions, isolation, and result granularity. Run deterministic format/keyword checks first, then compare outputs with A/B identities hidden. Do not put orchestration instructions such as “must invoke this Skill” into business cases. If the versions share no model, mark them incomparable. Subjective judgment belongs to an independent ephemeral judge Task that sees only randomized A/B outputs, not refs, diffs, or call order.
5. Call `local_expert_change(mode=cleanup)`. Report cleanup failure honestly; TTL remains the fallback.
6. Show a concise evaluation report and the real server-side diff. Failed or timed-out evaluations must not be reported as passes or trigger automatic publication.
7. For further edits, update the current patch and preview again without creating candidate versions. After an evaluation finishes or the patch changes, the earlier request and test-plan confirmation are consumed. Each re-evaluation needs a new explicit request for the new candidate and, where side effects are involved, a new plan and subsequent confirmation.
8. Test-plan confirmation authorizes only this evaluation of this candidate, not publication. Publish with the same patch and `preview_receipt` only after the user explicitly confirms the same diff in a later message.

If the target changes, conversation context loses the patch, the published
revision changes, or the tree hash differs, reread and preview again. Do not guess
or silently overwrite.

<a id="7-当前-turn-授权"></a>

## 7. Current-turn authorization

The local expert repository belongs to one amux installation and is shared by
its Feishu Bots. Publication authority belongs to the current Bot Profile's
owner. Owners can use it in private/group and thread/singleton conversations;
non-owners, external Bots, and automatic input are rejected.

Authorization binds to the current input turn, not permanently to the Session:

```text
Bridge verifies the message sender is the current Bot owner
  -> attach a structured turn grant through the trusted Hub route
  -> reverse tunnel adds in-process provenance that direct gRPC requests cannot forge
  -> Runtime binds the grant to this TurnID
  -> MCP publication checks the current authorized turn
  -> grant expires on turn_done / error / interrupt
```

`read`, `preview`, and `publish` use the same owner-turn validation. The model
cannot supply its own owner identity or create authorization. Preview requests
and subsequent confirmations must belong to different turns, preventing an Agent
from generating and immediately publishing within one request.

Publication also checks:

1. The MCP call carries a delegated Agent credential for the current Session. Even with legacy-host execution, Feishu Sessions attempt to obtain a Session-scoped credential on launch and rotate it on restart; they do not fall back to trusting only `AMUX_SESSION_ID`. If issuance fails, ordinary conversation may continue, but local authoring fails closed for that Session.
2. The currently active turn has a local authoring grant.
3. The signed receipt's Session, preview turn, operation, target/source, base revision, patch digest, and tree match the publication request, which occurs in a later owner turn.
4. The published revision still equals `base_revision`.

Multiple owners or Sessions may preview concurrently. Revision CAS prevents them
from silently overwriting one another's published commits.

<a id="8-发布与生效"></a>

## 8. Publication and activation

```text
patch + confirmed preview_receipt
  -> validate turn owner
  -> replay in temporary worktree
  -> validate Contract + tree hash + base revision
  -> one Git commit
  -> revision CAS
  -> immutable revision materialization
  -> atomic registry switch
```

Failure at any step retains the last-known-good revision. Fork and create also
produce only one official commit.

A new revision affects only new Sessions/Tasks or explicit
`restart --refresh-profile`. The Session requesting the edit keeps its frozen
launch revision, preventing an expert from changing its own behavior mid-execution.

<a id="9-实现落点"></a>

## 9. Implementation locations

| Location | Change |
| --- | --- |
| `docs/current/*.md` | Update Agent Profile, Distributed Task, and Feishu product rules before implementation |
| `internal/agentprofile` | Read, temporary worktrees, patches, Contract, tree hashes, and integration with the publication flow |
| `proto/amux/v1/amux.proto` | Current-turn grants and read/change RPCs |
| `internal/bridge/router.go` | Derive turn grants from authenticated Feishu messages |
| `internal/session`, `app/runtime` | Store, query, and clear grants at terminal turn state |
| `app/hub` | Authorize and execute local read/change on the installation host |
| `app/cli/mcp.go` | Two MCP tools, candidate refs, and bounded structured results |
| Distribution-provided natural-language authoring Skill | Natural-language preview, A/B Tasks, revision, and confirmed publication flow |

No public CLI command is added. Run `make proto` after protocol changes and the
full `GOCACHE=/private/tmp/amux-go-cache scripts/check.sh` before committing code.

<a id="10-验收"></a>

## 10. Acceptance

- Create, fork, and update can preview without persisting a candidate or creating a commit.
- One patch can edit a Profile, Prompt, and multiple Skill text files together. Reject binary patches.
- Multiple rounds of natural-language edits repeat preview without server-side candidate versions.
- Normal create/edit/optimize requests do not evaluate by default; only explicit requests prepare candidates.
- If the target Profile/Skill involves writes, notifications, or other external effects, show the plan and impact and obtain later confirmation even when the case looks read-only. Do the same for cases with side effects or uncertain read-only status.
- An evaluation request and plan confirmation authorize only this evaluation of the current patch. Re-evaluation needs a new explicit request and renewed plan confirmation for side effects. Evaluation confirmation does not authorize publication.
- Authorized evaluation uses the exact tree in the preview receipt, with the same cases and execution parameters for published and candidate versions.
- Both versions' Skill files travel with the Task snapshot; remote Runtimes project identical contents.
- Do not run A/B comparison without a shared model. Subjective comparison requires an independent judge Task with identities stripped.
- Temporary candidates stay out of ordinary discovery and the Feishu expert library, disappear on cleanup or TTL, and do not change the published local revision.
- Failed/timed-out evaluations or failed candidate launches do not publish and report their actual status.
- Missing confirmation, expired receipts, patch/tree mismatch, or changed base revisions prevent publication.
- Owners can publish in private/group and thread/singleton modes. Other input cannot inherit authorization.
- Publish creates one official commit; failure preserves last-known-good.
- The current Session keeps its old snapshot; new Sessions use the new revision.
- `amux update` updates builtin without overwriting local.

<a id="11-安全边界"></a>

## 11. Security boundary

The control plane jointly validates trusted tunnel provenance, a Session-scoped
Agent credential, the current owner-turn grant, and the preview receipt.
Ordinary amuxd Submit has no grant field. Direct amuxlet Submit calls cannot forge
internal boolean fields to bypass validation. Local authoring RPCs with missing
or incorrect Agent credentials also fail closed.

This does not promise protection against a malicious process under the same Unix
account that can read other processes' environments or steal bearer credentials.
Future hostile-agent requirements would need separate system identities or a
strong isolation backend and exclusive published local Git write access through
a separate daemon. That does not change the two-tool interaction or publication
transaction described here.
