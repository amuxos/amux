package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/memory"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type hubServer struct {
	pb.UnimplementedAmuxdServer
	hub                         *tunnel.Hub
	listTargets                 func() []*pb.Target
	feishuBotSessionControllers map[string]feishuBotSessionController
	feishuMessageSenders        []feishuMessageSender
	schedule                    *schedule.Store
	creds                       task.CredentialProvider
	principals                  *principal.Store
	memory                      *memory.Store
	memoryConfig                func() config.MemoryConfig
	version                     string
	startedAt                   time.Time
	credentialRotationHook      func(principalID, reason string) error
	listAgentProfiles           func() ([]agentprofile.Profile, error)
	resolveAgentProfile         func(ref string) (agentprofile.Profile, error)
	localExpertAuthoring        *localExpertAuthoringControl
	getLocalExpertAuthoringTurn func(context.Context, string, string) (*pb.LocalExpertAuthoringTurnResponse, error)
	// isolationConfig reads the hub host's isolation section for credential
	// store selection; nil defaults to config.GetIsolation. Tests override.
	isolationConfig func() (config.IsolationConfig, error)
}

func (s *hubServer) ListAgentProfiles(context.Context, *pb.ListAgentProfilesRequest) (*pb.ListAgentProfilesResponse, error) {
	list := s.listAgentProfiles
	if list == nil {
		list = availableHubAgentProfiles
	}
	profiles, profilesErr := list()
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Ref() < profiles[j].Ref() })
	out := &pb.ListAgentProfilesResponse{Profiles: make([]*pb.AgentProfileInfo, 0, len(profiles))}
	for _, profile := range profiles {
		out.Profiles = append(out.Profiles, agentProfileInfoToPB(profile))
	}
	if profilesErr != nil {
		out.ProfilesError = profilesErr.Error()
	}
	return out, nil
}

func (s *hubServer) GetAgentProfile(_ context.Context, req *pb.GetAgentProfileRequest) (*pb.GetAgentProfileResponse, error) {
	ref := strings.TrimSpace(req.GetRef())
	if ref == "" {
		return nil, status.Error(codes.InvalidArgument, "agent profile ref is required")
	}
	resolve := s.resolveAgentProfile
	if resolve == nil {
		resolve = func(ref string) (agentprofile.Profile, error) {
			return agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), ref)
		}
	}
	profile, err := resolve(ref)
	if err != nil {
		if agentprofile.IsProfileNotFound(err) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.GetAgentProfileResponse{Profile: agentProfileInfoToPB(profile)}, nil
}

func availableHubAgentProfiles() ([]agentprofile.Profile, error) {
	registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath())
	if err != nil {
		return agentprofile.Builtins(), err
	}
	return agentprofile.DiscoverProfiles(registry, false)
}

func agentProfileInfoToPB(profile agentprofile.Profile) *pb.AgentProfileInfo {
	skills := profile.ResolveSkills()
	out := &pb.AgentProfileInfo{
		Profile: agentprofile.ToPBProfileSpec(profile),
		Skills:  make([]*pb.AgentProfileSkillInfo, 0, len(skills)),
	}
	for _, skill := range skills {
		out.Skills = append(out.Skills, &pb.AgentProfileSkillInfo{
			Name: skill.Name, Description: skill.Description,
		})
	}
	return out
}

