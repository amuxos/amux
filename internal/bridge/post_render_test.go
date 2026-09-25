package bridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
)

func TestRenderAgentReplyPostMarkdownLite(t *testing.T) {
	content := renderAgentReplyPost("### 1. 看代码\n- 梳理结构\n- 修 bug\n\n```go\nfmt.Println(\"hi\")\n```\n\n普通段落")

	var got struct {
		ZhCN struct {
			Content [][]map[string]any `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderAgentReplyPost returned invalid JSON: %v\n%s", err, content)
	}

	if len(got.ZhCN.Content) != 5 {
		t.Fatalf("content blocks = %d, want 5: %#v", len(got.ZhCN.Content), got.ZhCN.Content)
	}
	assertPostText(t, got.ZhCN.Content[0][0], "1. 看代码")
	assertPostText(t, got.ZhCN.Content[1][0], "• 梳理结构")
	assertPostText(t, got.ZhCN.Content[2][0], "• 修 bug")
	if got.ZhCN.Content[3][0]["tag"] != "code_block" {
		t.Fatalf("code block tag = %#v, want code_block", got.ZhCN.Content[3][0]["tag"])
	}
	if got.ZhCN.Content[3][0]["language"] != "go" {
		t.Fatalf("code block language = %#v, want go", got.ZhCN.Content[3][0]["language"])
	}
	assertPostText(t, got.ZhCN.Content[4][0], "普通段落")
}

func TestRenderAgentReplyPostMarkdownTableAsTextRows(t *testing.T) {
	content := renderAgentReplyPost("最近提交：\n\n| 提交 | 说明 |\n|------|------|\n| abc | 修复 |\n| def | 重构 |\n\n总结")

	var got struct {
		ZhCN struct {
			Content [][]map[string]any `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderAgentReplyPost returned invalid JSON: %v\n%s", err, content)
	}

	if len(got.ZhCN.Content) != 6 {
		t.Fatalf("content blocks = %d, want 6: %#v", len(got.ZhCN.Content), got.ZhCN.Content)
	}
	assertPostText(t, got.ZhCN.Content[0][0], "最近提交：")
	assertPostText(t, got.ZhCN.Content[1][0], "| 提交 | 说明 |")
	assertPostText(t, got.ZhCN.Content[2][0], "|------|------|")
	assertPostText(t, got.ZhCN.Content[3][0], "| abc | 修复 |")
	assertPostText(t, got.ZhCN.Content[4][0], "| def | 重构 |")
	assertPostText(t, got.ZhCN.Content[5][0], "总结")
}

func TestRenderBotHandoffPostUsesNativeAtElement(t *testing.T) {
	content := renderBotHandoffPost("ou-caller-bot", "任务完成\n\n- 已修复")
	var got struct {
		ZhCN struct {
			Content [][]map[string]any `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderBotHandoffPost returned invalid JSON: %v\n%s", err, content)
	}
	if len(got.ZhCN.Content) < 2 || len(got.ZhCN.Content[0]) != 2 {
		t.Fatalf("handoff content = %#v", got.ZhCN.Content)
	}
	if got.ZhCN.Content[0][0]["tag"] != "at" || got.ZhCN.Content[0][0]["user_id"] != "ou-caller-bot" {
		t.Fatalf("first element = %#v, want native at", got.ZhCN.Content[0][0])
	}
	assertPostText(t, got.ZhCN.Content[0][1], " 协作结果")
	assertPostText(t, got.ZhCN.Content[1][0], "任务完成")
}

func TestRenderExternalBotDelegationCardKeepsStructuredMentionsAndPlainTask(t *testing.T) {
	content := renderExternalBotDelegationCard("ou-target-bot", "ou-callback-bot", "请检查设计\n  保留缩进")
	var got cardDoc
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderExternalBotDelegationCard returned invalid JSON: %v\n%s", err, content)
	}
	if got.Schema != "2.0" || !got.Config.WideScreenMode || got.Config.WidthMode != "fill" {
		t.Fatalf("delegation card config = %+v", got)
	}
	if len(got.Body.Elements) != 2 {
		t.Fatalf("delegation elements = %#v, want compatible answer and callback elements", got.Body.Elements)
	}
	answer, callback := got.Body.Elements[0], got.Body.Elements[1]
	if answer.Tag != "markdown" || answer.ElementID != cardKitReplyElementID || !strings.Contains(answer.Content, `<at id="ou-target-bot"></at>`) || !strings.Contains(answer.Content, amuxExternalBotDelegationProtocolURL()) {
		t.Fatalf("answer element = %#v, want compatible answer envelope with target mention and protocol marker", answer)
	}
	if !strings.Contains(answer.Content, "请检查设计\n  保留缩进") {
		t.Fatalf("answer element = %#v, want task formatting preserved", answer)
	}
	if callback.Tag != "markdown" || callback.ElementID != externalBotDelegationCallbackElementID || !strings.Contains(callback.Content, "完成后请在本话题回复并") || !strings.Contains(callback.Content, `<at id="ou-callback-bot"></at>`) {
		t.Fatalf("callback element = %#v, want native callback Bot mention", callback)
	}
	for _, leaked := range []string{"回传要求", "结构化", "自动回收"} {
		if strings.Contains(callback.Content, leaked) {
			t.Fatalf("callback text = %q, want no internal protocol term %q", callback.Content, leaked)
		}
	}
}

func TestRenderExternalBotDelegationCardKeepsLegacyAnswerTextEnvelope(t *testing.T) {
	content := renderExternalBotDelegationCard("ou-target-bot", "ou-callback-bot", "请检查设计\n  保留缩进")
	legacyText := extractCardKitAnswerText(content)
	if legacyText == "" {
		t.Fatal("legacy CardKit answer extractor returned empty text")
	}
	for _, want := range []string{
		amuxExternalBotDelegationProtocolURL(),
		"请检查设计\n  保留缩进",
	} {
		if !strings.Contains(legacyText, want) {
			t.Fatalf("legacy CardKit text = %q, want %q", legacyText, want)
		}
	}
	if strings.Contains(legacyText, "完成后请在本话题回复并") {
		t.Fatalf("legacy CardKit text = %q, want callback kept outside legacy task input", legacyText)
	}
}

func TestRenderAgentReplyCardUsesMarkdownElement(t *testing.T) {
	markdown := "# 标题\n\n- item\n\n```go\nfmt.Println(1)\n```"
	content := renderAgentReplyCard("\n" + markdown + "\n")

	var got struct {
		Schema string `json:"schema"`
		Config struct {
			WideScreenMode bool `json:"wide_screen_mode"`
		} `json:"config"`
		Body struct {
			Elements []struct {
				Tag     string `json:"tag"`
				Content string `json:"content"`
			} `json:"elements"`
		} `json:"body"`
		LegacyElements []any `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderAgentReplyCard returned invalid JSON: %v\n%s", err, content)
	}
	if got.Schema != "2.0" {
		t.Fatalf("schema = %q, want 2.0", got.Schema)
	}
	if !got.Config.WideScreenMode {
		t.Fatalf("wide_screen_mode = false, want true")
	}
	if len(got.LegacyElements) != 0 {
		t.Fatalf("top-level elements = %#v, want none for card json 2.0", got.LegacyElements)
	}
	if len(got.Body.Elements) != 3 {
		t.Fatalf("body.elements = %#v, want answer and branded footer", got.Body.Elements)
	}
	if got.Body.Elements[0].Tag != "markdown" {
		t.Fatalf("tag = %q, want markdown", got.Body.Elements[0].Tag)
	}
	if got.Body.Elements[0].Content != markdown {
		t.Fatalf("content = %q, want %q", got.Body.Elements[0].Content, markdown)
	}
	if got.Body.Elements[1].Tag != "hr" || got.Body.Elements[2].Tag != "markdown" || got.Body.Elements[2].Content != amuxBrandFooterMarkdown() {
		t.Fatalf("brand footer = %#v, want divider and %q", got.Body.Elements[1:], amuxBrandFooterMarkdown())
	}
}

type decodedActivityCard struct {
	Schema string          `json:"schema"`
	Config cardConfig      `json:"config"`
	Header json.RawMessage `json:"header"`
	Body   cardBody        `json:"body"`
}

func decodeActivityCard(t *testing.T, content string) decodedActivityCard {
	t.Helper()
	var got decodedActivityCard
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("renderToolActivityCard returned invalid JSON: %v\n%s", err, content)
	}
	return got
}

func TestRenderToolActivityCardIncludesTurnStatusAndQuestion(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq:  7,
		Status:   "running",
		Question: "分析当前仓库，查看分层是否合理",
	}, "Agent 正在工作...\n\n工具调用：1 次\n运行中：bash x1")

	got := decodeActivityCard(t, content)
	if got.Schema != "2.0" {
		t.Fatalf("schema = %q, want 2.0", got.Schema)
	}
	if !got.Config.WideScreenMode {
		t.Fatalf("wide_screen_mode = false, want true")
	}
	if len(got.Header) != 0 {
		t.Fatalf("legacy activity root header = %s, want omitted", got.Header)
	}
	if len(got.Body.Elements) != 1 || got.Body.Elements[0].Tag != "column_set" {
		t.Fatalf("body.elements = %#v, want one activity container", got.Body.Elements)
	}
	panel, _ := turnActivityLayout(t, got.Body.Elements[0])
	if panel.Expanded || panel.Header == nil || panel.Header.Title.Tag != "plain_text" || panel.Header.Title.Content != "正在处理" {
		t.Fatalf("activity panel = %+v, want collapsed running panel", panel)
	}
	if len(panel.Elements) != 1 || panel.Elements[0].Tag != "markdown" {
		t.Fatalf("panel elements = %#v, want one markdown element", panel.Elements)
	}
	wantContent := "Q: 分析当前仓库，查看分层是否合理\n\nAgent 正在工作...\n\n工具调用：1 次\n运行中：bash x1"
	if panel.Elements[0].Content != wantContent {
		t.Fatalf("content = %q", panel.Elements[0].Content)
	}
}

