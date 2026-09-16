package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentinstance"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── shared fakes ─────────────────────────────────────────────────────────────

type fakeRouterHub struct {
	targets  []*pb.Target
	routeFn  func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
	streamFn func(targetID, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error
}

func (h *fakeRouterHub) Route(_ context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
	return h.routeFn(targetID, method, payload)
}
func (h *fakeRouterHub) RouteStream(_ context.Context, targetID, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error {
	return h.streamFn(targetID, method, payload, fn)
}
func (h *fakeRouterHub) ListTargets() []*pb.Target { return h.targets }

func targets(ids ...string) []*pb.Target {
	out := make([]*pb.Target, 0, len(ids))
	for _, id := range ids {
		out = append(out, &pb.Target{Id: id, Name: id})
	}
	return out
}

// isolatingTargets is targets with the explicit isolation policy capability,
// which turns on the fleet-level identity boundary (fleetIsolationActive) for
// fixtures that assert ownership narrowing.
func isolatingTargets(ids ...string) []*pb.Target {
	out := targets(ids...)
	for _, t := range out {
		t.Capabilities = &pb.TargetCapabilities{Features: []string{registry.FeaturePrincipalIsolation}}
	}
	return out
}

func TestTargetsIncludeIsolatingRuntimeUsesPolicyIntentAndLegacyProtocol(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []*pb.Target
		want    bool
	}{
		{name: "no capabilities", targets: targets("t1")},
		{name: "unrelated feature", targets: []*pb.Target{{Capabilities: &pb.TargetCapabilities{Features: []string{"task"}}}}},
		{name: "policy feature", targets: isolatingTargets("t1"), want: true},
		{name: "legacy helper protocol", targets: []*pb.Target{{Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := targetsIncludeIsolatingRuntime(tc.targets); got != tc.want {
				t.Fatalf("targetsIncludeIsolatingRuntime() = %v, want %v", got, tc.want)
			}
		})
	}
}

func respJSON(t *testing.T, v interface{}) *pb.TunnelMsg {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &pb.TunnelMsg{Kind: "resp", Payload: raw}
}

type fakeScheduler struct {
	acquireFn func(registry.ScheduleRequest) (*registry.Placement, error)
	committed []registry.Placement
	released  []registry.Placement
}

func (s *fakeScheduler) Acquire(req registry.ScheduleRequest) (*registry.Placement, error) {
	return s.acquireFn(req)
}
func (s *fakeScheduler) Commit(p *registry.Placement) error {
	s.committed = append(s.committed, *p)
	return nil
}
func (s *fakeScheduler) Release(p *registry.Placement) error {
	s.released = append(s.released, *p)
	return nil
}

// ── taskIndex ────────────────────────────────────────────────────────────────

func TestTaskIndexPutGetDelete(t *testing.T) {
	idx := newTaskIndex()
	if _, ok := idx.get("x"); ok {
		t.Fatal("empty index returned a value")
	}
	idx.put("x", taskPlacement{TargetID: "t1", ReservationId: "r1"})
	p, ok := idx.get("x")
	if !ok || p.TargetID != "t1" || p.ReservationId != "r1" {
		t.Fatalf("get = %+v ok=%v", p, ok)
	}
	if snap := idx.snapshot(); len(snap) != 1 {
		t.Fatalf("snapshot len = %d", len(snap))
	}
	idx.delete("x")
	if _, ok := idx.get("x"); ok {
		t.Fatal("delete did not remove entry")
	}
}

// ── router Submit ────────────────────────────────────────────────────────────

func TestRouterSubmitPlacesCommitsAndIndexes(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			// Defaults are resolved before scheduling (no scheduler/runner drift).
			if req.AgentType != "claude" || req.Isolation != "process" || req.WorkspacePolicy != "temp-dir" {
				return nil, fmt.Errorf("unexpected schedule request: %+v", req)
			}
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if targetID != "t1" || method != pb.TaskMethodSubmit {
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-9","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "do it"})
	out, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if string(out.TaskJson) != `{"id":"task-9","status":"pending"}` {
		t.Fatalf("task json = %s", out.TaskJson)
	}
	if len(sched.committed) != 1 || sched.committed[0].ReservationId != "rsv1" {
		t.Fatalf("committed = %+v", sched.committed)
	}
	if len(sched.released) != 0 {
		t.Fatalf("unexpected release: %+v", sched.released)
	}
	p, ok := idx.get("task-9")
	if !ok || p.TargetID != "t1" || p.ReservationId != "rsv1" {
		t.Fatalf("index entry = %+v ok=%v", p, ok)
	}
}

func TestScheduleRequestIncludesResolvedCredentialProfiles(t *testing.T) {
	r := &taskRouter{}
	req, err := r.scheduleRequestFromSpec(context.Background(), task.Spec{
		Agent: "codex",
		Task:  "review",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "repo-review",
			Type: "codex",
			EnvRefs: []string{
				"credential:github:GITHUB_TOKEN",
				"credential:slack:SLACK_TOKEN",
			},
		},
	})
	if err != nil {
		t.Fatalf("scheduleRequestFromSpec: %v", err)
	}
	if got, want := req.CredentialProfiles, []string{"github", "slack"}; !equalStrings(got, want) {
		t.Fatalf("credential_profiles = %v, want %v", got, want)
	}
	if req.CredentialProfile != "" {
		t.Fatalf("legacy credential_profile = %q, want empty for multi-profile plan", req.CredentialProfile)
	}
}

