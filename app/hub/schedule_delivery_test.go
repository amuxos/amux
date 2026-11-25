package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