func TestRenderToolActivityCardIncludesReadOnlyViewerButton(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq: 1,
		Status:  "running",
		ViewURL: "https://amux.example.com/s/scoped-token",
	}, "工具调用：1 次")

	got := decodeActivityCard(t, content)
	if got.Schema != "2.0" {
		t.Fatalf("schema = %q, want 2.0", got.Schema)
	}
	if len(got.Body.Elements) != 1 {
		t.Fatalf("body.elements = %#v, want one shared turn-card container", got.Body.Elements)
	}
	children := turnCardChildren(t, got.Body.Elements[0])
	if len(children) != 2 || children[0].ElementID != cardKitActivityContentID {
		t.Fatalf("turn card children = %#v, want activity content plus action", children)
	}
	action := children[1]
	if action.Tag != "action" || len(action.Actions) != 1 {
		t.Fatalf("action element = %#v, want one button", action)
	}
	button := action.Actions[0]
	if button.Tag != "button" || button.Text == nil || button.Text.Content != "查看完整输出" {
		t.Fatalf("button = %#v, want 查看完整输出", button)
	}
	if button.URL != "https://amux.example.com/s/scoped-token" {
		t.Fatalf("button url = %q", button.URL)
	}
}

func TestRenderToolActivityCardUsesStructuredPresentation(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq:      3,
		Status:       "running",
		ViewURL:      "https://amux.example.com/s/token",
		AgentProfile: "team/reviewer",
		Subtasks:     []subtaskProgress{{Title: "查询降级", Status: "running"}},
		Activity: &agentui.ActivityView{
			TurnSeq:     3,
			Status:      "running",
			StartedAt:   time.Now().Add(-3 * time.Second),
			CurrentStep: "补齐统一事件",
			Plan: []agentui.PlanEntry{
				{Content: "检查原始事件", Status: "completed"},
				{Content: "补齐统一事件", Status: "in_progress"},
			},
			Tools: []agentui.ToolEvent{
				{ID: "run", Title: "Run tests", Status: "running", StartedAt: time.Now().Add(-2 * time.Second)},
				{ID: "read", Title: "Read proc.go", Status: "completed"},
			},
			UsageJSON: json.RawMessage(`{"used":14000,"size":100000}`),
		},
	}, "fingerprint")

	got := decodeActivityCard(t, content)
	if len(got.Header) != 0 {
		t.Fatalf("legacy activity root header = %s, want omitted", got.Header)
	}
	if len(got.Body.Elements) != 1 {
		t.Fatalf("body elements = %#v, want one shared turn-card container", got.Body.Elements)
	}
	panel, progress := turnActivityLayout(t, got.Body.Elements[0])
	if panel.Header == nil {
		t.Fatalf("execution summary = %+v", panel)
	}
	if panel.Header.Title.TextSize != "notation" || panel.Header.Title.TextColor != "grey" {
		t.Fatalf("execution summary style = size:%q color:%q, want quiet notation/grey", panel.Header.Title.TextSize, panel.Header.Title.TextColor)
	}
	headerTitle := panel.Header.Title.Content
	if !strings.Contains(headerTitle, "秒") {
		t.Fatalf("header title = %q, want elapsed time", headerTitle)
	}
	if !strings.Contains(headerTitle, "team/reviewer") || !strings.Contains(headerTitle, "协作任务 1 个进行中") {
		t.Fatalf("header title = %q, want profile and collaboration-task count", headerTitle)
	}
	wantTags := map[string]bool{
		"plan_progress":       false,
		"execution_records":   false,
		"collaboration_tasks": false,
	}
	for _, elem := range panel.Elements {
		if elem.ElementID != "" {
			wantTags[elem.ElementID] = true
		}
		if id := elem.ElementID; id == "execution_records" || id == "collaboration_tasks" {
			if elem.Tag != "column_set" {
				t.Fatalf("section %v tag = %#v, want column_set", id, elem.Tag)
			}
			if len(elem.Columns) != 1 {
				t.Fatalf("section %v columns = %#v, want one column", id, elem.Columns)
			}
			if id == "execution_records" && (elem.Columns[0].BackgroundStyle != "" || elem.Columns[0].Padding != "0px 12px" || !hasExecutionRecordDividers(elem)) {
				t.Fatalf("execution records = %#v, want compact divided block", elem)
			}
			if id == "collaboration_tasks" && (elem.Columns[0].BackgroundStyle != "grey-50" || elem.Columns[0].Padding != "10px 12px") {
				t.Fatalf("collaboration tasks = %#v, want padded grey block", elem)
			}
		}
	}
	for id, present := range wantTags {
		if !present {
			t.Errorf("missing structured element %q: %s", id, content)
		}
	}
	if progress == nil || progress.ElementID != "processing_update" || progress.Tag != "collapsible_panel" ||
		!progress.Expanded || progress.Header == nil || progress.Header.Title.Tag != "plain_text" ||
		progress.Header.Title.Content != "处理过程" || progress.Header.Title.TextSize != "normal" ||
		progress.Header.Title.TextColor != "grey" || progress.Header.Padding != "4px 0px 6px 0px" {
		t.Fatalf("processing block = %+v, want expanded independent panel after execution summary", progress)
	}
	if findActivityDetailByID(panel.Elements, "processing_update") != nil {
		t.Fatalf("processing block must not be inside collapsible summary: %+v", panel.Elements)
	}
	children := turnCardChildren(t, got.Body.Elements[0])
	if len(children) != 2 || children[1].ElementID != "viewer_action" {
		t.Fatalf("turn card children = %#v, want trailing viewer_action", children)
	}
	if strings.Contains(content, "**工具**") || strings.Contains(content, "工具\\\\n") {
		t.Fatalf("running activity card should not render standalone tool metric: %s", content)
	}
}

