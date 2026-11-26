package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/coordination"
	"github.com/amuxos/amux/internal/coordination/feishuexchange"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newExternalBotCoordinationAdapterForTest(bridges []*Bridge) (*feishuexchange.Adapter, error) {
	hosts := make([]feishuexchange.Host, 0, len(bridges))
	for _, b := range bridges {
		if b != nil {
			hosts = append(hosts, b.ExternalBotHost())
		}
	}
	return feishuexchange.NewAdapter(hosts)
}

func prepareExternalBotPeerForCreate(t *testing.T, b *Bridge, peer externalBotPeer) {
	t.Helper()
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), peer.ChatID, WorkspaceModeShared); err != nil {
		t.Fatal(err)
	}
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{
			ChatDisplayName: "devbox",
			Members:         []externalBotCatalogMember{{BotOpenID: peer.BotOpenID, BotAppID: peer.BotAppID, DisplayName: peer.BotDisplayName}},
			Complete:        true,
		}, nil
	}
}

func bindExternalBotInitiator(t *testing.T, b *Bridge, hub *bridgeTestHub, chatID string) coordination.CatalogScope {
	t.Helper()
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: chatID, RootMsgID: "om-initiator-root", ThreadID: "omt-initiator",
		TargetID: "target-1", TargetName: "local", SessionName: "session-main", Status: ChatStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub.targets = []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-main", Name: "session-main"}}}}
	return coordination.CatalogScope{InitiatorRef: "amux-session:session-id-main"}
}

func TestExternalBotCoordinationAdapterRepliesInInitiatorThread(t *testing.T) {
	var messagePath string
	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
			return
		}
		messagePath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
			t.Errorf("decode message body: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"message_id":"om-delegation","thread_id":"omt-origin"}}`)
	}))
	t.Cleanup(server.Close)

	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli-amux", AppSecret: "secret", BotOpenId: "ou-amux"}
	b.client = lark.NewClient("cli-amux", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-docman"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-docman", BotAppID: "cli-docman",
		BotDisplayName: "docman", ChatDisplayName: "devbox", LastSeenAt: time.Now().UTC(),
	}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc-shared", RootMsgID: "om-user-root", ThreadID: "omt-origin",
		TargetID: "target-1", TargetName: "local", SessionName: "session-main", Status: ChatStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub.targets = []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-main", Name: "session-main"}}}}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, coordination.CatalogScope{InitiatorRef: "amux-session:session-id-main"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Create(context.Background(), &coordination.Route{
		ID: "delegation-thread", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef,
		InitiatorRef: "amux-session:session-id-main", ScopeRef: "turn:thread", Input: "review in this thread",
		Deadline: time.Now().Add(time.Minute), TrustLevel: coordination.TrustExternalUntrusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/open-apis/im/v1/messages/om-user-root/reply"; messagePath != want {
		t.Fatalf("message path = %q, want %q (delegation must stay in initiator thread)", messagePath, want)
	}
	if messageBody["reply_in_thread"] != true {
		t.Fatalf("message body = %+v, want reply_in_thread=true", messageBody)
	}
	if messageBody["msg_type"] != "interactive" {
		t.Fatalf("message body = %+v, want Card JSON interactive message", messageBody)
	}
	content, ok := messageBody["content"].(string)
	if !ok {
		t.Fatalf("message content = %#v, want encoded Card JSON", messageBody["content"])
	}
	var card cardDoc
	if err := json.Unmarshal([]byte(content), &card); err != nil || card.Schema != "2.0" {
		t.Fatalf("message content = %q, want Card JSON 2.0: %v", content, err)
	}
	if _, ok := findCardElementByID(card.Body.Elements, cardKitReplyElementID); !ok {
		t.Fatalf("message card = %+v, want compatible answer envelope", card)
	}
}

