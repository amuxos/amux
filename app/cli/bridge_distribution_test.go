package cli

import "testing"

func TestPublicSetupUsesOfficialProvisioning(t *testing.T) {
	cmd := newFeishuSetupCmd()
	if cmd.Flags().Lookup("legacy-console-fallback") != nil {
		t.Fatal("unavailable legacy console flag is exposed")
	}
}
