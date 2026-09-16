package tunnel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestClientMessagesUseHubRuntimeTerminology(t *testing.T) {
	for _, text := range []string{
		dialHubErrorMessage(assertErr{}),
		tunnelConnectedMessage("127.0.0.1:7000", "runtime-1"),
	} {
		lower := strings.ToLower(text)
		if !strings.Contains(lower, "hub") {
			t.Fatalf("message should mention hub: %q", text)
		}
		if strings.Contains(lower, "amuxd") || strings.Contains(lower, "amuxlet") || strings.Contains(lower, "sandbox") {
			t.Fatalf("message should not expose old terminology: %q", text)
		}
	}
}

func TestClientRegisterPayloadIncludesRuntimeVersion(t *testing.T) {
	c := NewClient("127.0.0.1:7100", "runtime-1", "devbox", nil, nil)
	c.SetVersion("v0.1.5")

	raw, err := c.registerPayload()
	if err != nil {
		t.Fatalf("register payload: %v", err)
	}
	var payload pb.RegisterPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode register payload: %v", err)
	}
	if payload.Version != "v0.1.5" {
		t.Fatalf("version = %q, want v0.1.5", payload.Version)
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }

type recordingSubmitServer struct {
	pb.UnimplementedAmuxletServer
	request   *pb.SubmitRequest
	trusted   bool
	requestID string
}

func (s *recordingSubmitServer) Submit(ctx context.Context, req *pb.SubmitRequest) (*pb.SubmitResponse, error) {
	s.request = req
	s.trusted = IsTrustedSubmit(ctx)
	s.requestID = RequestID(ctx)
	return &pb.SubmitResponse{Ok: true, TurnId: "turn-image"}, nil
}

func TestHandleRequestPreservesMultipartSubmitOneofs(t *testing.T) {
	want := &pb.SubmitRequest{
		Name:   "vision",
		Source: "lark",
		Async:  true,
		Parts: []*pb.InputPart{
			{Value: &pb.InputPart_Text{Text: "inspect"}},
			{Value: &pb.InputPart_Image{Image: &pb.ImageInput{
				Data: []byte("image-bytes"), MimeType: "image/png", DisplayName: "screen.png",
			}}},
		},
	}
	raw, err := marshalTunnelPayload(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	server := &recordingSubmitServer{}
	client := NewClient("addr", "runtime-1", "devbox", nil, server)

	var mu sync.Mutex
	var sent []*pb.TunnelMsg
	client.handleRequest(context.Background(), func(msg *pb.TunnelMsg) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, msg)
		return nil
	}, &pb.TunnelMsg{Kind: "req", RequestId: "r-image", Method: "Submit", Payload: raw})

	if len(sent) != 1 || sent[0].Error != "" {
		t.Fatalf("response frames = %+v", sent)
	}
	got := server.request
	if got == nil || len(got.Parts) != 2 || got.Parts[0].GetText() != "inspect" {
		t.Fatalf("submit request = %+v", got)
	}
	image := got.Parts[1].GetImage()
	if image == nil || string(image.Data) != "image-bytes" || image.MimeType != "image/png" || image.DisplayName != "screen.png" {
		t.Fatalf("image part = %+v", image)
	}
}

