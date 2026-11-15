package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/webconsole"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bridges start with the Hub, so adding, disabling or re-enabling a Bot changes
// only the Hub-local credentials and takes effect after `amux system restart`.
// The browser never restarts the Hub and never receives an App Secret.
const consoleBotRestartCommand = "amux system restart"

var (
	consoleFetchBotIdentity = bridge.FetchBotIdentityWithCredentials
	consoleBotAppIDPattern  = regexp.MustCompile(`^cli_[A-Za-z0-9]{1,124}$`)
)

func consoleLocalBotAction(action string) bool {
	switch action {
	case "create", "update_credentials", "disable", "enable", "delete":
		return true
	}
	return false
}

type consoleLocalBot struct {
	profile  string
	disabled bool
	secrets  *bridge.Secrets
	err      error
}

func consoleLocalBots() (map[string]consoleLocalBot, error) {
	out := map[string]consoleLocalBot{}
	enabled, err := bridge.ListSecretProfiles()
	if err != nil {
		return nil, err
	}
	for _, profile := range enabled {
		secrets, err := bridge.LoadProfileSecrets(profile)
		out[profile] = consoleLocalBot{profile: profile, secrets: secrets, err: err}
	}
	disabled, err := bridge.ListDisabledSecretProfiles()
	if err != nil {
		return nil, err
	}
	for _, profile := range disabled {
		if _, ok := out[profile]; ok {
			continue
		}
		path, _ := bridge.DisabledProfileSecretsPath(profile)
		secrets, err := bridge.LoadSecrets(path)
		out[profile] = consoleLocalBot{profile: profile, disabled: true, secrets: secrets, err: err}
	}
	return out, nil
}

func (l consoleLocalBot) appID() string {
	if l.secrets == nil {
		return ""
	}
	return strings.TrimSpace(l.secrets.AppId)
}

func (l consoleLocalBot) actions() []string {
	if l.disabled {
		return []string{"enable", "delete"}
	}
	return []string{"update_credentials", "disable"}
}

// snapshotBots lists running Bots from the Hub and adds Bots configured on this
// host that are not running: newly added, disabled, or failed to start.
func (b *consoleBackend) snapshotBots(ctx context.Context, query url.Values) (any, error) {
	var hubResult struct {
		Status string           `json:"status"`
		Bots   []map[string]any `json:"bots"`
		Bot    map[string]any   `json:"bot,omitempty"`
	}
	bridgeAvailable := true
	if b.hub == nil {
		bridgeAvailable = false
	} else if raw, err := b.snapshotBridge(ctx, "bots", query); err != nil {
		// No Bridge, or the requested Bot is not running: local Bots remain listed.
		if code := status.Code(err); code != codes.Unavailable && code != codes.NotFound {
			return nil, err
		}
		bridgeAvailable = status.Code(err) != codes.Unavailable
	} else if err := json.Unmarshal(raw.(json.RawMessage), &hubResult); err != nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	b.localMu.Lock()
	local, err := consoleLocalBots()
	b.localMu.Unlock()
	if err != nil {
		return nil, consoleOperationError(err)
	}
	profileFilter := strings.TrimSpace(query.Get("profile"))
	bots := make([]map[string]any, 0, len(hubResult.Bots)+len(local))
	seen := map[string]bool{}
	for _, bot := range hubResult.Bots {
		profile, _ := bot["profile"].(string)
		seen[profile] = true
		actions := consoleStringList(bot["actions"])
		localBot, configured := local[profile]
		switch {
		case !configured || localBot.disabled:
			bot["pending_restart"] = true
			bot["configured"] = "disabled"
			if configured {
				actions = append(actions, "enable")
			}
		default:
			bot["configured"] = "enabled"
			if id, _ := bot["id"].(string); localBot.appID() != "" && id != bridge.BridgeIdFromAppId(localBot.appID()) {
				bot["pending_restart"] = true
			}
			actions = append(actions, localBot.actions()...)
		}
		if configured {
			bot["app_id"] = localBot.appID()
		}
		bot["actions"] = actions
		bots = append(bots, bot)
	}
	for _, profile := range consoleSortedKeys(local) {
		if seen[profile] || profileFilter != "" && profile != profileFilter {
			continue
		}
		localBot := local[profile]
		view := map[string]any{"profile": profile, "id": bridge.BridgeIdFromAppId(localBot.appID()), "name": profile,
			"app_id": localBot.appID(), "status": "stopped", "configured": "enabled", "pending_restart": true, "actions": localBot.actions()}
		if localBot.secrets != nil && strings.TrimSpace(localBot.secrets.BotName) != "" {
			view["name"] = localBot.secrets.BotName
		}
		if localBot.disabled {
			view["status"], view["configured"], view["pending_restart"] = "disabled", "disabled", false
		}
		if localBot.err != nil {
			view["error"] = "credentials_unreadable"
		}
		bots = append(bots, view)
	}
	if profileFilter != "" && len(bots) == 0 {
		return nil, consoleMissing()
	}
	out := map[string]any{"status": "success", "bots": bots, "actions": []string{"create"}, "restart_command": consoleBotRestartCommand, "bridge_available": bridgeAvailable}
	if profileFilter != "" {
		out["bot"] = bots[0]
	}
	return out, nil
}

func consoleStringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func consoleSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func consoleValidBotSecret(secret string) bool {
	if secret == "" || len(secret) > 256 {
		return false
	}
	for _, r := range secret {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func (b *consoleBackend) executeLocalBot(ctx context.Context, action string, data json.RawMessage) (any, error) {
	if consoleHasAgentContext() || !b.managedOperator && actorToken() != "" {
		return nil, consoleInvalid("operator_required", "")
	}
	var request struct {
		Profile         string `json:"profile"`
		AppID           string `json:"app_id"`
		AppSecret       string `json:"app_secret"`
		InteractionMode string `json:"interaction_mode"`
		Confirm         string `json:"confirm"`
	}
	if err := consoleDecode(data, &request); err != nil {
		return nil, err
	}
	request.Profile, request.AppID = strings.TrimSpace(request.Profile), strings.TrimSpace(request.AppID)
	if _, err := bridge.ProfileSecretsPath(request.Profile); err != nil || len(request.Profile) > 64 {
		return nil, consoleInvalid("invalid_field", "profile")
	}
	allowed := map[string]bool{"profile": true}
	switch action {
	case "create":
		allowed["app_id"], allowed["app_secret"], allowed["interaction_mode"] = true, true, true
	case "update_credentials":
		allowed["app_secret"] = true
	case "disable", "delete":
		allowed["confirm"] = true
		if request.Confirm != request.Profile {
			return nil, consoleInvalid("confirmation_required", "confirm")
		}
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for key := range fields {
		if !allowed[key] {
			return nil, consoleInvalid("invalid_data", "")
		}
	}
	var err error
	switch action {
	case "create":
		err = b.consoleCreateBot(ctx, request.Profile, request.AppID, request.AppSecret, request.InteractionMode)
	case "update_credentials":
		err = b.consoleUpdateBotSecret(ctx, request.Profile, request.AppSecret)
	case "disable":
		b.localMu.Lock()
		_, err = bridge.DisableProfileSecrets(request.Profile)
		b.localMu.Unlock()
	case "enable":
		b.localMu.Lock()
		err = consoleEnableBot(request.Profile)
		b.localMu.Unlock()
	case "delete":
		b.localMu.Lock()
		err = bridge.DeleteDisabledProfileSecrets(request.Profile)
		b.localMu.Unlock()
	default:
		return nil, consoleInvalid("unsupported_action", "")
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, consoleMissing()
	case errors.Is(err, fs.ErrExist):
		return nil, &consoleResourceError{code: "bot_profile_exists", field: "profile", cause: webconsole.ErrConflict}
	case err != nil:
		return nil, consoleOperationError(err)
	}
	value, err := b.snapshotBots(ctx, url.Values{})
	if err != nil {
		return nil, err
	}
	out := value.(map[string]any)
	out["status"], out["profile"], out["restart_required"] = "saved", request.Profile, true
	return out, nil
}

func (b *consoleBackend) consoleCreateBot(ctx context.Context, profile, appID, appSecret, interactionMode string) error {
	if !consoleBotAppIDPattern.MatchString(appID) {
		return consoleInvalid("invalid_field", "app_id")
	}
	if !consoleValidBotSecret(appSecret) {
		return consoleInvalid("invalid_field", "app_secret")
	}
	interactionMode = strings.TrimSpace(interactionMode)
	if interactionMode != "" && interactionMode != config.FeishuInteractionThread && interactionMode != config.FeishuInteractionSingleton {
		return consoleInvalid("invalid_field", "interaction_mode")
	}
	b.localMu.Lock()
	local, err := consoleLocalBots()
	b.localMu.Unlock()
	if err != nil {
		return err
	}
	if _, exists := local[profile]; exists {
		return &consoleResourceError{code: "bot_profile_exists", field: "profile", cause: webconsole.ErrConflict}
	}
	for _, other := range local {
		if other.appID() == appID {
			return &consoleResourceError{code: "bot_app_in_use", field: "app_id", cause: webconsole.ErrConflict}
		}
	}
	if interactionMode == config.FeishuInteractionSingleton {
		if err := validateLocalFeishuSingletonCompatibility(profile); err != nil {
			return consoleInvalid("invalid_field", "interaction_mode")
		}
	}
	// The only Feishu call: prove the credentials work before saving them.
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	identity, err := consoleFetchBotIdentity(lookupCtx, appID, appSecret)
	cancel()
	if err != nil {
		return consoleInvalid("bot_credentials_invalid", "app_secret")
	}
	b.localMu.Lock()
	defer b.localMu.Unlock()
	if err := bridge.CreateProfileSecrets(profile, &bridge.Secrets{AppId: appID, AppSecret: appSecret, BotName: identity.Name, BotOpenId: identity.OpenId}); err != nil {
		return err
	}
	return ensureLocalConfigForFeishuSetupWithInteraction(profile, interactionMode)
}

// consoleUpdateBotSecret rotates the App Secret of the same Feishu app. A
// different App ID is a different Bot whose owner and IDs do not carry over.
func (b *consoleBackend) consoleUpdateBotSecret(ctx context.Context, profile, appSecret string) error {
	if !consoleValidBotSecret(appSecret) {
		return consoleInvalid("invalid_field", "app_secret")
	}
	b.localMu.Lock()
	current, err := bridge.LoadProfileSecrets(profile)
	b.localMu.Unlock()
	if err != nil {
		return err
	}
	appID := strings.TrimSpace(current.AppId)
	if appID == "" {
		return consoleInvalid("invalid_field", "profile")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	identity, err := consoleFetchBotIdentity(lookupCtx, appID, appSecret)
	cancel()
	if err != nil {
		return consoleInvalid("bot_credentials_invalid", "app_secret")
	}
	b.localMu.Lock()
	defer b.localMu.Unlock()
	latest, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(latest.AppId) != appID {
		return &consoleResourceError{code: "operation_conflict", field: "profile", cause: webconsole.ErrConflict}
	}
	latest.AppSecret = appSecret
	if identity.Name != "" {
		latest.BotName = identity.Name
	}
	if identity.OpenId != "" {
		latest.BotOpenId = identity.OpenId
	}
	return bridge.SaveProfileSecrets(profile, latest)
}

// consoleEnableBot refuses to enable a second profile for an App another
// enabled profile already serves.
func consoleEnableBot(profile string) error {
	local, err := consoleLocalBots()
	if err != nil {
		return err
	}
	target, ok := local[profile]
	if !ok {
		return fs.ErrNotExist
	}
	if !target.disabled {
		return fs.ErrExist
	}
	for name, other := range local {
		if name != profile && !other.disabled && other.appID() != "" && other.appID() == target.appID() {
			return &consoleResourceError{code: "bot_app_in_use", field: "profile", cause: webconsole.ErrConflict}
		}
	}
	return bridge.EnableProfileSecrets(profile)
}