func TestExternalBotCoordinationAdapterRejectsCrossGroupInitiator(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-target", "ou-reviewer"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-target", BotOpenID: "ou-reviewer",
		BotDisplayName: "reviewer", ChatDisplayName: "target", LastSeenAt: time.Now().UTC(),
	}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc-source", RootMsgID: "om-source-root",
		TargetID: "target-1", TargetName: "local", SessionName: "session-main", Status: ChatStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub.targets = []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-main", Name: "session-main"}}}}
	sent := false
	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		sent = true
		return "om-cross-group", "", nil
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Create(context.Background(), &coordination.Route{
		ID: "cross-group", CollaboratorID: peer.CollaboratorID, TargetRef: feishuexchange.EncodeTarget(peer),
		InitiatorRef: "amux-session:session-id-main", ScopeRef: "turn:1", Input: "review",
		Deadline: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "current Feishu group") {
		t.Fatalf("cross-group Create error = %v, want current-group FailedPrecondition", err)
	}
	if sent {
		t.Fatal("cross-group delegation was sent")
	}
}

func TestExternalBotCoordinationAdapterListsOnlyInitiatorCurrentGroup(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	for _, chatID := range []string{"oc-source", "oc-other"} {
		if err := b.db.SetWorkspaceMode(b.currentBridgeId(), chatID, WorkspaceModeShared); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc-source", RootMsgID: "om-source-root",
		TargetID: "target-1", TargetName: "local", SessionName: "session-main", Status: ChatStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub.targets = []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-main", Name: "session-main"}}}}
	b.externalBotCatalogFetch = func(_ context.Context, chatID string) (externalBotCatalogSnapshot, error) {
		switch chatID {
		case "oc-source":
			return externalBotCatalogSnapshot{ChatDisplayName: "source", Members: []externalBotCatalogMember{{BotOpenID: "ou-source", DisplayName: "source-bot"}}, Complete: true}, nil
		case "oc-other":
			return externalBotCatalogSnapshot{ChatDisplayName: "other", Members: []externalBotCatalogMember{{BotOpenID: "ou-other", DisplayName: "other-bot"}}, Complete: true}, nil
		default:
			return externalBotCatalogSnapshot{}, fmt.Errorf("unexpected chat %q", chatID)
		}
	}
	if err := b.refreshKnownExternalBotCatalog(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var refreshedChats []string
	b.externalBotCatalogSeen = nil
	b.externalBotCatalogFetch = func(_ context.Context, chatID string) (externalBotCatalogSnapshot, error) {
		refreshedChats = append(refreshedChats, chatID)
		switch chatID {
		case "oc-source":
			return externalBotCatalogSnapshot{ChatDisplayName: "source", Members: []externalBotCatalogMember{{BotOpenID: "ou-source", DisplayName: "source-bot"}}, Complete: true}, nil
		case "oc-other":
			return externalBotCatalogSnapshot{ChatDisplayName: "other", Members: []externalBotCatalogMember{{BotOpenID: "ou-other", DisplayName: "other-bot"}}, Complete: true}, nil
		default:
			return externalBotCatalogSnapshot{}, fmt.Errorf("unexpected chat %q", chatID)
		}
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	scope := coordination.CatalogScope{InitiatorRef: "amux-session:session-id-main"}
	items, err := adapter.List(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "source-bot" {
		t.Fatalf("current-group collaborators = %+v, want only source-bot", items)
	}
	if len(refreshedChats) != 1 || refreshedChats[0] != "oc-source" {
		t.Fatalf("catalog refreshed chats = %v, want only current group", refreshedChats)
	}
	otherID := externalBotCollaboratorID(b.currentBridgeId(), "oc-other", "ou-other")
	if _, err := adapter.ResolveTarget(context.Background(), otherID, scope); err == nil {
		t.Fatal("ResolveTarget accepted a collaborator from another group")
	}
}

func TestExternalBotCoordinationAdapterHidesAndRejectsWithoutCurrentGroup(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-reviewer"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-reviewer", LastSeenAt: time.Now().UTC(),
	}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	scope := coordination.CatalogScope{InitiatorRef: "mcp:main"}
	items, err := adapter.List(context.Background(), scope)
	if err != nil || len(items) != 0 {
		t.Fatalf("non-Feishu List = %+v, %v; want no external Bot", items, err)
	}
	if _, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope); err == nil {
		t.Fatal("ResolveTarget accepted external Bot without a current Feishu group")
	}
	sent := false
	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		sent = true
		return "om-should-not-send", "", nil
	}
	_, err = adapter.Create(context.Background(), &coordination.Route{
		ID: "no-current-group", CollaboratorID: peer.CollaboratorID, TargetRef: feishuexchange.EncodeTarget(peer),
		InitiatorRef: scope.InitiatorRef, ScopeRef: "turn:1", Input: "review", Deadline: time.Now().Add(time.Minute),
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "current Feishu group") {
		t.Fatalf("non-Feishu Create error = %v, want current-group FailedPrecondition", err)
	}
	if sent {
		t.Fatal("external Bot delegation was sent without a current Feishu group")
	}
}

func TestExternalBotCoordinationAdapterCreatesAndCompletesExchange(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{BotOpenId: "ou-amux", BotName: "amux"}
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-reviewer"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-reviewer", BotAppID: "cli-reviewer",
		BotDisplayName: "docman", ChatDisplayName: "devbox", LastSeenAt: time.Unix(100, 0).UTC(),
	}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatalf("UpsertExternalBotPeer: %v", err)
	}
	var sentChat, sentRoot, sentBot, sentTask string
	sendCount := 0
	b.externalBotDelegation = func(_ context.Context, chatID, rootMsgID, botOpenID, task string) (string, string, error) {
		sendCount++
		sentChat, sentRoot, sentBot, sentTask = chatID, rootMsgID, botOpenID, task
		return "om-exchange-root", "omt-exchange", nil
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatalf("NewExternalBotCoordinationAdapter: %v", err)
	}
	now := time.Unix(200, 0).UTC()
	adapter.SetNow(func() time.Time { return now })
	scope := bindExternalBotInitiator(t, b, hub, peer.ChatID)

	items, err := adapter.List(context.Background(), scope)
	if err != nil || len(items) != 1 {
		t.Fatalf("List = %+v, %v", items, err)
	}
	if items[0].Kind != coordination.CollaboratorExternalBot || items[0].Availability != coordination.AvailabilityBestEffort || len(items[0].Capabilities) != 0 {
		t.Fatalf("descriptor = %+v", items[0])
	}
	if items[0].Name != "docman" || !strings.Contains(items[0].Description, "devbox") {
		t.Fatalf("human-readable descriptor = %+v", items[0])
	}
	resolved, err := adapter.ResolveTarget(context.Background(), items[0].ID, scope)
	if err != nil || resolved.BackendKind != coordination.BackendKindFeishuBot || resolved.TrustLevel != coordination.TrustExternalUntrusted {
		t.Fatalf("ResolveTarget = %+v, %v", resolved, err)
	}
	route := &coordination.Route{
		ID: "delegation-1", BackendKind: coordination.BackendKindFeishuBot, CollaboratorID: items[0].ID,
		TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, ScopeRef: "turn:1", Input: "review this design",
		Deadline: now.Add(time.Minute), TrustLevel: coordination.TrustExternalUntrusted, CreatedAt: now,
	}
	ref, err := adapter.Create(context.Background(), route)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	route.BackendRef = ref
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{ChatDisplayName: "devbox", Complete: true}, nil
	}
	if replayedRef, replayErr := adapter.Create(context.Background(), route); replayErr != nil || replayedRef != ref {
		t.Fatalf("idempotent Create = %q, %v", replayedRef, replayErr)
	}
	if sendCount != 1 {
		t.Fatalf("idempotent Create sent %d messages", sendCount)
	}
	if sentChat != "oc-shared" || sentRoot != "om-initiator-root" || sentBot != "ou-reviewer" || sentTask != "review this design" {
		t.Fatalf("sent = %q %q %q %q", sentChat, sentRoot, sentBot, sentTask)
	}
	running, err := adapter.Get(context.Background(), route)
	if err != nil || running.Delegation.Status != coordination.StatusRunning || running.Delegation.ReplyTo != "om-initiator-root" {
		t.Fatalf("Get running = %+v, %v", running, err)
	}

	now = now.Add(time.Second)
	handled, err := b.db.CompleteExternalBotExchangeReply(b.currentBridgeId(), "om-initiator-root", "ou-reviewer", "om-result", "looks good", now)
	if err != nil || !handled {
		t.Fatalf("CompleteExternalBotExchangeReply = %v, %v", handled, err)
	}
	waited, err := adapter.Wait(context.Background(), route, 2)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Delegation.Status != coordination.StatusSucceeded || waited.Delegation.Result == nil || waited.Delegation.Result.Content != "looks good" || waited.Delegation.Result.TrustLevel != coordination.TrustExternalUntrusted {
		t.Fatalf("Wait result = %+v", waited)
	}
	if len(waited.Events) != 1 || waited.Events[0].Kind != coordination.EventResult || waited.NextSeq != 3 {
		t.Fatalf("Wait events = %+v next=%d", waited.Events, waited.NextSeq)
	}
}

