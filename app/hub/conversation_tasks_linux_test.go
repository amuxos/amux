//go:build linux

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Opt-in integration: a private helper and an unused numeric UID, without user
// account provisioning, installed services, live sessions or provider credentials.
func TestConversationTaskLinuxIsolation(t *testing.T) {
	helper, uidText := os.Getenv("AMUX_TEST_HELPER_BINARY"), os.Getenv("AMUX_TEST_CONVERSATION_UID")
	if helper == "" || uidText == "" {
		t.Skip("requires dedicated helper and unused test UID")
	}
	if os.Geteuid() != 0 {
		t.Fatal("requires root for dedicated numeric UID")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 65536 {
		t.Fatal("invalid test UID")
	}
	root, err := os.MkdirTemp("/tmp", "amux-conversation-isolation-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	cfg := isolation.HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, ExecWrapperPath: helper, MaterializerPath: "/bin/false", SkipAccountProvision: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- isolation.ServeHelper(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	}()
	client := isolation.NewHelperClient(cfg.SocketPath)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := client.Handshake(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runtime, err := isolation.NewRuntime(config.IsolationConfig{Enabled: true, Backend: principal.BackendLinuxDAC, HelperSocket: cfg.SocketPath, MappingDB: filepath.Join(root, "mapping.db"), PrincipalRoot: cfg.PrincipalRoot, UIDRangeStart: uid, UIDRangeEnd: uid})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	store, identity, _, _ := authzTestPrincipals(t)
	identity.Source, identity.IsolationBackend = principal.SourceFeishu, principal.BackendLinuxDAC
	var saved []byte
	hub := &fakeRouterHub{targets: []*pb.Target{{Id: "target", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}, Sessions: []*pb.Session{{Id: "finished", Name: "conversation", ExecutionIdentity: principal.ToProtoIdentity(identity)}}}}}
	hub.routeFn = func(_ string, method string, value interface{}) (*pb.TunnelMsg, error) {
		if method == pb.TaskMethodGet {
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: saved}), nil
		}
		if method != pb.TaskMethodSubmit {
			t.Fatalf("unexpected parent interaction: %s", method)
		}
		req := value.(*pb.SubmitSubtaskRequest)
		var spec task.Spec
		if err := json.Unmarshal(req.SpecJson, &spec); err != nil {
			t.Fatal(err)
		}
		frozen := *spec.ExecutionIdentity
		if err := runtime.RequireIdentifiedExecution(frozen); err != nil {
			if !errors.Is(err, isolation.ErrUnidentifiedExecution) {
				t.Fatal(err)
			}
			saved, _ = json.Marshal(map[string]any{"id": "task-old", "status": "failed", "error": err.Error(), "execution_identity": frozen})
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: saved}), nil
		}
		launchCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		paths, backend, err := runtime.PrepareExecution(launchCtx, frozen, req.LaunchId, "")
		if err != nil {
			t.Fatal(err)
		}
		launch := isolation.LaunchContext{LaunchID: req.LaunchId, Identity: &frozen, Backend: backend}
		executor, err := runtime.BackendForContext(launch)
		if err != nil {
			t.Fatal(err)
		}
		proc, err := executor.Start(launchCtx, isolation.LaunchSpec{Path: "/usr/bin/id", Args: []string{"-u"}, Dir: paths.Workspace, Env: []string{"PATH=/usr/bin:/bin"}, Context: launch})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		defer proc.Kill(context.Background())
		output, err := io.ReadAll(proc.Stdout())
		if err != nil {
			t.Fatal(err)
		}
		exit, err := proc.Wait(launchCtx)
		if err != nil || exit.Code != 0 || strings.TrimSpace(string(output)) != uidText {
			t.Fatalf("child UID=%q exit=%+v err=%v", output, exit, err)
		}
		t.Logf("background child executed with backend=%s uid=%s", backend, strings.TrimSpace(string(output)))
		saved, _ = json.Marshal(map[string]any{"id": "task-fixed", "status": "succeeded", "execution_identity": frozen, "result": map[string]string{"summary": "draft"}})
		return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: saved}), nil
	}
	router := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
		return &registry.Placement{TargetID: "target", ReservationId: "r1"}, nil
	}}, newTaskIndex(), nil)
	router.principals = store
	reader := &bridgeSubtaskReader{router: router}
	spec := []byte(`{"agent":"codex","task":"draft","permission_policy":"deny-all","workspace":{"policy":"temp-dir"}}`)
	if _, err := router.SubmitScheduledTask(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "unidentified execution") {
		t.Fatalf("old path did not reproduce failure: %s", saved)
	}
	id, err := reader.SubmitConversationTask(context.Background(), "target", "conversation", spec)
	if err != nil {
		t.Fatal(err)
	}
	router.index = newTaskIndex()
	if raw, err := reader.ReadConversationTask(context.Background(), "target", "conversation", id); err != nil || !strings.Contains(string(raw), `"status":"succeeded"`) {
		t.Fatalf("read after restart: %s %v", raw, err)
	}
}
