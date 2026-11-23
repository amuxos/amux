package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/modelservice"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestToolActivityCardTitleShowsSharedPoolQuotaBilingual(t *testing.T) {
	started := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now := started.Add(10 * time.Second)
	meta := toolActivityCardMeta{
		AgentType:      "codex",
		EffectiveModel: "gpt-5",
		QuotaBorrowed:  true,
		Status:         "running",
		Now:            now,
		Activity: &agentui.ActivityView{
			StartedAt:      started,
			LastActivityAt: now,
		},
	}
	zh := toolActivityCardTitle(meta, i18n.Locale("zh-CN"))
	if !strings.Contains(zh, "额度提供：共享池") {
		t.Fatalf("zh title = %q, want 额度提供：共享池", zh)
	}
	en := toolActivityCardTitle(meta, i18n.Locale("en"))
	if !strings.Contains(en, "Quota: Shared Pool") {
		t.Fatalf("en title = %q, want Quota: Shared Pool", en)
	}

	meta.QuotaBorrowed = false
	if zhOwn := toolActivityCardTitle(meta, i18n.Locale("zh-CN")); strings.Contains(zhOwn, "共享池") {
		t.Fatalf("own quota zh title unexpectedly showed shared pool: %q", zhOwn)
	}
}

func TestSessionQuotaBorrowedResolvesSessionID(t *testing.T) {
	modelservice.SetSessionQuotaBorrowed("sess-uuid-42", true)
	defer modelservice.ClearSessionQuotaBorrowed("sess-uuid-42")

	hub := &bridgeTestHub{
		targets: []*pb.Target{{
			Id:   "local",
			Name: "local",
			Sessions: []*pb.Session{{
				Id:   "sess-uuid-42",
				Name: "work",
			}},
		}},
	}
	b := &Bridge{hub: hub}
	if !b.sessionQuotaBorrowed("local", "work") {
		t.Fatal("expected sessionQuotaBorrowed to resolve via session UUID")
	}
	if b.sessionQuotaBorrowed("local", "other") {
		t.Fatal("unexpected borrowed=true for other session")
	}
}
