package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// A set-model caller (CLI/MCP) does not resolve the profile registry; the
// replacement carries only the agent type and the new model args. amuxlet
// re-projects the switch through the profile registry, which for a native
// target still matches the frozen profile identity so the hot-reconfigure
// path applies.
func TestReconfigureExecutionBackfillsProfileFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("set-model", "codex", dir)
	if err != nil {
		t.Fatal(err)
	}
	oldReq := &pb.RunRequest{
		AgentExecutable: "/custom/bin/codex-team",
		AgentProfile:    "builtin/codex", AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.5"},
		ProfileSpec: &pb.AgentProfileSpec{Ref: "builtin/codex", Type: "codex", Model: "gpt-5.5", ReasoningEffort: "medium"},
	}
	if err := mgr.RecordLaunchParams(sess, oldReq.ExtraArgs, nil, "cli", "local", oldReq.AgentProfile, "gpt-5.5", launchSpecSnapshot(oldReq)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "native-thread"); err != nil {
		t.Fatal(err)
	}
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}

	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionContextHook: func(_ context.Context, _ adapter.Adapter, _ *session.Session, _, _ []string) error {
			t.Fatal("relaunch should not run: hot reconfigure expected")
			return nil
		},
	}
	replacement := &pb.RunRequest{AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.4"}}
	resp, err := srv.reconfigureExecutionAndRelaunch(context.Background(), sess, replacement, true)
	if err != nil {
		t.Fatalf("reconfigure execution: %v", err)
	}
	if resp == nil || resp.Session == nil {
		t.Fatalf("response = %+v", resp)
	}
	if replacement.AgentProfile != "builtin/codex" {
		t.Fatalf("AgentProfile backfill = %q, want %q", replacement.AgentProfile, "builtin/codex")
	}
	if replacement.ProfileSpec == nil || replacement.ProfileSpec.Ref != "builtin/codex" {
		t.Fatalf("ProfileSpec backfill = %+v", replacement.ProfileSpec)
	}
	if proc.closed {
		t.Fatal("process was restarted; hot reconfigure expected after backfill")
	}
	got := sess.Snapshot()
	if got.AgentSessionID != "native-thread" || got.EffectiveModel != "gpt-5.4" || launchSpecExecutable(got.LaunchSpec) != "/custom/bin/codex-team" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestSetModelPreservesExplicitRuntimeAndFrozenProfile(t *testing.T) {
	for _, tc := range []struct {
		name          string
		packState     string
		dynamicEffort bool
		provider      bool
	}{
		{name: "content-only profile"},
		{name: "updated pack", packState: "updated"},
		{name: "unavailable pack", packState: "unavailable"},
		{name: "provider to native restart", provider: true},
		{name: "dynamic native effort", dynamicEffort: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			packRoot := filepath.Join(dir, "pack")
			writeRefreshProfilePack(t, packRoot)
			registryPath := filepath.Join(dir, "agent-packs", "registry.json")
			registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
				"team": {Namespace: "team", SourceKind: agentprofile.PackSourceGit, MaterializedPath: packRoot, CurrentRevision: "oldrev"},
			}}
			if err := agentprofile.SavePackRegistry(registryPath, registry); err != nil {
				t.Fatal(err)
			}
			profile, err := agentprofile.ResolveProfile(registryPath, "team/coder")
			if err != nil {
				t.Fatal(err)
			}
			opts := agentprofile.LaunchOptions{Runtime: "codex", Model: "gpt-5.4", ReasoningEffort: "high"}
			launch, err := agentprofile.ProjectProfileLaunchChecked(profile, nil, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			if tc.dynamicEffort {
				launch.ProfileSpec.ReasoningEffort = "future-effort"
			}
			oldReq := &pb.RunRequest{AgentProfile: launch.AgentProfile, AgentType: launch.AgentType, ProfileSpec: launch.ProfileSpec, ExtraArgs: launch.ExtraArgs}
			if tc.provider {
				oldReq.ProfileSpec.ProviderRef = "previous"
				oldReq.ProfileSpec.Provider = &pb.AgentProfileProvider{Ref: "previous", Platform: "openai-compat", WireApi: "responses", BaseUrl: "https://old.example/v1", ApiKeyEnv: "MODEL_API_KEY", UpstreamModel: "old-upstream"}
				oldReq.ExtraArgs = []string{"--model", "old-upstream", "-c", `model_provider="amux"`, "-c", `model_providers.amux.base_url="https://old.example/v1"`}
			}
			if err := materializeProfileWorkspace(oldReq, dir, "", dir); err != nil {
				t.Fatal(err)
			}
			instructionsBefore, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(dir, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			sess, err := mgr.Create("switch", "codex", dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordLaunchParams(sess, oldReq.ExtraArgs, nil, "cli", "local", oldReq.AgentProfile, launch.Model, launchSpecSnapshot(oldReq)); err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordAgentSessionID(sess, "native-thread"); err != nil {
				t.Fatal(err)
			}
			proc := &serverTestProc{}
			sess.SetProc(proc)
			if err := mgr.SetRunning(sess); err != nil {
				t.Fatal(err)
			}
			switch tc.packState {
			case "updated":
				writeRefreshProfilePack(t, packRoot)
				if err := os.WriteFile(filepath.Join(packRoot, "instructions", "coder", "AGENTS.md"), []byte("# Changed expert\n"), 0600); err != nil {
					t.Fatal(err)
				}
				pack := registry.Packs["team"]
				pack.CurrentRevision = "newrev"
				registry.Packs["team"] = pack
				if err := agentprofile.SavePackRegistry(registryPath, registry); err != nil {
					t.Fatal(err)
				}
			case "unavailable":
				if err := os.Remove(registryPath); err != nil {
					t.Fatal(err)
				}
			}
			srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true,
				startSessionContextHook: func(context.Context, adapter.Adapter, *session.Session, []string, []string) error {
					if !tc.provider {
						t.Fatal("native model switch should not restart the process")
					}
					if err := mgr.RecordAgentSessionID(sess, "new-native-thread"); err != nil {
						return err
					}
					sess.SetProc(&serverTestProc{})
					return mgr.SetRunning(sess)
				},
			}
			before := sess.Snapshot()
			replacement, err := clientop.BuildSetModelReplacement(toProtoSession(sess), "gpt-5.5")
			if err != nil {
				t.Fatal(err)
			}
			_, err = srv.reconfigureExecutionAndRelaunch(context.Background(), sess, replacement, true)
			if err != nil {
				t.Fatal(err)
			}
			got := sess.Snapshot()
			wantEffort := "high"
			if tc.dynamicEffort {
				wantEffort = "future-effort"
			}
			wantCalls, wantThread := []string{"gpt-5.5:" + wantEffort}, "native-thread"
			if tc.provider {
				wantCalls, wantThread = nil, "new-native-thread"
			}
			if proc.closed != tc.provider || !reflect.DeepEqual(proc.reconfigurations, wantCalls) || got.AgentSessionID != wantThread || got.ID != before.ID || got.Cwd != before.Cwd {
				t.Fatalf("model switch did not retain the running conversation: calls=%v snapshot=%+v", proc.reconfigurations, got)
			}
			var saved struct {
				ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
			}
			if !decodeLaunchSpec(got.LaunchSpec, &saved) || saved.ProfileSpec == nil {
				t.Fatal("missing profile snapshot")
			}
			spec := saved.ProfileSpec
			if got.EffectiveModel != "gpt-5.5" || spec.RuntimeRef != "codex" || spec.ReasoningEffort != wantEffort || spec.PinnedRef != launch.ProfileSpec.PinnedRef || spec.Agents != launch.ProfileSpec.Agents {
				t.Fatalf("execution or frozen profile changed unexpectedly: %+v", spec)
			}
			instructionsAfter, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if string(instructionsAfter) != string(instructionsBefore) {
				t.Fatal("model switch changed expert instructions")
			}
			// A subsequent switch must still use the saved runtime, not fall back
			// to the original profile's restricted model list.
			next, err := clientop.BuildSetModelReplacement(toProtoSession(sess), "gpt-5.5")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := srv.reprojectSetModelReplacement(got, next); err != nil {
				t.Fatalf("second switch: %v", err)
			}
		})
	}
}
