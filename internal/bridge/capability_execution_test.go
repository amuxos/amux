package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
)

func TestCapabilityExecutionHonorsProfileModelPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	packRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(packRoot, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":"v1.1","pack_namespace":"policy","profiles":{
"unrestricted":{"type":"codex"},
"restricted":{"type":"codex","supported_models":["allowed-model"]},
"default":{"type":"codex","supported_models":[]}
}}`
	if err := os.WriteFile(filepath.Join(packRoot, "dist", "agent-pack.manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"policy": {Namespace: "policy", MaterializedPath: packRoot},
	}}
	if err := agentprofile.SavePackRegistry(agentprofile.DefaultPackRegistryPath(), registry); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		profile string
		model   string
		valid   bool
	}{
		{"unrestricted", "custom-native-model", true},
		{"unrestricted", "native/model", true},
		{"restricted", "allowed-model", true},
		{"restricted", "unlisted-model", false},
		{"default", "", true},
		{"default", "unlisted-model", false},
	} {
		t.Run(test.profile+"/"+test.model, func(t *testing.T) {
			for _, validate := range []func(string, string, string, string) error{
				validateGroupWatchExecution, validateDocumentCommentExecution,
			} {
				err := validate("policy/"+test.profile, "", test.model, "")
				if (err == nil) != test.valid {
					t.Fatalf("profile=%s model=%q valid=%v error=%v", test.profile, test.model, test.valid, err)
				}
			}
		})
	}
}
