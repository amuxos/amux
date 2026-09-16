package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type streamingSubtaskReader struct {
	mu          sync.Mutex
	currentJSON string
	finalJSON   string
	events      []subtaskEvent
	created     <-chan struct{}
	updated     <-chan struct{}
	streamCalls int
}

func (r *streamingSubtaskReader) SubtaskIDsByParent(string) []string {
	return []string{"task-1"}
}

func (r *streamingSubtaskReader) LiveSubtaskIDsByParent(string) []string {
	return []string{"task-1"}
}

func (r *streamingSubtaskReader) GetSubtaskJSON(context.Context, string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []byte(r.currentJSON), nil
}

func (r *streamingSubtaskReader) StreamSubtaskEvents(
	ctx context.Context,
	_ string,
	fromSeq uint64,
	handle func([]byte) error,
) error {
	r.mu.Lock()
	r.streamCalls++
	r.mu.Unlock()
	if r.created != nil {
		select {
		case <-r.created:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for index, event := range r.events {
		if event.Seq <= fromSeq {
			continue
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := handle(raw); err != nil {
			return err
		}
		if index == 0 && r.updated != nil {
			select {
			case <-r.updated:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	r.mu.Lock()
	r.currentJSON = r.finalJSON
	r.mu.Unlock()
	return nil
}

func TestRenderSubtaskActivityCardLooksLikeAgentProgress(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 1, 5, 0, time.UTC)
	snapshot := subtaskCardSnapshot{
		Progress: subtaskProgress{
			ID:             "task-1",
			AgentType:      "codex",
			AgentProfile:   "team/reviewer@revision",
			ChildSessionID: "885c55dc-498b-4de1-aeea-10c19eeff001",
			EffectiveModel: "DeepSeek-V4-Pro",
			Status:         "running",
			Title:          "处理 index 停写",
			RequestedAt:    now.Add(-time.Minute),
			StartedAt:      now.Add(-55 * time.Second),
		},
		Output: "正在检查服务状态",
		LastAt: now.Add(-time.Second),
	}

	card := renderSubtaskActivityCard(snapshot, "https://viewer.example/child", now)
	for _, want := range []string{
		"reviewer-885c55dc · 正在处理 · team/reviewer · 模型 DeepSeek-V4-Pro",
		"处理 index 停写",
		"正在检查服务状态",
		"查看完整输出",
		"Powered by",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "@revision") {
		t.Fatalf("card exposed pinned profile revision:\n%s", card)
	}
}

func TestSubtaskCardNamesDistinguishAgentsAndKeepInstanceIdentity(t *testing.T) {
	now := time.Now()
	reader := &streamingSubtaskReader{}
	bridge := &Bridge{subtasks: reader}
	for _, tc := range []struct {
		name, taskID, profile, agentType, instanceID, childID, want string
	}{
		{"task", "task-11111111-1111-4111-8111-111111111111", "builtin/codex", "codex", "", "885c55dc-498b-4de1-aeea-10c19eeff001", "codex-885c55dc"},
		{"delegation", "task-22222222-2222-4222-8222-222222222222", "builtin/codex", "codex", "", "92ff1234-498b-4de1-aeea-10c19eeff001", "codex-92ff1234"},
		{"instance first task", "task-33333333-3333-4333-8333-333333333333", "builtin/codex", "codex", "agent-70106b22-1d72-40ca-91b9-0dabc3c46956", "1638876a-3dfe-48d7-95f2-26c9e7969434", "codex-70106b22"},
		{"instance followup", "task-44444444-4444-4444-8444-444444444444", "builtin/codex", "codex", "agent-70106b22-1d72-40ca-91b9-0dabc3c46956", "1638876a-3dfe-48d7-95f2-26c9e7969434", "codex-70106b22"},
		{"pending instance", "task-55555555-5555-4555-8555-555555555555", "team/reviewer@revision", "codex", "agent-abcdef12-1d72-40ca-91b9-0dabc3c46956", "", "reviewer-abcdef12"},
		{"pending task", "task-66666666-6666-4666-8666-666666666666", "builtin/codex", "codex", "", "", "codex-66666666"},
		{"legacy type", "task-77777777-7777-4777-8777-777777777777", "", "codex", "", "", "codex-77777777"},
		{"unknown profile", "task-88888888-8888-4888-8888-888888888888", "", "", "", "", "agent-88888888"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{
				"id": tc.taskID, "agent_profile": tc.profile, "agent_type": tc.agentType,
				"agent_instance_id": tc.instanceID, "child_session_id": tc.childID, "status": "running",
			})
			if err != nil {
				t.Fatal(err)
			}
			reader.currentJSON = string(raw)
			progress, ok := bridge.readSubtaskProgress(context.Background(), tc.taskID, now)
			if !ok {
				t.Fatal("task metadata not decoded")
			}
			state := newSubtaskCardState(progress)
			for _, status := range []string{"running", "cancelled", "succeeded"} {
				// A sparse terminal snapshot must not erase a known agent identity.
				state.updateProgress(subtaskProgress{ID: tc.taskID, Status: status})
				snapshot := state.snapshot(now)
				for _, card := range []string{renderSubtaskActivityCard(snapshot, "", now), string(snapshot.activity("", now).JSON)} {
					if !strings.Contains(card, tc.want+" · ") || strings.Contains(card, "子 Agent · ") || strings.Contains(card, "@revision") {
						t.Fatalf("%s card missing agent name %q: %s", status, tc.want, card)
					}
				}
			}
		})
	}
}

func TestWatchSubtaskCardCreatesUpdatesAndFinishesOneCard(t *testing.T) {
	created := make(chan struct{})
	updated := make(chan struct{})
	var createdOnce sync.Once
	var updatedOnce sync.Once
	var mu sync.Mutex
	var creates, updates, finishes int
	var liveModel, liveAgentName bool
	var finalActivity string
	backend := &orderedReplyBackend{
		create: func(string) (*replyCardHandle, error) {
			t.Fatal("subtask result unexpectedly created a second card")
			return nil, nil
		},
		createActivity: func(activity turnCardActivity) (*replyCardHandle, error) {
			mu.Lock()
			creates++
			mu.Unlock()
			if activity.Terminal {
				t.Fatal("initial subtask card was terminal")
			}
			createdOnce.Do(func() { close(created) })
			return &replyCardHandle{mode: replyCardModeCardKit}, nil
		},
		update: func(_ *replyCardHandle, _ string) error { return nil },
		updateActivity: func(_ *replyCardHandle, activity turnCardActivity) error {
			mu.Lock()
			updates++
			if !activity.Terminal && strings.Contains(string(activity.JSON), "DeepSeek-V4-Pro") {
				liveModel = true
			}
			if !activity.Terminal && strings.Contains(string(activity.JSON), "reviewer-885c55dc · ") {
				liveAgentName = true
			}
			finalActivity = string(activity.JSON)
			mu.Unlock()
			updatedOnce.Do(func() { close(updated) })
			return nil
		},
		finish: func(handle *replyCardHandle, _ string) error {
			if handle.finished {
				return nil
			}
			mu.Lock()
			finishes++
			mu.Unlock()
			handle.finished = true
			return nil
		},
	}
	startedAt := time.Now().Add(-time.Minute).UTC()
	reader := &streamingSubtaskReader{
		currentJSON: `{"id":"task-1","agent_type":"codex","agent_profile":"team/reviewer","status":"running","task":"处理 index 停写","target_id":"target-1","child_session_name":"task-child","child_session_id":"885c55dc-498b-4de1-aeea-10c19eeff001","requested_at":"` + startedAt.Format(time.RFC3339Nano) + `","started_at":"` + startedAt.Format(time.RFC3339Nano) + `"}`,
		finalJSON:   `{"id":"task-1","agent_type":"codex","agent_profile":"team/reviewer","status":"succeeded","task":"处理 index 停写","target_id":"target-1","child_session_name":"task-child","child_session_id":"885c55dc-498b-4de1-aeea-10c19eeff001","result":{"summary":"已恢复写入"},"requested_at":"` + startedAt.Format(time.RFC3339Nano) + `","started_at":"` + startedAt.Format(time.RFC3339Nano) + `","finished_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`,
		created:     created,
		updated:     updated,
		events: []subtaskEvent{
			{TaskID: "task-1", Seq: 1, Kind: "status", Status: "running", At: time.Now().UTC()},
			{TaskID: "task-1", Seq: 2, Kind: "output", Output: []byte("正在检查索引状态"), At: time.Now().UTC()},
			{TaskID: "task-1", Seq: 3, Kind: "status", Status: "succeeded", At: time.Now().UTC()},
			{TaskID: "task-1", Seq: 4, Kind: "result", Status: "succeeded", Result: &subtaskEventResult{Summary: "已恢复写入"}, At: time.Now().UTC()},
		},
	}
	bridge := &Bridge{
		hub:      &bridgeTestHub{targets: []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "885c55dc-498b-4de1-aeea-10c19eeff001", Name: "task-child", EffectiveModel: "DeepSeek-V4-Pro"}}}}},
		subtasks: reader,
		subtaskCardPort: subtaskCardPort{
			subtaskCardBackend: func(*LarkChat) replyCardBackend {
				return backend
			},
		},
	}
	initial, ok := bridge.readSubtaskProgress(context.Background(), "task-1", time.Now())
	if !ok {
		t.Fatal("initial subtask progress was not decoded")
	}

	initial.Status = "pending"
	initial.TargetID = ""
	initial.ChildSessionID = ""
	initial.ChildSessionName = ""
	initial.EffectiveModel = ""
	bridge.watchSubtaskCard(context.Background(), &LarkChat{RootMsgID: "root-1"}, initial)

	mu.Lock()
	defer mu.Unlock()
	if !liveModel {
		t.Fatal("pending card did not acquire child model while running")
	}
	if !liveAgentName || !strings.Contains(finalActivity, "reviewer-885c55dc · ") {
		t.Fatal("pending card did not acquire and retain child agent name")
	}
	if creates != 1 {
		t.Fatalf("activity cards created = %d, want 1", creates)
	}
	if updates == 0 {
		t.Fatal("subtask activity card was never updated")
	}
	if finishes != 1 {
		t.Fatalf("activity cards finished = %d, want 1", finishes)
	}
	if !strings.Contains(finalActivity, "最终结果") || !strings.Contains(finalActivity, "已恢复写入") {
		t.Fatalf("final activity missing task summary: %s", finalActivity)
	}
	if reader.streamCalls != 1 {
		t.Fatalf("event stream calls = %d, want 1", reader.streamCalls)
	}
}

