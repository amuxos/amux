package acptui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLineEditorPromptsImmediatelyAndSendsOnlyOnEnter(t *testing.T) {
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		sent = append(sent, append([]byte(nil), data...))
	})

	echo := editor.Write([]byte{'a', 'b', 'c', 0x7f, 'd'})
	if got, want := string(echo), "> abc\b \bd"; got != want {
		t.Fatalf("Write(edit) echo = %q, want %q", got, want)
	}
	if len(sent) != 0 {
		t.Fatalf("sent before enter = %q, want none", sent)
	}

	echo = editor.Write([]byte{'\r'})
	if got, want := string(echo), "\r\n"; got != want {
		t.Fatalf("Write(enter) echo = %q, want %q", got, want)
	}
	if len(sent) != 1 || string(sent[0]) != "abd\r" {
		t.Fatalf("sent = %q, want [abd\\r]", sent)
	}

	echo = editor.Write([]byte{'x'})
	if got, want := string(echo), "> x"; got != want {
		t.Fatalf("Write(next input) echo = %q, want %q", got, want)
	}
}

func TestLineEditorDoesNotSendEmptyLines(t *testing.T) {
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		sent = append(sent, append([]byte(nil), data...))
	})

	echo := editor.Write([]byte{'\r'})
	if got, want := string(echo), "\r\n"; got != want {
		t.Fatalf("Write(enter) echo = %q, want %q", got, want)
	}
	if len(sent) != 0 {
		t.Fatalf("sent = %q, want none", sent)
	}
}

