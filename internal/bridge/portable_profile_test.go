package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/i18n"
)

func TestPortableBridgeUsesCallerRuntimeAndPermissions(t *testing.T) {
	t.Setenv("AMUX_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist/agent-pack.manifest.json"), []byte(`{"schema_version":"v2","pack_namespace":"example","profiles":{"reviewer":{"namespace":"common","description":"Review changes","envs":["REVIEW_TOKEN"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := agentprofile.SavePackRegistry(agentprofile.DefaultPackRegistryPath(), agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{"team": {Namespace: "team", MaterializedPath: root}}}); err != nil {
		t.Fatal(err)
	}
	launch, err := resolveBridgeAgentLaunchWithSelection("team/reviewer", []string{"--permission-mode", "plan"}, "claude", "review-model", "")
	if err != nil {
		t.Fatal(err)
	}
	if launch.AgentType != "claude" || launch.Model != "review-model" || !strings.Contains(strings.Join(launch.ExtraArgs, " "), "--permission-mode plan") {
		t.Fatalf("launch=%+v", launch)
	}
	if _, err := resolveBridgeAgentLaunchWithSelection("team/reviewer", nil, "", "", ""); err == nil {
		t.Fatal("bridge silently selected an engine")
	}
	state := managementState{Profiles: []managementProfileSummary{{Ref: "team/reviewer"}}}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		for _, label := range []any{managementEffectiveModelLabel(state, "team/reviewer", ""), managementProfileDefaultModelOptionLabel(state, "team/reviewer")} {
			if got := managementText(label).For(locale); got != managementCopy("management.choose.a.runtime").For(locale) {
				t.Fatalf("misleading default: %s", got)
			}
		}
	}
}
