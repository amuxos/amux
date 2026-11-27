package bridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestGroupWatchDedupAllowsAlertAfterFailedRun(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	setGroupWatchDedupTestCatalog(b)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		DedupEnabled: true, DedupWindowSeconds: 1800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	hub.routeErrs = map[string]error{"Run": errors.New("runtime unavailable")}
	msg := groupWatchMessage{ChatID: "oc_alarm", MessageID: "om_first", SenderID: "ou_alarm_bot", SenderType: "app", Text: "Service unavailable\nRule ID: 2071"}
	_ = b.routeGroupWatchMessage(context.Background(), msg)
	if chat, err := b.db.FindByRootMsg(b.currentBridgeId(), msg.MessageID); err != nil || chat != nil {
		t.Fatalf("failed Run created binding: %+v err=%v", chat, err)
	}
	hub.routeErrs = nil
	hub.routes = nil
	msg.MessageID = "om_after_recovery"
	if err := b.routeGroupWatchMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(hub.routes) != 2 {
		t.Fatalf("after runtime recovery: got %d routes, want Run and Submit; first alert never created a Session", len(hub.routes))
	}
}

func setGroupWatchDedupTestCatalog(b *Bridge) {
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{Complete: true, Members: []externalBotCatalogMember{{BotOpenID: "ou_alarm_bot", BotAppID: "cli_alarm_bot"}}}, nil
	}
}

func TestGroupWatchDedupAcrossSenderIDFormsAndLookupFailure(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	setGroupWatchDedupTestCatalog(b)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"}, DedupEnabled: true, DedupWindowSeconds: 1800}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	msg := groupWatchMessage{ChatID: "oc_alarm", MessageID: "om_event", SenderID: "ou_alarm_bot", SenderType: "app", Text: "Error rate\nRule ID: 1"}
	goodFetch := b.externalBotCatalogFetch
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{}, errors.New("unavailable")
	}
	if err := b.routeGroupWatchMessage(context.Background(), msg); err == nil {
		t.Fatal("missing lookup failure")
	}
	b.externalBotCatalogFetch = goodFetch
	if err := b.routeGroupWatchMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = "om_history"
	msg.SenderID = "cli_alarm_bot"
	if err := b.routeGroupWatchMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(hub.routes) != 2 {
		t.Fatalf("routes=%d, want one Run/Submit across both sender forms", len(hub.routes))
	}
}

func TestGroupWatchDedupReconcilesPendingLaunch(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			b, hub := newBridgeForInputTest(t)
			a := groupWatchAlertDedup{BridgeID: b.currentBridgeId(), ChatID: "chat", SenderID: "app", Fingerprint: "hash", Window: 30 * time.Minute}
			old := time.Now().Add(-2 * time.Minute)
			claimed, err := b.db.ReserveGroupWatchAlert(a, "old", "target", "old-session", old)
			if err != nil || !claimed {
				t.Fatalf("reserve=%v %v", claimed, err)
			}
			hub.routeErrs = map[string]error{"List": errors.New("offline")}
			if _, err := b.claimGroupWatchAlert(context.Background(), &a, "new", "target", "new-session"); err == nil {
				t.Fatal("offline pending outcome was discarded")
			}
			pending, err := b.db.PendingGroupWatchAlert(a)
			if err != nil || pending == nil {
				t.Fatalf("pending lost: %v %v", pending, err)
			}
			hub.routeErrs = nil
			hub.listResp = &pb.ListResponse{}
			if exists {
				hub.listResp.Sessions = []*pb.Session{{Name: "old-session", State: "running"}}
			}
			claimed, err = b.claimGroupWatchAlert(context.Background(), &a, "new", "target", "new-session")
			if err != nil || !claimed || (a.Resume != nil) != exists {
				t.Fatalf("claimed=%v exists=%v err=%v", claimed, exists, err)
			}
		})
	}
}