func TestHandleRequestMarksSubmitAsTrustedTunnelTraffic(t *testing.T) {
	want := &pb.SubmitRequest{
		Name: "owner-session", Source: "lark", Async: true, Text: "edit local expert",
		LocalExpertAuthoring: true,
	}
	raw, err := marshalTunnelPayload(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	server := &recordingSubmitServer{}
	client := NewClient("addr", "runtime-1", "devbox", nil, server)
	client.handleRequest(context.Background(), func(*pb.TunnelMsg) error { return nil }, &pb.TunnelMsg{
		Kind: "req", RequestId: "r-authoring", Method: "Submit", Payload: raw,
	})
	if server.request == nil || !server.request.GetLocalExpertAuthoring() || !server.trusted || server.requestID != "r-authoring" {
		t.Fatalf("trusted submit = request %+v trusted=%v request_id=%q", server.request, server.trusted, server.requestID)
	}
}

type recordingTaskServer struct {
	pb.UnimplementedAmuxTaskServer
	request *pb.ActiveTurnRequest
}

func (s *recordingTaskServer) ResolveParentTurn(_ context.Context, req *pb.ActiveTurnRequest) (*pb.ActiveTurnResponse, error) {
	s.request = req
	return &pb.ActiveTurnResponse{Active: true, TurnId: "turn-parent", SessionId: "session-parent"}, nil
}

func TestHandleRequestRoutesParentTurnResolutionThroughTaskPlane(t *testing.T) {
	raw, err := marshalTunnelPayload(&pb.ActiveTurnRequest{Name: "parent"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	server := &recordingTaskServer{}
	client := NewClient("addr", "runtime-1", "devbox", nil, nil)
	client.SetTaskServer(server)
	var sent *pb.TunnelMsg
	client.handleRequest(context.Background(), func(msg *pb.TunnelMsg) error {
		sent = msg
		return nil
	}, &pb.TunnelMsg{Kind: "req", RequestId: "r-parent", Method: pb.TaskMethodResolveParentTurn, Payload: raw})
	if server.request == nil || server.request.Name != "parent" {
		t.Fatalf("task-plane request = %+v", server.request)
	}
	if sent == nil || sent.Error != "" {
		t.Fatalf("response = %+v", sent)
	}
	var response pb.ActiveTurnResponse
	if err := json.Unmarshal(sent.Payload, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TurnId != "turn-parent" || response.SessionId != "session-parent" {
		t.Fatalf("response = %+v", response)
	}
}

func TestMarshalTunnelPayloadKeepsLegacyJSONWithoutOneof(t *testing.T) {
	want := &pb.RunRequest{
		Name:                     "child",
		AgentActorTokenExpiresAt: 123,
		ProfileSpec: &pb.AgentProfileSpec{
			CredentialEnvSuggestions: map[string]string{"API_KEY": "credential/default"},
			Provider: &pb.AgentProfileProvider{
				Thinking: map[string]string{"reasoning_effort": "high"},
			},
		},
	}
	raw, err := marshalTunnelPayload(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded pb.RunRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("legacy JSON decode: %v; payload=%s", err, raw)
	}
	if decoded.Name != "child" || decoded.AgentActorTokenExpiresAt != 123 || decoded.ProfileSpec == nil || decoded.ProfileSpec.Provider == nil {
		t.Fatalf("decoded request = %+v", decoded)
	}
	if got := decoded.ProfileSpec.CredentialEnvSuggestions["API_KEY"]; got != "credential/default" {
		t.Fatalf("credential env suggestion = %q", got)
	}
	if got := decoded.ProfileSpec.Provider.Thinking["reasoning_effort"]; got != "high" {
		t.Fatalf("provider thinking = %q", got)
	}
}

// TestPushSessionsPreservesOwnerTaskId guards against the hub-path converter
// drifting from the local toProtoSession: pushSessions must carry OwnerTaskId
// (and AgentProfile) so amux ls can still recognize and hide task child
// sessions reported through amuxd. Dropping OwnerTaskId made archived subtask
// sessions leak into the default listing.
func TestPushSessionsPreservesOwnerTaskId(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	if _, err := mgr.CreateOwned("subtask", "claude", "task-1", ""); err != nil {
		t.Fatalf("create owned: %v", err)
	}

	c := NewClient("127.0.0.1:7100", "runtime-1", "devbox", mgr, nil)

	var captured *pb.TunnelMsg
	c.pushSessions(func(msg *pb.TunnelMsg) error {
		captured = msg
		return nil
	})
	if captured == nil {
		t.Fatalf("pushSessions did not send a message")
	}

	var payload pb.SessionsPayload
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode sessions payload: %v", err)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(payload.Sessions))
	}
	if got := payload.Sessions[0].OwnerTaskId; got != "task-1" {
		t.Fatalf("OwnerTaskId = %q, want task-1", got)
	}
}

func TestPushSessionsPreservesInputCapabilities(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("vision", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.InputModalities = []string{"text", "image"}
	sess.InputCapabilitySource = "codex:default"

	c := NewClient("127.0.0.1:7100", "runtime-1", "devbox", mgr, nil)
	var captured *pb.TunnelMsg
	c.pushSessions(func(msg *pb.TunnelMsg) error {
		captured = msg
		return nil
	})
	if captured == nil {
		t.Fatal("pushSessions did not send a message")
	}

	var payload pb.SessionsPayload
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode sessions payload: %v", err)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(payload.Sessions))
	}
	got := payload.Sessions[0]
	if strings.Join(got.InputModalities, ",") != "text,image" || got.InputCapabilitySource != "codex:default" {
		t.Fatalf("input capabilities = %v source=%q", got.InputModalities, got.InputCapabilitySource)
	}
}

// TestPushSessionsReportsAgentInstanceId verifies the persistent-agent instance
// id is forwarded to amuxd so the hub can correlate sessions with durable
// instance records (v0.39 owner-lifecycle reclaim relies on this).
func TestPushSessionsReportsAgentInstanceId(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	if _, err := mgr.CreateOwned("subtask", "claude", "task-1", "agent-inst-1"); err != nil {
		t.Fatalf("create owned: %v", err)
	}

	c := NewClient("127.0.0.1:7100", "runtime-1", "devbox", mgr, nil)

	var captured *pb.TunnelMsg
	c.pushSessions(func(msg *pb.TunnelMsg) error {
		captured = msg
		return nil
	})
	if captured == nil {
		t.Fatalf("pushSessions did not send a message")
	}

	var payload pb.SessionsPayload
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode sessions payload: %v", err)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(payload.Sessions))
	}
	if got := payload.Sessions[0].AgentInstanceId; got != "agent-inst-1" {
		t.Fatalf("AgentInstanceId = %q, want agent-inst-1", got)
	}
}

func TestPushSessionsOmitsLargeLaunchDetails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.CreateOwned("subtask", "codex", "task-1", "", "/work")
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	largeValue := strings.Repeat("x", 256*1024)
	if err := mgr.RecordLaunchParams(
		sess,
		[]string{"--model", largeValue},
		[]string{"HUGE_ENV=" + largeValue},
		"feishu",
		"thread-1",
		"team/coder",
		"gpt-5.5",
		"",
	); err != nil {
		t.Fatalf("record launch params: %v", err)
	}

	c := NewClient("127.0.0.1:7100", "runtime-1", "devbox", mgr, nil)

	var captured *pb.TunnelMsg
	c.pushSessions(func(msg *pb.TunnelMsg) error {
		captured = msg
		return nil
	})
	if captured == nil {
		t.Fatalf("pushSessions did not send a message")
	}
	if len(captured.Payload) > 32*1024 {
		t.Fatalf("sessions payload = %d bytes, want <= 32KiB summary payload", len(captured.Payload))
	}

	var payload pb.SessionsPayload
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode sessions payload: %v", err)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(payload.Sessions))
	}
	got := payload.Sessions[0]
	if len(got.Env) != 0 {
		t.Fatalf("tunnel session Env = %d entries, want omitted", len(got.Env))
	}
	if len(got.ExtraArgs) != 0 {
		t.Fatalf("tunnel session ExtraArgs = %d entries, want omitted", len(got.ExtraArgs))
	}
	if got.OwnerTaskId != "task-1" || got.AgentProfile != "team/coder" || got.EffectiveModel != "gpt-5.5" || got.Cwd != "/work" || got.WorkspaceSource != "feishu" || got.WorkspaceScope != "thread-1" {
		t.Fatalf("summary dropped required fields: %+v", got)
	}
}

