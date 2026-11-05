package clientop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// SessionRef identifies a session, optionally on a named target.
type SessionRef struct {
	Name   string `json:"name" desc:"Session 名称或 ID，也可带 <target>/<name> 目标前缀"`
	Target string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
}

// SessionList returns the targets and sessions the caller may see. The hub
// applies principal scoping while fleet isolation is active; delegated actors
// can see same-principal sibling sessions, while non-isolated fleets are
// shared. This function does not filter further.
func SessionList(ctx context.Context, hub pb.AmuxdClient) (*pb.ListTargetsResponse, error) {
	return hub.ListTargets(ctx, &pb.ListTargetsRequest{})
}

// SessionRunRequest creates a session.
type SessionRunRequest struct {
	Runtime   string   `json:"runtime,omitempty"`
	Model     string   `json:"model,omitempty"`
	AgentArgs []string `json:"args,omitempty"`
	Profile   string   `json:"profile" desc:"Agent Profile 引用（例如 builtin/codex）或旧版 Agent Type"`
	Name      string   `json:"name" desc:"Session 名称（必填）"`
	Target    string   `json:"target,omitempty" desc:"目标 runtime ID；留空时仅在唯一连接 target 时自动选择"`
	Cwd       string   `json:"cwd,omitempty"`
	Repo      string   `json:"repo,omitempty"`
	RepoRef   string   `json:"ref,omitempty"`
	RunAs     string   `json:"run_as,omitempty"`

	// The command surface resolves Profile into this immutable launch snapshot
	// before calling SessionRun. Keeping registry access out of clientop lets
	// CLI use its local registry while isolated MCP uses the hub control plane.
	AgentType    string               `json:"-"`
	AgentProfile string               `json:"-"`
	ProfileSpec  *pb.AgentProfileSpec `json:"-"`
	ExtraArgs    []string             `json:"-"`
	Env          []string             `json:"-"`
}

// SessionRun creates a session and returns it.
func SessionRun(ctx context.Context, hub pb.AmuxdClient, req SessionRunRequest) (*pb.RunResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, RequiredError(FieldSessionName)
	}
	if strings.TrimSpace(req.Profile) == "" {
		return nil, RequiredError(FieldAgent)
	}
	if strings.TrimSpace(req.AgentType) == "" {
		return nil, errors.New("agent profile launch is unresolved")
	}
	var err error
	req.Target, err = ResolveSessionRunTarget(ctx, hub, req.Target)
	if err != nil {
		return nil, err
	}
	return hub.Run(ctx, &pb.AmuxdRunRequest{
		TargetId:     req.Target,
		AgentProfile: req.AgentProfile,
		ProfileSpec:  req.ProfileSpec,
		AgentType:    req.AgentType,
		Name:         req.Name,
		ExtraArgs:    req.ExtraArgs,
		Env:          req.Env,
		Cwd:          req.Cwd,
		Repo:         req.Repo,
		RepoRef:      req.RepoRef,
		RunAs:        req.RunAs,
	})
}

// ResolveSessionRunTarget returns an explicit runtime id for session creation.
// An omitted target is only safe to infer when the hub exposes exactly one
// connected target.
func ResolveSessionRunTarget(ctx context.Context, hub pb.AmuxdClient, target string) (string, error) {
	if target = strings.TrimSpace(target); target != "" {
		return target, nil
	}
	resp, err := hub.ListTargets(ctx, &pb.ListTargetsRequest{})
	if err != nil {
		return "", fmt.Errorf("resolve session target: %w", err)
	}
	switch len(resp.GetTargets()) {
	case 0:
		return "", errors.New("no connected targets")
	case 1:
	default:
		return "", fmt.Errorf("target is required when %d targets are connected", len(resp.GetTargets()))
	}
	target = strings.TrimSpace(resp.GetTargets()[0].GetId())
	if target == "" {
		return "", errors.New("connected target has no id")
	}
	return target, nil
}

// SessionSend queues a prompt into a session.
func SessionSend(ctx context.Context, hub pb.AmuxdClient, ref SessionRef, text string) (*pb.SubmitResponse, error) {
	return hub.Submit(ctx, &pb.AmuxdSubmitRequest{TargetId: ref.Target, Name: ref.Name, Text: text})
}