func TestRenderTurnActivityAppliesThinkingProcessPresentation(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         string
		wantProgress bool
		wantExpanded bool
	}{
		{name: "expanded", mode: config.FeishuThinkingProcessExpanded, wantProgress: true, wantExpanded: true},
		{name: "collapsed", mode: config.FeishuThinkingProcessCollapsed, wantProgress: true, wantExpanded: false},
		{name: "hidden", mode: config.FeishuThinkingProcessHidden, wantProgress: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := toolActivityCardMeta{
				Status:          "running",
				ThinkingProcess: test.mode,
				Activity: &agentui.ActivityView{
					Status:      "running",
					CurrentStep: "正在分析仓库",
				},
			}
			_, progress := turnActivityLayout(t, renderTurnActivity(meta).Element)
			if test.wantProgress {
				if progress == nil || progress.Expanded != test.wantExpanded ||
					!strings.Contains(activityElementText(*progress), "正在分析仓库") {
					t.Fatalf("processing panel = %+v, want present expanded=%v", progress, test.wantExpanded)
				}
				return
			}
			if progress != nil {
				t.Fatalf("processing panel = %+v, want hidden", progress)
			}
		})
	}
}

func TestRenderCompactActivityAppliesThinkingProcessPresentation(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         string
		wantProgress bool
		wantExpanded bool
	}{
		{name: "expanded", mode: config.FeishuThinkingProcessExpanded, wantProgress: true, wantExpanded: true},
		{name: "collapsed", mode: config.FeishuThinkingProcessCollapsed, wantProgress: true, wantExpanded: false},
		{name: "hidden", mode: config.FeishuThinkingProcessHidden, wantProgress: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := toolActivityCardMeta{
				Status:          "running",
				ThinkingProcess: test.mode,
				Activity: &agentui.ActivityView{
					Status:      "running",
					CurrentStep: strings.Repeat("处理进展", maxCompactActivityStepRunes),
				},
			}
			_, progress := turnActivityLayout(t, renderCompactActivityElement(meta))
			if test.wantProgress {
				if progress == nil || progress.Expanded != test.wantExpanded {
					t.Fatalf("compact processing panel = %+v, want present expanded=%v", progress, test.wantExpanded)
				}
				return
			}
			if progress != nil {
				t.Fatalf("compact processing panel = %+v, want hidden", progress)
			}
		})
	}
}

func TestRenderToolActivityCardReusesTurnActivityPanel(t *testing.T) {
	for _, status := range []string{"running", "done", "waiting", "failed", "stopped"} {
		t.Run(status, func(t *testing.T) {
			meta := toolActivityCardMeta{
				Status:       status,
				AgentProfile: "team/reviewer",
				ViewURL:      "https://amux.example.com/s/token",
				Activity: &agentui.ActivityView{
					Status:      status,
					CurrentStep: "检查降级样式",
				},
			}
			content := renderToolActivityCard(meta, "fingerprint")
			card := decodeActivityCard(t, content)
			if len(card.Header) != 0 {
				t.Fatalf("legacy activity card has root header: %s", content)
			}
			if len(card.Body.Elements) != 1 {
				t.Fatalf("body elements = %#v, want one shared turn-card container", card.Body.Elements)
			}
			activity := renderTurnActivity(meta)
			children := turnCardChildren(t, card.Body.Elements[0])
			gotJSON, err := json.Marshal(children[0])
			if err != nil {
				t.Fatalf("encode legacy activity element: %v", err)
			}
			if string(gotJSON) != string(activity.JSON) {
				t.Fatalf("legacy activity element = %s, want %s", gotJSON, activity.JSON)
			}
			panel, _ := turnActivityLayout(t, card.Body.Elements[0])
			if got, want := panel.Expanded, false; got != want {
				t.Fatalf("legacy activity panel expanded=%v for status %q, want %v", got, status, want)
			}
			if len(children) != 2 || children[1].ElementID != "viewer_action" {
				t.Fatalf("legacy activity children = %#v, want trailing viewer", children)
			}
		})
	}
}

func TestRenderToolActivityCardShowsDoneStatus(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq:  8,
		Status:   "done",
		Question: "继续",
	}, "工具调用：3 次\n已完成：bash x3")

	got := decodeActivityCard(t, content)
	if len(got.Body.Elements) != 1 {
		t.Fatalf("activity container = %+v, want one element", got.Body.Elements)
	}
	panel, _ := turnActivityLayout(t, got.Body.Elements[0])
	if panel.Header == nil || panel.Header.Title.Tag != "plain_text" || panel.Header.Title.Content != "已完成" {
		t.Fatalf("activity panel = %+v, want completed title", got.Body.Elements)
	}
}

func TestRenderCompletedLegacyActivityReusesTurnActivityDetails(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		Status:   "done",
		Question: "原始问题仍应保留",
		Activity: &agentui.ActivityView{
			Status: "done", CurrentStep: "old detail",
			Plan:      []agentui.PlanEntry{{Content: "old plan", Status: "completed"}},
			Tools:     []agentui.ToolEvent{{ID: "tool-1", Title: "old tool", Status: "completed"}},
			UsageJSON: json.RawMessage(`{"input_tokens":30,"output_tokens":12,"total_tokens":42,"cumulative_input_tokens":343,"cumulative_cached_input_tokens":300,"cumulative_output_tokens":80}`),
		},
	}, "fingerprint")
	card := decodeActivityCard(t, content)
	if len(card.Header) != 0 || len(card.Body.Elements) != 1 {
		t.Fatalf("completed legacy activity card = %+v, want one headerless panel", card)
	}
	process := card.Body.Elements[0]
	panel, progress := turnActivityLayout(t, process)
	if panel.Expanded {
		t.Fatalf("completed activity panel = %+v, want collapsed execution summary", panel)
	}
	if panel.Header == nil || !strings.Contains(panel.Header.Title.Content, "已完成") || !strings.Contains(panel.Header.Title.Content, "42 tokens · 累计输入 300/343（缓存/总量） · 累计输出 80") {
		t.Fatalf("completed activity panel header = %+v", panel.Header)
	}
	if progress == nil || !strings.Contains(activityElementText(*progress), "old detail") || findActivityDetailByID(panel.Elements, "processing_update") != nil {
		t.Fatalf("completed processing block = %+v, want visible outside folded details", progress)
	}
	if !strings.Contains(content, "old detail") || !strings.Contains(content, "old plan") || !strings.Contains(content, "old tool") {
		t.Fatalf("completed card missing execution details: %s", content)
	}
	if !strings.Contains(content, "**问题**") || !strings.Contains(content, "原始问题仍应保留") || !strings.Contains(content, `"background_style":"blue-50"`) {
		t.Fatalf("completed card missing question callout: %s", content)
	}
	if strings.Contains(content, "activity_metrics") {
		t.Fatalf("completed card must not render overview metrics: %s", content)
	}
}

