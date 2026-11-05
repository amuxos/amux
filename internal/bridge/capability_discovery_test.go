package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestDiscoveryCatalogCardsLanguagesAndNavigation(t *testing.T) {
	now := time.Date(2026, 10, 29, 11, 0, 0, 0, time.FixedZone("CST", 8*3600))
	ids := map[string]bool{}
	for _, c := range discoveryCatalog {
		if ids[c.ID] {
			t.Fatal("duplicate capability", c.ID)
		}
		ids[c.ID] = true
		for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, "", "fr"} {
			cards := []string{renderDiscoveryDetail(c, discoveryPreference{Enabled: true}, false, now), renderDiscoveryDetail(c, discoveryPreference{Enabled: true}, true, now), renderDiscoveryResult(c, "sent"), renderDiscoveryResult(c, "uncertain")}
			for i := 0; i < 2; i++ {
				effective := locale
				if locale != i18n.Chinese {
					effective = i18n.English
				}
				cards = append(cards, renderDiscoveryPrompt(c, i, effective, "0123456789abcdef0123456789abcdef"))
			}
			for _, raw := range cards {
				selected := selectManagementCardLocale(raw, locale)
				var doc any
				if err := json.Unmarshal([]byte(selected), &doc); err != nil {
					t.Fatal(err)
				}
				assertManagementFormScope(t, doc, false)
				assertDiscoveryUniqueNames(t, doc, map[string]bool{})
				findManagementButtonByAction(t, selected, managementActionStatusOpen)
				findManagementButtonByAction(t, selected, capabilityAction+"open")
				if len(selected) >= maxFeishuCardBytes {
					t.Fatal("oversized", c.ID)
				}
				walkManagementTestText(doc, func(text string) {
					if strings.Contains(text, "discovery.") || strings.Contains(text, "%!") {
						t.Fatal("unresolved copy", text)
					}
					if locale == i18n.English && strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
						t.Fatal("untranslated", text)
					}
				})
			}
		}
	}
	for _, category := range []string{"", "automation", "collaboration", "experts", "system"} {
		for page := 0; page < 4; page++ {
			card := renderDiscoveryPlaza(discoveryPreference{}, category, page, now)
			var doc any
			if err := json.Unmarshal([]byte(card), &doc); err != nil {
				t.Fatal(err)
			}
			assertManagementFormScope(t, doc, false)
			assertDiscoveryUniqueNames(t, doc, map[string]bool{})
			if len(card) >= maxFeishuCardBytes {
				t.Fatal("oversized plaza")
			}
		}
	}
	for _, id := range []string{"schedules", "experiments", "group_agent", "calendar"} {
		if !ids[id] {
			t.Fatal("missing requested capability", id)
		}
	}
}

// Feishu validates component names across the whole card, including buttons
// with the same action but different parameters and pagination destinations.
func assertDiscoveryUniqueNames(t *testing.T, node any, seen map[string]bool) {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		if name, ok := n["name"].(string); ok && name != "" && n["tag"] != nil {
			if seen[name] {
				t.Fatalf("duplicate native card component name: %s", name)
			}
			seen[name] = true
		}
		for _, child := range n {
			assertDiscoveryUniqueNames(t, child, seen)
		}
	case []any:
		for _, child := range n {
			assertDiscoveryUniqueNames(t, child, seen)
		}
	}
}

