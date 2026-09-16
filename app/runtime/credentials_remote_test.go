package runtime

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestHubCredentialProviderRequestsFilteredMaterial(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	fake := &fakeCredentialHub{}
	pb.RegisterAmuxdServer(grpcSrv, fake)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	defer grpcSrv.Stop()

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	defer conn.Close()

	provider := hubCredentialProvider{client: pb.NewAmuxdClient(conn)}
	mat, err := provider.MaterializeEnv(context.Background(), "openai-compat", "codex", []string{"GLM_API_KEY"})
	if err != nil {
		t.Fatalf("MaterializeEnv: %v", err)
	}
	if fake.got.Profile != "openai-compat" || fake.got.AgentType != "codex" {
		t.Fatalf("request = %+v, want profile openai-compat agent codex", fake.got)
	}
	if got, want := fake.got.EnvNames, []string{"GLM_API_KEY"}; !equalCredentialStrings(got, want) {
		t.Fatalf("env_names = %v, want %v", got, want)
	}
	if got, want := mat.Env, []string{"GLM_API_KEY=central-secret"}; !equalCredentialStrings(got, want) {
		t.Fatalf("materialized env = %v, want %v", got, want)
	}
	if mat.Kind != "long-lived" {
		t.Fatalf("kind = %q, want long-lived", mat.Kind)
	}
}

func TestHubCredentialProviderLooksUpEnvByName(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	fake := &fakeCredentialHub{profiles: []string{"shared-service"}}
	pb.RegisterAmuxdServer(grpcSrv, fake)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	defer grpcSrv.Stop()

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	defer conn.Close()

	provider := hubCredentialProvider{client: pb.NewAmuxdClient(conn)}
	mat, err := provider.MaterializeEnvByName(context.Background(), "codex", []string{"SERVICE_TOKEN"})
	if err != nil {
		t.Fatalf("MaterializeEnvByName: %v", err)
	}
	if fake.got.Profile != "" || fake.got.AgentType != "codex" {
		t.Fatalf("request = %+v, want empty profile agent codex", fake.got)
	}
	if got, want := fake.got.EnvNames, []string{"SERVICE_TOKEN"}; !equalCredentialStrings(got, want) {
		t.Fatalf("env_names = %v, want %v", got, want)
	}
	if got, want := mat.Profiles, []string{"shared-service"}; !equalCredentialStrings(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	if got, want := mat.Env, []string{"SERVICE_TOKEN=central-secret"}; !equalCredentialStrings(got, want) {
		t.Fatalf("materialized env = %v, want %v", got, want)
	}
}

func TestHubCredentialProviderForwardsLaunchActorToken(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	fake := &fakeCredentialHub{}
	pb.RegisterAmuxdServer(grpcSrv, fake)
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	provider := hubCredentialProvider{client: pb.NewAmuxdClient(conn)}
	ctx := task.WithCredentialActorToken(context.Background(), "principal-token")
	if _, err := provider.MaterializeEnv(ctx, "openai-compat", "codex", []string{"GLM_API_KEY"}); err != nil {
		t.Fatalf("MaterializeEnv: %v", err)
	}
	if fake.actorToken != "principal-token" {
		t.Fatalf("actor token = %q, want principal-token", fake.actorToken)
	}
}

func TestHubCredentialProviderRestoresMissingEnvError(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	st, detailErr := status.New(codes.FailedPrecondition, "credential store does not define env PROVIDER_API_KEY").WithDetails(
		&errdetails.ErrorInfo{
			Reason:   "CREDENTIAL_ENV_NOT_FOUND",
			Domain:   "amux.credentials",
			Metadata: map[string]string{"env_name": "PROVIDER_API_KEY"},
		},
	)
	if detailErr != nil {
		t.Fatalf("build missing env status: %v", detailErr)
	}
	fake := &fakeCredentialHub{err: st.Err()}
	pb.RegisterAmuxdServer(grpcSrv, fake)
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	defer conn.Close()

	provider := hubCredentialProvider{client: pb.NewAmuxdClient(conn)}
	_, err = provider.MaterializeEnvByName(context.Background(), "codex", []string{"EXAMPLE_API_KEY", "PROVIDER_API_KEY"})
	var notFound *task.CredentialEnvNotFoundError
	if !errors.As(err, &notFound) || notFound.EnvName != "PROVIDER_API_KEY" {
		t.Fatalf("MaterializeEnvByName error = %v, want typed PROVIDER_API_KEY not found", err)
	}
}

type fakeCredentialHub struct {
	pb.UnimplementedAmuxdServer
	got        *pb.MaterializeCredentialRequest
	profiles   []string
	err        error
	actorToken string
}

func (f *fakeCredentialHub) MaterializeCredential(ctx context.Context, req *pb.MaterializeCredentialRequest) (*pb.MaterializeCredentialResponse, error) {
	f.got = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		values := md.Get("x-amux-actor-token")
		if len(values) > 0 {
			f.actorToken = values[0]
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &pb.MaterializeCredentialResponse{
		Kind:     "long-lived",
		Env:      []string{req.EnvNames[0] + "=central-secret"},
		Profiles: append([]string(nil), f.profiles...),
	}, nil
}

var _ task.EnvCredentialProvider = hubCredentialProvider{}
var _ task.EnvLookupCredentialProvider = hubCredentialProvider{}

func equalCredentialStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
