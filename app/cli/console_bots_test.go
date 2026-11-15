package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge"
)

func TestConsoleBotsAddDisableEnableDelete(t *testing.T) {
	consoleTestOperator(t)
	var fetched []string
	original := consoleFetchBotIdentity
	consoleFetchBotIdentity = func(_ context.Context, appID, appSecret string) (bridge.BotIdentity, error) {
		fetched = append(fetched, appID)
		if appSecret != "good-secret" && appSecret != "rotated-secret" {
			return bridge.BotIdentity{}, errors.New("app secret invalid")
		}
		return bridge.BotIdentity{Name: "Ops Bot", OpenId: "ou_bot_" + appID}, nil
	}
	t.Cleanup(func() { consoleFetchBotIdentity = original })
	if err := bridge.SaveProfileSecrets("default", &bridge.Secrets{AppId: "cli_running", AppSecret: "running-secret", BotName: "Main"}); err != nil {
		t.Fatal(err)
	}
	hub := &consoleResourceHub{botsPayload: `{"status":"success","bots":[{"profile":"default","id":"feishu:cli_running","name":"Main","status":"running","actions":["get","save"],"settings":{},"version":3}]}`}
	backend := &consoleBackend{hub: hub}
	run := func(action, data string) (map[string]any, error) {
		t.Helper()
		value, err := backend.Execute(context.Background(), "bots", action, json.RawMessage(data))
		if err != nil {
			return nil, err
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(consoleTestJSON(t, value)), &out)
		return out, nil
	}
	// A malformed App ID is rejected before any Feishu request.
	_, err := run("create", `{"profile":"ops","app_id":"bad","app_secret":"good-secret"}`)
	consoleTestCode(t, err, "invalid_field")
	if len(fetched) != 0 {
		t.Fatalf("malformed App ID reached Feishu: %v", fetched)
	}
	if _, err := run("create", `{"profile":"ops","app_id":"cli_ops","app_secret":"wrong-secret"}`); err == nil {
		t.Fatal("accepted invalid credentials")
	}
	if _, err := os.Stat(mustBotSecretsPath(t, "ops")); !os.IsNotExist(err) {
		t.Fatalf("invalid credentials were saved: %v", err)
	}
	_, err = run("create", `{"profile":"other","app_id":"cli_running","app_secret":"good-secret"}`)
	consoleTestCode(t, err, "bot_app_in_use")
	_, err = run("create", `{"profile":"default","app_id":"cli_new","app_secret":"good-secret"}`)
	consoleTestCode(t, err, "bot_profile_exists")
	_, err = run("create", `{"profile":"ops","app_id":"cli_ops","app_secret":"good-secret","owner":"ou_x"}`)
	consoleTestCode(t, err, "invalid_data")
	created, err := run("create", `{"profile":"ops","app_id":"cli_ops","app_secret":"good-secret","interaction_mode":"thread"}`)
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, created)
	if strings.Contains(raw, "good-secret") || strings.Contains(raw, "running-secret") || created["restart_required"] != true || created["restart_command"] != consoleBotRestartCommand {
		t.Fatalf("create result = %s", raw)
	}
	bots := consoleTestBots(t, created)
	if bots["default"]["status"] != "running" || bots["default"]["app_id"] != "cli_running" || bots["default"]["pending_restart"] != nil {
		t.Fatalf("running bot = %#v", bots["default"])
	}
	if actions := consoleStringList(bots["default"]["actions"]); strings.Join(actions, ",") != "get,save,update_credentials,disable" {
		t.Fatalf("running actions = %v", actions)
	}
	if bots["ops"]["status"] != "stopped" || bots["ops"]["name"] != "Ops Bot" || bots["ops"]["pending_restart"] != true {
		t.Fatalf("new bot = %#v", bots["ops"])
	}
	secrets, err := bridge.LoadProfileSecrets("ops")
	if err != nil || secrets.AppSecret != "good-secret" || secrets.BotOpenId != "ou_bot_cli_ops" {
		t.Fatalf("saved secrets = %#v, %v", secrets, err)
	}
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := configValue(cfg, "bridge.feishu.profiles.ops.interaction.mode"); mode != "thread" {
		t.Fatalf("interaction mode = %v", mode)
	}
	if _, err := run("update_credentials", `{"profile":"ops","app_secret":"rotated-secret"}`); err != nil {
		t.Fatal(err)
	}
	if secrets, _ := bridge.LoadProfileSecrets("ops"); secrets.AppSecret != "rotated-secret" || secrets.AppId != "cli_ops" {
		t.Fatalf("rotated = %#v", secrets)
	}
	_, err = run("update_credentials", `{"profile":"ops","app_secret":"wrong-secret"}`)
	consoleTestCode(t, err, "bot_credentials_invalid")
	_, err = run("disable", `{"profile":"default"}`)
	consoleTestCode(t, err, "confirmation_required")
	disabled, err := run("disable", `{"profile":"default","confirm":"default"}`)
	if err != nil {
		t.Fatal(err)
	}
	bots = consoleTestBots(t, disabled)
	if bots["default"]["status"] != "running" || bots["default"]["pending_restart"] != true || bots["default"]["configured"] != "disabled" {
		t.Fatalf("pending disable = %#v", bots["default"])
	}
	_, err = run("delete", `{"profile":"ops","confirm":"ops"}`)
	consoleTestCode(t, err, "not_found")
	if _, err := run("disable", `{"profile":"ops","confirm":"ops"}`); err != nil {
		t.Fatal(err)
	}
	hub.botsPayload = `{"status":"success","bots":[]}`
	listed, err := backend.Snapshot(context.Background(), "bots", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	bots = consoleTestBots(t, listed)
	if bots["ops"]["status"] != "disabled" || strings.Join(consoleStringList(bots["ops"]["actions"]), ",") != "enable,delete" {
		t.Fatalf("disabled bot = %#v", bots["ops"])
	}
	if _, err := run("enable", `{"profile":"ops"}`); err != nil {
		t.Fatal(err)
	}
	_, err = run("enable", `{"profile":"ops"}`)
	consoleTestCode(t, err, "bot_profile_exists")
	if _, err := run("disable", `{"profile":"ops","confirm":"ops"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run("delete", `{"profile":"ops","confirm":"ops"}`); err != nil {
		t.Fatal(err)
	}
	listed, _ = backend.Snapshot(context.Background(), "bots", url.Values{})
	if _, ok := consoleTestBots(t, listed)["ops"]; ok {
		t.Fatal("deleted bot still listed")
	}
	if strings.Join(fetched, ",") != "cli_ops,cli_ops,cli_ops,cli_ops" {
		t.Fatalf("Feishu lookups = %v", fetched)
	}
}

func TestConsoleBotsListLocalProfilesWithoutBridge(t *testing.T) {
	consoleTestOperator(t)
	if err := bridge.SaveProfileSecrets("solo", &bridge.Secrets{AppId: "cli_solo", AppSecret: "solo-secret"}); err != nil {
		t.Fatal(err)
	}
	value, err := (&consoleBackend{hub: &consoleResourceHub{}}).Snapshot(context.Background(), "bots", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if strings.Contains(raw, "solo-secret") || !strings.Contains(raw, `"bridge_available":false`) {
		t.Fatalf("bots = %s", raw)
	}
	if bot := consoleTestBots(t, value)["solo"]; bot["status"] != "stopped" || bot["app_id"] != "cli_solo" {
		t.Fatalf("solo = %#v", bot)
	}
}

func mustBotSecretsPath(t *testing.T, profile string) string {
	t.Helper()
	path, err := bridge.ProfileSecretsPath(profile)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func consoleTestBots(t *testing.T, value any) map[string]map[string]any {
	t.Helper()
	var decoded struct {
		Bots []map[string]any `json:"bots"`
	}
	if err := json.Unmarshal([]byte(consoleTestJSON(t, value)), &decoded); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, bot := range decoded.Bots {
		out[bot["profile"].(string)] = bot
	}
	return out
}