func (s *hubServer) CreatePrincipal(_ context.Context, req *pb.CreatePrincipalRequest) (*pb.PrincipalResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	p, err := s.principals.Create(principal.Principal{
		Kind:                principal.Kind(req.Kind),
		DisplayName:         req.DisplayName,
		EnvironmentTargetID: req.EnvironmentTargetId,
		Metadata:            copyStringMap(req.Metadata),
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.PrincipalResponse{Principal: principalToPB(p)}, nil
}

func (s *hubServer) ListPrincipals(context.Context, *pb.ListPrincipalsRequest) (*pb.ListPrincipalsResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	items, err := s.principals.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := &pb.ListPrincipalsResponse{Principals: make([]*pb.Principal, 0, len(items))}
	for _, p := range items {
		out.Principals = append(out.Principals, principalToPB(p))
	}
	return out, nil
}

func (s *hubServer) DisablePrincipal(_ context.Context, req *pb.DisablePrincipalRequest) (*pb.PrincipalResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	if err := s.principals.Disable(req.PrincipalId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	p, _ := s.principals.Get(req.PrincipalId)
	s.revokePrincipalLaunches(context.Background(), req.PrincipalId, "", "principal_disabled")
	s.notifyCredentialRotation(req.PrincipalId, "principal_disabled")
	return &pb.PrincipalResponse{Principal: principalToPB(p)}, nil
}

func (s *hubServer) ListPrincipalAliases(context.Context, *pb.ListPrincipalAliasesRequest) (*pb.ListPrincipalAliasesResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	items, err := s.principals.ListAliases()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := &pb.ListPrincipalAliasesResponse{Aliases: make([]*pb.PrincipalAlias, 0, len(items))}
	for _, a := range items {
		out.Aliases = append(out.Aliases, &pb.PrincipalAlias{
			Provider:    string(a.Provider),
			Scope:       a.Scope,
			Subject:     a.Subject,
			PrincipalId: a.PrincipalID,
		})
	}
	return out, nil
}

func (s *hubServer) BindPrincipalAlias(_ context.Context, req *pb.BindPrincipalAliasRequest) (*pb.PrincipalResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	if err := s.principals.BindAlias(principal.Alias{
		Provider: principal.Provider(req.Provider), Scope: req.Scope,
		Subject: req.Subject, PrincipalID: req.PrincipalId,
	}); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	p, _ := s.principals.Get(req.PrincipalId)
	return &pb.PrincipalResponse{Principal: principalToPB(p)}, nil
}

func (s *hubServer) GrantPrincipalDelegation(_ context.Context, req *pb.GrantPrincipalDelegationRequest) (*pb.GrantPrincipalDelegationResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	id, err := s.principals.GrantDelegation(req.ActorPrincipalId, req.EffectivePrincipalId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.GrantPrincipalDelegationResponse{DelegationId: id}, nil
}

func (s *hubServer) RevokePrincipalDelegation(_ context.Context, req *pb.RevokePrincipalDelegationRequest) (*pb.EmptyResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	effectiveID, err := s.principals.DelegationEffectivePrincipal(req.DelegationId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.principals.RevokeDelegation(req.DelegationId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	s.revokePrincipalLaunches(context.Background(), effectiveID, req.DelegationId, "delegation_revoked")
	s.notifyCredentialRotation(effectiveID, "delegation_revoked")
	return &pb.EmptyResponse{}, nil
}

func (s *hubServer) GrantServicePrincipalMember(_ context.Context, req *pb.ServicePrincipalMemberRequest) (*pb.EmptyResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	if err := s.principals.GrantServiceMember(req.ServicePrincipalId, req.MemberPrincipalId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.EmptyResponse{}, nil
}

func (s *hubServer) RevokeServicePrincipalMember(_ context.Context, req *pb.ServicePrincipalMemberRequest) (*pb.EmptyResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	if err := s.principals.RevokeServiceMember(req.ServicePrincipalId, req.MemberPrincipalId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	s.revokePrincipalLaunches(
		context.Background(),
		req.ServicePrincipalId,
		principal.ServiceMembershipAuthorityID(req.ServicePrincipalId, req.MemberPrincipalId),
		"service_membership_revoked",
	)
	s.notifyCredentialRotation(req.ServicePrincipalId, "service_membership_revoked")
	return &pb.EmptyResponse{}, nil
}

func (s *hubServer) notifyCredentialRotation(principalID, reason string) {
	if s.credentialRotationHook != nil {
		if err := s.credentialRotationHook(principalID, reason); err != nil {
			fmt.Fprintf(os.Stderr, "hub: credential rotation notification failed for principal %s: %v\n", principalID, err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "hub: credential rotation recommended for principal %s after %s\n", principalID, reason)
}

func (s *hubServer) revokePrincipalLaunches(ctx context.Context, principalID, delegationID, reason string) {
	if s.hub == nil {
		return
	}
	for _, target := range s.currentTargets() {
		if target == nil || target.Id == "" {
			continue
		}
		_, err := routeUnary[pb.RevokePrincipalLaunchesRequest, pb.RevokePrincipalLaunchesResponse](
			s, ctx, target.Id, "RevokePrincipalLaunches",
			&pb.RevokePrincipalLaunchesRequest{
				PrincipalId:  principalID,
				DelegationId: delegationID,
				Reason:       reason,
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hub: revoke principal launches on %s: %v\n", target.Id, err)
		}
	}
}

func (s *hubServer) CreatePrincipalEnrollment(_ context.Context, req *pb.CreatePrincipalEnrollmentRequest) (*pb.CreatePrincipalEnrollmentResponse, error) {
	if s.principals == nil {
		return nil, status.Error(codes.FailedPrecondition, "principal registry unavailable")
	}
	e, err := s.principals.CreateEnrollment(principal.Provider(req.Provider), req.Scope, req.Subject, req.PrincipalId, req.Rotate)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.CreatePrincipalEnrollmentResponse{CredentialId: e.CredentialID, Token: e.Token}, nil
}

func (s *hubServer) GetIsolationStatus(context.Context, *pb.GetIsolationStatusRequest) (*pb.GetIsolationStatusResponse, error) {
	cfg, err := config.GetIsolation()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "isolation config: %v", err)
	}
	resp := &pb.GetIsolationStatusResponse{
		Enabled:           cfg.Enabled,
		ConfiguredBackend: cfg.Backend,
		ActualBackend:     principal.BackendPassthrough,
	}
	targets := s.currentTargets()
	if len(targets) == 0 {
		resp.DegradedReason = "no runtime capability source"
		return resp, nil
	}
	for _, target := range targets {
		if target == nil || target.Capabilities == nil {
			continue
		}
		caps := target.Capabilities
		if caps.IsolationHelperProtocol != "" {
			resp.HelperReady = true
			resp.HelperProtocol = caps.IsolationHelperProtocol
			resp.ActualBackend = principal.BackendLinuxDAC
			resp.DegradedReason = ""
			return resp, nil
		}
		if resp.DegradedReason == "" && caps.IsolationDegradedReason != "" {
			resp.DegradedReason = caps.IsolationDegradedReason
		}
	}
	if resp.DegradedReason == "" {
		resp.DegradedReason = "no helper-ready runtime registered"
	}
	return resp, nil
}

func principalToPB(p principal.Principal) *pb.Principal {
	return &pb.Principal{
		PrincipalId:         p.ID,
		Kind:                string(p.Kind),
		DisplayName:         p.DisplayName,
		Status:              string(p.Status),
		EnvironmentTargetId: p.EnvironmentTargetID,
		Metadata:            copyStringMap(p.Metadata),
		CreatedAt:           p.CreatedAt.Unix(),
		UpdatedAt:           p.UpdatedAt.Unix(),
	}
}

type feishuBotSessionController interface {
	BindBotSession(bridge.BotSession, string) error
	DisableBotSession() error
}

func (s *hubServer) Tunnel(stream pb.Amuxd_TunnelServer) error {
	return s.hub.Serve(stream)
}

func (s *hubServer) Info(context.Context, *pb.HubInfoRequest) (*pb.HubInfoResponse, error) {
	return &pb.HubInfoResponse{
		Version:   s.version,
		StartedAt: s.startedAt.Unix(),
	}, nil
}

func (s *hubServer) ListTargets(ctx context.Context, _ *pb.ListTargetsRequest) (*pb.ListTargetsResponse, error) {
	targets := s.currentTargets()
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return nil, err
	}
	// Principal-scoped visibility lives hub-side, so no client (CLI, gateway,
	// MCP) can bypass it by asking differently. Enrollment and unauthenticated
	// callers keep the fleet inventory contract. On a fleet without isolation,
	// session visibility remains shared.
	if s.fleetIsolationActive() {
		targets = filterTargetsBySessionAccess(targets, actor, true)
	}
	return &pb.ListTargetsResponse{Targets: targets}, nil
}

func (s *hubServer) MaterializeCredential(ctx context.Context, req *pb.MaterializeCredentialRequest) (*pb.MaterializeCredentialResponse, error) {
	if strings.TrimSpace(req.Profile) == "" && len(req.EnvNames) == 0 {
		return nil, status.Error(codes.InvalidArgument, "profile or env_names is required")
	}
	provider, err := s.credentialProviderForContext(ctx)
	if err != nil {
		return nil, err
	}
	plan := task.CredentialPlan{Requests: []task.CredentialMaterialRequest{{
		Profile:  req.Profile,
		EnvNames: append([]string(nil), req.EnvNames...),
		Source:   "hub",
	}}}
	mat, err := task.MaterializeCredentialPlan(ctx, provider, plan, req.AgentType)
	if err != nil {
		return nil, credentialMaterializeStatusError(err)
	}
	return &pb.MaterializeCredentialResponse{
		Env:      append([]string(nil), mat.Env...),
		Files:    copyStringMap(mat.Files),
		Secrets:  append([]string(nil), mat.Secrets...),
		Kind:     mat.Kind,
		Profiles: append([]string(nil), mat.Profiles...),
	}, nil
}

func (s *hubServer) SetCredential(ctx context.Context, req *pb.SetCredentialRequest) (*pb.CredentialProfileMetadata, error) {
	store, err := s.authenticatedCredentialStore(ctx, req.ExpectedPrincipalId)
	if err != nil {
		return nil, err
	}
	meta, err := store.SetEnv(req.Profile, req.EnvName, req.Value)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return credentialMetadataToProto(meta), nil
}

func (s *hubServer) GetCredential(ctx context.Context, req *pb.GetCredentialRequest) (*pb.CredentialProfileMetadata, error) {
	store, err := s.authenticatedCredentialStore(ctx, "")
	if err != nil {
		return nil, err
	}
	meta, err := store.Metadata(req.Profile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return credentialMetadataToProto(meta), nil
}

func (s *hubServer) ListCredentials(ctx context.Context, _ *pb.ListCredentialsRequest) (*pb.ListCredentialsResponse, error) {
	store, err := s.authenticatedCredentialStore(ctx, "")
	if err != nil {
		return nil, err
	}
	profiles, err := store.ListMetadata()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := &pb.ListCredentialsResponse{Profiles: make([]*pb.CredentialProfileMetadata, 0, len(profiles))}
	for _, meta := range profiles {
		out.Profiles = append(out.Profiles, credentialMetadataToProto(meta))
	}
	return out, nil
}

func (s *hubServer) credentialProviderForContext(ctx context.Context) (task.CredentialProvider, error) {
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return nil, err
	}
	if actor != nil && s.principalCredentialStoresEnabled() {
		dir, err := amuxhome.PrincipalSecretsDir(actor.ActorPrincipalID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
		return task.FileCredentials{Dir: dir}, nil
	}
	if s.creds != nil {
		return s.creds, nil
	}
	return defaultHubCredentialProvider()
}

// principalCredentialStoresEnabled reports whether credential materialization
// scopes stores per principal. Only hosts configured for per-user OS isolation
// qualify; on any other host every execution runs as the same OS user anyway,
// so identified sessions/tasks materialize from the shared legacy-host store
// instead of requiring each principal to re-provision identical credentials.
// Like the unidentified-execution rejection rule, the decision reads the
// configured backend, never helper readiness; a config read error fails closed
// toward per-principal stores.
func (s *hubServer) principalCredentialStoresEnabled() bool {
	get := s.isolationConfig
	if get == nil {
		get = config.GetIsolation
	}
	cfg, err := get()
	if err != nil {
		return true
	}
	return cfg.Enabled && cfg.Backend != principal.BackendPassthrough
}

func (s *hubServer) authenticatedCredentialStore(ctx context.Context, expectedPrincipalID string) (task.FileCredentials, error) {
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return task.FileCredentials{}, err
	}
	if actor == nil {
		return task.FileCredentials{}, status.Error(codes.Unauthenticated, "credential management requires CLI enrollment or AMUX_ACTOR_TOKEN")
	}
	if expected := strings.TrimSpace(expectedPrincipalID); expected != "" && actor.ActorPrincipalID != expected {
		return task.FileCredentials{}, status.Errorf(codes.PermissionDenied,
			"CLI is enrolled to Principal %s, not requested Principal %s; pass --rotate-enrollment to switch explicitly",
			actor.ActorPrincipalID, expected)
	}
	dir, err := amuxhome.PrincipalSecretsDir(actor.ActorPrincipalID)
	if err != nil {
		return task.FileCredentials{}, status.Errorf(codes.Internal, "%v", err)
	}
	return task.FileCredentials{Dir: dir}, nil
}

func credentialMetadataToProto(meta task.CredentialProfileMetadata) *pb.CredentialProfileMetadata {
	return &pb.CredentialProfileMetadata{
		Profile:   meta.Profile,
		Kind:      meta.Kind,
		EnvNames:  append([]string(nil), meta.EnvNames...),
		FileNames: append([]string(nil), meta.FileNames...),
		Path:      meta.Path,
	}
}

func credentialMaterializeStatusError(err error) error {
	st := status.New(codes.FailedPrecondition, err.Error())
	descriptor, ok := task.DescribeCredentialEnvError(err)
	if !ok {
		return st.Err()
	}
	withDetails, detailsErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: descriptor.Reason,
		Domain: "amux.credentials",
		Metadata: map[string]string{
			"env_name":       descriptor.EnvName,
			"first_profile":  descriptor.FirstProfile,
			"second_profile": descriptor.SecondProfile,
		},
	})
	if detailsErr != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func defaultHubCredentialProvider() (task.CredentialProvider, error) {
	return task.FileCredentials{Dir: amuxhome.SecretsDir()}, nil
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *hubServer) FeishuStatus(_ context.Context, req *pb.FeishuStatusRequest) (*pb.FeishuStatusResponse, error) {
	profile := feishuProfileOrDefault(req.Profile)
	out := &pb.FeishuStatusResponse{Profile: profile, BridgeDbHealthy: true}
	if secrets, err := bridge.LoadProfileSecrets(profile); err == nil {
		out.Configured = secrets.AppId != "" && secrets.AppSecret != ""
		out.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
		out.AppId = secrets.AppId
		out.OpenId = secrets.OpenId
		out.NotifyChatId = secrets.NotifyChatId
		out.BotName = secrets.BotName
		out.BotOpenId = secrets.BotOpenId
	} else if !os.IsNotExist(err) {
		out.Error = err.Error()
	}
	bindings, err := bridge.ListBindingsForBridgeId(bridge.DefaultDBPath(), out.BridgeId)
	if err != nil {
		out.BridgeDbHealthy = false
		if out.Error == "" {
			out.Error = err.Error()
		}
		return out, nil
	}
	out.BridgeDbHealthy = true
	out.BindingCount = int32(len(bindings))
	return out, nil
}

func (s *hubServer) FeishuBindings(_ context.Context, req *pb.FeishuBindingsRequest) (*pb.FeishuBindingsResponse, error) {
	profile := feishuProfileOrDefault(req.Profile)
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		if os.IsNotExist(err) {
			return &pb.FeishuBindingsResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "load Feishu profile %s: %v", profile, err)
	}
	bridgeID := bridge.BridgeIdFromAppId(secrets.AppId)
	bindings, err := bridge.ListBindingsForBridgeId(bridge.DefaultDBPath(), bridgeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list Feishu bindings: %v", err)
	}
	out := &pb.FeishuBindingsResponse{Bindings: make([]*pb.FeishuBinding, 0, len(bindings))}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, feishuBindingToPB(b))
	}
	return out, nil
}

func (s *hubServer) FeishuBots(context.Context, *pb.FeishuBotsRequest) (*pb.FeishuBotsResponse, error) {
	profiles, err := bridge.ListSecretProfiles()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list Feishu profiles: %v", err)
	}
	out := &pb.FeishuBotsResponse{Bots: make([]*pb.FeishuBot, 0, len(profiles))}
	for _, profile := range profiles {
		bot := &pb.FeishuBot{Profile: profile}
		secrets, err := bridge.LoadProfileSecrets(profile)
		if err != nil {
			bot.Error = err.Error()
			out.Bots = append(out.Bots, bot)
			continue
		}
		bot.Configured = secrets.AppId != "" && secrets.AppSecret != ""
		bot.AppId = secrets.AppId
		bot.OpenId = secrets.OpenId
		bot.BotName = secrets.BotName
		bot.BotOpenId = secrets.BotOpenId
		bot.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
		if bindings, err := bridge.ListBindingsForBridgeId(bridge.DefaultDBPath(), bot.BridgeId); err == nil {
			bot.BindingCount = int32(len(bindings))
		} else {
			bot.Error = err.Error()
		}
		out.Bots = append(out.Bots, bot)
	}
	return out, nil
}

func (s *hubServer) FeishuBotSessionStatus(_ context.Context, req *pb.FeishuBotSessionStatusRequest) (*pb.FeishuBotSessionStatusResponse, error) {
	return hubFeishuBotSessionStatus(req.Profile)
}

func (s *hubServer) FeishuBotSessionBind(_ context.Context, req *pb.FeishuBotSessionBindRequest) (*pb.FeishuBotSessionStatusResponse, error) {
	profile := feishuProfileOrDefault(req.Profile)
	if err := config.ValidateBridgeFeishuGroupWatchInteraction(config.GetBridgeFeishuGroupWatchForProfile(profile), config.FeishuInteractionSingleton); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	bridgeID, _, err := feishuProfileBridgeIDAndState(profile)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	target, sess, err := resolveFeishuBotSessionRef(s.currentTargets(), req.SessionRef)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	targetID := strings.TrimSpace(target.Id)
	if targetID == "" {
		targetID = strings.TrimSpace(target.Name)
	}
	targetName := strings.TrimSpace(target.Name)
	if targetName == "" {
		targetName = targetID
	}
	botSession := bridge.BotSession{
		BridgeId:     bridgeID,
		Enabled:      true,
		TargetID:     targetID,
		TargetName:   targetName,
		SessionName:  sess.Name,
		AgentProfile: sess.AgentProfile,
		AgentType:    sess.AgentType,
		Cwd:          sess.Cwd,
	}
	var bindErr error
	if controller := s.feishuBotSessionControllers[profile]; controller != nil {
		bindErr = controller.BindBotSession(botSession, "")
	} else {
		bindErr = bridge.SetBotSessionInDB(bridge.DefaultDBPath(), botSession)
		if bindErr == nil {
			_ = config.SetBridgeFeishuInteractionMode(profile, config.FeishuInteractionSingleton)
		}
	}
	if bindErr != nil {
		return nil, status.Errorf(codes.Internal, "bind Feishu bot session: %v", bindErr)
	}
	out, err := hubFeishuBotSessionStatus(profile)
	if err != nil {
		return nil, err
	}
	out.InteractionMode = config.FeishuInteractionSingleton
	return out, nil
}

func (s *hubServer) FeishuBotSessionOff(_ context.Context, req *pb.FeishuBotSessionOffRequest) (*pb.FeishuBotSessionStatusResponse, error) {
	profile := feishuProfileOrDefault(req.Profile)
	bridgeID, _, err := feishuProfileBridgeIDAndState(profile)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	var disableErr error
	if controller := s.feishuBotSessionControllers[profile]; controller != nil {
		disableErr = controller.DisableBotSession()
	} else {
		disableErr = bridge.DisableBotSessionInDB(bridge.DefaultDBPath(), bridgeID)
		if disableErr == nil {
			_ = config.SetBridgeFeishuInteractionMode(profile, config.FeishuInteractionThread)
		}
	}
	if disableErr != nil {
		return nil, status.Errorf(codes.Internal, "disable Feishu bot session: %v", disableErr)
	}
	return hubFeishuBotSessionStatus(profile)
}

func (s *hubServer) FeishuContextExplain(ctx context.Context, req *pb.FeishuContextExplainRequest) (*pb.FeishuContextExplainResponse, error) {
	profile := feishuProfileOrDefault(req.Profile)
	out := &pb.FeishuContextExplainResponse{Profile: profile}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
	store, err := threadcontext.OpenStore(threadcontext.DefaultDBPath())
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	defer store.Close()
	explain, err := store.Explain(ctx, threadcontext.ExplainRequest{
		RootMsgID:  req.RootMsgId,
		MessageID:  req.MessageId,
		BridgeId:   out.BridgeId,
		BotProfile: profile,
		DecisionID: req.DecisionId,
	})
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Decision = feishuContextDecisionToPB(explain.Decision)
	out.SelectedItems = make([]*pb.FeishuContextItem, 0, len(explain.SelectedItems))
	for _, item := range explain.SelectedItems {
		out.SelectedItems = append(out.SelectedItems, feishuContextItemToPB(item))
	}
	return out, nil
}

func (s *hubServer) currentTargets() []*pb.Target {
	if s.listTargets != nil {
		return s.listTargets()
	}
	if s.hub == nil {
		return nil
	}
	return s.hub.ListTargets()
}

func feishuProfileOrDefault(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return bridge.DefaultProfileName
	}
	return profile
}

func hubFeishuBotSessionStatus(profile string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	out := &pb.FeishuBotSessionStatusResponse{Profile: profile}
	bridgeID, state, err := feishuProfileBridgeIDAndState(profile)
	out.BridgeId = bridgeID
	out.InteractionMode = state.InteractionMode
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.DefaultAgent = state.DefaultAgent
	bs, err := bridge.GetBotSessionFromDB(bridge.DefaultDBPath(), bridgeID)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.BotSession = feishuBotSessionToPB(bs)
	return out, nil
}

func feishuProfileBridgeIDAndState(profile string) (bridgeID string, state bridge.BotProfileState, err error) {
	profile = feishuProfileOrDefault(profile)
	fallbackMode, err := config.GetBridgeFeishuInteractionMode(profile)
	if err != nil {
		return "", state, err
	}
	state.InteractionMode = fallbackMode
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		return "", state, fmt.Errorf("load Feishu profile %s: %w", profile, err)
	}
	if strings.TrimSpace(secrets.AppId) == "" {
		return "", state, fmt.Errorf("Feishu profile %s missing app_id", profile)
	}
	bridgeID = bridge.BridgeIdFromAppId(secrets.AppId)
	state, err = bridge.GetBotProfileStateFromDB(bridge.DefaultDBPath(), bridgeID, fallbackMode)
	if err != nil {
		state.InteractionMode = fallbackMode
		return bridgeID, state, err
	}
	return bridgeID, state, nil
}

func feishuBotSessionToPB(bs *bridge.BotSession) *pb.FeishuBotSession {
	if bs == nil {
		return nil
	}
	return &pb.FeishuBotSession{
		BridgeId:           bs.BridgeId,
		Enabled:            bs.Enabled,
		TargetId:           bs.TargetID,
		TargetName:         bs.TargetName,
		SessionName:        bs.SessionName,
		AgentProfile:       bs.AgentProfile,
		AgentType:          bs.AgentType,
		LastReplyKind:      bs.LastReplyKind,
		LastReplyChatId:    bs.LastReplyChatID,
		LastReplyOpenId:    bs.LastReplyOpenID,
		LastInputMessageId: bs.LastInputMessageID,
		Cwd:                bs.Cwd,
	}
}

func resolveFeishuBotSessionRef(targets []*pb.Target, ref string) (*pb.Target, *pb.Session, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, fmt.Errorf("session ref is required")
	}
	if targetRef, sessionRef, ok := splitFeishuBotTargetSession(ref); ok {
		var targetMatches []*pb.Target
		for _, target := range targets {
			if target == nil {
				continue
			}
			if target.Id == targetRef || target.Name == targetRef {
				targetMatches = append(targetMatches, target)
			}
		}
		if len(targetMatches) == 0 {
			return nil, nil, fmt.Errorf("target %q not found", targetRef)
		}
		if len(targetMatches) > 1 {
			return nil, nil, fmt.Errorf("target %q is ambiguous", targetRef)
		}
		matches := matchingFeishuBotSessions(targetMatches, sessionRef)
		if len(matches) == 0 {
			return nil, nil, fmt.Errorf("session %q not found on target %q", sessionRef, targetRef)
		}
		if len(matches) > 1 {
			return nil, nil, fmt.Errorf("session ref %q is ambiguous", ref)
		}
		return matches[0].target, matches[0].session, nil
	}
	matches := matchingFeishuBotSessions(targets, ref)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("session %q not found", ref)
	}
	if len(matches) > 1 {
		return nil, nil, fmt.Errorf("session ref %q is ambiguous", ref)
	}
	return matches[0].target, matches[0].session, nil
}

