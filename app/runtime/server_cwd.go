package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) ChangeCwd(ctx context.Context, req *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	s.cleanupLaunchMu.RLock()
	defer s.cleanupLaunchMu.RUnlock()
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	snap := sess.Snapshot()
	if snap.State == session.StateArchived {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is archived; cannot change cwd", snap.Name)
	}
	if snap.State != session.StateRunning {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is %s; resume it before changing cwd", snap.Name, snap.State)
	}
	a, err := adapter.Get(snap.AgentType)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	if a.Capabilities().CwdSwitch != adapter.CwdSwitchResume {
		return nil, status.Errorf(codes.FailedPrecondition, "cwd switching unsupported for agent %s", snap.AgentType)
	}
	if strings.TrimSpace(snap.AgentSessionID) == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q has no provider conversation id; cannot preserve conversation while changing cwd", snap.Name)
	}
	if expected := strings.TrimSpace(req.ExpectedCwd); expected != "" && filepath.Clean(expected) != filepath.Clean(snap.Cwd) {
		return nil, status.Errorf(codes.Aborted, "session cwd changed from expected %q to %q", expected, snap.Cwd)
	}
	newCwd, err := resolveChangedSessionCwd(snap.Cwd, req.Cwd)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cwd: %v", err)
	}
	if filepath.Clean(newCwd) == filepath.Clean(snap.Cwd) {
		return &pb.ChangeCwdResponse{Session: toProtoSession(sess), PreviousCwd: snap.Cwd, EffectiveCwd: snap.Cwd}, nil
	}
	if req.DeferUntilIdle {
		_, err := sess.SubmitLifecycleAsync("system", "switch workspace to "+newCwd, "cwd", func() error {
			return s.runDeferredCwdChange(sess, snap.Cwd, newCwd)
		})
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "schedule cwd switch: %v", err)
		}
		return &pb.ChangeCwdResponse{Session: toProtoSession(sess), PreviousCwd: snap.Cwd, EffectiveCwd: newCwd, Deferred: true}, nil
	}
	interrupted := false
	for {
		busy, pending := sess.QueueState()
		if pending > 0 {
			return nil, status.Errorf(codes.FailedPrecondition, "session %q has %d queued input(s); clear or cancel them before changing cwd", snap.Name, pending)
		}
		if busy {
			if req.Interrupt && !interrupted {
				if err := sess.Interrupt(); err != nil {
					return nil, status.Errorf(codes.FailedPrecondition, "interrupt before cwd switch: %v", err)
				}
				interrupted = true
			}
			if !req.Wait && !req.Interrupt {
				return nil, status.Errorf(codes.FailedPrecondition, "session %q is running a turn; retry after it finishes or use wait/interrupt", snap.Name)
			}
			select {
			case <-ctx.Done():
				return nil, grpcContextError(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		s.relaunchMu.Lock()
		unlockQueue, racedBusy, racedPending := sess.TryLockIdleQueue()
		if unlockQueue == nil {
			s.relaunchMu.Unlock()
			if racedPending > 0 {
				return nil, status.Errorf(codes.FailedPrecondition, "session %q has %d queued input(s); clear or cancel them before changing cwd", snap.Name, racedPending)
			}
			if racedBusy && (req.Wait || req.Interrupt) {
				continue
			}
			return nil, status.Errorf(codes.FailedPrecondition, "session %q started a turn before cwd could change; retry", snap.Name)
		}
		resp, changeErr := s.changeCwdLocked(ctx, sess, a, newCwd)
		unlockQueue()
		s.relaunchMu.Unlock()
		return resp, changeErr
	}
}
func (s *server) runDeferredCwdChange(sess *session.Session, expectedCwd, newCwd string) error {
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	snap := sess.Snapshot()
	if snap.State != session.StateRunning {
		err := fmt.Errorf("session %q is %s; deferred cwd switch cancelled", snap.Name, snap.State)
		s.recordCwdSwitchResult(sess, "", err)
		return err
	}
	if filepath.Clean(snap.Cwd) != filepath.Clean(expectedCwd) {
		err := fmt.Errorf("session cwd changed from expected %q to %q", expectedCwd, snap.Cwd)
		s.recordCwdSwitchResult(sess, "", err)
		return err
	}
	a, err := adapter.Get(snap.AgentType)
	if err != nil {
		s.recordCwdSwitchResult(sess, "", err)
		return err
	}
	if a.Capabilities().CwdSwitch != adapter.CwdSwitchResume {
		err := fmt.Errorf("cwd switching unsupported for agent %s", snap.AgentType)
		s.recordCwdSwitchResult(sess, "", err)
		return err
	}
	if _, err := s.changeCwdLocked(context.Background(), sess, a, newCwd); err != nil {
		s.recordCwdSwitchResult(sess, "", err)
		return err
	}
	s.recordCwdSwitchResult(sess, newCwd, nil)
	return nil
}
func (s *server) recordCwdSwitchResult(sess *session.Session, cwd string, err error) {
	msg := "\r\n[system] workspace changed to " + cwd + " (conversation preserved)\r\n"
	if err != nil {
		msg = "\r\n[system] deferred workspace switch failed: " + err.Error() + "\r\n"
	}
	sess.RecordReplayEvent(session.Event{Kind: session.EventOutput, Data: []byte(msg), Source: "system"})
}
func resolveChangedSessionCwd(current, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(current, requested)
	}
	abs, err := filepath.Abs(requested)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}
func (s *server) changeCwdLocked(ctx context.Context, sess *session.Session, a adapter.Adapter, newCwd string) (*pb.ChangeCwdResponse, error) {
	old := sess.Snapshot()
	actorToken, _ := readSessionActorToken(s.amuxDir, old.ID)
	newLaunchSpec, err := launchSpecWithCwd(old.LaunchSpec, newCwd)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update launch snapshot: %v", err)
	}
	workspaceReq := cwdSwitchRunRequest(old, newCwd)
	if err := materializeProfileWorkspace(workspaceReq, newCwd, "", s.amuxDir); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "profile materialization: %v", err)
	}
	if !s.disableMCPAutoInject && runArgsContainAmuxMCP(old.AgentType, old.ExtraArgs) {
		if _, err := materializeAmuxMCPForRun(old.AgentType, old.ID, s.amuxDir, newCwd, actorToken, memoryprotocol.Binding(old.Env)); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "mcp materialization: %v", err)
		}
	}
	credentialCtx := sessionCredentialMaterializationContext(ctx, old.ExecutionIdentity, actorToken)
	if _, err := s.materializeProviderCredentialEnvFromLaunchSpec(credentialCtx, old.ExecutionIdentity, newLaunchSpec, old.AgentType, old.Env); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "provider credential: %v", err)
	}
	launchEnv := append(os.Environ(), s.launchEnvForIdentity(old.ExecutionIdentity, old.Env)...)
	if t, ok := a.(adapter.WorkspaceTruster); ok && hostWorkspaceTrust(old) {
		if err := t.EnsureWorkspaceTrusted(newCwd, launchEnv); err != nil {
			fmt.Fprintf(os.Stderr, "runtime: ensure new workspace trust: %v\n", err)
		}
	}
	if err := s.mgr.SetCwd(sess, newCwd, newLaunchSpec); err != nil {
		return nil, status.Errorf(codes.Internal, "persist cwd: %v", err)
	}
	if _, err := s.relaunchSessionLockedPreservingQueue(sess, "switching workspace"); err == nil {
		if hostWorkspaceTrust(old) {
			s.cleanupWorkspaceTrustIfUnused(old.AgentType, old.Cwd, old.Env, sess.ID)
		}
		return &pb.ChangeCwdResponse{Session: toProtoSession(sess), PreviousCwd: old.Cwd, EffectiveCwd: newCwd, Changed: true}, nil
	} else {
		switchErr := err
		if restoreErr := s.mgr.SetCwd(sess, old.Cwd, old.LaunchSpec); restoreErr != nil {
			return nil, status.Errorf(codes.Internal, "switch cwd: %v; restore metadata: %v", switchErr, restoreErr)
		}
		if _, restoreErr := s.relaunchSessionLockedPreservingQueue(sess, "restoring workspace"); restoreErr != nil {
			return nil, status.Errorf(codes.Internal, "switch cwd: %v; restore previous workspace: %v", switchErr, restoreErr)
		}
		if hostWorkspaceTrust(old) {
			s.cleanupWorkspaceTrustIfUnused(old.AgentType, newCwd, old.Env, sess.ID)
		}
		return nil, status.Errorf(codes.Internal, "switch cwd: %v; previous workspace %s restored", switchErr, old.Cwd)
	}
}
func launchSpecWithCwd(spec, cwd string) (string, error) {
	decoded := map[string]any{}
	if strings.TrimSpace(spec) != "" {
		if err := json.Unmarshal([]byte(spec), &decoded); err != nil {
			return "", err
		}
	}
	decoded["cwd"] = cwd
	raw, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
func cwdSwitchRunRequest(snap session.Snapshot, cwd string) *pb.RunRequest {
	var saved struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	_ = decodeLaunchSpec(snap.LaunchSpec, &saved)
	return &pb.RunRequest{AgentProfile: snap.AgentProfile, ProfileSpec: saved.ProfileSpec, AgentType: snap.AgentType, Name: snap.Name, Cwd: cwd, WorkspaceSource: snap.WorkspaceSource, WorkspaceScope: snap.WorkspaceScope}
}
