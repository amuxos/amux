package runtime

import (
	"context"
	"net"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type bindingTestHub struct{ pb.UnimplementedAmuxdServer }

func (bindingTestHub) GetMemoryBinding(ctx context.Context, _ *pb.MemoryBindingRequest) (*pb.MemoryBindingResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	hub := md.Get("x-amux-hub-token")
	if len(hub) != 1 || hub[0] != "hub-token" {
		return nil, status.Error(codes.Unauthenticated, "wrong Hub credential")
	}
	actor := md.Get("x-amux-actor-token")
	if len(actor) == 0 {
		return &pb.MemoryBindingResponse{Enabled: true, Binding: "global"}, nil
	}
	if len(actor) != 1 {
		return nil, status.Error(codes.Unauthenticated, "ambiguous actor")
	}
	switch actor[0] {
	case "effective-service":
		return &pb.MemoryBindingResponse{Enabled: true, Binding: "principal:service"}, nil
	case "disabled":
		return &pb.MemoryBindingResponse{}, nil
	case "invalid-binding":
		return &pb.MemoryBindingResponse{Enabled: true, Binding: "principal:"}, nil
	default:
		return nil, status.Error(codes.Unauthenticated, "wrong effective actor")
	}
}

func TestMemoryRemoteBindingUsesEffectiveCredential(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterAmuxdServer(srv, bindingTestHub{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	resolve := newHubMemoryBindingResolver(lis.Addr().String(), "hub-token")
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-amux-hub-token", "inherited-hub", "x-amux-actor-token", "human-requesting-run-as"))
	for _, tc := range []struct{ actor, binding string }{
		{"effective-service", "principal:service"}, {"", "global"}, {"disabled", ""},
	} {
		got, err := resolve(ctx, tc.actor)
		if err != nil || got != tc.binding {
			t.Fatalf("actor %q: binding=%q, err=%v", tc.actor, got, err)
		}
	}
	if _, err := resolve(ctx, "invalid-binding"); err == nil {
		t.Fatal("invalid Hub binding accepted")
	}
	if got, err := resolve(ctx, "revoked"); got != "" || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("authentication failure became a binding: %q, %v", got, err)
	}
}