type feishuBotSessionMatch struct {
	target  *pb.Target
	session *pb.Session
}

func matchingFeishuBotSessions(targets []*pb.Target, sessionRef string) []feishuBotSessionMatch {
	var matches []feishuBotSessionMatch
	for _, target := range targets {
		if target == nil {
			continue
		}
		for _, sess := range target.Sessions {
			if sess == nil {
				continue
			}
			if sess.Name == sessionRef || sess.Id == sessionRef {
				matches = append(matches, feishuBotSessionMatch{target: target, session: sess})
			}
		}
	}
	return matches
}

func splitFeishuBotTargetSession(ref string) (target, session string, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	target = strings.TrimSpace(parts[0])
	session = strings.TrimSpace(parts[1])
	return target, session, target != "" && session != ""
}

func feishuContextDecisionToPB(d threadcontext.Decision) *pb.FeishuContextDecision {
	return &pb.FeishuContextDecision{
		Id:            d.ID,
		RootMsgId:     d.RootMsgID,
		MessageId:     d.MessageID,
		BridgeId:      d.BridgeId,
		BotProfile:    d.BotProfile,
		WorkspaceMode: d.WorkspaceMode,
		RequestText:   d.RequestText,
		ThreadContext: d.ThreadContext,
		PromptPreview: d.PromptPreview,
		CreatedAt:     d.CreatedAt,
	}
}

