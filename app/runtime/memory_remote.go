package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/metadata"
)

// newHubMemoryBindingResolver runs once for a new conversation, never per turn.
// The Hub derives scope from its policy and the effective launch credential.
func newHubMemoryBindingResolver(addr, token string) func(context.Context, string) (string, error) {
	p := hubCredentialProvider{addr: strings.TrimSpace(addr), token: strings.TrimSpace(token)}
	return func(ctx context.Context, actorToken string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		client, cleanup, err := p.clientForCall(ctx)
		if err != nil {
			return "", err
		}
		defer cleanup()
		// Resolve using the effective launch credential, never an inherited
		// caller's metadata (which may identify the human requesting run_as).
		md, _ := metadata.FromOutgoingContext(ctx)
		md = md.Copy()
		md.Delete("x-amux-hub-token")
		md.Delete("x-amux-actor-token")
		if p.token != "" {
			md.Set("x-amux-hub-token", p.token)
		}
		if actorToken != "" {
			md.Set("x-amux-actor-token", actorToken)
		}
		ctx = metadata.NewOutgoingContext(ctx, md)
		result, err := client.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
		if err != nil {
			return "", err
		}
		if !result.GetEnabled() {
			return "", nil
		}
		binding := result.GetBinding()
		if binding != "global" && (!strings.HasPrefix(binding, "principal:") || strings.TrimPrefix(binding, "principal:") == "") {
			return "", fmt.Errorf("Hub returned an invalid memory binding")
		}
		return binding, nil
	}
}