func TestIsDetachCommand(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "detach CR", data: []byte("/detach\r"), want: true},
		{name: "detach LF", data: []byte("/detach\n"), want: true},
		{name: "not detach", data: []byte("/interrupt\r"), want: false},
		{name: "message", data: []byte("please /detach later\r"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDetachCommand(tt.data); got != tt.want {
				t.Fatalf("isDetachCommand(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestIsInterruptCommand(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "interrupt CR", data: []byte("/interrupt\r"), want: true},
		{name: "interrupt LF", data: []byte("/interrupt\n"), want: true},
		{name: "not interrupt", data: []byte("/detach\r"), want: false},
		{name: "message", data: []byte("please /interrupt later\r"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInterruptCommand(tt.data); got != tt.want {
				t.Fatalf("isInterruptCommand(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

type interruptSender struct {
	sent         [][]byte
	interrupts   int
	interruptErr error
	queueItems   []QueueItem
	queueCleared int
	queueCancel  []string
	queueErr     error
}

type cwdSender struct {
	interruptSender
	cwd       string
	changed   string
	cwdOpts   WorkingDirectoryOptions
	changeErr error
}

func (s *cwdSender) WorkingDirectory(context.Context) (string, error) {
	return s.cwd, nil
}

func (s *cwdSender) ChangeWorkingDirectory(_ context.Context, path string, opts WorkingDirectoryOptions) (string, error) {
	s.changed = path
	s.cwdOpts = opts
	if s.changeErr != nil {
		return "", s.changeErr
	}
	s.cwd = path
	return path, nil
}

func (s *interruptSender) Send(_ context.Context, data []byte) error {
	s.sent = append(s.sent, append([]byte(nil), data...))
	return nil
}

func (s *interruptSender) Interrupt(context.Context) error {
	s.interrupts++
	return s.interruptErr
}

func (s *interruptSender) QueueList(context.Context) ([]QueueItem, error) {
	return s.queueItems, s.queueErr
}

func (s *interruptSender) QueueClear(context.Context) (int, error) {
	if s.queueErr != nil {
		return 0, s.queueErr
	}
	s.queueCleared++
	n := len(s.queueItems)
	s.queueItems = nil
	return n, nil
}

func (s *interruptSender) QueueCancel(_ context.Context, id string) error {
	if s.queueErr != nil {
		return s.queueErr
	}
	s.queueCancel = append(s.queueCancel, id)
	return nil
}

func TestHandleEditorCommandInterruptsWithoutSendingInput(t *testing.T) {
	sender := &interruptSender{}
	cancelled := false

	if handled := handleEditorCommand(context.Background(), []byte("/interrupt\r"), sender, func() { cancelled = true }, nil); !handled {
		t.Fatal("handleEditorCommand(/interrupt) = false, want true")
	}
	if sender.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", sender.interrupts)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %q, want none", sender.sent)
	}
	if cancelled {
		t.Fatal("interrupt command cancelled attach, want attached")
	}
}

func TestHandleEditorCommandReportsInterruptResult(t *testing.T) {
	sender := &interruptSender{}
	var notices []string

	handleEditorCommand(context.Background(), []byte("/interrupt\r"), sender, func() {}, func(text string) {
		notices = append(notices, text)
	})

	if len(notices) != 1 || notices[0] != "interrupt requested" {
		t.Fatalf("notices = %q, want interrupt requested", notices)
	}
}

func TestHandleEditorCommandReportsInterruptFailure(t *testing.T) {
	sender := &interruptSender{interruptErr: errors.New("unsupported")}
	var notices []string

	handleEditorCommand(context.Background(), []byte("/interrupt\r"), sender, func() {}, func(text string) {
		notices = append(notices, text)
	})

	if len(notices) != 1 || notices[0] != "interrupt failed: unsupported" {
		t.Fatalf("notices = %q, want failure notice", notices)
	}
}

func TestHandleEditorCommandDetachCancels(t *testing.T) {
	sender := &interruptSender{}
	cancelled := false

	if handled := handleEditorCommand(context.Background(), []byte("/detach\r"), sender, func() { cancelled = true }, nil); !handled {
		t.Fatal("handleEditorCommand(/detach) = false, want true")
	}
	if !cancelled {
		t.Fatal("detach did not cancel")
	}
	if sender.interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", sender.interrupts)
	}
}

func TestHandleEditorCommandShowsQueue(t *testing.T) {
	sender := &interruptSender{queueItems: []QueueItem{{ID: "1", Source: "lark", Text: "pending text"}}}
	var notices []string

	if handled := handleEditorCommand(context.Background(), []byte("/queue\r"), sender, func() {}, func(text string) {
		notices = append(notices, text)
	}); !handled {
		t.Fatal("handleEditorCommand(/queue) = false, want true")
	}

	if len(notices) != 1 || !strings.Contains(notices[0], "pending text") {
		t.Fatalf("notices = %q, want queue contents", notices)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %q, want none", sender.sent)
	}
}

func TestHandleEditorCommandQueueClear(t *testing.T) {
	sender := &interruptSender{queueItems: []QueueItem{{ID: "1", Text: "pending"}}}
	var notices []string

	handleEditorCommand(context.Background(), []byte("/queue clear\r"), sender, func() {}, func(text string) {
		notices = append(notices, text)
	})

	if sender.queueCleared != 1 {
		t.Fatalf("queueCleared = %d, want 1", sender.queueCleared)
	}
	if len(notices) != 1 || notices[0] != "queue cleared: 1" {
		t.Fatalf("notices = %q, want queue cleared", notices)
	}
}

func TestHandleEditorCommandQueueCancel(t *testing.T) {
	sender := &interruptSender{}
	var notices []string

	handleEditorCommand(context.Background(), []byte("/queue cancel 7\r"), sender, func() {}, func(text string) {
		notices = append(notices, text)
	})

	if len(sender.queueCancel) != 1 || sender.queueCancel[0] != "7" {
		t.Fatalf("queueCancel = %q, want [7]", sender.queueCancel)
	}
	if len(notices) != 1 || notices[0] != "queue cancelled: 7" {
		t.Fatalf("notices = %q, want queue cancelled", notices)
	}
}

func TestHandleEditorCommandShowsAndChangesCwd(t *testing.T) {
	sender := &cwdSender{cwd: "/repo/a"}
	var notices []string
	notice := func(text string) { notices = append(notices, text) }

	if !handleEditorCommand(context.Background(), []byte("/cwd\r"), sender, func() {}, notice) {
		t.Fatal("/cwd was not handled")
	}
	if len(notices) != 1 || notices[0] != "cwd: /repo/a" {
		t.Fatalf("query notices = %q", notices)
	}
	notices = nil
	if !handleEditorCommand(context.Background(), []byte("/cwd --interrupt ../b\r"), sender, func() {}, notice) {
		t.Fatal("/cwd switch was not handled")
	}
	if sender.changed != "../b" || !sender.cwdOpts.Interrupt || !sender.cwdOpts.Wait {
		t.Fatalf("change path=%q opts=%+v", sender.changed, sender.cwdOpts)
	}
	if len(notices) != 2 || !strings.Contains(notices[1], "conversation preserved") {
		t.Fatalf("switch notices = %q", notices)
	}
}

func TestHandleInputProcessesInterruptWhenBlocked(t *testing.T) {
	sender := &interruptSender{}
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		handleSubmittedLine(context.Background(), data, true, sender, func() {}, nil, func(data []byte) {
			sent = append(sent, append([]byte(nil), data...))
		})
	})
	cancelled := false

	handled := handleInputBytes([]byte("/interrupt\r"), editor)

	if !handled {
		t.Fatal("handleInputBytes(/interrupt, blocked) = false, want true")
	}
	if sender.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", sender.interrupts)
	}
	if len(sent) != 0 {
		t.Fatalf("sent = %q, want none", sent)
	}
	if cancelled {
		t.Fatal("interrupt cancelled attach, want attached")
	}
}

func TestHandleInputProcessesInterruptTypedByteByByteWhenBlocked(t *testing.T) {
	sender := &interruptSender{}
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		handleSubmittedLine(context.Background(), data, true, sender, func() {}, nil, func(data []byte) {
			sent = append(sent, append([]byte(nil), data...))
		})
	})

	for _, b := range []byte("/interrupt\r") {
		handleInputBytes([]byte{b}, editor)
	}

	if sender.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", sender.interrupts)
	}
	if len(sent) != 0 {
		t.Fatalf("sent = %q, want none", sent)
	}
}

