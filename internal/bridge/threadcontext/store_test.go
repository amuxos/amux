package threadcontext

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStoreKeepsTopicRootSenderRoleNeutral(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.RecordEvent(ctx, ThreadEvent{
		ChatID:    "oc-topic",
		RootMsgID: "om-root",
		ThreadID:  "omt-topic",
		MessageID: "om-root",
		EventType: EventTypeTopicRoot,
		ActorType: "app",
		ActorID:   "cli-agent",
		BridgeId:  "bridge-a",
		Text:      "智能体生成的任务背景",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	rootText, err := store.RootMessageText(ctx, "om-root", "bridge-a")
	if err != nil {
		t.Fatalf("RootMessageText: %v", err)
	}
	if rootText != "智能体生成的任务背景" {
		t.Fatalf("root text = %q, want app-authored topic root", rootText)
	}

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:        "oc-topic",
		RootMsgID:     "om-root",
		MessageID:     "om-ask",
		BridgeId:      "bridge-a",
		WorkspaceMode: WorkspaceModeShared,
		RequestText:   "开始处理",
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if len(pkg.SelectedItems) != 1 {
		t.Fatalf("selected items = %+v, want one topic root", pkg.SelectedItems)
	}
	item := pkg.SelectedItems[0]
	if item.ItemType != string(EventTypeTopicRoot) || item.Role != "context" || item.AuthorLabel != "app/cli-agent" {
		t.Fatalf("topic root item = %+v, want neutral context with app source", item)
	}
}

func TestStoreDeduplicatesLegacyHumanRootAndTopicRoot(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().Unix()
	for _, event := range []ThreadEvent{
		{
			ChatID:    "oc-topic",
			RootMsgID: "om-root",
			MessageID: "om-root",
			EventType: EventTypeHumanMessage,
			ActorType: "human",
			ActorID:   "legacy-author",
			BridgeId:  "bridge-a",
			Text:      "旧版本缓存的根消息",
			CreatedAt: now,
		},
		{
			ChatID:    "oc-topic",
			RootMsgID: "om-root",
			MessageID: "om-root",
			EventType: EventTypeTopicRoot,
			ActorType: "app",
			ActorID:   "cli-agent",
			BridgeId:  "bridge-a",
			Text:      "新版本缓存的根消息",
			CreatedAt: now + 1,
		},
	} {
		if err := store.RecordEvent(ctx, event); err != nil {
			t.Fatalf("RecordEvent(%s): %v", event.EventType, err)
		}
	}

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:        "oc-topic",
		RootMsgID:     "om-root",
		MessageID:     "om-ask",
		BridgeId:      "bridge-a",
		WorkspaceMode: WorkspaceModeShared,
		RequestText:   "开始处理",
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if len(pkg.SelectedItems) != 1 || pkg.SelectedItems[0].ItemType != string(EventTypeTopicRoot) {
		t.Fatalf("selected items = %+v, want only the newest topic root", pkg.SelectedItems)
	}
}

func TestStoreSelectsSharedThreadContextAcrossBots(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	record := func(ev ThreadEvent) {
		t.Helper()
		if err := store.RecordEvent(ctx, ev); err != nil {
			t.Fatalf("RecordEvent(%s): %v", ev.EventType, err)
		}
	}

	record(ThreadEvent{
		ChatID:    "oc_shared",
		RootMsgID: "root-1",
		MessageID: "m-human-1",
		EventType: EventTypeHumanMessage,
		ActorType: "human",
		ActorID:   "ou_owner",
		BridgeId:  "feishu:coder",
		Text:      "我们先做一个 H5 贪吃蛇，移动端也要可玩。",
	})
	record(ThreadEvent{
		ChatID:      "oc_shared",
		RootMsgID:   "root-1",
		MessageID:   "m-planner-1",
		EventType:   EventTypeAgentOutput,
		ActorType:   "agent",
		ActorID:     "planner",
		BridgeId:    "feishu:planner",
		BotProfile:  "planner",
		TargetName:  "planner",
		SessionName: "planner-h5",
		Text:        "# 方案 v1\n使用 Canvas 2D。",
	})
	record(ThreadEvent{
		ChatID:      "oc_shared",
		RootMsgID:   "root-1",
		MessageID:   "m-planner-2",
		EventType:   EventTypeAgentOutput,
		ActorType:   "agent",
		ActorID:     "planner",
		BridgeId:    "feishu:planner",
		BotProfile:  "planner",
		TargetName:  "planner",
		SessionName: "planner-h5",
		Text:        "验收：PC 键盘、移动端滑动、最高分持久化。",
	})
	record(ThreadEvent{
		ChatID:      "oc_shared",
		RootMsgID:   "root-1",
		MessageID:   "m-coder-old",
		EventType:   EventTypeAgentOutput,
		ActorType:   "agent",
		ActorID:     "coder",
		BridgeId:    "feishu:coder",
		BotProfile:  "coder",
		TargetName:  "coder",
		SessionName: "coder-h5",
		Text:        "这是 coder 自己上一轮输出，不应回灌。",
	})
	record(ThreadEvent{
		ChatID:    "oc_shared",
		RootMsgID: "root-1",
		MessageID: "m-request",
		EventType: EventTypeUserRequest,
		ActorType: "human",
		ActorID:   "ou_owner",
		BridgeId:  "feishu:coder",
		Text:      "根据上述方案完成代码开发",
	})

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:             "oc_shared",
		RootMsgID:          "root-1",
		MessageID:          "m-request",
		BridgeId:           "feishu:coder",
		BotProfile:         "coder",
		WorkspaceMode:      WorkspaceModeShared,
		RequestText:        "根据上述方案完成代码开发",
		TargetName:         "coder",
		SessionName:        "coder-h5",
		ThreadContextLimit: defaultThreadContextLimit,
		MaxContextChars:    defaultMaxContextChars,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if !strings.Contains(pkg.ThreadContext, "H5 贪吃蛇") {
		t.Fatalf("expected human context in package, got:\n%s", pkg.ThreadContext)
	}
	if !strings.Contains(pkg.ThreadContext, "方案 v1") || !strings.Contains(pkg.ThreadContext, "最高分持久化") {
		t.Fatalf("expected multi-round planner output in package, got:\n%s", pkg.ThreadContext)
	}
	if strings.Contains(pkg.ThreadContext, "coder 自己上一轮输出") {
		t.Fatalf("expected current bot output to be excluded, got:\n%s", pkg.ThreadContext)
	}
	if strings.Contains(pkg.ThreadContext, "根据上述方案完成代码开发") {
		t.Fatalf("expected same-turn request to be recorded but not selected, got:\n%s", pkg.ThreadContext)
	}

	decisionID, err := store.RecordDecision(ctx, pkg, "rendered prompt preview")
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	explain, err := store.Explain(ctx, ExplainRequest{
		RootMsgID:  "root-1",
		BridgeId:   "feishu:coder",
		DecisionID: decisionID,
	})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if explain.Decision.ID != decisionID {
		t.Fatalf("expected decision %d, got %d", decisionID, explain.Decision.ID)
	}
	if explain.Decision.PromptPreview != "rendered prompt preview" {
		t.Fatalf("unexpected prompt preview: %q", explain.Decision.PromptPreview)
	}
	if len(explain.SelectedItems) != len(pkg.SelectedItems) {
		t.Fatalf("expected %d selected items, got %d", len(pkg.SelectedItems), len(explain.SelectedItems))
	}
}

func TestStoreOwnerOnlyUsesOnlyOtherBotOutputs(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	for _, ev := range []ThreadEvent{
		{
			ChatID:    "oc_owner",
			RootMsgID: "root-2",
			MessageID: "m-human",
			EventType: EventTypeHumanMessage,
			ActorType: "human",
			ActorID:   "ou_other",
			BridgeId:  "feishu:coder",
			Text:      "这条 owner_only 人类消息不应进入上下文。",
		},
		{
			ChatID:      "oc_owner",
			RootMsgID:   "root-2",
			MessageID:   "m-planner",
			EventType:   EventTypeAgentOutput,
			ActorType:   "agent",
			ActorID:     "planner",
			BridgeId:    "feishu:planner",
			BotProfile:  "planner",
			TargetName:  "planner",
			SessionName: "planner-session",
			Text:        "planner 给出的 owner_only 可见方案。",
		},
	} {
		if err := store.RecordEvent(ctx, ev); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:             "oc_owner",
		RootMsgID:          "root-2",
		BridgeId:           "feishu:coder",
		WorkspaceMode:      WorkspaceModeOwnerOnly,
		RequestText:        "继续实现",
		ThreadContextLimit: defaultThreadContextLimit,
		MaxContextChars:    defaultMaxContextChars,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if strings.Contains(pkg.ThreadContext, "人类消息") {
		t.Fatalf("owner_only should not select human messages, got:\n%s", pkg.ThreadContext)
	}
	if !strings.Contains(pkg.ThreadContext, "owner_only 可见方案") {
		t.Fatalf("owner_only should select other bot output, got:\n%s", pkg.ThreadContext)
	}
}

func TestStoreSharedHumanMessagesCanBeSelectedAcrossBridgeIdsWithoutDuplication(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if err := store.RecordEvent(ctx, ThreadEvent{
		ChatID:    "oc_shared",
		RootMsgID: "root-human",
		MessageID: "m-human",
		EventType: EventTypeHumanMessage,
		ActorType: "human",
		ActorID:   "ou_owner",
		BridgeId:  "feishu:coder",
		Text:      "两位辛苦了",
	}); err != nil {
		t.Fatalf("RecordEvent coder: %v", err)
	}

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:             "oc_shared",
		RootMsgID:          "root-human",
		BridgeId:           "feishu:planner",
		BotProfile:         "planner",
		WorkspaceMode:      WorkspaceModeShared,
		RequestText:        "下一步做什么",
		ThreadContextLimit: defaultThreadContextLimit,
		MaxContextChars:    defaultMaxContextChars,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if !strings.Contains(pkg.ThreadContext, "两位辛苦了") {
		t.Fatalf("expected planner to see human text recorded by coder bridge, got:\n%s", pkg.ThreadContext)
	}

	if err := store.RecordEvent(ctx, ThreadEvent{
		ChatID:    "oc_shared",
		RootMsgID: "root-human",
		MessageID: "m-human",
		EventType: EventTypeHumanMessage,
		ActorType: "human",
		ActorID:   "ou_owner",
		BridgeId:  "feishu:planner",
		Text:      "两位辛苦了",
	}); err != nil {
		t.Fatalf("RecordEvent planner: %v", err)
	}
	pkg, err = store.BuildPackage(ctx, BuildRequest{
		ChatID:             "oc_shared",
		RootMsgID:          "root-human",
		BridgeId:           "feishu:planner",
		BotProfile:         "planner",
		WorkspaceMode:      WorkspaceModeShared,
		RequestText:        "下一步做什么",
		ThreadContextLimit: defaultThreadContextLimit,
		MaxContextChars:    defaultMaxContextChars,
	})
	if err != nil {
		t.Fatalf("BuildPackage after duplicate: %v", err)
	}
	if count := strings.Count(pkg.ThreadContext, "两位辛苦了"); count != 1 {
		t.Fatalf("expected deduplicated human text once, got %d in:\n%s", count, pkg.ThreadContext)
	}
}

func TestStoreMessageTextIsScopedToChat(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	for _, ev := range []ThreadEvent{
		{ChatID: "oc-a", RootMsgID: "root-a", MessageID: "om-same", EventType: EventTypeAgentOutput, BridgeId: "feishu:a", Text: "chat A"},
		{ChatID: "oc-b", RootMsgID: "root-b", MessageID: "om-same", EventType: EventTypeAgentOutput, BridgeId: "feishu:b", Text: "chat B"},
	} {
		if err := store.RecordEvent(ctx, ev); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
	got, err := store.MessageText(ctx, "oc-a", "om-same")
	if err != nil {
		t.Fatalf("MessageText: %v", err)
	}
	if got != "chat A" {
		t.Fatalf("MessageText = %q, want chat A", got)
	}
}

func TestStoreTruncatesAgentOutput(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/feishu_context.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	longText := strings.Repeat("x", defaultMaxAgentOutputChars+100)
	if err := store.RecordEvent(ctx, ThreadEvent{
		ChatID:      "oc_shared",
		RootMsgID:   "root-3",
		MessageID:   "m-long",
		EventType:   EventTypeAgentOutput,
		ActorType:   "agent",
		ActorID:     "planner",
		BridgeId:    "feishu:planner",
		TargetName:  "planner",
		SessionName: "planner-session",
		Text:        longText,
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	pkg, err := store.BuildPackage(ctx, BuildRequest{
		ChatID:             "oc_shared",
		RootMsgID:          "root-3",
		BridgeId:           "feishu:coder",
		WorkspaceMode:      WorkspaceModeShared,
		RequestText:        "继续",
		ThreadContextLimit: defaultThreadContextLimit,
		MaxContextChars:    defaultMaxContextChars,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if len(pkg.SelectedItems) != 1 {
		t.Fatalf("expected one selected item, got %d", len(pkg.SelectedItems))
	}
	if len(pkg.SelectedItems[0].Text) != defaultMaxAgentOutputChars {
		t.Fatalf("expected text to be truncated to %d chars, got %d", defaultMaxAgentOutputChars, len(pkg.SelectedItems[0].Text))
	}
	if !pkg.SelectedItems[0].Truncated {
		t.Fatalf("expected selected item to be marked truncated")
	}
}
