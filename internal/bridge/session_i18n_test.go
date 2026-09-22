package bridge

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
)

func TestSessionI18nActivityLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	for _, status := range []string{"running", "done", "waiting", "failed", "stopped"} {
		for _, idle := range []time.Duration{0, 20 * time.Second, 2 * time.Minute} {
			t.Run(status+idle.String(), func(t *testing.T) {
				meta := toolActivityCardMeta{Status: status, Now: now, Question: "User question", Followup: "More context", AgentProfile: "example/expert", EffectiveModel: "example-model", ViewURL: "https://example.com/output", Activity: &agentui.ActivityView{Status: status, StartedAt: now.Add(-90 * time.Second), LastActivityAt: now.Add(-idle), CurrentStep: "Checking input", Plan: []agentui.PlanEntry{{Content: "Inspect", Status: "done"}}, Tools: []agentui.ToolEvent{{Title: "echo test", Status: "running", StartedAt: now.Add(-time.Second)}}, UsageJSON: json.RawMessage(`{"total_tokens":42,"cumulative_input_tokens":100,"cumulative_cached_input_tokens":80,"cumulative_output_tokens":12,"ttft_ms":500}`)}, Subtasks: []subtaskProgress{{Title: "Review", Status: "timed_out", Elapsed: time.Minute}}}
				raw := renderToolActivityCard(meta, "")
				en := assertSessionCardLanguages(t, raw)
				for _, want := range []string{"Question", "Follow-up", "Execution records", "Plan progress", "Subtasks", "Processing", "Total input", "Total output", "First token", "View full output"} {
					if !strings.Contains(en, want) {
						t.Errorf("missing %q in English card", want)
					}
				}
				if status == "waiting" && !strings.Contains(en, "Waiting for your reply") {
					t.Fatal("waiting status untranslated")
				}
			})
		}
	}
	// Empty activity, unstructured legacy activity, and bounded fallbacks.
	for _, meta := range []toolActivityCardMeta{
		{Status: "running"},
		{Status: "done", Question: "Question text", Followup: "Follow-up text"},
		{Status: "failed", Activity: &agentui.ActivityView{CurrentStep: strings.Repeat("progress ", 20000), Plan: []agentui.PlanEntry{{Content: strings.Repeat("x", 500)}}}},
	} {
		assertSessionCardLanguages(t, renderToolActivityCard(meta, ""))
	}
}

func assertSessionCardLanguages(t *testing.T, raw string) string {
	t.Helper()
	if len(raw) > maxFeishuCardBytes {
		t.Fatalf("oversized bilingual card: %d", len(raw))
	}
	var english, chinese any
	if err := json.Unmarshal([]byte(selectManagementCardLocale(raw, i18n.English)), &english); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(selectManagementCardLocale(raw, i18n.Chinese)), &chinese); err != nil {
		t.Fatal(err)
	}
	var texts []string
	walkManagementTestText(english, func(s string) {
		if strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) || strings.Contains(s, "session.") || strings.Contains(s, "%!") {
			t.Errorf("untranslated/invalid English copy: %q", s)
		}
		texts = append(texts, s)
	})
	assertManagementFormScope(t, english, false)
	stripManagementTestText(english)
	stripManagementTestText(chinese)
	if !reflect.DeepEqual(english, chinese) {
		t.Error("localization changed structure, identifiers, or actions")
	}
	return strings.Join(texts, "\n")
}