func TestHandleInputProcessesDetachWhenBlocked(t *testing.T) {
	sender := &interruptSender{}
	cancelled := false
	editor := NewLineEditor(func(data []byte) {
		handleSubmittedLine(context.Background(), data, true, sender, func() { cancelled = true }, nil, func([]byte) {})
	})

	handled := handleInputBytes([]byte("/detach\r"), editor)

	if !handled {
		t.Fatal("handleInputBytes(/detach, blocked) = false, want true")
	}
	if !cancelled {
		t.Fatal("detach did not cancel when blocked")
	}
	if sender.interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", sender.interrupts)
	}
}

func TestHandleInputProcessesDetachTypedByteByByteWhenBlocked(t *testing.T) {
	sender := &interruptSender{}
	cancelled := false
	editor := NewLineEditor(func(data []byte) {
		handleSubmittedLine(context.Background(), data, true, sender, func() { cancelled = true }, nil, func([]byte) {})
	})

	for _, b := range []byte("/detach\r") {
		handleInputBytes([]byte{b}, editor)
	}

	if !cancelled {
		t.Fatal("detach did not cancel when typed byte by byte while blocked")
	}
	if sender.interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", sender.interrupts)
	}
}

func TestHandleInputDropsNormalInputWhenBlocked(t *testing.T) {
	sender := &interruptSender{}
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		handleSubmittedLine(context.Background(), data, true, sender, func() {}, nil, func(data []byte) {
			sent = append(sent, append([]byte(nil), data...))
		})
	})

	handled := handleInputBytes([]byte("hello\r"), editor)

	if !handled {
		t.Fatal("handleInputBytes(normal, blocked) = false, want true")
	}
	if len(sent) != 0 {
		t.Fatalf("sent = %q, want none", sent)
	}
	if sender.interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", sender.interrupts)
	}
}

