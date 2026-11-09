package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const managementActionLanguage = "bot.language.set"
const managementLanguageField = "amux_interface_language"

func (db *bridgeDB) managementLocale(bridgeID, openID string) (i18n.Locale, error) {
	return db.managementLocaleContext(context.Background(), bridgeID, openID)
}

func (db *bridgeDB) managementLocaleContext(ctx context.Context, bridgeID, openID string) (i18n.Locale, error) {
	var value string
	err := db.db.QueryRowContext(ctx, `SELECT locale FROM feishu_management_locales WHERE bridge_id=? AND open_id=?`, bridgeID, openID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return i18n.Normalize(value), err
}

func (db *bridgeDB) setManagementLocale(bridgeID, openID, preference string) error {
	if bridgeID == "" || openID == "" {
		return fmt.Errorf("missing management locale identity")
	}
	locale := i18n.Normalize(preference)
	if preference != "auto" && preference != string(locale) {
		return fmt.Errorf("invalid management locale")
	}
	if preference == "auto" {
		_, err := db.db.Exec(`DELETE FROM feishu_management_locales WHERE bridge_id=? AND open_id=?`, bridgeID, openID)
		return err
	}
	_, err := db.db.Exec(`INSERT INTO feishu_management_locales(bridge_id,open_id,locale) VALUES(?,?,?) ON CONFLICT(bridge_id,open_id) DO UPDATE SET locale=excluded.locale`, bridgeID, openID, locale)
	return err
}

func (db *bridgeDB) recordPrivateManagementCard(bridgeID, openID, messageID string) error {
	if bridgeID == "" || openID == "" || messageID == "" {
		return nil
	}
	_, err := db.db.Exec(`INSERT INTO feishu_private_management_cards(bridge_id,message_id,open_id) VALUES(?,?,?) ON CONFLICT(bridge_id,message_id) DO UPDATE SET open_id=excluded.open_id`, bridgeID, messageID, openID)
	return err
}

func (db *bridgeDB) isPrivateManagementCard(bridgeID, openID, messageID string) bool {
	return db.isPrivateManagementCardContext(context.Background(), bridgeID, openID, messageID)
}

func (db *bridgeDB) isPrivateManagementCardContext(ctx context.Context, bridgeID, openID, messageID string) bool {
	if messageID == "" || openID == "" {
		return false
	}
	var owner string
	return db.db.QueryRowContext(ctx, `SELECT open_id FROM feishu_private_management_cards WHERE bridge_id=? AND message_id=?`, bridgeID, messageID).Scan(&owner) == nil && owner == openID
}

func (b *Bridge) managementLocale(openID string) i18n.Locale {
	if b.db == nil || openID == "" {
		return ""
	}
	// Language preference is presentation-only. A busy single-connection pool
	// must not keep a card callback from acknowledging an accepted operation.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	locale, err := b.db.managementLocaleContext(ctx, b.currentBridgeId(), openID)
	if err != nil {
		log.Warn().Err(err).Msg("bridge: could not read management language preference")
	}
	return locale
}

func (b *Bridge) managementCardForRecipient(openID, cardJSON string) string {
	return selectManagementCardLocale(cardJSON, b.managementLocale(openID))
}

func (b *Bridge) managementCardForPatch(openID, messageID, cardJSON string) string {
	// Only server-recorded direct messages may use a personal override. Unknown
	// and shared cards retain native per-viewer translations, including old cards.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if b.db == nil || !b.db.isPrivateManagementCardContext(ctx, b.currentBridgeId(), openID, messageID) {
		return cardJSON
	}
	return b.managementCardForRecipient(openID, cardJSON)
}

func (b *Bridge) rememberPrivateManagementCard(openID, messageID string) {
	if b.db == nil {
		return
	}
	if err := b.db.recordPrivateManagementCard(b.currentBridgeId(), openID, messageID); err != nil {
		log.Warn().Err(err).Msg("bridge: could not record private management card")
	}
}

