package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestHubCreateAndUpdateScheduleDeliveryDefaultsAndValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	store, err := schedule.OpenStore(filepath.Join(home, "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := os.MkdirAll(filepath.Dir(bridge.DefaultDBPath()), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", bridge.DefaultDBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE feishu_turn_links (bridge_id TEXT, root_msg_id TEXT, message_id TEXT, open_id TEXT, target_id TEXT, session_id TEXT, session_name TEXT, launch_id TEXT, turn_id TEXT, submitted_at INTEGER);
		CREATE TABLE lark_chats (id INTEGER PRIMARY KEY AUTOINCREMENT, root_msg_id TEXT, chat_id TEXT, target_id TEXT, session_name TEXT, bridge_id TEXT);
		INSERT INTO feishu_turn_links VALUES('bot-1', 'om_topic_root', 'm1', 'ou_alice', 'runtime', 'sess-feishu-1', 'feishu-s1', 'launch-1', 'turn-1', 1);
		INSERT INTO lark_chats (root_msg_id, chat_id, target_id, session_name, bridge_id) VALUES('om_topic_root', 'oc_group_1', 'runtime', 'feishu-s1', 'bot-1');
	`)
	if err != nil {
		t.Fatal(err)
	}

	principals := agentTokenTestStore(t)
	alice := scheduleHuman(t, principals, "bot-1", "ou_alice")
	credential, err := principals.IssueAgentCredential(alice.ID, sessionAgentCredentialSubject("runtime", "feishu-s1"), principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRouterHub{routeFn: func(target, method string, payload interface{}) (*pb.TunnelMsg, error) {
		raw, _ := json.Marshal(&pb.ActiveTurnResponse{Active: true, TurnId: "turn-1", SessionId: "sess-feishu-1"})
		return &pb.TunnelMsg{Payload: raw}, nil
	}}
	srv := &hubServer{schedule: store, principals: principals, scheduleRuntime: runtime}

	mcpCtx := actorCtx(credential.Token)
	specJSON, _ := json.Marshal(task.Spec{Agent: "builtin/claude", Task: "summarize alerts"})

	// 1) Feishu-originated MCP subtask omitting delivery_target auto-defaults to origin + always
	//    and captures OriginRootMsgID + OriginChatID from bridge.db.
	resp, err := srv.CreateSchedule(mcpCtx, &pb.CreateScheduleRequest{
		Name:         "morning-brief",
		Spec:         "daily 09:00",
		ScheduleType: schedule.ScheduleTypeSubtask,
		TaskSpecJson: specJSON,
		CreatedBy:    "mcp",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("CreateSchedule resp.Error = %q", resp.Error)
	}
	if resp.DeliveryTarget != schedule.DeliveryTargetOrigin || resp.DeliveryPolicy != schedule.DeliveryPolicyAlways {
		t.Fatalf("auto-default delivery = (%q, %q), want (origin, always)", resp.DeliveryTarget, resp.DeliveryPolicy)
	}
	vis, err := store.Visibility(resp.Id)
	if err != nil {
		t.Fatalf("Visibility: %v", err)
	}
	if vis.Creator.OriginRootMsgID != "om_topic_root" || vis.Creator.OriginChatID != "oc_group_1" || vis.Creator.OpenID != "ou_alice" {
		t.Fatalf("captured creator origin = %+v", vis.Creator)
	}

	// 2) UpdateSchedule changes delivery_target & delivery_policy in place.
	newTarget := schedule.DeliveryTargetCreatorDM
	newPolicy := schedule.DeliveryPolicyOnNonEmpty
	upd, err := srv.UpdateSchedule(mcpCtx, &pb.UpdateScheduleRequest{
		Id:             resp.Id,
		DeliveryTarget: &newTarget,
		DeliveryPolicy: &newPolicy,
	})
	if err != nil || upd.Error != "" {
		t.Fatalf("UpdateSchedule: err=%v respErr=%q", err, upd.GetError())
	}
	if upd.Schedule.DeliveryTarget != schedule.DeliveryTargetCreatorDM || upd.Schedule.DeliveryPolicy != schedule.DeliveryPolicyOnNonEmpty {
		t.Fatalf("updated delivery = (%q, %q)", upd.Schedule.DeliveryTarget, upd.Schedule.DeliveryPolicy)
	}

	// 3) Explicit delivery_target=origin without a verified Feishu creator is rejected.
	_, err = srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:           "cli-no-origin",
		Spec:           "daily 09:00",
		ScheduleType:   schedule.ScheduleTypeSubtask,
		TaskSpecJson:   specJSON,
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		CreatedBy:      "cli",
	})
	if err == nil || !strings.Contains(err.Error(), "Feishu") {
		t.Fatalf("expected Feishu creator error, got %v", err)
	}

	// 4) session-prompt + reuse-session rejects delivery_target=origin.
	_, err = srv.CreateSchedule(mcpCtx, &pb.CreateScheduleRequest{
		Name:           "bad-reuse",
		Spec:           "every 1h",
		ScheduleType:   schedule.ScheduleTypeSessionPrompt,
		TargetId:       "runtime",
		SessionName:    "feishu-s1",
		RunMode:        schedule.RunModeReuseSession,
		Prompt:         "ping",
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		CreatedBy:      "mcp",
	})
	if err == nil || !strings.Contains(err.Error(), "reuse-session") {
		t.Fatalf("expected reuse-session error, got %v", err)
	}
}

func TestHubP1CreatedCardWakeAdminPauseDeliverySwitchPruneAndAutoPauseReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	store, err := schedule.OpenStore(filepath.Join(home, "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := os.MkdirAll(filepath.Dir(bridge.DefaultDBPath()), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", bridge.DefaultDBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE feishu_turn_links (bridge_id TEXT, root_msg_id TEXT, message_id TEXT, open_id TEXT, target_id TEXT, session_id TEXT, session_name TEXT, launch_id TEXT, turn_id TEXT, submitted_at INTEGER);
		CREATE TABLE lark_chats (id INTEGER PRIMARY KEY AUTOINCREMENT, root_msg_id TEXT, chat_id TEXT, target_id TEXT, session_name TEXT, bridge_id TEXT);
		INSERT INTO feishu_turn_links VALUES('bot-1', 'om_topic_root', 'm1', 'ou_alice', 'runtime', 'sess-feishu-1', 'feishu-s1', 'launch-1', 'turn-1', 1);
		INSERT INTO lark_chats (root_msg_id, chat_id, target_id, session_name, bridge_id) VALUES('om_topic_root', 'oc_group_1', 'runtime', 'feishu-s1', 'bot-1');
	`)
	if err != nil {
		t.Fatal(err)
	}

	principals := agentTokenTestStore(t)
	alice := scheduleHuman(t, principals, "bot-1", "ou_alice")
	credential, err := principals.IssueAgentCredential(alice.ID, sessionAgentCredentialSubject("runtime", "feishu-s1"), principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRouterHub{routeFn: func(target, method string, payload interface{}) (*pb.TunnelMsg, error) {
		raw, _ := json.Marshal(&pb.ActiveTurnResponse{Active: true, TurnId: "turn-1", SessionId: "sess-feishu-1"})
		return &pb.TunnelMsg{Payload: raw}, nil
	}}
	srv := &hubServer{schedule: store, principals: principals, scheduleRuntime: runtime}
	woken := 0
	srv.RegisterScheduleDeliveryWaker(func() { woken++ })

	mcpCtx := actorCtx(credential.Token)
	specJSON, _ := json.Marshal(task.Spec{Agent: "builtin/claude", Task: "daily report"})
	resp, err := srv.CreateSchedule(mcpCtx, &pb.CreateScheduleRequest{
		Name:         "p1-created-card",
		Spec:         "daily 09:00",
		ScheduleType: schedule.ScheduleTypeSubtask,
		TaskSpecJson: specJSON,
		CreatedBy:    "mcp",
	})
	if err != nil || resp.Error != "" {
		t.Fatalf("CreateSchedule: err=%v respErr=%q", err, resp.GetError())
	}
	if woken != 1 {
		t.Fatalf("expected delivery waker to be called once on MCP create, got %d", woken)
	}
	pending, err := store.PendingResultDeliveries("bot-1", storeCreatedTimeNow())
	if err != nil || len(pending) != 1 || pending[0].Kind != schedule.DeliveryKindCreated || pending[0].ScheduleID != resp.Id {
		t.Fatalf("expected queued created card, got err=%v pending=%+v", err, pending)
	}

	ctrl := &bridgeSchedules{server: srv}
	// 1) delivery_target & delivery_policy switches via Act.
	if err := ctrl.Act(context.Background(), "bot-1", "ou_alice", resp.Id, "delivery_target:creator_dm"); err != nil {
		t.Fatalf("delivery_target act: %v", err)
	}
	if err := ctrl.Act(context.Background(), "bot-1", "ou_alice", resp.Id, "delivery_policy:on_failure"); err != nil {
		t.Fatalf("delivery_policy act: %v", err)
	}
	got, _ := store.Get(resp.Id)
	if got.DeliveryTarget != schedule.DeliveryTargetCreatorDM || got.DeliveryPolicy != schedule.DeliveryPolicyOnFailure {
		t.Fatalf("unexpected delivery fields after Act: %+v", got)
	}

	// 2) Scoped admin_pause disables future runs for same bot, rejects wrong bot.
	if err := ctrl.Act(context.Background(), "other-bot", "ou_admin", resp.Id, "admin_pause"); err == nil {
		t.Fatalf("admin_pause must reject wrong bot")
	}
	if err := ctrl.Act(context.Background(), "bot-1", "ou_admin", resp.Id, "admin_pause"); err != nil {
		t.Fatalf("admin_pause failed: %v", err)
	}
	got, _ = store.Get(resp.Id)
	if got.Enabled {
		t.Fatalf("admin_pause did not disable schedule")
	}

	// 3) Resuming via Act resets auto-pause counters.
	if err := ctrl.Act(context.Background(), "bot-1", "ou_alice", resp.Id, "resume"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	got, _ = store.Get(resp.Id)
	if !got.Enabled {
		t.Fatalf("resume did not re-enable schedule")
	}

	// 4) prune_completed deletes creator's completed one-shot schedules only.
	past := int64(1700000000)
	doneOnce, err := store.Create(schedule.Task{
		Name:         "done-once",
		Spec:         "at 2026-01-01T00:00:00Z",
		Kind:         schedule.KindOnce,
		ScheduleType: schedule.ScheduleTypeSubtask,
		TaskSpecJson: specJSON,
		Enabled:      true,
		NextRunAt:    &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(doneOnce.ID, schedule.Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: alice.ID, Source: "turn:1"}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimTracked(doneOnce, time.Unix(past, 0), nil); err != nil || !claimed {
		t.Fatalf("ClaimTracked done-once: claimed=%v err=%v", claimed, err)
	}
	if err := ctrl.Act(context.Background(), "bot-1", "ou_alice", 0, "prune_completed"); err != nil {
		t.Fatalf("prune_completed failed: %v", err)
	}
	if _, err := store.Get(doneOnce.ID); err == nil {
		t.Fatalf("prune_completed did not delete completed one-shot")
	}
	if _, err := store.Get(resp.Id); err != nil {
		t.Fatalf("prune_completed deleted active recurring schedule: %v", err)
	}

	// 5) Creator delete via Act removes schedule cleanly.
	if err := ctrl.Act(context.Background(), "bot-1", "ou_alice", resp.Id, "delete"); err != nil {
		t.Fatalf("delete act failed: %v", err)
	}
	if _, err := store.Get(resp.Id); err == nil {
		t.Fatalf("delete act did not remove schedule")
	}
}

func storeCreatedTimeNow() (t time.Time) {
	return time.Now().Add(time.Minute)
}

func TestRegisterScheduleDeliveryWakersWiresHubRPCInstance(t *testing.T) {
	// Production hub constructs a separate hubServer for scheduleControl vs the
	// gRPC hubRPC instance that executes CreateSchedule. Verify wakers register
	// on hubRPC and survive nil bridge entries.
	scheduleControlSrv := &hubServer{}
	hubRPC := &hubServer{}
	b := &bridge.Bridge{}

	registerScheduleDeliveryWakers(hubRPC, []*bridge.Bridge{nil, b})

	if len(scheduleControlSrv.scheduleDeliveryWakers) != 0 {
		t.Fatalf("unexpected wakers on scheduleControl server: %d", len(scheduleControlSrv.scheduleDeliveryWakers))
	}
	if len(hubRPC.scheduleDeliveryWakers) != 1 {
		t.Fatalf("expected 1 waker on hubRPC, got %d", len(hubRPC.scheduleDeliveryWakers))
	}
	// Invoking notifyScheduleDelivery on hubRPC must cleanly wake the bridge.
	hubRPC.notifyScheduleDelivery()
}
