package config_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func TestGroupWatchChatSettingsPersistIndependentlyAndRejectStaleWrites(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.bot_a.group_watch]
enabled = true
chat_ids = ["group_a", "group_b", "legacy"]
agent_profile_ref = "builtin/codex"
runtime_ref = "codex"
model_ref = "legacy-model"
max_active_sessions_per_chat = 50
idle_timeout_seconds = 10800
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)
	a := config.BridgeFeishuGroupWatchChatConfig{Enabled: true, ThinkingProcess: "hidden", MaxActiveSessionsPerChat: 3, IdleTimeoutSeconds: 120}
	b := config.BridgeFeishuGroupWatchChatConfig{Enabled: false, AgentProfileRef: "builtin/claude", RuntimeRef: "claude", ModelRef: "example", ThinkingProcess: "expanded", MaxActiveSessionsPerChat: 7, IdleTimeoutSeconds: 300}
	for chatID, value := range map[string]config.BridgeFeishuGroupWatchChatConfig{"group_a": a, "group_b": b} {
		if err := config.SetBridgeFeishuGroupWatchChat("bot_a", chatID, value, 0); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		cfg := config.GetBridgeFeishuGroupWatchForProfile("bot_a")
		gotA, gotB, legacy := cfg.ForChat("group_a"), cfg.ForChat("group_b"), cfg.ForChat("legacy")
		if !gotA.Enabled || gotA.AgentProfileRef != "" || gotA.RuntimeRef != "" || gotA.ModelRef != "" || gotA.MaxActiveSessionsPerChat != 3 || gotA.IdleTimeoutSeconds != 120 || gotA.ThinkingProcess != "hidden" {
			t.Fatalf("group A: %+v", gotA)
		}
		if gotB.Enabled || gotB.AgentProfileRef != "builtin/claude" || gotB.ModelRef != "example" || gotB.ThinkingProcess != "expanded" || gotB.IdleTimeoutSeconds != 300 {
			t.Fatalf("group B: %+v", gotB)
		}
		if legacy.ModelRef != "legacy-model" || legacy.MaxActiveSessionsPerChat != 50 || legacy.IdleTimeoutSeconds != 10800 {
			t.Fatalf("legacy group: %+v", legacy)
		}
		if len(config.GetBridgeFeishuGroupWatchForProfile("bot_b").ChatSettings) != 0 {
			t.Fatal("policy leaked to another Bot")
		}
	}
	check()
	viper.Reset()
	requireLoad(t, path)
	check()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SetBridgeFeishuGroupWatchChat("bot_a", "group_a", b, 0); err == nil {
		t.Fatal("stale update accepted")
	}
	bad := a
	bad.IdleTimeoutSeconds = 59
	if err := config.SetBridgeFeishuGroupWatchChat("bot_a", "group_a", bad, 1); err == nil {
		t.Fatal("invalid update accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected update changed the file")
	}
	// A global pause/resume keeps per-group state and tuples intact.
	cfg := config.GetBridgeFeishuGroupWatchForProfile("bot_a")
	cfg.Enabled = false
	if err := config.SetBridgeFeishuGroupWatch("bot_a", cfg); err != nil {
		t.Fatal(err)
	}
	if config.GetBridgeFeishuGroupWatchForProfile("bot_a").ForChat("group_a").Enabled {
		t.Fatal("group bypassed global pause")
	}
	cfg.Enabled = true
	if err := config.SetBridgeFeishuGroupWatch("bot_a", cfg); err != nil {
		t.Fatal(err)
	}
	check()
}