func feishuContextItemToPB(item threadcontext.ContextItem) *pb.FeishuContextItem {
	return &pb.FeishuContextItem{
		Id:          item.ID,
		ItemType:    item.ItemType,
		AuthorLabel: item.AuthorLabel,
		Text:        item.Text,
		BridgeId:    item.BridgeId,
		BotProfile:  item.BotProfile,
		TargetName:  item.TargetName,
		SessionName: item.SessionName,
		MessageId:   item.MessageID,
		CreatedAt:   item.CreatedAt,
		Truncated:   item.Truncated,
	}
}

func feishuBindingToPB(b bridge.Binding) *pb.FeishuBinding {
	return &pb.FeishuBinding{
		BridgeId:    b.BridgeId,
		RootMsgId:   b.RootMsgID,
		ThreadId:    b.ThreadID,
		OpenId:      b.OpenId,
		TargetId:    b.TargetID,
		TargetName:  b.TargetName,
		SessionName: b.SessionName,
		AgentType:   b.AgentType,
		Status:      b.Status,
	}
}

func (s *hubServer) Run(ctx context.Context, req *pb.AmuxdRunRequest) (*pb.RunResponse, error) {
	if agentprofile.IsManagedEvaluationProfileRef(req.GetAgentProfile()) ||
		(req.GetProfileSpec() != nil && agentprofile.IsManagedEvaluationProfileRef(req.GetProfileSpec().GetRef())) {
		return nil, status.Error(codes.InvalidArgument, "local expert evaluation profiles only support ephemeral tasks")
	}
	actor, actorErr := authenticatedActor(ctx, s.principals)
	if actorErr != nil {
		return nil, actorErr
	}
	identityResult := executionauth.New(s.principals).FreezeSessionIdentity(executionauth.FreezeSessionRequest{
		Actor:             actor,
		Source:            principal.SourceCLI,
		RunAs:             req.RunAs,
		ExecutionStateRef: "session:" + req.Name,
		Sessions:          sessionFactsFromTargets(s.currentTargets()),
	})
	if !identityResult.Decision.Allowed {
		if identityResult.Decision.Reason == executionauth.ReasonRunAsRequiresActor {
			return nil, status.Error(codes.Unauthenticated, "run_as requires CLI enrollment")
		}
		return nil, status.Errorf(codes.PermissionDenied, "resolve execution identity: %v", identityResult.Decision.Err)
	}
	identity := identityResult.Identity
	if identity.Identified() {
		if p, err := s.principals.Get(identity.EffectivePrincipalID); err == nil &&
			p.EnvironmentTargetID != "" && p.EnvironmentTargetID != req.TargetId {
			return nil, status.Errorf(codes.FailedPrecondition,
				"principal environment is fixed to target %s", p.EnvironmentTargetID)
		}
	}
	launchID := "launch-" + uuid.NewString()
	payload := &pb.RunRequest{
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
		ExecutionIdentity:     principal.ToProtoIdentity(identity),
		LaunchId:              launchID,
	}
	payload.AgentActorToken, payload.AgentActorTokenExpiresAt = s.issueAgentActorToken(
		identity, sessionAgentCredentialSubject(req.TargetId, req.Name))
	return routeUnary[pb.RunRequest, pb.RunResponse](s, ctx, req.TargetId, "Run", payload)
}

