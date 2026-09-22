package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (b *Bridge) prepareGroupWatchAlertDedup(ctx context.Context, cfg config.BridgeFeishuGroupWatchConfig, msg groupWatchMessage) (*groupWatchAlertDedup, error) {
	if !cfg.DedupEnabled || normalizeGroupWatchSenderType(msg.SenderType) != "app" {
		return nil, nil
	}
	switch msg.Kind {
	case "image", "file", "audio", "media", "merge_forward":
		// Material placeholders do not identify an alert's contents.
		return nil, nil
	}
	if msg.Content != nil {
		visible := strings.TrimSpace(extractGroupWatchText(msg.Kind, msg.Content))
		if visible == "" {
			return nil, nil
		}
		msg.Text = visible
	}
	fingerprint := groupWatchAlertFingerprint(msg)
	if fingerprint == "" {
		return nil, nil
	}
	senderID := strings.TrimSpace(msg.SenderID)
	// Both event open IDs and history app IDs must map through the same catalog.
	// Do not fall back to raw IDs: that silently splits one app into two senders.
	fetch := b.externalBotCatalogFetch
	if fetch == nil {
		fetch = b.fetchExternalBotCatalog
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := fetch(lookupCtx, msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("group watch resolve sending app: %w", err)
	}
	if !snapshot.Complete {
		return nil, fmt.Errorf("group watch resolve sending app: incomplete bot list")
	}
	for _, member := range snapshot.Members {
		appID := strings.TrimSpace(member.BotAppID)
		if appID != "" && (senderID == appID || senderID == strings.TrimSpace(member.BotOpenID)) {
			window := cfg.DedupWindowSeconds
			if window < 60 || window > config.MaxFeishuGroupWatchDedupWindowSeconds {
				window = config.DefaultFeishuGroupWatchDedupWindowSeconds
			}
			b.resolveCapabilityPermissionScopes(capabilityGroupWatch, externalBotCatalogScope)
			return &groupWatchAlertDedup{BridgeID: b.currentBridgeId(), ChatID: msg.ChatID, SenderID: appID, Fingerprint: fingerprint, Window: time.Duration(window) * time.Second}, nil
		}
	}
	return nil, fmt.Errorf("group watch resolve sending app: sender absent from bot list")
}

func (b *Bridge) claimGroupWatchAlert(ctx context.Context, a *groupWatchAlertDedup, messageID, targetID, sessionName string) (bool, error) {
	now := time.Now()
	pending, err := b.db.PendingGroupWatchAlert(*a)
	if err != nil {
		return false, err
	}

	if pending != nil {
		submitted, receiptErr := b.db.GroupWatchAlertSubmitted(*a, pending)
		if receiptErr != nil {
			return false, receiptErr
		}
		if submitted {
			acceptedAt := pending.SubmissionStartedAt
			if acceptedAt.IsZero() {
				acceptedAt = now
			}
			if err := b.db.FinishGroupWatchAlert(*a, pending.MessageID, true, acceptedAt); err != nil {
				return false, err
			}
		} else if !pending.SubmissionStartedAt.IsZero() {
			// Losing a Submit response is not proof that the Runtime did no work.
			// Hold this window instead of replaying the possibly accepted input.
			if now.Before(pending.SubmissionStartedAt.Add(a.Window)) {
				return false, b.db.ObserveGroupWatchDuplicate(*a, messageID, now)
			}
			if err := b.db.FinishGroupWatchAlert(*a, pending.MessageID, false, now); err != nil {
				return false, err
			}
		} else {
			if now.Sub(pending.StartedAt) < time.Minute {
				return false, b.db.ObserveGroupWatchDuplicate(*a, messageID, now)
			}
			existing, err := b.groupWatchSession(ctx, pending.TargetID, pending.SessionName)
			if err != nil {
				return false, fmt.Errorf("resolve pending group watch launch: %w", err)
			}
			if existing != nil {
				owned, err := b.db.ResumeGroupWatchAlert(*a, pending, now)
				if err != nil || !owned {
					return false, err
				}
				a.Resume, a.ResumeClaim = existing, pending
				return true, nil
			}
			if err := b.db.FinishGroupWatchAlert(*a, pending.MessageID, false, now); err != nil {
				return false, err
			}
		}
	}

	claimed, err := b.db.ReserveGroupWatchAlert(*a, messageID, targetID, sessionName, now)
	if err != nil || claimed {
		return claimed, err
	}
	return false, b.db.ObserveGroupWatchDuplicate(*a, messageID, now)
}

// A failed probe is not evidence that Run failed. Retain its durable claim so
// the next alert can reconcile it when the Runtime becomes reachable again.
func (b *Bridge) groupWatchSession(ctx context.Context, targetID, sessionName string) (*pb.Session, error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	resp, err := b.hub.Route(probeCtx, targetID, "List", &pb.ListRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("empty session list response")
	}
	var list pb.ListResponse
	if err = json.Unmarshal(resp.Payload, &list); err != nil {
		return nil, err
	}
	for _, s := range list.Sessions {
		if s != nil && s.Name == sessionName {
			switch s.State {
			case "running", "idle", "sleeping":
				return s, nil
			case "archived", "failed":
				return nil, nil
			default:
				return nil, fmt.Errorf("group watch launch outcome is unresolved (session state %s)", s.State)
			}
		}
	}
	return nil, nil
}
