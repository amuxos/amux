package runtime

import (
	"context"
	"reflect"
	"testing"
)

func TestPublicProviderEndpointPreservesExplicitLaunch(t *testing.T) {
	args := []string{"--model", "user-model", "-c", `model_providers.custom.base_url="https://provider.example/v1"`}
	srv := &server{providerEndpointSelector: newProviderEndpointSelector()}
	for _, spec := range []string{"", "invalid json", `{"profile_spec":{"provider":{"base_url":"https://provider.example/v1"}}}`} {
		got, selected, err := srv.resolveProviderEndpoint(context.Background(), spec, "codex-app-server", args)
		if err != nil || selected != "" || !reflect.DeepEqual(got, args) {
			t.Fatalf("resolveProviderEndpoint = %v, %q, %v; want unchanged args with no selected endpoint", got, selected, err)
		}
	}
}