func TestStartSubtaskCardsClaimsEachTaskOnce(t *testing.T) {
	reader := &streamingSubtaskReader{}
	bridge := &Bridge{subtasks: reader}
	if !bridge.claimSubtaskCard("task-1") {
		t.Fatal("first claim was rejected")
	}
	if bridge.claimSubtaskCard("task-1") {
		t.Fatal("duplicate claim was accepted")
	}
	if !bridge.claimSubtaskCard("task-2") {
		t.Fatal("different task claim was rejected")
	}
}

func TestSubtaskCardDeliveryFallsBackToOneLegacyCard(t *testing.T) {
	var sends, patches int
	backend := &bindingReplyCardBackend{
		cardKit: &fakeCardKitReplyAPI{createErr: errors.New("cardkit unavailable")},
		sendCard: func(context.Context, string, string) (string, error) {
			t.Fatal("CardKit message sent after create failure")
			return "", nil
		},
	}
	state := newSubtaskCardState(subtaskProgress{
		ID: "task-1", Status: "running", Title: "处理任务", StartedAt: time.Now(),
	})
	delivery := newSubtaskCardDelivery(
		backend,
		func() turnCardActivity {
			now := time.Now()
			return state.snapshot(now).activity("", now)
		},
		func(string) (string, error) {
			sends++
			return "legacy-1", nil
		},
		func(string, string) error {
			patches++
			return nil
		},
		nil,
	)

	delivery.Update("running", true)
	state.updateProgress(subtaskProgress{
		ID: "task-1", Status: "succeeded", Title: "处理任务", Summary: "完成",
		StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(),
	})
	delivery.Finish("done")

	if sends != 1 {
		t.Fatalf("legacy cards sent = %d, want 1", sends)
	}
	if patches != 1 {
		t.Fatalf("legacy card patches = %d, want 1", patches)
	}
}

