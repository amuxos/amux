package agentprofile

import (
	"reflect"
	"strings"
	"testing"
)

func TestProfileDefaultRuntimeProjectsNativeReasoningEffort(t *testing.T) {
	for _, agentType := range []string{"codex", "claude"} {
		for _, selection := range []struct {
			name         string
			profileModel string
			model        string
			args         []string
			want         string
		}{
			{name: "agent default"},
			{name: "profile model", profileModel: "profile-model", want: "profile-model"},
			{name: "selected model", profileModel: "profile-model", model: "selected-model", want: "selected-model"},
			{name: "explicit model arg", profileModel: "profile-model", model: "selected-model", args: []string{"--model", "arg-model"}, want: "arg-model"},
		} {
			t.Run(agentType+"/"+selection.name, func(t *testing.T) {
				profile := Profile{
					Namespace: "team", Name: "coder", Revision: "abc123", Type: agentType,
					Model: selection.profileModel, DefaultArgs: []string{"--verbose"}, DefaultEnv: []string{"TEAM=amux"},
				}
				args := append([]string{"--extra"}, selection.args...)
				baseline, err := ProjectProfileLaunchChecked(profile, args, []string{"EXTRA=1"}, LaunchOptions{Model: selection.model})
				if err != nil {
					t.Fatal(err)
				}
				launch, err := ProjectProfileLaunchChecked(profile, args, []string{"EXTRA=1"}, LaunchOptions{Model: selection.model, ReasoningEffort: " high "})
				if err != nil {
					t.Fatal(err)
				}
				if launch.AgentType != agentType || launch.AgentProfile != profile.PinnedRef() || launch.Model != selection.want || launch.ProfileSpec.Model != selection.want {
					t.Fatalf("reasoning changed profile/model selection: %+v", launch)
				}
				if launch.Runtime != "" || launch.ProfileSpec.RuntimeRef != "" || launch.ReasoningEffort != "high" || launch.ProfileSpec.ReasoningEffort != "high" {
					t.Fatalf("default runtime reasoning snapshot = %+v", launch)
				}
				wantArgs := append([]string(nil), baseline.ExtraArgs...)
				if agentType == "codex" {
					wantArgs = append(wantArgs, "-c", `model_reasoning_effort="high"`)
				} else {
					wantArgs = append(wantArgs, "--effort", "high")
				}
				if !reflect.DeepEqual(launch.ExtraArgs, wantArgs) || !reflect.DeepEqual(launch.Env, baseline.Env) || !reflect.DeepEqual(launch.ProfileSpec.DefaultArgs, profile.DefaultArgs) {
					t.Fatalf("launch lost profile/explicit defaults: args=%q env=%q spec=%+v", launch.ExtraArgs, launch.Env, launch.ProfileSpec)
				}
				roundTrip, err := FromPBProfileSpec(launch.ProfileSpec)
				if err != nil || roundTrip.ReasoningEffort != "high" || roundTrip.ExecutionRuntimeRef != "" {
					t.Fatalf("round trip = %+v, err=%v", roundTrip, err)
				}
			})
		}
	}
}

func TestProfileDefaultRuntimeValidatesReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		agentType string
		model     string
		effort    string
	}{
		{agentType: "codex", effort: "invalid"},
		{agentType: "codex", model: "custom-model", effort: "invalid"},
		{agentType: "claude", model: "opus", effort: "invalid"},
		{agentType: "pi", effort: "high"},
		{agentType: "pi", model: "custom-model", effort: "high"},
	} {
		t.Run(tc.agentType+"/"+tc.model, func(t *testing.T) {
			profile := Profile{Namespace: "team", Name: "coder", Type: tc.agentType, Model: tc.model}
			for _, runtime := range []string{"", tc.agentType} {
				_, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: runtime, Model: tc.model, ReasoningEffort: tc.effort})
				if err == nil || !strings.Contains(err.Error(), "reasoning effort") || !strings.Contains(err.Error(), "not supported") {
					t.Fatalf("runtime %q error = %v, want unsupported effort", runtime, err)
				}
			}
		})
	}
}