func authenticatedActor(ctx context.Context, store *principal.Store) (*principal.ActorContext, error) {
	if store == nil {
		return nil, nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	token := ""
	for _, value := range md.Get("x-amux-actor-token") {
		if strings.TrimSpace(value) != "" {
			token = strings.TrimSpace(value)
			break
		}
	}
	if token == "" {
		return nil, nil
	}
	requestID := ""
	for _, value := range md.Get("x-amux-request-id") {
		if strings.TrimSpace(value) != "" {
			requestID = strings.TrimSpace(value)
			break
		}
	}
	actor, err := store.AuthenticateActor(token, requestID)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%v", err)
	}
	return &actor, nil
}

func (s *hubServer) Kill(ctx context.Context, req *pb.AmuxdKillRequest) (*pb.KillResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.KillRequest, pb.KillResponse](s, ctx, req.TargetId, "Kill", &pb.KillRequest{Name: req.Name})
}

func (s *hubServer) Restart(ctx context.Context, req *pb.AmuxdRestartRequest) (*pb.RunResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	if req.Replacement != nil && req.RefreshProfile {
		return nil, status.Error(codes.InvalidArgument, "replacement and refresh_profile are mutually exclusive")
	}
	token, expires := s.relaunchAgentActorToken(req.TargetId, req.Name)
	return routeUnary[pb.RestartRequest, pb.RunResponse](s, ctx, req.TargetId, "Restart", &pb.RestartRequest{
		Name: req.Name, RefreshProfile: req.RefreshProfile,
		Replacement: req.Replacement, PreserveHistory: req.PreserveHistory,
		AgentActorToken: token, AgentActorTokenExpiresAt: expires,
	})
}

func (s *hubServer) ChangeCwd(ctx context.Context, req *pb.AmuxdChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.ChangeCwdRequest, pb.ChangeCwdResponse](s, ctx, req.TargetId, "ChangeCwd", runtimeChangeCwdRequest(req))
}

func runtimeChangeCwdRequest(req *pb.AmuxdChangeCwdRequest) *pb.ChangeCwdRequest {
	return &pb.ChangeCwdRequest{
		Name:           req.Name,
		Cwd:            req.Cwd,
		Wait:           req.Wait,
		Interrupt:      req.Interrupt,
		ExpectedCwd:    req.ExpectedCwd,
		DeferUntilIdle: req.DeferUntilIdle,
	}
}

func (s *hubServer) Sleep(ctx context.Context, req *pb.AmuxdSleepRequest) (*pb.SleepResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.SleepRequest, pb.SleepResponse](s, ctx, req.TargetId, "Sleep", &pb.SleepRequest{Name: req.Name})
}

func (s *hubServer) Resume(ctx context.Context, req *pb.AmuxdResumeRequest) (*pb.RunResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	token, expires := s.relaunchAgentActorToken(req.TargetId, req.Name)
	return routeUnary[pb.ResumeRequest, pb.RunResponse](s, ctx, req.TargetId, "Resume", &pb.ResumeRequest{
		Name: req.Name, AgentActorToken: token, AgentActorTokenExpiresAt: expires,
	})
}

func (s *hubServer) Prune(ctx context.Context, req *pb.AmuxdPruneRequest) (*pb.PruneResponse, error) {
	if err := s.authorizeSessionPrune(ctx); err != nil {
		return nil, err
	}
	out, err := routeUnary[pb.PruneRequest, pb.PruneResponse](s, ctx, req.TargetId, "Prune", &pb.PruneRequest{})
	if err != nil {
		return nil, err
	}
	s.hub.RemoveSessions(req.TargetId, out.Names)
	return out, nil
}

func (s *hubServer) Interrupt(ctx context.Context, req *pb.AmuxdInterruptRequest) (*pb.InterruptResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.InterruptRequest, pb.InterruptResponse](s, ctx, req.TargetId, "Interrupt", &pb.InterruptRequest{Name: req.Name, PreserveGoal: req.PreserveGoal})
}

func (s *hubServer) Submit(ctx context.Context, req *pb.AmuxdSubmitRequest) (*pb.SubmitResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.SubmitRequest, pb.SubmitResponse](s, ctx, req.TargetId, "Submit", &pb.SubmitRequest{
		Name: req.Name, Text: req.Text, Source: req.Source, Async: req.Async, Preempt: req.Preempt,
		PreemptAlreadyInterrupted: req.PreemptAlreadyInterrupted, Control: req.Control, Parts: req.Parts,
		Steer: req.Steer,
	})
}

func (s *hubServer) QueueList(ctx context.Context, req *pb.AmuxdQueueListRequest) (*pb.QueueListResponse, error) {
	if err := s.authorizeSessionRead(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.QueueListRequest, pb.QueueListResponse](s, ctx, req.TargetId, "QueueList", &pb.QueueListRequest{Name: req.Name})
}

func (s *hubServer) QueueClear(ctx context.Context, req *pb.AmuxdQueueClearRequest) (*pb.QueueClearResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.QueueClearRequest, pb.QueueClearResponse](s, ctx, req.TargetId, "QueueClear", &pb.QueueClearRequest{Name: req.Name})
}

func (s *hubServer) QueueCancel(ctx context.Context, req *pb.AmuxdQueueCancelRequest) (*pb.QueueCancelResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.QueueCancelRequest, pb.QueueCancelResponse](s, ctx, req.TargetId, "QueueCancel", &pb.QueueCancelRequest{Name: req.Name, Id: req.Id})
}

func (s *hubServer) RespondPermission(ctx context.Context, req *pb.AmuxdRespondPermissionRequest) (*pb.RespondPermissionResponse, error) {
	if err := s.authorizeSessionMutation(ctx, req.TargetId, req.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.RespondPermissionRequest, pb.RespondPermissionResponse](s, ctx, req.TargetId, "RespondPermission", &pb.RespondPermissionRequest{Name: req.Name, PermissionId: req.PermissionId, OptionId: req.OptionId, OptionIds: req.OptionIds, Cancel: req.Cancel, TurnId: req.TurnId})
}

// sessionExecutionIdentity looks up the frozen identity of a live session on a
// target, so hub-side handlers can authorize against it or mint for it.
func (s *hubServer) sessionExecutionIdentity(targetID, name string) (principal.ExecutionIdentity, bool) {
	if s == nil {
		return principal.ExecutionIdentity{}, false
	}
	sess := sessionByRef(s.currentTargets(), targetID, name)
	if sess == nil {
		return principal.ExecutionIdentity{}, false
	}
	return principal.FromProtoIdentity(sess.ExecutionIdentity), true
}

func (s *hubServer) authorizeSessionMutation(ctx context.Context, targetID, name string) error {
	return s.authorizeSessionAction(ctx, targetID, name, "mutation")
}

// authorizeSessionRead guards a single-session read (Attach, QueueList). Reads
// and mutations share one decision rule — a caller may not read what it may
// not mutate — so this runs the same core with a read resource word.
func (s *hubServer) authorizeSessionRead(ctx context.Context, targetID, name string) error {
	return s.authorizeSessionAction(ctx, targetID, name, "read")
}

// authorizeSessionAction is the single decision core for session authorization.
// The identified-session ownership boundary only applies while fleet isolation
// is active; without it every session already runs as the same host user and is
// shared across callers. Explicit invalid credentials are still rejected.
func (s *hubServer) authorizeSessionAction(ctx context.Context, targetID, name, action string) error {
	if s.principals == nil {
		return nil
	}
	targets := s.currentTargets()
	sess := sessionByRef(targets, targetID, name)
	if sess == nil {
		// A session the hub cannot see carries no identity to check; the
		// routed RPC itself reports the miss downstream.
		return nil
	}
	identity := principal.FromProtoIdentity(sess.ExecutionIdentity)
	if identity.IdentityMode == "" || identity.IdentityMode == principal.IdentityModeLegacyHost {
		return nil
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return err
	}
	decision := executionauth.New(s.principals).SessionAccess(executionauth.SessionAccessRequest{
		Actor: actor,
		Target: executionauth.SessionFacts{
			ID:       sess.Id,
			TargetID: targetID,
			Name:     sess.Name,
			LaunchID: sess.LaunchId,
			Identity: identity,
		},
		Sessions:        sessionFactsFromTargets(targets),
		IsolationActive: s.fleetIsolationActive(),
	})
	if decision.Allowed {
		return nil
	}
	if decision.Kind == executionauth.DenialUnauthenticated {
		return status.Errorf(codes.Unauthenticated, "identified session %s requires enrollment", action)
	}
	if decision.Reason == executionauth.ReasonOutsideDelegatedReach {
		return status.Error(codes.PermissionDenied, "session is outside this agent's reach")
	}
	return status.Errorf(codes.PermissionDenied, "session authorization: %v", decision.Err)
}

