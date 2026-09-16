package bridge

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestBridgeDBDocumentCommentFinalizationCanOnlyBeClaimedOnce(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	if claimed, err := db.ClaimDocumentCommentEvent(DocumentCommentEvent{
		BridgeID: "feishu:cli_test", EventID: "event-finalize",
	}); err != nil || !claimed {
		t.Fatalf("claim event = %v, %v", claimed, err)
	}

	if claimed, err := db.TryBeginDocumentCommentEventFinalization(
		"feishu:cli_test", "event-finalize",
	); err != nil || !claimed {
		t.Fatalf("first finalization claim = %v, %v; want true, nil", claimed, err)
	}
	if claimed, err := db.TryBeginDocumentCommentEventFinalization(
		"feishu:cli_test", "event-finalize",
	); err != nil || claimed {
		t.Fatalf("second finalization claim = %v, %v; want false, nil", claimed, err)
	}
}

func TestBridgeDBTopicRootPromptDeliveryFollowsBindingIdentity(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	chat := LarkChat{
		BridgeId:    "feishu:cli_test",
		ChatID:      "oc-topic",
		RootMsgID:   "root-1",
		ThreadID:    "thread-1",
		OpenId:      "open-1",
		TargetID:    "target-1",
		TargetName:  "local",
		SessionName: "session-1",
		AgentType:   "codex",
	}
	if err := db.UpsertChat(chat); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if delivered, err := db.TopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID); err != nil || delivered {
		t.Fatalf("initial delivery state = %v, err=%v; want false", delivered, err)
	}
	if marked, err := db.MarkTopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID, chat.TargetID, chat.SessionName); err != nil || !marked {
		t.Fatalf("MarkTopicRootPromptDelivered: %v", err)
	}
	if err := db.UpsertChat(chat); err != nil {
		t.Fatalf("UpsertChat same binding: %v", err)
	}
	if delivered, err := db.TopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID); err != nil || !delivered {
		t.Fatalf("same-binding delivery state = %v, err=%v; want true", delivered, err)
	}

	chat.SessionName = "session-2"
	if err := db.UpsertChat(chat); err != nil {
		t.Fatalf("UpsertChat replacement binding: %v", err)
	}
	if delivered, err := db.TopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID); err != nil || delivered {
		t.Fatalf("replacement-binding delivery state = %v, err=%v; want false", delivered, err)
	}
	if marked, err := db.MarkTopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID, "target-1", "session-1"); err != nil || marked {
		t.Fatalf("stale MarkTopicRootPromptDelivered: %v", err)
	}
	if delivered, err := db.TopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID); err != nil || delivered {
		t.Fatalf("state after stale delivery mark = %v, err=%v; want false", delivered, err)
	}
}

func TestBridgeDBPendingManagementRecoveryLifecycle(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.UpsertPendingManagementRecovery("feishu:bot-a", "owner-open", "message-old"); err != nil {
		t.Fatalf("UpsertPendingManagementRecovery: %v", err)
	}
	got, err := db.GetPendingManagementRecovery("feishu:bot-a")
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if got == nil || got.BridgeID != "feishu:bot-a" || got.OpenID != "owner-open" || got.MessageID != "message-old" || got.CreatedAt.IsZero() {
		t.Fatalf("pending recovery = %#v", got)
	}

	if err := db.UpsertPendingManagementRecovery("feishu:bot-a", "owner-open", "message-new"); err != nil {
		t.Fatalf("replace pending recovery: %v", err)
	}
	if err := db.DeletePendingManagementRecovery("feishu:bot-a", "message-old"); err != nil {
		t.Fatalf("conditional delete old recovery: %v", err)
	}
	got, err = db.GetPendingManagementRecovery("feishu:bot-a")
	if err != nil {
		t.Fatalf("get replacement recovery: %v", err)
	}
	if got == nil || got.MessageID != "message-new" {
		t.Fatalf("replacement recovery = %#v, want message-new", got)
	}

	if err := db.DeletePendingManagementRecovery("feishu:bot-a", "message-new"); err != nil {
		t.Fatalf("delete pending recovery: %v", err)
	}
	got, err = db.GetPendingManagementRecovery("feishu:bot-a")
	if err != nil {
		t.Fatalf("get deleted recovery: %v", err)
	}
	if got != nil {
		t.Fatalf("pending recovery after delete = %#v, want nil", got)
	}
}

