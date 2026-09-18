package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/memory"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func memoryTestServer(t *testing.T, isolated bool) *hubServer {
	t.Helper()
	store, err := memory.New(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	return &hubServer{
		memory: store, principals: agentTokenTestStore(t),
		memoryConfig: func() config.MemoryConfig { return config.MemoryConfig{Enabled: true} },
		isolationConfig: func() (config.IsolationConfig, error) {
			return config.IsolationConfig{Enabled: isolated, Backend: "auto"}, nil
		},
	}
}

func memoryTestPrincipal(t *testing.T, s *hubServer, name string) principal.Principal {
	t.Helper()
	p, err := s.principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: name})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func memoryTestAgent(t *testing.T, s *hubServer, p principal.Principal, launch string) context.Context {
	t.Helper()
	credential, err := s.principals.IssueAgentCredential(p.ID, launch, principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", credential.Token))
}

func memoryTestBinding(t *testing.T, s *hubServer, ctx context.Context) string {
	t.Helper()
	result, err := s.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
	if err != nil || !result.GetEnabled() || result.GetBinding() == "" {
		t.Fatalf("binding = %v, err = %v", result, err)
	}
	return result.Binding
}

func TestMemoryGlobalSharedAcrossActorsAndProfiles(t *testing.T) {
	s := memoryTestServer(t, false)
	alice := memoryTestAgent(t, s, memoryTestPrincipal(t, s, "alice"), "runtime-one:raw")
	bob := memoryTestAgent(t, s, memoryTestPrincipal(t, s, "bob"), "runtime-two:profile")
	if got := memoryTestBinding(t, s, alice); got != "global" {
		t.Fatalf("binding = %q", got)
	}
	written, err := s.MemoryUpsert(alice, &pb.MemoryUpsertRequest{Path: "projects/release.md", Content: "发布前运行测试", ExpectedRevision: "absent", Binding: "global"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.MemoryGet(bob, &pb.MemoryGetRequest{Path: written.Path, Binding: "global"})
	if err != nil || doc.GetContent() != "发布前运行测试" {
		t.Fatalf("shared read = %v, %v", doc, err)
	}
	// Human CLI without an actor is valid only for the explicit global policy.
	result, err := s.MemorySearch(context.Background(), &pb.MemorySearchRequest{Query: "发布"})
	if err != nil || len(result.GetMatches()) != 1 {
		t.Fatalf("global search = %v, %v", result, err)
	}
}

func TestMemoryIsolationUsesPrincipalWithoutAnyRuntimeOrHelper(t *testing.T) {
	s := memoryTestServer(t, true)
	alice := memoryTestPrincipal(t, s, "alice")
	a1 := memoryTestAgent(t, s, alice, "runtime-one")
	a2 := memoryTestAgent(t, s, alice, "runtime-two")
	bob := memoryTestAgent(t, s, memoryTestPrincipal(t, s, "bob"), "runtime-bob")
	ab := memoryTestBinding(t, s, a1)
	bb := memoryTestBinding(t, s, bob)
	if ab != "principal:"+alice.ID || memoryTestBinding(t, s, a2) != ab || bb == ab {
		t.Fatal("binding is not derived from the effective principal")
	}
	_, err := s.MemoryUpsert(a1, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "回答简洁", ExpectedRevision: "absent", Binding: ab})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.MemoryGet(a2, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: ab})
	if err != nil || doc.GetContent() != "回答简洁" {
		t.Fatalf("same-principal read = %v, %v", doc, err)
	}
	doc, err = s.MemoryGet(bob, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: bb})
	if err != nil || doc.GetExists() {
		t.Fatalf("other principal observed private memory: %v, %v", doc, err)
	}
	search, err := s.MemorySearch(bob, &pb.MemorySearchRequest{Query: "简洁", Binding: bb})
	if err != nil || len(search.GetMatches()) != 0 {
		t.Fatalf("other principal search = %v, %v", search, err)
	}
	_, err = s.MemoryGet(bob, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: ab})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("forged binding = %v", err)
	}
	_, err = s.MemoryGet(context.Background(), &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: ab})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous private read = %v", err)
	}
	_, err = s.MemoryGet(a1, &pb.MemoryGetRequest{Path: "MEMORY.md"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unmanaged agent read = %v", err)
	}
	if err := s.principals.Disable(alice.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.MemoryGet(a2, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: ab})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled principal read = %v", err)
	}
}

