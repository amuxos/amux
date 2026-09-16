package hub

import (
	"context"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHubRestartForwardsReplacementAndPreserveHistory(t *testing.T) {
	srv, stream := newRunScopeHub(t, nil, []*pb.Session{{Id: "s1", Name: "work"}})
	replacement := &pb.RunRequest{
		AgentType: "codex", AgentProfile: "builtin/codex@abc123",
		ExtraArgs: []string{"--model", "new-model"},
	}
	resp, err := srv.Restart(context.Background(), &pb.AmuxdRestartRequest{
		TargetId: "t1", Name: "work", Replacement: replacement, PreserveHistory: true,
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if resp == nil || resp.Session == nil || resp.Session.Name != "work" {
		t.Fatalf("response = %+v", resp)
	}
	got := stream.lastRestartRequest()
	if got == nil {
		t.Fatal("runtime received no Restart request")
	}
	if got.Name != "work" || !got.PreserveHistory || got.RefreshProfile {
		t.Fatalf("routed request = %+v", got)
	}
	if got.Replacement == nil || got.Replacement.AgentType != "codex" ||
		len(got.Replacement.ExtraArgs) != 2 || got.Replacement.ExtraArgs[1] != "new-model" {
		t.Fatalf("routed replacement = %+v", got.Replacement)
	}
}

func TestHubRestartRejectsReplacementWithRefreshProfile(t *testing.T) {
	srv, stream := newRunScopeHub(t, nil, []*pb.Session{{Id: "s1", Name: "work"}})
	_, err := srv.Restart(context.Background(), &pb.AmuxdRestartRequest{
		TargetId: "t1", Name: "work", RefreshProfile: true,
		Replacement: &pb.RunRequest{AgentType: "codex"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Restart err = %v, want InvalidArgument", err)
	}
	if got := stream.lastRestartRequest(); got != nil {
		t.Fatalf("invalid request was routed: %+v", got)
	}
}
