package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
)

func TestDiscoveryAllExamplesReachAgentWithCapabilityContext(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		for _, c := range discoveryCatalog {
			for example := range 2 {
				t.Run(fmt.Sprintf("%s/%s/%d", locale, c.ID, example), func(t *testing.T) {
					loadBridgeTestConfig(t, "[bridge.feishu.profiles.default.interaction]\nmode = \"thread\"\n")
					b, cards := guideTestBridge(t)
					if err := b.db.setManagementLocale(b.currentBridgeId(), "owner", string(locale)); err != nil {
						t.Fatal(err)
					}
					h := b.hub.(*bridgeTestHub)
					h.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
					setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
					b.chatNotice = func(context.Context, string, string) (string, string, error) { return "request-root", "", nil }
					b.threadNotice = func(context.Context, string, string) error { return nil }
					b.reactionAdd = func(context.Context, string, string) error { return nil }
					prompt := c.text(fmt.Sprintf("prompt.%d", example)).For(locale)
					b.dispatchDiscoveryPrompt("owner", "chat", "guide-card", c, prompt)
					if len(h.routes) == 0 || h.routes[len(h.routes)-1].method != "Submit" {
						t.Fatalf("example did not reach normal Agent submission: %v", h.routes)
					}
					text := h.routes[len(h.routes)-1].payload.(*pb.SubmitRequest).Text
					for _, expected := range []string{prompt, c.text("title").For(locale), c.text("requires").For(locale), c.text("guidance").For(locale), "experiments_running", "new_session_defaults", "not_checked"} {
						if !strings.Contains(text, expected) {
							t.Fatalf("Agent input is missing capability context %q", expected)
						}
					}
					if strings.Contains(text, "discovery.") || strings.Contains(text, "%!") {
						t.Fatal("unresolved guidance translation")
					}
					select {
					case <-cards:
					case <-time.After(2 * time.Second):
						t.Fatal("missing submission receipt")
					}
				})
			}
		}
	}
}

func TestDiscoveryContextPreservesFormalCommands(t *testing.T) {
	b, _ := guideTestBridge(t)
	for _, c := range discoveryCatalog {
		for _, text := range []string{"goal", " goal ", "goal pause", "goal resume", "goal clear", "goal budget 1000", "goal finish the audit", "goal set replacement", "/amux", "/help"} {
			if actual := b.discoveryTaskPrompt(c, "owner", text); actual != text {
				t.Fatalf("%s changed formal command %q into %q", c.ID, text, actual)
			}
		}
	}
}

func TestDiscoveryContextStaysOutOfUserFacingEchoes(t *testing.T) {
	b, _ := guideTestBridge(t)
	for _, locale := range []string{"en", "zh-CN"} {
		if err := b.db.setManagementLocale(b.currentBridgeId(), "owner", locale); err != nil {
			t.Fatal(err)
		}
		for _, c := range discoveryCatalog {
			prompt := "Please explain @assistant /amux"
			input := b.discoveryTaskPrompt(c, "owner", prompt)
			if actual := stripAmuxGuidance(input); actual != prompt {
				t.Fatalf("%s/%s exposed context or lost user text: %q", locale, c.ID, actual)
			}
			if displayTitleFromRequest(input) != prompt || strings.Contains(queueListLine("1", "lark", input), "amux-capability-context") {
				t.Fatal("internal capability guidance leaked into a conversation title or queue")
			}
		}
	}
}

func TestDiscoverySnapshotSeparatesSavedRunningAndOmitsSensitiveState(t *testing.T) {
	loadBridgeTestConfig(t, "[memory]\nenabled = false\n")
	b, _ := guideTestBridge(t)
	b.secrets.OpenId = "private-owner-sentinel"
	b.managementSystemInfo.HubIP = "private-host-sentinel"
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{ChatIDs: []string{"private-group-sentinel"}}
	saved := config.GetExperimentsSettings()
	saved.MemoryEnabled = true
	saved.MentionTurnInitiator = true
	if err := config.SaveExperimentsSettings(saved); err != nil {
		t.Fatal(err)
	}
	snapshot := b.discoverySnapshot()
	if snapshot["experiments_saved_status"] != "ok" ||
		snapshot["experiments_saved"].(map[string]any)["memory"] != true ||
		snapshot["experiments_running"].(map[string]any)["memory"] != false ||
		snapshot["experiments_saved"].(map[string]any)["mention_turn_initiator"] != true ||
		snapshot["experiments_running"].(map[string]any)["mention_turn_initiator"] != false {
		t.Fatal("saved preferences were confused with active settings", snapshot)
	}
	if snapshot["full_system_health"] != "not_checked" || snapshot["current_work_session_goal_support"] != "unknown" {
		t.Fatal("unobserved health or goal capability was asserted")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-owner-sentinel", "private-host-sentinel", "private-group-sentinel", "ActorToken", "AppSecret", "Provider"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("snapshot leaked sensitive state", forbidden)
		}
	}
	if err := os.WriteFile(viper.ConfigFileUsed(), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot = b.discoverySnapshot()
	if snapshot["experiments_saved_status"] != "unavailable" || snapshot["experiments_saved"] != nil {
		t.Fatal("failed saved-state read was presented as a default value")
	}
}
