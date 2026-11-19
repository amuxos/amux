package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const capabilityAction = "bot.discover."
const capabilityPageSize = 5

type discoveryCapability struct{ ID, Category, Settings string }

// Entries describe existing workflows. Integrations explicitly name their
// prerequisites; neither the catalog nor daily delivery executes Agent work.
var discoveryCatalog = []discoveryCapability{
	{"schedules", "automation", "schedule.list"},
	{"experiments", "system", managementActionExperimentsOpen},
	{"group_agent", "collaboration", managementActionExperimentsOpen},
	{"calendar", "automation", ""},
	{"experts", "experts", managementActionSettingsOpen},
	{"libraries", "experts", managementActionPackLibraryOpen},
	{"skills", "experts", managementActionPackLibraryOpen},
	{"delegation", "experts", ""},
	{"group_watch", "collaboration", managementActionGroupWatchOpen},
	{"document_comments", "collaboration", managementActionDocumentCommentsOpen},
	{"context", "collaboration", managementActionSettingsOpen},
	{"background", "automation", managementActionExperimentsOpen},
	{"goals", "automation", ""},
	{"runtimes", "system", managementActionSettingsOpen},
	{"bot_collaboration", "collaboration", managementActionBotToBotOpen},
	{"maintenance", "system", managementActionSystemMaintenance},
}

func discoveryCopy(key string, args ...any) i18n.Text {
	return managementCopy("discovery."+key, args...)
}
func (c discoveryCapability) text(key string) i18n.Text { return discoveryCopy(c.ID + "." + key) }
func discoveryFind(id string) (discoveryCapability, bool) {
	for _, c := range discoveryCatalog {
		if c.ID == id {
			return c, true
		}
	}
	return discoveryCapability{}, false
}
func discoveryRandom(n int) int {
	if n <= 1 {
		return 0
	}
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(i.Int64())
}
func discoveryToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func discoveryButton(action string, text any, values map[string]string) managementButton {
	parts := []string{capabilityAction + action}
	for _, key := range []string{"capability", "example", "category", "page", "exclude"} {
		if value := values[key]; value != "" {
			parts = append(parts, value)
		}
	}
	return managementButton{Text: text, Type: "default", Name: strings.Join(parts, "."), Action: capabilityAction + action, Values: values}
}
func discoveryCard(title any, elements []cardElement) string {
	card := marshalNativeManagementCard(managementCardView{Title: title, Summary: title, Theme: "wathet", Icon: managementIcon("app-default_outlined", "purple"), Elements: elements})
	return renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
}
func discoveryParent() cardElement {
	return managementSubpageBackElement(discoveryCopy("back"), capabilityAction+"open", "amux.discovery.back")
}

type discoveryPreference struct {
	Enabled   bool
	StartDate string
}

func (db *bridgeDB) discoveryPreference(bridgeID, owner string) (discoveryPreference, error) {
	var p discoveryPreference
	err := db.db.QueryRow(`SELECT enabled,start_date FROM feishu_discovery_preferences WHERE bridge_id=? AND open_id=?`, bridgeID, owner).Scan(&p.Enabled, &p.StartDate)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}
func (db *bridgeDB) initDiscovery(bridgeID, owner string, now time.Time) error {
	if bridgeID == "" || owner == "" {
		return nil
	}
	_, err := db.db.Exec(`INSERT OR IGNORE INTO feishu_discovery_preferences(bridge_id,open_id,enabled,start_date) VALUES(?,?,1,?)`, bridgeID, owner, now.Format("2006-01-02"))
	return err
}
func (db *bridgeDB) setDiscovery(bridgeID, owner string, enabled bool, now time.Time) error {
	_, err := db.db.Exec(`INSERT INTO feishu_discovery_preferences(bridge_id,open_id,enabled,start_date) VALUES(?,?,?,?) ON CONFLICT(bridge_id,open_id) DO UPDATE SET enabled=excluded.enabled,start_date=CASE WHEN feishu_discovery_preferences.enabled=excluded.enabled THEN feishu_discovery_preferences.start_date ELSE excluded.start_date END`, bridgeID, owner, enabled, now.Format("2006-01-02"))
	return err
}

