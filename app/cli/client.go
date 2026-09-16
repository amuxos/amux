package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// sessionEntry is a session with optional target routing info.
type sessionEntry struct {
	pb.Session
	TargetID   string
	TargetName string
}

// sessionClient is the unified interface for talking to the hub-routed session
// control plane.
type sessionClient interface {
	Run(ctx context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error)
	List(ctx context.Context) ([]*sessionEntry, error)
	Kill(ctx context.Context, targetID, name string) error
	Restart(ctx context.Context, targetID, name string, refreshProfile bool) error
	SetModel(ctx context.Context, targetID, name string, replacement *pb.RunRequest) (*pb.RunResponse, error)
	ChangeCwd(ctx context.Context, targetID string, req *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error)
	Sleep(ctx context.Context, targetID, name string) error
	Resume(ctx context.Context, targetID, name string) error
	Prune(ctx context.Context, targetID string) (int32, []string, error)
	Interrupt(ctx context.Context, targetID, name string) error
	Attach(ctx context.Context, targetID, name string) (pb.Amuxlet_AttachClient, error)
	Submit(ctx context.Context, targetID, name, text string) error
	SubmitAsync(ctx context.Context, targetID, name, text string) error
	SubmitPreempt(ctx context.Context, targetID, name, text string) error
	QueueList(ctx context.Context, targetID, name string) ([]*pb.QueueItem, error)
	QueueClear(ctx context.Context, targetID, name string) (int32, error)
	QueueCancel(ctx context.Context, targetID, name, id string) error
	RespondPermission(ctx context.Context, targetID, name, permissionID, optionID string) error
	Close() error
}

// ── local runtime maintenance client ─────────────────────────────────────────

// localRuntimeClient is reserved for installation maintenance that must still
// work when the hub is unavailable. Ordinary session commands use hubClient.
type localRuntimeClient struct {
	c    pb.AmuxletClient
	conn *grpc.ClientConn
}

func newLocalRuntimeClient() (*localRuntimeClient, error) {
	return newLocalRuntimeClientWithTimeout(3 * time.Second)
}

func newLocalRuntimeClientWithTimeout(timeout time.Duration) (*localRuntimeClient, error) {
	sockPath := filepath.Join(defaultAmuxDir(), "runtime.sock")
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}, transport.GRPCClientDialOptions()...)
	conn, err := grpc.DialContext(ctx, "unix://"+sockPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to local runtime at %s: %w\nRun `amux system start` first.", sockPath, err)
	}
	return &localRuntimeClient{c: pb.NewAmuxletClient(conn), conn: conn}, nil
}

func (lc *localRuntimeClient) Close() error { return lc.conn.Close() }

func (lc *localRuntimeClient) Info(ctx context.Context) (*pb.InfoResponse, error) {
	return lc.c.Info(ctx, &pb.InfoRequest{})
}

func (lc *localRuntimeClient) List(ctx context.Context) ([]*sessionEntry, error) {
	resp, err := lc.c.List(ctx, &pb.ListRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]*sessionEntry, len(resp.Sessions))
	for i, s := range resp.Sessions {
		out[i] = &sessionEntry{Session: *s}
	}
	return out, nil
}

func (lc *localRuntimeClient) Restart(ctx context.Context, name string, refreshProfile bool) error {
	_, err := lc.c.Restart(ctx, &pb.RestartRequest{Name: name, RefreshProfile: refreshProfile})
	return err
}

// ── hub client ───────────────────────────────────────────────────────────────

type hubClient struct {
	c    pb.AmuxdClient
	conn *grpc.ClientConn
}

func newHubClient(addr string) (*hubClient, error) {
	return newHubClientWithTimeout(addr, 3*time.Second)
}

func newHubClientWithTimeout(addr string, timeout time.Duration) (*hubClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, hubDialOptions()...)
	if err != nil {
		return nil, hubUnavailableError(addr, err)
	}
	return &hubClient{c: pb.NewAmuxdClient(conn), conn: conn}, nil
}

func hubUnavailableError(addr string, err error) error {
	return fmt.Errorf("hub is not reachable at %s: %w\nCheck this installation with `amux system status`, then recover local services with `amux system start`. For a remote hub, set `hub.addr` or `AMUX_HUB_ADDR` to its address.", addr, err)
}

func hubDialOptions() []grpc.DialOption {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}
	opts = append(opts, transport.GRPCClientDialOptions()...)
	hubAuthToken := hubToken()
	actorAuthToken := actorToken()
	if hubAuthToken != "" || actorAuthToken != "" {
		opts = append(opts,
			grpc.WithUnaryInterceptor(hubUnaryTokenInterceptor(hubAuthToken, actorAuthToken)),
			grpc.WithStreamInterceptor(hubStreamTokenInterceptor(hubAuthToken, actorAuthToken)),
		)
	}
	return opts
}