// SessionInterrupt interrupts a session's current turn.
func SessionInterrupt(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.InterruptResponse, error) {
	return hub.Interrupt(ctx, &pb.AmuxdInterruptRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionSleep puts a session to sleep.
func SessionSleep(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.SleepResponse, error) {
	return hub.Sleep(ctx, &pb.AmuxdSleepRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionResume wakes a sleeping session.
func SessionResume(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.RunResponse, error) {
	return hub.Resume(ctx, &pb.AmuxdResumeRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionStop archives a session.
func SessionStop(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.KillResponse, error) {
	return hub.Kill(ctx, &pb.AmuxdKillRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionRestart restarts a session.
func SessionRestart(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.RunResponse, error) {
	return hub.Restart(ctx, &pb.AmuxdRestartRequest{TargetId: ref.Target, Name: ref.Name})
}

// BuildSetModelReplacement builds the replacement execution that switches a
// session to model while retaining its amux session identity, workspace, and
// conversation history. Name/Cwd/Workspace are forced from the frozen launch
// snapshot by amuxlet, so they are left unset here. The patched args are only
// the carrier for the requested model: amuxlet re-projects the switch through
// the Agent Profile registry, so provider-backed models (for example
// custom/example-model) resolve to their full provider launch configuration there.
func BuildSetModelReplacement(sess *pb.Session, model string) (*pb.RunRequest, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("model is required")
	}
	if sess == nil {
		return nil, errors.New("session is required")
	}
	if sess.GetOwnerTaskId() != "" || sess.GetAgentInstanceId() != "" {
		return nil, fmt.Errorf("session %q is owned by a task or agent instance; set-model is unsupported for owner-held sessions", sess.GetName())
	}
	return &pb.RunRequest{
		AgentType:    sess.GetAgentType(),
		AgentProfile: sess.GetAgentProfile(),
		ExtraArgs:    agentprofile.ReplaceArgsModel(sess.GetAgentType(), sess.GetExtraArgs(), model),
		Env:          append([]string(nil), sess.GetEnv()...),
	}, nil
}

// SessionSetModel switches the session identified by ref to model, preserving
// the conversation history through the replacement-restart mechanism.
func SessionSetModel(ctx context.Context, hub pb.AmuxdClient, ref SessionRef, model string) (*pb.RunResponse, error) {
	sess, targetID, err := sessionByRef(ctx, hub, ref)
	if err != nil {
		return nil, err
	}
	replacement, err := BuildSetModelReplacement(sess, model)
	if err != nil {
		return nil, err
	}
	return hub.Restart(ctx, &pb.AmuxdRestartRequest{
		TargetId: targetID, Name: sess.GetName(),
		Replacement: replacement, PreserveHistory: true,
	})
}

// sessionByRef resolves a single session through the hub listing. The hub
// already scopes the listing to the caller, so a match here is visible to the
// caller. It also returns the matched target's id: callers routing follow-up
// RPCs need a concrete target even when ref only named the session.
func sessionByRef(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.Session, string, error) {
	want := strings.TrimSpace(ref.Name)
	if want == "" {
		return nil, "", RequiredError(FieldSessionName)
	}
	resp, err := hub.ListTargets(ctx, &pb.ListTargetsRequest{})
	if err != nil {
		return nil, "", err
	}
	target := strings.TrimSpace(ref.Target)
	var found *pb.Session
	var targetID string
	for _, t := range resp.GetTargets() {
		if target != "" && t.GetId() != target && t.GetName() != target {
			continue
		}
		for _, sess := range t.GetSessions() {
			if sess.GetName() != want && sess.GetId() != want {
				continue
			}
			if found != nil {
				return nil, "", fmt.Errorf("session %q is ambiguous; specify a target", want)
			}
			found, targetID = sess, t.GetId()
		}
	}
	if found != nil {
		return found, targetID, nil
	}
	return nil, "", fmt.Errorf("session %q not found", want)
}

// ResolveSessionRef locates one caller-visible session without guessing a target.
func ResolveSessionRef(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (SessionRef, error) {
	sess, target, err := sessionByRef(ctx, hub, ref)
	if err != nil {
		return SessionRef{}, err
	}
	return SessionRef{Name: sess.GetName(), Target: target}, nil
}

// SessionQueueList lists a session's queued inputs.
func SessionQueueList(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.QueueListResponse, error) {
	return hub.QueueList(ctx, &pb.AmuxdQueueListRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionQueueClear clears a session's queued inputs.
func SessionQueueClear(ctx context.Context, hub pb.AmuxdClient, ref SessionRef) (*pb.QueueClearResponse, error) {
	return hub.QueueClear(ctx, &pb.AmuxdQueueClearRequest{TargetId: ref.Target, Name: ref.Name})
}

// SessionQueueCancel cancels one queued input.
func SessionQueueCancel(ctx context.Context, hub pb.AmuxdClient, ref SessionRef, queueID string) (*pb.QueueCancelResponse, error) {
	return hub.QueueCancel(ctx, &pb.AmuxdQueueCancelRequest{TargetId: ref.Target, Name: ref.Name, Id: queueID})
}

// SessionPrune removes archived sessions from the target. An empty target uses
// the hub default.
func SessionPrune(ctx context.Context, hub pb.AmuxdClient, target string) (*pb.PruneResponse, error) {
	return hub.Prune(ctx, &pb.AmuxdPruneRequest{TargetId: target})
}
