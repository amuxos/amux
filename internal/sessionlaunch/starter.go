// Package sessionlaunch starts ordinary sessions through the amuxd hub.
// It owns only Run request construction and response normalization; callers
// retain trigger-specific policy, binding, submission, and presentation.
package sessionlaunch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Starter creates an ordinary session and returns its resolved identity.
type Starter interface {
	Start(ctx context.Context, req StartRequest) (SessionRef, error)
}

// StartRequest contains the launch inputs already resolved by the caller.
type StartRequest struct {
	TargetID           string
	Name               string
	Launch             agentprofile.LaunchProjection
	Workspace          WorkspaceRequest
	IdleTimeoutSeconds *int64
	// Execution identity for identified (per-principal isolated) launches. The
	// zero value means an anonymous legacy-host launch. These map onto the
	// RunRequest execution_identity / launch_id / agent_actor_token fields and
	// are opaque to this package — only amuxd knows how they were minted.
	ExecutionIdentity        *pb.ExecutionIdentity
	LaunchID                 string
	AgentActorToken          string
	AgentActorTokenExpiresAt int64
	// SystemInstructions are session-level out-of-band instructions applied
	// from the first provider launch (RunRequest.system_instructions).
	SystemInstructions string
}

// WorkspaceRequest describes the workspace fields forwarded to Run.
type WorkspaceRequest struct {
	Cwd     string
	Repo    string
	RepoRef string
	Source  string
	Scope   string
}

// SessionRef is the normalized identity returned by Run.
type SessionRef struct {
	TargetID       string
	Name           string
	AgentProfile   string
	AgentType      string
	EffectiveModel string
	Cwd            string
	State          string
	// Warnings carries non-fatal launch findings (e.g. an expiring provider
	// credential) so callers can surface them; the session still started.
	Warnings []string
}

// Router is the transport seam needed to start a session.
type Router interface {
	Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
}

type starter struct {
	router Router
}

// New creates a Starter backed by router.
func New(router Router) Starter {
	return &starter{router: router}
}

// ErrorKind classifies a failed Start without hiding its cause.
type ErrorKind string

const (
	ErrorInvalidRequest  ErrorKind = "invalid_request"
	ErrorRoute           ErrorKind = "route"
	ErrorInvalidResponse ErrorKind = "invalid_response"
)

// StartError reports which Start phase failed and preserves the underlying error.
type StartError struct {
	Kind ErrorKind
	Err  error
}

func (e *StartError) Error() string {
	if e == nil {
		return "session start failed"
	}
	return fmt.Sprintf("session start %s: %v", e.Kind, e.Err)
}

func (e *StartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (s *starter) Start(ctx context.Context, req StartRequest) (SessionRef, error) {
	if strings.TrimSpace(req.TargetID) == "" {
		return SessionRef{}, startError(ErrorInvalidRequest, "target id is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return SessionRef{}, startError(ErrorInvalidRequest, "session name is required")
	}
	if strings.TrimSpace(req.Launch.AgentType) == "" {
		return SessionRef{}, startError(ErrorInvalidRequest, "agent type is required")
	}
	if s == nil || s.router == nil {
		return SessionRef{}, startError(ErrorInvalidRequest, "router is required")
	}

	runReq := &pb.RunRequest{
		AgentProfile:       req.Launch.AgentProfile,
		ProfileSpec:        req.Launch.ProfileSpec,
		AgentType:          req.Launch.AgentType,
		Name:               req.Name,
		Env:                req.Launch.Env,
		ExtraArgs:          req.Launch.ExtraArgs,
		Cwd:                req.Workspace.Cwd,
		Repo:               req.Workspace.Repo,
		RepoRef:            req.Workspace.RepoRef,
		WorkspaceSource:    req.Workspace.Source,
		WorkspaceScope:     req.Workspace.Scope,
		SystemInstructions: req.SystemInstructions,
	}
	if runReq.AgentType == "codex" {
		runReq.AgentExecutable = config.GetCodexExecutable()
	}
	if req.ExecutionIdentity != nil || req.LaunchID != "" || req.AgentActorToken != "" {
		runReq.ExecutionIdentity = req.ExecutionIdentity
		runReq.LaunchId = req.LaunchID
		runReq.AgentActorToken = req.AgentActorToken
		runReq.AgentActorTokenExpiresAt = req.AgentActorTokenExpiresAt
	}
	if req.IdleTimeoutSeconds != nil {
		runReq.IdleTimeoutSeconds = *req.IdleTimeoutSeconds
		runReq.IdleTimeoutSecondsSet = true
	}

	resp, err := s.router.Route(ctx, req.TargetID, "Run", runReq)
	if err != nil {
		return SessionRef{}, &StartError{Kind: ErrorRoute, Err: err}
	}
	if resp == nil {
		return SessionRef{}, startError(ErrorInvalidResponse, "empty tunnel response")
	}
	var out pb.RunResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return SessionRef{}, &StartError{Kind: ErrorInvalidResponse, Err: fmt.Errorf("parse run response: %w", err)}
	}
	if out.Session == nil {
		return SessionRef{}, startError(ErrorInvalidResponse, "run response session is required")
	}
	if strings.TrimSpace(out.Session.Name) == "" {
		return SessionRef{}, startError(ErrorInvalidResponse, "run response session name is required")
	}

	return SessionRef{
		TargetID:       req.TargetID,
		Name:           out.Session.Name,
		AgentProfile:   fallback(out.Session.AgentProfile, req.Launch.AgentProfile),
		AgentType:      fallback(out.Session.AgentType, req.Launch.AgentType),
		EffectiveModel: fallback(out.Session.EffectiveModel, req.Launch.Model),
		Cwd:            fallback(out.Session.Cwd, req.Workspace.Cwd),
		State:          out.Session.State,
		Warnings:       out.Warnings,
	}, nil
}

func startError(kind ErrorKind, message string) error {
	return &StartError{Kind: kind, Err: fmt.Errorf("%s", message)}
}

func fallback(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}
