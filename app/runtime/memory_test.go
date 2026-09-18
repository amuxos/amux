package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func memoryTestServer(t *testing.T) (*server, *[][]string, *[][]string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := session.NewManager(db)
	var args, envs [][]string
	s := &server{mgr: mgr, amuxDir: t.TempDir(), startSessionHook: func(_ adapter.Adapter, sess *session.Session, a, e []string) error {
		args = append(args, append([]string(nil), a...))
		envs = append(envs, append([]string(nil), e...))
		sess.SetProc(&serverTestProc{})
		return mgr.SetRunning(sess)
	}}
	if err := os.MkdirAll(filepath.Join(s.amuxDir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.amuxDir, "bin", "amux"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return s, &args, &envs
}

func TestMemoryFreshRawAndProfileRunsFreezeBinding(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "pi"} {
		for _, profile := range []string{"", "test-profile"} {
			t.Run(agent+"/"+profile, func(t *testing.T) {
				s, args, envs := memoryTestServer(t)
				calls := 0
				s.memoryBindingResolver = func(_ context.Context, actor string) (string, error) {
					calls++
					if actor != "launch-actor" {
						t.Errorf("actor = %q", actor)
					}
					return "global", nil
				}
				res, err := s.Run(context.Background(), &pb.RunRequest{Name: "memory", AgentType: agent, AgentProfile: profile, AgentActorToken: "launch-actor", Env: []string{memoryprotocol.BindingEnv + "=principal:forged"}, ExtraArgs: func() []string {
					if agent == "codex" {
						return nil
					}
					return []string{"--append-system-prompt", "user instructions"}
				}()})
				if err != nil {
					t.Fatal(err)
				}
				sess, _ := s.mgr.Get(res.Session.Id)
				if binding := memoryprotocol.Binding(sess.Snapshot().Env); binding != "global" {
					t.Fatalf("stored binding = %q", binding)
				}
				if binding := memoryprotocol.Binding((*envs)[0]); binding != "global" {
					t.Fatalf("launch binding = %q", binding)
				}
				if agent == "codex" && !strings.Contains(strings.Join((*args)[0], " "), `AMUX_MEMORY_BINDING="global"`) {
					t.Fatalf("Codex MCP binding missing: %v", (*args)[0])
				}
				if agent != "codex" {
					joined := strings.Join((*args)[0], " ")
					if !strings.Contains(joined, "user instructions") || !strings.Contains(joined, "Use amux for cross-session memory") || strings.Count(joined, "--append-system-prompt") != 1 {
						t.Fatalf("prompt not merged: %s", joined)
					}
				}
				if _, err := s.relaunchSession(sess, "restarting"); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || memoryprotocol.Binding((*envs)[1]) != "global" {
					t.Fatalf("relaunch re-resolved/lost binding: calls=%d env=%v", calls, (*envs)[1])
				}
			})
		}
	}
}

func TestMemoryUnavailableDoesNotBlockOrdinaryLaunch(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	s.memoryBindingResolver = func(context.Context, string) (string, error) { return "", errors.New("Hub unavailable") }
	res, err := s.Run(context.Background(), &pb.RunRequest{Name: "offline", AgentType: "codex", Env: []string{memoryprotocol.BindingEnv + "=global"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Hub unavailable") || memoryprotocol.Binding((*envs)[0]) != "" {
		t.Fatalf("unsafe/unreported fallback: %+v %v", res, *envs)
	}
}

func TestMemoryRejectsPrincipalMismatch(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	s.memoryBindingResolver = func(context.Context, string) (string, error) { return "principal:another", nil }
	_, err := s.Run(context.Background(), &pb.RunRequest{Name: "mismatch", AgentType: "codex"})
	if err == nil || !strings.Contains(err.Error(), "effective execution principal") || len(*envs) != 0 {
		t.Fatalf("mismatched launch: %v %v", err, *envs)
	}
}

func TestMemoryTaskInstanceRetainsBindingWithoutResolutionOnReuse(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	calls := 0
	s.memoryBindingResolver = func(context.Context, string) (string, error) { calls++; return "global", nil }
	controller := newTaskController(s, "local")
	spec := task.ChildSpec{TaskID: "first", InstanceID: "persistent", AgentType: "claude", Env: []string{memoryprotocol.BindingEnv + "=forged"}, RecordEnv: []string{memoryprotocol.BindingEnv + "=forged"}}
	child, err := controller.CreateChild(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.SetSleeping("finished"); err != nil {
		t.Fatal(err)
	}
	spec.TaskID = "second"
	resumed, err := controller.CreateChild(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if child.ID() != resumed.ID() || calls != 1 || len(*envs) != 2 || memoryprotocol.Binding((*envs)[1]) != "global" {
		t.Fatalf("instance scope lost: calls=%d env=%v", calls, *envs)
	}
	spec.ExecutionIdentity = principal.ExecutionIdentity{EffectivePrincipalID: "another"}
	if _, err := controller.CreateChild(context.Background(), spec); err == nil {
		t.Fatal("instance reused another execution principal")
	}
}

func TestMemoryLegacySessionDoesNotInheritBinding(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	s.memoryBindingResolver = func(context.Context, string) (string, error) {
		t.Fatal("must not resolve old conversation")
		return "global", nil
	}
	sess, err := s.mgr.Create("legacy", "codex")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := adapter.Get("codex")
	if err := s.startSession(a, sess, nil, []string{memoryprotocol.BindingEnv + "=global"}); err != nil {
		t.Fatal(err)
	}
	if memoryprotocol.Binding((*envs)[0]) != "" {
		t.Fatalf("legacy conversation adopted memory: %v", *envs)
	}
}
