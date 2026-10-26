package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func scheduleHuman(t *testing.T, s *principal.Store, bot, open string) principal.Principal {
	t.Helper()
	p, e := s.EnsureAliasPrincipal(principal.Alias{Provider: principal.ProviderFeishu, Scope: bot, Subject: open}, open)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPersonalSchedulesAlwaysRequireCreatorAndCurrentAlias(t *testing.T) {
	srv := &hubServer{schedule: newScheduleTestStore(t), principals: agentTokenTestStore(t)}
	alice := scheduleHuman(t, srv.principals, "bot-a", "alice")
	scheduleHuman(t, srv.principals, "bot-a", "bob")
	scheduleHuman(t, srv.principals, "bot-b", "alice")
	c := &bridgeSchedules{server: srv}
	row, e := srv.schedule.Create(schedule.Task{Name: "private details", Spec: "every 1m", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = srv.schedule.BindCreator(row.ID, schedule.Creator{PrincipalID: alice.ID, BotID: "bot-a", OpenID: "alice", Source: "turn:original"}); e != nil {
		t.Fatal(e)
	}
	for _, who := range []struct{ bot, open string }{{"bot-a", "bob"}, {"bot-b", "alice"}, {"bot-a", "bot-owner"}} {
		list, e := c.List(context.Background(), who.bot, who.open)
		if e != nil || len(list) != 0 {
			t.Fatalf("list leak: %+v %v", list, e)
		}
		if _, e = c.Get(context.Background(), who.bot, who.open, row.ID); e == nil {
			t.Fatal("detail leak")
		}
		for _, op := range []string{"pause", "resume", "ack", "snooze", "off", "on", "recheck"} {
			if e = c.Act(context.Background(), who.bot, who.open, row.ID, op); e == nil {
				t.Fatalf("unauthorized %s", op)
			}
		}
	}
	if _, e = c.Get(context.Background(), "bot-a", "alice", row.ID); e != nil {
		t.Fatal(e)
	}
	if e = c.Act(context.Background(), "bot-a", "alice", row.ID, "pause"); e != nil {
		t.Fatal(e)
	}
	if e = srv.principals.Disable(alice.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Get(context.Background(), "bot-a", "alice", row.ID); e == nil {
		t.Fatal("disabled creator retained access")
	}
}
func TestScheduleCreatorUsesExactTurnReceiptInsteadOfExecutionPrincipal(t *testing.T) {
	t.Setenv("AMUX_HOME", t.TempDir())
	principals := agentTokenTestStore(t)
	alice := scheduleHuman(t, principals, "bot", "alice")
	scheduleHuman(t, principals, "bot", "bob")
	service, e := principals.Create(principal.Principal{Kind: principal.KindService, DisplayName: "execution"})
	if e != nil {
		t.Fatal(e)
	}
	credential, e := principals.IssueAgentCredential(service.ID, sessionAgentCredentialSubject("runtime", "shared"), principal.AgentCredentialTTL)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(bridge.DefaultDBPath()), 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", bridge.DefaultDBPath())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE feishu_turn_links (bridge_id TEXT,root_msg_id TEXT,message_id TEXT,open_id TEXT,target_id TEXT,session_id TEXT,session_name TEXT,launch_id TEXT,turn_id TEXT,submitted_at INTEGER);
 INSERT INTO feishu_turn_links VALUES('bot','root','m1','alice','runtime','s-id','shared','launch','turn-a',1),('bot','root','m2','bob','runtime','s-id','shared','launch','turn-b',2);`)
	if e != nil {
		t.Fatal(e)
	}
	active := "turn-a"
	runtime := &fakeRouterHub{routeFn: func(target, method string, payload interface{}) (*pb.TunnelMsg, error) {
		if target != "runtime" || method != pb.TaskMethodResolveParentTurn {
			t.Fatal("wrong provenance route")
		}
		raw, _ := json.Marshal(&pb.ActiveTurnResponse{Active: true, TurnId: active, SessionId: "s-id"})
		return &pb.TunnelMsg{Payload: raw}, nil
	}}
	srv := &hubServer{principals: principals, scheduleRuntime: runtime}
	creator, e := srv.resolveScheduleCreator(actorCtx(credential.Token))
	if e != nil || creator.PrincipalID != alice.ID || creator.OpenID != "alice" {
		t.Fatalf("wrong creator: %+v %v", creator, e)
	}
	// A queued/later speaker does not change a previously frozen creator.
	active = "turn-b"
	later, e := srv.resolveScheduleCreator(actorCtx(credential.Token))
	if e != nil || later.OpenID != "bob" || creator.OpenID != "alice" {
		t.Fatal("turn attribution failed")
	}
	active = "unlinked"
	unknown, e := srv.resolveScheduleCreator(actorCtx(credential.Token))
	if e != nil || unknown.OpenID != "" {
		t.Fatal("guessed a creator")
	}
}
func TestScheduleOutcomeOnlyClosesForExactCanonicalTurn(t *testing.T) {
	now := time.Now()
	seq := uint64(0)
	events := []struct {
		turn string
		body agentstream.Body
	}{{"other", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeError}}}, {"wanted", agentstream.ResultObserved{Text: "done"}}}
	runtime := &fakeRouterHub{streamFn: func(target, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error {
		if payload.(*pb.AttachRequest).TimelineMode != pb.AttachRequest_HISTORY {
			t.Fatal("not durable history")
		}
		for _, v := range events {
			seq++
			raw, e := agentstream.MarshalEvent(agentstream.Event{SessionID: "session-id", Cursor: agentstream.Cursor{Epoch: "epoch", Seq: seq}, Turn: &agentstream.TurnRef{ID: v.turn, Seq: 1}, At: now, Body: v.body})
			if e != nil {
				t.Fatal(e)
			}
			msg, _ := json.Marshal(&pb.AttachEvent{CanonicalJson: raw})
			if e = fn(&pb.TunnelMsg{Payload: msg}); e != nil {
				return e
			}
		}
		return nil
	}}
	c := &bridgeSchedules{server: &hubServer{scheduleRuntime: runtime}}
	r, e := c.ObserveScheduleExecution(context.Background(), schedule.Execution{TargetID: "runtime", SessionName: "session", SessionID: "session-id", TurnID: "wanted", Status: "submitted"})
	if e != nil || r.Terminal || r.Result != "done" {
		t.Fatalf("result inferred completion: %+v %v", r, e)
	}
	events = append(events, struct {
		turn string
		body agentstream.Body
	}{"wanted", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}}})
	r, e = c.ObserveScheduleExecution(context.Background(), r)
	if e != nil || !r.Terminal || r.Status != "completed" {
		t.Fatalf("exact completion lost: %+v %v", r, e)
	}
}

func TestPersonalScheduleRevokedExecutionAccessStaysDeniedWhenRuntimeOffline(t *testing.T) {
	srv := &hubServer{schedule: newScheduleTestStore(t), principals: agentTokenTestStore(t)}
	alice := scheduleHuman(t, srv.principals, "bot", "alice")
	bob := scheduleHuman(t, srv.principals, "bot", "bob")
	delegation, e := srv.principals.GrantDelegation(alice.ID, bob.ID)
	if e != nil {
		t.Fatal(e)
	}
	identity := principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: alice.ID, EffectivePrincipalID: bob.ID}
	raw, _ := json.Marshal(identity)
	row, e := srv.schedule.Create(schedule.Task{Name: "delegated", Spec: "every 1m", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = srv.schedule.BindCreator(row.ID, schedule.Creator{PrincipalID: alice.ID, BotID: "bot", OpenID: "alice", Source: "turn:alice", AccessIdentityJSON: string(raw)}); e != nil {
		t.Fatal(e)
	}
	c := &bridgeSchedules{server: srv}
	ctx := context.Background()
	if _, e = c.Get(ctx, "bot", "alice", row.ID); e != nil {
		t.Fatal(e)
	}
	if e = srv.principals.RevokeDelegation(delegation); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Get(ctx, "bot", "alice", row.ID); e == nil {
		t.Fatal("offline runtime relaxed frozen authority")
	}
	if entries, e := c.List(ctx, "bot", "alice"); e != nil || len(entries) != 0 {
		t.Fatal("revoked result included in personal aggregation")
	}
	for _, op := range []string{"pause", "resume", "ack", "snooze", "off", "on", "recheck"} {
		if e = c.Act(ctx, "bot", "alice", row.ID, op); e == nil {
			t.Fatalf("revoked authority allowed %s", op)
		}
	}
}