func TestScheduleRequestHardPlacesIdentifiedPrincipalEnvironment(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer principals.Close()
	effective, err := principals.Create(principal.Principal{
		Kind:                principal.KindHuman,
		DisplayName:         "pinned",
		EnvironmentTargetID: "target-pinned",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &taskRouter{principals: principals}
	req, err := r.scheduleRequestFromSpec(context.Background(), task.Spec{
		Agent: "codex",
		ExecutionIdentity: &principal.ExecutionIdentity{
			IdentityMode:         principal.IdentityModeIdentified,
			EffectivePrincipalID: effective.ID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.RequiredTargetID != "target-pinned" {
		t.Fatalf("required target = %q, want target-pinned", req.RequiredTargetID)
	}
}

func TestRouterSubmitResolvesEnvCredentialProfilesBeforeScheduling(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shared-service.json"), []byte(`{
		"kind": "short-lived",
		"env": {"SERVICE_TOKEN": "demo-secret", "OTHER": "ignored"}
	}`), 0600); err != nil {
		t.Fatalf("write credential: %v", err)
	}

	var gotReq registry.ScheduleRequest
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			gotReq = req
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if targetID != "t1" || method != pb.TaskMethodSubmit {
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-env","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, newTaskIndex(), nil)
	r.resolveProfile = nil
	r.credentialProvider = func() (task.CredentialProvider, error) {
		return task.FileCredentials{Dir: dir}, nil
	}

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "operator",
			Type: "codex",
			Envs: []string{"SERVICE_TOKEN"},
		},
	})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if got, want := gotReq.CredentialProfiles, []string{"shared-service"}; !equalStrings(got, want) {
		t.Fatalf("credential_profiles = %v, want %v", got, want)
	}
	if gotReq.CredentialProfile != "shared-service" {
		t.Fatalf("legacy credential_profile = %q, want shared-service", gotReq.CredentialProfile)
	}
}

func TestRouterSubmitReleasesOnRouteFailure(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(string, string, interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("tunnel boom")
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "x"})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err == nil {
		t.Fatal("expected submit error")
	}
	if len(sched.released) != 1 || sched.released[0].ReservationId != "rsv1" {
		t.Fatalf("reservation not released on failure: %+v", sched.released)
	}
	if len(sched.committed) != 0 {
		t.Fatalf("should not commit on failure: %+v", sched.committed)
	}
}

func TestRouterTaskMutationUsesSavedIdentityAndParentExecution(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer principals.Close()
	owner, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: owner.ID,
		EffectivePrincipalID:   owner.ID,
		Source:                 principal.SourceMCP,
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"parent_session_id":  "parent-session",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: []*pb.Target{{
			Id:           "t1",
			Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"},
			Sessions: []*pb.Session{{
				Id:                "parent-session",
				ExecutionIdentity: principal.ToProtoIdentity(identity),
			}},
		}},
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodGet:
				return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
			case pb.TaskMethodCancel:
				return respJSON(t, &pb.CancelSubtaskResponse{Ok: true}), nil
			default:
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1", ParentSessionID: "parent-session"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = principals

	if _, err := r.CancelSubtask(context.Background(), &pb.CancelSubtaskRequest{Id: "task-owned"}); err != nil {
		t.Fatalf("parent execution could not cancel owned task: %v", err)
	}

	hub.targets[0].Sessions = nil
	if _, err := r.CancelSubtask(context.Background(), &pb.CancelSubtaskRequest{Id: "task-owned"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("orphan identified task mutation code = %v, want unauthenticated (err=%v)", status.Code(err), err)
	}
}

func TestRouterSubmitNoMatchingRuntime(t *testing.T) {
	r := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, registry.ErrNoMatchingRuntime
		},
	}, newTaskIndex(), nil)
	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "x"})
	_, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}

// ── router List fan-out ──────────────────────────────────────────────────────

func TestRouterListFansOutAndRepopulatesIndex(t *testing.T) {
	idx := newTaskIndex()
	hub := &fakeRouterHub{
		targets: targets("t1", "t2"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodList {
				return nil, fmt.Errorf("unexpected method %s", method)
			}
			switch targetID {
			case "t1":
				return respJSON(t, &pb.ListSubtasksResponse{TasksJson: [][]byte{[]byte(`{"id":"a"}`)}}), nil
			case "t2":
				return respJSON(t, &pb.ListSubtasksResponse{TasksJson: [][]byte{[]byte(`{"id":"b"}`)}}), nil
			}
			return nil, fmt.Errorf("unknown target %s", targetID)
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, idx, nil)
	out, err := r.ListSubtasks(context.Background(), &pb.ListSubtasksRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out.TasksJson) != 2 {
		t.Fatalf("tasks = %d", len(out.TasksJson))
	}
	if p, ok := idx.get("a"); !ok || p.TargetID != "t1" {
		t.Fatalf("index[a] = %+v ok=%v", p, ok)
	}
	if p, ok := idx.get("b"); !ok || p.TargetID != "t2" {
		t.Fatalf("index[b] = %+v ok=%v", p, ok)
	}
}

// ── router Get via index ─────────────────────────────────────────────────────

func TestRouterGetRoutesToIndexedTarget(t *testing.T) {
	idx := newTaskIndex()
	idx.put("task-1", taskPlacement{TargetID: "t1"})
	var gotTarget string
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			gotTarget = targetID
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"running"}`)}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, idx, nil)
	out, err := r.GetSubtask(context.Background(), &pb.GetSubtaskRequest{Id: "task-1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gotTarget != "t1" {
		t.Fatalf("routed to %q, want t1", gotTarget)
	}
	if string(out.TaskJson) != `{"id":"task-1","status":"running"}` {
		t.Fatalf("task json = %s", out.TaskJson)
	}
}

// ── reconciler ───────────────────────────────────────────────────────────────

func TestReconcilerRetriesReleaseUntilRuntimeConfirms(t *testing.T) {
	for _, acknowledgedLate := range []bool{false, true} {
		t.Run(fmt.Sprintf("release_executed_before_response_loss=%t", acknowledgedLate), func(t *testing.T) {
			now := time.Unix(100, 0)
			adm := registry.NewAdmission(registry.AdmissionConfig{
				MaxSessions: 1, PerAgentMax: map[string]int{"claude": 1},
				ReservationTTL: time.Second, Clock: func() time.Time { return now },
			})
			reservation, err := adm.Reserve("claude")
			if err != nil {
				t.Fatal(err)
			}
			if err := adm.Commit(reservation); err != nil {
				t.Fatal(err)
			}
			idx := newTaskIndex()
			idx.put("done", taskPlacement{TargetID: "t1", ReservationId: reservation})
			releaseAttempts := 0
			hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
				switch method {
				case pb.TaskMethodGet:
					return respJSON(t, &pb.GetSubtaskResponse{TaskJson: []byte(`{"id":"done","status":"succeeded"}`)}), nil
				case "Release":
					releaseAttempts++
					if releaseAttempts > 1 || acknowledgedLate {
						adm.Release(payload.(*pb.ReleaseRequest).ReservationId)
					}
					if releaseAttempts == 1 {
						return nil, fmt.Errorf("injected release transport failure")
					}
					return respJSON(t, &pb.ReleaseResponse{}), nil
				default:
					return nil, fmt.Errorf("unexpected method %s", method)
				}
			}}
			sched := registry.NewScheduler(nil, newTunnelReserver(hub))
			rc := newTaskReconciler(hub, sched, idx)
			rc.reconcileOnce(context.Background())
			if p, ok := idx.get("done"); !ok || p.ReservationId != reservation {
				t.Fatal("unconfirmed release lost its retry record")
			}
			now = now.Add(time.Hour)
			adm.ExpireReservations()
			rc.reconcileOnce(context.Background())
			if _, ok := idx.get("done"); ok {
				t.Fatal("confirmed release remains indexed")
			}
			if _, err := adm.Reserve("claude"); err != nil {
				t.Fatalf("released capacity unavailable: %v", err)
			}
			rc.reconcileOnce(context.Background())
			if releaseAttempts != 2 {
				t.Fatalf("release attempts = %d, want one failure and one confirmation", releaseAttempts)
			}
		})
	}
}

func TestReconcilerReleasesTerminalKeepsRunning(t *testing.T) {
	idx := newTaskIndex()
	idx.put("done", taskPlacement{TargetID: "t1", ReservationId: "rsv-done"})
	idx.put("busy", taskPlacement{TargetID: "t1", ReservationId: "rsv-busy"})
	sched := &fakeScheduler{}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
			req := payload.(*pb.GetSubtaskRequest)
			st := "running"
			if req.Id == "done" {
				st = "succeeded"
			}
			tj, _ := json.Marshal(task.TaskRun{ID: req.Id, Status: task.Status(st)})
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: tj}), nil
		},
	}
	rc := newTaskReconciler(hub, sched, idx)
	rc.reconcileOnce(context.Background())

	if len(sched.released) != 1 || sched.released[0].ReservationId != "rsv-done" {
		t.Fatalf("released = %+v, want only rsv-done", sched.released)
	}
	if _, ok := idx.get("done"); ok {
		t.Fatal("terminal task not dropped from index")
	}
	if _, ok := idx.get("busy"); !ok {
		t.Fatal("running task wrongly dropped from index")
	}
}

