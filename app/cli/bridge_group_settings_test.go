package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/spf13/viper"
)

func TestFeishuGroupSettingsCommandLoadsSavedExperiment(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("AMUX_HOME", t.TempDir())
			viper.Reset()
			t.Cleanup(viper.Reset)
			if err := bridge.SaveProfileSecrets("default", &bridge.Secrets{AppId: "test-app", AppSecret: "test-secret"}); err != nil {
				t.Fatal(err)
			}
			body := "[bridge.feishu.group_settings]\nenabled = " + enabled + "\n[bridge.feishu.profiles.default.interaction]\nmode = \"singleton\"\n"
			if err := os.WriteFile(filepath.Join(defaultAmuxDir(), "config.toml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newFeishuGroupSettingsCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"show", "group-a"})
			err := cmd.Execute()
			if enabled == "false" {
				if err == nil || !strings.Contains(err.Error(), "实验已关闭") {
					t.Fatalf("disabled command = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("saved experiment did not enable command: %v", err)
			}
			var settings bridge.GroupSettings
			if err := json.Unmarshal(output.Bytes(), &settings); err != nil {
				t.Fatal(err)
			}
			if settings.Version != 0 || settings.InteractionMode != "singleton" {
				t.Fatalf("command ignored configured Bot defaults: %+v", settings)
			}
		})
	}
}

func TestConfigSetGroupSettingsExperiment(t *testing.T) {
	cfg := cliConfigFile{}
	key := "bridge.feishu.group_settings.enabled"
	for _, raw := range []string{"true", "false"} {
		if err := setConfigValue(cfg, key, raw); err != nil {
			t.Fatal(err)
		}
		value, ok := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
		if !ok || value != (raw == "true") {
			t.Fatalf("experiment must be a TOML boolean: %T %v", value, value)
		}
	}
	if err := setConfigValue(cfg, key, "invalid"); err == nil {
		t.Fatal("accepted invalid experiment toggle")
	}
	value, _ := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
	if value != false {
		t.Fatal("invalid toggle changed saved value")
	}
}

func TestConfigGroupWatchValidationUsesIndependentGroupPolicy(t *testing.T) {
	t.Setenv("AMUX_HOME", t.TempDir())
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("bridge.feishu.group_settings.enabled", true)
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "test-app", AppSecret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	settings := &bridge.GroupSettings{BridgeID: "feishu:test-app", ChatID: "oc_alarm", DefaultAgent: "builtin/codex", InteractionMode: "thread", Mode: "owner_only", ThinkingProcess: "hidden"}
	settings, err := bridge.SaveGroupSettingsInDB(bridge.DefaultDBPath(), "bot_a", settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cliConfigFile{}
	for key, value := range map[string]string{
		"bridge.feishu.profiles.bot_a.interaction.mode":     "singleton",
		"bridge.feishu.profiles.bot_a.group_watch.enabled":  "true",
		"bridge.feishu.profiles.bot_a.group_watch.chat_ids": "oc_alarm",
	} {
		if err := setConfigValue(cfg, key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, enabled := range []string{"true", "false"} {
		if err := setConfigValue(cfg, "bridge.feishu.group_settings.enabled", enabled); err != nil {
			t.Fatal(err)
		}
		if err := validateCLIGroupWatchInteractionCompatibility(cfg, "bridge.feishu.group_settings.enabled"); err != nil {
			t.Fatalf("valid group policy blocked experiment=%s: %v", enabled, err)
		}
	}
	settings.InteractionMode = "singleton"
	if _, err := bridge.SaveGroupSettingsInDB(bridge.DefaultDBPath(), "bot_a", settings, settings.Version); err != nil {
		t.Fatal(err)
	}
	if err := validateCLIGroupWatchInteractionCompatibility(cfg, ""); err != nil {
		t.Fatalf("paused saved group should not inherit Bot mode: %v", err)
	}
	if err := setConfigValue(cfg, "bridge.feishu.group_settings.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := validateCLIGroupWatchInteractionCompatibility(cfg, ""); err == nil {
		t.Fatal("accepted active singleton group watch")
	}
	if err := setConfigValue(cfg, "bridge.feishu.group_settings.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(cfg, "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_unconfigured"); err != nil {
		t.Fatal(err)
	}
	if err := validateCLIGroupWatchInteractionCompatibility(cfg, ""); err == nil {
		t.Fatal("unconfigured group ignored Bot singleton policy")
	}
}

func TestFeishuGroupSettingsDecodeRequiresCompleteSnapshot(t *testing.T) {
	original := bridge.GroupSettings{BridgeID: "feishu:test", ChatID: "group", InteractionMode: "thread", DefaultRuntimeRef: "codex", DefaultModelRef: "native-model", DefaultReasoningEffort: "high", Version: 7, DefaultArgs: []string{"--config", "name=a b"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeFeishuGroupSettings(strings.NewReader(string(data)))
	if err != nil || decoded.Version != 7 || decoded.DefaultModelRef != "native-model" || len(decoded.DefaultArgs) != 2 || decoded.DefaultArgs[1] != "name=a b" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for _, input := range []string{
		`{"bridge_id":"feishu:test","chat_id":"group","version":7}`,
		strings.Replace(string(data), `"version":7`, `"version":null`, 1),
		strings.TrimSuffix(string(data), "}") + `,"unknown":true}`,
		string(data) + ` {}`,
		strings.Replace(string(data), `"default_args":["--config","name=a b"]`, `"default_args":["--config",null]`, 1),
	} {
		if _, err := decodeFeishuGroupSettings(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid snapshot: %s", input)
		}
	}
}