func TestSubtaskCardStateKeepsTerminalSnapshotDuringReplay(t *testing.T) {
	state := newSubtaskCardState(subtaskProgress{ID: "task-1", Status: "succeeded", Summary: "完成"})
	state.applyEvent(subtaskEvent{Seq: 1, Kind: "status", Status: "running"})
	state.applyEvent(subtaskEvent{Seq: 2, Kind: "output", Output: []byte("过程")})
	state.applyEvent(subtaskEvent{Seq: 3, Kind: "status", Status: "succeeded"})

	snapshot := state.snapshot(time.Now())
	if snapshot.Progress.Status != "succeeded" {
		t.Fatalf("status regressed during replay: %q", snapshot.Progress.Status)
	}
	if snapshot.Output != "过程" {
		t.Fatalf("output = %q, want replayed progress", snapshot.Output)
	}
}

func TestSubtaskCardModelUsesExactChildMetadataAndSurvivesCleanup(t *testing.T) {
	now := time.Now()
	hub := &bridgeTestHub{targets: []*pb.Target{
		{Id: "other-target", Sessions: []*pb.Session{{Id: "child-id", Name: "child", EffectiveModel: "wrong-target-model"}}},
		{Id: "target", Sessions: []*pb.Session{
			{Id: "parent-id", Name: "parent", EffectiveModel: "parent-model"},
			{Id: "reused-name", Name: "child", EffectiveModel: "wrong-child-model"},
			{Id: "child-id", Name: "child"},
		}},
	}}
	reader := &streamingSubtaskReader{currentJSON: `{"id":"task-1","status":"running","target_id":"target","child_session_id":"child-id","child_session_name":"child","model":"requested-model"}`}
	b := &Bridge{hub: hub, subtasks: reader}
	progress, ok := b.readSubtaskProgress(context.Background(), "task-1", now)
	if !ok || progress.EffectiveModel != "" {
		t.Fatalf("unknown child model must not use parent/requested/other session model: %+v", progress)
	}
	state := newSubtaskCardState(progress)
	if card := renderSubtaskActivityCard(b.subtaskCardSnapshot(state, now), "", now); strings.Contains(card, "模型 ") {
		t.Fatalf("unknown model should be omitted: %s", card)
	}

	// Provider-confirmed metadata can arrive after the card was first created.
	hub.targets[1].Sessions[2].EffectiveModel = " DeepSeek-V4-Pro "
	snapshot := b.subtaskCardSnapshot(state, now)
	if snapshot.Progress.EffectiveModel != "DeepSeek-V4-Pro" {
		t.Fatalf("late child model not backfilled: %+v", snapshot.Progress)
	}
	progress, ok = b.readSubtaskProgress(context.Background(), "task-1", now)
	if !ok || progress.EffectiveModel != "DeepSeek-V4-Pro" {
		t.Fatalf("task snapshot did not resolve child model: %+v", progress)
	}

	// Runtime disconnection or child cleanup must not erase the learned model.
	hub.targets = nil
	state.updateProgress(subtaskProgress{ID: "task-1", Status: "succeeded", Summary: "done"})
	for _, status := range []string{"succeeded", "cancelled", "failed"} {
		snapshot = b.subtaskCardSnapshot(state, now)
		snapshot.Progress.Status = status
		legacy := renderSubtaskActivityCard(snapshot, "", now)
		live := string(snapshot.activity("", now).JSON)
		for _, card := range []string{legacy, live} {
			if !strings.Contains(card, "模型 DeepSeek-V4-Pro") {
				t.Fatalf("%s card lost model: %s", status, card)
			}
		}
	}
}