func TestRenderTurnActivityKeepsLongProgressBeyondFormerSummaryLimit(t *testing.T) {
	progress := "先检查仓库。\n\n" + strings.Repeat("保留这段处理动态", 80) + "\n\n最后核对测试。"
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:   "done",
		Activity: &agentui.ActivityView{Status: "done", CurrentStep: progress},
	})
	got := string(activity.JSON)
	if !strings.Contains(got, "先检查仓库") || !strings.Contains(got, "最后核对测试") {
		t.Fatalf("activity lost long progress: %s", got)
	}
	if strings.Contains(got, omittedAssistantProgress) {
		t.Fatalf("ordinary progress was unnecessarily truncated: %s", got)
	}
	panel, progressBlock := turnActivityLayout(t, activity.Element)
	if panel.Expanded || progressBlock == nil || !progressBlock.Expanded {
		t.Fatal("completed execution summary should default collapsed while processing remains expanded")
	}
}

func TestRenderTurnActivityAdaptsLongProgressToCardBudget(t *testing.T) {
	progress := "开始检查。" + strings.Repeat("很长的处理动态", 2000) + "最新进展。"
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Activity: &agentui.ActivityView{Status: "running", CurrentStep: progress},
	})
	got := string(activity.JSON)
	if len(activity.JSON) > maxTurnCardActivityBytes {
		t.Fatalf("activity JSON = %d bytes, want <= %d", len(activity.JSON), maxTurnCardActivityBytes)
	}
	for _, want := range []string{"开始检查", omittedAssistantProgress, "最新进展"} {
		if !strings.Contains(got, want) {
			t.Fatalf("adapted progress missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "执行详情过长") {
		t.Fatalf("progress should be adapted before whole-card fallback: %s", got)
	}
}

func TestRenderTurnActivityDetailsOmitsOverview(t *testing.T) {
	details := renderTurnActivityDetails(toolActivityCardMeta{
		Question: "检查降级情况",
		Subtasks: []subtaskProgress{
			{Title: "查询主链路", Status: "running"},
			{Title: "查询降级", Status: "succeeded"},
		},
		Activity: &agentui.ActivityView{
			Status: "running",
			Tools: []agentui.ToolEvent{
				{Title: "profile_list", Status: "completed"},
				{Title: "task_submit", Status: "completed"},
				{Title: "task_stream", Status: "running"},
			},
			UsageJSON: json.RawMessage(`{"total_tokens":34600}`),
		},
	})
	if len(details) < 2 || details[0].Tag != "column_set" || len(details[0].Columns) != 1 ||
		!strings.Contains(details[0].Columns[0].Elements[0].Content, "**问题**") {
		t.Fatalf("details[0] = %+v, want question first", details)
	}
	for _, detail := range details {
		if detail.ElementID == "activity_metrics" || strings.Contains(activityElementText(detail), "**子任务**\n") {
			t.Fatalf("details still contain overview metrics: %+v", details)
		}
	}
}

func TestRenderCompletedTurnActivityKeepsQuestion(t *testing.T) {
	details := renderTurnActivityDetails(toolActivityCardMeta{
		Question: "完成后仍然展示这个问题",
		Subtasks: []subtaskProgress{{Title: "查询降级", Status: "running"}},
		Activity: &agentui.ActivityView{
			Status: "done",
			Tools:  []agentui.ToolEvent{{Title: "task_stream", Status: "completed"}},
		},
	})
	if len(details) == 0 || details[0].Tag != "column_set" || !strings.Contains(activityElementText(details[0]), "完成后仍然展示这个问题") {
		t.Fatalf("completed details = %+v, want question callout first", details)
	}
	for _, detail := range details {
		if detail.ElementID == "activity_metrics" {
			t.Fatalf("completed details contain removed overview: %+v", details)
		}
	}
}

func TestRenderTurnActivityDetailsUsesScannableSectionHeadings(t *testing.T) {
	details := renderTurnActivityDetails(toolActivityCardMeta{
		Subtasks: []subtaskProgress{{Title: "检查降级", Status: "running"}},
		Activity: &agentui.ActivityView{
			Status: "running",
			Plan:   []agentui.PlanEntry{{Content: "确认影响", Status: "in_progress"}},
			Tools: []agentui.ToolEvent{
				{Title: "task_submit", Status: "completed"},
				{Title: "task_stream", Status: "running"},
			},
		},
	})
	var content strings.Builder
	for _, detail := range details {
		content.WriteString(activityElementText(detail))
		content.WriteByte('\n')
	}
	for _, want := range []string{"**计划进度 0/1**", "**执行记录 · 2 次**", "**协作任务 · 1 个**"} {
		if !strings.Contains(content.String(), want) {
			t.Fatalf("details = %q, missing %q", content.String(), want)
		}
	}
}

func TestRenderTurnActivityStylesExecutionAndCollaborationBlocks(t *testing.T) {
	details := renderTurnActivityDetails(toolActivityCardMeta{
		Subtasks: []subtaskProgress{{Title: "检查降级", Status: "running"}},
		Activity: &agentui.ActivityView{
			Status: "running",
			Tools:  []agentui.ToolEvent{{Title: "task_stream", Status: "running"}},
		},
	})

	for _, heading := range []string{"**执行记录 · 1 次**", "**协作任务 · 1 个**"} {
		var found *cardElement
		for i := range details {
			if strings.Contains(activityElementText(details[i]), heading) {
				found = &details[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("missing section %q in %+v", heading, details)
		}
		if found.Tag != "column_set" || len(found.Columns) != 1 {
			t.Fatalf("section %q = %+v, want single-column block", heading, found)
		}
		if heading == "**执行记录 · 1 次**" && (found.Columns[0].BackgroundStyle != "" || found.Columns[0].Padding != "0px 12px" || !hasExecutionRecordDividers(*found)) {
			t.Fatalf("execution section = %+v, want compact divided block", found)
		}
		if heading == "**协作任务 · 1 个**" && (found.Columns[0].BackgroundStyle != "grey-50" || found.Columns[0].Padding != "10px 12px") {
			t.Fatalf("collaboration section = %+v, want padded grey block", found)
		}
	}
}

func TestRenderTurnActivityUsesCompactQuestionAndExecutionRecords(t *testing.T) {
	activity := renderTurnActivity(toolActivityCardMeta{
		Question: "看看有哪些 MCP",
		Activity: &agentui.ActivityView{
			Status: "done",
			Tools: []agentui.ToolEvent{
				{Name: "codex/list_mcp_resources", Title: "codex/list_mcp_resources", Status: "completed"},
				{Name: "codex/list_mcp_resource_templates", Title: "codex/list_mcp_resource_templates", Status: "completed"},
			},
		},
	})
	got := string(activity.JSON)
	for _, want := range []string{
		`"vertical_spacing":"8px"`,
		`"element_id":"turn_question"`,
		`"content":"**问题**　看看有哪些 MCP"`,
		`"background_style":"blue-50"`,
		`"padding":"0px 12px"`,
		`"element_id":"execution_records"`,
		`\u003cfont color='grey'\u003ecodex/\u003c/font\u003elist_mcp_resources`,
		`\u003cfont color='grey'\u003ecodex/\u003c/font\u003elist_mcp_resource_templates`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact activity JSON = %s, missing %s", got, want)
		}
	}
}

func TestRenderToolRowsDoesNotStyleSlashInDisplayTitle(t *testing.T) {
	active, _ := renderToolRows([]agentui.ToolEvent{{
		Name:   "shell",
		Title:  "go test ./...",
		Status: "running",
	}}, time.Now())
	if strings.Contains(active, "<font") {
		t.Fatalf("renderToolRows() = %q, must not infer a source prefix from display text", active)
	}
	if !strings.Contains(active, "go test ./...") {
		t.Fatalf("renderToolRows() = %q, missing original display title", active)
	}
}

func TestRenderTurnActivityDetailsUsesUnifiedContainers(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "running", status: "running"},
		{name: "completed", status: "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := toolActivityCardMeta{
				Question: "统一活动详情样式",
				Subtasks: []subtaskProgress{{Title: "检查样式", Status: "running"}},
				Activity: &agentui.ActivityView{
					Status:      tt.status,
					CurrentStep: "检查活动详情",
					Plan:        []agentui.PlanEntry{{Content: "统一容器", Status: "in_progress"}},
					Tools:       []agentui.ToolEvent{{Title: "go test", Status: "running"}},
				},
			}
			details := renderTurnActivityDetails(meta)

			want := map[string]struct {
				background string
				padding    string
				content    string
				dividers   bool
			}{
				"turn_question":       {background: "blue-50", padding: "8px 12px", content: "**问题**"},
				"plan_progress":       {background: "grey-50", padding: "10px 12px", content: "**计划进度 0/1**"},
				"execution_records":   {padding: "0px 12px", content: "**执行记录 · 1 次**", dividers: true},
				"collaboration_tasks": {background: "grey-50", padding: "10px 12px", content: "**协作任务 · 1 个**"},
			}
			for elementID, style := range want {
				detail := findActivityDetailByID(details, elementID)
				if detail == nil {
					t.Fatalf("missing detail %q in %+v", elementID, details)
				}
				if detail.Tag != "column_set" || len(detail.Columns) != 1 {
					t.Fatalf("detail %q = %+v, want single-column container", elementID, detail)
				}
				column := detail.Columns[0]
				if column.BackgroundStyle != style.background || column.Padding != style.padding {
					t.Fatalf("detail %q style = background:%q padding:%q, want background:%q padding:%q", elementID, column.BackgroundStyle, column.Padding, style.background, style.padding)
				}
				if style.dividers && !hasExecutionRecordDividers(*detail) {
					t.Fatalf("detail %q = %+v, want top and bottom dividers", elementID, detail)
				}
				if !strings.Contains(activityElementText(*detail), style.content) {
					t.Fatalf("detail %q = %q, missing %q", elementID, activityElementText(*detail), style.content)
				}
			}
			for _, obsoleteID := range []string{"current_step", "final_step"} {
				if detail := findActivityDetailByID(details, obsoleteID); detail != nil {
					t.Fatalf("obsolete detail %q still rendered: %+v", obsoleteID, detail)
				}
			}
			if detail := findActivityDetailByID(details, "processing_update"); detail != nil {
				t.Fatalf("processing block leaked into collapsible details: %+v", detail)
			}
			execution, progress := turnActivityLayout(t, renderTurnActivity(meta).Element)
			if execution.Header == nil || execution.Header.Title.TextSize != "notation" || execution.Header.Title.TextColor != "grey" || execution.VerticalSpacing != "8px" {
				t.Fatalf("quiet execution summary header = %+v", execution)
			}
			if progress == nil || progress.Tag != "collapsible_panel" || !progress.Expanded ||
				progress.Header == nil || progress.Header.Title.Tag != "plain_text" ||
				progress.Header.Title.Content != "处理过程" || progress.Header.Title.TextSize != "normal" ||
				progress.Header.Title.TextColor != "grey" || progress.Header.Padding != "4px 0px 6px 0px" || len(progress.Elements) != 1 ||
				progress.Elements[0].Tag != "column_set" || len(progress.Elements[0].Columns) != 1 ||
				progress.Elements[0].Columns[0].BackgroundStyle != "grey-50" || progress.Elements[0].Columns[0].Padding != "10px 12px" ||
				!strings.Contains(activityElementText(*progress), "检查活动详情") {
				t.Fatalf("always-visible processing block = %+v", progress)
			}
		})
	}
}