type recordingFileServer struct {
	pb.UnimplementedAmuxletServer
	request *pb.SessionFileRequest
	trusted bool
}

func (s *recordingFileServer) PutSessionFile(ctx context.Context, r *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	s.request = r
	s.trusted = IsTrustedSubmit(ctx)
	return &pb.SessionFileResponse{Path: "amux-inbox/file"}, nil
}
func (s *recordingFileServer) ReadSessionArtifact(ctx context.Context, r *pb.SessionFileRequest) (*pb.SessionFileResponse, error) {
	s.request = r
	s.trusted = IsTrustedSubmit(ctx)
	return &pb.SessionFileResponse{Data: []byte("artifact")}, nil
}
func TestHandleRequestTrustedSessionFiles(t *testing.T) {
	for _, method := range []string{"PutSessionFile", "ReadSessionArtifact"} {
		srv := &recordingFileServer{}
		client := NewClient("addr", "runtime", "devbox", nil, srv)
		raw, err := marshalTunnelPayload(&pb.SessionFileRequest{Name: "s1", SessionId: "sid", LaunchId: "launch", FileName: "report", Data: []byte("file")})
		if err != nil {
			t.Fatal(err)
		}
		var result *pb.TunnelMsg
		client.handleRequest(context.Background(), func(m *pb.TunnelMsg) error { result = m; return nil }, &pb.TunnelMsg{Kind: "req", Method: method, RequestId: "file-req", Payload: raw})
		if result == nil || result.Error != "" || !srv.trusted || srv.request.SessionId != "sid" || string(srv.request.Data) != "file" {
			t.Fatalf("%s: %+v %+v", method, result, srv)
		}
	}
}
