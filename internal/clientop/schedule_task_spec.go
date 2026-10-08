package clientop

import (
	"strings"

	"github.com/amuxos/amux/internal/task"
)

// RepoSpec is the caller-facing workspace repo description.
//
// Remote and Subdir are dead fields: they are accepted by the CLI's
// --repo-remote / --repo-subdir flags and carried on the wire (task.RepoSpec),
// but no runtime code reads either one today (repoCacheAndCommit in
// internal/task/runner.go only consults Commit and CacheKey). They keep
// schema:"-" so the MCP tool surface does not promise behavior nothing
// implements; TestMCPToolSchemasOmitUnsupportedRepoFields pins that. The
// fields and CLI flags stay for now — removing them would be a user-visible
// CLI behavior change, which is out of scope here.
type RepoSpec struct {
	Kind     string `json:"kind,omitempty" desc:"Workspace repo 类型，例如 git"`
	Remote   string `json:"remote,omitempty" schema:"-" desc:"Workspace repo 的远程 URL 或稳定 ID"`
	CacheKey string `json:"cache_key,omitempty" desc:"在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key"`
	Commit   string `json:"commit,omitempty" desc:"Workspace repo 的 commit 或 ref"`
	Subdir   string `json:"subdir,omitempty" schema:"-" desc:"Workspace repo 子目录"`
}

func (r *RepoSpec) empty() bool {
	return r == nil || (strings.TrimSpace(r.Kind) == "" && strings.TrimSpace(r.Remote) == "" &&
		strings.TrimSpace(r.CacheKey) == "" && strings.TrimSpace(r.Commit) == "" &&
		strings.TrimSpace(r.Subdir) == "")
}

// WorkspaceSpec is the caller-facing workspace description.
type WorkspaceSpec struct {
	Policy   string    `json:"policy,omitempty" enum:"temp-dir,git-worktree" desc:"temp-dir 为每次运行创建一次性 Workspace；git-worktree 从 repo cache 检出 worktree"`
	LocalDir string    `json:"local_dir,omitempty" desc:"目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key"`
	Repo     *RepoSpec `json:"repo,omitempty"`
}

// ScheduledTaskSpec is what a caller may set on a scheduled agent task. It is
// deliberately not task.Spec: that type also carries control-plane-only fields
// (execution_identity, resolved_profile, parent_task_id) which the hub rejects
// from clients, and blocking, which a schedule never uses.
type ScheduledTaskSpec struct {
	ExtraArgs            []string      `json:"extra_args,omitempty"`
	Agent                string        `json:"agent" desc:"Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref，例如 builtin/codex 或 team/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口"`
	Runtime              string        `json:"runtime,omitempty"`
	Model                string        `json:"model,omitempty" desc:"每次定时运行面向用户的模型覆盖；留空时使用 Profile 或 Agent 默认模型"`
	Task                 string        `json:"task" desc:"Schedule 每次触发时提交的 Task 提示词"`
	RunAs                string        `json:"run_as,omitempty" desc:"可选的已登记 Principal alias；保存 Schedule 时解析并固化"`
	Isolation            string        `json:"isolation,omitempty" enum:"process,private-home" desc:"Sandbox 隔离模式；Phase 1 为 best-effort trusted mode"`
	CredentialProfile    string        `json:"credential_profile,omitempty" desc:"控制面 credential profile 名称"`
	PermissionPolicy     string        `json:"permission_policy,omitempty" enum:"default,policy-allow,deny-all" desc:"留空或 default 时使用 Profile 或 Task 默认策略；允许 policy-allow 和 deny-all；定时 Agent Task 不允许 escalate-to-main"`
	Workspace            WorkspaceSpec `json:"workspace,omitempty"`
	TimeoutSeconds       int           `json:"timeout_seconds,omitempty" desc:"整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）"`
	LegacyTrustedModeAck bool          `json:"trusted_mode_ack,omitempty" desc:"旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留"`
}