func TestLineEditorBackspaceDeletesOneWideRune(t *testing.T) {
	var sent [][]byte
	editor := NewLineEditor(func(data []byte) {
		sent = append(sent, append([]byte(nil), data...))
	})

	echo := editor.Write([]byte("你好"))
	if got, want := string(echo), "> 你好"; got != want {
		t.Fatalf("Write(chinese) echo = %q, want %q", got, want)
	}

	echo = editor.Write([]byte{0x7f})
	if got, want := string(echo), "\b \b\b \b"; got != want {
		t.Fatalf("Write(backspace) echo = %q, want %q", got, want)
	}

	_ = editor.Write([]byte{'\r'})
	if len(sent) != 1 || string(sent[0]) != "你\r" {
		t.Fatalf("sent = %q, want [你\\r]", sent)
	}
}

func TestDelayedTimerFiresAfterQuietPeriod(t *testing.T) {
	fired := make(chan struct{}, 1)
	p := NewDelayedTimer(20*time.Millisecond, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	defer p.Stop()

	p.Schedule()
	select {
	case <-fired:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for delayed timer")
	}
}

func TestPanelSnapshotShowsInputAndStatus(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "mycoco", AgentType: "coco"}, editor)
	panel.SetSize(60, 12)
	editor.Write([]byte("abc"))
	panel.SetStatus("waiting")

	got := string(panel.RenderPanel())
	if bytes.Contains([]byte(got), []byte("\033[H\033[2J")) {
		t.Fatalf("snapshot clears whole screen: %q", got)
	}
	if bytes.Contains([]byte(got), []byte("\033[?1049h")) || bytes.Contains([]byte(got), []byte("\033[?1049l")) {
		t.Fatalf("snapshot toggles alternate screen: %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("> abc")) {
		t.Fatalf("snapshot missing input: %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("waiting")) {
		t.Fatalf("snapshot missing status: %q", got)
	}
}

func TestPanelSnapshotRendersAttachTimelineBox(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{
		SessionName: "impl",
		AgentType:   "claude",
		TargetName:  "devbox",
	}, editor)
	panel.SetSize(68, 14)
	panel.SetStatus("running")
	panel.AddTimelineEvent(TimelineEvent{Time: "10:21", Source: "lark", Text: "修复登录页 bug"})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:21", Source: "agent", Text: "我先查看项目结构..."})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:22", Source: "tool", Text: "read_file src/pages/Login.tsx"})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:24", Source: "lark", Pending: true, PendingIndex: 1, Text: "顺便改按钮颜色"})

	got := string(panel.RenderPanel())
	for _, want := range []string{
		"┌─ amux attach: claude @ devbox [impl]",
		"state: running | queue: 0 | source: lark+cli",
		"[10:21] 📱 lark: 修复登录页 bug",
		"[10:21] 🤖 agent: 我先查看项目结构...",
		"[10:22] 🔧 tool: read_file src/pages/Login.tsx",
		"[10:24] 📱 lark pending #1: 顺便改按钮颜色",
		"> 输入消息，/detach，/interrupt，/queue，/cwd",
		"└",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("panel missing %q:\n%s", want, got)
		}
	}
}

func TestTimelineEventsFromOutputClassifiesSourcesAndTools(t *testing.T) {
	lark := timelineEventsFromOutput(OutputEvent{Source: "lark", Input: true}, []byte("修复登录页 bug\r\n"))
	if len(lark) != 1 || lark[0].Source != "lark" || lark[0].Text != "修复登录页 bug" {
		t.Fatalf("lark events = %#v", lark)
	}

	agent := timelineEventsFromOutput(OutputEvent{Source: "agent"}, []byte("我先查看项目结构...\r\n"))
	if len(agent) != 1 || agent[0].Source != "agent" || agent[0].Text != "我先查看项目结构...\n" {
		t.Fatalf("agent events = %#v", agent)
	}

	tool := timelineEventsFromOutput(OutputEvent{Source: "agent"}, []byte("\r\n🔧 工具开始: read_file src/pages/Login.tsx\r\n"))
	if len(tool) != 1 || tool[0].Source != "tool" || tool[0].Text != "read_file src/pages/Login.tsx" {
		t.Fatalf("tool events = %#v", tool)
	}

}