func TestBridgeSubtaskReaderFindsTerminalTaskAfterReconcilerDropsIndex(t *testing.T) {
	idx := newTaskIndex()
	idx.put("done", taskPlacement{TargetID: "t1", ReservationId: "rsv-done", ParentSessionID: "parent-session"})
	sched := &fakeScheduler{}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodGet:
				req := payload.(*pb.GetSubtaskRequest)
				tj, _ := json.Marshal(task.TaskRun{ID: req.Id, ParentSessionID: "parent-session", Status: task.StatusSucceeded})
				return respJSON(t, &pb.GetSubtaskResponse{TaskJson: tj}), nil
			case pb.TaskMethodList:
				tj, _ := json.Marshal(task.TaskRun{ID: "done", ParentSessionID: "parent-session", Status: task.StatusSucceeded})
				return respJSON(t, &pb.ListSubtasksResponse{TasksJson: [][]byte{tj}}), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		},
	}
	router := newTaskRouter(hub, sched, idx, nil)
	rc := newTaskReconciler(hub, sched, idx)
	rc.reconcileOnce(context.Background())
	if _, ok := idx.get("done"); ok {
		t.Fatal("terminal task should be dropped from live routing index")
	}

	reader := &bridgeSubtaskReader{router: router}
	if got := reader.SubtaskIDsByParent("parent-session"); len(got) != 1 || got[0] != "done" {
		t.Fatalf("SubtaskIDsByParent after reconcile = %v, want [done]", got)
	}
}

func TestBridgeSubtaskReaderStreamsEventJSON(t *testing.T) {
	idx := newTaskIndex()
	idx.put("task-1", taskPlacement{TargetID: "t1", ParentSessionID: "parent-session"})
	eventJSON := []byte(`{"task_id":"task-1","seq":4,"kind":"output","output":"d29ya2luZw=="}`)
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(string, string, interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("unexpected unary route")
		},
		streamFn: func(targetID, method string, payload interface{}, emit func(*pb.TunnelMsg) error) error {
			if targetID != "t1" || method != pb.TaskMethodStream {
				return fmt.Errorf("unexpected stream route %s/%s", targetID, method)
			}
			request, ok := payload.(*pb.StreamSubtaskRequest)
			if !ok || request.Id != "task-1" || request.FromSeq != 3 {
				return fmt.Errorf("unexpected stream request %#v", payload)
			}
			encoded, err := json.Marshal(&pb.SubtaskEvent{EventJson: eventJSON})
			if err != nil {
				return err
			}
			return emit(&pb.TunnelMsg{Payload: encoded})
		},
	}
	reader := &bridgeSubtaskReader{router: newTaskRouter(hub, &fakeScheduler{}, idx, nil)}
	var got []byte
	if err := reader.StreamSubtaskEvents(context.Background(), "task-1", 3, func(raw []byte) error {
		got = append([]byte(nil), raw...)
		return nil
	}); err != nil {
		t.Fatalf("StreamSubtaskEvents: %v", err)
	}
	if string(got) != string(eventJSON) {
		t.Fatalf("event JSON = %s, want %s", got, eventJSON)
	}
}

func TestReconcilerReclaimsGoneKeepsDisconnected(t *testing.T) {
	idx := newTaskIndex()
	idx.put("gone", taskPlacement{TargetID: "t1", ReservationId: "rsv-gone"})
	idx.put("offline", taskPlacement{TargetID: "t2", ReservationId: "rsv-off"})
	sched := &fakeScheduler{}
	hub := &fakeRouterHub{
		targets: targets("t1", "t2"),
		routeFn: func(targetID string, _ string, _ interface{}) (*pb.TunnelMsg, error) {
			if targetID == "t1" {
				return nil, status.Errorf(codes.NotFound, "task not found")
			}
			return nil, fmt.Errorf("target %q not connected", targetID)
		},
	}
	rc := newTaskReconciler(hub, sched, idx)
	rc.reconcileOnce(context.Background())

	if len(sched.released) != 1 || sched.released[0].ReservationId != "rsv-gone" {
		t.Fatalf("released = %+v, want only rsv-gone", sched.released)
	}
	if _, ok := idx.get("gone"); ok {
		t.Fatal("gone task not dropped")
	}
	if _, ok := idx.get("offline"); !ok {
		t.Fatal("disconnected task wrongly dropped (must retry next tick)")
	}
}

// TestRouterResolvesProfileBeforeScheduling: a submit with a profile ref is
// resolved to its base agent BEFORE scheduling (the scheduler must see the base
// type), and the rewritten spec (base agent + ResolvedProfile) is what gets
// routed to the runtime.
func TestSubmitScheduledTaskNormalizesDefaultPermissionPolicyBeforeRouting(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			if req.AgentType != "claude" {
				return nil, fmt.Errorf("scheduler saw agent %q, want claude", req.AgentType)
			}
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if targetID != "t1" || method != pb.TaskMethodSubmit {
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
			req := payload.(*pb.SubmitSubtaskRequest)
			var routed task.Spec
			if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
				return nil, fmt.Errorf("decode routed spec: %v", err)
			}
			if routed.PermissionPolicy == task.PermissionPolicy("default") {
				return nil, fmt.Errorf("routed literal default permission_policy: %s", req.SpecJson)
			}
			if routed.PermissionPolicy != "" {
				return nil, fmt.Errorf("routed permission_policy = %q, want omitted/default", routed.PermissionPolicy)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-scheduled","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(*task.Spec) error { return nil }

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "scheduled", PermissionPolicy: task.PermissionPolicy("default")})
	id, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if err != nil {
		t.Fatalf("SubmitScheduledTask: %v", err)
	}
	if id != "task-scheduled" {
		t.Fatalf("task id = %q, want task-scheduled", id)
	}
}

func TestSubmitScheduledTaskNormalizesEffectiveDefaultPermissionPolicyBeforeRouting(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			if req.AgentType != "claude" {
				return nil, fmt.Errorf("scheduler saw agent %q, want claude", req.AgentType)
			}
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if targetID != "t1" || method != pb.TaskMethodSubmit {
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
			req := payload.(*pb.SubmitSubtaskRequest)
			var routed task.Spec
			if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
				return nil, fmt.Errorf("decode routed spec: %v", err)
			}
			if routed.PermissionPolicy == task.PermissionPolicy("default") || strings.Contains(string(req.SpecJson), "default") {
				return nil, fmt.Errorf("routed literal default permission_policy: %s", req.SpecJson)
			}
			if routed.PermissionPolicy != "" {
				return nil, fmt.Errorf("routed permission_policy = %q, want omitted/default", routed.PermissionPolicy)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-scheduled","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(spec *task.Spec) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "claude"
			spec.PermissionPolicy = task.PermissionPolicy("default")
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "claude", PermissionPolicy: "default"}
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Agent: "team/reviewer", Task: "scheduled"})
	id, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if err != nil {
		t.Fatalf("SubmitScheduledTask: %v", err)
	}
	if id != "task-scheduled" {
		t.Fatalf("task id = %q, want task-scheduled", id)
	}
}