func hubUnaryTokenInterceptor(hubAuthToken, actorAuthToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(outgoingAuthContext(ctx, hubAuthToken, actorAuthToken), method, req, reply, cc, opts...)
	}
}

func hubStreamTokenInterceptor(hubAuthToken, actorAuthToken string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(outgoingAuthContext(ctx, hubAuthToken, actorAuthToken), desc, cc, method, opts...)
	}
}

func outgoingAuthContext(ctx context.Context, hubAuthToken, actorAuthToken string) context.Context {
	if hubAuthToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-hub-token", hubAuthToken)
	}
	if actorAuthToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-actor-token", actorAuthToken)
	}
	return ctx
}

func (oc *hubClient) Close() error { return oc.conn.Close() }

func (oc *hubClient) Run(ctx context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error) {
	return oc.c.Run(ctx, &pb.AmuxdRunRequest{
		TargetId:              targetID,
		AgentProfile:          req.AgentProfile,
		ProfileSpec:           req.ProfileSpec,
		AgentType:             req.AgentType,
		Name:                  req.Name,
		ExtraArgs:             req.ExtraArgs,
		Env:                   req.Env,
		Cwd:                   req.Cwd,
		Repo:                  req.Repo,
		RepoRef:               req.RepoRef,
		IdleTimeoutSeconds:    req.IdleTimeoutSeconds,
		IdleTimeoutSecondsSet: req.IdleTimeoutSecondsSet,
		WorkspaceSource:       req.WorkspaceSource,
		WorkspaceScope:        req.WorkspaceScope,
		RunAs:                 req.RunAs,
	})
}

func (oc *hubClient) SetCredential(ctx context.Context, profile, envName, value, expectedPrincipalID string) (*pb.CredentialProfileMetadata, error) {
	return oc.c.SetCredential(ctx, &pb.SetCredentialRequest{
		Profile: profile, EnvName: envName, Value: value, ExpectedPrincipalId: expectedPrincipalID,
	})
}

func (oc *hubClient) GetCredential(ctx context.Context, profile string) (*pb.CredentialProfileMetadata, error) {
	return oc.c.GetCredential(ctx, &pb.GetCredentialRequest{Profile: profile})
}