func TestExternalBotCoordinationAdapterSerializesSameThreadBot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{BotOpenId: "ou-amux"}
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-docman"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-docman", LastSeenAt: time.Now().UTC(),
	}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: peer.ChatID, RootMsgID: "om-user-root", ThreadID: "omt-origin",
		TargetID: "target-1", SessionName: "session-main", Status: ChatStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub.targets = []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-main", Name: "session-main"}}}}
	sendCount := 0
	b.externalBotDelegation = func(_ context.Context, _, rootMsgID, _, _ string) (string, string, error) {
		sendCount++
		if rootMsgID != "om-user-root" {
			t.Fatalf("rootMsgID = %q", rootMsgID)
		}
		return fmt.Sprintf("om-request-%d", sendCount), "omt-origin", nil
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, coordination.CatalogScope{InitiatorRef: "amux-session:session-id-main"})
	if err != nil {
		t.Fatal(err)
	}
	newRoute := func(id string) *coordination.Route {
		return &coordination.Route{
			ID: id, CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef,
			InitiatorRef: "amux-session:session-id-main", ScopeRef: "turn:" + id, Input: "task",
			Deadline: time.Now().Add(time.Minute), TrustLevel: coordination.TrustExternalUntrusted,
		}
	}
	first := newRoute("first")
	first.BackendRef, err = adapter.Create(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := newRoute("second")
	if _, err := adapter.Create(context.Background(), second); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("concurrent Create error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount = %d, want 1 before first terminal", sendCount)
	}
	if handled, err := b.db.CompleteExternalBotExchangeReply(b.currentBridgeId(), "om-user-root", peer.BotOpenID, "om-result", "done", time.Now()); err != nil || !handled {
		t.Fatalf("complete first = %v, %v", handled, err)
	}
	second.BackendRef, err = adapter.Create(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if sendCount != 2 {
		t.Fatalf("sendCount = %d, want sequential second send", sendCount)
	}
}

