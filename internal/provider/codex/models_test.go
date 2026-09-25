package codex

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/provider/jsonrpc"
)

func modelTestClient(t *testing.T, respond func(jsonrpc.Message) any) *jsonrpc.Client {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	client := jsonrpc.NewClient(a, a)
	go func() { _ = client.Run() }()
	go func() {
		decoder, encoder := json.NewDecoder(b), json.NewEncoder(b)
		for {
			var request jsonrpc.Message
			if decoder.Decode(&request) != nil {
				return
			}
			result := respond(request)
			if request.ID == 0 {
				continue
			}
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
		}
	}()
	return client
}

func TestNativeModelsDiscoverFreshPaginatedCapabilities(t *testing.T) {
	for _, name := range []string{"new-model", "replacement-model"} {
		client := modelTestClient(t, func(req jsonrpc.Message) any {
			switch req.Method {
			case "initialize", "initialized":
				return map[string]any{}
			case "account/read":
				return map[string]any{"account": map[string]string{"type": "apiKey"}, "requiresOpenaiAuth": true}
			case "model/list":
				var params struct {
					Cursor        string `json:"cursor"`
					IncludeHidden bool   `json:"includeHidden"`
				}
				_ = json.Unmarshal(req.Params, &params)
				if params.IncludeHidden {
					t.Error("requested hidden models")
				}
				if params.Cursor == "" {
					return map[string]any{"data": []any{map[string]any{"id": "ui-id", "model": name, "displayName": "Native name", "isDefault": true,
						"supportedReasoningEfforts": []any{map[string]string{"reasoningEffort": "future-effort"}}, "defaultReasoningEffort": "future-effort"}}, "nextCursor": "second"}
				}
				return map[string]any{"data": []any{map[string]any{"model": name}, map[string]any{"id": "other"}, map[string]any{"model": "secret", "hidden": true}}}
			default:
				t.Errorf("unexpected RPC (must not create a conversation): %s", req.Method)
				return nil
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		models, signedIn, err := discoverModels(ctx, client)
		cancel()
		if err != nil || !signedIn || len(models) != 2 {
			t.Fatalf("models=%+v signedIn=%v err=%v", models, signedIn, err)
		}
		if models[0].Model != name || models[1].Model != "other" || models[0].DefaultReasoningEffort != "future-effort" || models[0].SupportedReasoningEfforts[0].ReasoningEffort != "future-effort" {
			t.Fatalf("lost live capabilities: %+v", models)
		}
	}
}

func TestNativeModelsSignedOutDoesNotReadBundledCatalog(t *testing.T) {
	client := modelTestClient(t, func(req jsonrpc.Message) any {
		if req.Method == "model/list" {
			t.Error("signed-out account queried model/list")
		}
		return map[string]any{"account": nil, "requiresOpenaiAuth": true}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	models, signedIn, err := discoverModels(ctx, client)
	if err != nil || signedIn || len(models) != 0 {
		t.Fatalf("models=%v signedIn=%v err=%v", models, signedIn, err)
	}
}

func TestNativeModelsCustomProviderAndMalformedPagination(t *testing.T) {
	client := modelTestClient(t, func(req jsonrpc.Message) any {
		if req.Method == "model/list" {
			return map[string]any{"data": []any{}, "nextCursor": "loop"}
		}
		return map[string]any{"account": nil, "requiresOpenaiAuth": false}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	models, _, err := discoverModels(ctx, client)
	if err == nil || len(models) != 0 {
		t.Fatalf("partial catalog accepted: models=%v err=%v", models, err)
	}
}

func TestNativeModelsDiscoveryCancellation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	client := jsonrpc.NewClient(a, a)
	go func() { _ = client.Run() }()
	go func() { var req jsonrpc.Message; _ = json.NewDecoder(b).Decode(&req) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := discoverModels(ctx, client); err == nil {
		t.Fatal("missing timeout")
	}
}