// authorizeScheduleSessionTarget applies the normal session boundary when the
// target is currently visible. On an isolating fleet, an authenticated owner
// may create a deferred schedule for an offline target because every trigger is
// rechecked; an unidentified caller may not leave an unowned name-squatting
// row behind. Without isolation deferred targets are shared too.
func (s *hubServer) authorizeScheduleSessionTarget(ctx context.Context, targetID, name, ownerPrincipalID string) error {
	if s.principals == nil {
		return nil
	}
	if sessionByRef(s.currentTargets(), targetID, name) == nil {
		if !s.fleetIsolationActive() {
			return nil
		}
		if strings.TrimSpace(ownerPrincipalID) == "" {
			return status.Error(codes.Unauthenticated,
				"an unavailable session-prompt target requires an authenticated schedule owner")
		}
		return nil
	}
	return s.authorizeSessionAction(ctx, targetID, name, "schedule creation")
}

// authorizeSessionPrune keeps fleet-wide prune enrollment-only while principal
// isolation is active because the delete cannot be attributed to one
// principal. Without isolation every session already runs under the same host
// user, so a valid delegated actor has the same maintenance reach as an
// enrollment actor.
func (s *hubServer) authorizeSessionPrune(ctx context.Context) error {
	if s.principals == nil {
		return nil
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return err
	}
	decision := executionauth.New(s.principals).PruneAccess(actor, s.fleetIsolationActive())
	if !decision.Allowed {
		if decision.Reason == executionauth.ReasonEnrollmentRequired {
			return status.Error(codes.PermissionDenied, "session prune is restricted to enrollment actors; delegated agent credentials cannot prune — ask the enrolling operator to run it")
		}
		return status.Error(codes.Unauthenticated, "session prune requires enrollment")
	}
	return nil
}

func (s *hubServer) Attach(req *pb.AmuxdAttachRequest, stream pb.Amuxd_AttachServer) error {
	if req == nil || req.TimelineMode == pb.AttachRequest_TIMELINE_MODE_UNSPECIFIED {
		return status.Error(codes.InvalidArgument, "timeline mode is required")
	}
	if err := s.authorizeSessionRead(stream.Context(), req.TargetId, req.Name); err != nil {
		return err
	}
	payload := &pb.AttachRequest{
		Name:         req.Name,
		TimelineMode: req.TimelineMode,
		AfterCursor:  req.AfterCursor,
	}
	return s.hub.RouteStream(stream.Context(), req.TargetId, "Attach", payload,
		func(msg *pb.TunnelMsg) error {
			var ev pb.AttachEvent
			if err := json.Unmarshal(msg.Payload, &ev); err != nil {
				return err
			}
			return stream.Send(&ev)
		},
	)
}

// routeUnary is the template shared by every routed unary RPC: marshal-free
// dispatch through the hub, then decode the JSON reply. Attach (streaming) and
// the schedule RPCs (hub-local) do not use it.
func routeUnary[Req any, Resp any](s *hubServer, ctx context.Context, targetID, method string, payload *Req) (*Resp, error) {
	resp, err := s.hub.Route(ctx, targetID, method, payload)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var out Resp
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode response: %v", err)
	}
	return &out, nil
}

// ── Schedule RPCs (hub-local; not routed to a runtime) ───────────────────────

func scheduleItemFromTask(t schedule.Task, runs []schedule.Run) *pb.ScheduleItem {
	scheduleType := t.ScheduleType
	if scheduleType == "" {
		scheduleType = schedule.ScheduleTypeSessionPrompt
	}
	item := &pb.ScheduleItem{
		Id:           t.ID,
		Name:         t.Name,
		ScheduleType: scheduleType,
		TargetId:     t.TargetID,
		TargetName:   t.TargetName,
		SessionName:  t.SessionName,
		Spec:         t.Spec,
		Kind:         t.Kind,
		RunMode:      t.RunMode,
		Prompt:       t.Prompt,
		TaskSpecJson: append([]byte(nil), t.TaskSpecJson...),
		Enabled:      t.Enabled,
		LastStatus:   t.LastStatus,
	}
	if t.NextRunAt != nil {
		item.NextRunAt = *t.NextRunAt
	}
	if t.LastRunAt != nil {
		item.LastRunAt = *t.LastRunAt
	}
	if len(runs) > 0 {
		item.Runs = make([]*pb.ScheduleRun, 0, len(runs))
		for _, r := range runs {
			item.Runs = append(item.Runs, &pb.ScheduleRun{
				Id:         r.ID,
				ScheduleId: r.ScheduleID,
				FiredAt:    r.FiredAt,
				TaskId:     r.TaskID,
				Status:     r.Status,
			})
		}
	}
	return item
}

var resolveScheduledTaskSpec = agentprofile.ResolveTaskSpec

func normalizeScheduledTaskSpec(spec *task.Spec) error {
	if spec.Agent == "" {
		return fmt.Errorf("agent is required")
	}
	if spec.Task == "" {
		return fmt.Errorf("task is required")
	}
	permissionPolicy, err := normalizeScheduledTaskPermissionPolicy(spec.PermissionPolicy)
	if err != nil {
		return err
	}
	spec.PermissionPolicy = permissionPolicy
	if err := normalizeScheduledTaskWorkspace(&spec.Workspace); err != nil {
		return err
	}
	return nil
}

func normalizeScheduledTaskPermissionPolicy(value task.PermissionPolicy) (task.PermissionPolicy, error) {
	switch policy := task.PermissionPolicy(strings.TrimSpace(string(value))); policy {
	case "", "default":
		return "", nil
	case task.PolicyAllow, task.PolicyDenyAll:
		return policy, nil
	case task.PolicyEscalateToMain:
		return "", fmt.Errorf("permission_policy %q is not allowed for scheduled subtask", task.PolicyEscalateToMain)
	default:
		return "", fmt.Errorf("invalid permission_policy %q for scheduled subtask (expected empty/default, policy-allow, or deny-all)", value)
	}
}

func normalizeScheduledTaskWorkspace(workspace *task.WorkspaceSpec) error {
	workspace.Policy = task.WorkspacePolicy(strings.TrimSpace(string(workspace.Policy)))
	workspace.LocalDir = strings.TrimSpace(workspace.LocalDir)
	if workspace.Repo != nil {
		workspace.Repo.CacheKey = strings.TrimSpace(workspace.Repo.CacheKey)
		workspace.Repo.Commit = strings.TrimSpace(workspace.Repo.Commit)
	}
	if workspace.Policy == task.WorkspaceGitWorktree && workspace.LocalDir == "" && (workspace.Repo == nil || workspace.Repo.CacheKey == "") {
		return fmt.Errorf("workspace.local_dir or workspace.repo.cache_key is required with workspace.policy=git-worktree")
	}
	return nil
}

func resolveScheduledTaskSpecCopy(spec task.Spec) (task.Spec, error) {
	resolved := spec
	if resolveScheduledTaskSpec != nil {
		if err := resolveScheduledTaskSpec(&resolved, agentprofile.DefaultPackRegistryPath()); err != nil {
			return task.Spec{}, err
		}
	}
	return resolved, nil
}

func normalizeResolvedScheduledTaskSpec(spec *task.Spec) error {
	permissionPolicy, err := normalizeScheduledTaskPermissionPolicy(spec.PermissionPolicy)
	if err != nil {
		return fmt.Errorf("effective %w", err)
	}
	spec.PermissionPolicy = permissionPolicy
	if err := normalizeScheduledTaskWorkspace(&spec.Workspace); err != nil {
		return fmt.Errorf("effective %w", err)
	}
	if spec.ResolvedProfile != nil {
		profilePolicy, err := normalizeScheduledTaskPermissionPolicy(task.PermissionPolicy(spec.ResolvedProfile.PermissionPolicy))
		if err != nil {
			return fmt.Errorf("effective profile %w", err)
		}
		spec.ResolvedProfile.PermissionPolicy = string(profilePolicy)
	}
	return nil
}

