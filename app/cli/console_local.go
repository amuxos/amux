package cli

import (
	"context"
	"encoding/json"
	"net/url"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (b *consoleBackend) snapshotBridge(ctx context.Context, resource string, query url.Values) (any, error) {
	action := "list"
	if query.Get("chat_id") != "" || query.Get("profile") != "" && resource == "bots" {
		action = "get"
	}
	params := map[string]string{}
	for _, key := range []string{"profile", "chat_id"} {
		if value := query.Get(key); value != "" {
			params[key] = value
		}
	}
	data, _ := json.Marshal(params)
	return b.bridgeManagement(ctx, resource, action, data)
}
func (b *consoleBackend) executeBridge(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	return b.bridgeManagement(ctx, resource, action, data)
}
func (b *consoleBackend) bridgeManagement(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	response, err := b.hub.ConsoleManagement(ctx, &pb.ConsoleManagementRequest{Resource: resource, Action: action, PayloadJson: data})
	if err != nil {
		return nil, consoleOperationError(err)
	}
	if len(response.GetPayloadJson()) == 0 || !json.Valid(response.GetPayloadJson()) {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	return json.RawMessage(response.GetPayloadJson()), nil
}
