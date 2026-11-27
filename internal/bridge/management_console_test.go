package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
)

type fakeManagementConsoleActions struct {
	mu         sync.Mutex
	info       ManagementConsoleInfo
	opens      int
	closes     int
	snapshots  int
	err        error
	openCalled chan struct{}
	openBlock  chan struct{}
}

func (a *fakeManagementConsoleActions) Snapshot() ManagementConsoleInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snapshots++
	return a.info
}
func (a *fakeManagementConsoleActions) Open(ctx context.Context) (ManagementConsoleInfo, error) {
	a.mu.Lock()
	a.opens++
	a.mu.Unlock()
	if a.openCalled != nil {
		close(a.openCalled)
	}
	if a.openBlock != nil {
		select {
		case <-ctx.Done():
			return ManagementConsoleInfo{}, ctx.Err()
		case <-a.openBlock:
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return ManagementConsoleInfo{}, a.err
	}
	a.info = ManagementConsoleInfo{Enabled: true, URL: "http://192.0.2.10:7892", Password: "only-owner-memory-password", ExpiresAt: time.Now().Add(30 * time.Minute)}
	return a.info, nil
}
func (a *fakeManagementConsoleActions) Close(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closes++
	if a.err != nil {
		return a.err
	}
	a.info = ManagementConsoleInfo{}
	return nil
}

func TestManagementConsoleOwnerPrivateCallbacksAndNavigation(t *testing.T) {
	for _, action := range []string{managementActionConsoleOpen, managementActionConsoleEnable, managementActionConsoleDisable, managementActionConsoleRefresh} {
		for _, actor := range []string{"owner_private", "nonowner_private", "owner_group", "missing_context", "other_bot_card"} {
			t.Run(action+"/"+actor, func(t *testing.T) {
				b, _ := newBridgeForInputTest(t)
				b.secrets = &Secrets{OpenId: "owner-open"}
				a := &fakeManagementConsoleActions{}
				b.SetManagementConsoleActions(a)
				cards := make(chan string, 5)
				b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
				event := settingsCallback(action, nil)
				switch actor {
				case "owner_private":
					b.rememberPrivateManagementCard("owner-open", "om-settings")
				case "nonowner_private":
					b.rememberPrivateManagementCard("other", "om-settings")
					event.Event.Operator.OpenID = "other"
				case "owner_group":
					// The caller's owner identity never turns an unknown/group card into a DM.
					event.Event.Context.OpenChatID = "oc-group"
				case "missing_context":
					event.Event.Context = nil
				case "other_bot_card":
					if err := b.db.recordPrivateManagementCard("feishu:another", "owner-open", "om-settings"); err != nil {
						t.Fatal(err)
					}
				}
				resp, handled, err := b.handleManagementCardAction(context.Background(), event)
				if err != nil || !handled || resp == nil || resp.Toast == nil {
					t.Fatalf("callback = %v %v %v", resp, handled, err)
				}
				if actor != "owner_private" {
					if resp.Toast.Type != "error" {
						t.Fatal("unauthorized callback accepted")
					}
					a.mu.Lock()
					defer a.mu.Unlock()
					if a.opens != 0 || a.closes != 0 || a.snapshots != 0 || len(cards) != 0 {
						t.Fatal("unauthorized callback read or changed console")
					}
					return
				}
				card := waitForManagementCard(t, cards, func(card string) bool { return !strings.Contains(card, "正在更新后台状态…") })
				if card == "" || !strings.Contains(card, managementActionStatusOpen) || !strings.Contains(card, managementActionConsoleRefresh) {
					t.Fatal("missing control page or overview navigation")
				}
				a.mu.Lock()
				defer a.mu.Unlock()
				if (action == managementActionConsoleOpen || action == managementActionConsoleRefresh) && (a.opens != 0 || a.closes != 0) {
					t.Fatal("navigation implicitly opened console")
				}
				if action == managementActionConsoleEnable && (a.opens != 1 || !strings.Contains(card, "only-owner-memory-password")) {
					t.Fatal("owner enable did not show the temporary password")
				}
				if action == managementActionConsoleDisable && (a.closes != 1 || strings.Contains(card, "only-owner-memory-password")) {
					t.Fatal("close did not clear credentials")
				}
			})
		}
	}
}

func TestManagementConsoleCardLanguagesStatesAndSecretBoundary(t *testing.T) {
	t.Parallel()
	now := time.Now()
	info := ManagementConsoleInfo{Enabled: true, URL: "http://192.0.2.10:7892", Password: "private-password-123", ExpiresAt: now.Add(29*time.Minute + 3*time.Second)}
	for _, state := range []string{"on", "off", "expired", "working", "failed", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			current, notice := info, ""
			switch state {
			case "off":
				current.Enabled = false
			case "expired":
				current.ExpiresAt = now.Add(-time.Second)
			case "working", "failed", "unavailable":
				current = ManagementConsoleInfo{}
				notice = state
			}
			raw := renderManagementConsoleCard(current, notice, now)
			if !json.Valid([]byte(raw)) {
				t.Fatal("invalid card JSON")
			}
			for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese, "fr"} {
				card := selectManagementCardLocale(raw, locale)
				if strings.Contains(card, "management.console.") || !strings.Contains(card, managementActionStatusOpen) || !strings.Contains(card, managementActionConsoleRefresh) {
					t.Fatal("raw translation key or missing actions")
				}
				wantTitle := "Web management console"
				if locale == i18n.Chinese {
					wantTitle = "Web管理后台"
				}
				if !strings.Contains(card, wantTitle) {
					t.Fatalf("missing %s title", locale)
				}
				if state == "on" {
					if !strings.Contains(card, info.URL) || !strings.Contains(card, info.Password) || !strings.Contains(card, "29:03") || !strings.Contains(card, managementActionConsoleDisable) {
						t.Fatal("enabled state lacks address/password/time/close")
					}
					var doc any
					_ = json.Unmarshal([]byte(card), &doc)
					assertConsoleNoSecretInCallback(t, doc, info.Password)
				} else if strings.Contains(card, info.Password) || strings.Contains(card, info.URL) {
					t.Fatal("inactive state retained secret/address")
				}
			}
		})
	}
	overview := renderManagementOverviewCard(managementState{})
	if !strings.Contains(overview, managementActionConsoleOpen) || !strings.Contains(overview, "Web管理后台") || !strings.Contains(overview, "Web management console") || strings.Contains(overview, info.Password) {
		t.Fatal("overview entry missing or leaking credentials")
	}
}