func TestSubmitScheduledTaskRejectsEffectiveEscalateToMainAndDoesNotRoute(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler should not be called for effective escalate-to-main scheduled task: %+v", req)
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("route should not be called for effective escalate-to-main scheduled task")
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(spec *task.Spec) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "codex"
			spec.PermissionPolicy = task.PolicyEscalateToMain
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "codex", PermissionPolicy: string(task.PolicyEscalateToMain)}
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Agent: "team/reviewer", Task: "review"})
	_, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SubmitScheduledTask error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "effective permission_policy") || !strings.Contains(err.Error(), string(task.PolicyEscalateToMain)) {
		t.Fatalf("SubmitScheduledTask error = %v, want effective permission policy rejection", err)
	}
}

func TestSubmitScheduledTaskRejectsGitWorktreeWithoutLocalDirAndDoesNotRoute(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler should not be called for invalid git-worktree scheduled task: %+v", req)
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("route should not be called for invalid git-worktree scheduled task")
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(*task.Spec) error { return nil }

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "claude",
		Task:  "review",
		Workspace: task.WorkspaceSpec{
			Policy: task.WorkspaceGitWorktree,
		},
	})
	_, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SubmitScheduledTask error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "workspace.local_dir or workspace.repo.cache_key is required") {
		t.Fatalf("SubmitScheduledTask error = %v, want workspace.local_dir rejection", err)
	}
}

func TestSubmitScheduledTaskRejectsEffectiveGitWorktreeWithoutLocalDirAndDoesNotRoute(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler should not be called for effective invalid git-worktree scheduled task: %+v", req)
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("route should not be called for effective invalid git-worktree scheduled task")
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(spec *task.Spec) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "claude"
			spec.Workspace.Policy = task.WorkspaceGitWorktree
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "claude"}
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Agent: "team/reviewer", Task: "review"})
	_, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SubmitScheduledTask error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "effective workspace.local_dir or workspace.repo.cache_key is required") {
		t.Fatalf("SubmitScheduledTask error = %v, want effective workspace.local_dir rejection", err)
	}
}

func TestSubmitScheduledTaskAcceptsGitWorktreeWithRepoCacheKey(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			if req.RepoCacheKey != "org/repo" {
				return nil, fmt.Errorf("scheduler repo cache key = %q, want org/repo", req.RepoCacheKey)
			}
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if targetID != "t1" || method != pb.TaskMethodSubmit {
				return nil, fmt.Errorf("unexpected route %s/%s", targetID, method)
			}
			req := payload.(*pb.SubmitSubtaskRequest)
			var routed task.Spec
			if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
				return nil, fmt.Errorf("decode routed spec: %v", err)
			}
			if routed.Workspace.Policy != task.WorkspaceGitWorktree || routed.Workspace.Repo == nil || routed.Workspace.Repo.CacheKey != "org/repo" {
				return nil, fmt.Errorf("routed workspace = %#v, want git-worktree repo cache key", routed.Workspace)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-scheduled","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	r.resolveProfile = func(*task.Spec) error { return nil }

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "claude",
		Task:  "review",
		Workspace: task.WorkspaceSpec{
			Policy: task.WorkspaceGitWorktree,
			Repo:   &task.RepoSpec{CacheKey: "org/repo", Commit: "abc123"},
		},
	})
	id, err := r.SubmitScheduledTask(context.Background(), specJSON)
	if err != nil {
		t.Fatalf("SubmitScheduledTask: %v", err)
	}
	if id != "task-scheduled" {
		t.Fatalf("task id = %q, want task-scheduled", id)
	}
}

func TestRouterResolvesProfileBeforeScheduling(t *testing.T) {
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			if req.AgentType != "claude" {
				return nil, fmt.Errorf("scheduler saw non-base agent %q", req.AgentType)
			}
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			req := payload.(*pb.SubmitSubtaskRequest)
			var s task.Spec
			if err := json.Unmarshal(req.SpecJson, &s); err != nil {
				t.Fatalf("decode routed spec: %v", err)
			}
			if s.Agent != "claude" || s.ResolvedProfile == nil || s.ResolvedProfile.Name != "code-reviewer" || s.ResolvedProfile.Type != "claude" {
				return nil, fmt.Errorf("routed spec not resolved: %+v", s)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	// Inject a fake resolver; profile loading itself is unit-tested in internal/agentprofile.
	r.resolveProfile = func(spec *task.Spec) error {
		if spec.Agent == "code-reviewer" {
			spec.Agent = "claude"
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "code-reviewer", Type: "claude"}
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Agent: "code-reviewer", Task: "do it"})
	out, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if string(out.TaskJson) != `{"id":"task-1","status":"pending"}` {
		t.Fatalf("task json = %s", out.TaskJson)
	}
}

