package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/task"
	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type hubCredentialProvider struct {
	addr   string
	token  string
	client pb.AmuxdClient
}

func newHubCredentialProvider(addr, token string) task.CredentialProvider {
	return hubCredentialProvider{addr: strings.TrimSpace(addr), token: strings.TrimSpace(token)}
}

func (p hubCredentialProvider) Materialize(ctx context.Context, profile, agentType string) (task.CredentialMaterial, error) {
	return p.MaterializeEnv(ctx, profile, agentType, nil)
}

func (p hubCredentialProvider) MaterializeEnv(ctx context.Context, profile, agentType string, envNames []string) (task.CredentialMaterial, error) {
	return p.materialize(ctx, profile, agentType, envNames)
}

func (p hubCredentialProvider) MaterializeEnvByName(ctx context.Context, agentType string, envNames []string) (task.CredentialMaterial, error) {
	return p.materialize(ctx, "", agentType, envNames)
}

func (p hubCredentialProvider) materialize(ctx context.Context, profile, agentType string, envNames []string) (task.CredentialMaterial, error) {
	client, cleanup, err := p.clientForCall(ctx)
	if err != nil {
		return task.CredentialMaterial{}, err
	}
	defer cleanup()
	callCtx := ctx
	if p.token != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-amux-hub-token", p.token)
	}
	if actorToken := task.CredentialActorToken(ctx); actorToken != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-amux-actor-token", actorToken)
	}
	resp, err := client.MaterializeCredential(callCtx, &pb.MaterializeCredentialRequest{
		Profile:   profile,
		AgentType: agentType,
		EnvNames:  append([]string(nil), envNames...),
	})
	if err != nil {
		return task.CredentialMaterial{}, restoreHubCredentialError(err)
	}
	return task.CredentialMaterial{
		Env:      append([]string(nil), resp.Env...),
		Files:    copyCredentialFiles(resp.Files),
		Secrets:  append([]string(nil), resp.Secrets...),
		Kind:     resp.Kind,
		Profiles: append([]string(nil), resp.Profiles...),
	}, nil
}

func restoreHubCredentialError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.Domain != "amux.credentials" {
			continue
		}
		restored, ok := task.RestoreCredentialEnvError(task.CredentialEnvErrorDescriptor{
			Reason:        info.Reason,
			EnvName:       info.Metadata["env_name"],
			FirstProfile:  info.Metadata["first_profile"],
			SecondProfile: info.Metadata["second_profile"],
		})
		if ok {
			return restored
		}
	}
	return err
}

func (p hubCredentialProvider) clientForCall(ctx context.Context) (pb.AmuxdClient, func(), error) {
	if p.client != nil {
		return p.client, func() {}, nil
	}
	if p.addr == "" {
		return nil, func() {}, fmt.Errorf("hub credential provider requires hub address")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}, transport.GRPCClientDialOptions()...)
	conn, err := grpc.DialContext(dialCtx, p.addr, opts...)
	cancel()
	if err != nil {
		return nil, func() {}, fmt.Errorf("dial hub credential store: %w", err)
	}
	return pb.NewAmuxdClient(conn), func() { _ = conn.Close() }, nil
}

func copyCredentialFiles(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