func TestSubtaskEffectiveModelLegacyNameFallback(t *testing.T) {
	b := &Bridge{hub: &bridgeTestHub{targets: []*pb.Target{{Id: "target", Sessions: []*pb.Session{
		{Id: "child-id", Name: "child", EffectiveModel: "child-model"},
	}}}}}
	if got := b.subtaskChildMetadata("target", "", "child").EffectiveModel; got != "child-model" {
		t.Fatalf("legacy name lookup = %q", got)
	}
	if got := b.subtaskChildMetadata("target", "missing-id", "child").EffectiveModel; got != "" {
		t.Fatalf("ID mismatch must not fall back to a reused name: %q", got)
	}
}

func TestSubtaskCardInstanceNameUsesExactChildMetadataWhenTaskOmitsInstanceID(t *testing.T) {
	now := time.Now()
	child := &pb.Session{Id: "1638876a-3dfe-48d7-95f2-26c9e7969434", Name: "child"}
	hub := &bridgeTestHub{targets: []*pb.Target{
		{Id: "other-target", Sessions: []*pb.Session{{Id: child.Id, AgentInstanceId: "agent-wrong-target"}}},
		{Id: "target", Sessions: []*pb.Session{
			{Id: "wrong-child", Name: "child", AgentInstanceId: "agent-wrong-child"}, child,
		}},
	}}
	reader := &streamingSubtaskReader{currentJSON: `{"id":"task-1","agent_profile":"builtin/codex","status":"running","target_id":"target","child_session_id":"1638876a-3dfe-48d7-95f2-26c9e7969434","child_session_name":"child"}`}
	b := &Bridge{hub: hub, subtasks: reader}
	progress, ok := b.readSubtaskProgress(context.Background(), "task-1", now)
	if !ok || progress.AgentInstanceID != "" || subtaskAgentName(progress) != "codex-1638876a" {
		t.Fatalf("unknown instance must not use another target or a reused session name: %+v", progress)
	}
	state := newSubtaskCardState(progress)
	child.AgentInstanceId = "agent-70106b22-1d72-40ca-91b9-0dabc3c46956"
	child.EffectiveModel = "DeepSeek-V4-Pro"
	snapshot := b.subtaskCardSnapshot(state, now)
	if subtaskAgentName(snapshot.Progress) != "codex-70106b22" {
		t.Fatalf("live card did not acquire instance identity: %+v", snapshot.Progress)
	}
	progress, ok = b.readSubtaskProgress(context.Background(), "task-1", now)
	if !ok || progress.AgentInstanceID != child.AgentInstanceId {
		t.Fatalf("Task JSON without instance ID must resolve child metadata: %+v", progress)
	}
	hub.targets = nil
	state.updateProgress(subtaskProgress{ID: "task-1", Status: "cancelled"})
	snapshot = b.subtaskCardSnapshot(state, now)
	for _, card := range []string{renderSubtaskActivityCard(snapshot, "", now), string(snapshot.activity("", now).JSON)} {
		if !strings.Contains(card, "codex-70106b22 · 已停止 · builtin/codex · 模型 DeepSeek-V4-Pro") {
			t.Fatalf("terminal card lost learned instance identity or model: %s", card)
		}
	}
}