func TestExternalBotCoordinationAdapterRecordsDeliveryFailureAndTimeout(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	peer := externalBotPeer{CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-bot"), BridgeID: b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-bot", LastSeenAt: time.Now()}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0).UTC()
	adapter.SetNow(func() time.Time { return now })
	scope := bindExternalBotInitiator(t, b, hub, peer.ChatID)
	resolved, _ := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope)
	route := &coordination.Route{ID: "delivery-fails", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "task", Deadline: now.Add(time.Minute), CreatedAt: now}
	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		return "", "", errors.New("send denied")
	}
	ref, err := adapter.Create(context.Background(), route)
	if err != nil {
		t.Fatalf("Create failure exchange: %v", err)
	}
	route.BackendRef = ref
	failed, err := adapter.Get(context.Background(), route)
	if err != nil || failed.Delegation.Status != coordination.StatusFailed || failed.Delegation.Failure == nil || !strings.Contains(failed.Delegation.Failure.Message, "send denied") {
		t.Fatalf("failed snapshot = %+v, %v", failed, err)
	}

	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		return "om-timeout", "", nil
	}
	timedRoute := &coordination.Route{ID: "times-out", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "task", Deadline: now.Add(time.Second), CreatedAt: now}
	timedRoute.BackendRef, err = adapter.Create(context.Background(), timedRoute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	timed, err := adapter.Get(context.Background(), timedRoute)
	if err != nil || timed.Delegation.Status != coordination.StatusTimedOut || timed.Delegation.Failure.Code != coordination.FailureDeadlineExceeded {
		t.Fatalf("timed snapshot = %+v, %v", timed, err)
	}
	if code := status.Code(adapter.Cancel(context.Background(), timedRoute)); code != codes.FailedPrecondition {
		t.Fatalf("Cancel status = %s", code)
	}
}

