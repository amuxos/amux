package hub

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/memory"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const memoryTransportHubToken = "memory-test-hub-token"

// These tests cross the actual protobuf/gRPC boundary and production Hub
// authentication interceptor. Separate connections represent callers on
// different Runtimes; no Runtime cache or local filesystem is involved.
func memoryTransport(t *testing.T, server *hubServer) func(string, string) pb.AmuxdClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(hubUnaryAuthInterceptor(memoryTransportHubToken)))
	pb.RegisterAmuxdServer(grpcServer, server)
	done := make(chan error, 1)
	go func() { done <- grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
		if err := <-done; err != nil && err != grpc.ErrServerStopped {
			t.Errorf("memory transport stopped: %v", err)
		}
	})
	return func(hubToken, actorToken string) pb.AmuxdClient {
		t.Helper()
		conn, err := grpc.NewClient("passthrough:///memory-test",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
			grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				ctx = metadata.AppendToOutgoingContext(ctx,
					"x-amux-hub-token", hubToken, "x-amux-actor-token", actorToken)
				return invoke(ctx, method, req, reply, conn, opts...)
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return pb.NewAmuxdClient(conn)
	}
}

func memoryTransportAgentToken(t *testing.T, s *hubServer, p principal.Principal, launch string) string {
	t.Helper()
	credential, err := s.principals.IssueAgentCredential(p.ID, launch, principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	return credential.Token
}

func memoryTransportBinding(t *testing.T, ctx context.Context, client pb.AmuxdClient) string {
	t.Helper()
	result, err := client.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
	if err != nil || !result.GetEnabled() || result.GetBinding() == "" {
		t.Fatalf("transport binding: %+v, %v", result, err)
	}
	return result.Binding
}

func TestMemoryTransportGlobalSharingAndConcurrentCAS(t *testing.T) {
	s := memoryTestServer(t, false)
	connect := memoryTransport(t, s)
	alice, bob := memoryTestPrincipal(t, s, "alice"), memoryTestPrincipal(t, s, "bob")
	first := connect(memoryTransportHubToken, memoryTransportAgentToken(t, s, alice, "runtime-one:raw"))
	second := connect(memoryTransportHubToken, memoryTransportAgentToken(t, s, bob, "runtime-two:profile"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, client := range []pb.AmuxdClient{first, second} {
		if binding := memoryTransportBinding(t, ctx, client); binding != "global" {
			t.Fatalf("global binding = %q", binding)
		}
	}
	written, err := first.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{
		Path: "projects/release.md", Content: "# 发布\n发布前运行 ReleaseTests", ExpectedRevision: "absent", Binding: "global",
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := second.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: "global"})
	if err != nil || !doc.GetExists() || doc.GetContent() != "# 发布\n发布前运行 ReleaseTests" || doc.GetRevision() != written.Revision {
		t.Fatalf("cross-client read: %+v, %v", doc, err)
	}
	search, err := second.MemorySearch(ctx, &pb.MemorySearchRequest{Query: "releasetests 发布", PathPrefix: "projects/", Binding: "global"})
	if err != nil || len(search.GetMatches()) != 1 || search.GetMatches()[0].GetRevision() != written.Revision || search.GetIncomplete() || search.GetScannedFiles() != 1 {
		t.Fatalf("cross-client search: %+v, %v", search, err)
	}
	// Competing clients must not silently overwrite each other's shared write;
	// the loser receives the new revision in structured gRPC status details.
	type writeOutcome struct {
		result *pb.MemoryWriteResponse
		err    error
	}
	results := make(chan writeOutcome, 2)
	var wg sync.WaitGroup
	for i, client := range []pb.AmuxdClient{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{
				Path: written.Path, Content: []string{"first client's update", "second client's update"}[i], ExpectedRevision: written.Revision, Binding: "global",
			})
			results <- writeOutcome{result, err}
		}()
	}
	wg.Wait()
	close(results)
	var winningRevision, conflictRevision string
	for outcome := range results {
		if outcome.err == nil {
			if winningRevision != "" {
				t.Fatal("both competing writes succeeded")
			}
			winningRevision = outcome.result.GetRevision()
			continue
		}
		if status.Code(outcome.err) != codes.Aborted {
			t.Fatalf("CAS transport status = %v", outcome.err)
		}
		for _, detail := range status.Convert(outcome.err).Details() {
			if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "REVISION_CONFLICT" && info.Domain == "amux.memory" {
				conflictRevision = info.Metadata["current_revision"]
			}
		}
	}
	if winningRevision == "" || winningRevision != conflictRevision || winningRevision == written.Revision {
		t.Fatalf("lost CAS details across protobuf transport: winner=%q conflict=%q", winningRevision, conflictRevision)
	}
	for i, client := range []pb.AmuxdClient{first, second} {
		deleted, err := client.MemoryForget(ctx, &pb.MemoryForgetRequest{Path: written.Path, ExpectedRevision: winningRevision, Binding: "global"})
		if err != nil || deleted.GetAlreadyAbsent() != (i == 1) {
			t.Fatalf("delete retry %d: %+v, %v", i, deleted, err)
		}
	}
}