func renderDiscoveryPlaza(p discoveryPreference, category string, page int, now time.Time) string {
	var entries []discoveryCapability
	for _, c := range discoveryCatalog {
		if category == "" || c.Category == category {
			entries = append(entries, c)
		}
	}
	pages := max(1, (len(entries)+capabilityPageSize-1)/capabilityPageSize)
	page = max(0, min(page, pages-1))
	options := []cardOption{{Text: managementPlainText(discoveryCopy("category.all")), Value: "all"}}
	for _, id := range []string{"automation", "collaboration", "experts", "system"} {
		options = append(options, cardOption{Text: managementPlainText(discoveryCopy("category." + id)), Value: id})
	}
	selected := category
	if selected == "" {
		selected = "all"
	}
	filter := managementSelectStaticElement("capability_category", discoveryCopy("category"), selected, options)
	filter.Required = false
	filter.Behaviors = []cardBehavior{{Type: "callback", Value: managementButtonValue(discoveryButton("open", discoveryCopy("title"), nil))}}
	elements := []cardElement{managementHintElement(discoveryCopy("intro")), filter}
	for _, c := range entries[page*capabilityPageSize : min(len(entries), (page+1)*capabilityPageSize)] {
		detailSpec := discoveryButton("detail", discoveryCopy("examples"), map[string]string{"capability": c.ID})
		detailSpec.Type = "primary_text"
		detailBtn := managementButtonElement(detailSpec)
		detailBtn.Size = "small"
		header := cardElement{
			Tag:               "column_set",
			FlexMode:          "none",
			HorizontalSpacing: "8px",
			Columns: []cardColumn{
				{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(c.text("title"), "normal", "default", nil)}},
				{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{detailBtn}},
			},
		}
		item := cardElement{
			Tag:             "interactive_container",
			ElementID:       "amux_discovery_" + c.ID,
			Width:           "fill",
			BackgroundStyle: "grey-50",
			HasBorder:       true,
			BorderColor:     "grey-200",
			CornerRadius:    "8px",
			Padding:         "10px 12px 10px 12px",
			Direction:       "vertical",
			VerticalSpacing: "4px",
			Behaviors: []cardBehavior{{
				Type:  "callback",
				Value: managementButtonValue(detailSpec),
			}},
			Elements: []cardElement{
				header,
				managementTextElement(c.text("body"), "notation", "grey", nil),
			},
		}
		elements = append(elements, item)
	}
	pageColumns := []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(discoveryCopy("page", page+1, pages), "notation", "grey", nil)}}}
	for _, delta := range []int{-1, 1} {
		if next := page + delta; next >= 0 && next < pages {
			key := "next"
			if delta < 0 {
				key = "previous"
			}
			btn := managementButtonElement(discoveryButton("open", discoveryCopy(key), map[string]string{"category": category, "page": strconv.Itoa(next)}))
			btn.Size = "small"
			pageColumns = append(pageColumns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{btn}})
		}
	}
	elements = append(elements, cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "8px", Columns: pageColumns})
	elements = append(elements, discoveryPreferenceElements(p, now)...)
	elements = append(elements, managementButtonRowElement([]managementButton{discoveryButton("random", discoveryCopy("random"), nil), {Text: managementCopy("management.help"), Action: managementActionHelpOpen}}))
	return discoveryCard(discoveryCopy("title"), elements)
}
func discoveryPreferenceElements(p discoveryPreference, now time.Time) []cardElement {
	key, action := "daily.off", "enable"
	if p.Enabled {
		key, action = "daily.on", "disable"
	}
	toggle := managementButtonElement(discoveryButton(action, discoveryCopy("daily."+action), nil))
	toggle.Type = "text"
	toggle.Size = "small"
	return []cardElement{
		managementDividerElement(),
		cardElement{
			Tag:               "column_set",
			FlexMode:          "none",
			HorizontalSpacing: "8px",
			Columns: []cardColumn{
				{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(discoveryCopy(key, now.Format("MST")), "notation", "grey", nil)}},
				{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{toggle}},
			},
		},
	}
}
func renderDiscoveryDetail(c discoveryCapability, p discoveryPreference, daily bool, now time.Time) string {
	elements := []cardElement{discoveryParent(), managementDividerElement(), managementTextElement(c.text("body"), "normal", "default", nil), managementHintElement(c.text("requires"))}
	for i := 0; i < 2; i++ {
		useSpec := discoveryButton("prompt", discoveryCopy("use", i+1), map[string]string{"capability": c.ID, "example": strconv.Itoa(i)})
		useSpec.Type = "primary_text"
		useBtn := managementButtonElement(useSpec)
		useBtn.Size = "small"
		box := managementVerticalSummaryElement(fmt.Sprintf("amux_discovery_example_%d", i),
			managementTextElement(c.text(fmt.Sprintf("prompt.%d", i)), "normal", "default", nil),
			cardElement{
				Tag:      "column_set",
				FlexMode: "none",
				Columns:  []cardColumn{{Tag: "column", Width: "auto", Elements: []cardElement{useBtn}}},
			},
		)
		box.Columns[0].VerticalSpacing = "4px"
		elements = append(elements, box)
	}
	footerButtons := []managementButton{}
	if c.Settings != "" {
		footerButtons = append(footerButtons, managementButton{Text: discoveryCopy("settings"), Type: "default", Action: c.Settings})
	}
	footerButtons = append(footerButtons, discoveryButton("random", discoveryCopy("random"), map[string]string{"exclude": c.ID}))
	elements = append(elements, managementButtonRowElement(footerButtons))
	if daily {
		elements = append([]cardElement{managementHintElement(discoveryCopy("daily.title"))}, elements...)
		elements = append(elements, discoveryPreferenceElements(p, now)...)
	}
	return discoveryCard(c.text("title"), elements)
}
func renderDiscoveryPrompt(c discoveryCapability, example int, locale i18n.Locale, token string) string {
	input := cardElement{Tag: "input", Name: "capability_prompt", DefaultValue: c.text(fmt.Sprintf("prompt.%d", example)).For(locale), Placeholder: managementPlainTextPtr(discoveryCopy("edit")), Required: true, Width: "fill"}
	button := managementFormSubmitButton(capabilityAction+"send", discoveryCopy("send"))
	button.Value, _ = json.Marshal(managementButtonValue(discoveryButton("send", discoveryCopy("send"), map[string]string{"capability": c.ID, "token": token})))
	elements := []cardElement{discoveryParent(), managementDividerElement(), managementButtonRowElement([]managementButton{discoveryButton("detail", discoveryCopy("back.detail"), map[string]string{"capability": c.ID})}), managementHintElement(discoveryCopy("review")), managementTextElement(c.text("requires"), "notation", "grey", nil), {Tag: "form", Name: "capability_prompt_form", Direction: "vertical", Elements: []cardElement{input, button}}}
	return discoveryCard(c.text("title"), elements)
}
func renderDiscoveryResult(c discoveryCapability, key string) string {
	return discoveryCard(c.text("title"), []cardElement{discoveryParent(), managementDividerElement(), managementHintElement(discoveryCopy(key)), managementButtonRowElement([]managementButton{discoveryButton("detail", discoveryCopy("back.detail"), map[string]string{"capability": c.ID})})})
}