func TestProfileDefaultRuntimeReasoningPreservesProviderAndModelPolicy(t *testing.T) {
	provider := &Provider{
		Ref: "service", Platform: "openai-compat", WireAPI: "responses",
		BaseURL: "https://models.example/v1", APIKeyEnv: "PROVIDER_API_KEY", UpstreamModel: "upstream-model",
		Thinking: map[string]string{"reasoning_effort": "low", "thinking_effort": "low", "type": "enabled", "verbosity": "low"},
	}
	profile := Profile{
		Namespace: "team", Name: "coder", Type: "codex", Model: "service/model", Provider: provider,
		SupportedModels: []string{"service/model", "native-model"}, ModelPolicyEnforced: true,
		DefaultArgs: []string{"-c", `approval_policy="on-request"`},
		ModelOptions: map[string]ModelOption{
			"service/model": {Ref: "service/model", Kind: ModelKindProvider, AgentModel: "upstream-model", ProviderRef: "service", Provider: provider},
		},
	}
	for _, requested := range []string{"", "service/model"} {
		launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: requested, ReasoningEffort: "high"})
		if err != nil {
			t.Fatal(err)
		}
		spec := launch.ProfileSpec
		if launch.Model != "service/model" || spec.ModelRef != "service/model" || spec.ModelKind != string(ModelKindProvider) || spec.ProviderRef != "service" || spec.Provider == nil {
			t.Fatalf("lost profile provider selection: %+v", launch)
		}
		if spec.Provider.BaseUrl != provider.BaseURL || spec.Provider.ApiKeyEnv != provider.APIKeyEnv || spec.Provider.UpstreamModel != provider.UpstreamModel || !reflect.DeepEqual(spec.SupportedModels, profile.SupportedModels) {
			t.Fatalf("provider/model policy changed: %+v", spec)
		}
		wantThinking := map[string]string{"reasoning_effort": "high", "thinking_effort": "high", "type": "enabled", "verbosity": "low"}
		if !reflect.DeepEqual(spec.Provider.Thinking, wantThinking) || launch.ReasoningEffort != "high" || spec.ReasoningEffort != "high" {
			t.Fatalf("provider reasoning snapshot = %+v", spec)
		}
		for _, want := range []string{`model_reasoning_effort="high"`, `model_provider="amux"`, `approval_policy="on-request"`} {
			if !containsString(launch.ExtraArgs, want) {
				t.Fatalf("args = %q, missing %q", launch.ExtraArgs, want)
			}
		}
		if model, ok := AgentArgsModel("codex", launch.ExtraArgs); !ok || model != "upstream-model" {
			t.Fatalf("process model = %q, %t; want upstream-model", model, ok)
		}
	}
	if provider.Thinking["reasoning_effort"] != "low" || provider.Thinking["thinking_effort"] != "low" || profile.ReasoningEffort != "" {
		t.Fatalf("source profile/provider mutated: %+v", profile)
	}
	baseline, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{})
	if err != nil || !reflect.DeepEqual(baseline.ProfileSpec.Provider.Thinking, provider.Thinking) || !containsString(baseline.ExtraArgs, `model_reasoning_effort="low"`) {
		t.Fatalf("provider defaults changed without effort override: %+v, err=%v", baseline, err)
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "unlisted-model", ReasoningEffort: "high"}); err == nil || !strings.Contains(err.Error(), "not supported by profile") {
		t.Fatalf("profile model policy bypassed: %v", err)
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{ReasoningEffort: "invalid"}); err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("provider accepted invalid effort: %v", err)
	}
	native, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "native-model", ReasoningEffort: "high"})
	if err != nil || native.ProfileSpec.Provider != nil || native.Model != "native-model" || !containsString(native.ExtraArgs, `model_reasoning_effort="high"`) {
		t.Fatalf("native model override = %+v, err=%v", native, err)
	}
}