func TestExternalBotCoordinationRecoveryFailsOnlyAmbiguousPendingSend(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	now := time.Unix(400, 0).UTC()
	for _, exchange := range []externalBotExchange{
		{BackendRef: "feishu-exchange:pending", BridgeID: b.currentBridgeId(), CollaboratorID: "external-bot:pending", ChatID: "oc-shared", BotOpenID: "ou-bot", Deadline: now.Add(time.Minute), CreatedAt: now},
		{BackendRef: "feishu-exchange:running", BridgeID: b.currentBridgeId(), CollaboratorID: "external-bot:running", ChatID: "oc-shared", BotOpenID: "ou-bot", Deadline: now.Add(time.Minute), CreatedAt: now},
	} {
		if _, err := b.db.CreateExternalBotExchange(exchange); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.db.MarkExternalBotExchangeRunning("feishu-exchange:running", "om-root", now); err != nil {
		t.Fatal(err)
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	adapter.SetNow(func() time.Time { return now })
	pending, _, _, err := b.db.externalBotStore().ReadExchange("feishu-exchange:pending", 0, now)
	if err != nil || pending.Status != coordination.StatusFailed || !strings.Contains(pending.FailureMessage, "unknown after restart") {
		t.Fatalf("pending recovery = %+v, %v", pending, err)
	}
	running, _, _, err := b.db.externalBotStore().ReadExchange("feishu-exchange:running", 0, now)
	if err != nil || running.Status != coordination.StatusRunning || running.RootMessageID != "om-root" {
		t.Fatalf("running recovery = %+v, %v", running, err)
	}
}

func TestExternalBotCoordinationIsHiddenWhenBotToBotIsDisabled(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	peer := externalBotPeer{CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-bot"), BridgeID: b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-bot", LastSeenAt: time.Now()}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	b.botToBot.Enabled = true
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	scope := bindExternalBotInitiator(t, b, hub, peer.ChatID)
	resolved, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope)
	if err != nil {
		t.Fatal(err)
	}
	b.botToBot.Enabled = false
	items, err := adapter.List(context.Background(), scope)
	if err != nil || len(items) != 0 {
		t.Fatalf("disabled List = %+v, %v", items, err)
	}
	route := &coordination.Route{ID: "disabled", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "task", Deadline: time.Now().Add(time.Minute), CreatedAt: time.Now()}
	if _, err := adapter.Create(context.Background(), route); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("disabled Create error = %v", err)
	}
}

func TestExternalBotCoordinationCreateRejectsBotThatLeftGroup(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	peer := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-left"),
		BridgeID:       b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-left", LastSeenAt: time.Now(),
	}
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), peer.ChatID, WorkspaceModeShared); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	scope := bindExternalBotInitiator(t, b, hub, peer.ChatID)
	resolved, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope)
	if err != nil {
		t.Fatal(err)
	}
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{ChatDisplayName: "devbox", Complete: true}, nil
	}
	sent := false
	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		sent = true
		return "om-should-not-send", "", nil
	}
	route := &coordination.Route{ID: "left-group", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "task", Deadline: time.Now().Add(time.Minute), CreatedAt: time.Now()}
	if _, err := adapter.Create(context.Background(), route); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "no longer a member") {
		t.Fatalf("Create error = %v", err)
	}
	if sent {
		t.Fatal("delegation was sent after membership verification removed the Bot")
	}
}

