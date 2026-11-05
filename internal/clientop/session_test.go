package clientop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type sessionFakeAmuxd struct {
	pb.AmuxdClient
	runReq         *pb.AmuxdRunRequest
	runResp        *pb.RunResponse
	submitReq      *pb.AmuxdSubmitRequest
	submitResp     *pb.SubmitResponse
	interruptReq   *pb.AmuxdInterruptRequest
	sleepReq       *pb.AmuxdSleepRequest
	resumeReq      *pb.AmuxdResumeRequest
	killReq        *pb.AmuxdKillRequest
	restartReq     *pb.AmuxdRestartRequest
	queueListReq   *pb.AmuxdQueueListRequest
	queueListResp  *pb.QueueListResponse
	queueClearReq  *pb.AmuxdQueueClearRequest
	queueClearResp *pb.QueueClearResponse
	queueCancelReq *pb.AmuxdQueueCancelRequest
	queueCancelOk  bool
	pruneReq       *pb.AmuxdPruneRequest
	pruneResp      *pb.PruneResponse
	listCalled     bool
	listResp       *pb.ListTargetsResponse
}

func (f *sessionFakeAmuxd) Run(_ context.Context, in *pb.AmuxdRunRequest, _ ...grpc.CallOption) (*pb.RunResponse, error) {
	f.runReq = in
	return f.runResp, nil
}
func (f *sessionFakeAmuxd) Submit(_ context.Context, in *pb.AmuxdSubmitRequest, _ ...grpc.CallOption) (*pb.SubmitResponse, error) {
	f.submitReq = in
	return f.submitResp, nil
}
func (f *sessionFakeAmuxd) Interrupt(_ context.Context, in *pb.AmuxdInterruptRequest, _ ...grpc.CallOption) (*pb.InterruptResponse, error) {
	f.interruptReq = in
	return &pb.InterruptResponse{}, nil
}
func (f *sessionFakeAmuxd) Sleep(_ context.Context, in *pb.AmuxdSleepRequest, _ ...grpc.CallOption) (*pb.SleepResponse, error) {
	f.sleepReq = in
	return &pb.SleepResponse{}, nil
}
func (f *sessionFakeAmuxd) Resume(_ context.Context, in *pb.AmuxdResumeRequest, _ ...grpc.CallOption) (*pb.RunResponse, error) {
	f.resumeReq = in
	return &pb.RunResponse{}, nil
}
func (f *sessionFakeAmuxd) Kill(_ context.Context, in *pb.AmuxdKillRequest, _ ...grpc.CallOption) (*pb.KillResponse, error) {
	f.killReq = in
	return &pb.KillResponse{}, nil
}
func (f *sessionFakeAmuxd) Restart(_ context.Context, in *pb.AmuxdRestartRequest, _ ...grpc.CallOption) (*pb.RunResponse, error) {
	f.restartReq = in
	return &pb.RunResponse{}, nil
}
func (f *sessionFakeAmuxd) ListTargets(context.Context, *pb.ListTargetsRequest, ...grpc.CallOption) (*pb.ListTargetsResponse, error) {
	f.listCalled = true
	return f.listResp, nil
}
func (f *sessionFakeAmuxd) QueueList(_ context.Context, in *pb.AmuxdQueueListRequest, _ ...grpc.CallOption) (*pb.QueueListResponse, error) {
	f.queueListReq = in
	if f.queueListResp != nil {
		return f.queueListResp, nil
	}
	return &pb.QueueListResponse{}, nil
}
func (f *sessionFakeAmuxd) QueueClear(_ context.Context, in *pb.AmuxdQueueClearRequest, _ ...grpc.CallOption) (*pb.QueueClearResponse, error) {
	f.queueClearReq = in
	if f.queueClearResp != nil {
		return f.queueClearResp, nil
	}
	return &pb.QueueClearResponse{}, nil
}
func (f *sessionFakeAmuxd) QueueCancel(_ context.Context, in *pb.AmuxdQueueCancelRequest, _ ...grpc.CallOption) (*pb.QueueCancelResponse, error) {
	f.queueCancelReq = in
	return &pb.QueueCancelResponse{Ok: f.queueCancelOk}, nil
}
func (f *sessionFakeAmuxd) Prune(_ context.Context, in *pb.AmuxdPruneRequest, _ ...grpc.CallOption) (*pb.PruneResponse, error) {
	f.pruneReq = in
	if f.pruneResp != nil {
		return f.pruneResp, nil
	}
	return &pb.PruneResponse{}, nil
}

func TestSessionRunSendsRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{runResp: &pb.RunResponse{Session: &pb.Session{Id: "s1"}}}
	resp, err := SessionRun(context.Background(), fake, SessionRunRequest{
		Profile:      "builtin/codex",
		Name:         "fix-bug",
		Target:       "dev",
		Cwd:          "/repo",
		Repo:         "example-team/amux",
		RepoRef:      "main",
		RunAs:        "ci",
		AgentType:    "codex",
		AgentProfile: "builtin/codex",
		ProfileSpec:  &pb.AgentProfileSpec{Ref: "builtin/codex", Type: "codex"},
		ExtraArgs:    []string{"--model", "gpt-5.4"},
		Env:          []string{"A=B"},
	})
	if err != nil {
		t.Fatalf("SessionRun: %v", err)
	}
	if resp == nil || resp.Session == nil || resp.Session.Id != "s1" {
		t.Fatalf("resp = %+v", resp)
	}
	got := fake.runReq
	if got == nil {
		t.Fatal("hub.Run was not called")
	}
	if got.TargetId != "dev" || got.AgentProfile != "builtin/codex" || got.Name != "fix-bug" ||
		got.Cwd != "/repo" || got.Repo != "example-team/amux" || got.RepoRef != "main" || got.RunAs != "ci" {
		t.Fatalf("request = %+v", got)
	}
	if got.AgentType != "codex" || got.ProfileSpec == nil || got.ProfileSpec.Ref != "builtin/codex" ||
		len(got.ExtraArgs) != 2 || got.ExtraArgs[1] != "gpt-5.4" || len(got.Env) != 1 || got.Env[0] != "A=B" {
		t.Fatalf("resolved launch = %+v", got)
	}
}

func TestSessionRunResolvesSoleConnectedTarget(t *testing.T) {
	fake := &sessionFakeAmuxd{
		listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{{Id: "n37", Name: "devbox"}}},
		runResp:  &pb.RunResponse{Session: &pb.Session{Id: "s1"}},
	}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{
		Profile:      "builtin/codex",
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "builtin/codex",
	})
	if err != nil {
		t.Fatalf("SessionRun: %v", err)
	}
	if !fake.listCalled {
		t.Fatal("SessionRun did not inspect connected targets")
	}
	if fake.runReq == nil || fake.runReq.TargetId != "n37" {
		t.Fatalf("run request = %+v, want sole connected target n37", fake.runReq)
	}
}

func TestSessionRunRequiresExplicitTargetWhenMultipleAreConnected(t *testing.T) {
	fake := &sessionFakeAmuxd{listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{
		{Id: "local"},
		{Id: "devbox"},
	}}}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{
		Profile:      "builtin/codex",
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "builtin/codex",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required when 2 targets are connected") {
		t.Fatalf("err = %v, want explicit-target requirement", err)
	}
	if fake.runReq != nil {
		t.Fatal("ambiguous session run was sent to the hub")
	}
}

func TestSessionRunRejectsMissingConnectedTarget(t *testing.T) {
	fake := &sessionFakeAmuxd{listResp: &pb.ListTargetsResponse{}}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{
		Profile:      "builtin/codex",
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "builtin/codex",
	})
	if err == nil || !strings.Contains(err.Error(), "no connected targets") {
		t.Fatalf("err = %v, want no-connected-target error", err)
	}
	if fake.runReq != nil {
		t.Fatal("session run without a connected target was sent to the hub")
	}
}

func TestSessionRunRejectsUnresolvedProfile(t *testing.T) {
	fake := &sessionFakeAmuxd{}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{Profile: "builtin/codex", Name: "fix-bug"})
	if err == nil || !strings.Contains(err.Error(), "launch is unresolved") {
		t.Fatalf("err = %v, want unresolved launch error", err)
	}
	if fake.runReq != nil {
		t.Fatal("unresolved request was sent to the hub")
	}
}

func TestSessionRunRejectsEmptyName(t *testing.T) {
	fake := &sessionFakeAmuxd{}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{Profile: "builtin/codex", Name: "  "})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldSessionName {
		t.Fatalf("err = %v, want FieldError on name", err)
	}
	if fake.runReq != nil {
		t.Fatal("invalid request was sent to the hub")
	}
}

