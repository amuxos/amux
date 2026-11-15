package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/rs/zerolog/log"
)

const (
	externalBotCatalogRefreshInterval = 5 * time.Minute
	externalBotCatalogPageSize        = "100"
	externalBotCatalogMaxPages        = 100
	externalBotCatalogScope           = "im:chat.members:read"
)

type externalBotCatalogMember struct {
	BotOpenID   string
	BotAppID    string
	DisplayName string
}

type externalBotCatalogSnapshot struct {
	ChatDisplayName string
	Members         []externalBotCatalogMember
	Complete        bool
}

type externalBotCatalogResponse struct {
	larkcore.CodeError
	Data *struct {
		Bots []struct {
			MemberID string `json:"member_id"`
			AppID    string `json:"app_id"`
			Name     string `json:"name"`
		} `json:"bots"`
		BotTotal    int               `json:"bot_total"`
		HasMore     bool              `json:"has_more"`
		PageToken   string            `json:"page_token"`
		Truncations []json.RawMessage `json:"truncations"`
	} `json:"data"`
}

func (b *Bridge) runExternalBotCatalogRefresher(ctx context.Context) {
	b.refreshExternalBotCatalogBestEffort(ctx)
	ticker := time.NewTicker(externalBotCatalogRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshExternalBotCatalogBestEffort(ctx)
		}
	}
}

func (b *Bridge) refreshExternalBotCatalogBestEffort(ctx context.Context) {
	if !externalBotCatalogEnabled(b) {
		return
	}
	if err := b.refreshKnownExternalBotCatalog(ctx, false); err != nil && ctx.Err() == nil {
		b.observeCapabilityPermissionError(ctx, "bot_to_bot", "智能体协作", err, true)
		log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: external Bot catalog refresh failed")
	}
}

func (b *Bridge) refreshExternalBotCatalogChatBestEffort(ctx context.Context, chatID string) {
	if !externalBotCatalogEnabled(b) {
		return
	}
	if err := b.refreshExternalBotCatalogChat(ctx, chatID, false); err != nil && ctx.Err() == nil {
		b.observeCapabilityPermissionError(ctx, "bot_to_bot", "智能体协作", err, true)
		log.Warn().Err(err).Str("profile", b.ProfileName()).Str("chat_id", strings.TrimSpace(chatID)).Msg("bridge: current-group external Bot catalog refresh failed")
	}
}