func TestExternalBotReplyIsConsumedByExchangeBeforeLegacyBotToBot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "ou-owner", BotOpenId: "ou-amux", BotName: "amux"}
	b.botToBot.Enabled = true
	b.botToBot.MaxTurnsPerThread = 10
	b.botE2EConfig = config.BridgeFeishuBotE2EConfig{SenderOpenIDs: []string{"ou-reviewer"}}
	peer := externalBotPeer{CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), "oc-shared", "ou-reviewer"), BridgeID: b.currentBridgeId(), ChatID: "oc-shared", BotOpenID: "ou-reviewer", LastSeenAt: time.Now()}
	prepareExternalBotPeerForCreate(t, b, peer)
	if err := b.db.UpsertExternalBotPeer(peer); err != nil {
		t.Fatal(err)
	}
	scope := bindExternalBotInitiator(t, b, hub, peer.ChatID)
	b.externalBotDelegation = func(context.Context, string, string, string, string) (string, string, error) {
		return "om-exchange-root", "", nil
	}
	adapter, err := newExternalBotCoordinationAdapterForTest([]*Bridge{b})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope)
	route := &coordination.Route{ID: "delegation-1", CollaboratorID: peer.CollaboratorID, TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "review", Deadline: time.Now().Add(time.Minute), CreatedAt: time.Now()}
	route.BackendRef, err = adapter.Create(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	event := feishuBotMessageEvent("topic_group", "oc-shared", "om-initiator-root", "om-reply", "ou-reviewer", "@_bot /e2e looks good")
	addStructuredBotMention(event, "@_bot", "amux", "ou-amux")
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("legacy routes = %+v, want exchange-only consumption", hub.routes)
	}
	got, err := adapter.Get(context.Background(), route)
	if err != nil || got.Delegation.Status != coordination.StatusSucceeded || got.Delegation.Result.Content != "/e2e looks good" || got.Delegation.Result.TrustLevel != coordination.TrustExternalUntrusted {
		t.Fatalf("exchange result = %+v, %v", got, err)
	}
}

func TestFailedExternalBotExchangeWithKnownRootConsumesLateReply(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{BotOpenId: "ou-amux", BotName: "amux"}
	b.botToBot.Enabled = true
	now := time.Unix(500, 0).UTC()
	ref := "feishu-exchange:failed-with-root"
	if _, err := b.db.CreateExternalBotExchange(externalBotExchange{
		BackendRef: ref, BridgeID: b.currentBridgeId(), CollaboratorID: "external-bot:reviewer",
		ChatID: "oc-shared", BotOpenID: "ou-reviewer", Deadline: now.Add(time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.externalBotStore().FailExchangeWithRoot(ref, "om-known-root", coordination.FailureBackendFailed, "local correlation write failed", now); err != nil {
		t.Fatal(err)
	}
	event := feishuBotMessageEvent("topic_group", "oc-shared", "om-known-root", "om-late", "ou-reviewer", "@_bot late result")
	addStructuredBotMention(event, "@_bot", "amux", "ou-amux")
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("late terminal reply fell through to legacy routes: %+v", hub.routes)
	}
}

func TestPendingExternalBotExchangeDoesNotConsumeReply(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	now := time.Unix(600, 0).UTC()
	ref := "feishu-exchange:pending-with-root"
	if _, err := b.db.CreateExternalBotExchange(externalBotExchange{
		BackendRef: ref, BridgeID: b.currentBridgeId(), CollaboratorID: "external-bot:reviewer",
		ChatID: "oc-shared", BotOpenID: "ou-reviewer", RootMessageID: "om-known-root",
		Deadline: now.Add(time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	handled, err := b.db.CompleteExternalBotExchangeReply(
		b.currentBridgeId(), "om-known-root", "ou-reviewer", "om-before-running", "stale result", now,
	)
	if err != nil || handled {
		t.Fatalf("pending reply match = %v, %v; want unhandled", handled, err)
	}
	exchange, _, _, err := b.db.externalBotStore().ReadExchange(ref, 0, now)
	if err != nil || exchange.Status != coordination.StatusPending {
		t.Fatalf("pending exchange = %+v, %v", exchange, err)
	}
}
