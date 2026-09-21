package agentprofile

import (
	"fmt"
	"strings"
	"testing"
)

func TestProviderExtensionsCannotReplaceSharedValidation(t *testing.T) {
	previous := customization
	t.Cleanup(func() { customization = previous })
	calls := 0
	customization.ValidateAdditionalProvider = func(string, *Provider) error { calls++; return nil }
	valid := Provider{Ref: "service/example", Platform: "openai-compat", UpstreamModel: "example", APIKeyEnv: "EXAMPLE_KEY", BaseURL: "https://example.invalid/v1"}
	for _, tc := range []struct {
		name   string
		change func(*Provider)
		want   string
	}{
		{"model", func(p *Provider) { p.UpstreamModel = "  " }, "upstream_model"},
		{"credential", func(p *Provider) { p.APIKeyEnv = "bad-name" }, "api_key_env"},
		{"endpoint", func(p *Provider) { p.BaseURL = "  " }, "base_url"},
		{"unknown protocol", func(p *Provider) { p.WireAPI = "invalid" }, "wire_api"},
		{"compatibility agent", func(p *Provider) { p.WireAPI = "responses-via-chat-completions" }, "only supported for codex"},
		{"protocol platform", func(p *Provider) { p.Platform = "additional"; p.WireAPI = "responses" }, "only supported with openai-compat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := valid
			tc.change(&provider)
			profile := Profile{Namespace: "test", Name: "example", Type: "claude", Provider: &provider}
			if _, err := RenderProfileWorkspace(profile, t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation = %v, want %s", err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid providers reached extension %d times", calls)
	}
	if _, err := RenderProfileWorkspace(Profile{Namespace: "test", Name: "example", Type: "claude", Provider: &valid}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("additional combination was not checked: calls=%d", calls)
	}
	customization.ValidateAdditionalProvider = func(string, *Provider) error { return fmt.Errorf("extension must not own native rules") }
	for _, agent := range []string{"codex", "pi"} {
		if _, err := RenderProfileWorkspace(Profile{Namespace: "test", Name: "example", Type: agent, Model: "service/example", Provider: &valid}, t.TempDir()); err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
	}
}

func TestNativeProviderWireRulesSurviveCustomization(t *testing.T) {
	previous := customization
	t.Cleanup(func() { customization = previous })
	customization.ValidateAdditionalProvider = func(string, *Provider) error { t.Fatal("native validation delegated to extension"); return nil }
	for _, tc := range []struct{ agent, wire string }{{"codex", "chat-completions"}, {"pi", "responses"}, {"pi", "responses-via-chat-completions"}} {
		profile := Profile{Namespace: "test", Name: "example", Type: tc.agent, Provider: &Provider{Platform: "openai-compat", BaseURL: "https://example.invalid/v1", APIKeyEnv: "KEY", UpstreamModel: "example", WireAPI: tc.wire}}
		if _, err := RenderProfileWorkspace(profile, t.TempDir()); err == nil {
			t.Fatalf("accepted %s with %s", tc.agent, tc.wire)
		}
	}
}