func TestSessionRunRejectsEmptyProfile(t *testing.T) {
	fake := &sessionFakeAmuxd{}
	_, err := SessionRun(context.Background(), fake, SessionRunRequest{Name: "fix-bug", Profile: " "})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldAgent {
		t.Fatalf("err = %v, want FieldError on agent", err)
	}
	if fake.runReq != nil {
		t.Fatal("invalid request was sent to the hub")
	}
}

func TestSessionSendSendsRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{submitResp: &pb.SubmitResponse{}}
	_, err := SessionSend(context.Background(), fake, SessionRef{Name: "fix-bug", Target: "dev"}, "hello")
	if err != nil {
		t.Fatalf("SessionSend: %v", err)
	}
	got := fake.submitReq
	if got == nil || got.TargetId != "dev" || got.Name != "fix-bug" || got.Text != "hello" {
		t.Fatalf("request = %+v", got)
	}
}

func TestSessionStopSendsKillRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{}
	_, err := SessionStop(context.Background(), fake, SessionRef{Name: "fix-bug", Target: "dev"})
	if err != nil {
		t.Fatalf("SessionStop: %v", err)
	}
	got := fake.killReq
	if got == nil || got.TargetId != "dev" || got.Name != "fix-bug" {
		t.Fatalf("request = %+v", got)
	}
}

func TestSessionLifecycleSendsRequests(t *testing.T) {
	ref := SessionRef{Name: "fix-bug", Target: "dev"}
	fake := &sessionFakeAmuxd{}
	if _, err := SessionInterrupt(context.Background(), fake, ref); err != nil {
		t.Fatalf("SessionInterrupt: %v", err)
	}
	if _, err := SessionSleep(context.Background(), fake, ref); err != nil {
		t.Fatalf("SessionSleep: %v", err)
	}
	if _, err := SessionResume(context.Background(), fake, ref); err != nil {
		t.Fatalf("SessionResume: %v", err)
	}
	if _, err := SessionRestart(context.Background(), fake, ref); err != nil {
		t.Fatalf("SessionRestart: %v", err)
	}
	if got := fake.interruptReq; got == nil || got.TargetId != "dev" || got.Name != "fix-bug" {
		t.Fatalf("interrupt request = %+v", got)
	}
	if got := fake.sleepReq; got == nil || got.TargetId != "dev" || got.Name != "fix-bug" {
		t.Fatalf("sleep request = %+v", got)
	}
	if got := fake.resumeReq; got == nil || got.TargetId != "dev" || got.Name != "fix-bug" {
		t.Fatalf("resume request = %+v", got)
	}
	if got := fake.restartReq; got == nil || got.TargetId != "dev" || got.Name != "fix-bug" {
		t.Fatalf("restart request = %+v", got)
	}
}

func TestSessionListCallsListTargets(t *testing.T) {
	want := &pb.ListTargetsResponse{Targets: []*pb.Target{{Id: "local"}}}
	fake := &sessionFakeAmuxd{listResp: want}
	got, err := SessionList(context.Background(), fake)
	if err != nil {
		t.Fatalf("SessionList: %v", err)
	}
	if !fake.listCalled {
		t.Fatal("ListTargets was not called")
	}
	if got != want {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
}

func TestSessionQueueListSendsRequest(t *testing.T) {
	want := &pb.QueueListResponse{Items: []*pb.QueueItem{{Id: "q1", Text: "hello"}}}
	fake := &sessionFakeAmuxd{queueListResp: want}
	got, err := SessionQueueList(context.Background(), fake, SessionRef{Name: "fix-bug", Target: "dev"})
	if err != nil {
		t.Fatalf("SessionQueueList: %v", err)
	}
	if got != want {
		t.Fatalf("resp = %+v, want %+v", got, want)
	}
	req := fake.queueListReq
	if req == nil || req.TargetId != "dev" || req.Name != "fix-bug" {
		t.Fatalf("request = %+v", req)
	}
}

func TestSessionQueueClearSendsRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{queueClearResp: &pb.QueueClearResponse{Cleared: 3}}
	resp, err := SessionQueueClear(context.Background(), fake, SessionRef{Name: "fix-bug", Target: "dev"})
	if err != nil {
		t.Fatalf("SessionQueueClear: %v", err)
	}
	if resp.GetCleared() != 3 {
		t.Fatalf("cleared = %d, want 3", resp.GetCleared())
	}
	req := fake.queueClearReq
	if req == nil || req.TargetId != "dev" || req.Name != "fix-bug" {
		t.Fatalf("request = %+v", req)
	}
}

func TestSessionQueueCancelSendsRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{queueCancelOk: true}
	resp, err := SessionQueueCancel(context.Background(), fake, SessionRef{Name: "fix-bug", Target: "dev"}, "q1")
	if err != nil {
		t.Fatalf("SessionQueueCancel: %v", err)
	}
	if !resp.GetOk() {
		t.Fatal("ok = false, want true")
	}
	req := fake.queueCancelReq
	if req == nil || req.TargetId != "dev" || req.Name != "fix-bug" || req.Id != "q1" {
		t.Fatalf("request = %+v", req)
	}
}

func TestSessionPruneSendsRequest(t *testing.T) {
	fake := &sessionFakeAmuxd{pruneResp: &pb.PruneResponse{Count: 2, Names: []string{"a", "b"}}}
	resp, err := SessionPrune(context.Background(), fake, "dev")
	if err != nil {
		t.Fatalf("SessionPrune: %v", err)
	}
	if resp.GetCount() != 2 {
		t.Fatalf("count = %d, want 2", resp.GetCount())
	}
	req := fake.pruneReq
	if req == nil || req.TargetId != "dev" {
		t.Fatalf("request = %+v", req)
	}
}

func TestBuildSetModelReplacement(t *testing.T) {
	sess := &pb.Session{
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "builtin/codex@abc123",
		ExtraArgs:    []string{"--model", "old-model", "--verbose"},
		Env:          []string{"A=B"},
	}
	replacement, err := BuildSetModelReplacement(sess, "new-model")
	if err != nil {
		t.Fatalf("BuildSetModelReplacement: %v", err)
	}
	if replacement.AgentType != "codex" || replacement.AgentProfile != "builtin/codex@abc123" {
		t.Fatalf("replacement identity = %+v", replacement)
	}
	wantArgs := []string{"--model", "new-model", "--verbose"}
	if !reflect.DeepEqual(replacement.ExtraArgs, wantArgs) {
		t.Fatalf("replacement args = %v, want %v", replacement.ExtraArgs, wantArgs)
	}
	if !reflect.DeepEqual(replacement.Env, []string{"A=B"}) {
		t.Fatalf("replacement env = %v", replacement.Env)
	}
	if replacement.Name != "" || replacement.Cwd != "" {
		t.Fatalf("amuxlet-owned fields must stay unset: %+v", replacement)
	}
}

func TestBuildSetModelReplacementRejects(t *testing.T) {
	base := &pb.Session{Name: "work", AgentType: "codex", ExtraArgs: []string{"--model", "old"}}
	if _, err := BuildSetModelReplacement(base, "  "); err == nil {
		t.Fatal("empty model accepted")
	}
	ownerTask := &pb.Session{Name: "work", AgentType: "codex", OwnerTaskId: "task-1"}
	if _, err := BuildSetModelReplacement(ownerTask, "new"); err == nil || !strings.Contains(err.Error(), "task or agent instance") {
		t.Fatalf("owner task err = %v", err)
	}
	ownerInstance := &pb.Session{Name: "work", AgentType: "codex", AgentInstanceId: "inst-1"}
	if _, err := BuildSetModelReplacement(ownerInstance, "new"); err == nil || !strings.Contains(err.Error(), "task or agent instance") {
		t.Fatalf("owner instance err = %v", err)
	}
}

func TestBuildSetModelReplacementAllowsProviderBackedSession(t *testing.T) {
	// Provider-backed sessions are supported: amuxlet re-projects the switch
	// through the profile registry, so the patched args only carry the
	// requested model.
	sess := &pb.Session{
		Name: "work", AgentType: "codex", AgentProfile: "builtin/codex",
		ExtraArgs: []string{"--model", "default_model", "-c", `model_provider="custom"`},
	}
	replacement, err := BuildSetModelReplacement(sess, "custom/example-model")
	if err != nil {
		t.Fatalf("BuildSetModelReplacement: %v", err)
	}
	wantArgs := []string{"--model", "custom/example-model", "-c", `model_provider="custom"`}
	if !reflect.DeepEqual(replacement.ExtraArgs, wantArgs) {
		t.Fatalf("replacement args = %v, want %v", replacement.ExtraArgs, wantArgs)
	}
}