func (b *Bridge) handleDiscoveryAction(ctx context.Context, event *callback.CardActionTriggerEvent, action, owner, messageID string) (*callback.CardActionTriggerResponse, bool, error) {
	if !strings.HasPrefix(action, capabilityAction) {
		return nil, false, nil
	}
	if b.db == nil || !b.isBotOwner(owner) || !b.db.isPrivateManagementCard(b.currentBridgeId(), owner, messageID) {
		return cardToast("error", discoveryCopy("private")), true, nil
	}
	p, err := b.db.discoveryPreference(b.currentBridgeId(), owner)
	if err != nil {
		return cardToast("error", discoveryCopy("failed")), true, nil
	}
	a := event.Event.Action
	var card string
	switch strings.TrimPrefix(action, capabilityAction) {
	case "open":
		category := managementCallbackString(a, "category")
		if a.Option != "" {
			category = a.Option
		}
		if category == "all" {
			category = ""
		}
		valid := category == ""
		for _, c := range discoveryCatalog {
			valid = valid || c.Category == category
		}
		if !valid {
			category = ""
		}
		page, _ := strconv.Atoi(managementCallbackString(a, "page"))
		card = renderDiscoveryPlaza(p, category, page, time.Now())
	case "enable", "disable":
		p.Enabled = action == capabilityAction+"enable"
		if err := b.db.setDiscovery(b.currentBridgeId(), owner, p.Enabled, time.Now()); err != nil {
			return cardToast("error", discoveryCopy("failed")), true, nil
		}
		card = renderDiscoveryPlaza(p, "", 0, time.Now())
	case "detail", "prompt", "random", "send":
		c, ok := discoveryFind(managementCallbackString(a, "capability"))
		if action == capabilityAction+"random" {
			choices := []discoveryCapability{}
			for _, entry := range discoveryCatalog {
				if entry.ID != managementCallbackString(a, "exclude") {
					choices = append(choices, entry)
				}
			}
			c, ok = choices[discoveryRandom(len(choices))], true
		}
		if !ok {
			return cardToast("error", discoveryCopy("missing")), true, nil
		}
		switch action {
		case capabilityAction + "prompt":
			example, _ := strconv.Atoi(managementCallbackString(a, "example"))
			example = max(0, min(example, 1))
			token := discoveryToken()
			if token == "" {
				return cardToast("error", discoveryCopy("failed")), true, nil
			}
			card = renderDiscoveryPrompt(c, example, b.guideLocale(owner), token)
		case capabilityAction + "send":
			prompt, _ := a.FormValue["capability_prompt"].(string)
			prompt = strings.TrimSpace(prompt)
			token := managementCallbackString(a, "token")
			if prompt == "" || len(prompt) > 16000 || len(token) != 32 || event.Event.Context == nil || event.Event.Context.OpenChatID == "" {
				return cardToast("error", discoveryCopy("invalid")), true, nil
			}
			if _, err := hex.DecodeString(token); err != nil {
				return cardToast("error", discoveryCopy("invalid")), true, nil
			}
			key, _ := json.Marshal([]string{"discovery", b.currentBridgeId(), owner, messageID, token})
			claimed, err := b.db.ConsumeManagementAction(string(key))
			if err != nil {
				return cardToast("error", discoveryCopy("failed")), true, nil
			}
			if claimed {
				go b.dispatchDiscoveryPrompt(owner, event.Event.Context.OpenChatID, messageID, c, prompt)
			}
			card = renderDiscoveryResult(c, "submitted")
		default:
			card = renderDiscoveryDetail(c, p, false, time.Now())
		}
	default:
		return cardToast("error", discoveryCopy("missing")), true, nil
	}
	return &callback.CardActionTriggerResponse{Card: &callback.Card{Type: "raw", Data: json.RawMessage(b.managementCardForPatch(owner, messageID, card))}}, true, nil
}

