package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/bridge"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const consoleManagementPayloadLimit = 64 << 10

// Only the Bridge owns settings storage and transitions. This interface is
// implemented by the very same *bridge.Bridge as the Bot-session controller.
type consoleManagementController interface {
	ConsoleBot(context.Context) (bridge.ConsoleBot, error)
	ConsoleSaveBot(context.Context, bridge.ConsoleBotSave) (bridge.ConsoleBotResult, error)
	ConsoleGroups(context.Context) ([]bridge.ConsoleGroup, error)
	ConsoleGroupSettings(context.Context, string) (bridge.ConsoleGroupsResult, error)
	ConsoleSaveGroup(context.Context, string, *bridge.GroupSettings, int64) (bridge.ConsoleGroupsResult, error)
	ConsoleAgentReferences(context.Context) ([]bridge.ConsoleAgentReference, error)
}

type consoleRead struct {
	Profile string `json:"profile,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
}

type consoleGroupSave struct {
	Profile         string                `json:"profile"`
	ChatID          string                `json:"chat_id"`
	Settings        *bridge.GroupSettings `json:"settings"`
	ExpectedVersion *int64                `json:"expected_version,omitempty"`
}

type consoleBotsResult struct {
	Status string              `json:"status"`
	Bots   []bridge.ConsoleBot `json:"bots"`
	Bot    *bridge.ConsoleBot  `json:"bot,omitempty"`
}

// consoleReferencesResult lists every Bot and group default expert in the Bridge
// database, running Bot or not, for the console's Pack usage and removal checks.
type consoleReferencesResult struct {
	Status     string                         `json:"status"`
	References []bridge.ConsoleAgentReference `json:"references"`
}

const consoleMaxAgentReferences = 5000

// ConsoleManagement is an operator-only protocol, not an arbitrary method or
// config proxy. Shared-token access is still enforced by the Hub interceptor;
// any actor credential is rejected before parsing or reaching a controller,
// including when no principal store exists. Unknown methods remain gRPC's
// Unimplemented default and unknown resources/actions never dispatch.
func (s *hubServer) ConsoleManagement(ctx context.Context, req *pb.ConsoleManagementRequest) (*pb.ConsoleManagementResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, token := range md.Get("x-amux-actor-token") {
		if strings.TrimSpace(token) != "" {
			return nil, status.Error(codes.PermissionDenied, "console_operator_required")
		}
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "console_request_required")
	}
	if req.Resource != "bots" && req.Resource != "groups" {
		return nil, status.Error(codes.InvalidArgument, "console_unknown_resource")
	}
	if req.Action != "list" && req.Action != "get" && req.Action != "save" && req.Action != "references" {
		return nil, status.Error(codes.InvalidArgument, "console_unknown_action")
	}
	if req.Action == "references" && req.Resource != "bots" {
		return nil, status.Error(codes.InvalidArgument, "console_unknown_action")
	}
	body := req.PayloadJson
	if len(body) == 0 {
		body = []byte("{}")
	}
	if len(body) > consoleManagementPayloadLimit {
		return nil, status.Error(codes.ResourceExhausted, "console_payload_too_large")
	}
	if !utf8.Valid(body) || !consoleJSONValid(body) {
		return nil, status.Error(codes.InvalidArgument, "console_invalid_payload")
	}
	budget := 45 * time.Second
	if req.Resource == "groups" {
		budget = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var value any
	var err error
	if req.Resource == "bots" {
		value, err = s.consoleBots(ctx, req.Action, body)
	} else {
		value, err = s.consoleGroups(ctx, req.Action, body)
	}
	if err != nil {
		return nil, consoleManagementError(ctx, err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, status.Error(codes.Internal, "console_encode_failed")
	}
	if len(payload) > 1<<20 {
		return nil, status.Error(codes.ResourceExhausted, "console_response_too_large")
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, status.Error(codes.Internal, "console_invalid_result")
	}
	switch envelope.Status {
	case bridge.BotSettingsStatusSuccess, bridge.BotSettingsStatusNeedsConfirmation, bridge.BotSettingsStatusNeedsRecoveryChoice, bridge.BotSettingsStatusStateChanged:
	default:
		return nil, status.Error(codes.Internal, "console_invalid_result")
	}
	return &pb.ConsoleManagementResponse{Status: envelope.Status, PayloadJson: payload}, nil
}

func (s *hubServer) consoleControllers() map[string]consoleManagementController {
	out := make(map[string]consoleManagementController, len(s.consoleManagementControllers))
	for profile, controller := range s.consoleManagementControllers {
		if controller != nil {
			out[profile] = controller
		}
	}
	// Preserve the same-Bridge seam for tests and older assembly sites. Never
	// construct a fallback Bridge or open a database outside its owner.
	for profile, existing := range s.feishuBotSessionControllers {
		if _, found := out[profile]; !found {
			if controller, ok := existing.(consoleManagementController); ok {
				out[profile] = controller
			}
		}
	}
	return out
}

func (s *hubServer) consoleController(profile string) (consoleManagementController, error) {
	if !consoleProfileValid(profile) {
		return nil, status.Error(codes.InvalidArgument, "console_invalid_profile")
	}
	controller := s.consoleControllers()[profile]
	if controller == nil {
		return nil, status.Error(codes.NotFound, "console_bot_not_found")
	}
	return controller, nil
}

func (s *hubServer) consoleBots(ctx context.Context, action string, body []byte) (any, error) {
	if action == "references" {
		var input struct{}
		if err := consoleDecode(body, &input); err != nil {
			return nil, err
		}
		controllers := s.consoleControllers()
		if len(controllers) == 0 {
			// A stopped or disabled Bridge can leave persisted Bot and group
			// defaults behind. No controller means the references are unknown.
			return nil, status.Error(codes.Unavailable, "console_bridge_references_unavailable")
		}
		if len(controllers) > 256 {
			return nil, status.Error(codes.ResourceExhausted, "console_too_many_bots")
		}
		profiles := make([]string, 0, len(controllers))
		for profile := range controllers {
			profiles = append(profiles, profile)
		}
		sort.Strings(profiles)
		// Every Bridge reads the same stored settings, so the union is deduplicated.
		out := consoleReferencesResult{Status: bridge.BotSettingsStatusSuccess, References: []bridge.ConsoleAgentReference{}}
		seen := map[bridge.ConsoleAgentReference]bool{}
		for _, profile := range profiles {
			references, err := controllers[profile].ConsoleAgentReferences(ctx)
			if err != nil {
				return nil, err
			}
			for _, reference := range references {
				if seen[reference] {
					continue
				}
				if seen[reference] = true; len(out.References) >= consoleMaxAgentReferences {
					return nil, status.Error(codes.ResourceExhausted, "console_too_many_references")
				}
				out.References = append(out.References, reference)
			}
		}
		return out, nil
	}
	if action == "save" {
		var input bridge.ConsoleBotSave
		if err := consoleDecode(body, &input); err != nil {
			return nil, err
		}
		if !consoleHasFields(body, "expected_version", "settings") || input.Settings == nil || input.ExpectedVersion < 0 ||
			len(input.RequestID) > 128 || len(input.ConfirmationToken) > 128 || len(input.RecoveryAttemptID) > 128 {
			return nil, status.Error(codes.InvalidArgument, "console_invalid_bot_settings")
		}
		var raw struct {
			Settings json.RawMessage `json:"settings"`
		}
		_ = json.Unmarshal(body, &raw)
		if !consoleHasFields(raw.Settings, "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "interaction_mode", "isolation_required") {
			return nil, status.Error(codes.InvalidArgument, "console_incomplete_bot_settings")
		}
		controller, err := s.consoleController(input.Profile)
		if err != nil {
			return nil, err
		}
		return controller.ConsoleSaveBot(ctx, input)
	}
	var input consoleRead
	if err := consoleDecode(body, &input); err != nil {
		return nil, err
	}
	if input.ChatID != "" {
		return nil, status.Error(codes.InvalidArgument, "console_invalid_payload")
	}
	out := consoleBotsResult{Status: bridge.BotSettingsStatusSuccess, Bots: []bridge.ConsoleBot{}}
	if action == "get" || input.Profile != "" {
		controller, err := s.consoleController(input.Profile)
		if err != nil {
			return nil, err
		}
		bot, err := controller.ConsoleBot(ctx)
		if err != nil {
			return nil, err
		}
		out.Bot, out.Bots = &bot, []bridge.ConsoleBot{bot}
		return out, nil
	}
	controllers := s.consoleControllers()
	if len(controllers) > 256 {
		return nil, status.Error(codes.ResourceExhausted, "console_too_many_bots")
	}
	profiles := make([]string, 0, len(controllers))
	for profile := range controllers {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	for _, profile := range profiles {
		bot, err := controllers[profile].ConsoleBot(ctx)
		if err != nil {
			return nil, err
		}
		out.Bots = append(out.Bots, bot)
	}
	return out, nil
}

func (s *hubServer) consoleGroups(ctx context.Context, action string, body []byte) (any, error) {
	if action == "save" {
		var input consoleGroupSave
		if err := consoleDecode(body, &input); err != nil {
			return nil, err
		}
		if input.Settings == nil || !consoleChatValid(input.ChatID) || len(input.Settings.DefaultArgs) > 64 {
			return nil, status.Error(codes.InvalidArgument, "console_invalid_group_settings")
		}
		var raw struct {
			Settings json.RawMessage `json:"settings"`
		}
		_ = json.Unmarshal(body, &raw)
		// A group save is replacement, never an implicit patch resetting fields.
		if !consoleHasFields(raw.Settings, "interaction_mode", "isolation_required", "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "thinking_process", "mode", "default_target", "default_cwd", "default_args", "idle_timeout_seconds", "thread_context_limit", "max_sessions_total", "max_sessions_per_user", "version") {
			return nil, status.Error(codes.InvalidArgument, "console_incomplete_group_settings")
		}
		version := input.Settings.Version
		if input.ExpectedVersion != nil {
			version = *input.ExpectedVersion
		}
		if version < 0 {
			return nil, status.Error(codes.InvalidArgument, "console_invalid_version")
		}
		controller, err := s.consoleController(input.Profile)
		if err != nil {
			return nil, err
		}
		return controller.ConsoleSaveGroup(ctx, input.ChatID, input.Settings, version)
	}
	var input consoleRead
	if err := consoleDecode(body, &input); err != nil {
		return nil, err
	}
	if action == "get" || input.ChatID != "" {
		if !consoleChatValid(input.ChatID) {
			return nil, status.Error(codes.InvalidArgument, "console_invalid_chat_id")
		}
		controller, err := s.consoleController(input.Profile)
		if err != nil {
			return nil, err
		}
		return controller.ConsoleGroupSettings(ctx, input.ChatID)
	}
	out := bridge.ConsoleGroupsResult{Status: bridge.BotSettingsStatusSuccess, Groups: []bridge.ConsoleGroup{}, Profile: input.Profile, Source: "known_bindings", Actions: []string{}}
	controllers := s.consoleControllers()
	if input.Profile != "" {
		controller, err := s.consoleController(input.Profile)
		if err != nil {
			return nil, err
		}
		controllers = map[string]consoleManagementController{input.Profile: controller}
	}
	if len(controllers) > 256 {
		return nil, status.Error(codes.ResourceExhausted, "console_too_many_bots")
	}
	for _, controller := range controllers {
		groups, err := controller.ConsoleGroups(ctx)
		if err != nil {
			return nil, err
		}
		out.Groups = append(out.Groups, groups...)
		if len(out.Groups) > 1000 {
			return nil, status.Error(codes.ResourceExhausted, "console_too_many_groups")
		}
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.Profile != b.Profile {
			return a.Profile < b.Profile
		}
		return a.ChatID < b.ChatID
	})
	return out, nil
}

func consoleDecode(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return status.Error(codes.InvalidArgument, "console_invalid_payload")
	}
	return nil
}

func consoleHasFields(body []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func consoleProfileValid(value string) bool {
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func consoleChatValid(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Reject duplicate keys, trailing documents, deep nesting and control-bearing
// values before typed decoding. Arbitrary maps never reach a domain controller.
func consoleJSONValid(body []byte) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	if !consoleJSONContainer(d, '{', 1) {
		return false
	}
	_, err = d.Token()
	return err == io.EOF
}

func consoleJSONContainer(d *json.Decoder, kind json.Delim, depth int) bool {
	if depth > 8 {
		return false
	}
	keys := map[string]bool{}
	for d.More() {
		if kind == '{' {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] || len(key) > 128 {
				return false
			}
			keys[key] = true
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case json.Delim:
			if value != '{' && value != '[' || !consoleJSONContainer(d, value, depth+1) {
				return false
			}
		case string:
			if len(value) > 4096 {
				return false
			}
			for _, r := range value {
				if unicode.IsControl(r) {
					return false
				}
			}
		}
	}
	end, err := d.Token()
	return err == nil && (kind == '{' && end == json.Delim('}') || kind == '[' && end == json.Delim(']'))
}

func consoleManagementError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	st, ok := status.FromError(err)
	if ok && strings.HasPrefix(st.Message(), "console_") {
		for _, r := range st.Message() {
			if !(r >= 'a' && r <= 'z' || r == '_') {
				return status.Error(codes.Internal, "console_management_failed")
			}
		}
		return err
	}
	return status.Error(codes.Internal, "console_management_failed")
}