// Validate reports the first rule the spec breaks, naming the field so each
// surface can phrase it in its own vocabulary.
func (s ScheduledTaskSpec) Validate() *FieldError {
	if strings.TrimSpace(s.Agent) == "" {
		return RequiredError(FieldAgent)
	}
	if strings.TrimSpace(s.Task) == "" {
		return RequiredError(FieldTask)
	}
	if _, err := normalizeScheduledPermissionPolicy(s.PermissionPolicy); err != nil {
		return err
	}
	policy := task.WorkspacePolicy(strings.TrimSpace(s.Workspace.Policy))
	if policy == task.WorkspaceGitWorktree &&
		strings.TrimSpace(s.Workspace.LocalDir) == "" &&
		(s.Workspace.Repo == nil || strings.TrimSpace(s.Workspace.Repo.CacheKey) == "") {
		return RequiredEitherError(FieldWorkspaceLocalDir, FieldRepoCacheKey,
			"with workspace policy git-worktree")
	}
	return nil
}

// normalizeScheduledPermissionPolicy maps the caller-facing word to its wire
// value. escalate-to-main is refused here rather than at the hub so the caller
// learns at creation time, not at the first trigger.
func normalizeScheduledPermissionPolicy(value string) (task.PermissionPolicy, *FieldError) {
	switch policy := task.PermissionPolicy(strings.TrimSpace(value)); policy {
	case "", "default":
		return "", nil
	case task.PolicyAllow, task.PolicyDenyAll:
		return policy, nil
	case task.PolicyEscalateToMain:
		return "", &FieldError{
			Field:    FieldPermissionPolicy,
			Problem:  "value escalate-to-main is not supported for scheduled agent tasks",
			RawValue: strings.TrimSpace(value),
		}
	default:
		return "", InvalidError(FieldPermissionPolicy, strings.TrimSpace(value), "default, policy-allow, or deny-all")
	}
}

// ToTaskSpec converts to the wire type. Callers must Validate first: this
// method panics if the permission policy is not one Validate would accept,
// since continuing would silently coerce a refused policy (escalate-to-main)
// into the permissive empty-string default instead of failing loudly.
func (s ScheduledTaskSpec) ToTaskSpec() task.Spec {
	policy, err := normalizeScheduledPermissionPolicy(s.PermissionPolicy)
	if err != nil {
		// Reaching here means a caller skipped Validate. Continuing would
		// coerce a refused policy into the permissive default, so fail loudly
		// instead of shipping a spec that says something the caller never asked
		// for.
		panic("clientop: ToTaskSpec called on an unvalidated spec: " + err.Error())
	}
	out := task.Spec{
		ExtraArgs:         append([]string(nil), s.ExtraArgs...),
		Agent:             strings.TrimSpace(s.Agent),
		Runtime:           strings.TrimSpace(s.Runtime),
		Model:             strings.TrimSpace(s.Model),
		Task:              strings.TrimSpace(s.Task),
		Isolation:         task.IsolationMode(strings.TrimSpace(s.Isolation)),
		CredentialProfile: strings.TrimSpace(s.CredentialProfile),
		PermissionPolicy:  policy,
		Workspace: task.WorkspaceSpec{
			Policy:   task.WorkspacePolicy(strings.TrimSpace(s.Workspace.Policy)),
			LocalDir: strings.TrimSpace(s.Workspace.LocalDir),
		},
		TimeoutSeconds: s.TimeoutSeconds,
		RunAs:          strings.TrimSpace(s.RunAs),
	}
	if !s.Workspace.Repo.empty() {
		out.Workspace.Repo = &task.RepoSpec{
			Kind:     strings.TrimSpace(s.Workspace.Repo.Kind),
			Remote:   strings.TrimSpace(s.Workspace.Repo.Remote),
			CacheKey: strings.TrimSpace(s.Workspace.Repo.CacheKey),
			Commit:   strings.TrimSpace(s.Workspace.Repo.Commit),
			Subdir:   strings.TrimSpace(s.Workspace.Repo.Subdir),
		}
	}
	return out
}
