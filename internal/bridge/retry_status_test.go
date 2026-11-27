package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
)

func TestToolActivityCardTitleShowsRetryStatusBilingual(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now := started.Add(14 * time.Second)
	meta := toolActivityCardMeta{
		AgentType:      "codex",
		EffectiveModel: "gpt-5",
		Status:         "running",
		Now:            now,
		Activity: &agentui.ActivityView{
			StartedAt:      started,
			LastActivityAt: now,
			RetryCount:     2,
			RetryCode:      "rate_limit_retry",
		},
	}
	zh := toolActivityCardTitle(meta, i18n.Locale("zh-CN"))
	if !strings.Contains(zh, "上游限流重试中") || !strings.Contains(zh, "2") {
		t.Fatalf("zh retry title = %q, want rate limit retry wording", zh)
	}
	en := toolActivityCardTitle(meta, i18n.Locale("en"))
	if !strings.Contains(en, "Upstream rate-limited, retrying") || !strings.Contains(en, "#2") {
		t.Fatalf("en retry title = %q, want rate limit retry wording", en)
	}

	meta.Activity.RetryCode = "provider_retry"
	zhProvider := toolActivityCardTitle(meta, i18n.Locale("zh-CN"))
	if !strings.Contains(zhProvider, "上游繁忙/排队重试中") {
		t.Fatalf("zh provider retry title = %q", zhProvider)
	}
	enProvider := toolActivityCardTitle(meta, i18n.Locale("en"))
	if !strings.Contains(enProvider, "Upstream busy or queued, retrying") {
		t.Fatalf("en provider retry title = %q", enProvider)
	}
}
