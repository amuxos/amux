package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
)

func TestManagementExperimentsSteerForegroundAppliesLive(t *testing.T) {
	loadBridgeTestConfig(t, "[memory]\nenabled = false\n")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	cards := make(chan string, 5)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		cards <- content
		return nil
	}
	// Open: default off, item and current state rendered.
	resp, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionExperimentsOpen, nil))
	if err != nil || resp.Toast.Type != "success" {
		t.Fatalf("open = %+v %v", resp, err)
	}
	select {
	case card := <-cards:
		for _, want := range []string{"前台/后台分工与中途注入（实验）", "steer_foreground_enabled", "当前状态：已关闭", "无需重启"} {
			if !strings.Contains(card, want) {
				t.Fatalf("card missing %q: %s", want, card)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no card")
	}

	chat := &LarkChat{BridgeId: "b", RootMsgID: "root", TargetID: "t", SessionName: "s"}
	if b.busyInputSteers() || b.sessionSystemInstructions(chat) != "" {
		t.Fatal("steer must be off by default")
	}

	event := settingsCallback("system.experiments.save_restart", nil)
	event.Event.Operator.OpenID = "not-owner"
	event.Event.Action.FormValue = map[string]interface{}{"completion_enabled": "false", "steer_foreground_enabled": "true"}
	resp, _, _ = b.handleManagementCardAction(context.Background(), event)
	if resp.Toast.Type != "error" || b.steerForeground.Load() {
		t.Fatalf("non-owner toggled steer: %+v", resp)
	}

	event.Event.Operator.OpenID = "owner-open"
	resp, _, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || resp.Toast.Type != "success" {
		t.Fatalf("owner save = %+v %v", resp, err)
	}
	<-cards
	saved, err := config.ReadExperimentsSettings()
	if err != nil || !saved.SteerForeground {
		t.Fatalf("not persisted: %+v %v", saved, err)
	}
	if !b.busyInputSteers() || b.sessionSystemInstructions(chat) == "" {
		t.Fatal("steer not applied live")
	}
	if b.sessionSystemInstructions(chat) == "" {
		t.Fatal("every submit must carry the session instructions")
	}
	b.handleManagementCardAction(context.Background(), settingsCallback(managementActionExperimentsOpen, nil))
	card := <-cards
	if !strings.Contains(card, "当前状态：已开启") {
		t.Fatalf("card not showing on: %s", card)
	}

	// Off then on again applies live on the next submit.
	event.Event.Action.FormValue["steer_foreground_enabled"] = "false"
	b.handleManagementCardAction(context.Background(), event)
	<-cards
	if b.busyInputSteers() || b.sessionSystemInstructions(chat) != "" {
		t.Fatal("steer not disabled live")
	}
	event.Event.Action.FormValue["steer_foreground_enabled"] = "true"
	b.handleManagementCardAction(context.Background(), event)
	<-cards
	if b.sessionSystemInstructions(chat) == "" {
		t.Fatal("re-enable must apply on the next submit")
	}

	event.Event.Action.FormValue["steer_foreground_enabled"] = "maybe"
	resp, _, _ = b.handleManagementCardAction(context.Background(), event)
	if resp.Toast.Type != "error" || !b.steerForeground.Load() {
		t.Fatalf("invalid value accepted: %+v", resp)
	}
	// Older cards that omit the field keep the saved value.
	delete(event.Event.Action.FormValue, "steer_foreground_enabled")
	b.handleManagementCardAction(context.Background(), event)
	if saved, _ := config.ReadExperimentsSettings(); !saved.SteerForeground || !b.steerForeground.Load() {
		t.Fatal("omitted field changed steer")
	}
}