func TestRouterPruneFansOutAndReleasesIndexedReservations(t *testing.T) {
	idx := newTaskIndex()
	idx.put("task-1", taskPlacement{TargetID: "t1", ReservationId: "rsv-1"})
	idx.put("offline-task", taskPlacement{TargetID: "offline", ReservationId: "rsv-off"})
	sched := &fakeScheduler{}
	hub := &fakeRouterHub{
		targets: targets("t1", "t2"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodPrune {
				t.Fatalf("method = %s", method)
			}
			if targetID == "t2" {
				return nil, fmt.Errorf("boom")
			}
			return respJSON(t, &pb.PruneSubtasksResponse{Tasks: []*pb.PrunedSubtask{{Id: "task-1"}}}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, nil)
	out, err := r.PruneSubtasks(context.Background(), &pb.PruneSubtasksRequest{})
	if err != nil {
		t.Fatalf("PruneSubtasks: %v", err)
	}
	if out.DeletedCount != 1 || len(out.Tasks) != 1 || out.Tasks[0].TargetId != "t1" {
		t.Fatalf("out = %+v", out)
	}
	if _, ok := idx.get("task-1"); ok {
		t.Fatal("deleted task remained indexed")
	}
	if len(sched.released) != 1 || sched.released[0].ReservationId != "rsv-1" {
		t.Fatalf("released = %+v", sched.released)
	}
	var sawT2, sawOffline bool
	for _, skipped := range out.Skipped {
		if skipped.TargetId == "t2" {
			sawT2 = true
		}
		if skipped.TargetId == "offline" && skipped.Id == "offline-task" {
			sawOffline = true
		}
	}
	if !sawT2 || !sawOffline {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
}

// ── persistent agent instance sticky routing ──────────────────────────────────

func TestRouterPruneDryRunKeepsAdmissionAndIndex(t *testing.T) {
	idx := newTaskIndex()
	idx.put("done", taskPlacement{TargetID: "t1", ReservationId: "reservation-1"})
	sched := &fakeScheduler{}
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
		if method != pb.TaskMethodPrune || !payload.(*pb.PruneSubtasksRequest).GetDryRun() {
			t.Fatalf("expected a dry-run prune, got %s %+v", method, payload)
		}
		return respJSON(t, &pb.PruneSubtasksResponse{Tasks: []*pb.PrunedSubtask{{Id: "done"}}}), nil
	}}
	router := newTaskRouter(hub, sched, idx, nil)
	out, err := router.PruneSubtasks(context.Background(), &pb.PruneSubtasksRequest{DryRun: true})
	if err != nil || len(out.GetTasks()) != 1 {
		t.Fatalf("dry-run prune = (%v, %v), want one candidate", out, err)
	}
	if p, ok := idx.get("done"); !ok || p.ReservationId != "reservation-1" {
		t.Fatal("dry-run changed the placement")
	}
	if len(sched.released) != 0 {
		t.Fatal("dry-run released runtime admission")
	}
}

func TestRouterPruneRetriesReleaseAfterTaskHistoryIsGone(t *testing.T) {
	adm := registry.NewAdmission(registry.AdmissionConfig{MaxSessions: 1, PerAgentMax: map[string]int{"claude": 1}})
	reservation, err := adm.Reserve("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := adm.Commit(reservation); err != nil {
		t.Fatal(err)
	}
	idx := newTaskIndex()
	idx.put("done", taskPlacement{TargetID: "t1", ReservationId: reservation})
	releaseAttempts := 0
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
		switch method {
		case pb.TaskMethodPrune:
			return respJSON(t, &pb.PruneSubtasksResponse{Tasks: []*pb.PrunedSubtask{{Id: "done"}}}), nil
		case pb.TaskMethodGet:
			return nil, status.Error(codes.NotFound, "task history was pruned")
		case "Release":
			releaseAttempts++
			if releaseAttempts == 1 {
				return nil, fmt.Errorf("injected release transport failure")
			}
			adm.Release(payload.(*pb.ReleaseRequest).ReservationId)
			return respJSON(t, &pb.ReleaseResponse{}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}}
	sched := registry.NewScheduler(nil, newTunnelReserver(hub))
	router := newTaskRouter(hub, sched, idx, nil)
	out, err := router.PruneSubtasks(context.Background(), &pb.PruneSubtasksRequest{})
	if err != nil || out.GetDeletedCount() != 1 {
		t.Fatalf("prune = (%v, %v), want completed history deletion", out, err)
	}
	if _, ok := idx.get("done"); !ok {
		t.Fatal("prune forgot the unconfirmed admission release")
	}
	newTaskReconciler(hub, sched, idx).reconcileOnce(context.Background())
	if _, ok := idx.get("done"); ok {
		t.Fatal("confirmed release remains indexed")
	}
	if _, err := adm.Reserve("claude"); err != nil {
		t.Fatalf("pruned task still occupies capacity: %v", err)
	}
}

func newTestAgentInstanceStore(t *testing.T) *agentinstance.Store {
	t.Helper()
	s, err := agentinstance.OpenStore(filepath.Join(t.TempDir(), "agent_instances.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRouterSubmitPersistentStickyFromInstanceStore verifies that a persistent
// task whose agent instance already has a recorded target is scheduled with
// StickyTargetID set, so the scheduler routes the follow-up back to the durable
// environment's host.
func TestRouterSubmitPersistentStickyFromInstanceStore(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-1", AgentType: "claude", AgentProfile: "builtin/claude",
		ModelRef: "example-model", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	var seenReq registry.ScheduleRequest
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			seenReq = req
			if req.StickyTargetID != "t-sticky" {
				return nil, fmt.Errorf("expected sticky target t-sticky, got %q", req.StickyTargetID)
			}
			return &registry.Placement{TargetID: "t-sticky", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t-sticky"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-p1","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, store)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "follow up", Mode: task.ModePersistent, AgentInstanceID: "inst-1"})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if seenReq.AgentInstanceID != "inst-1" || seenReq.StickyTargetID != "t-sticky" {
		t.Fatalf("schedule request = %+v, want instance inst-1 sticky t-sticky", seenReq)
	}
	// The placement is recorded with the instance id for later instance-level RPCs.
	p, ok := idx.get("task-p1")
	if !ok || p.AgentInstanceID != "inst-1" || p.TargetID != "t-sticky" {
		t.Fatalf("index entry = %+v ok=%v", p, ok)
	}
	in, err := store.Get("inst-1")
	if err != nil || in.AgentProfile != "builtin/claude" || in.ModelRef != "example-model" {
		t.Fatalf("instance snapshot = %+v err=%v, want preserved profile/model", in, err)
	}
}

func TestRouterSubmitPersistentHydratesOmittedIdentityFromInstanceStore(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-hydrate", AgentType: "claude", AgentProfile: "builtin/claude",
		ModelRef: "example-model", TargetID: "t-sticky", Status: agentinstance.StatusIdle,
	}); err != nil {
		t.Fatal(err)
	}

	var resolvedInput task.Spec
	r := newTaskRouter(&fakeRouterHub{
		targets: targets("t-sticky"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-followup","status":"pending"}`)}), nil
		},
	}, &fakeScheduler{acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
		if req.AgentType != "claude" || req.StickyTargetID != "t-sticky" {
			t.Fatalf("schedule request = %+v, want hydrated claude on t-sticky", req)
		}
		return &registry.Placement{TargetID: "t-sticky", ReservationId: "rsv-followup"}, nil
	}}, newTaskIndex(), store)
	r.resolveProfile = func(spec *task.Spec) error {
		resolvedInput = *spec
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Ref: "builtin/claude", PinnedRef: "builtin/claude", ModelRef: spec.Model}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Task: "follow up", Mode: task.ModePersistent, AgentInstanceID: "inst-hydrate"})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if resolvedInput.Agent != "builtin/claude" || resolvedInput.Model != "example-model" {
		t.Fatalf("resolver input = %+v, want stored profile/model", resolvedInput)
	}
}

func TestRouterRejectsPersistentFollowupModelChange(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-model", AgentType: "codex", ModelRef: "example-model",
		TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	r := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler must not run for a model-changing follow-up")
		},
	}, newTaskIndex(), store)
	r.resolveProfile = func(*task.Spec) error { return nil }

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "codex", Model: "gpt-5.5", Task: "follow up",
		Mode: task.ModePersistent, AgentInstanceID: "inst-model",
	})
	_, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "fixed to model") {
		t.Fatalf("SubmitSubtask error = %v, want fixed-model rejection", err)
	}
}

func TestRouterPersistentFollowupBackfillsLegacyProfileAndModel(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-legacy", AgentType: "codex", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	sched := &fakeScheduler{acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
		if req.StickyTargetID != "t-sticky" {
			t.Fatalf("StickyTargetID = %q, want t-sticky", req.StickyTargetID)
		}
		return &registry.Placement{TargetID: "t-sticky", ReservationId: "rsv-legacy"}, nil
	}}
	hub := &fakeRouterHub{
		targets: targets("t-sticky"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-legacy","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, newTaskIndex(), store)
	r.resolveProfile = func(spec *task.Spec) error {
		spec.Agent = "codex"
		spec.ResolvedProfile = &pb.AgentProfileSpec{
			Ref:       "team/coder",
			PinnedRef: "team/coder@rev-1",
			ModelRef:  "gpt-5.5",
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "team/coder", Task: "follow up",
		Mode: task.ModePersistent, AgentInstanceID: "inst-legacy",
	})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}

	in, err := store.Get("inst-legacy")
	if err != nil {
		t.Fatalf("Get legacy instance: %v", err)
	}
	if in.AgentProfile != "team/coder@rev-1" || in.ModelRef != "gpt-5.5" {
		t.Fatalf("legacy instance = %+v, want resolved profile/model backfilled", in)
	}
}