func selectManagementCardLocale(cardJSON string, locale i18n.Locale) string {
	if locale == "" {
		return cardJSON
	}
	var doc any
	decoder := json.NewDecoder(strings.NewReader(cardJSON))
	decoder.UseNumber()
	if decoder.Decode(&doc) != nil {
		return cardJSON
	}
	selectManagementLocale(doc, locale)
	data, err := json.Marshal(doc)
	if err != nil {
		return cardJSON
	}
	return string(data)
}

func selectManagementLocale(value any, locale i18n.Locale) {
	switch node := value.(type) {
	case map[string]any:
		if translations, ok := node["i18n_content"].(map[string]any); ok {
			key := "en_us"
			if locale == i18n.Chinese {
				key = "zh_cn"
			}
			if content, ok := translations[key].(string); ok {
				node["content"] = content
				delete(node, "i18n_content")
			}
		}
		if node["name"] == managementLanguageField {
			node["initial_option"] = string(locale)
		}
		for key, child := range node {
			if key != "value" && key != "behaviors" {
				selectManagementLocale(child, locale)
			}
		}
	case []any:
		for _, child := range node {
			selectManagementLocale(child, locale)
		}
	}
}

func managementLanguageElement() cardElement {
	options := []cardOption{
		{Text: managementPlainText(managementCopy("management.language.auto")), Value: "auto"},
		{Text: managementPlainText("English"), Value: "en"},
		{Text: managementPlainText("简体中文"), Value: "zh-CN"},
	}
	field := managementSelectStaticElement(managementLanguageField, managementCopy("management.language"), "auto", options)
	// This selector invokes a callback outside a form; Feishu rejects the
	// form-only required field on standalone controls.
	field.Required = false
	field.Behaviors = []cardBehavior{{Type: "callback", Value: managementButtonValue(managementButton{Action: managementActionLanguage})}}
	return managementSettingsFieldRow(managementCopy("management.language"), field)
}

func (b *Bridge) changeManagementLocale(openID, messageID string, action *callback.CallBackAction) *callback.CardActionTriggerResponse {
	preference := action.Option
	if preference == "" {
		preference, _ = action.FormValue[managementLanguageField].(string)
	}
	if preference != "auto" && preference != "en" && preference != "zh-CN" {
		return cardToast("error", managementCopy("management.language.invalid"))
	}
	if b.db == nil || !b.db.isPrivateManagementCard(b.currentBridgeId(), openID, messageID) {
		return cardToast("warning", managementCopy("management.language.invalid"))
	}
	if err := b.db.setManagementLocale(b.currentBridgeId(), openID, preference); err != nil {
		return cardToast("error", managementCopy("management.language.failed"))
	}
	b.updateManagementCardFromCallback(openID, messageID, renderManagementOverviewCard(b.managementState()))
	return cardToast("success", managementCopy("management.language.saved"))
}

func (b *Bridge) localizeManagementToast(response *callback.CardActionTriggerResponse, openID string) {
	if response == nil || response.Toast == nil {
		return
	}
	locale := b.managementLocale(openID)
	if locale == "" {
		return
	}
	key := "en_us"
	if locale == i18n.Chinese {
		key = "zh_cn"
	}
	if text, ok := response.Toast.I18nContent[key]; ok {
		response.Toast.Content = text
		response.Toast.I18nContent = nil
	}
}

// Plain messages cannot carry Feishu's per-viewer language fields. Without an
// explicit preference, retain both translations in this rare delivery fallback.
func (b *Bridge) managementFallbackText(openID string, value any) string {
	text := managementText(value)
	if locale := b.managementLocale(openID); locale != "" {
		return text.For(locale)
	}
	en, zh := text.For(i18n.English), text.For(i18n.Chinese)
	if en == zh {
		return en
	}
	return en + "\n\n" + zh
}