func TestBridgeDBPendingManagementUpgradeRecovery(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.UpsertPendingManagementRecoveryForOperation(
		"feishu:bot-a", "owner-open", "message-upgrade", managementRecoveryUpgrade, "1.2.3",
	); err != nil {
		t.Fatalf("UpsertPendingManagementRecoveryForOperation: %v", err)
	}
	got, err := db.GetPendingManagementRecovery("feishu:bot-a")
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if got == nil || got.Operation != managementRecoveryUpgrade || got.ExpectedVersion != "1.2.3" {
		t.Fatalf("recovery = %#v", got)
	}
}

func TestBridgeDBPendingManagementRecoveryPreservesSurface(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.UpsertPendingManagementRecoveryForSurface(
		"feishu:bot-a",
		"owner-open",
		"message-amux",
		managementRecoveryRestart,
		"",
		string(managementSurfaceAmux),
	); err != nil {
		t.Fatalf("UpsertPendingManagementRecoveryForSurface: %v", err)
	}
	got, err := db.GetPendingManagementRecovery("feishu:bot-a")
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if got == nil || got.Surface != string(managementSurfaceAmux) {
		t.Fatalf("recovery = %#v, want navigation surface", got)
	}
}

func TestBridgeDBMessageSeenPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	inserted, err := db.MarkMessageSeen("feishu:cli_test", "om-old", "root-1", "oc-workspace")
	if err != nil {
		t.Fatalf("MarkMessageSeen first: %v", err)
	}
	if !inserted {
		t.Fatal("first MarkMessageSeen returned false, want true")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("reopen bridge db: %v", err)
	}
	defer reopened.Close()
	inserted, err = reopened.MarkMessageSeen("feishu:cli_test", "om-old", "root-1", "oc-workspace")
	if err != nil {
		t.Fatalf("MarkMessageSeen second: %v", err)
	}
	if inserted {
		t.Fatal("duplicate message was inserted after reopen")
	}
	otherBridgeInserted, err := reopened.MarkMessageSeen("feishu:cli_other", "om-old", "root-1", "oc-workspace")
	if err != nil {
		t.Fatalf("MarkMessageSeen other bridge: %v", err)
	}
	if !otherBridgeInserted {
		t.Fatal("message id should dedupe per bridge_id")
	}
}