func TestRouterRejectsLegacyPersistentFollowupAgentTypeChange(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-legacy-type", AgentType: "codex", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	r := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler must not run for an agent-type-changing follow-up")
		},
	}, newTaskIndex(), store)
	r.resolveProfile = func(spec *task.Spec) error {
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Ref: "team/reviewer", PinnedRef: "team/reviewer@rev-1"}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "team/reviewer", Task: "follow up",
		Mode: task.ModePersistent, AgentInstanceID: "inst-legacy-type",
	})
	_, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "fixed to agent type") {
		t.Fatalf("SubmitSubtask error = %v, want fixed-agent-type rejection", err)
	}
}

func TestRouterRejectsPersistentFollowupWhenIdentityCannotBeLoaded(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-store-error", AgentType: "codex", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return nil, fmt.Errorf("scheduler must not run when persistent identity cannot be loaded")
		},
	}, newTaskIndex(), store)
	r.resolveProfile = func(spec *task.Spec) error {
		spec.Agent = "codex"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Ref: "team/coder", PinnedRef: "team/coder@rev-1"}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "team/coder", Task: "follow up",
		Mode: task.ModePersistent, AgentInstanceID: "inst-store-error",
	})
	_, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "claim persistent agent identity") {
		t.Fatalf("SubmitSubtask error = %v, want identity-store failure before scheduling", err)
	}
}

func TestRouterRollsBackLegacyIdentityWhenSubmitIsNotAccepted(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-route-error", AgentType: "codex", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: targets("t-sticky"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if _, err := store.UpdateStatus("inst-route-error", agentinstance.StatusArchived); err != nil {
				t.Fatalf("UpdateStatus during route: %v", err)
			}
			return nil, fmt.Errorf("runtime unavailable")
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t-sticky", ReservationId: "rsv-route-error"}, nil
		},
	}, newTaskIndex(), store)
	r.resolveProfile = func(spec *task.Spec) error {
		spec.Agent = "codex"
		spec.ResolvedProfile = &pb.AgentProfileSpec{
			Ref: "team/coder", PinnedRef: "team/coder@rev-1", ModelRef: "gpt-5.5",
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{
		Agent: "team/coder", Task: "follow up",
		Mode: task.ModePersistent, AgentInstanceID: "inst-route-error",
	})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); status.Code(err) != codes.Internal {
		t.Fatalf("SubmitSubtask error = %v, want route failure", err)
	}
	in, err := store.Get("inst-route-error")
	if err != nil {
		t.Fatalf("Get legacy instance: %v", err)
	}
	if in.AgentProfile != "" || in.ModelRef != "" || in.Status != agentinstance.StatusArchived {
		t.Fatalf("legacy instance = %+v, failed submit must roll back identity without reverting archive", in)
	}
}

func TestRouterSerializesLegacyPersistentProfileBackfill(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{
		ID: "inst-legacy-race", AgentType: "codex", TargetID: "t-sticky", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	firstRouted := make(chan struct{})
	secondRouted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var routeMu sync.Mutex
	routeCount := 0
	hub := &fakeRouterHub{
		targets: targets("t-sticky"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			routeMu.Lock()
			routeCount++
			call := routeCount
			routeMu.Unlock()
			if call == 1 {
				close(firstRouted)
				<-releaseFirst
			} else {
				close(secondRouted)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(fmt.Sprintf(`{"id":"task-race-%d","status":"pending"}`, call))}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t-sticky", ReservationId: "rsv-race"}, nil
		},
	}, newTaskIndex(), store)
	secondResolved := make(chan struct{})
	r.resolveProfile = func(spec *task.Spec) error {
		profile := spec.Agent
		spec.Agent = "codex"
		spec.ResolvedProfile = &pb.AgentProfileSpec{
			Ref: profile, PinnedRef: profile + "@rev-1", ModelRef: profile + "-model",
		}
		if profile == "team/second" {
			close(secondResolved)
		}
		return nil
	}

	submit := func(profile string) <-chan error {
		done := make(chan error, 1)
		go func() {
			specJSON, _ := json.Marshal(task.Spec{
				Agent: profile, Task: "follow up", Mode: task.ModePersistent, AgentInstanceID: "inst-legacy-race",
			})
			_, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
			done <- err
		}()
		return done
	}

	firstDone := submit("team/first")
	<-firstRouted
	secondDone := submit("team/second")
	<-secondResolved
	select {
	case <-secondRouted:
		close(releaseFirst)
		<-firstDone
		<-secondDone
		t.Fatal("competing legacy follow-up routed before the first profile/model backfill completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first SubmitSubtask: %v", err)
	}
	if err := <-secondDone; status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "fixed to profile") {
		t.Fatalf("second SubmitSubtask error = %v, want first profile to remain fixed", err)
	}

	in, err := store.Get("inst-legacy-race")
	if err != nil {
		t.Fatalf("Get legacy instance: %v", err)
	}
	if in.AgentProfile != "team/first@rev-1" || in.ModelRef != "team/first-model" {
		t.Fatalf("legacy instance = %+v, want first successful profile/model backfill", in)
	}
}

