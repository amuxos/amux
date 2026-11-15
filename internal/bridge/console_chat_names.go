package bridge

import (
	"context"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Group names are remembered whenever the Bridge resolves them through Feishu,
// so surfaces that must not call Feishu while reading, such as the console's
// group list, can still show them. An operator refresh fills in the rest.
const (
	consoleChatNameStaleAfter  = 24 * time.Hour
	consoleChatNameListPages   = 20
	consoleChatNameLookupLimit = 50
	consoleChatNameMaxLength   = 256
)

func (b *bridgeDB) migrateChatNames() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_chat_names (
			bridge_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bridge_id, chat_id)
		)
	`)
	return err
}

type chatNameRecord struct {
	Name      string
	UpdatedAt int64
}

// SaveChatName records the latest known display name of a group.
func (b *bridgeDB) SaveChatName(ctx context.Context, bridgeID, chatID, name string, at time.Time) error {
	name = strings.TrimSpace(name)
	if bridgeID == "" || chatID == "" || name == "" {
		return nil
	}
	if runes := []rune(name); len(runes) > consoleChatNameMaxLength {
		name = string(runes[:consoleChatNameMaxLength])
	}
	_, err := b.db.ExecContext(ctx, `INSERT INTO feishu_chat_names(bridge_id, chat_id, name, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(bridge_id, chat_id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`, bridgeID, chatID, name, at.Unix())
	return err
}

// ChatNames returns the remembered group names of one Bridge.
func (b *bridgeDB) ChatNames(ctx context.Context, bridgeID string) (map[string]chatNameRecord, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT chat_id, name, updated_at FROM feishu_chat_names WHERE bridge_id=?`, bridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]chatNameRecord{}
	for rows.Next() {
		var chatID string
		var record chatNameRecord
		if err := rows.Scan(&chatID, &record.Name, &record.UpdatedAt); err != nil {
			return nil, err
		}
		out[chatID] = record
	}
	return out, rows.Err()
}

// lookupChatName resolves a group name through Feishu and remembers it.
func (b *Bridge) lookupChatName(ctx context.Context, chatID string) (string, error) {
	lookup := b.chatNameLookup
	if lookup == nil {
		lookup = b.fetchChatName
	}
	name, err := lookup(ctx, chatID)
	if err == nil {
		b.rememberChatName(ctx, chatID, name)
	}
	return name, err
}

func (b *Bridge) rememberChatName(ctx context.Context, chatID, name string) {
	if b == nil || b.db == nil || b.currentBridgeId() == "" {
		return
	}
	if err := b.db.SaveChatName(ctx, b.currentBridgeId(), strings.TrimSpace(chatID), name, time.Now()); err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("bridge: remember chat name")
	}
}

// listBotChatNames pages through the groups the Bot belongs to. One listing
// names many groups at once, instead of one Feishu request per group.
func (b *Bridge) listBotChatNames(ctx context.Context) (map[string]string, error) {
	if b.chatListLookup != nil {
		return b.chatListLookup(ctx)
	}
	if b.client == nil {
		return nil, status.Error(codes.Unavailable, "console_feishu_unavailable")
	}
	out := map[string]string{}
	token := ""
	for page := 0; page < consoleChatNameListPages; page++ {
		builder := larkim.NewListChatReqBuilder().PageSize(100)
		if token != "" {
			builder = builder.PageToken(token)
		}
		resp, err := b.client.Im.Chat.List(ctx, builder.Build())
		if err != nil {
			return out, err
		}
		if !resp.Success() || resp.Data == nil {
			return out, status.Error(codes.Unavailable, "console_feishu_unavailable")
		}
		for _, item := range resp.Data.Items {
			if item == nil {
				continue
			}
			if id, name := strings.TrimSpace(stringPtr(item.ChatId)), strings.TrimSpace(stringPtr(item.Name)); id != "" && name != "" {
				out[id] = name
			}
		}
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || stringPtr(resp.Data.PageToken) == "" {
			break
		}
		token = stringPtr(resp.Data.PageToken)
	}
	return out, nil
}

// ConsoleRefreshGroupNames is an explicit operator action: it asks Feishu for
// the names of known groups whose name is missing or stale and remembers them.
// Reads never do this. A group the Bot has left keeps its chat ID.
func (b *Bridge) ConsoleRefreshGroupNames(ctx context.Context) (ConsoleGroupsResult, error) {
	groups, err := b.ConsoleGroups(ctx)
	if err != nil {
		return ConsoleGroupsResult{}, err
	}
	stale := time.Now().Add(-consoleChatNameStaleAfter).Unix()
	pending := map[string]bool{}
	for _, group := range groups {
		if group.NameUpdatedAt == 0 || group.NameUpdatedAt < stale {
			pending[group.ChatID] = true
		}
	}
	resolved, missing := 0, 0
	if len(pending) > 0 {
		listed, listErr := b.listBotChatNames(ctx)
		if listErr != nil {
			log.Warn().Err(listErr).Msg("bridge: list chats for console names")
		}
		for chatID := range pending {
			if name := listed[chatID]; name != "" {
				b.rememberChatName(ctx, chatID, name)
				delete(pending, chatID)
				resolved++
			}
		}
		// The listing covers groups the Bot is in. Ask for the rest one by
		// one, within a bound, so a long tail cannot hold the request.
		looked := 0
		for chatID := range pending {
			if looked >= consoleChatNameLookupLimit || ctx.Err() != nil {
				break
			}
			looked++
			if _, err := b.lookupChatName(ctx, chatID); err == nil {
				delete(pending, chatID)
				resolved++
			}
		}
		missing = len(pending)
	}
	if ctx.Err() != nil {
		return ConsoleGroupsResult{}, status.FromContextError(ctx.Err()).Err()
	}
	groups, err = b.ConsoleGroups(ctx)
	if err != nil {
		return ConsoleGroupsResult{}, err
	}
	return ConsoleGroupsResult{Status: BotSettingsStatusSuccess, Groups: groups, Profile: b.ProfileName(), Source: "known_bindings",
		Actions: b.consoleGroupListActions(), NamesResolved: resolved, NamesMissing: missing}, nil
}

func chatNameFromRecord(records map[string]chatNameRecord, chatID string) (string, int64) {
	if record, ok := records[chatID]; ok {
		return record.Name, record.UpdatedAt
	}
	return "", 0
}