func TestBridgeDBClaimsBotTurnsWithDurableDedupeAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}

	claim, err := db.ClaimBotTurn("feishu:cli_test", "oc-chat", "root-1", "om-1", "ou-caller", 2)
	if err != nil || !claim.Accepted || claim.Exceeded || claim.Turn != 1 {
		t.Fatalf("first claim = %+v err=%v", claim, err)
	}
	duplicate, err := db.ClaimBotTurn("feishu:cli_test", "oc-chat", "root-1", "om-1", "ou-caller", 2)
	if err != nil || duplicate.Accepted || duplicate.Exceeded || duplicate.Turn != 1 {
		t.Fatalf("duplicate claim = %+v err=%v", duplicate, err)
	}
	busy, err := db.ClaimBotTurn("feishu:cli_test", "oc-chat", "root-1", "om-busy", "ou-other", 2)
	if err != nil || busy.Accepted || !busy.Busy || busy.Turn != 1 {
		t.Fatalf("busy claim = %+v err=%v", busy, err)
	}
	if _, err := db.ClearPendingBotHandoff("feishu:cli_test", "root-1", "om-1"); err != nil {
		t.Fatalf("clear first pending: %v", err)
	}
	second, err := db.ClaimBotTurn("feishu:cli_test", "oc-chat", "root-1", "om-2", "ou-caller", 2)
	if err != nil || !second.Accepted || second.Exceeded || second.Turn != 2 {
		t.Fatalf("second claim = %+v err=%v", second, err)
	}
	if _, err := db.ClearPendingBotHandoff("feishu:cli_test", "root-1", "om-2"); err != nil {
		t.Fatalf("clear second pending: %v", err)
	}
	exceeded, err := db.ClaimBotTurn("feishu:cli_test", "oc-chat", "root-1", "om-3", "ou-other", 2)
	if err != nil || exceeded.Accepted || !exceeded.Exceeded || exceeded.Turn != 2 {
		t.Fatalf("exceeded claim = %+v err=%v", exceeded, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("reopen bridge db: %v", err)
	}
	defer reopened.Close()
	pending, err := reopened.PendingBotHandoff("feishu:cli_test", "root-1")
	if err != nil || pending != nil {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
}

func TestBridgeDBWorkspaceDefaultsRoundTrip(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.SetWorkspaceDefaultAgent("feishu:cli_test", "oc-workspace", "codex"); err != nil {
		t.Fatalf("SetWorkspaceDefaultAgent: %v", err)
	}
	if err := db.SetWorkspaceDefaultTarget("feishu:cli_test", "oc-workspace", "local"); err != nil {
		t.Fatalf("SetWorkspaceDefaultTarget: %v", err)
	}
	if err := db.SetWorkspaceDefaultCwd("feishu:cli_test", "oc-workspace", "/repo/amux"); err != nil {
		t.Fatalf("SetWorkspaceDefaultCwd: %v", err)
	}
	if err := db.SetWorkspaceDefaultModel("feishu:cli_test", "oc-workspace", "DeepSeek-V4-Flash"); err != nil {
		t.Fatalf("SetWorkspaceDefaultModel: %v", err)
	}
	if err := db.SetWorkspaceDefaultArgs("feishu:cli_test", "oc-workspace", []string{"--yolo", "--profile", "fast"}); err != nil {
		t.Fatalf("SetWorkspaceDefaultArgs: %v", err)
	}

	ws, err := db.GetWorkspace("feishu:cli_test", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.DefaultAgent != "codex" || ws.DefaultTarget != "local" || ws.DefaultCwd != "/repo/amux" || ws.DefaultModel != "DeepSeek-V4-Flash" {
		t.Fatalf("workspace defaults = %+v, want codex/local//repo/amux/DeepSeek-V4-Flash", ws)
	}
	if got, want := strings.Join(ws.DefaultArgs, " "), "--yolo --profile fast"; got != want {
		t.Fatalf("DefaultArgs = %q, want %q", got, want)
	}
}

func TestBridgeDBWorkspaceDefaultsSeedFromConfig(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.workspace_defaults]
agent = "codex"
target = "local"
cwd = "/repo/amux"
model = "DeepSeek-V4-Flash"
args = ["--yolo", "--profile", "fast"]
`)
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	ws, err := db.GetWorkspace("feishu:cli_test", "oc-config")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.DefaultAgent != "codex" || ws.DefaultTarget != "local" || ws.DefaultCwd != "/repo/amux" || ws.DefaultModel != "DeepSeek-V4-Flash" {
		t.Fatalf("workspace defaults = %+v, want config defaults", ws)
	}
	if got, want := strings.Join(ws.DefaultArgs, " "), "--yolo --profile fast"; got != want {
		t.Fatalf("DefaultArgs = %q, want %q", got, want)
	}
}

func dbPathForTest(t *testing.T, db *bridgeDB) string {
	t.Helper()
	var seq int
	var name, file string
	if err := db.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatalf("database_list: %v", err)
	}
	return file
}

func TestBridgeDBChatDisplayTitleRoundTrip(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.UpsertChat(LarkChat{
		BridgeId:       "feishu:cli_test",
		ChatID:         "oc-workspace",
		RootMsgID:      "root-1",
		ThreadID:       "thread-1",
		OpenId:         "open-1",
		TargetID:       "target-1",
		TargetName:     "local",
		SessionName:    "feishu-abc123",
		AgentProfile:   "builtin/codex",
		AgentType:      "codex",
		EffectiveModel: "gpt-5.4",
		DisplayTitle:   "修复登录问题",
	}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	chat, err := db.FindByRootMsg("feishu:cli_test", "root-1")
	if err != nil {
		t.Fatalf("FindByRootMsg: %v", err)
	}
	if chat == nil || chat.DisplayTitle != "修复登录问题" || chat.AgentProfile != "builtin/codex" {
		t.Fatalf("chat = %+v, want display title and agent profile", chat)
	}
	bindings, err := listBindingsFromDB(db, "feishu:cli_test")
	if err != nil {
		t.Fatalf("listBindingsFromDB: %v", err)
	}
	if len(bindings) != 1 || bindings[0].DisplayTitle != "修复登录问题" || bindings[0].AgentProfile != "builtin/codex" {
		t.Fatalf("bindings = %+v, want display title and agent profile", bindings)
	}
}

func TestBridgeDBUpsertAcrossProfilesSurvivesConcurrentReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	writer, err := openBridgeDBForProfile(path, "writer")
	if err != nil {
		t.Fatalf("open writer bridge DB: %v", err)
	}
	defer writer.Close()
	reader, err := openBridgeDBForProfile(path, "reader")
	if err != nil {
		t.Fatalf("open reader bridge DB: %v", err)
	}
	defer reader.Close()
	for name, db := range map[string]*bridgeDB{"writer": writer, "reader": reader} {
		var journalMode string
		if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("read %s journal mode: %v", name, err)
		}
		if journalMode != "wal" {
			t.Fatalf("%s journal mode = %q, want wal", name, journalMode)
		}
		var busyTimeout int
		if err := db.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("read %s busy timeout: %v", name, err)
		}
		if busyTimeout != bridgeDBBusyTimeoutMS {
			t.Fatalf("%s busy timeout = %d, want %d", name, busyTimeout, bridgeDBBusyTimeoutMS)
		}
	}

	first := LarkChat{
		BridgeId:    "feishu:writer",
		ChatID:      "oc-workspace",
		RootMsgID:   "root-1",
		ThreadID:    "thread-1",
		OpenId:      "open-1",
		TargetID:    "target-1",
		TargetName:  "runtime-1",
		SessionName: "session-1",
		AgentType:   "codex",
	}
	seed := first
	seed.RootMsgID = "root-seed"
	seed.ThreadID = "thread-seed"
	seed.SessionName = "session-seed"
	if err := writer.UpsertChat(seed); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	rows, err := reader.db.Query(`SELECT root_msg_id FROM lark_chats ORDER BY id`)
	if err != nil {
		t.Fatalf("start concurrent reader: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("concurrent reader returned no seed binding")
	}
	if err := writer.UpsertChat(first); err != nil {
		t.Fatalf("UpsertChat while another profile is reading: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close concurrent reader: %v", err)
	}

	competingWriter, err := reader.db.Begin()
	if err != nil {
		t.Fatalf("begin competing writer: %v", err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		released <- competingWriter.Rollback()
	}()
	first.ThreadID = "thread-2"
	if err := writer.UpsertChat(first); err != nil {
		t.Fatalf("UpsertChat after lock contention: %v", err)
	}
	if err := <-released; err != nil {
		t.Fatalf("release competing writer: %v", err)
	}
}

func TestBridgeDBWorkspaceModeIsScopedByBridgeId(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.SetWorkspaceMode("feishu:cli_planner", "oc-workspace", WorkspaceModeShared); err != nil {
		t.Fatalf("SetWorkspaceMode planner: %v", err)
	}
	planner, err := db.GetWorkspace("feishu:cli_planner", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace planner: %v", err)
	}
	coder, err := db.GetWorkspace("feishu:cli_coder", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace coder: %v", err)
	}
	if planner.Mode != WorkspaceModeShared {
		t.Fatalf("planner mode = %q, want shared", planner.Mode)
	}
	if coder.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("coder mode = %q, want owner_only", coder.Mode)
	}
}

func TestBridgeDBBotSessionRoundTripAndDisable(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.SetBotSession(BotSession{
		BridgeId:       "feishu:cli_test",
		Enabled:        true,
		TargetID:       "target-1",
		TargetName:     "local",
		SessionName:    "bot-main",
		AgentProfile:   "builtin/codex",
		AgentType:      "codex",
		EffectiveModel: "gpt-5.4",
		Cwd:            "/repo/amux",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}

	got, err := db.GetBotSession("feishu:cli_test")
	if err != nil {
		t.Fatalf("GetBotSession: %v", err)
	}
	if got == nil || !got.Enabled || got.TargetID != "target-1" || got.TargetName != "local" || got.SessionName != "bot-main" || got.AgentProfile != "builtin/codex" || got.AgentType != "codex" || got.EffectiveModel != "gpt-5.4" || got.Cwd != "/repo/amux" {
		t.Fatalf("bot session = %+v, want enabled local/bot-main codex", got)
	}

	if err := db.DisableBotSession("feishu:cli_test"); err != nil {
		t.Fatalf("DisableBotSession: %v", err)
	}
	got, err = db.GetBotSession("feishu:cli_test")
	if err != nil {
		t.Fatalf("GetBotSession disabled: %v", err)
	}
	if got == nil || got.Enabled || got.TargetName != "local" || got.SessionName != "bot-main" {
		t.Fatalf("disabled bot session = %+v, want disabled but preserving session", got)
	}
}

func TestBridgeDBMigratesBotSessionCwdColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE feishu_bot_sessions (
			bridge_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL DEFAULT '',
			agent_profile TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT '',
			effective_model TEXT NOT NULL DEFAULT '',
			last_reply_kind TEXT NOT NULL DEFAULT '',
			last_reply_chat_id TEXT NOT NULL DEFAULT '',
			last_reply_open_id TEXT NOT NULL DEFAULT '',
			last_input_message_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy bot session table: %v", err)
	}
	_ = legacy.Close()

	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("openBridgeDB migration: %v", err)
	}
	defer db.Close()
	if err := db.SetBotSession(BotSession{
		BridgeId: "feishu:test", Enabled: true, TargetID: "target-1", SessionName: "main", Cwd: "/repo/amux",
	}); err != nil {
		t.Fatalf("SetBotSession after migration: %v", err)
	}
	got, err := db.GetBotSession("feishu:test")
	if err != nil || got == nil || got.Cwd != "/repo/amux" {
		t.Fatalf("GetBotSession after migration = %+v err=%v, want cwd", got, err)
	}
}

func TestBridgeDBDetachActiveTopicBindingForBotSession(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.UpsertChat(LarkChat{
		BridgeId:    "feishu:cli_test",
		ChatID:      "oc-workspace",
		RootMsgID:   "root-1",
		ThreadID:    "thread-1",
		OpenId:      "open-1",
		TargetID:    "target-1",
		TargetName:  "local",
		SessionName: "bot-main",
		AgentType:   "codex",
	}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	detached, err := db.DetachActiveTopicBindingForSession("target-1", "bot-main")
	if err != nil {
		t.Fatalf("DetachActiveTopicBindingForSession: %v", err)
	}
	if detached == nil || detached.RootMsgID != "root-1" {
		t.Fatalf("detached = %+v, want root-1", detached)
	}
	chat, err := db.FindByRootMsg("feishu:cli_test", "root-1")
	if err != nil {
		t.Fatalf("FindByRootMsg: %v", err)
	}
	if chat == nil || chat.Status != ChatStatusDetached {
		t.Fatalf("chat = %+v, want detached", chat)
	}
}

func TestBridgeDBWorkspaceDefaultsToOwnerOnly(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	ws, err := db.GetWorkspace("feishu:cli_test", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("workspace mode = %q, want %q", ws.Mode, WorkspaceModeOwnerOnly)
	}

	if ws.MaxSessionsTotal != 500 {
		t.Fatalf("MaxSessionsTotal = %d, want 500", ws.MaxSessionsTotal)
	}
	if ws.MaxSessionsPerUser != 0 {
		t.Fatalf("MaxSessionsPerUser = %d, want 0", ws.MaxSessionsPerUser)
	}
	if ws.ThreadContextLimit != 20 {
		t.Fatalf("ThreadContextLimit = %d, want 20", ws.ThreadContextLimit)
	}
	if ws.IdleTimeoutSeconds != 0 {
		t.Fatalf("IdleTimeoutSeconds = %d, want 0", ws.IdleTimeoutSeconds)
	}
}

func TestCommitSingletonBotSettingsAtomicallyUpdatesProfileAndBinding(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	state, err := db.GetBotProfileState("feishu:test", "thread")
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}

	got, err := db.CommitSingletonBotSettings("feishu:test", state.Version, "builtin/codex", BotSession{
		BridgeId:       "feishu:test",
		Enabled:        true,
		TargetID:       "runtime-1",
		TargetName:     "local",
		SessionName:    "managed-1",
		AgentProfile:   "builtin/codex",
		AgentType:      "codex",
		EffectiveModel: "gpt-5",
		Cwd:            "/repo/amux",
	})
	if err != nil {
		t.Fatalf("CommitSingletonBotSettings: %v", err)
	}
	if got.InteractionMode != "singleton" || got.DefaultAgent != "builtin/codex" || got.Version != state.Version+1 {
		t.Fatalf("state = %+v, want singleton/builtin-codex next version", got)
	}
	binding, err := db.GetBotSession("feishu:test")
	if err != nil {
		t.Fatalf("GetBotSession: %v", err)
	}
	if !botSessionReady(binding) || binding.SessionName != "managed-1" || binding.AgentProfile != "builtin/codex" || binding.Cwd != "/repo/amux" {
		t.Fatalf("binding = %+v, want enabled managed-1 builtin/codex", binding)
	}
}

func TestBridgeDBSetWorkspaceMode(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.SetWorkspaceMode("feishu:cli_test", "oc-workspace", WorkspaceModeShared); err != nil {
		t.Fatalf("SetWorkspaceMode shared: %v", err)
	}
	ws, err := db.GetWorkspace("feishu:cli_test", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Mode != WorkspaceModeShared {
		t.Fatalf("Mode = %q, want %q", ws.Mode, WorkspaceModeShared)
	}

	if err := db.SetWorkspaceMode("feishu:cli_test", "oc-workspace", WorkspaceModeOwnerOnly); err != nil {
		t.Fatalf("SetWorkspaceMode owner_only: %v", err)
	}
	ws, err = db.GetWorkspace("feishu:cli_test", "oc-workspace")
	if err != nil {
		t.Fatalf("GetWorkspace again: %v", err)
	}
	if ws.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("Mode = %q, want %q", ws.Mode, WorkspaceModeOwnerOnly)
	}
}

func TestBridgeDBSetWorkspaceModeRejectsInvalidMode(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.SetWorkspaceMode("feishu:cli_test", "oc-workspace", "public"); err == nil {
		t.Fatal("SetWorkspaceMode accepted invalid mode, want error")
	}
}

func TestBridgeDBPrivateWorkspaceKind(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.EnsureWorkspaceKind("feishu:cli_test", "oc-private", WorkspaceKindPrivate); err != nil {
		t.Fatalf("EnsureWorkspaceKind private: %v", err)
	}
	ws, err := db.GetWorkspace("feishu:cli_test", "oc-private")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Kind != WorkspaceKindPrivate {
		t.Fatalf("Kind = %q, want %q", ws.Kind, WorkspaceKindPrivate)
	}
	if ws.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("Mode = %q, want owner_only", ws.Mode)
	}

	kind, err := db.WorkspaceKind("feishu:cli_test", "oc-private")
	if err != nil {
		t.Fatalf("WorkspaceKind: %v", err)
	}
	if kind != WorkspaceKindPrivate {
		t.Fatalf("WorkspaceKind = %q, want %q", kind, WorkspaceKindPrivate)
	}
}

func TestBridgeDBRejectsSharedModeForPrivateWorkspace(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	if err := db.EnsureWorkspaceKind("feishu:cli_test", "oc-private", WorkspaceKindPrivate); err != nil {
		t.Fatalf("EnsureWorkspaceKind private: %v", err)
	}
	if err := db.SetWorkspaceMode("feishu:cli_test", "oc-private", WorkspaceModeShared); err == nil {
		t.Fatal("SetWorkspaceMode shared on private workspace succeeded, want error")
	}
	ws, err := db.GetWorkspace("feishu:cli_test", "oc-private")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("Mode after rejected shared = %q, want owner_only", ws.Mode)
	}
}

func TestBridgeDBTracksGroupWatchBindingProvenance(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	for _, chat := range []LarkChat{
		{BridgeId: "feishu:bot", ChatID: "oc-alert", RootMsgID: "root-watch", ThreadID: "root-watch", OpenId: "app-1", TargetID: "target-1", TargetName: "local", SessionName: "watch-1", EntrySource: ChatEntrySourceGroupWatch},
		{BridgeId: "feishu:bot", ChatID: "oc-alert", RootMsgID: "root-manual", ThreadID: "root-manual", OpenId: "user-1", TargetID: "target-1", TargetName: "local", SessionName: "manual-1"},
		{BridgeId: "feishu:bot", ChatID: "oc-other", RootMsgID: "root-other", ThreadID: "root-other", OpenId: "app-1", TargetID: "target-1", TargetName: "local", SessionName: "watch-2", EntrySource: ChatEntrySourceGroupWatch},
	} {
		if err := db.UpsertChat(chat); err != nil {
			t.Fatalf("UpsertChat(%s): %v", chat.RootMsgID, err)
		}
	}

	got, err := db.ListGroupWatchCapacityBindingsByChatID("feishu:bot", "oc-alert")
	if err != nil {
		t.Fatalf("ListGroupWatchCapacityBindingsByChatID: %v", err)
	}
	if len(got) != 1 || got[0].RootMsgID != "root-watch" || got[0].EntrySource != ChatEntrySourceGroupWatch {
		t.Fatalf("group watch bindings = %+v, want only root-watch", got)
	}
	if source, err := db.FindChatEntrySource("feishu:bot", "root-watch"); err != nil || source != ChatEntrySourceGroupWatch {
		t.Fatalf("FindChatEntrySource(root-watch) = %q, %v; want group_watch", source, err)
	}
	if source, err := db.FindChatEntrySource("feishu:bot", "root-manual"); err != nil || source != ChatEntrySourceManual {
		t.Fatalf("FindChatEntrySource(root-manual) = %q, %v; want manual", source, err)
	}
	if source, err := db.FindChatEntrySource("feishu:bot", "missing"); err != nil || source != "" {
		t.Fatalf("FindChatEntrySource(missing) = %q, %v; want empty", source, err)
	}
	if err := db.DeactivateChat("feishu:bot", "root-watch"); err != nil {
		t.Fatalf("DeactivateChat: %v", err)
	}
	got, err = db.ListGroupWatchCapacityBindingsByChatID("feishu:bot", "oc-alert")
	if err != nil {
		t.Fatalf("ListGroupWatchCapacityBindingsByChatID after detach: %v", err)
	}
	if len(got) != 1 || got[0].Status != ChatStatusDetached {
		t.Fatalf("detached group watch bindings = %+v, want detached root-watch", got)
	}
	if err := db.TerminateChat("feishu:bot", "root-watch"); err != nil {
		t.Fatalf("TerminateChat: %v", err)
	}
	got, err = db.ListGroupWatchCapacityBindingsByChatID("feishu:bot", "oc-alert")
	if err != nil {
		t.Fatalf("ListGroupWatchCapacityBindingsByChatID after terminal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("group watch bindings after terminal = %+v, want none", got)
	}

	if _, err := db.db.Exec(`DELETE FROM feishu_binding_sources WHERE bridge_id=? AND root_msg_id=?`, "feishu:bot", "root-other"); err != nil {
		t.Fatalf("remove source to emulate pre-upgrade binding: %v", err)
	}
	got, err = db.ListGroupWatchCapacityBindingsByChatID("feishu:bot", "oc-other")
	if err != nil {
		t.Fatalf("ListGroupWatchCapacityBindingsByChatID legacy: %v", err)
	}
	if len(got) != 1 || got[0].RootMsgID != "root-other" || got[0].EntrySource != "" {
		t.Fatalf("legacy unclassified bindings = %+v, want conservative root-other candidate", got)
	}
}

func TestIsDuplicateColumnError(t *testing.T) {
	if !isDuplicateColumnError(errors.New("SQL logic error: duplicate column name: thread_id (1)")) {
		t.Fatal("duplicate column error was not recognised")
	}
	if isDuplicateColumnError(errors.New("database is locked")) {
		t.Fatal("database locked should not be treated as duplicate column")
	}
	if !strings.Contains("duplicate column name", "duplicate column") {
		t.Fatal("sanity")
	}
}
