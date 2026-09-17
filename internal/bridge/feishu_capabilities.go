package bridge

import (
	"sort"

	"github.com/amuxos/amux/internal/config"
)

type feishuCapabilityActivation int

const capabilityCoreMessaging = "core_messaging"

const (
	feishuCapabilityAlways feishuCapabilityActivation = iota
	feishuCapabilityBotToBot
	feishuCapabilityGroupWatch
	feishuCapabilityDocumentComments
)

type feishuCapabilitySpec struct {
	ID           string
	Name         string
	Activation   feishuCapabilityActivation
	TenantScopes []string
	UserScopes   []string
	TenantEvents []string
	UserEvents   []string
	Callbacks    []string
}

// feishuCapabilitySpecs is the shared declaration used both by setup and by
// the runtime permission audit. Setup takes the union; runtime selects only
// capabilities active for the current Bot profile.
var feishuCapabilitySpecs = []feishuCapabilitySpec{
	{
		ID:         capabilityCoreMessaging,
		Name:       "基础消息",
		Activation: feishuCapabilityAlways,
		TenantScopes: []string{
			"cardkit:card:write",
			"im:message:send_as_bot",
			"im:message.group_at_msg:readonly",
			"im:message.p2p_msg:readonly",
			"im:message:readonly",
			"im:message.reactions:write_only",
			"im:chat:read",
		},
		TenantEvents: []string{
			"im.message.receive_v1",
			"im.chat.access_event.bot_p2p_chat_entered_v1",
		},
		Callbacks: []string{"card.action.trigger"},
	},
	{
		ID:         "native_commands",
		Name:       "原生命令",
		Activation: feishuCapabilityAlways,
		TenantScopes: []string{
			"application:app_slash_command:read",
			"application:app_slash_command:write",
		},
		TenantEvents: []string{"application.bot.menu_v6"},
	},
	{
		ID:         "bot_to_bot",
		Name:       "智能体协作",
		Activation: feishuCapabilityBotToBot,
		TenantScopes: []string{
			"application:bot.basic_info:read",
			"im:chat.members:read",
			"im:message.group_at_msg.include_bot:readonly",
		},
	},
	{
		ID:           capabilityGroupWatch,
		Name:         "群值守",
		Activation:   feishuCapabilityGroupWatch,
		TenantScopes: []string{"im:message.group_msg", "im:chat.members:read"},
	},
	{
		ID:         capabilityDocumentComments,
		Name:       "云文档评论",
		Activation: feishuCapabilityDocumentComments,
		TenantScopes: []string{
			"docs:document.comment:create",
			"docs:document.comment:read",
			"docs:event:subscribe",
			"docx:document:readonly",
		},
		TenantEvents: []string{"drive.notice.comment_add_v1"},
	},
}

func allFeishuCapabilityManifest() FeishuAppManifest {
	manifest := FeishuAppManifest{Preset: false}
	for _, capability := range feishuCapabilitySpecs {
		manifest.TenantScopes = append(manifest.TenantScopes, capability.TenantScopes...)
		manifest.UserScopes = append(manifest.UserScopes, capability.UserScopes...)
		manifest.TenantEvents = append(manifest.TenantEvents, capability.TenantEvents...)
		manifest.UserEvents = append(manifest.UserEvents, capability.UserEvents...)
		manifest.Callbacks = append(manifest.Callbacks, capability.Callbacks...)
	}
	manifest.TenantScopes = normalizeFeishuScopes(manifest.TenantScopes)
	manifest.UserScopes = normalizeFeishuScopes(manifest.UserScopes)
	if len(manifest.UserScopes) == 0 {
		manifest.UserScopes = nil
	}
	manifest.TenantEvents = normalizeFeishuCapabilityStrings(manifest.TenantEvents)
	manifest.UserEvents = normalizeFeishuCapabilityStrings(manifest.UserEvents)
	manifest.Callbacks = normalizeFeishuCapabilityStrings(manifest.Callbacks)
	return manifest
}

func normalizeFeishuCapabilityStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *Bridge) feishuCapabilityActive(spec feishuCapabilitySpec) bool {
	switch spec.Activation {
	case feishuCapabilityAlways:
		return true
	case feishuCapabilityBotToBot:
		return b != nil && b.botToBotSnapshot().Enabled
	case feishuCapabilityGroupWatch:
		return b != nil && b.groupWatchConfigured() && b.feishuThreadCapabilitiesActive()
	case feishuCapabilityDocumentComments:
		if b == nil {
			return false
		}
		return b.documentCommentsActive() || b.documentCommentsConfiguredForProfile()
	default:
		return false
	}
}

func (b *Bridge) feishuThreadCapabilitiesActive() bool {
	if b == nil {
		return false
	}
	// Lightweight unit bridges do not carry a DB-backed profile state. A real
	// Bridge always does, so production activation follows the authoritative
	// interaction mode while focused helpers can still exercise scope mapping.
	if b.db == nil {
		return true
	}
	mode, err := b.interactionMode()
	return err == nil && mode == config.FeishuInteractionThread
}

func (b *Bridge) activeFeishuCapabilitySpecs() []feishuCapabilitySpec {
	out := make([]feishuCapabilitySpec, 0, len(feishuCapabilitySpecs))
	for _, capability := range feishuCapabilitySpecs {
		if b.feishuCapabilityActive(capability) {
			out = append(out, capability)
		}
	}
	return out
}
