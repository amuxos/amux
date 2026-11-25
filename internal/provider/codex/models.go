package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/provider/jsonrpc"
)

type NativeModel struct {
	BusinessMetadata          json.RawMessage      `json:"businessMetadata"`
	Variants                  []NativeModelVariant `json:"-"`
	ID                        string               `json:"id"`
	Model                     string               `json:"model"`
	DisplayName               string               `json:"displayName"`
	Hidden                    bool                 `json:"hidden"`
	IsDefault                 bool                 `json:"isDefault"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort string `json:"defaultReasoningEffort"`
}

type NativeModelVariant struct {
	DisplayName    string
	BackendVariant string
}

// DiscoverModels uses only account/model RPCs: it never creates a thread or turn.
// The caller supplies the same native environment and isolation backend as launch.
func DiscoverModels(parent context.Context, opts StartOptions) ([]NativeModel, bool, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	providerMu.RLock()
	registration := registeredProviders[opts.ProviderName]
	providerMu.RUnlock()
	if prepare := registration.PrepareNativeModelDiscovery; prepare != nil {
		if err := prepare(ctx, &opts); err != nil {
			return nil, false, err
		}
	}
	bin := opts.Executable
	if bin == "" {
		bin = "codex"
	}
	bin, err := provider.ResolveExecutableFromEnvPATH(bin, opts.Env)
	if err != nil {
		return nil, false, err
	}
	backend := opts.Backend
	if backend == nil {
		backend = isolation.NewPassthroughBackend()
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path: bin, Args: []string{"app-server"}, Env: opts.Env, Dir: opts.Cwd,
		SetProcessGroup: true, Context: opts.LaunchContext,
	})
	if err != nil {
		return nil, false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = handle.Kill(cleanup)
		_, _ = handle.Wait(cleanup)
		_ = handle.Close()
	}()
	go func() { _, _ = io.Copy(io.Discard, handle.Stderr()) }()
	client := jsonrpc.NewClient(handle.Stdout(), handle.Stdin())
	go func() { _ = client.Run() }()
	models, signedIn, err := discoverModels(ctx, client)
	if err == nil && signedIn {
		if registration.DecorateNativeModels != nil {
			models = registration.DecorateNativeModels(models)
		}
	}
	return models, signedIn, err
}

func discoverModels(ctx context.Context, client *jsonrpc.Client) ([]NativeModel, bool, error) {
	if _, err := client.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "amux", "version": "dev"},
	}); err != nil {
		return nil, false, err
	}
	if err := client.Notify("initialized", nil); err != nil {
		return nil, false, err
	}
	account, err := client.Call(ctx, "account/read", map[string]any{"refreshToken": true})
	if err != nil {
		return nil, false, err
	}
	var auth struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth *bool           `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(account.Result, &auth); err != nil {
		return nil, false, err
	}
	// A disconnected account must not expose the CLI's bundled fallback catalog.
	if (len(auth.Account) == 0 || string(auth.Account) == "null") && (auth.RequiresOpenAIAuth == nil || *auth.RequiresOpenAIAuth) {
		return nil, false, nil
	}
	var models []NativeModel
	cursor := ""
	seenCursors, seenModels := map[string]bool{}, map[string]bool{}
	for page := 0; page < 100; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, err := client.Call(ctx, "model/list", params)
		if err != nil {
			return nil, false, err
		}
		var result struct {
			Data       []NativeModel `json:"data"`
			NextCursor string        `json:"nextCursor"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, false, err
		}
		for _, model := range result.Data {
			model.Model = strings.TrimSpace(model.Model)
			if model.Model == "" {
				model.Model = strings.TrimSpace(model.ID)
			}
			if model.Hidden || model.Model == "" || seenModels[model.Model] {
				continue
			}
			seenModels[model.Model] = true
			models = append(models, model)
		}
		cursor = result.NextCursor
		if cursor == "" {
			return models, true, nil
		}
		if seenCursors[cursor] {
			return nil, false, fmt.Errorf("codex model/list repeated cursor")
		}
		seenCursors[cursor] = true
	}
	return nil, false, fmt.Errorf("codex model/list exceeded page limit")
}