func TestSessionSetModelSendsRestartWithReplacement(t *testing.T) {
	fake := &sessionFakeAmuxd{
		listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{{
			Id: "dev",
			Sessions: []*pb.Session{{
				Name: "work", AgentType: "codex", AgentProfile: "builtin/codex@abc123",
				ExtraArgs: []string{"--model", "old-model"}, Env: []string{"A=B"},
			}},
		}}},
	}
	if _, err := SessionSetModel(context.Background(), fake, SessionRef{Name: "work", Target: "dev"}, "new-model"); err != nil {
		t.Fatalf("SessionSetModel: %v", err)
	}
	req := fake.restartReq
	if req == nil {
		t.Fatal("hub.Restart was not called")
	}
	if req.TargetId != "dev" || req.Name != "work" || !req.PreserveHistory || req.RefreshProfile {
		t.Fatalf("restart request = %+v", req)
	}
	replacement := req.Replacement
	if replacement == nil {
		t.Fatal("restart request carries no replacement")
	}
	if replacement.AgentType != "codex" || replacement.AgentProfile != "builtin/codex@abc123" {
		t.Fatalf("replacement identity = %+v", replacement)
	}
	wantArgs := []string{"--model", "new-model"}
	if !reflect.DeepEqual(replacement.ExtraArgs, wantArgs) {
		t.Fatalf("replacement args = %v, want %v", replacement.ExtraArgs, wantArgs)
	}
}

func TestSessionSetModelResolvesTargetFromMatch(t *testing.T) {
	fake := &sessionFakeAmuxd{
		listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{{
			Id: "n37", Name: "dev-box",
			Sessions: []*pb.Session{{
				Name: "work", AgentType: "codex", ExtraArgs: []string{"--model", "old"},
			}},
		}}},
	}
	// No Target in the ref: the hub must still receive the matched target's id,
	// otherwise Restart fails with target "" not connected.
	if _, err := SessionSetModel(context.Background(), fake, SessionRef{Name: "work"}, "new"); err != nil {
		t.Fatalf("SessionSetModel: %v", err)
	}
	if fake.restartReq == nil || fake.restartReq.TargetId != "n37" {
		t.Fatalf("restart request = %+v, want target id n37", fake.restartReq)
	}
}

func TestSessionSetModelRejectsUnknownSession(t *testing.T) {
	fake := &sessionFakeAmuxd{listResp: &pb.ListTargetsResponse{}}
	_, err := SessionSetModel(context.Background(), fake, SessionRef{Name: "missing"}, "new-model")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if fake.restartReq != nil {
		t.Fatal("restart was sent for a missing session")
	}
}

func TestSessionSetModelRejectsOwnerHeldSession(t *testing.T) {
	fake := &sessionFakeAmuxd{
		listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{{
			Id:       "dev",
			Sessions: []*pb.Session{{Name: "work", AgentType: "codex", OwnerTaskId: "task-1"}},
		}}},
	}
	_, err := SessionSetModel(context.Background(), fake, SessionRef{Name: "work", Target: "dev"}, "new-model")
	if err == nil || !strings.Contains(err.Error(), "task or agent instance") {
		t.Fatalf("err = %v, want owner-held rejection", err)
	}
	if fake.restartReq != nil {
		t.Fatal("restart was sent for an owner-held session")
	}
}

func TestResolveSessionRefRequiresUniqueVisibleMatch(t *testing.T) {
	fake := &sessionFakeAmuxd{listResp: &pb.ListTargetsResponse{Targets: []*pb.Target{
		{Id: "a", Name: "alpha", Sessions: []*pb.Session{{Id: "s1", Name: "work"}}},
		{Id: "b", Name: "beta", Sessions: []*pb.Session{{Id: "s2", Name: "work"}}},
	}}}
	if _, err := ResolveSessionRef(context.Background(), fake, SessionRef{Name: "work"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous err=%v", err)
	}
	for _, ref := range []SessionRef{{Name: "s2"}, {Name: "work", Target: "beta"}} {
		got, err := ResolveSessionRef(context.Background(), fake, ref)
		if err != nil || got.Name != "work" || got.Target != "b" {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if _, err := ResolveSessionRef(context.Background(), fake, SessionRef{Name: "missing"}); err == nil {
		t.Fatal("missing session accepted")
	}
}