func TestDiscoveryDailyPersistsOneCardPerDateWithoutRepeats(t *testing.T) {
	b, _ := guideTestBridge(t)
	start := time.Date(2026, 10, 29, 11, 0, 0, 0, time.FixedZone("CST", 8*3600))
	var sequence int
	var dbName, databasePath string
	if err := b.db.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &dbName, &databasePath); err != nil {
		t.Fatal(err)
	}
	// Existing installations are not silently enrolled.
	id, err := b.db.claimDiscovery(b.currentBridgeId(), "owner", start)
	if err != nil || id != "" {
		t.Fatal(id, err)
	}
	if err = b.db.initDiscovery(b.currentBridgeId(), "owner", start); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{start, start.AddDate(0, 0, 1).Add(-time.Minute)} {
		if id, err = b.db.claimDiscovery(b.currentBridgeId(), "owner", now); err != nil || id != "" {
			t.Fatal("sent before tomorrow 11", id, err)
		}
	}
	for round := 0; round < 2; round++ {
		seen := map[string]bool{}
		for day := 1; day <= len(discoveryCatalog); day++ {
			now := start.AddDate(0, 0, round*len(discoveryCatalog)+day)
			id, err = b.db.claimDiscovery(b.currentBridgeId(), "owner", now)
			if err != nil || id == "" || seen[id] {
				t.Fatal("missing or repeated capability within a round", id, err)
			}
			seen[id] = true
			// A second DB handle represents another process or a restart.
			other, openErr := openBridgeDB(databasePath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			again, againErr := other.claimDiscovery(b.currentBridgeId(), "owner", now)
			other.Close()
			if againErr != nil || again != "" {
				t.Fatal("restart replayed daily card", again, againErr)
			}
		}
	}
	if id, err = b.db.claimDiscovery("another-bot", "owner", start.AddDate(0, 0, 100)); err != nil || id != "" {
		t.Fatal("cross-bot delivery")
	}
	if id, err = b.db.claimDiscovery(b.currentBridgeId(), "another-owner", start.AddDate(0, 0, 100)); err != nil || id != "" {
		t.Fatal("cross-owner delivery")
	}
	if err = b.db.setDiscovery(b.currentBridgeId(), "owner", false, start); err != nil {
		t.Fatal(err)
	}
	if id, err = b.db.claimDiscovery(b.currentBridgeId(), "owner", start.AddDate(0, 0, 100)); err != nil || id != "" {
		t.Fatal("disabled delivery")
	}
}

