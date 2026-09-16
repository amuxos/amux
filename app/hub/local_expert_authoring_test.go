package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type localExpertTestContract struct{}

func (localExpertTestContract) Init(ctx context.Context, target string) error {
	return (&fakeAuthoringContract{}).Init(ctx, target)
}

func (localExpertTestContract) Check(ctx context.Context, target string) error {
	return (&fakeAuthoringContract{}).Check(ctx, target)
}

// fakeAuthoringContract keeps this control-plane test focused by delegating to
// the real test executable fixture installed below.
type fakeAuthoringContract struct{}

func (fakeAuthoringContract) Init(_ context.Context, target string) error {
	return writeAuthoringContractFixture(target)
}

func (fakeAuthoringContract) Check(_ context.Context, target string) error {
	return renderAuthoringContractFixture(target)
}

func writeAuthoringContractFixture(target string) error {
	if err := os.MkdirAll(filepath.Join(target, "dist"), 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "agent-pack.toml"), []byte("schema_version = \"v1.1\"\nname = \"local\"\nnamespace = \"local\"\n"), 0600)
}

func renderAuthoringContractFixture(target string) error {
	entries, err := os.ReadDir(filepath.Join(target, "profiles", "common"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	profiles := map[string]any{}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".toml")
		names = append(names, name)
		profiles[name] = map[string]any{
			"namespace": "common", "type": "codex", "providers": []string{},
			"instructions": []string{"instructions/common/" + name + "/AGENTS.md"},
			"skills":       []string{}, "subagents": []string{}, "permission": map[string]any{"permission_mode": "default"},
		}
	}
	manifest := map[string]any{
		"schema_version": "v1.1", "pack_namespace": "local", "profiles": profiles,
		"providers": map[string]any{}, "namespaces": map[string]any{"common": map[string]any{"profiles": names}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(target, "dist"), 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "dist", "agent-pack.manifest.json"), data, 0600)
}

func TestLocalExpertRPCRequiresLaterOwnerTurnForPublish(t *testing.T) {
	root := t.TempDir()
	library := agentprofile.NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		localExpertTestContract{},
	)
	created, err := library.Create(context.Background(), agentprofile.ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := agentprofile.NewManagedAuthoringReceiptSigner([]byte("01234567890123456789012345678901"), time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	turnID := "turn-preview"
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := principals.IssueAgentCredential(p.ID, "launch-owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", credential.Token))
	server := &hubServer{
		principals: principals,
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-1", Name: "work", LaunchId: "launch-owner"}}}}
		},
		localExpertAuthoring: &localExpertAuthoringControl{library: library, receipts: receipts},
		getLocalExpertAuthoringTurn: func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error) {
			return &pb.LocalExpertAuthoringTurnResponse{Active: true, Allowed: true, TurnId: turnID}, nil
		},
	}
	read, err := server.ReadLocalExpert(ctx, &pb.ReadLocalExpertRequest{
		SessionId: "session-1", Operation: "update", TargetRef: "local/reviewer",
	})
	if err != nil || read.GetBaseRevision() != created.Revision {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	patch := "diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md\n--- a/instructions/common/reviewer/AGENTS.md\n+++ b/instructions/common/reviewer/AGENTS.md\n@@ -1 +1 @@\n-old\n+new\n"
	request := &pb.ChangeLocalExpertRequest{
		SessionId: "session-1", Mode: "preview", Operation: "update", TargetRef: "local/reviewer",
		BaseRevision: created.Revision, Patch: patch,
	}
	preview, err := server.ChangeLocalExpert(ctx, request)
	if err != nil || preview.GetPreviewReceipt() == "" || !strings.Contains(preview.GetDiff(), "+new") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if preview.GetEvaluationProfileRef() != "" || preview.GetEvaluationExpiresAt() != 0 {
		t.Fatalf("plain preview unexpectedly prepared evaluation: %+v", preview)
	}
	request.Mode = "publish"
	request.PreviewReceipt = preview.GetPreviewReceipt()
	if _, err := server.ChangeLocalExpert(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("same-turn publish error=%v", err)
	}
	turnID = "turn-confirm"
	request.BaseRevision = ""
	if _, err := server.ChangeLocalExpert(ctx, request); status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "base_revision") {
		t.Fatalf("missing base_revision publish error=%v, want field-specific FailedPrecondition", err)
	}
	request.BaseRevision = created.Revision
	published, err := server.ChangeLocalExpert(ctx, request)
	if err != nil || !published.GetPublished() || published.GetRevision() == created.Revision {
		t.Fatalf("published=%+v err=%v", published, err)
	}
}