func TestTurnCardElementIDsMeetCardKitConstraints(t *testing.T) {
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:   "done",
		Question: "检查卡片 element ID",
		ViewURL:  "https://viewer.example/session",
		Subtasks: []subtaskProgress{{ID: "task-1", Title: "检查协作任务", Status: "done"}},
		Activity: &agentui.ActivityView{
			Status:      "done",
			CurrentStep: "检查处理过程",
			Plan:        []agentui.PlanEntry{{Content: "检查计划", Status: "completed"}},
			Tools:       []agentui.ToolEvent{{ID: "tool-1", Title: "检查工具", Status: "completed"}},
		},
	})
	cards := []string{
		renderCardKitData(activity.elements()),
		renderCardKitReplyData("最终结果"),
	}
	seen := map[string]bool{}
	for _, raw := range cards {
		var card struct {
			Body struct {
				Elements []cardElement `json:"elements"`
			} `json:"body"`
		}
		if err := json.Unmarshal([]byte(raw), &card); err != nil {
			t.Fatalf("decode CardKit card: %v\n%s", err, raw)
		}
		assertCardKitElementIDs(t, card.Body.Elements, seen)
	}
	if !seen["processing_body"] {
		t.Fatalf("processing body ID missing: %+v", seen)
	}
	if seen["processing_update_content"] {
		t.Fatalf("obsolete overlong processing ID is still rendered: %+v", seen)
	}
}

func assertCardKitElementIDs(t *testing.T, elements []cardElement, seen map[string]bool) {
	t.Helper()
	for _, element := range elements {
		if id := element.ElementID; id != "" {
			seen[id] = true
			if len(id) > 20 || !isASCIIAlpha(id[0]) {
				t.Errorf("invalid CardKit element ID %q: must start with a letter and contain at most 20 characters", id)
			}
			for i := 1; i < len(id); i++ {
				if c := id[i]; !isASCIIAlpha(c) && (c < '0' || c > '9') && c != '_' {
					t.Errorf("invalid CardKit element ID %q: unsupported character %q", id, c)
				}
			}
		}
		assertCardKitElementIDs(t, element.Elements, seen)
		assertCardKitElementIDs(t, element.Actions, seen)
		for _, column := range element.Columns {
			assertCardKitElementIDs(t, column.Elements, seen)
		}
	}
}