func TestTimelineEventsFromOutputKeepsAgentChunkTogether(t *testing.T) {
	input := []byte("建议结构：\n- 标题行\n- 状态行\n- 时间线\n")

	got := timelineEventsFromOutput(OutputEvent{Source: "agent"}, input)
	if len(got) != 1 {
		t.Fatalf("events len = %d, want 1: %#v", len(got), got)
	}
	want := "建议结构：\n- 标题行\n- 状态行\n- 时间线\n"
	if got[0].Text != want {
		t.Fatalf("event text = %q, want %q", got[0].Text, want)
	}
}

func TestPanelPreservesStreamingAgentMarkdownNewlines(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(100, 18)

	chunks := []string{
		"##", " Go", "语言", "示例", "\n\n",
		"|", " 功能", " |", " 函数名", " |", " 说明", " |", "\n",
		"|", "------", "|", "--------", "|", "------", "|", "\n",
		"|", " 字符串拼接", " |", " `strings.Join`", " |", " 将字符串切片连接为一个字符串", " |", "\n\n",
		"```", "go", "\n", "package", " main", "\n", "```",
	}
	for _, chunk := range chunks {
		for _, ev := range timelineEventsFromOutput(OutputEvent{Source: "agent"}, []byte(chunk)) {
			panel.AddTimelineEvent(ev)
		}
	}

	got := string(panel.RenderPanel())
	for _, bad := range []string{
		"示例|功能",
		"|------|--------|------| |字符串",
		"|gopackage",
	} {
		if strings.Contains(got, bad) {
			t.Fatalf("streaming markdown newline boundary was lost around %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"## Go语言示例",
		"功能",
		"函数名",
		"字符串拼接",
		"package main",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("streaming markdown output missing %q:\n%s", want, got)
		}
	}
}

func TestPanelMergesAgentChunksWithoutDroppingSpaces(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(80, 12)

	panel.AddTimelineEvent(TimelineEvent{Time: "10:48", Source: "agent", Text: "I'll start "})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:48", Source: "agent", Text: "by exploring"})

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "I'll start by exploring") {
		t.Fatalf("merged agent chunks should preserve spaces:\n%s", got)
	}
}

func TestPanelMergesAgentChunksAfterMultilineChunk(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(100, 12)

	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "agent", Text: "## 当前代码库分析\n### 项目概览\n"})
	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "agent", Text: "| 项目 | 描述 |\n"})
	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "agent", Text: "|---|---|\n"})

	got := string(panel.RenderPanel())
	if strings.Count(got, "🤖 agent") != 1 {
		t.Fatalf("multiline agent chunks should stay in one timeline event:\n%s", got)
	}
	if strings.Contains(got, "🤖 agent: ## 当前代码库分析") {
		t.Fatalf("multiline agent content should start below the agent header:\n%s", got)
	}
	for _, want := range []string{"## 当前代码库分析", "### 项目概览", "项目", "描述"} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged multiline agent output missing %q:\n%s", want, got)
		}
	}
}

func TestPanelFormatsLongMarkdownAgentContentBelowHeader(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(100, 16)

	panel.AddTimelineEvent(TimelineEvent{Time: "12:43", Source: "agent", Text: "## 代码库分析：amux\n\n### 项目概述\n\n**amux** 是一个跨沙箱 Agent 管理器。\n\n| 二进制 | 入口 | 职责 |\n|---|---|---|\n| amux | cmd/amux/main.go | 用户 CLI |\n"})

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "[12:43] 🤖 agent") {
		t.Fatalf("agent header missing:\n%s", got)
	}
	if strings.Contains(got, "[12:43] 🤖 agent: ##") {
		t.Fatalf("long markdown should not be rendered on the agent header line:\n%s", got)
	}
	for _, want := range []string{"## 代码库分析：amux", "### 项目概述", "amux 是一个跨沙箱 Agent 管理器。", "二进制", "入口", "职责"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted markdown missing %q:\n%s", want, got)
		}
	}
}