func (oc *hubClient) ListCredentials(ctx context.Context) ([]*pb.CredentialProfileMetadata, error) {
	resp, err := oc.c.ListCredentials(ctx, &pb.ListCredentialsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Profiles, nil
}

func (oc *hubClient) List(ctx context.Context) ([]*sessionEntry, error) {
	resp, err := oc.c.ListTargets(ctx, &pb.ListTargetsRequest{})
	if err != nil {
		return nil, err
	}
	var out []*sessionEntry
	for _, sb := range resp.Targets {
		for _, s := range sb.Sessions {
			out = append(out, &sessionEntry{
				Session:    *s,
				TargetID:   sb.Id,
				TargetName: sb.Name,
			})
		}
	}
	return out, nil
}

func (oc *hubClient) ListTargets(ctx context.Context) ([]targetSessions, error) {
	resp, err := oc.c.ListTargets(ctx, &pb.ListTargetsRequest{})
	if err != nil {
		return nil, err
	}
	return targetsToTargetSessions(oc, resp.Targets), nil
}

func (oc *hubClient) Kill(ctx context.Context, targetID, name string) error {
	_, err := oc.c.Kill(ctx, &pb.AmuxdKillRequest{TargetId: targetID, Name: name})
	return err
}

func (oc *hubClient) Restart(ctx context.Context, targetID, name string, refreshProfile bool) error {
	_, err := oc.c.Restart(ctx, &pb.AmuxdRestartRequest{TargetId: targetID, Name: name, RefreshProfile: refreshProfile})
	return err
}

func (oc *hubClient) SetModel(ctx context.Context, targetID, name string, replacement *pb.RunRequest) (*pb.RunResponse, error) {
	return oc.c.Restart(ctx, &pb.AmuxdRestartRequest{TargetId: targetID, Name: name, Replacement: replacement, PreserveHistory: true})
}

func (oc *hubClient) ChangeCwd(ctx context.Context, targetID string, req *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	return oc.c.ChangeCwd(ctx, amuxdChangeCwdRequest(targetID, req))
}

func amuxdChangeCwdRequest(targetID string, req *pb.ChangeCwdRequest) *pb.AmuxdChangeCwdRequest {
	return &pb.AmuxdChangeCwdRequest{
		TargetId:       targetID,
		Name:           req.Name,
		Cwd:            req.Cwd,
		Wait:           req.Wait,
		Interrupt:      req.Interrupt,
		ExpectedCwd:    req.ExpectedCwd,
		DeferUntilIdle: req.DeferUntilIdle,
	}
}

func (oc *hubClient) Sleep(ctx context.Context, targetID, name string) error {
	_, err := oc.c.Sleep(ctx, &pb.AmuxdSleepRequest{TargetId: targetID, Name: name})
	return err
}

func (oc *hubClient) Resume(ctx context.Context, targetID, name string) error {
	_, err := oc.c.Resume(ctx, &pb.AmuxdResumeRequest{TargetId: targetID, Name: name})
	return err
}

func (oc *hubClient) Prune(ctx context.Context, targetID string) (int32, []string, error) {
	resp, err := oc.c.Prune(ctx, &pb.AmuxdPruneRequest{TargetId: targetID})
	if err != nil {
		return 0, nil, err
	}
	return resp.Count, resp.Names, nil
}

func (oc *hubClient) Interrupt(ctx context.Context, targetID, name string) error {
	_, err := oc.c.Interrupt(ctx, &pb.AmuxdInterruptRequest{TargetId: targetID, Name: name})
	return err
}

func (oc *hubClient) Attach(ctx context.Context, targetID, name string) (pb.Amuxlet_AttachClient, error) {
	return oc.c.Attach(ctx, &pb.AmuxdAttachRequest{TargetId: targetID, Name: name, TimelineMode: pb.AttachRequest_FOLLOW})
}

func (oc *hubClient) Submit(ctx context.Context, targetID, name, text string) error {
	return oc.SubmitFrom(ctx, targetID, name, text, "cli")
}

func (oc *hubClient) SubmitAsync(ctx context.Context, targetID, name, text string) error {
	return oc.SubmitAsyncFrom(ctx, targetID, name, text, "cli")
}

func (oc *hubClient) SubmitFrom(ctx context.Context, targetID, name, text, source string) error {
	_, err := oc.c.Submit(ctx, &pb.AmuxdSubmitRequest{TargetId: targetID, Name: name, Text: text, Source: source})
	return err
}

func (oc *hubClient) SubmitAsyncFrom(ctx context.Context, targetID, name, text, source string) error {
	_, err := oc.c.Submit(ctx, &pb.AmuxdSubmitRequest{TargetId: targetID, Name: name, Text: text, Source: source, Async: true})
	return err
}

func (oc *hubClient) SubmitPreempt(ctx context.Context, targetID, name, text string) error {
	_, err := oc.c.Submit(ctx, &pb.AmuxdSubmitRequest{TargetId: targetID, Name: name, Text: text, Source: "cli", Async: true, Preempt: true})
	return err
}

func (oc *hubClient) QueueList(ctx context.Context, targetID, name string) ([]*pb.QueueItem, error) {
	resp, err := oc.c.QueueList(ctx, &pb.AmuxdQueueListRequest{TargetId: targetID, Name: name})
	if err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (oc *hubClient) QueueClear(ctx context.Context, targetID, name string) (int32, error) {
	resp, err := oc.c.QueueClear(ctx, &pb.AmuxdQueueClearRequest{TargetId: targetID, Name: name})
	if err != nil {
		return 0, err
	}
	return resp.Cleared, nil
}

func (oc *hubClient) QueueCancel(ctx context.Context, targetID, name, id string) error {
	_, err := oc.c.QueueCancel(ctx, &pb.AmuxdQueueCancelRequest{TargetId: targetID, Name: name, Id: id})
	return err
}

func (oc *hubClient) RespondPermission(ctx context.Context, targetID, name, permissionID, optionID string) error {
	_, err := oc.c.RespondPermission(ctx, &pb.AmuxdRespondPermissionRequest{TargetId: targetID, Name: name, PermissionId: permissionID, OptionId: optionID})
	return err
}

// ── routing ──────────────────────────────────────────────────────────────────

// newSessionClient returns a client talking to the hub control plane. The local
// runtime is represented as a hub target; direct runtime RPC is reserved for
// runtime maintenance commands.
func newSessionClient() (sessionClient, error) {
	_ = loadUserConfig()
	return newHubClient(hubAddr())
}

// newAmuxdClient connects to the effective hub for hub-only RPCs (e.g.
// schedule management).
func newAmuxdClient() (pb.AmuxdClient, func() error, error) {
	return newAmuxdClientWithTimeout(3 * time.Second)
}

func newAmuxdClientWithTimeout(timeout time.Duration) (pb.AmuxdClient, func() error, error) {
	_ = loadUserConfig()
	addr := hubAddr()
	hc, err := newHubClientWithTimeout(addr, timeout)
	if err != nil {
		return nil, nil, err
	}
	return hc.c, hc.Close, nil
}
