package feishuexchange

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

type fakeHost struct {
	bridgeID    string
	profileName string
	store       *Store
	binding     *InitiatorBinding
	ready       bool
	validateErr error
	sendFn      func(ctx context.Context, chatID, threadRootID, botOpenID, input string) (string, error)
}

func (h *fakeHost) BridgeID() string                                          { return h.bridgeID }
func (h *fakeHost) ProfileName() string                                       { return h.profileName }
func (h *fakeHost) ExchangeStore() *Store                                     { return h.store }
func (h *fakeHost) ResolveInitiatorBinding(string) (*InitiatorBinding, error) { return h.binding, nil }
func (h *fakeHost) ExternalBotReady(string) (bool, error)                     { return h.ready, nil }
func (h *fakeHost) ValidateExternalBotCreate(context.Context, string) error   { return h.validateErr }
func (h *fakeHost) RefreshExternalBotChatBestEffort(context.Context, string)  {}
func (h *fakeHost) SendExternalBotDelegation(ctx context.Context, chatID, threadRootID, botOpenID, input string) (string, error) {
	if h.sendFn != nil {
		return h.sendFn(ctx, chatID, threadRootID, botOpenID, input)
	}
	return "om-sent", nil
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "exchange.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return NewStore(db)
}

func TestAdapterLifecycleWithoutBridgeDependency(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1000, 0).UTC()
	peer := Peer{
		CollaboratorID:  CollaboratorID("bridge-1", "oc-chat", "ou-peer"),
		BridgeID:        "bridge-1",
		ChatID:          "oc-chat",
		BotOpenID:       "ou-peer",
		BotDisplayName:  "docman",
		ChatDisplayName: "engineering",
		LastSeenAt:      now,
	}
	if err := store.UpsertPeer(peer); err != nil {
		t.Fatalf("UpsertPeer: %v", err)
	}
	sends := 0
	host := &fakeHost{
		bridgeID:    "bridge-1",
		profileName: "default",
		store:       store,
		binding:     &InitiatorBinding{BridgeID: "bridge-1", ChatID: "oc-chat", RootMsgID: "om-root"},
		ready:       true,
		sendFn: func(context.Context, string, string, string, string) (string, error) {
			sends++
			return "om-card-1", nil
		},
	}
	adapter, err := NewAdapter([]Host{host})
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	adapter.SetNow(func() time.Time { return now })

	scope := coordination.CatalogScope{InitiatorRef: "amux-session:s1"}
	list, err := adapter.List(context.Background(), scope)
	if err != nil || len(list) != 1 || list[0].Name != "docman" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	resolved, err := adapter.ResolveTarget(context.Background(), peer.CollaboratorID, scope)
	if err != nil || resolved.BackendKind != coordination.BackendKindFeishuBot || resolved.TrustLevel != coordination.TrustExternalUntrusted {
		t.Fatalf("ResolveTarget = %+v, %v", resolved, err)
	}
	route := &coordination.Route{
		ID: "del-1", BackendKind: coordination.BackendKindFeishuBot, CollaboratorID: peer.CollaboratorID,
		TargetRef: resolved.TargetRef, InitiatorRef: scope.InitiatorRef, Input: "check",
		Deadline: now.Add(time.Minute), TrustLevel: coordination.TrustExternalUntrusted, CreatedAt: now,
	}
	ref, err := adapter.Create(context.Background(), route)
	if err != nil || ref != ExchangePrefix+"del-1" {
		t.Fatalf("Create = %q, %v", ref, err)
	}
	route.BackendRef = ref
	// Idempotent replay must not send twice.
	if ref2, err := adapter.Create(context.Background(), route); err != nil || ref2 != ref || sends != 1 {
		t.Fatalf("replay Create = %q, %v, sends=%d", ref2, err, sends)
	}
	// Concurrent second delegation to same thread+Bot must be rejected.
	route2 := *route
	route2.ID = "del-2"
	route2.BackendRef = ""
	if _, err := adapter.Create(context.Background(), &route2); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("concurrent Create err = %v", err)
	}
	// Reply completes exchange with untrusted envelope.
	now = now.Add(2 * time.Second)
	handled, err := store.CompleteReply("bridge-1", "om-root", "ou-peer", "om-reply", "all clear", now)
	if err != nil || !handled {
		t.Fatalf("CompleteReply = %v, %v", handled, err)
	}
	snap, err := adapter.Wait(context.Background(), route, 2)
	if err != nil || snap.Delegation.Status != coordination.StatusSucceeded || snap.Delegation.Result == nil || snap.Delegation.Result.Content != "all clear" {
		t.Fatalf("Wait = %+v, %v", snap, err)
	}
	if status.Code(adapter.Cancel(context.Background(), route)) != codes.FailedPrecondition {
		t.Fatal("Cancel must return FailedPrecondition")
	}
	if status.Code(adapter.RespondPermission(context.Background(), route, coordination.PermissionResponse{})) != codes.FailedPrecondition {
		t.Fatal("RespondPermission must return FailedPrecondition")
	}
}

func TestStoreRecoveryAndDeadline(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(2000, 0).UTC()
	for _, ex := range []Exchange{
		{BackendRef: ExchangePrefix + "pending", BridgeID: "b1", CollaboratorID: "external-bot:p", ChatID: "oc-1", BotOpenID: "ou-1", Deadline: now.Add(time.Minute), CreatedAt: now},
		{BackendRef: ExchangePrefix + "running", BridgeID: "b1", CollaboratorID: "external-bot:r", ChatID: "oc-1", BotOpenID: "ou-2", Deadline: now.Add(5 * time.Second), CreatedAt: now},
	} {
		if _, err := store.CreateExchange(ex); err != nil {
			t.Fatalf("CreateExchange: %v", err)
		}
	}
	if err := store.MarkExchangeRunning(ExchangePrefix+"running", "om-root-2", now); err != nil {
		t.Fatalf("MarkExchangeRunning: %v", err)
	}
	if err := store.RecoverExchanges("b1", now); err != nil {
		t.Fatalf("RecoverExchanges: %v", err)
	}
	pending, _, _, err := store.ReadExchange(ExchangePrefix+"pending", 0, now)
	if err != nil || pending.Status != coordination.StatusFailed || !strings.Contains(pending.FailureMessage, "unknown after restart") {
		t.Fatalf("pending after recovery = %+v, %v", pending, err)
	}
	running, _, _, err := store.ReadExchange(ExchangePrefix+"running", 0, now.Add(10*time.Second))
	if err != nil || running.Status != coordination.StatusTimedOut || running.FailureCode != coordination.FailureDeadlineExceeded {
		t.Fatalf("running after deadline = %+v, %v", running, err)
	}
}