func TestPanelReformatsAgentHeaderWhenMarkdownArrivesAfterPlainChunk(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(100, 16)

	panel.AddTimelineEvent(TimelineEvent{Time: "12:56", Source: "agent", Text: "Now I have a complete picture of the codebase. "})
	panel.AddTimelineEvent(TimelineEvent{Time: "12:56", Source: "agent", Text: "Here is the report.\n\n---\n# Amux - Full Architecture Analysis\n"})

	got := string(panel.RenderPanel())
	if strings.Contains(got, "report.---#") || strings.Contains(got, "agent: Now I have") {
		t.Fatalf("agent markdown continuation should re-render as a block:\n%s", got)
	}
	for _, want := range []string{"[12:56] 🤖 agent", "Now I have a complete picture", "Here is the report.", "---", "# Amux - Full Architecture Analysis"} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent block missing %q:\n%s", want, got)
		}
	}
}

func TestPanelRepairsJoinedMarkdownBoundariesBeforeRendering(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(120, 18)

	panel.AddTimelineEvent(TimelineEvent{Time: "13:29", Source: "agent", Text: "以下是总结：  ##总览分支相对 main 改动。 ---## 一、Bridge 层重构 |提交 | 改动 ||---|---|| a37b565 | Refactor attach metadata || 9cd6643 | Split bridge routing | ## 二、消息展示优化"})

	got := string(panel.RenderPanel())
	for _, bad := range []string{"---##", "|提交 | 改动 ||---|---||", "metadata || 9cd6643"} {
		if strings.Contains(got, bad) {
			t.Fatalf("joined markdown boundary %q should be repaired before rendering:\n%s", bad, got)
		}
	}
	for _, want := range []string{"## 总览", "## 一、Bridge 层重构", "提交", "改动", "a37b565", "9cd6643", "## 二、消息展示优化"} {
		if !strings.Contains(got, want) {
			t.Fatalf("repaired markdown missing %q:\n%s", want, got)
		}
	}
}

func TestPanelSummarizesConsecutiveToolEvents(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(100, 12)

	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "tool", Text: "bash"})
	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "tool", Text: "bash"})
	panel.AddTimelineEvent(TimelineEvent{Time: "12:03", Source: "tool", Text: "Read /Users/developer/code/amux/pkg/acptui/panel.go"})

	got := string(panel.RenderPanel())
	if strings.Count(got, "🔧 tool:") != 1 {
		t.Fatalf("consecutive tool events should render as one summary row:\n%s", got)
	}
	for _, want := range []string{"tools: 3 calls", "bash x2", "Read x1", "last: Read panel.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool summary missing %q:\n%s", want, got)
		}
	}
}

// TestPanelRendersStructuredToolEvents asserts the v0.1.32 structured tool
// event path: a *ToolEvent is formatted into the compact "<name> <title>
// <status>" form and aggregated by addToolEventLocked into the tool digest,
// without relying on "🔧 工具开始:" text-prefix matching.
func TestPanelRendersStructuredToolEvents(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "codex"}, editor)
	panel.SetSize(100, 12)

	panel.AddTimelineEvent(TimelineEvent{
		Time:   "12:10",
		Source: "tool",
		Text:   formatStructuredToolEvent(&ToolEvent{Name: "command", Title: "ls -la", Status: "running"}),
	})
	panel.AddTimelineEvent(TimelineEvent{
		Time:   "12:10",
		Source: "tool",
		Text:   formatStructuredToolEvent(&ToolEvent{Name: "command", Title: "ls -la", Status: "completed"}),
	})
	panel.AddTimelineEvent(TimelineEvent{
		Time:   "12:11",
		Source: "tool",
		Text:   formatStructuredToolEvent(&ToolEvent{Name: "file_change", Title: "src/a.go", Status: "completed"}),
	})

	got := string(panel.RenderPanel())
	if strings.Count(got, "🔧 tool:") != 1 {
		t.Fatalf("structured tool events should render as one summary row:\n%s", got)
	}
	for _, want := range []string{"tools: 3 calls", "command x2", "file_change x1", "last: file_change a.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("structured tool summary missing %q:\n%s", want, got)
		}
	}
}

