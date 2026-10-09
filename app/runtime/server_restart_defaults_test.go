package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestRestartRecoversMissingLaunchIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, engine, profile, wantEngine, wantProfile string
		frozen, resume                                 bool
	}{
		{name: "missing both", wantEngine: "codex", wantProfile: "builtin/codex"},
		{name: "missing expert", engine: "claude", wantEngine: "claude", wantProfile: "builtin/codex"},
		{name: "missing engine", profile: "builtin/claude", wantEngine: "codex", wantProfile: "builtin/claude"},
		{name: "frozen engine and expert", frozen: true, wantEngine: "claude", wantProfile: "team/deleted"},
		{name: "automatic wake", resume: true, wantEngine: "codex", wantProfile: "builtin/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			sess, err := mgr.Create("recover", tc.engine, dir)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--model", "saved-model", "--effort", "high"}
			launchSpec := ""
			if tc.frozen {
				launchSpec = launchSpecSnapshot(&pb.RunRequest{AgentType: "claude", AgentProfile: "team/deleted", ProfileSpec: &pb.AgentProfileSpec{Ref: "team/deleted", Type: "claude", RuntimeRef: "claude", ConfiguredModel: "saved-model", ReasoningEffort: "high", PermissionPolicy: "deny-all"}})
			}
			if err := mgr.RecordLaunchParams(sess, args, []string{"TEST_VALUE=retained"}, "cli", "local", tc.profile, "saved-model", launchSpec); err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordAgentSessionID(sess, "old-native-thread"); err != nil {
				t.Fatal(err)
			}
			if err := db.AppendMessage(sess.ID, "user", []byte("retain the previous conversation")); err != nil {
				t.Fatal(err)
			}
			if tc.resume {
				if err := mgr.SetSleeping(sess); err != nil {
					t.Fatal(err)
				}
			}
			launched := false
			srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true,
				startSessionContextHook: func(_ context.Context, _ adapter.Adapter, got *session.Session, args, env []string) error {
					launched = true
					snap := got.Snapshot()
					if snap.AgentType != tc.wantEngine || snap.AgentProfile != tc.wantProfile || snap.EffectiveModel != "saved-model" {
						t.Fatalf("recovered identity = %s/%s/%s", snap.AgentType, snap.AgentProfile, snap.EffectiveModel)
					}
					if !slices.Contains(args, "saved-model") || !slices.Contains(args, "high") || !slices.Contains(env, "TEST_VALUE=retained") || slices.Contains(args, "bypassPermissions") {
						t.Fatalf("changed launch settings: args=%q env=%q", args, env)
					}
					if tc.engine != "" || tc.frozen {
						if snap.AgentSessionID != "old-native-thread" {
							t.Fatal("lost known engine's provider history")
						}
					} else if snap.AgentSessionID != "" {
						t.Fatal("reused provider history with unknown original engine")
					}
					if tc.engine == "" && !tc.frozen && !strings.Contains(snap.HandoffContext, "retain the previous conversation") {
						t.Fatal("lost conversation handoff")
					}
					if tc.frozen && launchSpecPermissionPolicy(snap.LaunchSpec) != "deny-all" {
						t.Fatal("lost frozen permission policy")
					}
					return mgr.SetRunning(got)
				}}
			if tc.resume {
				err = srv.ensureSessionActive(sess)
			} else {
				_, err = srv.Restart(context.Background(), &pb.RestartRequest{Name: sess.Name})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !launched {
				t.Fatal("no launch")
			}
		})
	}
}

func TestRestartKeepsUnavailableEngineAndLiveProcess(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("unavailable", "removed-engine", dir)
	if err != nil {
		t.Fatal(err)
	}
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	srv := &server{mgr: mgr, amuxDir: dir}
	if _, err := srv.Restart(context.Background(), &pb.RestartRequest{Name: sess.Name}); err == nil {
		t.Fatal("silently replaced unavailable engine")
	}
	if proc.closed || sess.Snapshot().AgentType != "removed-engine" {
		t.Fatal("changed existing execution before validation")
	}
}