// TestRouterSubmitPersistentNewInstanceAcquiresAndRecords verifies that a
// persistent task for an unknown instance goes through normal Acquire (no
// sticky) and then records the instance with the chosen target, so the next
// task for the same instance can sticky-route.
func TestRouterSubmitPersistentNewInstanceAcquiresAndRecords(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	idx := newTaskIndex()
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			if req.StickyTargetID != "" {
				return nil, fmt.Errorf("new instance should not be sticky, got %q", req.StickyTargetID)
			}
			return &registry.Placement{TargetID: "t-fresh", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t-fresh"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-p2","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, store)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "first run", Mode: task.ModePersistent, AgentInstanceID: "inst-new"})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}

	// The instance record is created with the chosen target.
	in, err := store.Get("inst-new")
	if err != nil {
		t.Fatalf("instance not recorded: %v", err)
	}
	if in.TargetID != "t-fresh" || in.AgentType != "claude" || in.Status != agentinstance.StatusActive {
		t.Fatalf("recorded instance = %+v", in)
	}
}

// TestRouterSubmitPersistentFallsBackToIndexForSticky verifies that when the
// durable store has no target for an instance but the in-memory index does
// (window between creation and durable write, or a fresh store), the sticky
// target is resolved from the index.
func TestRouterSubmitPersistentFallsBackToIndexForSticky(t *testing.T) {
	store := newTestAgentInstanceStore(t) // empty store
	idx := newTaskIndex()
	// Simulate a prior task for inst-3 landing on t-indexed.
	idx.put("task-prev", taskPlacement{TargetID: "t-indexed", AgentInstanceID: "inst-3"})

	var seenSticky string
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			seenSticky = req.StickyTargetID
			return &registry.Placement{TargetID: "t-indexed", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t-indexed"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-p3","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, store)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "again", Mode: task.ModePersistent, AgentInstanceID: "inst-3"})
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if seenSticky != "t-indexed" {
		t.Fatalf("sticky = %q, want t-indexed (index fallback)", seenSticky)
	}
}

// TestRouterSubmitEphemeralHasNoSticky verifies ephemeral tasks never set a
// sticky target, even when an instance store is configured.
func TestRouterSubmitEphemeralHasNoSticky(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	idx := newTaskIndex()
	var seenSticky string
	sched := &fakeScheduler{
		acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
			seenSticky = req.StickyTargetID
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(targetID, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-e1","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, sched, idx, store)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "ephemeral"}) // no mode, no instance id
	if _, err := r.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if seenSticky != "" {
		t.Fatalf("ephemeral sticky = %q, want empty", seenSticky)
	}
	// No instance record should be created for ephemeral.
	if in, err := store.List(agentinstance.Filter{}); err != nil || len(in) != 0 {
		t.Fatalf("ephemeral should not record instance: %v (err %v)", in, err)
	}
}

// ── agent-instance RPC routing ───────────────────────────────────────────────

// instanceRouteHub captures the (target, method, payload) of the last Route
// call so instance-RPC tests can assert amuxd routes to the right target with
// the right tunnel method, and reply with a canned response.
func newInstanceRouteHub(t *testing.T, target string, reply *pb.TunnelMsg) (*fakeRouterHub, *string, *string, *pb.TunnelMsg) {
	t.Helper()
	var gotTarget, gotMethod string
	var gotPayload *pb.TunnelMsg
	hub := &fakeRouterHub{
		targets: targets(target),
		routeFn: func(tgt, method string, payload interface{}) (*pb.TunnelMsg, error) {
			gotTarget = tgt
			gotMethod = method
			raw, _ := json.Marshal(payload)
			gotPayload = &pb.TunnelMsg{Payload: raw}
			return reply, nil
		},
	}
	return hub, &gotTarget, &gotMethod, gotPayload
}

func TestRouterSendAgentMessageRoutesToInstanceTarget(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", TargetID: "t-1", Status: agentinstance.StatusActive})
	reply := respJSON(t, &pb.SendAgentMessageResponse{Ok: true, PendingMessages: 2})
	hub, gotTarget, gotMethod, _ := newInstanceRouteHub(t, "t-1", reply)
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
		return nil, fmt.Errorf("acquire should not run")
	}}, newTaskIndex(), store)

	resp, err := r.SendAgentMessage(context.Background(), &pb.SendAgentMessageRequest{Id: "inst-1", Text: "hi", Source: "test"})
	if err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}
	if *gotTarget != "t-1" || *gotMethod != pb.TaskMethodInstanceSend {
		t.Fatalf("route = %s/%s, want t-1/%s", *gotTarget, *gotMethod, pb.TaskMethodInstanceSend)
	}
	if !resp.Ok || resp.PendingMessages != 2 {
		t.Fatalf("response = %+v, want ok pending=2", resp)
	}
	// last-used is refreshed on the durable record.
	in, _ := store.Get("inst-1")
	if in.LastUsedAt == 0 {
		t.Fatalf("last_used_at not refreshed")
	}
}

func TestRouterInterruptAgentInstanceRoutesToInstanceTarget(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", TargetID: "t-1", Status: agentinstance.StatusActive})
	reply := respJSON(t, &pb.InterruptAgentInstanceResponse{Ok: true})
	hub, gotTarget, gotMethod, _ := newInstanceRouteHub(t, "t-1", reply)
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.InterruptAgentInstance(context.Background(), &pb.InterruptAgentInstanceRequest{Id: "inst-1"})
	if err != nil {
		t.Fatalf("InterruptAgentInstance: %v", err)
	}
	if *gotTarget != "t-1" || *gotMethod != pb.TaskMethodInstanceInterrupt {
		t.Fatalf("route = %s/%s, want t-1/%s", *gotTarget, *gotMethod, pb.TaskMethodInstanceInterrupt)
	}
	if !resp.Ok {
		t.Fatalf("response not ok")
	}
}