func assertConsoleNoSecretInCallback(t *testing.T, value any, secret string) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "value" || key == "behaviors" || key == "summary" {
				raw, _ := json.Marshal(child)
				if strings.Contains(string(raw), secret) {
					t.Fatalf("password leaked in %s", key)
				}
			}
			assertConsoleNoSecretInCallback(t, child, secret)
		}
	case []any:
		for _, child := range node {
			assertConsoleNoSecretInCallback(t, child, secret)
		}
	}
}

func TestManagementConsoleOperationFailureAndUnavailable(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "unavailable", false: "failure"}[unavailable], func(t *testing.T) {
			b, _ := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner-open"}
			b.rememberPrivateManagementCard("owner-open", "om-settings")
			if !unavailable {
				b.SetManagementConsoleActions(&fakeManagementConsoleActions{err: errors.New("diagnostic-with-secret-password")})
			}
			cards := make(chan string, 5)
			b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
			resp, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionConsoleEnable, nil))
			if err != nil || resp == nil {
				t.Fatal(err)
			}
			card := waitForManagementCard(t, cards, func(card string) bool {
				return strings.Contains(card, "暂不支持") || strings.Contains(card, "后台操作失败")
			})
			if card == "" || strings.Contains(card, "diagnostic-with-secret-password") || !strings.Contains(card, managementActionStatusOpen) {
				t.Fatal("failure state missing or leaked diagnostics")
			}
		})
	}
}

func TestManagementConsoleSlowOpenDoesNotReplaceNewerOverview(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.rememberPrivateManagementCard("owner-open", "om-settings")
	a := &fakeManagementConsoleActions{openCalled: make(chan struct{}), openBlock: make(chan struct{})}
	b.SetManagementConsoleActions(a)
	cards := make(chan string, 8)
	b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
	if _, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionConsoleEnable, nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.openCalled:
	case <-time.After(time.Second):
		t.Fatal("open not started")
	}
	if _, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionStatusOpen, map[string]interface{}{managementSurfaceKey: string(managementSurfaceAmux)})); err != nil {
		t.Fatal(err)
	}
	if card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "amux_overview_config") }); card == "" {
		t.Fatal("overview was not delivered")
	}
	close(a.openBlock)
	select {
	case card := <-cards:
		if strings.Contains(card, "only-owner-memory-password") {
			t.Fatal("late open replaced overview with secret-bearing control page")
		}
	case <-time.After(60 * time.Millisecond):
	}
}

func TestManagementConsolePrivateFallbackAndLanguage(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		t.Run(string(locale), func(t *testing.T) {
			b, _ := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner-open"}
			b.rememberPrivateManagementCard("owner-open", "om-settings")
			if err := b.db.setManagementLocale(b.currentBridgeId(), "owner-open", string(locale)); err != nil {
				t.Fatal(err)
			}
			a := &fakeManagementConsoleActions{info: ManagementConsoleInfo{Enabled: true, URL: "http://192.0.2.10:7892", Password: "only-owner-memory-password", ExpiresAt: time.Now().Add(time.Minute)}}
			b.SetManagementConsoleActions(a)
			b.managementCardPatch = func(context.Context, string, string) error {
				return errors.New("request failed with only-owner-memory-password")
			}
			cards := make(chan string, 1)
			b.dmInteractive = func(_ context.Context, recipient, card string) (string, string, error) {
				if recipient != "owner-open" {
					t.Error("secret fallback sent to a nonowner")
				}
				cards <- card
				return "fallback-private-card", "", nil
			}
			resp, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionConsoleRefresh, nil))
			if err != nil || resp.Toast.Type != "success" {
				t.Fatal("refresh failed")
			}
			card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "only-owner-memory-password") })
			if card == "" || strings.Contains(card, "i18n_content") {
				t.Fatal("private fallback lost saved language")
			}
			want := "Temporary password"
			if locale == i18n.Chinese {
				want = "临时密码"
			}
			if !strings.Contains(card, want) {
				t.Fatal("fallback has incorrect language")
			}
			// Only identity metadata is recorded for fallback cards, never their contents.
			var recipient string
			if err := b.db.db.QueryRow(`SELECT open_id FROM feishu_private_management_cards WHERE message_id=?`, "fallback-private-card").Scan(&recipient); err != nil || recipient != "owner-open" {
				t.Fatal("fallback provenance not recorded")
			}
		})
	}
}
