package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type groupWatchMention struct {
	openID string
	kind   string
}

func groupWatchEventMentions(mentions []*larkim.MentionEvent) []groupWatchMention {
	var targets []groupWatchMention
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		target := groupWatchMention{kind: strings.ToLower(strings.TrimSpace(stringPtr(mention.MentionedType)))}
		if mention.Id != nil {
			target.openID = strings.TrimSpace(stringPtr(mention.Id.OpenId))
		}
		targets = append(targets, target)
	}
	return targets
}

func groupWatchHistoryMentions(mentions []*larkim.Mention) []groupWatchMention {
	var targets []groupWatchMention
	for _, mention := range mentions {
		if mention != nil {
			targets = append(targets, groupWatchMention{openID: strings.TrimSpace(stringPtr(mention.Id))})
		}
	}
	return targets
}

// A poll creates one checker per chat. It lazily fetches a complete bot list
// only for untyped mentions and shares that result (including errors) across
// the batch. Never interpret a partial/failed list as proof that a target is
// human, or require Bot-to-Bot to be enabled for ordinary on-call alerts.
func (b *Bridge) newGroupWatchBotMentionCheck(ctx context.Context, chatID string) func([]groupWatchMention) (bool, error) {
	var loaded bool
	var lookupErr error
	var botIDs map[string]bool
	return func(targets []groupWatchMention) (bool, error) {
		var unknown []string
		for _, target := range targets {
			switch target.kind {
			case "bot", "app":
				return true, nil
			case "user":
				continue
			default:
				unknown = append(unknown, target.openID)
			}
		}
		if len(unknown) == 0 {
			return false, nil
		}
		for _, id := range unknown {
			if id == "" {
				return false, fmt.Errorf("group_watch mention is missing a target open_id")
			}
		}
		if !loaded {
			loaded = true
			fetch := b.externalBotCatalogFetch
			if fetch == nil {
				fetch = b.fetchExternalBotCatalog
			}
			lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			snapshot, err := fetch(lookupCtx, chatID)
			cancel()
			if err != nil {
				lookupErr = fmt.Errorf("group_watch resolve mention targets: %w", err)
			} else if !snapshot.Complete {
				lookupErr = fmt.Errorf("group_watch resolve mention targets: incomplete chat bot list")
			} else {
				botIDs = make(map[string]bool, len(snapshot.Members))
				for _, member := range snapshot.Members {
					botIDs[strings.TrimSpace(member.BotOpenID)] = true
				}
				b.resolveCapabilityPermissionScopes(capabilityGroupWatch, externalBotCatalogScope)
			}
		}
		if lookupErr != nil {
			return false, lookupErr
		}
		for _, id := range unknown {
			if botIDs[id] {
				return true, nil
			}
		}
		return false, nil
	}
}