func TestRouterWaitAgentInstanceRoutesToInstanceTarget(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", TargetID: "t-1", Status: agentinstance.StatusActive})
	reply := respJSON(t, &pb.WaitAgentInstanceResponse{Activity: true, State: "running"})
	hub, gotTarget, gotMethod, _ := newInstanceRouteHub(t, "t-1", reply)
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.WaitAgentInstance(context.Background(), &pb.WaitAgentInstanceRequest{Id: "inst-1", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("WaitAgentInstance: %v", err)
	}
	if *gotTarget != "t-1" || *gotMethod != pb.TaskMethodInstanceWait {
		t.Fatalf("route = %s/%s, want t-1/%s", *gotTarget, *gotMethod, pb.TaskMethodInstanceWait)
	}
	if !resp.Activity || resp.State != "running" {
		t.Fatalf("response = %+v, want activity running", resp)
	}
}

func TestRouterListAgentInstancesFromStoreEnriched(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", AgentType: "claude", TargetID: "t-1", Status: agentinstance.StatusActive})
	hub := &fakeRouterHub{
		targets: targets("t-1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			// The describe fan-out for enrichment.
			if method == pb.TaskMethodInstanceDescribe {
				return respJSON(t, &pb.DescribeAgentInstanceResponse{ChildSessionState: "running", PendingMessages: 0}), nil
			}
			return respJSON(t, &pb.ListAgentInstancesResponse{}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.ListAgentInstances(context.Background(), &pb.ListAgentInstancesRequest{})
	if err != nil {
		t.Fatalf("ListAgentInstances: %v", err)
	}
	if len(resp.Instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(resp.Instances))
	}
	inst := resp.Instances[0]
	if inst.Id != "inst-1" || inst.AgentType != "claude" || inst.TargetId != "t-1" {
		t.Fatalf("instance = %+v", inst)
	}
	if inst.ChildSessionState != "running" {
		t.Fatalf("child state = %q, want running (enriched)", inst.ChildSessionState)
	}
	if inst.Status != "active" {
		t.Fatalf("status = %q, want active", inst.Status)
	}
}

func TestApplyLiveInstanceDoesNotReactivateDurableArchived(t *testing.T) {
	inst := &pb.AgentInstance{Id: "inst-owner-ended", Status: agentinstance.StatusArchived}
	applyLiveInstance(inst, &pb.DescribeAgentInstanceResponse{ChildSessionId: "stranded", ChildSessionState: "running"})
	if inst.Status != agentinstance.StatusArchived {
		t.Fatalf("status = %q, want durable archived to remain authoritative", inst.Status)
	}
	if inst.ChildSessionId != "stranded" || inst.ChildSessionState != "running" {
		t.Fatalf("live diagnostics not preserved: %+v", inst)
	}
}

func TestRouterListAgentInstancesStatusFilter(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", TargetID: "t-1", Status: agentinstance.StatusActive})
	store.Put(agentinstance.Instance{ID: "inst-2", TargetID: "t-1", Status: agentinstance.StatusIdle})
	hub := &fakeRouterHub{
		targets: targets("t-1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.ListAgentInstancesResponse{}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.ListAgentInstances(context.Background(), &pb.ListAgentInstancesRequest{Status: "idle"})
	if err != nil {
		t.Fatalf("ListAgentInstances: %v", err)
	}
	if len(resp.Instances) != 1 || resp.Instances[0].Id != "inst-2" {
		t.Fatalf("filtered instances = %+v, want only inst-2", resp.Instances)
	}
}

func TestRouterListAgentInstancesFiltersByLiveArchivedStatus(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-failed", TargetID: "t-1", Status: agentinstance.StatusActive})
	hub := &fakeRouterHub{
		targets: targets("t-1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method == pb.TaskMethodInstanceDescribe {
				return respJSON(t, &pb.DescribeAgentInstanceResponse{ChildSessionId: "child-failed", ChildSessionState: "archived"}), nil
			}
			return respJSON(t, &pb.ListAgentInstancesResponse{}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.ListAgentInstances(context.Background(), &pb.ListAgentInstancesRequest{Status: agentinstance.StatusArchived})
	if err != nil {
		t.Fatalf("ListAgentInstances: %v", err)
	}
	if len(resp.Instances) != 1 || resp.Instances[0].Id != "inst-failed" || resp.Instances[0].Status != agentinstance.StatusArchived {
		t.Fatalf("instances = %+v, want live-archived inst-failed", resp.Instances)
	}
	got, err := store.Get("inst-failed")
	if err != nil || got.Status != agentinstance.StatusArchived {
		t.Fatalf("durable instance = %+v err=%v, want archived convergence", got, err)
	}
}

func TestRouterGetAgentInstanceFromStore(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-1", AgentType: "claude", TargetID: "t-1", Status: agentinstance.StatusIdle})
	hub := &fakeRouterHub{
		targets: targets("t-1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.DescribeAgentInstanceResponse{ChildSessionState: "sleeping"}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	resp, err := r.GetAgentInstance(context.Background(), &pb.GetAgentInstanceRequest{Id: "inst-1"})
	if err != nil {
		t.Fatalf("GetAgentInstance: %v", err)
	}
	if resp.Instance == nil || resp.Instance.Id != "inst-1" {
		t.Fatalf("instance = %+v", resp.Instance)
	}
	if resp.Instance.ChildSessionState != "sleeping" {
		t.Fatalf("child state = %q, want sleeping (enriched)", resp.Instance.ChildSessionState)
	}
}

func TestRouterInstanceRPCMissingInstanceNotFound(t *testing.T) {
	store := newTestAgentInstanceStore(t) // empty
	hub := &fakeRouterHub{
		targets: targets("t-1"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			// Describe fan-out: instance not found on the runtime.
			return nil, fmt.Errorf("not found")
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), store)

	_, err := r.SendAgentMessage(context.Background(), &pb.SendAgentMessageRequest{Id: "no-such"})
	if err == nil {
		t.Fatal("SendAgentMessage on missing instance should fail")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("error code = %v, want NotFound", st.Code())
	}
}

func TestRouterInstanceRPCRejectsEmptyID(t *testing.T) {
	r := newTaskRouter(&fakeRouterHub{targets: nil}, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) { return nil, fmt.Errorf("nope") }}, newTaskIndex(), nil)
	_, err := r.SendAgentMessage(context.Background(), &pb.SendAgentMessageRequest{Id: ""})
	if err == nil {
		t.Fatal("empty agent_instance_id should be rejected")
	}
}

// TestReclaimInstancesForOwner archives every active instance owned by the
// given owner session and routes a CleanupAgentInstance RPC to each instance's
// hosting target. Already-archived instances are skipped (v0.39).
func TestReclaimInstancesForOwner(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-a", OwnerSessionID: "owner-1", TargetID: "t-1", Status: agentinstance.StatusActive})
	store.Put(agentinstance.Instance{ID: "inst-b", OwnerSessionID: "owner-1", TargetID: "t-2", Status: agentinstance.StatusActive})
	store.Put(agentinstance.Instance{ID: "inst-c", OwnerSessionID: "owner-1", TargetID: "t-3", Status: agentinstance.StatusArchived}) // already archived, skip
	store.Put(agentinstance.Instance{ID: "inst-d", OwnerSessionID: "owner-2", TargetID: "t-1", Status: agentinstance.StatusActive})   // different owner, untouched

	type cleanupCall struct{ target, method, id string }
	var mu sync.Mutex
	var calls []cleanupCall
	hub := &fakeRouterHub{
		targets: targets("t-1", "t-2", "t-3"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if req, ok := payload.(*pb.CleanupAgentInstanceRequest); ok {
				mu.Lock()
				calls = append(calls, cleanupCall{targetID, method, req.Id})
				mu.Unlock()
			}
			return respJSON(t, &pb.CleanupAgentInstanceResponse{Ok: true}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), store)

	r.ReclaimInstancesForOwner("owner-1")

	// Both active instances for owner-1 were archived in the durable store.
	for _, id := range []string{"inst-a", "inst-b"} {
		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.Status != agentinstance.StatusArchived {
			t.Errorf("instance %s status = %s, want archived", id, got.Status)
		}
	}
	// inst-c stays archived, inst-d stays active (different owner).
	if got, _ := store.Get("inst-d"); got.Status != agentinstance.StatusActive {
		t.Errorf("inst-d (other owner) status = %s, want active", got.Status)
	}

	// Cleanup RPCs were routed to each active instance's target with the
	// instance id, using the TaskMethodInstanceCleanup method tag.
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("cleanup calls = %v, want 2", calls)
	}
	gotIDs := map[string]bool{}
	for _, c := range calls {
		if c.method != pb.TaskMethodInstanceCleanup {
			t.Errorf("method = %s, want %s", c.method, pb.TaskMethodInstanceCleanup)
		}
		gotIDs[c.id] = true
	}
	for _, want := range []string{"inst-a", "inst-b"} {
		if !gotIDs[want] {
			t.Errorf("cleanup for %s not routed; calls=%v", want, calls)
		}
	}
}

// TestReclaimInstancesForOwnerOfflineTargetStillArchives verifies that when the
// hosting target is unreachable (Route errors), the durable store record is
// still flipped to archived — the durable sandbox is left for the amuxlet-side
// sweeper, but the instance is hidden from instance_list / sticky scheduling.
func TestReclaimInstancesForOwnerOfflineTargetStillArchives(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	store.Put(agentinstance.Instance{ID: "inst-a", OwnerSessionID: "owner-1", TargetID: "t-gone", Status: agentinstance.StatusActive})
	hub := &fakeRouterHub{
		targets: targets("t-gone"),
		routeFn: func(targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
			return nil, fmt.Errorf("target offline")
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), store)

	r.ReclaimInstancesForOwner("owner-1")

	got, err := store.Get("inst-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != agentinstance.StatusArchived {
		t.Errorf("status = %s, want archived even though target offline", got.Status)
	}
}

func TestManagedEvaluationProfileOnlyAllowsEphemeralUnscheduledTask(t *testing.T) {
	profile := &pb.AgentProfileSpec{Ref: "local-eval-0123456789abcdef-dev/reviewer"}
	if err := validateManagedEvaluationTask(task.Spec{ResolvedProfile: profile}, false); err != nil {
		t.Fatalf("default ephemeral evaluation task rejected: %v", err)
	}
	if err := validateManagedEvaluationTask(task.Spec{Mode: task.ModeEphemeral, ResolvedProfile: profile}, false); err != nil {
		t.Fatalf("ephemeral evaluation task rejected: %v", err)
	}
	if err := validateManagedEvaluationTask(task.Spec{Mode: task.ModePersistent, ResolvedProfile: profile}, false); err == nil {
		t.Fatal("persistent evaluation task accepted")
	}
	if err := validateManagedEvaluationTask(task.Spec{Mode: task.ModeEphemeral, ResolvedProfile: profile}, true); err == nil {
		t.Fatal("scheduled evaluation task accepted")
	}
}
