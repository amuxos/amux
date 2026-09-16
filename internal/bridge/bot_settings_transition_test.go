package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
)

type contextBlockingManagementAgentPackActions struct {
	*fakeManagementAgentPackActions
}

func (a *contextBlockingManagementAgentPackActions) WithRegistryStableContext(ctx context.Context, _ func() error) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestApplyBotSettingsBoundsRegistryStableWaitByContext(t *testing.T) {
	b := &Bridge{managementPackActions: &contextBlockingManagementAgentPackActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := b.ApplyBotSettings(ctx, BotSettingsRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ApplyBotSettings error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("ApplyBotSettings elapsed = %s, want bounded registry wait", elapsed)
	}
}

func TestRoutingUsesDBModeInsteadOfLegacyConfig(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), config.FeishuInteractionThread)
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if state.InteractionMode != config.FeishuInteractionThread {
		state, err = b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, state.DefaultAgent)
		if err != nil {
			t.Fatalf("CommitThreadBotSettings: %v", err)
		}
	}
	if err := config.SetBridgeFeishuInteractionMode(b.ProfileName(), config.FeishuInteractionSingleton); err != nil {
		t.Fatalf("SetBridgeFeishuInteractionMode: %v", err)
	}

	mode, err := b.interactionMode()
	if err != nil {
		t.Fatalf("interactionMode: %v", err)
	}
	if mode != config.FeishuInteractionThread {
		t.Fatalf("mode = %q, want DB thread despite legacy config singleton", mode)
	}
}

func seedSingletonBotSettings(t *testing.T, b *Bridge, agent, sessionName string) BotProfileState {
	return seedSingletonBotSettingsWithCwd(t, b, agent, sessionName, "/repo/amux")
}

func seedSingletonBotSettingsWithCwd(t *testing.T, b *Bridge, agent, sessionName, cwd string) BotProfileState {
	t.Helper()
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), config.FeishuInteractionThread)
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	state, err = b.db.CommitSingletonBotSettings(b.currentBridgeId(), state.Version, agent, BotSession{
		BridgeId:     b.currentBridgeId(),
		Enabled:      true,
		TargetID:     "target-1",
		TargetName:   "local",
		SessionName:  sessionName,
		AgentProfile: agent,
		AgentType:    "codex",
		Cwd:          cwd,
	})
	if err != nil {
		t.Fatalf("CommitSingletonBotSettings: %v", err)
	}
	return state
}

func switchSingletonToThread(t *testing.T, b *Bridge, state BotProfileState) BotProfileState {
	t.Helper()
	result, err := b.ApplyBotSettings(context.Background(), BotSettingsRequest{
		Interaction: config.FeishuInteractionThread, DefaultAgent: state.DefaultAgent,
		ExpectedVersion: state.Version, RequestID: "switch-to-thread", OpenID: "owner-open",
	})
	if err != nil || result.Status != BotSettingsStatusSuccess {
		t.Fatalf("switch to thread: result=%+v err=%v", result, err)
	}
	return result.State
}
