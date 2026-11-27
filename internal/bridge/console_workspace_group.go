package bridge

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const browserTopicAction = "browser.group.topic"

func (b *Bridge) browserGroupCard(ctx context.Context, openID, chatID string) (string, error) {
	if !b.isBotOwner(openID) {
		return "", status.Error(codes.PermissionDenied, "console_bot_owner_required")
	}
	group, err := b.consoleGroup(ctx, chatID)
	if err != nil {
		return "", err
	}
	public, paused, err := b.groupCommandStatus(ctx, chatID)
	if err != nil {
		return "", err
	}
	private, err := b.groupCommandManagement(ctx, openID, chatID, group.Name, paused)
	if err != nil {
		return "", err
	}
	var summary, editor cardDoc
	if err := json.Unmarshal([]byte(public), &summary); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(private), &editor); err != nil {
		return "", err
	}
	if !paused {
		summary.Body.Elements = append(summary.Body.Elements, managementDividerElement())
		editor.Body.Elements = append(summary.Body.Elements, editor.Body.Elements...)
	}
	bindings, err := b.db.ListWorkspaceBindingsByChatID(b.currentBridgeId(), chatID)
	if err != nil {
		return "", err
	}
	var options []cardOption
	for _, binding := range bindings {
		if binding.Status == ChatStatusActive && binding.RootMsgID != "" {
			title := binding.DisplayTitle
			if title == "" {
				title = binding.SessionName
			}
			options = append(options, cardOption{Text: managementPlainText(title), Value: binding.RootMsgID})
		}
	}
	if len(options) > 0 {
		selectTopic := managementSelectStaticElement("topic", managementCopy("workspace.topic"), "", options)
		selectTopic.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: browserTopicAction, Values: map[string]string{"chat_id": chatID}}))
		editor.Body.Elements = append(editor.Body.Elements, managementDividerElement(), selectTopic)
	}
	raw, err := json.Marshal(editor)
	return string(raw), err
}

func (b *Bridge) browserTopicCard(ctx context.Context, openID, chatID, root string) (string, error) {
	if !b.isBotOwner(openID) {
		return "", status.Error(codes.PermissionDenied, "console_bot_owner_required")
	}
	if _, err := b.consoleGroup(ctx, chatID); err != nil {
		return "", err
	}
	chat, err := b.db.FindByRootMsgContext(ctx, b.currentBridgeId(), root)
	if err != nil {
		return "", err
	}
	if chat == nil || chat.ChatID != chatID || chat.Status != ChatStatusActive {
		return "", status.Error(codes.NotFound, "console_topic_not_found")
	}
	return renderActiveTopicStatusCard(chat), nil
}