func TestDiscoveryRetiredCapabilitiesDoNotAdvanceCurrentRound(t *testing.T) {
	b, _ := guideTestBridge(t)
	start := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	if err := b.db.initDiscovery(b.currentBridgeId(), "owner", start); err != nil {
		t.Fatal(err)
	}
	history := []string{"knowledge", "documents", "memory", "files"}
	for _, c := range discoveryCatalog[:len(discoveryCatalog)-1] {
		history = append(history, c.ID)
	}
	for day, id := range history {
		_, err := b.db.db.Exec(`INSERT INTO feishu_discovery_deliveries(bridge_id,open_id,local_date,capability_id,status) VALUES(?,?,?,?,'sent')`, b.currentBridgeId(), "owner", start.AddDate(0, 0, day+1).Format("2006-01-02"), id)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := start.AddDate(0, 0, len(history)+1)
	id, err := b.db.claimDiscovery(b.currentBridgeId(), "owner", now)
	if err != nil || id != discoveryCatalog[len(discoveryCatalog)-1].ID {
		t.Fatal("retired entries caused a repeat before the current round finished", id, err)
	}
	seen := map[string]bool{}
	for day := 1; day <= len(discoveryCatalog); day++ {
		id, err := b.db.claimDiscovery(b.currentBridgeId(), "owner", now.AddDate(0, 0, day))
		_, active := discoveryFind(id)
		if err != nil || !active || seen[id] {
			t.Fatal("next round repeated or recommended a retired capability", id, err)
		}
		seen[id] = true
	}
}

func TestDiscoveryRetiredCardCallbacksDoNotStartWork(t *testing.T) {
	b, _ := guideTestBridge(t)
	for _, id := range []string{"knowledge", "documents", "memory", "files"} {
		for _, action := range []string{"detail", "prompt", "send"} {
			resp := discoveryTestAction(t, b, action, map[string]interface{}{"capability": id, "token": "0123456789abcdef0123456789abcdef"}, map[string]interface{}{"capability_prompt": "Old example"})
			if resp.Card != nil || resp.Toast == nil || resp.Toast.Type != "error" {
				t.Fatal("retired card remained actionable", id, action, resp)
			}
		}
	}
	if routes := b.hub.(*bridgeTestHub).routes; len(routes) != 0 {
		t.Fatal("retired card started work", routes)
	}
}

func TestDiscoveryConcurrentAndUncertainDeliveryNotReplayed(t *testing.T) {
	b, _ := guideTestBridge(t)
	start := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	if err := b.db.initDiscovery(b.currentBridgeId(), "owner", start); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	b.dmInteractive = func(_ context.Context, owner, card string) (string, string, error) {
		if owner != "owner" {
			return "", "", fmt.Errorf("wrong recipient")
		}
		sends.Add(1)
		return "", "", errors.New("unknown network outcome")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.deliverCapabilityDiscovery(context.Background(), start.AddDate(0, 0, 1))
		}()
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatal("duplicate sends", sends.Load())
	}
	if err := b.deliverCapabilityDiscovery(context.Background(), start.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 {
		t.Fatal("replayed uncertain outcome")
	}
	var status string
	if err := b.db.db.QueryRow(`SELECT status FROM feishu_discovery_deliveries`).Scan(&status); err != nil || status != "uncertain" {
		t.Fatal(status, err)
	}
	// Offline dates do not create a backlog; only today's card is claimed.
	_ = b.deliverCapabilityDiscovery(context.Background(), start.AddDate(0, 0, 9))
	if sends.Load() != 2 {
		t.Fatal("offline dates were backfilled")
	}
}

func discoveryTestAction(t *testing.T, b *Bridge, action string, values map[string]interface{}, form map[string]interface{}) *callback.CardActionTriggerResponse {
	t.Helper()
	if values == nil {
		values = map[string]interface{}{}
	}
	values[managementActionKey] = capabilityAction + action
	e := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "owner"}, Context: &callback.Context{OpenMessageID: "guide-card", OpenChatID: "chat"}, Action: &callback.CallBackAction{Value: values, FormValue: form}}}
	resp, handled, err := b.handleManagementCardAction(context.Background(), e)
	if err != nil || !handled || resp == nil {
		t.Fatal(resp, handled, err)
	}
	return resp
}
func TestDiscoveryBrowsingAndSubmittingAreSeparateFromOnboarding(t *testing.T) {
	b, cards := guideTestBridge(t)
	p := onboardingProgress{Step: "create", Expert: onboardingExpert{Name: "Saved expert"}}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	b.chatNotice = func(context.Context, string, string) (string, string, error) {
		sends.Add(1)
		return "", "", errors.New("unknown outcome")
	}
	for _, action := range []string{"open", "detail", "prompt", "random"} {
		resp := discoveryTestAction(t, b, action, map[string]interface{}{"capability": "calendar", "example": "0"}, nil)
		if resp.Card == nil || resp.Toast != nil {
			t.Fatal("navigation did not return native card", resp)
		}
	}
	if sends.Load() != 0 {
		t.Fatal("browsing started work")
	}
	if !reflect.DeepEqual(p, guideTestProgress(t, b)) {
		t.Fatal("changed onboarding draft")
	}
	values := map[string]interface{}{"capability": "calendar", "token": "0123456789abcdef0123456789abcdef"}
	for i := 0; i < 2; i++ {
		discoveryTestAction(t, b, "send", values, map[string]interface{}{"capability_prompt": "An edited request"})
	}
	select {
	case <-cards:
	case <-time.After(2 * time.Second):
		t.Fatal("no task result card")
	}
	if sends.Load() != 1 {
		t.Fatal("duplicate task request", sends.Load())
	}
	if !reflect.DeepEqual(p, guideTestProgress(t, b)) {
		t.Fatal("submission overwrote onboarding")
	}
	for _, tc := range []struct{ owner, message string }{{"stranger", "guide-card"}, {"owner", "forwarded-card"}} {
		e := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: tc.owner}, Context: &callback.Context{OpenMessageID: tc.message}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: capabilityAction + "enable"}}}}
		r, handled, err := b.handleManagementCardAction(context.Background(), e)
		if err != nil || !handled || r.Toast == nil || r.Toast.Type != "error" {
			t.Fatal("unauthorized action", r, err)
		}
	}
}