func isASCIIAlpha(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func findActivityDetailByID(details []cardElement, elementID string) *cardElement {
	for i := range details {
		if details[i].ElementID == elementID {
			return &details[i]
		}
	}
	return nil
}

func hasExecutionRecordDividers(element cardElement) bool {
	if len(element.Columns) != 1 {
		return false
	}
	elements := element.Columns[0].Elements
	return len(elements) == 3 && elements[0].Tag == "hr" && elements[2].Tag == "hr"
}

func turnActivityLayout(t *testing.T, activity cardElement) (cardElement, *cardElement) {
	t.Helper()
	if activity.ElementID == cardKitActivityElementID {
		children := turnCardChildren(t, activity)
		if len(children) == 0 {
			t.Fatalf("turn card = %+v, want activity content", activity)
		}
		activity = children[0]
	}
	if activity.Tag != "column_set" || activity.ElementID != cardKitActivityContentID || len(activity.Columns) != 1 {
		t.Fatalf("activity content = %+v, want one-column %s", activity, cardKitActivityContentID)
	}
	children := activity.Columns[0].Elements
	if len(children) == 0 || children[0].Tag != "collapsible_panel" || children[0].ElementID != cardKitExecutionSummaryID {
		t.Fatalf("activity children = %+v, want execution summary first", children)
	}
	for i := range children[1:] {
		child := &children[i+1]
		if child.ElementID == "processing_update" {
			return children[0], child
		}
	}
	return children[0], nil
}

func turnCardChildren(t *testing.T, root cardElement) []cardElement {
	t.Helper()
	if root.Tag != "column_set" || root.ElementID != cardKitActivityElementID || len(root.Columns) != 1 {
		t.Fatalf("turn card root = %+v, want one-column %s", root, cardKitActivityElementID)
	}
	return root.Columns[0].Elements
}

func TestRenderTurnActivityHeaderShowsProfileAndUsage(t *testing.T) {
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:         "running",
		AgentProfile:   "example/reviewer@89eaacfc52fed728e538a6a88274eac5602901c4",
		AgentType:      "codex",
		EffectiveModel: "gpt-5",
		Now:            time.Unix(108, 0),
		Subtasks:       []subtaskProgress{{Title: "查询状态", Status: "running"}},
		Activity: &agentui.ActivityView{
			Status:    "running",
			StartedAt: time.Unix(100, 0),
			UsageJSON: json.RawMessage(`{"input_tokens":20000,"output_tokens":2800,"total_tokens":22800,"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000}`),
		},
	})
	panel, _ := turnActivityLayout(t, activity.Element)
	if panel.Header == nil {
		t.Fatal("activity header is nil")
	}
	got := panel.Header.Title.Content
	if panel.Header.Title.Tag != "plain_text" || got != "正在处理 · example/reviewer · 模型 gpt-5 · 8 秒 · 22.8k tokens · 累计输入 300k/343k（缓存/总量） · 累计输出 12k · 协作任务 1 个进行中" {
		t.Fatalf("header = %q, want unpinned profile ref and usage", got)
	}
	if strings.Contains(got, "codex") {
		t.Fatalf("header = %q, agent type should only be a fallback", got)
	}

	legacy := renderTurnActivity(toolActivityCardMeta{Status: "running", AgentType: "codex"})
	legacyPanel, _ := turnActivityLayout(t, legacy.Element)
	if legacyPanel.Header == nil || legacyPanel.Header.Title.Content != "正在处理 · codex" {
		t.Fatalf("legacy header = %+v, want agent type fallback", legacyPanel.Header)
	}

	completedSubtask := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Subtasks: []subtaskProgress{{Title: "查询状态", Status: "succeeded"}},
	})
	completedSubtaskPanel, _ := turnActivityLayout(t, completedSubtask.Element)
	if completedSubtaskPanel.Header == nil || completedSubtaskPanel.Header.Title.Content != "正在处理 · 协作任务 1 个" {
		t.Fatalf("completed-subtask header = %+v, want total collaboration count while parent still runs", completedSubtaskPanel.Header)
	}
	timedOutSubtask := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Subtasks: []subtaskProgress{{Title: "查询状态", Status: "timed_out"}},
	})
	timedOutSubtaskPanel, _ := turnActivityLayout(t, timedOutSubtask.Element)
	if timedOutSubtaskPanel.Header == nil || timedOutSubtaskPanel.Header.Title.Content != "正在处理 · 协作任务 1 个" {
		t.Fatalf("timed-out subtask header = %+v, terminal task must not be shown as running", timedOutSubtaskPanel.Header)
	}
}

func activityElementText(element cardElement) string {
	var text strings.Builder
	text.WriteString(element.Content)
	for _, child := range element.Elements {
		text.WriteString(activityElementText(child))
	}
	for _, column := range element.Columns {
		for _, child := range column.Elements {
			text.WriteString(activityElementText(child))
		}
	}
	return text.String()
}

func TestRenderTurnActivityUsesNativeBackgroundStyles(t *testing.T) {
	activity := renderTurnActivity(toolActivityCardMeta{
		Question: "clone amux 代码到工作区，并检查可用的专家配置",
		Subtasks: []subtaskProgress{{Title: "检查专家", Status: "running"}},
		Activity: &agentui.ActivityView{
			Status:    "running",
			Tools:     []agentui.ToolEvent{{Title: "profile_list", Status: "completed"}},
			UsageJSON: json.RawMessage(`{"total_tokens":22800}`),
		},
	})
	got := string(activity.JSON)
	for _, want := range []string{
		`"background_style":"blue-50"`,
		`"background_style":"grey-50"`,
		`"padding":"10px 12px"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("activity JSON = %s, missing %s", got, want)
		}
	}
}

func TestRenderSubtaskLineRendersTerminalDetailAsPlainText(t *testing.T) {
	got := renderSubtaskLine(subtaskProgress{
		Title:   "查询降级",
		Status:  "succeeded",
		Summary: "**`data.aweme.psort` 降级情况汇总**\n### 查询时间：12:16 ~~旧结果~~；查看 *.go；2 * 3；_强调_；`__init__`；__raw_name__",
	})
	want := "data.aweme.psort 降级情况汇总 查询时间：12:16 旧结果；查看 \\*.go；2 \\* 3；强调；\\_\\_init\\_\\_；\\_\\_raw_name\\_\\_"
	if !strings.Contains(got, want) {
		t.Fatalf("renderSubtaskLine() = %q, want plain detail %q", got, want)
	}
	for _, marker := range []string{"**", "`", "###", "~~"} {
		if strings.Contains(got, marker) {
			t.Fatalf("renderSubtaskLine() = %q, still contains markdown marker %q", got, marker)
		}
	}
}

func TestRenderPlanNumbersExplicitEntries(t *testing.T) {
	got := renderPlan([]agentui.PlanEntry{
		{Content: "检查配置", Status: "completed"},
		{Content: "委派分析", Status: "in_progress"},
		{Content: "汇总结论", Status: "pending"},
	})
	for _, want := range []string{"✓ 1. 检查配置", "● 2. 委派分析", "○ 3. 汇总结论"} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderPlan() = %q, missing %q", got, want)
		}
	}
}

func TestRenderToolRowsGroupsRepeatedTitlesByStatus(t *testing.T) {
	active, completed := renderToolRows([]agentui.ToolEvent{
		{Title: "amux.task_submit", Status: "completed", OutputPreview: "ok"},
		{Title: "amux.task_submit", Status: "completed", OutputPreview: "ok"},
		{Title: "amux.task_submit", Status: "running"},
		{Title: "amux.task_submit", Status: "failed", OutputPreview: "denied"},
	}, time.Unix(100, 0))
	if !strings.Contains(completed, "✓ amux.task_submit ×2") {
		t.Fatalf("completed = %q", completed)
	}
	if !strings.Contains(active, "● amux.task_submit") || !strings.Contains(active, "✕ amux.task_submit") || strings.Contains(active, "denied") {
		t.Fatalf("active = %q", active)
	}
}