func TestMemoryEffectiveServicePrincipalOwnsRunAsMemory(t *testing.T) {
	s := memoryTestServer(t, true)
	actor := memoryTestPrincipal(t, s, "human")
	service, err := s.principals.Create(principal.Principal{Kind: principal.KindService, DisplayName: "service"})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.issueAgentActorToken(principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: actor.ID, EffectivePrincipalID: service.ID,
	}, "scheduled-service-execution")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-actor-token", token))
	if got := memoryTestBinding(t, s, ctx); got != "principal:"+service.ID {
		t.Fatalf("service binding = %q", got)
	}
}

func TestMemoryPolicyChangeDoesNotMergeOrReuseFrozenScope(t *testing.T) {
	s := memoryTestServer(t, true)
	ctx := memoryTestAgent(t, s, memoryTestPrincipal(t, s, "alice"), "launch")
	binding := memoryTestBinding(t, s, ctx)
	_, err := s.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "private", ExpectedRevision: "absent", Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	s.isolationConfig = func() (config.IsolationConfig, error) { return config.IsolationConfig{}, nil }
	_, err = s.MemoryGet(ctx, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: binding})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("old scope = %v", err)
	}
	doc, err := s.MemoryGet(context.Background(), &pb.MemoryGetRequest{Path: "MEMORY.md"})
	if err != nil || doc.GetExists() {
		t.Fatalf("private memory copied to global: %v, %v", doc, err)
	}
	s.isolationConfig = func() (config.IsolationConfig, error) { return config.IsolationConfig{}, errors.New("invalid policy") }
	_, err = s.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("invalid policy fell back to global: %v", err)
	}
}

func TestMemoryDisabledPreservesFilesAndConflictsReturnRevision(t *testing.T) {
	s := memoryTestServer(t, false)
	ctx := context.Background()
	written, err := s.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "remember", ExpectedRevision: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "overwrite", ExpectedRevision: "absent"})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("concurrent write = %v", err)
	}
	details := status.Convert(err).Details()
	if len(details) != 1 || details[0].(*errdetails.ErrorInfo).Metadata["current_revision"] != written.Revision {
		t.Fatalf("conflict details = %v", details)
	}
	s.memoryConfig = func() config.MemoryConfig { return config.MemoryConfig{} }
	binding, err := s.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
	if err != nil || binding.GetEnabled() {
		t.Fatalf("disabled binding = %v, %v", binding, err)
	}
	for name, call := range map[string]func() error{
		"get": func() error {
			_, err := s.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: "global"})
			return err
		},
		"search": func() error {
			_, err := s.MemorySearch(ctx, &pb.MemorySearchRequest{Query: "remember", Binding: "global"})
			return err
		},
		"upsert": func() error {
			_, err := s.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: written.Path, Content: "changed", ExpectedRevision: written.Revision, Binding: "global"})
			return err
		},
		"forget": func() error {
			_, err := s.MemoryForget(ctx, &pb.MemoryForgetRequest{Path: written.Path, ExpectedRevision: written.Revision, Binding: "global"})
			return err
		},
	} {
		if err := call(); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("disabled %s = %v", name, err)
		}
	}
	s.memoryConfig = func() config.MemoryConfig { return config.MemoryConfig{Enabled: true} }
	doc, err := s.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path})
	if err != nil || doc.GetContent() != "remember" {
		t.Fatalf("disabled memory lost data: %v, %v", doc, err)
	}
	invalid := metadata.NewIncomingContext(ctx, metadata.Pairs("x-amux-actor-token", "invalid"))
	_, err = s.MemoryGet(invalid, &pb.MemoryGetRequest{Path: written.Path})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid explicit actor accepted in global mode: %v", err)
	}
}