func TestLocalExpertRPCPreparesAndCleansEvaluationCandidate(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := agentprofile.NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, localExpertTestContract{})
	created, err := library.Create(context.Background(), agentprofile.ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := agentprofile.NewManagedAuthoringReceiptSigner([]byte("01234567890123456789012345678901"), time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := principals.IssueAgentCredential(p.ID, "launch-owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", credential.Token))
	server := &hubServer{
		principals: principals,
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-1", Name: "work", LaunchId: "launch-owner"}}}}
		},
		localExpertAuthoring: &localExpertAuthoringControl{library: library, receipts: receipts},
		getLocalExpertAuthoringTurn: func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error) {
			return &pb.LocalExpertAuthoringTurnResponse{Active: true, Allowed: true, TurnId: "turn-preview"}, nil
		},
	}
	patch := "diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md\n--- a/instructions/common/reviewer/AGENTS.md\n+++ b/instructions/common/reviewer/AGENTS.md\n@@ -1 +1 @@\n-old\n+candidate\n"
	preview, err := server.ChangeLocalExpert(ctx, &pb.ChangeLocalExpertRequest{
		SessionId: "session-1", Mode: "preview", Operation: "update", TargetRef: "local/reviewer",
		BaseRevision: created.Revision, Patch: patch, PrepareEvaluation: true,
	})
	if err != nil || preview.GetEvaluationProfileRef() == "" || preview.GetEvaluationExpiresAt() <= time.Now().Unix() {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	evaluated, err := agentprofile.ResolveProfile(registryPath, preview.GetEvaluationProfileRef())
	if err != nil || !strings.Contains(evaluated.AgentsContent, "candidate") {
		t.Fatalf("evaluation profile=%+v err=%v", evaluated, err)
	}
	cleaned, err := server.ChangeLocalExpert(ctx, &pb.ChangeLocalExpertRequest{
		SessionId: "session-1", Mode: "cleanup", PreviewReceipt: preview.GetPreviewReceipt(),
	})
	if err != nil || cleaned.GetMode() != "cleanup" {
		t.Fatalf("cleanup=%+v err=%v", cleaned, err)
	}
	if _, err := agentprofile.ResolveProfile(registryPath, preview.GetEvaluationProfileRef()); !agentprofile.IsProfileNotFound(err) {
		t.Fatalf("candidate after cleanup error=%v", err)
	}
}

func TestLocalExpertAuthoringRejectsUnauthenticatedSessionCaller(t *testing.T) {
	server := &hubServer{
		principals: agentTokenTestStore(t),
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-1", Name: "work"}}}}
		},
		getLocalExpertAuthoringTurn: func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error) {
			return &pb.LocalExpertAuthoringTurnResponse{Active: true, Allowed: true, TurnId: "turn-owner"}, nil
		},
	}
	if _, err := server.authorizeLocalExpertTurn(context.Background(), "session-1"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("authorize error = %v, want PermissionDenied", err)
	}
}

func TestLocalExpertAuthoringRejectsUnauthenticatedCallerWithoutPrincipalStore(t *testing.T) {
	server := &hubServer{
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-1", Name: "work"}}}}
		},
		getLocalExpertAuthoringTurn: func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error) {
			return &pb.LocalExpertAuthoringTurnResponse{Active: true, Allowed: true, TurnId: "turn-owner"}, nil
		},
	}
	if _, err := server.authorizeLocalExpertTurn(context.Background(), "session-1"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("authorize error = %v, want PermissionDenied", err)
	}
}

func TestLocalExpertAuthoringAcceptsCredentialFromCurrentSession(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := principals.IssueAgentCredential(p.ID, "launch-owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server := &hubServer{
		principals: principals,
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{
				Id: "session-1", Name: "work", LaunchId: "launch-owner",
			}}}}
		},
		getLocalExpertAuthoringTurn: func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error) {
			return &pb.LocalExpertAuthoringTurnResponse{Active: true, Allowed: true, TurnId: "turn-owner"}, nil
		},
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", credential.Token))
	turnID, err := server.authorizeLocalExpertTurn(ctx, "session-1")
	if err != nil || turnID != "turn-owner" {
		t.Fatalf("authorize turn = %q, err=%v", turnID, err)
	}
}