func TestSubtaskExecutionHistoryCountsCallsAcrossReplayAndTerminal(t *testing.T) {
	now := time.Now().UTC()
	state := newSubtaskCardState(subtaskProgress{ID: "task-child", Status: "running", StartedAt: now, EffectiveModel: "DeepSeek-V4-Pro"})
	seq := uint64(0)
	apply := func(id, name, status string) {
		seq++
		raw, _ := json.Marshal(map[string]any{"task_id": "task-child", "turn_id": "child-turn", "seq": seq, "kind": "tool", "at": now, "tool": map[string]string{"id": id, "name": name, "title": name, "status": status}})
		var event subtaskEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		state.applyEvent(event)
		state.applyEvent(event) // reconnect replay must not inflate counts
	}
	apply("call-1", "amux/amux.task_get", "running")
	if card := renderSubtaskActivityCard(state.snapshot(now), "", now); !strings.Contains(card, "执行记录 · 1 次") || !strings.Contains(card, "●") {
		t.Fatalf("missing live call: %s", card)
	}
	apply("call-1", "amux/amux.task_get", "completed")
	apply("call-2", "amux/amux.task_get", "completed")
	apply("call-3", "amux/amux.task_run", "failed")
	snapshot := state.snapshot(now)
	if snapshot.Activity == nil || len(snapshot.Activity.Tools) != 3 {
		t.Fatalf("tools = %+v", snapshot.Activity)
	}
	for _, status := range []string{"running", "succeeded", "cancelled", "failed"} {
		snapshot.Progress.Status = status
		for _, card := range []string{renderSubtaskActivityCard(snapshot, "", now), string(snapshot.activity("", now).JSON)} {
			for _, want := range []string{"执行记录 · 3 次", "amux.task_get", "×2", "amux.task_run", "✓", "✕"} {
				if !strings.Contains(card, want) {
					t.Fatalf("%s missing %q: %s", status, want, card)
				}
			}
		}
	}
	next := newSubtaskCardState(subtaskProgress{ID: "task-followup", Status: "running"})
	if card := renderSubtaskActivityCard(next.snapshot(now), "", now); strings.Contains(card, "执行记录") || strings.Contains(card, "task_get") {
		t.Fatalf("followup inherited previous task calls: %s", card)
	}
}