// normalizeAndResolveSubtaskSpec decodes a raw task spec JSON, then runs the
// normalize → resolve → normalize-resolved pipeline used by both CreateSchedule
// and UpdateSchedule for subtask schedules. Returns the re-encoded normalized
// spec JSON ready to store on the schedule row.
func (s *hubServer) normalizeAndResolveSubtaskSpec(ctx context.Context, raw []byte) ([]byte, error) {
	var spec task.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode task spec: %v", err)
	}
	if spec.ExecutionIdentity != nil {
		return nil, status.Error(codes.InvalidArgument, "execution_identity is server-resolved")
	}
	if err := normalizeScheduledTaskSpec(&spec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	resolvedSpec, err := resolveScheduledTaskSpecCopy(spec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve task spec: %v", err)
	}
	if err := validateManagedEvaluationTask(resolvedSpec, true); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := normalizeResolvedScheduledTaskSpec(&resolvedSpec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return nil, err
	}
	identityResult := executionauth.New(s.principals).FreezeScheduleIdentity(executionauth.FreezeScheduleRequest{
		Actor:             actor,
		RunAs:             spec.RunAs,
		ExecutionStateRef: "schedule-definition:" + uuid.NewString(),
		IsolationActive:   s.isolatingRuntimeRegistered(),
	})
	if identityResult.Decision.Allowed {
		if identityResult.Identity.Identified() {
			identity := identityResult.Identity
			spec.ExecutionIdentity = &identity
			spec.RunAs = ""
		}
	} else if identityResult.Decision.Reason == executionauth.ReasonRunAsRequiresActor {
		return nil, status.Error(codes.Unauthenticated, "scheduled run_as requires CLI enrollment")
	} else if identityResult.Decision.Reason == executionauth.ReasonIdentifiedScheduleRequired {
		// Storing this schedule would only defer the failure: an isolating
		// runtime refuses an unidentified execution, so every future trigger
		// dies in the task log rather than here, where the caller can still act.
		return nil, status.Error(codes.Unauthenticated,
			"this host isolates executions by principal; a scheduled subtask must be created by an identified caller "+
				"(from an agent session started after enrollment, so it carries AMUX_ACTOR_TOKEN, or from a CLI enrolled with `amux isolation enroll`)")
	} else {
		return nil, status.Errorf(codes.PermissionDenied, "resolve scheduled execution identity: %v", identityResult.Decision.Err)
	}
	normalizedSpecJson, err := json.Marshal(spec)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode task spec: %v", err)
	}
	return normalizedSpecJson, nil
}

// targetsIncludeIsolatingRuntime reports whether any target declares the
// per-principal isolation policy. New runtimes advertise policy intent through
// FeaturePrincipalIsolation even when their helper is degraded; the helper
// protocol check preserves compatibility with older isolating runtimes.
func targetsIncludeIsolatingRuntime(targets []*pb.Target) bool {
	for _, target := range targets {
		if target == nil || target.Capabilities == nil {
			continue
		}
		if slices.Contains(target.Capabilities.Features, registry.FeaturePrincipalIsolation) ||
			strings.TrimSpace(target.Capabilities.IsolationHelperProtocol) != "" {
			return true
		}
	}
	return false
}

// isolatingRuntimeRegistered reports whether any registered runtime enforces
// per-principal isolation. A subtask schedule carries no target binding — it is
// placed at trigger time — so "will this run" is a property of the fleet, not
// of one runtime.
func (s *hubServer) isolatingRuntimeRegistered() bool {
	return targetsIncludeIsolatingRuntime(s.currentTargets())
}

// fleetIsolationActive reports whether the fleet-level identity boundary
// applies on the control plane: session principal ownership, schedule
// ownership (rule four), and task/instance ownership authorization and list
// filtering (rule six). These rules mirror the sandbox boundary: they keep an
// agent inside an isolated execution from reaching other principals' work
// through the control plane while allowing same-principal sibling sessions. On
// a fleet with no isolating runtime there is no such boundary — every session
// already runs as the same host user — so the boundary has nothing to protect
// and would only hide work from the agents operating it. Like the
// subtask-schedule rule, the judgment is fleet-wide rather than per target.
func (s *hubServer) fleetIsolationActive() bool {
	return s.isolatingRuntimeRegistered()
}

// requireSchedule returns the schedule store or an Unavailable error when the
// hub is not running a scheduler. Every schedule RPC guards on it.
func (s *hubServer) requireSchedule() (*schedule.Store, error) {
	if s.schedule == nil {
		return nil, status.Errorf(codes.Unavailable, "scheduler not available")
	}
	return s.schedule, nil
}

// scheduleOwner returns the principal a schedule RPC acts as, or "" when the
// caller carries no credential. One hub serves every Feishu user, so this is
// the only thing that tells two of them apart.
func (s *hubServer) scheduleOwner(ctx context.Context) (string, error) {
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return "", err
	}
	if actor == nil {
		return "", nil
	}
	return actor.ActorPrincipalID, nil
}

// scheduleOwnerPrincipal returns the principal a schedule belongs to. Rows
// stored before ownership was recorded carry no owner of their own, but most
// of them are still attributable: a subtask schedule froze its creator's
// execution identity, and a session-prompt schedule drives one session that
// carries an identity. Only what neither names a principal for — a legacy-host
// session, or an installation with no identities at all — stays unowned, and
// therefore shared.
func (s *hubServer) scheduleOwnerPrincipal(row schedule.Task) string {
	if owner := strings.TrimSpace(row.OwnerPrincipalID); owner != "" {
		return owner
	}
	var identity principal.ExecutionIdentity
	switch row.ScheduleType {
	case schedule.ScheduleTypeSubtask:
		var spec task.Spec
		if err := json.Unmarshal(row.TaskSpecJson, &spec); err != nil || spec.ExecutionIdentity == nil {
			return ""
		}
		identity = *spec.ExecutionIdentity
	default:
		found := false
		if identity, found = s.sessionExecutionIdentity(row.TargetID, row.SessionName); !found {
			return ""
		}
	}
	if !identity.Identified() {
		return ""
	}
	if origin := strings.TrimSpace(identity.OriginActorPrincipalID); origin != "" {
		return origin
	}
	return strings.TrimSpace(identity.EffectivePrincipalID)
}

// scheduleOwnerPrincipalBackfilled resolves ownership and writes a derived
// owner back, so a row stops depending on its session still being registered
// to stay protected. The write is best effort: failing a read because the
// backfill could not be persisted would be worse than deriving it again.
func (s *hubServer) scheduleOwnerPrincipalBackfilled(store *schedule.Store, row schedule.Task) string {
	owner := s.scheduleOwnerPrincipal(row)
	if owner != "" && strings.TrimSpace(row.OwnerPrincipalID) == "" {
		_ = store.SetOwnerIfUnset(row.ID, owner)
	}
	return owner
}

// authorizeScheduledSessionPrompt revalidates the frozen schedule owner against
// the session identity visible at trigger time while fleet isolation is active.
// Owner backfill still runs without isolation so attribution is ready if the
// boundary is enabled later.
func (s *hubServer) authorizeScheduledSessionPrompt(_ context.Context, row schedule.Task, session *pb.Session) error {
	if s.principals == nil {
		return nil
	}
	identity := principal.FromProtoIdentity(session.GetExecutionIdentity())
	if identity.IdentityMode == "" || identity.IdentityMode == principal.IdentityModeLegacyHost {
		return nil
	}
	owner := strings.TrimSpace(row.OwnerPrincipalID)
	if owner == "" {
		if identity.OriginActorPrincipalID != "" {
			owner = strings.TrimSpace(identity.OriginActorPrincipalID)
		} else {
			owner = strings.TrimSpace(identity.EffectivePrincipalID)
		}
		if owner != "" && s.schedule != nil {
			_ = s.schedule.SetOwnerIfUnset(row.ID, owner)
		}
	}
	decision := executionauth.New(s.principals).ScheduleTriggerAccess(owner, identity, s.fleetIsolationActive())
	if decision.Allowed {
		return nil
	}
	if decision.Reason == executionauth.ReasonScheduleOwnerRequired {
		return errors.New("session authorization: identified target requires a schedule owner")
	}
	return fmt.Errorf("session authorization: %w", decision.Err)
}