func TestFormatStructuredToolEventShortensPathAndStatus(t *testing.T) {
	cases := []struct {
		te   *ToolEvent
		want string
	}{
		{&ToolEvent{Name: "command", Title: "ls -la", Status: "running"}, "command ls -la running"},
		{&ToolEvent{Name: "file_change", Title: "src/a.go", Status: "completed"}, "file_change a.go done"},
		{&ToolEvent{Name: "bash", Title: "", Status: "failed"}, "bash bash failed"},
	}
	for _, c := range cases {
		if got := formatStructuredToolEvent(c.te); got != c.want {
			t.Errorf("formatStructuredToolEvent(%+v) = %q, want %q", c.te, got, c.want)
		}
	}
}

func TestPanelMergesConsecutiveAgentChunks(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(80, 12)

	panel.AddTimelineEvent(TimelineEvent{Time: "10:47", Source: "agent", Text: "你好"})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:47", Source: "agent", Text: "!"})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:47", Source: "agent", Text: "有什么可以"})
	panel.AddTimelineEvent(TimelineEvent{Time: "10:47", Source: "agent", Text: "帮你的吗?"})

	got := string(panel.RenderPanel())
	if strings.Count(got, "🤖 agent:") != 1 {
		t.Fatalf("agent chunks should merge into one row:\n%s", got)
	}
	if !strings.Contains(got, "🤖 agent: 你好!有什么可以帮你的吗?") {
		t.Fatalf("merged agent row missing:\n%s", got)
	}
}

func TestClearPreviousPanelStartsFromLineBelowPrompt(t *testing.T) {
	got := string(clearPreviousPanel(3))
	if !strings.HasPrefix(got, "\033[1B\r\033[2K") {
		t.Fatalf("clearPreviousPanel() = %q, should first move to bottom border before clearing", got)
	}
}

func TestPanelPlaceholderIsDimAndCursorStaysAtPrompt(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "impl", AgentType: "coco"}, editor)
	panel.SetSize(80, 12)

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "\033[2m> 输入消息，/detach，/interrupt，/queue，/cwd\033[0m") {
		t.Fatalf("placeholder should be dimmed:\n%s", got)
	}
	if !strings.HasSuffix(got, "\r\033[A\033[3C") {
		t.Fatalf("cursor should stay after prompt prefix, got suffix %q", got[len(got)-20:])
	}
}

func TestNormalizeLineModeOutputConvertsLFToCRLF(t *testing.T) {
	input := []byte("one\ntwo\r\nthree")
	got := NormalizeLineModeOutput(input)
	want := []byte("one\r\ntwo\r\nthree")
	if !bytes.Equal(got, want) {
		t.Fatalf("NormalizeLineModeOutput() = %q, want %q", got, want)
	}
}

func TestFilterTerminalResponsesStripsOSCAndCPR(t *testing.T) {
	input := []byte("hi" +
		"\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\" +
		"\x1b]10;rgb:cdcd/d6d6/f4f4\x07" +
		"\x1b[24;80R" +
		"\r")

	got := FilterTerminalResponses(input)
	want := []byte("hi\r")
	if string(got) != string(want) {
		t.Fatalf("FilterTerminalResponses() = %q, want %q", got, want)
	}
}

func TestFilterTerminalResponsesKeepsNormalInputAndDetach(t *testing.T) {
	input := []byte{'h', 'i', 0x1d}

	got := FilterTerminalResponses(input)
	if string(got) != string(input) {
		t.Fatalf("FilterTerminalResponses() = %q, want %q", got, input)
	}
}

func TestIsDetachRaw(t *testing.T) {
	if !IsDetach([]byte{0x1d}) {
		t.Fatal("IsDetach(0x1d) = false, want true")
	}
	if IsDetach([]byte("hello")) {
		t.Fatal("IsDetach(hello) = true, want false")
	}
}

func TestIsDetachKitty(t *testing.T) {
	kitty := []byte{0x1b, 0x5b, 0x39, 0x33, 0x3b, 0x35, 0x75}
	if !IsDetach(kitty) {
		t.Fatal("IsDetach(kitty Ctrl-]) = false, want true")
	}
}