func TestDiscoverySubmittedPromptUsesOrdinaryPrivateRouting(t *testing.T) {
	for _, mode := range []string{config.FeishuInteractionThread, config.FeishuInteractionSingleton} {
		t.Run(mode, func(t *testing.T) {
			loadBridgeTestConfig(t, "[bridge.feishu.profiles.default.interaction]\nmode = \""+mode+"\"\n")
			b, cards := guideTestBridge(t)
			hub := b.hub.(*bridgeTestHub)
			hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
			setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
			if mode == config.FeishuInteractionSingleton {
				seedSingletonBotSettings(t, b, "builtin/codex", "existing-session")
				hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "existing-session", State: "running"}}}
			}
			var sends int
			b.chatNotice = func(_ context.Context, chatID, card string) (string, string, error) {
				sends++
				if chatID != "chat" || !strings.Contains(card, "My edited request") {
					return "", "", errors.New("request preview lost")
				}
				return "request-root", "", nil
			}
			b.threadNotice = func(context.Context, string, string) error { return nil }
			b.dmNotice = func(context.Context, string, string) (string, string, error) {
				return "", "", errors.New("unexpected routing failure notification")
			}
			b.reactionAdd = func(context.Context, string, string) error { return nil }
			values := map[string]interface{}{"capability": "calendar", "token": "0123456789abcdef0123456789abcdef"}
			for range 2 {
				discoveryTestAction(t, b, "send", values, map[string]interface{}{"capability_prompt": "My edited request"})
			}
			select {
			case card := <-cards:
				if !strings.Contains(card, discoveryCopy("sent").For(b.guideLocale("owner"))) {
					t.Fatal("submission did not finish routing", card)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not return")
			}
			if sends != 1 {
				t.Fatal("replayed request", sends)
			}
			wantRoutes := 2
			if mode == config.FeishuInteractionSingleton {
				wantRoutes = 1
			}
			if len(hub.routes) != wantRoutes || hub.routes[len(hub.routes)-1].method != "Submit" {
				t.Fatalf("routes=%+v", hub.routes)
			}
			submit := hub.routes[len(hub.routes)-1].payload.(*pb.SubmitRequest)
			if !strings.HasPrefix(submit.Text, "My edited request") || submit.Source != "lark" {
				t.Fatalf("lost submitted prompt: %+v", submit)
			}
			if mode == config.FeishuInteractionThread {
				chat, err := b.db.FindByRootMsg(b.currentBridgeId(), "request-root")
				if err != nil || chat == nil || chat.ChatID != "chat" {
					t.Fatal("request lost private topic binding", chat, err)
				}
			} else if submit.Name != "existing-session" {
				t.Fatal("did not use the bound continuous conversation", submit.Name)
			}
		})
	}
}

func TestDiscoveryDeliveredCardCanDisableDailyWithoutChangingGuide(t *testing.T) {
	b, cards := guideTestBridge(t)
	start := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	if err := b.db.initDiscovery(b.currentBridgeId(), "owner", start); err != nil {
		t.Fatal(err)
	}
	if err := b.deliverCapabilityDiscovery(context.Background(), start.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case card := <-cards:
		findManagementButtonByAction(t, card, capabilityAction+"disable")
	case <-time.After(time.Second):
		t.Fatal("no daily guide")
	}
	var status, messageID string
	if err := b.db.db.QueryRow(`SELECT status,message_id FROM feishu_discovery_deliveries`).Scan(&status, &messageID); err != nil || status != "sent" || !b.db.isPrivateManagementCard(b.currentBridgeId(), "owner", messageID) {
		t.Fatal("delivered card is not usable", status, messageID, err)
	}
	before := guideTestProgress(t, b)
	discoveryTestAction(t, b, "disable", nil, nil)
	if err := b.db.initDiscovery(b.currentBridgeId(), "owner", start); err != nil {
		t.Fatal(err)
	}
	if err := b.deliverCapabilityDiscovery(context.Background(), start.AddDate(0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 || !reflect.DeepEqual(before, guideTestProgress(t, b)) {
		t.Fatal("disabled delivery sent a card or changed guide")
	}
}