func TestRenderToolRowsOmitsConflictingGroupedPreview(t *testing.T) {
	_, completed := renderToolRows([]agentui.ToolEvent{
		{Title: "task_stream", Status: "completed", OutputPreview: "first"},
		{Title: "task_stream", Status: "completed", OutputPreview: "second"},
	}, time.Unix(100, 0))
	if completed != "✓ task_stream ×2" {
		t.Fatalf("completed = %q", completed)
	}
}

func TestRenderToolRowsKeepsOutputPreviewOutOfCard(t *testing.T) {
	_, completed := renderToolRows([]agentui.ToolEvent{{
		Title: "读取 go.mod", Status: "completed",
		OutputPreview: "module github.com/amuxos/amux go 1.25.0 require ...",
	}}, time.Unix(100, 0))
	if completed != "✓ 读取 go.mod" {
		t.Fatalf("completed = %q, want title only", completed)
	}
}

func TestTurnActivityTitleShowsSilenceWithoutClaimingStall(t *testing.T) {
	started := time.Unix(100, 0)
	view := &agentui.ActivityView{Status: "running", StartedAt: started, LastActivityAt: started}

	recent := toolActivityCardTitle(toolActivityCardMeta{Status: "running", Now: time.Unix(112, 0), Activity: view})
	if recent != "正在处理 · 最近更新于 12 秒前" {
		t.Fatalf("recent silence title = %q", recent)
	}
	long := toolActivityCardTitle(toolActivityCardMeta{Status: "running", Now: time.Unix(165, 0), Activity: view})
	if long != "仍在处理 · 1 分 5 秒未收到新进展" {
		t.Fatalf("long silence title = %q", long)
	}
}

func TestRenderTurnActivityBoundsLargeDetails(t *testing.T) {
	plan := make([]agentui.PlanEntry, maxPlanEntries)
	for i := range plan {
		plan[i] = agentui.PlanEntry{Content: strings.Repeat("很长的计划", 100), Status: "in_progress"}
	}
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		ViewURL:  "https://viewer.example/session",
		Activity: &agentui.ActivityView{Status: "running", Plan: plan},
	})
	if len(activity.JSON) > maxTurnCardActivityBytes {
		t.Fatalf("activity JSON = %d bytes, want bounded", len(activity.JSON))
	}
	if !strings.Contains(string(activity.JSON), "计划进度") || len(activity.Trailing) != 1 {
		t.Fatalf("bounded activity lost compact summary or viewer: %s %+v", activity.JSON, activity.Trailing)
	}
}

func TestRenderTurnActivityCompactFallbackPreservesProgressAndLatestStep(t *testing.T) {
	plan := make([]agentui.PlanEntry, maxPlanEntries)
	for i := range plan {
		plan[i] = agentui.PlanEntry{Content: strings.Repeat("很长的计划", 100), Status: "in_progress"}
	}
	plan[0].Status = "completed"
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:       "running",
		AgentProfile: "team/reviewer",
		ViewURL:      "https://viewer.example/session",
		Activity: &agentui.ActivityView{
			Status:      "running",
			CurrentStep: "正在检查下游配置",
			Plan:        plan,
			Tools: []agentui.ToolEvent{
				{Title: "query_config", Status: "completed"},
				{Title: "submit_change", Status: "running"},
			},
		},
	})
	got := string(activity.JSON)
	if len(activity.JSON) > maxTurnCardActivityBytes {
		t.Fatalf("activity JSON = %d bytes, want bounded", len(activity.JSON))
	}
	// Compact fallback should show panel header with status and agent identity.
	if !strings.Contains(got, "正在处理") || !strings.Contains(got, "team/reviewer") {
		t.Fatalf("compact fallback lost status or agent identity: %s", got)
	}
	// Compact fallback should show a count-based summary, not a bare sentence.
	if !strings.Contains(got, "计划进度 1/50") || !strings.Contains(got, "工具调用 2 次") {
		t.Fatalf("compact fallback lost progress summary: %s", got)
	}
	// Compact fallback should keep the latest processing step visible.
	if !strings.Contains(got, "正在检查下游配置") {
		t.Fatalf("compact fallback lost latest processing step: %s", got)
	}
	// Viewer button should still be present.
	if len(activity.Trailing) != 1 || activity.Trailing[0].ElementID != "viewer_action" {
		t.Fatalf("compact fallback lost viewer: %+v", activity.Trailing)
	}
	// Should NOT fall all the way through to the bare single-sentence fallback.
	if strings.Contains(got, "执行详情过长") {
		t.Fatalf("compact fallback should not show bare truncation message: %s", got)
	}
}

func TestMarshalActivityCardPreservesNestedViewerInOversizeFallback(t *testing.T) {
	doc := newCardDoc(wrapTurnCardElements([]cardElement{
		{Tag: "markdown", Content: strings.Repeat("很长的执行详情", maxFeishuCardBytes)},
		{
			Tag:       "action",
			ElementID: "viewer_action",
			Actions: []cardAction{{
				Tag:  "button",
				Text: &cardTitle{Tag: "plain_text", Content: "查看完整输出"},
				URL:  "https://viewer.example/session",
			}},
		},
	}))

	card := decodeActivityCard(t, marshalActivityCard(doc, toolActivityCardMeta{
		Status:  "running",
		ViewURL: "https://viewer.example/session",
	}))
	if len(card.Body.Elements) != 1 {
		t.Fatalf("fallback body = %+v, want one shared turn-card container", card.Body.Elements)
	}
	children := turnCardChildren(t, card.Body.Elements[0])
	if len(children) != 2 || children[1].ElementID != "viewer_action" || !strings.Contains(activityElementText(children[0]), "执行详情过长") {
		t.Fatalf("fallback children = %+v, want fallback text plus nested viewer", children)
	}
}

func TestRenderToolActivityCardShowsWaitingStatus(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq:  4,
		Status:   "waiting",
		Question: "review 飞书消息展示",
	}, "工具调用：2 次")

	got := decodeActivityCard(t, content)
	if len(got.Body.Elements) != 1 {
		t.Fatalf("activity container = %+v, want one element", got.Body.Elements)
	}
	panel, _ := turnActivityLayout(t, got.Body.Elements[0])
	if panel.Header == nil || panel.Header.Title.Content != "等待你回复" {
		t.Fatalf("activity panel = %+v, want waiting title", got.Body.Elements)
	}
}

func TestRenderToolActivityCardIncludesFollowup(t *testing.T) {
	content := renderToolActivityCard(toolActivityCardMeta{
		TurnSeq:  3,
		Status:   "running",
		Question: "review 飞书消息展示",
		Followup: "保留 post，只把状态做 card",
	}, "工具调用：2 次")

	got := decodeActivityCard(t, content)
	want := "Q: review 飞书消息展示\n补充：保留 post，只把状态做 card\n\n工具调用：2 次"
	panel, _ := turnActivityLayout(t, got.Body.Elements[0])
	if len(panel.Elements) != 1 || panel.Elements[0].Content != want {
		t.Fatalf("panel elements = %#v, want content %q", panel.Elements, want)
	}
}

func TestToolActivityQuestionTitleCleansAndTruncatesInputFrames(t *testing.T) {
	got := toolActivityQuestionTitle("\r\n📱 来自飞书: 请分析当前仓库，并给出一个非常非常非常非常非常非常非常非常非常长的问题标题\r\n")
	want := "请分析当前仓库，并给出一个非常非常非常非常非常非常非常非常非常长的问题标题"
	if got != want {
		t.Fatalf("toolActivityQuestionTitle() = %q, want %q", got, want)
	}
}