func (b *Bridge) dispatchDiscoveryPrompt(owner, chatID, messageID string, c discoveryCapability, prompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result := "sent"
	err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), chatID, WorkspaceKindPrivate)
	var root string
	if err == nil {
		request := marshalNativeManagementCard(managementCardView{Title: discoveryCopy("request"), Summary: discoveryCopy("request"), Theme: "blue", Elements: []cardElement{managementTextElement(prompt, "normal", "default", nil)}})
		if b.chatNotice != nil {
			root, _, err = b.chatNotice(ctx, chatID, b.managementCardForRecipient(owner, request))
		} else {
			root, _, err = b.sendInteractiveToChat(ctx, chatID, b.managementCardForRecipient(owner, request), "capability prompt")
		}
	}
	if err == nil && root == "" {
		err = errors.New("missing capability request receipt")
	}
	if err == nil {
		prompt = b.discoveryTaskPrompt(c, owner, prompt)
		// This is programmatic private-chat input, not a Feishu mention event.
		// Literal examples such as "@assistant /amux" must reach the Agent intact.
		mention := botMentionInfo{Mentioned: true, PreserveText: true}
		if handled, routeErr := b.routeBotSessionInput(ctx, botSessionInput{ChatID: chatID, MessageID: root, OpenID: owner, Text: prompt, ReplyKind: botSessionReplyDM}); handled || routeErr != nil {
			err = routeErr
		} else {
			err = b.routeToSessionInChatWithMention(ctx, chatID, root, root, owner, prompt, mention)
		}
	}
	if err != nil {
		result = "uncertain"
		log.Warn().Msg("bridge: capability prompt delivery did not complete")
	}
	b.updateManagementCardFromCallback(owner, messageID, renderDiscoveryResult(c, result))
}