func (b *Bridge) refreshKnownExternalBotCatalog(ctx context.Context, force bool) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("bridge db is required")
	}
	chatIDs, err := b.db.ListGroupWorkspaceChatIDs(b.currentBridgeId())
	if err != nil {
		return fmt.Errorf("list known Feishu groups: %w", err)
	}
	var errs []error
	for _, chatID := range chatIDs {
		if err := b.refreshExternalBotCatalogChat(ctx, chatID, force); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (b *Bridge) refreshExternalBotCatalogChat(ctx context.Context, chatID string, force bool) error {
	if !externalBotCatalogEnabled(b) {
		return fmt.Errorf("Feishu Bot-to-Bot collaboration is disabled")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("chat_id is required")
	}
	b.externalBotCatalogMu.Lock()
	defer b.externalBotCatalogMu.Unlock()
	if !force && b.externalBotCatalogSeen != nil && time.Since(b.externalBotCatalogSeen[chatID]) < externalBotCatalogRefreshInterval {
		return nil
	}
	fetch := b.externalBotCatalogFetch
	if fetch == nil {
		fetch = b.fetchExternalBotCatalog
	}
	snapshot, err := fetch(ctx, chatID)
	if err != nil {
		return fmt.Errorf("refresh Feishu Bot catalog for %s: %w", chatID, err)
	}
	when := time.Now().UTC()
	peers := make([]externalBotPeer, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		botOpenID := strings.TrimSpace(member.BotOpenID)
		botAppID := strings.TrimSpace(member.BotAppID)
		if botOpenID == "" || (b.secrets != nil && (botOpenID == strings.TrimSpace(b.secrets.BotOpenId) || (botAppID != "" && botAppID == strings.TrimSpace(b.secrets.AppId)))) {
			continue
		}
		peers = append(peers, externalBotPeer{
			CollaboratorID:  externalBotCollaboratorID(b.currentBridgeId(), chatID, botOpenID),
			BridgeID:        b.currentBridgeId(),
			ChatID:          chatID,
			BotOpenID:       botOpenID,
			BotAppID:        botAppID,
			BotDisplayName:  strings.TrimSpace(member.DisplayName),
			ChatDisplayName: strings.TrimSpace(snapshot.ChatDisplayName),
			LastSeenAt:      when,
		})
	}
	if err := b.db.SyncExternalBotPeers(b.currentBridgeId(), chatID, peers, snapshot.Complete); err != nil {
		return fmt.Errorf("persist Feishu Bot catalog for %s: %w", chatID, err)
	}
	b.resolveCapabilityPermissionScopes("bot_to_bot", externalBotCatalogScope)
	if b.externalBotCatalogSeen == nil {
		b.externalBotCatalogSeen = make(map[string]time.Time)
	}
	b.externalBotCatalogSeen[chatID] = when
	return nil
}

func (b *Bridge) fetchExternalBotCatalog(ctx context.Context, chatID string) (externalBotCatalogSnapshot, error) {
	if b == nil || b.client == nil {
		return externalBotCatalogSnapshot{}, fmt.Errorf("nil lark client")
	}
	chatName, err := b.fetchChatName(ctx, chatID)
	if err != nil {
		return externalBotCatalogSnapshot{}, err
	}
	b.rememberChatName(ctx, chatID, chatName)
	result := externalBotCatalogSnapshot{ChatDisplayName: chatName, Complete: true}
	pageToken := ""
	seenTokens := map[string]bool{}
	seenBots := map[string]bool{}
	botTotal := 0
	for page := 0; page < externalBotCatalogMaxPages; page++ {
		query := larkcore.QueryParams{}
		query.Set("member_types", "bot")
		query.Set("member_id_type", "open_id")
		query.Set("page_size", externalBotCatalogPageSize)
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		apiResp, err := b.client.Do(ctx, &larkcore.ApiReq{
			HttpMethod:                http.MethodGet,
			ApiPath:                   "/open-apis/im/v1/chats/:chat_id/members/list",
			PathParams:                larkcore.PathParams{"chat_id": chatID},
			QueryParams:               query,
			SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
		})
		if err != nil {
			return externalBotCatalogSnapshot{}, fmt.Errorf("lark list chat Bots: %w", err)
		}
		var resp externalBotCatalogResponse
		if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
			return externalBotCatalogSnapshot{}, fmt.Errorf("decode lark chat Bot list: %w", err)
		}
		if resp.Code != 0 {
			return externalBotCatalogSnapshot{}, feishuAPIResponseErrorForRequiredScopes(
				"lark list chat Bots", b.secretsAppID(), resp.CodeError, externalBotCatalogScope,
			)
		}
		if resp.Data == nil {
			return externalBotCatalogSnapshot{}, fmt.Errorf("lark list chat Bots: missing data")
		}
		if len(resp.Data.Truncations) > 0 {
			result.Complete = false
		}
		if resp.Data.BotTotal > botTotal {
			botTotal = resp.Data.BotTotal
		}
		for _, bot := range resp.Data.Bots {
			botID := strings.TrimSpace(bot.MemberID)
			if botID == "" || seenBots[botID] {
				continue
			}
			seenBots[botID] = true
			result.Members = append(result.Members, externalBotCatalogMember{BotOpenID: botID, BotAppID: bot.AppID, DisplayName: bot.Name})
		}
		if !resp.Data.HasMore {
			if botTotal > len(seenBots) {
				result.Complete = false
			}
			return result, nil
		}
		next := strings.TrimSpace(resp.Data.PageToken)
		if next == "" || seenTokens[next] {
			return externalBotCatalogSnapshot{}, fmt.Errorf("lark list chat Bots: invalid repeated page token")
		}
		seenTokens[next] = true
		pageToken = next
	}
	return externalBotCatalogSnapshot{}, fmt.Errorf("lark list chat Bots: exceeded %d pages", externalBotCatalogMaxPages)
}

func externalBotCatalogEnabled(b *Bridge) bool {
	return b != nil && b.botToBotSnapshot().Enabled
}