func TestGroupWatchDedupConcurrentReservationsSurviveReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	a := groupWatchAlertDedup{BridgeID: "bridge", ChatID: "chat", SenderID: "app", Fingerprint: "hash", Window: 30 * time.Minute}
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := db.ReserveGroupWatchAlert(a, fmt.Sprint(i), "target", "session", time.Now())
			if err != nil {
				t.Error(err)
			}
			results <- claimed
		}(i)
	}
	wg.Wait()
	close(results)
	count := 0
	for claimed := range results {
		if claimed {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("winners=%d", count)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending, err := db.PendingGroupWatchAlert(a)
	if err != nil || pending == nil {
		t.Fatalf("reopen=%v %v", pending, err)
	}
	// An unrelated completion must not release another message's reservation.
	if err := db.FinishGroupWatchAlert(a, "unrelated", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ReserveGroupWatchAlert(a, "later", "target", "session", time.Now())
	if err != nil || claimed {
		t.Fatalf("pending bypassed=%v %v", claimed, err)
	}
	if err := db.FinishGroupWatchAlert(a, pending.MessageID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ReserveGroupWatchAlert(a, "later", "target", "session", time.Now())
	if err != nil || !claimed {
		t.Fatalf("failed launch not released=%v %v", claimed, err)
	}
}

func TestGroupWatchDedupRecoverySubmitsToExistingSession(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	setGroupWatchDedupTestCatalog(b)
	cfg := config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"}, DedupEnabled: true, DedupWindowSeconds: 1800}
	b.groupWatchConfig = cfg
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	msg := groupWatchMessage{ChatID: "oc_alarm", MessageID: "new-root", SenderID: "cli_alarm_bot", SenderType: "app", Text: "Error rate\nRule ID: 1"}
	a, err := b.prepareGroupWatchAlertDedup(context.Background(), cfg, msg)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := b.db.ReserveGroupWatchAlert(*a, "old-root", "target-1", "recovered", time.Now().Add(-2*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("reserve %v %v", claimed, err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "recovered", State: "created"}}}
	if err := b.routeGroupWatchMessage(context.Background(), msg); err == nil {
		t.Fatal("created state accepted")
	}
	msg.MessageID = "next-root"
	hub.listResp.Sessions[0].State = "running"
	hub.listResp.Sessions[0].AgentProfile = "builtin/codex"
	hub.listResp.Sessions[0].AgentType = "codex"
	if err := b.routeGroupWatchMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes=%+v; want reuse and Submit, no Run", hub.routes)
	}
	accepted, err := b.db.GroupWatchAlertAccepted(*a, time.Now())
	if err != nil || !accepted {
		t.Fatalf("accepted %v %v", accepted, err)
	}
}

func TestGroupWatchDedupSkipsImageOnlyPosts(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	cfg := config.BridgeFeishuGroupWatchConfig{DedupEnabled: true}
	for _, imageKey := range []string{"image-one", "image-two"} {
		content := fmt.Sprintf(`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":%q}]]}}`, imageKey)
		msg := groupWatchMessage{SenderType: "app", Kind: "post", Content: &content, Text: "[图片素材]"}
		dedup, err := b.prepareGroupWatchAlertDedup(context.Background(), cfg, msg)
		if err != nil || dedup != nil {
			t.Fatalf("material placeholder dedup=%v err=%v", dedup, err)
		}
	}
}

func TestGroupWatchDedupUnknownSubmitDoesNotReplayWithinWindow(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	a := groupWatchAlertDedup{BridgeID: b.currentBridgeId(), ChatID: "chat", SenderID: "app", Fingerprint: "fingerprint", Window: 30 * time.Minute}
	now := time.Now()
	started := now.Add(-2 * time.Minute)
	claimed, err := b.db.ReserveGroupWatchAlert(a, "old", "target", "session", started)
	if err != nil || !claimed {
		t.Fatalf("reserve %v %v", claimed, err)
	}
	if err := b.db.StartGroupWatchAlertSubmission(a, "old", started); err != nil {
		t.Fatal(err)
	}
	// No local turn receipt, but Runtime may already be handling this input.
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "session", State: "archived"}}}
	claimed, err = b.claimGroupWatchAlert(context.Background(), &a, "new", "target", "new-session")
	if err != nil || claimed || a.Resume != nil {
		t.Fatalf("unknown Submit replayed: %v %v", claimed, err)
	}
	if err := b.db.StartGroupWatchAlertSubmission(a, "old", now.Add(-31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimed, err = b.claimGroupWatchAlert(context.Background(), &a, "after-window", "target", "new-session")
	if err != nil || !claimed {
		t.Fatalf("expired unknown Submit blocked new window: %v %v", claimed, err)
	}
}

func TestGroupWatchDedupArchivedUnsubmittedSessionReleasesClaim(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	a := groupWatchAlertDedup{BridgeID: b.currentBridgeId(), ChatID: "chat", SenderID: "app", Fingerprint: "fingerprint", Window: 30 * time.Minute}
	claimed, err := b.db.ReserveGroupWatchAlert(a, "old", "target", "session", time.Now().Add(-2*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("reserve %v %v", claimed, err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "session", State: "archived"}}}
	claimed, err = b.claimGroupWatchAlert(context.Background(), &a, "new", "target", "new-session")
	if err != nil || !claimed {
		t.Fatalf("terminal session blocked new launch: %v %v", claimed, err)
	}
}