// claimDiscovery combines selection and the daily claim in one transaction.
// The claim is durable before network IO, including uncertain delivery outcomes.
func (db *bridgeDB) claimDiscovery(bridgeID, owner string, now time.Time) (string, error) {
	p, err := db.discoveryPreference(bridgeID, owner)
	day := now.Format("2006-01-02")
	if err != nil || !p.Enabled || now.Hour() < 11 || day <= p.StartDate {
		return "", err
	}
	tx, err := db.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Retired capabilities remain in delivery history but do not count toward
	// a round of the current catalog. Keep its position across catalog removals.
	var placeholders []string
	args := []any{bridgeID, owner}
	for _, c := range discoveryCatalog {
		placeholders = append(placeholders, "?")
		args = append(args, c.ID)
	}
	scope := ` WHERE bridge_id=? AND open_id=? AND capability_id IN (` + strings.Join(placeholders, ",") + `)`
	var total int
	if err = tx.QueryRow(`SELECT count(*) FROM feishu_discovery_deliveries`+scope, args...).Scan(&total); err != nil {
		return "", err
	}
	cycleCount := total % len(discoveryCatalog)
	seen := map[string]bool{}
	if cycleCount > 0 {
		r, queryErr := tx.Query(`SELECT capability_id FROM feishu_discovery_deliveries`+scope+` ORDER BY local_date DESC LIMIT ?`, append(args, cycleCount)...)
		if queryErr != nil {
			return "", queryErr
		}
		for r.Next() {
			var id string
			if err = r.Scan(&id); err != nil {
				r.Close()
				return "", err
			}
			seen[id] = true
		}
		err = r.Err()
		r.Close()
		if err != nil {
			return "", err
		}
	}
	var candidates []discoveryCapability
	for _, c := range discoveryCatalog {
		if !seen[c.ID] {
			candidates = append(candidates, c)
		}
	}
	id := candidates[discoveryRandom(len(candidates))].ID
	res, err := tx.Exec(`INSERT OR IGNORE INTO feishu_discovery_deliveries(bridge_id,open_id,local_date,capability_id,status) VALUES(?,?,?,?,'claimed')`, bridgeID, owner, day, id)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	return id, nil
}
func (b *Bridge) deliverCapabilityDiscovery(ctx context.Context, now time.Time) error {
	if b.db == nil || b.secrets == nil || !b.ownerConfirmed() {
		return nil
	}
	bridgeID, owner := b.currentBridgeId(), b.secrets.OpenId
	id, err := b.db.claimDiscovery(bridgeID, owner, now)
	if err != nil || id == "" {
		return err
	}
	c, ok := discoveryFind(id)
	if !ok {
		return nil
	}
	card := b.managementCardForRecipient(owner, renderDiscoveryDetail(c, discoveryPreference{Enabled: true}, true, now))
	var messageID string
	if b.dmInteractive != nil {
		messageID, _, err = b.dmInteractive(ctx, owner, card)
	} else {
		sum := sha256.Sum256([]byte(bridgeID + ":" + owner + ":" + now.Format("2006-01-02")))
		messageID, _, err = b.sendInteractiveToDMWithUUID(ctx, owner, card, "daily capability", hex.EncodeToString(sum[:16]))
	}
	if err == nil && messageID == "" {
		err = errors.New("missing daily capability receipt")
	}
	status := "sent"
	if err != nil {
		status = "uncertain"
	} else {
		b.rememberPrivateManagementCard(owner, messageID)
	}
	_, saveErr := b.db.db.Exec(`UPDATE feishu_discovery_deliveries SET status=?,message_id=? WHERE bridge_id=? AND open_id=? AND local_date=?`, status, messageID, bridgeID, owner, now.Format("2006-01-02"))
	if err != nil {
		return err
	}
	return saveErr
}
func (b *Bridge) runCapabilityDiscovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := b.deliverCapabilityDiscovery(callCtx, time.Now()); err != nil {
			log.Warn().Msg("bridge: daily capability delivery unavailable")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SendCapabilityPlaza uses the normal owner-only management card entry. With
// enableDaily it also opts an existing installation into future daily cards.
func SendCapabilityPlaza(ctx context.Context, profile string, enableDaily bool) error {
	secrets, err := LoadProfileSecrets(profile)
	if err != nil {
		return err
	}
	if secrets.OpenId == "" || secrets.AppId == "" || secrets.AppSecret == "" {
		return ErrOnboardingOwnerRequired
	}
	db, err := openBridgeDBForProfile(DefaultDBPath(), profile)
	if err != nil {
		return err
	}
	defer db.Close()
	b := &Bridge{db: db, profile: profile, secrets: secrets, bridgeID: BridgeIdFromAppId(secrets.AppId), client: lark.NewClient(secrets.AppId, secrets.AppSecret, larkClientOptions()...)}
	if enableDaily {
		if err = db.setDiscovery(b.currentBridgeId(), secrets.OpenId, true, time.Now()); err != nil {
			return err
		}
	}
	p, err := db.discoveryPreference(b.currentBridgeId(), secrets.OpenId)
	if err != nil {
		return err
	}
	return b.sendManagementCardToDM(ctx, secrets.OpenId, renderDiscoveryPlaza(p, "", 0, time.Now()))
}