func TestMemoryTransportPrivateEffectivePrincipalAndBinding(t *testing.T) {
	s := memoryTestServer(t, true)
	connect := memoryTransport(t, s)
	alice, bob := memoryTestPrincipal(t, s, "alice"), memoryTestPrincipal(t, s, "bob")
	a1 := connect(memoryTransportHubToken, memoryTransportAgentToken(t, s, alice, "runtime-one"))
	a2 := connect(memoryTransportHubToken, memoryTransportAgentToken(t, s, alice, "runtime-two"))
	b := connect(memoryTransportHubToken, memoryTransportAgentToken(t, s, bob, "runtime-bob"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ab, bb := memoryTransportBinding(t, ctx, a1), memoryTransportBinding(t, ctx, b)
	if ab != "principal:"+alice.ID || bb != "principal:"+bob.ID || memoryTransportBinding(t, ctx, a2) != ab {
		t.Fatal("transport binding did not preserve authenticated principal")
	}
	written, err := a1.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "private-answer-preference", ExpectedRevision: "absent", Binding: ab})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := a2.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: ab})
	if err != nil || doc.GetContent() != "private-answer-preference" {
		t.Fatalf("same principal on separate connection: %+v, %v", doc, err)
	}
	doc, err = b.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: bb})
	if err != nil || doc.GetExists() {
		t.Fatalf("other principal observed a document: %+v, %v", doc, err)
	}
	search, err := b.MemorySearch(ctx, &pb.MemorySearchRequest{Query: "private-answer-preference", Binding: bb})
	if err != nil || len(search.GetMatches()) != 0 {
		t.Fatalf("other principal searched private content: %+v, %v", search, err)
	}
	// A bearer credential authenticates an effective Principal. Binding is a
	// precondition, never authority to select somebody else's storage scope.
	for _, binding := range []string{ab, "global", ""} {
		calls := []func() error{
			func() error {
				_, err := b.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: binding})
				return err
			},
			func() error {
				_, err := b.MemorySearch(ctx, &pb.MemorySearchRequest{Query: "private", Binding: binding})
				return err
			},
			func() error {
				_, err := b.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: written.Path, Content: "overwrite", ExpectedRevision: written.Revision, Binding: binding})
				return err
			},
			func() error {
				_, err := b.MemoryForget(ctx, &pb.MemoryForgetRequest{Path: written.Path, ExpectedRevision: written.Revision, Binding: binding})
				return err
			},
		}
		for i, call := range calls {
			if err := call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("forged/missing binding %q call %d: %v", binding, i, err)
			}
		}
	}
	service, err := s.principals.Create(principal.Principal{Kind: principal.KindService, DisplayName: "release-service"})
	if err != nil {
		t.Fatal(err)
	}
	serviceToken, _ := s.issueAgentActorToken(principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: alice.ID, EffectivePrincipalID: service.ID,
	}, "service-scheduled-execution")
	serviceClient := connect(memoryTransportHubToken, serviceToken)
	sb := memoryTransportBinding(t, ctx, serviceClient)
	if sb != "principal:"+service.ID {
		t.Fatalf("run_as used the origin actor instead of effective principal: %q", sb)
	}
	doc, err = serviceClient.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: sb})
	if err != nil || doc.GetExists() {
		t.Fatalf("service saw originating human memory: %+v, %v", doc, err)
	}
	_, err = serviceClient.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "MEMORY.md", Content: "service preference", ExpectedRevision: "absent", Binding: sb})
	if err != nil {
		t.Fatal(err)
	}
	doc, err = a2.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: ab})
	if err != nil || doc.GetContent() != "private-answer-preference" {
		t.Fatalf("service write altered originating human memory: %+v, %v", doc, err)
	}
	if err := s.principals.RevokeAgentCredentials("runtime-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := a1.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: ab}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked launch still accessed memory: %v", err)
	}
	if _, err := a2.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Binding: ab}); err != nil {
		t.Fatalf("revoking one launch broke another: %v", err)
	}
}

