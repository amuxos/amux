package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	_ "modernc.org/sqlite"
)

func TestHubAgentProfileDiscoveryReturnsResolvedSkills(t *testing.T) {
	packPath := t.TempDir()
	skillDir := filepath.Join(packPath, "skills", "service-health")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Diagnose service health\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := agentprofile.Profile{
		Namespace: "team", Name: "operator", Revision: "abc123", Type: "codex",
		Description: "Service operator", Skills: []string{"service-health"}, Source: agentprofile.SourcePack, PackPath: packPath,
	}
	srv := &hubServer{
		listAgentProfiles: func() ([]agentprofile.Profile, error) {
			return []agentprofile.Profile{profile}, errors.New("another pack is unavailable")
		},
		resolveAgentProfile: func(ref string) (agentprofile.Profile, error) {
			if ref != profile.Ref() {
				return agentprofile.Profile{}, agentprofile.ErrProfileNotFound
			}
			return profile, nil
		},
	}

	listed, err := srv.ListAgentProfiles(context.Background(), &pb.ListAgentProfilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Profiles) != 1 || listed.Profiles[0].Profile.GetRef() != "team/operator" {
		t.Fatalf("profiles = %+v", listed.Profiles)
	}
	if got := listed.Profiles[0].Skills; len(got) != 1 || got[0].GetName() != "service-health" || got[0].GetDescription() != "Diagnose service health" {
		t.Fatalf("skills = %+v", got)
	}
	if listed.GetProfilesError() != "another pack is unavailable" {
		t.Fatalf("profiles_error = %q", listed.GetProfilesError())
	}

	detail, err := srv.GetAgentProfile(context.Background(), &pb.GetAgentProfileRequest{Ref: profile.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Profile.GetProfile().GetPinnedRef() != "team/operator@abc123" || len(detail.Profile.GetSkills()) != 1 {
		t.Fatalf("profile detail = %+v", detail.Profile)
	}
}

func TestHubGetAgentProfileValidatesRefAndMapsNotFound(t *testing.T) {
	srv := &hubServer{resolveAgentProfile: func(string) (agentprofile.Profile, error) {
		return agentprofile.Profile{}, fmt.Errorf("missing: %w", agentprofile.ErrProfileNotFound)
	}}
	if _, err := srv.GetAgentProfile(context.Background(), &pb.GetAgentProfileRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty ref code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
	if _, err := srv.GetAgentProfile(context.Background(), &pb.GetAgentProfileRequest{Ref: "team/missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing ref code = %v, want NotFound (err=%v)", status.Code(err), err)
	}
}

func TestRuntimeChangeCwdRequestPreservesDeferredMode(t *testing.T) {
	got := runtimeChangeCwdRequest(&pb.AmuxdChangeCwdRequest{
		TargetId: "target-1", Name: "work", Cwd: "/repo", ExpectedCwd: "/old", DeferUntilIdle: true,
	})
	if got.Name != "work" || got.Cwd != "/repo" || got.ExpectedCwd != "/old" || !got.DeferUntilIdle {
		t.Fatalf("runtime cwd request = %+v", got)
	}
}

func TestFeishuStatusAndBindingsReadHubState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("default", &bridge.Secrets{
		AppId:        "cli_test",
		AppSecret:    "secret",
		OpenId:       "ou_owner",
		NotifyChatId: "oc_workspace",
		BotName:      "build-bot",
		BotOpenId:    "ou_bot",
	}); err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}
	dbPath := bridge.DefaultDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE lark_chats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			open_id TEXT NOT NULL,
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL,
			agent_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			latest_agent_output TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO lark_chats (bridge_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_type, status)
		VALUES ('feishu:cli_test', 'root-1', 'thread-1', 'ou_owner', 'target-1', 'remote-dev-1', 'c1', 'codex', 'active');
	`); err != nil {
		t.Fatal(err)
	}

	srv := &hubServer{}
	status, err := srv.FeishuStatus(context.Background(), &pb.FeishuStatusRequest{Profile: "default"})
	if err != nil {
		t.Fatalf("FeishuStatus: %v", err)
	}
	if !status.Configured || status.AppId != "cli_test" || status.OpenId != "ou_owner" || status.NotifyChatId != "oc_workspace" || status.BotName != "build-bot" || status.BotOpenId != "ou_bot" || status.BindingCount != 1 || !status.BridgeDbHealthy {
		t.Fatalf("status = %+v", status)
	}

	bindings, err := srv.FeishuBindings(context.Background(), &pb.FeishuBindingsRequest{Profile: "default"})
	if err != nil {
		t.Fatalf("FeishuBindings: %v", err)
	}
	if len(bindings.Bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(bindings.Bindings))
	}
	got := bindings.Bindings[0]
	if got.BridgeId != "feishu:cli_test" || got.RootMsgId != "root-1" || got.TargetName != "remote-dev-1" || got.SessionName != "c1" || got.Status != "active" {
		t.Fatalf("binding = %+v", got)
	}
}

func TestFeishuBotSessionBindStatusAndOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{
		AppId:     "cli_bot_a",
		AppSecret: "secret",
		OpenId:    "ou_owner",
	}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	controller := &recordingFeishuBotSessionController{profile: "bot_a", bridgeID: "feishu:cli_bot_a"}
	srv := &hubServer{
		listTargets: func() []*pb.Target {
			return []*pb.Target{{
				Id:   "target-1",
				Name: "local",
				Sessions: []*pb.Session{{
					Id:           "session-1",
					Name:         "bot-a-main",
					AgentType:    "codex",
					AgentProfile: "builtin/codex",
					Cwd:          "/repo/amux",
				}},
			}}
		},
		feishuBotSessionControllers: map[string]feishuBotSessionController{"bot_a": controller},
	}

	bound, err := srv.FeishuBotSessionBind(context.Background(), &pb.FeishuBotSessionBindRequest{
		Profile:    "bot_a",
		SessionRef: "local/bot-a-main",
	})
	if err != nil {
		t.Fatalf("FeishuBotSessionBind: %v", err)
	}
	if bound.Profile != "bot_a" || bound.BridgeId != "feishu:cli_bot_a" || bound.BotSession == nil || !bound.BotSession.Enabled {
		t.Fatalf("bound response = %+v", bound)
	}
	if bound.InteractionMode != config.FeishuInteractionSingleton {
		t.Fatalf("bound mode = %q, want singleton", bound.InteractionMode)
	}
	if got := bound.BotSession; got.TargetId != "target-1" || got.TargetName != "local" || got.SessionName != "bot-a-main" || got.AgentProfile != "builtin/codex" || got.AgentType != "codex" || got.Cwd != "/repo/amux" {
		t.Fatalf("bot session = %+v, want local/bot-a-main codex", got)
	}
	if controller.bindCalls != 1 || controller.lastBound.SessionName != "bot-a-main" || controller.lastBound.Cwd != "/repo/amux" {
		t.Fatalf("controller bind calls = %d, last = %+v", controller.bindCalls, controller.lastBound)
	}

	status, err := srv.FeishuBotSessionStatus(context.Background(), &pb.FeishuBotSessionStatusRequest{Profile: "bot_a"})
	if err != nil {
		t.Fatalf("FeishuBotSessionStatus: %v", err)
	}
	if status.BotSession == nil || !status.BotSession.Enabled || status.BotSession.SessionName != "bot-a-main" {
		t.Fatalf("status = %+v, want enabled bot-a-main", status)
	}
	if status.InteractionMode != config.FeishuInteractionSingleton {
		t.Fatalf("status mode = %q, want singleton", status.InteractionMode)
	}

	off, err := srv.FeishuBotSessionOff(context.Background(), &pb.FeishuBotSessionOffRequest{Profile: "bot_a"})
	if err != nil {
		t.Fatalf("FeishuBotSessionOff: %v", err)
	}
	if off.BotSession == nil || off.BotSession.Enabled {
		t.Fatalf("off response = %+v, want disabled bot session", off)
	}
	if controller.disableCalls != 1 {
		t.Fatalf("controller disable calls = %d, want 1", controller.disableCalls)
	}
}

type recordingFeishuBotSessionController struct {
	profile      string
	bridgeID     string
	bindCalls    int
	disableCalls int
	lastBound    bridge.BotSession
}

func (c *recordingFeishuBotSessionController) BindBotSession(bs bridge.BotSession, _ string) error {
	c.bindCalls++
	c.lastBound = bs
	if err := bridge.SetBotSessionInDB(bridge.DefaultDBPath(), bs); err != nil {
		return err
	}
	return config.SetBridgeFeishuInteractionMode(c.profile, config.FeishuInteractionSingleton)
}

func (c *recordingFeishuBotSessionController) DisableBotSession() error {
	c.disableCalls++
	return bridge.DisableBotSessionInDB(bridge.DefaultDBPath(), c.bridgeID)
}

func TestFeishuBotSessionBindRejectsMissingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "cli_bot_a", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	srv := &hubServer{
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Name: "local"}}
		},
	}

	_, err := srv.FeishuBotSessionBind(context.Background(), &pb.FeishuBotSessionBindRequest{
		Profile:    "bot_a",
		SessionRef: "local/missing",
	})
	if err == nil || status.Code(err) != codes.NotFound {
		t.Fatalf("FeishuBotSessionBind error = %v, want NotFound", err)
	}
}

func TestFeishuBotSessionBindWithoutControllerRejectsGroupWatchConfiguredProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	configPath := filepath.Join(home, ".amux", "config.toml")
	if err := config.UpdateConfigFile(configPath, func(raw map[string]any) error {
		raw["bridge"] = map[string]any{"feishu": map[string]any{"profiles": map[string]any{
			"bot_a": map[string]any{"group_watch": map[string]any{"enabled": true, "chat_ids": []string{"oc_alarm"}}},
		}}}
		return nil
	}); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	if err := config.Load(configPath); err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "cli_bot_a", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	srv := &hubServer{
		listTargets: func() []*pb.Target {
			return []*pb.Target{{Id: "target-1", Name: "local", Sessions: []*pb.Session{{Name: "bot-a-main", AgentType: "codex"}}}}
		},
		feishuBotSessionControllers: map[string]feishuBotSessionController{},
	}

	_, err := srv.FeishuBotSessionBind(context.Background(), &pb.FeishuBotSessionBindRequest{Profile: "bot_a", SessionRef: "local/bot-a-main"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("bind error = %v, want FailedPrecondition 群值守 incompatibility", err)
	}
	if _, statErr := os.Stat(bridge.DefaultDBPath()); !os.IsNotExist(statErr) {
		t.Fatalf("bridge DB exists after rejected bind: %v", statErr)
	}
}

func TestHubMaterializeCredentialFiltersEnvNames(t *testing.T) {
	srv := &hubServer{
		creds: staticCredentialProvider{mat: task.CredentialMaterial{
			Kind: "long-lived",
			Env:  []string{"GLM_API_KEY=central-secret", "OTHER=ignored"},
		}},
	}
	resp, err := srv.MaterializeCredential(context.Background(), &pb.MaterializeCredentialRequest{
		Profile:   "openai-compat",
		AgentType: "codex",
		EnvNames:  []string{"GLM_API_KEY"},
	})
	if err != nil {
		t.Fatalf("MaterializeCredential: %v", err)
	}
	if got, want := resp.Env, []string{"GLM_API_KEY=central-secret"}; !equalStrings(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	if resp.Kind != "long-lived" {
		t.Fatalf("kind = %q, want long-lived", resp.Kind)
	}
}

func TestHubMaterializeCredentialUsesAMUXHome(t *testing.T) {
	home := t.TempDir()
	amuxHome := filepath.Join(t.TempDir(), ".amux-test")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", amuxHome)

	if err := os.MkdirAll(filepath.Join(amuxHome, "secrets"), 0700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(amuxHome, "secrets", "openai-compat.json"), []byte(`{
		"kind": "long-lived",
		"env": {"GLM_API_KEY": "central-from-amux-home", "OTHER": "ignored"}
	}`), 0600); err != nil {
		t.Fatalf("write credential: %v", err)
	}

	resp, err := (&hubServer{}).MaterializeCredential(context.Background(), &pb.MaterializeCredentialRequest{
		Profile:   "openai-compat",
		AgentType: "codex",
		EnvNames:  []string{"GLM_API_KEY"},
	})
	if err != nil {
		t.Fatalf("MaterializeCredential: %v", err)
	}
	if got, want := resp.Env, []string{"GLM_API_KEY=central-from-amux-home"}; !equalStrings(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}

func TestHubMaterializeCredentialLooksUpEnvByName(t *testing.T) {
	home := t.TempDir()
	amuxHome := filepath.Join(t.TempDir(), ".amux-test")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", amuxHome)

	secretsDir := filepath.Join(amuxHome, "secrets")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "shared-service.json"), []byte(`{
		"kind": "short-lived",
		"env": {"SERVICE_TOKEN": "central-demo", "OTHER": "ignored"}
	}`), 0600); err != nil {
		t.Fatalf("write credential: %v", err)
	}

	resp, err := (&hubServer{}).MaterializeCredential(context.Background(), &pb.MaterializeCredentialRequest{
		AgentType: "codex",
		EnvNames:  []string{"SERVICE_TOKEN"},
	})
	if err != nil {
		t.Fatalf("MaterializeCredential: %v", err)
	}
	if got, want := resp.Profiles, []string{"shared-service"}; !equalStrings(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	if got, want := resp.Env, []string{"SERVICE_TOKEN=central-demo"}; !equalStrings(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}

func TestHubCredentialsAreScopedByAuthenticatedPrincipal(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	principals := agentTokenTestStore(t)
	first, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "second"})
	if err != nil {
		t.Fatal(err)
	}
	firstCredential, err := principals.IssueAgentCredential(first.ID, "launch-first", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	secondCredential, err := principals.IssueAgentCredential(second.ID, "launch-second", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	srv := &hubServer{principals: principals, isolationConfig: func() (config.IsolationConfig, error) {
		return config.IsolationConfig{Enabled: true, Backend: principal.BackendLinuxDAC}, nil
	}}

	if _, err := srv.SetCredential(actorCtx(firstCredential.Token), &pb.SetCredentialRequest{
		Profile: "openai-compat", EnvName: "GLM_API_KEY", Value: "first-secret",
	}); err != nil {
		t.Fatalf("SetCredential(first): %v", err)
	}
	if _, err := srv.SetCredential(actorCtx(secondCredential.Token), &pb.SetCredentialRequest{
		Profile: "openai-compat", EnvName: "GLM_API_KEY", Value: "second-secret",
	}); err != nil {
		t.Fatalf("SetCredential(second): %v", err)
	}

	for _, tc := range []struct {
		name        string
		principalID string
		token       string
		wantSecret  string
	}{
		{name: "first", principalID: first.ID, token: firstCredential.Token, wantSecret: "first-secret"},
		{name: "second", principalID: second.ID, token: secondCredential.Token, wantSecret: "second-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.MaterializeCredential(actorCtx(tc.token), &pb.MaterializeCredentialRequest{
				Profile: "openai-compat", AgentType: "codex", EnvNames: []string{"GLM_API_KEY"},
			})
			if err != nil {
				t.Fatalf("MaterializeCredential: %v", err)
			}
			if got, want := resp.Env, []string{"GLM_API_KEY=" + tc.wantSecret}; !equalStrings(got, want) {
				t.Fatalf("env = %v, want %v", got, want)
			}
			listed, err := srv.ListCredentials(actorCtx(tc.token), &pb.ListCredentialsRequest{})
			if err != nil {
				t.Fatalf("ListCredentials: %v", err)
			}
			if len(listed.Profiles) != 1 || listed.Profiles[0].Profile != "openai-compat" {
				t.Fatalf("profiles = %+v", listed.Profiles)
			}
			wantPath := filepath.Join(amuxHome, "principals", tc.principalID, "secrets", "openai-compat.json")
			if listed.Profiles[0].Path != wantPath {
				t.Fatalf("metadata path = %q, want %q", listed.Profiles[0].Path, wantPath)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(amuxHome, "secrets", "openai-compat.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy global credential unexpectedly written: %v", err)
	}
}

func TestHubMaterializeCredentialUsesLegacyStoreWhenIsolationDisabled(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	principals := agentTokenTestStore(t)
	user, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "feishu-user"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := principals.IssueAgentCredential(user.ID, "launch-derived", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(amuxHome, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(amuxHome, "secrets", "openai-compat.json"), []byte(`{
		"kind": "long-lived",
		"env": {"GLM_API_KEY": "shared-secret"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hosts without per-user OS isolation — never enabled, or explicitly
	// passthrough — materialize identified executions from the shared
	// legacy-host store instead of requiring per-principal credentials.
	for _, tc := range []struct {
		name string
		cfg  config.IsolationConfig
	}{
		{name: "disabled", cfg: config.IsolationConfig{Enabled: false, Backend: "auto"}},
		{name: "passthrough", cfg: config.IsolationConfig{Enabled: true, Backend: principal.BackendPassthrough}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &hubServer{principals: principals, isolationConfig: func() (config.IsolationConfig, error) {
				return tc.cfg, nil
			}}
			resp, err := srv.MaterializeCredential(actorCtx(credential.Token), &pb.MaterializeCredentialRequest{
				Profile: "openai-compat", AgentType: "codex", EnvNames: []string{"GLM_API_KEY"},
			})
			if err != nil {
				t.Fatalf("MaterializeCredential: %v", err)
			}
			if got, want := resp.Env, []string{"GLM_API_KEY=shared-secret"}; !equalStrings(got, want) {
				t.Fatalf("env = %v, want %v", got, want)
			}
			if _, err := os.Stat(filepath.Join(amuxHome, "principals", user.ID, "secrets", "openai-compat.json")); !os.IsNotExist(err) {
				t.Fatalf("principal store unexpectedly touched: %v", err)
			}

			// Credential management stays principal-scoped regardless of the
			// materialization store selection.
			listed, err := srv.ListCredentials(actorCtx(credential.Token), &pb.ListCredentialsRequest{})
			if err != nil {
				t.Fatalf("ListCredentials: %v", err)
			}
			if len(listed.Profiles) != 0 {
				t.Fatalf("management unexpectedly saw legacy profiles: %+v", listed.Profiles)
			}
		})
	}
}

func TestHubCredentialManagementRequiresAuthenticatedPrincipal(t *testing.T) {
	srv := &hubServer{principals: agentTokenTestStore(t)}
	_, err := srv.SetCredential(context.Background(), &pb.SetCredentialRequest{
		Profile: "openai-compat", EnvName: "GLM_API_KEY", Value: "secret",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("SetCredential code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

func TestHubCredentialSetupRejectsUnexpectedPrincipal(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	principals := agentTokenTestStore(t)
	first, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "second"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "operator", first.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	_, err = (&hubServer{principals: principals}).SetCredential(actorCtx(credential.Token), &pb.SetCredentialRequest{
		Profile: "openai-compat", EnvName: "GLM_API_KEY", Value: "must-not-be-written", ExpectedPrincipalId: second.ID,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("SetCredential code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
	if _, err := os.Stat(filepath.Join(amuxHome, "principals", first.ID, "secrets", "openai-compat.json")); !os.IsNotExist(err) {
		t.Fatalf("mismatched setup wrote credential: %v", err)
	}
}

func TestHubMaterializeCredentialPreservesMissingEnvDetails(t *testing.T) {
	secretsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretsDir, "example.json"), []byte(`{
		"kind": "long-lived",
		"env": {"EXAMPLE_API_KEY": "example-secret"}
	}`), 0600); err != nil {
		t.Fatalf("write example credential: %v", err)
	}
	_, err := (&hubServer{creds: task.FileCredentials{Dir: secretsDir}}).MaterializeCredential(
		context.Background(),
		&pb.MaterializeCredentialRequest{
			AgentType: "codex",
			EnvNames:  []string{"EXAMPLE_API_KEY", "PROVIDER_API_KEY"},
		},
	)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("MaterializeCredential error = %v, want FailedPrecondition", err)
	}
	st, _ := status.FromError(err)
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.Reason == "CREDENTIAL_ENV_NOT_FOUND" && info.Metadata["env_name"] == "PROVIDER_API_KEY" {
			return
		}
	}
	t.Fatalf("MaterializeCredential error details = %v, want missing env descriptor", st.Details())
}

type staticCredentialProvider struct {
	mat task.CredentialMaterial
}

func (s staticCredentialProvider) Materialize(context.Context, string, string) (task.CredentialMaterial, error) {
	return s.mat, nil
}

func equalStrings(a, b []string) bool {
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

func TestFeishuStatusBindingsAndBotsUseProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("planner", &bridge.Secrets{AppId: "cli_planner", AppSecret: "secret", OpenId: "ou_planner", BotName: "planner"}); err != nil {
		t.Fatalf("SaveProfileSecrets planner: %v", err)
	}
	if err := bridge.SaveProfileSecrets("coder", &bridge.Secrets{AppId: "cli_coder", AppSecret: "secret", OpenId: "ou_coder", BotName: "coder"}); err != nil {
		t.Fatalf("SaveProfileSecrets coder: %v", err)
	}
	dbPath := bridge.DefaultDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE lark_chats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			open_id TEXT NOT NULL,
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL,
			agent_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			latest_agent_output TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO lark_chats (bridge_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_type, status)
		VALUES
		  ('feishu:cli_planner', 'root-shared', 'thread-shared', 'ou_planner', 'target-1', 'planner-target', 'plan-login', 'codex', 'active'),
		  ('feishu:cli_coder', 'root-shared', 'thread-shared', 'ou_coder', 'target-2', 'coder-target', 'impl-login', 'codex', 'active');
	`); err != nil {
		t.Fatal(err)
	}

	srv := &hubServer{}
	status, err := srv.FeishuStatus(context.Background(), &pb.FeishuStatusRequest{Profile: "coder"})
	if err != nil {
		t.Fatalf("FeishuStatus coder: %v", err)
	}
	if !status.Configured || status.Profile != "coder" || status.BridgeId != "feishu:cli_coder" || status.AppId != "cli_coder" || status.BindingCount != 1 {
		t.Fatalf("coder status = %+v", status)
	}
	bindings, err := srv.FeishuBindings(context.Background(), &pb.FeishuBindingsRequest{Profile: "planner"})
	if err != nil {
		t.Fatalf("FeishuBindings planner: %v", err)
	}
	if len(bindings.Bindings) != 1 || bindings.Bindings[0].BridgeId != "feishu:cli_planner" || bindings.Bindings[0].TargetName != "planner-target" {
		t.Fatalf("planner bindings = %+v", bindings.Bindings)
	}
	bots, err := srv.FeishuBots(context.Background(), &pb.FeishuBotsRequest{})
	if err != nil {
		t.Fatalf("FeishuBots: %v", err)
	}
	if len(bots.Bots) != 2 || bots.Bots[0].Profile != "coder" || bots.Bots[1].Profile != "planner" {
		t.Fatalf("bots = %+v, want coder/planner", bots.Bots)
	}
}

func TestCreateScheduleRejectsEscalateToMainSubtask(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent:            "builtin/codex",
		Task:             "review",
		PermissionPolicy: task.PolicyEscalateToMain,
	})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), string(task.PolicyEscalateToMain)) {
		t.Fatalf("CreateSchedule error = %v, want permission policy rejection", err)
	}
}

func TestCreateScheduleNormalizesDefaultPermissionPolicy(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent:            "builtin/codex",
		Task:             "review",
		PermissionPolicy: task.PermissionPolicy("default"),
	})

	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:         "scheduled review",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	storedJSON := got.Schedule.TaskSpecJson
	if strings.Contains(string(storedJSON), "default") {
		t.Fatalf("stored task spec json = %s, want normalized permission policy without literal default", storedJSON)
	}
	var stored task.Spec
	if err := json.Unmarshal(storedJSON, &stored); err != nil {
		t.Fatalf("decode stored task spec: %v", err)
	}
	if stored.PermissionPolicy != "" {
		t.Fatalf("stored permission_policy = %q, want omitted/default", stored.PermissionPolicy)
	}
}

func TestCreateScheduleFreezesEnrolledRunAsIdentity(t *testing.T) {
	store := newScheduleTestStore(t)
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = principals.Close() })
	actor, err := principals.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: "schedule owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := principals.Create(principal.Principal{
		Kind: principal.KindService, DisplayName: "release team",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := principals.BindAlias(principal.Alias{
		Provider: principal.ProviderCLI, Scope: "local", Subject: "release", PrincipalID: service.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := principals.GrantServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "schedule-owner", actor.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-amux-actor-token", enrollment.Token,
		"x-amux-request-id", "create-schedule-1",
	))
	srv := &hubServer{schedule: store, principals: principals}
	created, err := srv.CreateSchedule(ctx, &pb.CreateScheduleRequest{
		Name:         "release review",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{
			Agent: "builtin/codex", Task: "review", RunAs: "release",
		}),
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Read back as the creator: a schedule is only reachable by the principal
	// that created it.
	got, err := srv.GetSchedule(ctx, &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	var stored task.Spec
	if err := json.Unmarshal(got.Schedule.TaskSpecJson, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RunAs != "" {
		t.Fatalf("stored run_as = %q, want cleared after resolution", stored.RunAs)
	}
	if stored.ExecutionIdentity == nil {
		t.Fatal("stored execution identity is nil")
	}
	identity := *stored.ExecutionIdentity
	if identity.IdentityMode != principal.IdentityModeIdentified ||
		identity.OriginActorPrincipalID != actor.ID ||
		identity.EffectivePrincipalID != service.ID ||
		identity.RequestedRunAs != "release" ||
		identity.Source != principal.SourceSchedule {
		t.Fatalf("stored execution identity = %+v", identity)
	}

	otherService, err := principals.Create(principal.Principal{
		Kind: principal.KindService, DisplayName: "other release team",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := principals.BindAlias(principal.Alias{
		Provider: principal.ProviderCLI, Scope: "local", Subject: "release", PrincipalID: otherService.ID,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = srv.GetSchedule(ctx, &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Schedule.TaskSpecJson, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionIdentity == nil || stored.ExecutionIdentity.EffectivePrincipalID != service.ID {
		t.Fatalf("alias rebinding changed frozen schedule identity: %+v", stored.ExecutionIdentity)
	}
}

func TestCreateScheduleRunAsRequiresEnrollment(t *testing.T) {
	store := newScheduleTestStore(t)
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = principals.Close() })
	srv := &hubServer{schedule: store, principals: principals}
	_, err = srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{
			Agent: "builtin/codex", Task: "review", RunAs: "release",
		}),
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.Unauthenticated, err)
	}
}

func TestUpdateScheduleSubtask(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent: "builtin/codex",
		Task:  "review",
	})
	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:         "scheduled review",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	newName := "renamed review"
	newSpec := "daily 09:00"
	newTask := mustTaskSpecJson(t, task.Spec{Agent: "builtin/codex", Task: "audit"})
	resp, err := srv.UpdateSchedule(context.Background(), &pb.UpdateScheduleRequest{
		Id:           created.Id,
		Name:         &newName,
		Spec:         &newSpec,
		TaskSpecJson: newTask,
	})
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("UpdateSchedule error: %s", resp.Error)
	}
	if resp.Schedule.Name != newName || resp.Schedule.Spec != "daily 09:00" || resp.Schedule.Kind != schedule.KindDaily {
		t.Fatalf("UpdateSchedule result mismatch: %+v", resp.Schedule)
	}
	var stored task.Spec
	if err := json.Unmarshal(resp.Schedule.TaskSpecJson, &stored); err != nil {
		t.Fatalf("decode stored task spec: %v", err)
	}
	if stored.Task != "audit" {
		t.Fatalf("stored task = %q, want audit", stored.Task)
	}
}

func TestUpdateScheduleRejectsPromptForSubtask(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{Agent: "builtin/codex", Task: "review"}),
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	prompt := "hello"
	_, err = srv.UpdateSchedule(context.Background(), &pb.UpdateScheduleRequest{Id: created.Id, Prompt: &prompt})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
}

func TestUpdateScheduleNotFound(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	name := "x"
	_, err := srv.UpdateSchedule(context.Background(), &pb.UpdateScheduleRequest{Id: 999999, Name: &name})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("UpdateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.NotFound, err)
	}
}

func TestCreateScheduleRejectsUnknownPermissionPolicy(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent:            "builtin/codex",
		Task:             "review",
		PermissionPolicy: task.PermissionPolicy("weird"),
	})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid permission_policy") || !strings.Contains(err.Error(), "weird") {
		t.Fatalf("CreateSchedule error = %v, want invalid permission_policy rejection", err)
	}
}

func TestCreateScheduleRejectsGitWorktreeWithoutRepoCache(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent: "builtin/codex",
		Task:  "review",
		Workspace: task.WorkspaceSpec{
			Policy: task.WorkspaceGitWorktree,
		},
	})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "workspace.local_dir or workspace.repo.cache_key is required") {
		t.Fatalf("CreateSchedule error = %v, want workspace repo cache rejection", err)
	}
}

func TestCreateScheduleAcceptsGitWorktreeWithRepoCacheKey(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent: "builtin/codex",
		Task:  "review",
		Workspace: task.WorkspaceSpec{
			Policy: task.WorkspaceGitWorktree,
			Repo:   &task.RepoSpec{CacheKey: "org/repo", Commit: "abc123"},
		},
	})

	resp, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: resp.Id})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	var stored task.Spec
	if err := json.Unmarshal(got.Schedule.TaskSpecJson, &stored); err != nil {
		t.Fatalf("decode stored spec: %v", err)
	}
	if stored.Workspace.Repo == nil || stored.Workspace.Repo.CacheKey != "org/repo" || stored.Workspace.Repo.Commit != "abc123" {
		t.Fatalf("stored workspace = %#v, want repo cache key", stored.Workspace)
	}
}

func TestCreateScheduleRejectsEffectiveEscalateToMainFromProfileDefault(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	oldResolver := resolveScheduledTaskSpec
	resolveScheduledTaskSpec = func(spec *task.Spec, _ string) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "codex"
			spec.PermissionPolicy = task.PolicyEscalateToMain
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "codex", PermissionPolicy: string(task.PolicyEscalateToMain)}
		}
		return nil
	}
	t.Cleanup(func() { resolveScheduledTaskSpec = oldResolver })
	specJSON := mustTaskSpecJson(t, task.Spec{Agent: "team/reviewer", Task: "review"})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "effective permission_policy") || !strings.Contains(err.Error(), string(task.PolicyEscalateToMain)) {
		t.Fatalf("CreateSchedule error = %v, want effective permission policy rejection", err)
	}
}

func TestCreateScheduleNormalizesEffectiveDefaultPermissionPolicy(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	oldResolver := resolveScheduledTaskSpec
	resolveScheduledTaskSpec = func(spec *task.Spec, _ string) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "codex"
			spec.PermissionPolicy = task.PermissionPolicy("default")
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "codex", PermissionPolicy: "default"}
		}
		return nil
	}
	t.Cleanup(func() { resolveScheduledTaskSpec = oldResolver })
	specJSON := mustTaskSpecJson(t, task.Spec{Agent: "team/reviewer", Task: "review"})

	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:         "scheduled review",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if strings.Contains(string(got.Schedule.TaskSpecJson), "default") {
		t.Fatalf("stored task spec json = %s, want no literal default", got.Schedule.TaskSpecJson)
	}
}

func TestCreateScheduleRejectsEffectiveUnknownPermissionPolicy(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	oldResolver := resolveScheduledTaskSpec
	resolveScheduledTaskSpec = func(spec *task.Spec, _ string) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "codex"
			spec.PermissionPolicy = task.PermissionPolicy("bogus")
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "codex", PermissionPolicy: "bogus"}
		}
		return nil
	}
	t.Cleanup(func() { resolveScheduledTaskSpec = oldResolver })
	specJSON := mustTaskSpecJson(t, task.Spec{Agent: "team/reviewer", Task: "review"})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid permission_policy") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("CreateSchedule error = %v, want invalid effective permission_policy rejection", err)
	}
}

func TestCreateScheduleRejectsEffectiveGitWorktreeWithoutLocalDir(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	oldResolver := resolveScheduledTaskSpec
	resolveScheduledTaskSpec = func(spec *task.Spec, _ string) error {
		if spec.Agent == "team/reviewer" {
			spec.Agent = "codex"
			spec.Workspace.Policy = task.WorkspaceGitWorktree
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "reviewer", Type: "codex"}
		}
		return nil
	}
	t.Cleanup(func() { resolveScheduledTaskSpec = oldResolver })
	specJSON := mustTaskSpecJson(t, task.Spec{Agent: "team/reviewer", Task: "review"})

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSchedule error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "effective workspace.local_dir or workspace.repo.cache_key is required") {
		t.Fatalf("CreateSchedule error = %v, want effective workspace.local_dir rejection", err)
	}
}

func TestCreateAndGetSubtaskSchedule(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}
	specJSON := mustTaskSpecJson(t, task.Spec{
		Agent:            "builtin/codex",
		Task:             "review",
		PermissionPolicy: task.PolicyDenyAll,
	})

	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:         "scheduled review",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 30m",
		TaskSpecJson: specJSON,
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.Id == 0 {
		t.Fatalf("CreateSchedule id = 0")
	}
	if created.NextRunAt == 0 {
		t.Fatalf("CreateSchedule next_run_at = 0")
	}

	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: created.Id, IncludeRuns: false})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	item := got.Schedule
	if item == nil {
		t.Fatalf("GetSchedule returned nil schedule")
	}
	if item.ScheduleType != schedule.ScheduleTypeSubtask {
		t.Fatalf("schedule_type = %q, want %q", item.ScheduleType, schedule.ScheduleTypeSubtask)
	}
	if string(item.TaskSpecJson) != string(specJSON) {
		t.Fatalf("task spec json = %s, want %s", item.TaskSpecJson, specJSON)
	}
	if item.TargetId != "" || item.SessionName != "" || item.Prompt != "" {
		t.Fatalf("subtask schedule should not require session fields: %+v", item)
	}
}

func TestCreateSessionPromptScheduleStillRequiresSessionAndPrompt(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}

	_, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{Spec: "every 30m"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing target/session error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "target and session are required") {
		t.Fatalf("missing target/session error = %v", err)
	}

	_, err = srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		TargetId:    "target-1",
		SessionName: "session-1",
		Spec:        "every 30m",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing prompt error code = %v, want %v (err=%v)", status.Code(err), codes.InvalidArgument, err)
	}
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("missing prompt error = %v", err)
	}
}

func newScheduleTestStore(t *testing.T) *schedule.Store {
	t.Helper()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustTaskSpecJson(t *testing.T, spec task.Spec) []byte {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal task spec: %v", err)
	}
	return b
}
