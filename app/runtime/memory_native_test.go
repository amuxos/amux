package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestManagedClaudeKeepsNativeMemoryDisabledAfterExperimentTurnsOff(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	binding, lookups := "global", 0
	s.memoryBindingResolver = func(context.Context, string) (string, error) {
		lookups++
		return binding, nil
	}
	res, err := s.Run(context.Background(), &pb.RunRequest{
		Name: "claude-native-memory", AgentType: "claude",
		Env: []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY=0", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := s.mgr.Get(res.Session.Id)
	binding = "" // The Hub now reports the experiment as disabled to fresh sessions.
	if _, err := s.relaunchSession(sess, "restarting"); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 {
		t.Fatalf("bound resume queried current policy %d times", lookups)
	}
	for launch, env := range *envs {
		count := 0
		for _, entry := range env {
			if strings.HasPrefix(entry, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=") {
				count++
				if entry != "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1" {
					t.Fatalf("launch %d retained native auto-memory: %s", launch, entry)
				}
			}
		}
		if count != 1 {
			t.Fatalf("launch %d has %d native auto-memory controls", launch, count)
		}
	}
}

func TestUnsupportedMemoryProviderOnlyWarnsWhenExperimentEnabled(t *testing.T) {
	for _, binding := range []string{"", "global"} {
		s, _, _ := memoryTestServer(t)
		s.memoryBindingResolver = func(context.Context, string) (string, error) { return binding, nil }
		got, err := s.resolveMemoryBinding(context.Background(), "", principal.ExecutionIdentity{}, "extension-provider")
		if got != "" {
			t.Fatalf("unsupported provider acquired a memory binding: %q", got)
		}
		if (err != nil) != (binding != "") {
			t.Fatalf("binding=%q error=%v", binding, err)
		}
	}
}

func TestUnmanagedClaudePreservesNativeMemoryConfiguration(t *testing.T) {
	s, _, envs := memoryTestServer(t)
	s.memoryBindingResolver = func(context.Context, string) (string, error) { return "", nil }
	_, err := s.Run(context.Background(), &pb.RunRequest{
		Name: "claude-native-only", AgentType: "claude",
		Env: []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY=0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := launchEnvValue((*envs)[0], "CLAUDE_CODE_DISABLE_AUTO_MEMORY"); got != "0" {
		t.Fatalf("unmanaged Claude native setting = %q", got)
	}
}
