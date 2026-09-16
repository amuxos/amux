package runtime

import (
	"testing"

	"github.com/amuxos/amux/internal/distribution"
)

func TestResponsesChatOptionsUseSelectedEndpoint(t *testing.T) {
	previous := distribution.SupportsResponsesChat
	distribution.SupportsResponsesChat = true
	t.Cleanup(func() { distribution.SupportsResponsesChat = previous })
	spec := `{"profile_spec":{"provider":{"wire_api":"responses-via-chat-completions","base_url":"https://models.example/v1","api_key_env":"MODEL_API_KEY"}}}`
	opts, err := responsesChatOptionsFromLaunchSpec(spec, "codex-app-server", "http://127.0.0.1:8800/v1")
	if err != nil {
		t.Fatal(err)
	}
	if opts == nil || opts.UpstreamBaseURL != "http://127.0.0.1:8800/v1" || opts.APIKeyEnv != "MODEL_API_KEY" {
		t.Fatalf("gateway options = %+v", opts)
	}
}