func TestPanelSnapshotShowsContextUsageWithWindow(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "work", AgentType: "codex"}, editor)
	panel.SetSize(80, 14)
	panel.SetUsage(&UsageStats{
		InputTokens:           45000,
		OutputTokens:          3000,
		TotalTokens:           48000,
		CachedInputTokens:     10000,
		ReasoningOutputTokens: 500,
		ContextWindow:         200000,
	})

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "state: idle | queue: 0 | source: cli | ctx: 48k/200k (24%)") {
		t.Fatalf("state line missing ctx usage:\n%s", got)
	}
}

func TestPanelSnapshotShowsContextUsageWithoutWindow(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "work", AgentType: "codex"}, editor)
	panel.SetSize(80, 14)
	panel.SetUsage(&UsageStats{TotalTokens: 12000})

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "ctx: 12k") {
		t.Fatalf("state line missing token-only ctx usage:\n%s", got)
	}
	if strings.Contains(got, "ctx: 12k/") {
		t.Fatalf("state line should not divide by missing window:\n%s", got)
	}
}

func TestPanelSnapshotOmitsContextWithoutUsage(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "work", AgentType: "coco"}, editor)
	panel.SetSize(80, 14)

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "state: idle | queue: 0 | source: cli") {
		t.Fatalf("state line changed unexpectedly:\n%s", got)
	}
	if strings.Contains(got, "ctx:") {
		t.Fatalf("state line should omit ctx without usage:\n%s", got)
	}
}

func TestUsageTimelineTextIncludesTokenBreakdown(t *testing.T) {
	u := &UsageStats{
		InputTokens:              100,
		OutputTokens:             50,
		TotalTokens:              150,
		CachedInputTokens:        80,
		CacheCreationInputTokens: 10,
		ReasoningOutputTokens:    20,
		ContextWindow:            1000,
	}
	got := usageTimelineText(u)
	for _, want := range []string{
		"in 100",
		"out 50",
		"cached 80",
		"cache-write 10",
		"reasoning 20",
		"ctx: 150/1k (15%)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("timeline text %q missing %q", got, want)
		}
	}
}

func TestCompactTokenCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{48_000, "48k"},
		{999_999, "999.9k"},
		{1_000_000, "1M"},
		{1_500_000, "1.5M"},
		{128_400_000, "128.4M"},
		{1_000_000_000, "1B"},
		{1_100_000_000, "1.1B"},
	}
	for _, c := range cases {
		if got := compactTokenCount(c.in); got != c.want {
			t.Fatalf("compactTokenCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPanelSnapshotAdaptsLargeContextUnits(t *testing.T) {
	editor := NewLineEditor(func([]byte) {})
	panel := NewPanel(PanelConfig{SessionName: "work", AgentType: "codex"}, editor)
	panel.SetSize(80, 14)
	panel.SetUsage(&UsageStats{TotalTokens: 1_048_576, ContextWindow: 10_000_000})

	got := string(panel.RenderPanel())
	if !strings.Contains(got, "ctx: 1M/10M (10%)") {
		t.Fatalf("state line should scale both counts past k:\n%s", got)
	}
}

func TestUsageTimelineTextIncludesSpeedMetrics(t *testing.T) {
	u := &UsageStats{
		InputTokens:   100,
		OutputTokens:  450,
		TotalTokens:   550,
		ContextWindow: 200000,
		TTFTMs:        1230,
		TokensPerSec:  45,
	}
	got := usageTimelineText(u)
	for _, want := range []string{
		"ttft 1.2s",
		"45 tok/s",
		"ctx: 550/200k (0%)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("timeline text %q missing %q", got, want)
		}
	}
}

func TestUsageTimelineTextOmitsMissingSpeedMetrics(t *testing.T) {
	u := &UsageStats{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	got := usageTimelineText(u)
	if strings.Contains(got, "ttft") || strings.Contains(got, "tok/s") {
		t.Fatalf("timeline text %q should omit unreported speed metrics", got)
	}
}