func TestRestartDefaultsUseRegisteredGenericContent(t *testing.T) {
	if os.Getenv("AMUX_TEST_RESTART_DEFAULTS") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRestartDefaultsUseRegisteredGenericContent$")
		cmd.Env = append(os.Environ(), "AMUX_TEST_RESTART_DEFAULTS=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("distribution recovery: %v\n%s", err, output)
		}
		return
	}
	pack := fstest.MapFS{
		"dist/agent-pack.manifest.json": {Data: []byte(`{"schema_version":"v2","pack_namespace":"builtin","profiles":{"default":{"instructions":["instructions/default.md"]},"coordinator":{"instructions":["instructions/coordinator.md"]}}}`)},
		"instructions/default.md":       {Data: []byte("General-purpose assistant.")},
		"instructions/coordinator.md":   {Data: []byte("Coordinate other experts.")},
	}
	if err := agentprofile.RegisterCustomization(agentprofile.Customization{Builtin: pack, DefaultProfile: "coordinator", Catalog: []byte(`{"schema_version":"v1","packs":[]}`),
		BuiltinLaunchPresets: map[string]agentprofile.BuiltinLaunchPreset{"coordinator": {Runtime: "claude", PermissionMode: "bypass", Model: "coordinator-model"}},
		RuntimeCatalog: []agentprofile.RuntimeCatalogSource{{Ref: "claude", DisplayName: "Claude", Models: []string{"saved-model"}, Capabilities: map[string]agentprofile.RuntimeModelCapability{
			"saved-model": {SupportedReasoningEfforts: []string{"medium", "high"}, DefaultReasoningEffort: "medium"},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	srv := &server{amuxDir: t.TempDir()}
	snap, req, err := srv.recoverRestartDefaults(session.Snapshot{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if snap.AgentType != "claude" || snap.AgentProfile != "builtin/default" || snap.EffectiveModel != "" || strings.TrimSpace(req.ProfileSpec.AgentsContent) != "General-purpose assistant." {
		t.Fatalf("recovery used coordinator content/settings: %+v, %+v", snap, req)
	}
	if slices.Contains(req.ExtraArgs, "bypassPermissions") || agentprofile.DefaultProfileRef != "builtin/coordinator" {
		t.Fatal("recovery borrowed permission bypass or changed global default")
	}
	args := []string{"--model", "saved-model", "--effort", "high", "--permission-mode", "plan"}
	snap, _, err = srv.recoverRestartDefaults(session.Snapshot{AgentType: "claude", ExtraArgs: args, EffectiveModel: "saved-model"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(snap.ExtraArgs, args) {
		t.Fatalf("catalog defaults replaced saved args: %q", snap.ExtraArgs)
	}
}

func TestRestartRecoversPartialProfileIdentity(t *testing.T) {
	for _, spec := range []string{
		`{"profile_spec":{"type":"codex","configured_model":"saved-model","reasoning_effort":"high","permission_policy":"deny-all"}}`,
		`{"agent_profile":"team/deleted","profile_spec":{"type":"codex","configured_model":"saved-model","reasoning_effort":"high","permission_policy":"deny-all"}}`,
	} {
		srv := &server{amuxDir: t.TempDir()}
		snap, _, err := srv.recoverRestartDefaults(session.Snapshot{AgentType: "codex", LaunchSpec: spec})
		if err != nil {
			t.Fatal(err)
		}
		want := "builtin/codex"
		if strings.Contains(spec, "team/deleted") {
			want = "team/deleted"
		}
		if snap.AgentProfile != want || snap.EffectiveModel != "saved-model" || launchSpecReasoningEffort(snap.LaunchSpec) != "high" || launchSpecPermissionPolicy(snap.LaunchSpec) != "deny-all" {
			t.Fatalf("lost partial snapshot settings: %+v", snap)
		}
	}
}

func TestRestartSnapshotRecoveryPreservesProviderAndUnknownFields(t *testing.T) {
	srv := &server{amuxDir: t.TempDir()}
	spec := `{"agent_type":"codex","future_field":{"keep":true},"profile_spec":{"ref":"team/removed","type":"codex","configured_model":"saved-model","permission_policy":"deny-all","future_profile_field":"retained","provider":{"ref":"gateway","platform":"openai-compat","base_url":"https://example.com/v1"}}}`
	snap, req, err := srv.recoverRestartDefaults(session.Snapshot{LaunchSpec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if req != nil || snap.AgentType != "codex" || snap.AgentProfile != "team/removed" || snap.EffectiveModel != "saved-model" {
		t.Fatalf("snapshot recovery = %+v, req=%+v", snap, req)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(spec), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(snap.LaunchSpec), &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"future_field", "profile_spec"} {
		if string(before[key]) != string(after[key]) {
			var a, b any
			_ = json.Unmarshal(before[key], &a)
			_ = json.Unmarshal(after[key], &b)
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			if string(x) != string(y) {
				t.Fatalf("changed frozen %s: %s", key, after[key])
			}
		}
	}
	for _, launchSpec := range []string{`{broken`, `{"profile_spec":{"ref":"team/expert","runtime_ref":"removed-runtime"}}`} {
		if _, _, err := srv.recoverRestartDefaults(session.Snapshot{LaunchSpec: launchSpec}); err == nil {
			t.Fatalf("silently repaired invalid or unavailable execution: %s", launchSpec)
		}
	}
}
