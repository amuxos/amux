package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
)

func TestQuestionTitleExcludesForegroundProtocol(t *testing.T) {
	t.Parallel()
	raw := "分 5 步跑完告诉我" + artifactInstructions + foregroundInstructions
	got := toolActivityQuestionTitle(raw)
	if got != "分 5 步跑完告诉我" {
		t.Fatalf("question = %q", got)
	}
	// Older/variant protocol wording is stripped too.
	legacy := "hi\n\n[amux 前台协调说明：旧版说明文本。]"
	if got := toolActivityQuestionTitle(legacy); got != "hi" {
		t.Fatalf("legacy question = %q", got)
	}
	if strings.Contains(stripAmuxGuidance(raw), "amux 前台协调说明") {
		t.Fatal("strip left protocol")
	}
}

func TestForegroundProtocolMentionsCompletionMessages(t *testing.T) {
	t.Parallel()
	if !strings.Contains(foregroundInstructions, "后台任务完成") || !strings.Contains(foregroundInstructions, "无需轮询") {
		t.Fatal("foreground protocol must explain automatic completion messages")
	}
}

func TestSubtaskNoticeNeverRenderedInCard(t *testing.T) {
	t.Parallel()
	// A subtask turn starts with an empty question, so the card is a neutral
	// reply card carrying only the agent's answer.
	card := renderToolActivityCard(toolActivityCardMeta{Question: ""}, "后台任务已完成，结果如下。")
	for _, bad := range []string{"后台任务完成", "task_id", "agent_instance_id", "status=", "turn_question"} {
		if strings.Contains(card, bad) {
			t.Fatalf("card leaks %q: %s", bad, card)
		}
	}
	if !strings.Contains(card, "结果如下") {
		t.Fatal("agent reply missing from card")
	}
}

func TestQueueListHidesSubtaskNotice(t *testing.T) {
	t.Parallel()
	line := queueListLine("q1", "subtask", "[后台任务完成] agent_instance_id=agent-1 task_id=task-9 status=succeeded")
	if strings.Contains(line, "task-9") || strings.Contains(line, "[后台任务完成]") || strings.Contains(line, "status=") {
		t.Fatalf("queue line leaks notice: %q", line)
	}
}

func TestSubtaskSourceStartsVisibleTurn(t *testing.T) {
	t.Parallel()
	if !isInputStartedTurnSource(agentui.SourceSubtask) {
		t.Fatal("subtask completion turns must render in Feishu")
	}
}

func TestBackgroundTaskCardsHiddenByDefault(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	b.subtasks = &streamingSubtaskReader{}
	b.startSubtaskCards(&LarkChat{RootMsgID: "r"}, []subtaskProgress{{ID: "task-1"}})
	b.subtaskCardMu.Lock()
	n := len(b.subtaskCards)
	b.subtaskCardMu.Unlock()
	if n != 0 {
		t.Fatal("show_background_tasks off must not start subtask cards")
	}
}

func TestRunningExperimentsReportsShowBackgroundTasks(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	b.showBackgroundTasks.Store(true)
	if !b.runningExperimentsSettings().ShowBackgroundTasks {
		t.Fatal("running settings must mirror live switch")
	}
	card := renderManagementExperimentsCard(config.ExperimentsSettings{}, config.ExperimentsSettings{})
	if !strings.Contains(card, "show_background_tasks_enabled") {
		t.Fatal("experiments card lacks show_background_tasks item")
	}
}

func TestQueueNoticeOnRunningCardOnly(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	b.steerForeground.Store(true)
	refreshed := 0
	b.registerQueueNoticeRefresher("r", 1, func() bool { refreshed++; return true })
	b.setQueueNotice("r", 2, true)
	if refreshed != 1 || b.queueNoticeCount("r") != 2 {
		t.Fatalf("refreshed=%d count=%d", refreshed, b.queueNoticeCount("r"))
	}
	b.setQueueNotice("r", 2, true) // unchanged: no re-render
	if refreshed != 1 {
		t.Fatal("unchanged count re-rendered")
	}
	running := renderTurnActivity(toolActivityCardMeta{Status: "running", QueuedInputs: 2})
	raw, _ := json.Marshal(running.contentElements())
	if !strings.Contains(string(raw), "queue_notice") {
		t.Fatalf("running card lacks queue line: %s", raw)
	}
	done := renderTurnActivity(toolActivityCardMeta{Status: "done", QueuedInputs: 2})
	raw, _ = json.Marshal(done.contentElements())
	if strings.Contains(string(raw), "queue_notice") {
		t.Fatal("finished card must drop queue line")
	}
	b.setQueueNotice("r", 0, false)
	if b.queueNoticeCount("r") != 0 {
		t.Fatal("not cleared")
	}
	b.steerForeground.Store(false)
	b.setQueueNotice("r", 3, false)
	if b.queueNoticeCount("r") != 0 {
		t.Fatal("steer_foreground off must hide queue line")
	}
	// No running card: only logs, no panic.
	b.unregisterQueueNoticeRefresher("r", 1)
	b.steerForeground.Store(true)
	b.setQueueNotice("r", 1, true)
}

func TestNoteTurnSteeredNotifiesRegisteredObserver(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	var got string
	b.registerSteerObserver("root", 7, func(turnID string) { got = turnID })
	b.noteTurnSteered("root", "turn-9")
	if got != "turn-9" {
		t.Fatalf("got %q", got)
	}
	b.unregisterSteerObserver("root", 7)
	b.noteTurnSteered("root", "turn-10")
	if got != "turn-9" {
		t.Fatal("observer fired after unregister")
	}
}