func TestSessionI18nPreservesLiteralContent(t *testing.T) {
	literal := "最终结果 / 问题 / 执行记录 %s /tmp/中文"
	meta := toolActivityCardMeta{Status: "done", Question: literal, Activity: &agentui.ActivityView{Status: "done", CurrentStep: literal, Plan: []agentui.PlanEntry{{Content: literal}}, Tools: []agentui.ToolEvent{{Title: literal, Status: "done"}}}, Subtasks: []subtaskProgress{{Title: literal, Status: "done", Summary: literal}}}
	en := selectManagementCardLocale(renderToolActivityCard(meta, ""), i18n.English)
	if strings.Count(en, literal) < 5 {
		t.Fatalf("literal text translated or lost: %s", en)
	}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		var card cardDoc
		if err := json.Unmarshal([]byte(selectManagementCardLocale(renderCardKitReplyData(literal), locale)), &card); err != nil {
			t.Fatal(err)
		}
		body, ok := findCardElementByID(card.Body.Elements, cardKitReplyElementID)
		if !ok || body.Content != literal || len(body.I18nContent) != 0 {
			t.Fatalf("Agent body changed: %+v", body)
		}
		heading, ok := findCardElementByID(card.Body.Elements, cardKitAnswerHeadingID)
		if !ok || heading.Content != sessionMessage("result").For(locale) {
			t.Fatal("missing localized heading")
		}
	}
}

func TestSessionI18nStreamingRetainsHeadings(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-i18n"}
	backend := &bindingReplyCardBackend{cardKit: api, sendCard: func(context.Context, string, string) (string, error) { return "message-i18n", nil }}
	ctx := context.Background()
	handle, err := backend.Create(ctx, "Agent 输出")
	if err != nil {
		t.Fatal(err)
	}
	assertSessionCardLanguages(t, renderCardKitReplyData("Agent output"))
	if err := backend.Update(ctx, handle, "最终结果 is literal Agent output"); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 1 || api.updates[0].ElementID != cardKitReplyElementID || api.updates[0].Text != "最终结果 is literal Agent output" {
		t.Fatalf("stream overwrote translated heading: %+v", api.updates)
	}
	activity := renderTurnActivity(toolActivityCardMeta{Status: "waiting", Activity: &agentui.ActivityView{Status: "waiting", CurrentStep: "Await input"}})
	if err := backend.UpdateActivity(ctx, handle, activity); err != nil {
		t.Fatal(err)
	}
	if api.inserts[0].TargetElementID != cardKitAnswerHeadingID {
		t.Fatal("activity inserted between heading and answer")
	}
	if err := backend.UpdateActivity(ctx, handle, renderTurnActivity(toolActivityCardMeta{Status: "done"})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(api.replaces[0].Element, `"en_us":"Completed"`) {
		t.Fatal("replacement lost localization")
	}
	if err := backend.Finish(ctx, handle, handle.lastText); err != nil {
		t.Fatal(err)
	}
	if !handle.finished {
		t.Fatal("stream did not finish")
	}
}

func TestSessionI18nSubtaskAndCountFallbacks(t *testing.T) {
	for _, status := range []string{"done", "failed", "timed_out", "cancelled"} {
		raw := renderSubtaskActivityCard(subtaskCardSnapshot{Progress: subtaskProgress{Title: "Task", Status: status}}, "", time.Now())
		assertSessionCardLanguages(t, raw)
	}
	for _, sample := range []struct {
		key   string
		count int
		want  string
	}{
		{"records.count", 1, "Execution records · 1 call"}, {"records.count", 2, "Execution records · 2 calls"},
		{"elapsed.seconds", 1, "1 second"}, {"elapsed.seconds", 0, "0 seconds"},
	} {
		text := sessionMessage(sample.key, sample.count)
		if text.For(i18n.English) != sample.want || text.For("unknown") != sample.want {
			t.Fatalf("plural/fallback: %v", text)
		}
	}
}

func TestSessionI18nAnswerExtractionPreservesAgentHeading(t *testing.T) {
	literal := "**最终结果**\n\nUser-requested heading stays literal"
	if got := extractCardKitAnswerText(renderCardKitReplyData(literal)); got != literal {
		t.Fatalf("Agent text changed: %q", got)
	}
	legacy := `{"body":{"elements":[{"tag":"markdown","element_id":"answer_text","content":"**最终结果**\n\nLegacy output"}]}}`
	if got := extractCardKitAnswerText(legacy); got != "Legacy output" {
		t.Fatalf("legacy heading leaked: %q", got)
	}
}
