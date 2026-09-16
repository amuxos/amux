package cli

import (
	"context"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type setModelCaptureClient struct {
	runTestClient
	resp           *pb.RunResponse
	gotTargetID    string
	gotName        string
	gotReplacement *pb.RunRequest
}

func (c *setModelCaptureClient) SetModel(_ context.Context, targetID, name string, replacement *pb.RunRequest) (*pb.RunResponse, error) {
	c.gotTargetID = targetID
	c.gotName = name
	c.gotReplacement = replacement
	return c.resp, nil
}

func withSetModelResolver(t *testing.T, backend sessionClient, sess *sessionEntry) {
	t.Helper()
	old := newSetModelSessionResolver
	t.Cleanup(func() { newSetModelSessionResolver = old })
	newSetModelSessionResolver = func() (*sessionResolver, error) {
		return resolverForTests([]targetSessions{{
			target:   targetRef{ID: "rt-local", Name: "local", Backend: backend},
			sessions: []*sessionEntry{sess},
		}}), nil
	}
}

func TestSessionSetModelCommandSendsReplacement(t *testing.T) {
	capture := &setModelCaptureClient{resp: &pb.RunResponse{Session: &pb.Session{Name: "work", EffectiveModel: "new-model"}}}
	withSetModelResolver(t, capture, &sessionEntry{Session: pb.Session{
		Id: "s1", Name: "work", AgentType: "codex", AgentProfile: "builtin/codex@abc123",
		ExtraArgs: []string{"--model", "old-model"}, Env: []string{"A=B"},
	}})

	cmd := newSessionSetModelCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"work", "new-model"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("set-model: %v", err)
	}
	if capture.gotTargetID != "rt-local" || capture.gotName != "work" {
		t.Fatalf("SetModel target/name = %q/%q", capture.gotTargetID, capture.gotName)
	}
	replacement := capture.gotReplacement
	if replacement == nil {
		t.Fatal("SetModel replacement = nil")
	}
	if replacement.AgentType != "codex" || replacement.AgentProfile != "builtin/codex@abc123" {
		t.Fatalf("replacement identity = %+v", replacement)
	}
	if len(replacement.ExtraArgs) != 2 || replacement.ExtraArgs[0] != "--model" || replacement.ExtraArgs[1] != "new-model" {
		t.Fatalf("replacement args = %v", replacement.ExtraArgs)
	}
	if len(replacement.Env) != 1 || replacement.Env[0] != "A=B" {
		t.Fatalf("replacement env = %v", replacement.Env)
	}
	text := out.String()
	if !strings.Contains(text, "work") || !strings.Contains(text, "new-model") || !strings.Contains(text, "history retained") {
		t.Fatalf("output = %q", text)
	}
}

func TestSessionSetModelCommandRejectsOwnerHeldSession(t *testing.T) {
	capture := &setModelCaptureClient{}
	withSetModelResolver(t, capture, &sessionEntry{Session: pb.Session{
		Id: "s1", Name: "work", AgentType: "codex", OwnerTaskId: "task-1",
	}})

	cmd := newSessionSetModelCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetArgs([]string{"work", "new-model"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "task or agent instance") {
		t.Fatalf("err = %v, want owner-held rejection", err)
	}
	if capture.gotReplacement != nil {
		t.Fatal("SetModel was called for an owner-held session")
	}
}
