package bridge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBrowserWorkspacePrivateBindingAndNavigation(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppSecret: "test-only"}
	consoleTestBinding(t, b, "oc_known", "topic-one")
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b.schedules = scheduleTestControl{store}
	for _, user := range []string{"owner", "member"} {
		t.Run(user, func(t *testing.T) {
			ctx := context.Background()
			input := ConsoleWorkspaceRequest{Profile: b.ProfileName(), Browser: strings.Repeat("a", 64), Expires: time.Now().Add(time.Minute).Unix()}
			call := func(action string) ConsoleWorkspaceResult {
				t.Helper()
				r, err := b.ConsoleWorkspace(ctx, action, input)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			t.Cleanup(func() { call("disconnect") })
			pending := call("connect")
			if pending.Code == "" || !strings.Contains(b.withBrowserLinkForm("{}"), "browser_code") {
				t.Fatal("missing private link form")
			}
			event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: user}, Context: &callback.Context{OpenMessageID: "dm-" + user}, Action: &callback.CallBackAction{Value: map[string]any{managementActionKey: browserLinkAction}, FormValue: map[string]any{"browser_code": pending.Code}}}}
			// Only an actual stored delivery can attach a browser.
			if result := b.browserLinkCallback(ctx, event, browserLinkAction); result.Card != nil || call("get").State != "pending" {
				t.Fatal("unrecorded delivery accepted")
			}
			b.rememberPrivateManagementCard(user, event.Event.Context.OpenMessageID)
			receipt := b.browserLinkCallback(ctx, event, browserLinkAction)
			if receipt.Card == nil {
				t.Fatal("recorded private delivery did not link")
			}
			actions := func() map[string]string {
				w := &b.browserCards
				w.mu.Lock()
				defer w.mu.Unlock()
				out := map[string]string{}
				for id, c := range w.sessions[input.Browser].frame.controls {
					key, _ := c.values[managementActionKey].(string)
					out[key] = id
				}
				return out
			}
			home := call("get")
			if home.State != "connected" || home.Code != "" || home.Revision == 0 || actions()["schedule.list"] == "" {
				t.Fatal("missing linked personal workspace")
			}
			other := input
			other.Browser = strings.Repeat("b", 64)
			if r, err := b.ConsoleWorkspace(ctx, "get", other); err != nil || r.State != "disconnected" {
				t.Fatal("browser scopes overlap")
			}
			destinations := []string{"schedule.list"}
			if user == "owner" {
				destinations = append(destinations, "schedule.alerts", managementActionGuideOpen, capabilityAction+"open", managementActionHelpOpen, managementActionSystemMaintenance, managementActionCapabilitiesOpen, managementActionSettingsEdit, managementActionExperimentsOpen, managementActionPackLibraryOpen)
			} else if actions()[managementActionCapabilitiesOpen] != "" || actions()["schedule.alerts"] != "" {
				t.Fatal("member saw administrator controls")
			}
			for _, action := range destinations {
				input.Revision = call("get").Revision
				input.Control = actions()[action]
				if input.Control == "" {
					t.Fatalf("missing home control %s among %v", action, actions())
				}
				page := call("invoke")
				for deadline := time.Now().Add(3 * time.Second); string(page.Card) == string(home.Card) && (page.Toast == nil || page.Toast.Type != "error") && time.Now().Before(deadline); {
					time.Sleep(10 * time.Millisecond)
					page = call("get")
				}
				if page.Toast != nil && page.Toast.Type == "error" || len(page.Card) == 0 || string(page.Card) == string(home.Card) {
					t.Fatalf("destination %s did not open: %+v", action, page.Toast)
				}
				if _, err := b.ConsoleWorkspace(ctx, "invoke", input); status.Code(err) != codes.Aborted {
					t.Fatalf("old frame remained usable: %v", err)
				}
				input.Revision = page.Revision
				call("home")
			}
			input.ChatID = "oc_known"
			input.Revision = call("get").Revision
			if user == "owner" {
				if group := call("group"); !group.CanOpenGroup || actions()[browserTopicAction] == "" {
					t.Fatal("missing group management or bound topic")
				}
				input.Control = actions()[browserTopicAction]
				input.Revision = call("get").Revision
				input.Option = "topic-one"
				if topic := call("invoke"); !strings.Contains(string(topic.Card), "Topic title") {
					t.Fatal("topic status missing its title")
				}
				input.Revision = call("get").Revision
				input.ChatID = "oc_missing"
				if _, err := b.ConsoleWorkspace(ctx, "group", input); status.Code(err) != codes.NotFound {
					t.Fatal("unknown group opened", err)
				}
			} else if _, err := b.ConsoleWorkspace(ctx, "group", input); status.Code(err) != codes.PermissionDenied {
				t.Fatal("member opened owner group context", err)
			}
			b.browserLinkCallback(ctx, event, browserDisconnectAction)
			if call("get").State != "disconnected" {
				t.Fatal("private disconnect left workspace active")
			}
		})
	}
}

func TestBrowserWorkspaceFrameOwnsInputsAndPassword(t *testing.T) {
	t.Parallel()
	card := renderManagementConsoleCard(ManagementConsoleInfo{Enabled: true, URL: "http://localhost:7892", Password: "private-only-password", ExpiresAt: time.Now().Add(time.Minute)}, "", time.Now())
	if raw := string(browserFrame(card).card); strings.Contains(raw, "private-only-password") || !strings.Contains(raw, "localhost:7892") {
		t.Fatal("temporary password projection is invalid")
	}
	field := managementSelectStaticElement("mode", "Mode", "one", []cardOption{{Text: managementPlainText("One"), Value: "one"}})
	raw := marshalNativeManagementCard(managementCardView{Elements: []cardElement{{Tag: "form", Elements: []cardElement{field, managementFormSubmitButton("test.save", "Save")}}}})
	frame := browserFrame(raw)
	var id string
	for key, c := range frame.controls {
		if c.values[managementActionKey] == "test.save" {
			id = key
		}
	}
	good := ConsoleWorkspaceRequest{Control: id, Fields: map[string]string{"mode": "one"}}
	event, err := frame.action(good)
	if err != nil || event.Value[managementActionKey] != "test.save" || event.FormValue["mode"] != "one" {
		t.Fatal("form action was not preserved")
	}
	good.Fields["mode"] = "not-listed"
	if _, err := frame.action(good); err == nil {
		t.Fatal("unlisted option accepted")
	}
	if !json.Valid(frame.card) {
		t.Fatal("invalid projected card")
	}
}