func TestToolActivityQuestionTitleBoundsOnlyVeryLongInput(t *testing.T) {
	got := toolActivityQuestionTitle(strings.Repeat("问", 250))
	want := strings.Repeat("问", 240) + "..."
	if got != want {
		t.Fatalf("toolActivityQuestionTitle() length = %d, want %d", len([]rune(got)), len([]rune(want)))
	}
}

func TestSharedThreadSubmittedInputReachesQuestionTitleAsOnePrompt(t *testing.T) {
	input := "[Amux Feishu Thread Mode]\n" +
		"You are replying in a Feishu thread connected to an amux agent session.\n\n" +
		"[Feishu thread context]\n历史讨论内容\n\n" +
		"[User request]\n看看 GIP 报警群最新的两条消息\r\n"

	events := agentui.FromAgentEvent(agentstream.Event{
		Turn:   &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		Origin: agentstream.Origin{Actor: agentstream.ActorUser, Channel: "lark"},
		Body:   agentstream.TurnStarted{InputSummary: input},
	})
	if len(events) != 1 {
		t.Fatalf("mapped events = %d, want 1", len(events))
	}
	actions := agentui.NewPresenter().Consume(events[0])
	if len(actions) != 1 || actions[0].Kind != agentui.ActionTurnStarted {
		t.Fatalf("presentation actions = %+v, want one turn_started", actions)
	}
	got := toolActivityQuestionTitle(actions[0].Text)
	want := "看看 GIP 报警群最新的两条消息"
	if got != want {
		t.Fatalf("toolActivityQuestionTitle() = %q, want %q", got, want)
	}
}

func assertPostText(t *testing.T, elem map[string]any, want string) {
	t.Helper()
	if elem["tag"] != "text" {
		t.Fatalf("tag = %#v, want text", elem["tag"])
	}
	if elem["text"] != want {
		t.Fatalf("text = %#v, want %q", elem["text"], want)
	}
}

func TestUsageSummaryShowsTokenCountsAndContextWindowRatio(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "native cumulative counts keep latest context ratio",
			raw:  `{"total_tokens":22800,"context_window":272000,"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000,"cumulative_total_tokens":355000}`,
			want: "22.8k tokens (8%) · 累计输入 300k/343k（缓存/总量） · 累计输出 12k",
		},
		{
			name: "cumulative only and explicit zero cache",
			raw:  `{"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":0,"cumulative_output_tokens":0}`,
			want: "累计输入 0/343k（缓存/总量） · 累计输出 0",
		},
		{
			name: "missing cumulative cache is not zero",
			raw:  `{"cumulative_input_tokens":343000}`,
			want: "累计输入 343k",
		},
		{
			name: "cumulative output without input",
			raw:  `{"cumulative_output_tokens":12000,"cumulative_cached_input_tokens":300000}`,
			want: "累计输出 12k",
		},
		{
			name: "codex usage with context window",
			raw:  `{"input_tokens":20000,"output_tokens":2800,"total_tokens":22800,"cached_input_tokens":15000,"cache_creation_input_tokens":500,"reasoning_output_tokens":800,"context_window":272000}`,
			want: "22.8k tokens (8%)",
		},
		{
			name: "input and output without reported total",
			raw:  `{"input_tokens":100,"output_tokens":20}`,
			want: "120 tokens",
		},
		{
			name: "missing output is omitted",
			raw:  `{"input_tokens":100}`,
			want: "100 tokens",
		},
		{
			name: "missing input is omitted",
			raw:  `{"output_tokens":20}`,
			want: "20 tokens",
		},
		{
			name: "reported zero is retained",
			raw:  `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`,
			want: "0 tokens",
		},
		{
			name: "usage without context window keeps plain total",
			raw:  `{"total_tokens":22800}`,
			want: "22.8k tokens",
		},
		{
			name: "legacy used/size form keeps its own ratio format",
			raw:  `{"used":14000,"size":100000}`,
			want: "14k / 100k tokens",
		},
		{
			name: "zero window is ignored",
			raw:  `{"total_tokens":500,"context_window":0}`,
			want: "500 tokens",
		},
	}
	for _, c := range cases {
		if got := usageSummary(json.RawMessage(c.raw)); got != c.want {
			t.Fatalf("%s: usageSummary(%s) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestToolActivityCardTitleShowsContextUsageRatio(t *testing.T) {
	title := toolActivityCardTitle(toolActivityCardMeta{
		Status: "done",
		Activity: &agentui.ActivityView{
			Status:    "done",
			StartedAt: time.Unix(100, 0),
			UsageJSON: json.RawMessage(`{"total_tokens":48200,"context_window":200000}`),
		},
	})
	if !strings.Contains(title, "48.2k tokens (24%)") {
		t.Fatalf("title = %q, want context usage ratio", title)
	}
}

func TestUsageSummaryShowsSpeedMetrics(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "codex usage with ttft and speed",
			raw:  `{"input_tokens":20000,"output_tokens":2800,"total_tokens":22800,"context_window":272000,"ttft_ms":1230,"tokens_per_sec":45}`,
			want: "22.8k tokens (8%) · 首 token 1.2s · 45 tok/s",
		},
		{
			name: "only ttft",
			raw:  `{"total_tokens":100,"ttft_ms":800}`,
			want: "100 tokens · 首 token 800ms",
		},
		{
			name: "zero and missing speed fields are omitted",
			raw:  `{"total_tokens":100,"ttft_ms":0}`,
			want: "100 tokens",
		},
		{
			name: "legacy usage without speed fields unchanged",
			raw:  `{"total_tokens":22800}`,
			want: "22.8k tokens",
		},
	}
	for _, c := range cases {
		if got := usageSummary(json.RawMessage(c.raw)); got != c.want {
			t.Fatalf("%s: usageSummary(%s) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestCompactNumberAdaptsMagnitudeUnit(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_000, "1k"},
		{22_800, "22.8k"},
		{343_000, "343k"},
		{343_100, "343.1k"},
		{999_000, "999k"},
		{999_900, "999.9k"},
		// The largest value below the M step stays in k rather than rounding up,
		// so a cached/total pair never renders in an order that contradicts it.
		{999_949, "999.9k"},
		{999_999, "999.9k"},
		{1_000_000, "1M"},
		{1_008_400, "1M"},
		{10_500_000, "10.5M"},
		{128_400_000, "128.4M"},
		{999_999_999, "999.9M"},
		{1_000_000_000, "1B"},
		{1_100_000_000, "1.1B"},
		{12_345_678_901, "12.3B"},
	}
	for _, c := range cases {
		if got := compactNumber(c.in); got != c.want {
			t.Fatalf("compactNumber(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUsageSummaryAdaptsCumulativeMagnitudeUnit(t *testing.T) {
	raw := json.RawMessage(`{"cumulative_input_tokens":128400000,"cumulative_cached_input_tokens":10500000,"cumulative_output_tokens":12345678901}`)
	want := "累计输入 10.5M/128.4M（缓存/总量） · 累计输出 12.3B"
	if got := usageSummary(raw); got != want {
		t.Fatalf("usageSummary(%s) = %q, want %q", raw, got, want)
	}
}

func TestCompactDurationText(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{800, "800ms"},
		{1_230, "1.2s"},
		{75_400, "1m15s"},
	}
	for _, c := range cases {
		if got := compactDurationText(c.ms); got != c.want {
			t.Fatalf("compactDurationText(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}