// authorizeScheduleAccess resolves the schedule and refuses callers other than
// its owner. It reports NotFound before PermissionDenied so a missing schedule
// never reads as someone else's. The ownership refusal applies only while fleet
// isolation is active: on a fleet with no isolating runtime every enrolled
// caller may read or change any schedule, the pre-rule-four behavior.
func (s *hubServer) authorizeScheduleAccess(ctx context.Context, store *schedule.Store, id int64) (schedule.Task, error) {
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return schedule.Task{}, err
	}
	row, err := store.Get(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schedule.Task{}, status.Errorf(codes.NotFound, "schedule %d not found", id)
		}
		return schedule.Task{}, status.Errorf(codes.Internal, "%v", err)
	}
	// The derived owner is backfilled even when the refusal is gated off, so the
	// row stays attributable if an isolating runtime joins the fleet later.
	owner := s.scheduleOwnerPrincipalBackfilled(store, row)
	decision := executionauth.New(s.principals).ScheduleAccess(actor, owner, s.fleetIsolationActive())
	if !decision.Allowed {
		return schedule.Task{}, status.Errorf(codes.PermissionDenied,
			"schedule %d belongs to another user; only its creator can read or change it", id)
	}
	return row, nil
}

func (s *hubServer) CreateSchedule(ctx context.Context, req *pb.CreateScheduleRequest) (*pb.CreateScheduleResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	scheduleType, err := schedule.NormalizeScheduleType(req.ScheduleType)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	spec, err := schedule.ParseSpec(req.Spec)
	if err != nil {
		return &pb.CreateScheduleResponse{Error: err.Error()}, nil
	}
	next, err := schedule.FirstRun(spec, time.Now())
	if err != nil {
		return &pb.CreateScheduleResponse{Error: err.Error()}, nil
	}
	ownerPrincipalID, err := s.scheduleOwner(ctx)
	if err != nil {
		return nil, err
	}
	taskRow := schedule.Task{
		Name:             req.Name,
		ScheduleType:     scheduleType,
		Spec:             spec.Raw,
		Kind:             spec.Kind,
		Enabled:          true,
		NextRunAt:        next,
		CreatedBy:        req.CreatedBy,
		OwnerPrincipalID: ownerPrincipalID,
	}
	switch scheduleType {
	case schedule.ScheduleTypeSessionPrompt:
		if req.TargetId == "" || req.SessionName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "target and session are required")
		}
		if req.Prompt == "" {
			return nil, status.Errorf(codes.InvalidArgument, "prompt is required")
		}
		runMode, err := schedule.NormalizeRunMode(req.RunMode)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if err := s.authorizeScheduleSessionTarget(ctx, req.TargetId, req.SessionName, ownerPrincipalID); err != nil {
			return nil, err
		}
		taskRow.TargetID = req.TargetId
		taskRow.TargetName = req.TargetName
		taskRow.SessionName = req.SessionName
		taskRow.Prompt = req.Prompt
		taskRow.RunMode = runMode
	case schedule.ScheduleTypeSubtask:
		normalizedSpecJson, err := s.normalizeAndResolveSubtaskSpec(ctx, req.TaskSpecJson)
		if err != nil {
			return nil, err
		}
		taskRow.TaskSpecJson = normalizedSpecJson
	}
	t, err := store.Create(taskRow)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	resp := &pb.CreateScheduleResponse{Id: t.ID}
	if t.NextRunAt != nil {
		resp.NextRunAt = *t.NextRunAt
	}
	return resp, nil
}

func (s *hubServer) ListSchedules(ctx context.Context, req *pb.ListSchedulesRequest) (*pb.ListSchedulesResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return nil, err
	}
	tasks, err := store.List(schedule.Filter{TargetID: req.TargetId, SessionName: req.SessionName})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := &pb.ListSchedulesResponse{Schedules: make([]*pb.ScheduleItem, 0, len(tasks))}
	isolation := s.fleetIsolationActive()
	for _, t := range tasks {
		// Ownership is resolved (and derived owners backfilled) unconditionally,
		// so the data is ready the moment an isolating runtime joins. Rule four
		// itself applies only while fleet isolation is active: on a fleet
		// without isolation there is no per-principal sandbox boundary, so
		// every row stays visible to every caller.
		owner := s.scheduleOwnerPrincipalBackfilled(store, t)
		if !executionauth.New(s.principals).ScheduleAccess(actor, owner, isolation).Allowed {
			continue
		}
		out.Schedules = append(out.Schedules, scheduleItemFromTask(t, nil))
	}
	return out, nil
}

func (s *hubServer) GetSchedule(ctx context.Context, req *pb.GetScheduleRequest) (*pb.GetScheduleResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	t, err := s.authorizeScheduleAccess(ctx, store, req.Id)
	if err != nil {
		return nil, err
	}
	var runs []schedule.Run
	if req.IncludeRuns {
		runs, err = store.ListRuns(req.Id, schedule.DefaultRunHistoryLimit)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}
	return &pb.GetScheduleResponse{Schedule: scheduleItemFromTask(t, runs)}, nil
}

func (s *hubServer) DeleteSchedule(ctx context.Context, req *pb.DeleteScheduleRequest) (*pb.DeleteScheduleResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeScheduleAccess(ctx, store, req.Id); err != nil {
		return nil, err
	}
	ok, err := store.Delete(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.DeleteScheduleResponse{Ok: ok}, nil
}

func (s *hubServer) SetScheduleEnabled(ctx context.Context, req *pb.SetScheduleEnabledRequest) (*pb.SetScheduleEnabledResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeScheduleAccess(ctx, store, req.Id); err != nil {
		return nil, err
	}
	ok, err := store.SetEnabled(req.Id, req.Enabled)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.SetScheduleEnabledResponse{Ok: ok}, nil
}

func (s *hubServer) UpdateSchedule(ctx context.Context, req *pb.UpdateScheduleRequest) (*pb.UpdateScheduleResponse, error) {
	store, err := s.requireSchedule()
	if err != nil {
		return nil, err
	}
	row, err := s.authorizeScheduleAccess(ctx, store, req.Id)
	if err != nil {
		return nil, err
	}
	scheduleType := row.ScheduleType
	if scheduleType == "" {
		scheduleType = schedule.ScheduleTypeSessionPrompt
	}

	if req.Name != nil {
		row.Name = *req.Name
	}
	if req.Spec != nil {
		spec, err := schedule.ParseSpec(*req.Spec)
		if err != nil {
			return &pb.UpdateScheduleResponse{Error: err.Error()}, nil
		}
		next, err := schedule.FirstRun(spec, time.Now())
		if err != nil {
			return &pb.UpdateScheduleResponse{Error: err.Error()}, nil
		}
		row.Spec = spec.Raw
		row.Kind = spec.Kind
		row.NextRunAt = next
	}

	switch scheduleType {
	case schedule.ScheduleTypeSessionPrompt:
		if len(req.TaskSpecJson) > 0 {
			return nil, status.Errorf(codes.InvalidArgument, "task_spec_json is only valid for subtask schedules")
		}
		if req.Prompt != nil {
			if strings.TrimSpace(*req.Prompt) == "" {
				return nil, status.Errorf(codes.InvalidArgument, "prompt must not be empty")
			}
			row.Prompt = *req.Prompt
		}
		if req.RunMode != nil {
			runMode, err := schedule.NormalizeRunMode(*req.RunMode)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "%v", err)
			}
			row.RunMode = runMode
		}
	case schedule.ScheduleTypeSubtask:
		if req.Prompt != nil || req.RunMode != nil {
			return nil, status.Errorf(codes.InvalidArgument, "prompt and run_mode are only valid for session-prompt schedules")
		}
		if len(req.TaskSpecJson) > 0 {
			normalizedSpecJson, err := s.normalizeAndResolveSubtaskSpec(ctx, req.TaskSpecJson)
			if err != nil {
				return nil, err
			}
			row.TaskSpecJson = normalizedSpecJson
		}
	}

	updated, err := store.Update(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "schedule %d not found", req.Id)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.UpdateScheduleResponse{Schedule: scheduleItemFromTask(updated, nil)}, nil
}