func TestMemoryTransportRequiresHubAndActorAuthentication(t *testing.T) {
	s := memoryTestServer(t, true)
	connect := memoryTransport(t, s)
	alice := memoryTestPrincipal(t, s, "alice")
	actorToken := memoryTransportAgentToken(t, s, alice, "authenticated-agent")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, test := range []struct{ name, hubToken, actorToken string }{
		{"missing Hub token", "", actorToken},
		{"wrong Hub token", "wrong", actorToken},
		{"missing actor token", memoryTransportHubToken, ""},
		{"invalid actor token", memoryTransportHubToken, "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := connect(test.hubToken, test.actorToken)
			if _, err := client.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("binding bypassed authentication: %v", err)
			}
			if _, err := client.MemoryGet(ctx, &pb.MemoryGetRequest{Path: "MEMORY.md", Binding: "principal:" + alice.ID}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("read bypassed authentication: %v", err)
			}
		})
	}
}

func TestMemoryTransportBoundedUnicodeContinuation(t *testing.T) {
	s := memoryTestServer(t, false)
	connect := memoryTransport(t, s)
	// An explicitly non-isolated human CLI uses Hub authentication and the
	// system scope without manufacturing a delegated actor credential.
	client := connect(memoryTransportHubToken, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	content := strings.Repeat("中文\t\\", 5000)
	written, err := client.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: "long.md", Content: content, ExpectedRevision: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	var offset int64
	for range 100 {
		doc, err := client.MemoryGet(ctx, &pb.MemoryGetRequest{Path: written.Path, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if doc.GetRevision() != written.Revision || !utf8.ValidString(doc.GetContent()) || proto.Size(doc) > memory.MaxOutputBytes {
			t.Fatalf("invalid bounded wire document: bytes=%d revision=%q", proto.Size(doc), doc.GetRevision())
		}
		got.WriteString(doc.GetContent())
		if !doc.GetTruncated() {
			break
		}
		if doc.GetNextOffset() <= offset {
			t.Fatalf("transport continuation did not advance: %+v", doc)
		}
		offset = doc.NextOffset
	}
	if got.String() != content {
		t.Fatalf("Unicode continuation lost content: got %d bytes, want %d", got.Len(), len(content))
	}
	for _, req := range []*pb.MemoryGetRequest{
		{Path: "../outside.md"}, {Path: written.Path, Offset: 1}, {Path: written.Path, Offset: 1<<63 - 1},
	} {
		if _, err := client.MemoryGet(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid read returned wrong gRPC status: %v", err)
		}
	}
	if _, err := client.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{
		Path: "MEMORY.md", Content: strings.Repeat("a", memory.MaxEntryBytes+1), ExpectedRevision: "absent",
	}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("entry capacity returned wrong gRPC status: %v", err)
	}
}
