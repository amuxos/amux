package bridge

import (
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
)

func TestAssistantProgressKeepsLatestSummaryAndLastCompleteMessage(t *testing.T) {
	var progress assistantProgress

	if got := progress.Append("我先检查仓库。\n正在查看 MR "); got != "我先检查仓库。\n正在查看 MR" {
		t.Fatalf("first summary = %q", got)
	}
	if got := progress.Append("审核规则。"); got != "我先检查仓库。\n正在查看 MR 审核规则。" {
		t.Fatalf("continued summary = %q", got)
	}
	if completed := progress.Boundary(); !completed {
		t.Fatal("non-empty assistant message boundary was not reported as completed")
	}
	if !progress.HasCompletedMessage() || progress.Summary() != "我先检查仓库。\n正在查看 MR 审核规则。" {
		t.Fatalf("completed progress summary = %q", progress.Summary())
	}
	if completed := progress.Boundary(); completed {
		t.Fatal("empty assistant message boundary was reported as completed")
	}

	progress.Append("最终结果草稿")
	if got := progress.Summary(); got != "我先检查仓库。\n正在查看 MR 审核规则。\n\n最终结果草稿" {
		t.Fatalf("cumulative progress = %q", got)
	}
	if got := progress.LastMessage(); got != "最终结果草稿" {
		t.Fatalf("last message = %q, want current message", got)
	}
	progress.Boundary()
	if got := progress.LastMessage(); got != "最终结果草稿" {
		t.Fatalf("completed fallback = %q", got)
	}
}

func TestAssistantResultFallbackRejectsFailedAndInterruptedTurns(t *testing.T) {
	for _, turn := range []agentui.TurnEvent{
		{State: "failed"},
		{State: "stopped"},
		{State: "turn_done", StopReason: "cancelled"},
	} {
		if shouldUseAssistantResultFallback(turn) {
			t.Fatalf("fallback allowed for terminal %+v", turn)
		}
	}
	if !shouldUseAssistantResultFallback(agentui.TurnEvent{State: "turn_done", StopReason: "end_turn"}) {
		t.Fatal("successful turn should allow last-message fallback")
	}
}

func TestAssistantProgressSummaryIsBoundedAndResettable(t *testing.T) {
	var progress assistantProgress
	input := "开始" + strings.Repeat("进", maxAssistantProgressRunes+20) + "最新"
	got := progress.Append(input)
	if len([]rune(got)) != maxAssistantProgressRunes || !strings.Contains(got, omittedAssistantProgress) ||
		!strings.HasPrefix(got, "开始") || !strings.HasSuffix(got, "最新") {
		t.Fatalf("bounded summary = %q", got)
	}
	progress.Reset()
	if got := progress.Summary(); got != "" {
		t.Fatalf("progress after reset = %q", got)
	}
}

func TestAssistantProgressRetainsCompletedMessagesInOrder(t *testing.T) {
	var progress assistantProgress
	for _, message := range []string{"先检查仓库状态。", "再运行聚焦测试。", "最后核对变更。"} {
		progress.Append(message)
		progress.Boundary()
	}
	want := "先检查仓库状态。\n\n再运行聚焦测试。\n\n最后核对变更。"
	if got := progress.Summary(); got != want {
		t.Fatalf("progress history = %q, want %q", got, want)
	}
	if got := progress.LastMessage(); got != "最后核对变更。" {
		t.Fatalf("fallback message = %q", got)
	}
}

func TestAssistantProgressReconcileResultRemovesOnlyLatestExactSuffix(t *testing.T) {
	var progress assistantProgress
	progress.Append("先检查仓库状态。")
	progress.Boundary()
	progress.Append("验证完成。\n\n最终答案")

	if got := progress.ReconcileResult("  最终答案\n"); got != "先检查仓库状态。\n\n验证完成。" {
		t.Fatalf("reconciled progress = %q, want earlier progress without final result suffix", got)
	}
	if got := progress.LastMessage(); got != "验证完成。" {
		t.Fatalf("latest message after reconciliation = %q, want retained narration prefix", got)
	}
}

func TestAssistantProgressReconcileCompletedResultKeepsEarlierMessages(t *testing.T) {
	var progress assistantProgress
	for _, message := range []string{"先检查仓库。", "再运行测试。", "最终答案"} {
		progress.Append(message)
		progress.Boundary()
	}

	if got := progress.ReconcileResult("最终答案"); got != "先检查仓库。\n\n再运行测试。" {
		t.Fatalf("reconciled completed progress = %q", got)
	}
}

func TestAssistantProgressReconcileResultDoesNotRewriteEarlierOrFuzzyMatches(t *testing.T) {
	var progress assistantProgress
	progress.Append("最终答案曾在这里作为讨论对象。")
	progress.Boundary()
	progress.Append("正在完成最后检查。")

	want := progress.Summary()
	if got := progress.ReconcileResult("最终答案"); got != want {
		t.Fatalf("non-suffix result reconciliation = %q, want unchanged %q", got, want)
	}
}

func TestAssistantProgressWithoutResultKeepsLastMessageFallback(t *testing.T) {
	var progress assistantProgress
	progress.Append("处理说明。")
	progress.Boundary()
	progress.Append("没有原生 result 的最终回答")
	progress.Boundary()

	before := progress.Summary()
	if got := progress.ReconcileResult("  "); got != before {
		t.Fatalf("empty result changed progress = %q, want %q", got, before)
	}
	if got := progress.LastMessage(); got != "没有原生 result 的最终回答" {
		t.Fatalf("fallback = %q, want last complete assistant message", got)
	}
}

func TestMergeSteeredFinalAnswerKeepsEachSteerInOrder(t *testing.T) {
	got := mergeSteeredFinalAnswer([]string{"DONE-FG", "收到甲", "收到乙", "收到丙"}, "收到丙")
	want := "DONE-FG\n\n收到甲\n\n收到乙\n\n收到丙"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMergeSteeredFinalAnswerDropsExactDuplicates(t *testing.T) {
	got := mergeSteeredFinalAnswer([]string{"收到甲", "收到甲", "收到乙"}, "收到乙")
	want := "收到甲\n\n收到乙"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMergeSteeredFinalAnswerPrefersComprehensiveResult(t *testing.T) {
	progress := "我直接执行，等待 45 秒后读取 README 的第一行。"
	result := "已直接执行 sleep 45。README 第一行是 amux。另外，7 × 8 = 56。"
	got := mergeSteeredFinalAnswer([]string{progress}, result)
	if got != result {
		t.Fatalf("progress leaked into final answer: %q", got)
	}
}

func TestMergeSteeredFinalAnswerUsesProviderMergedResult(t *testing.T) {
	result := "DONE-FG\n\n收到甲\n\n收到乙\n\n收到丙"
	got := mergeSteeredFinalAnswer([]string{"DONE-FG", "收到甲", "收到乙", "收到丙"}, result)
	if got != result {
		t.Fatalf("got %q, want provider result", got)
	}
}
