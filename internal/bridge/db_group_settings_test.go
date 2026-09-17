package bridge

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
)

func testGroupSettings() GroupSettings {
	return GroupSettings{
		BridgeID: "feishu:group-test", ChatID: "oc-group", InteractionMode: config.FeishuInteractionThread,
		IsolationRequired: true, DefaultAgent: "review/reviewer", DefaultRuntimeRef: "runtime/test",
		DefaultModelRef: "model/test", DefaultReasoningEffort: "high", ThinkingProcess: config.FeishuThinkingProcessHidden,
		Mode: WorkspaceModeShared, DefaultTarget: "worker", DefaultCwd: "/repo/group project",
		DefaultArgs: []string{"--setting", "a quoted \"value\"\nwith a newline"}, IdleTimeoutSeconds: 900,
		ThreadContextLimit: 35, MaxSessionsTotal: 27, MaxSessionsPerUser: 4, UpdatedBy: "ou-owner",
	}
}

func openGroupSettingsTestDB(t *testing.T, path string) *bridgeDB {
	t.Helper()
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func requireGroupWorkspaceProjection(t *testing.T, db *bridgeDB, settings *GroupSettings) {
	t.Helper()
	ws, err := db.ReadWorkspace(settings.BridgeID, settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	want := &FeishuWorkspace{
		BridgeId: settings.BridgeID, ChatID: settings.ChatID, Kind: ws.Kind,
		Mode: settings.Mode, DefaultAgent: settings.DefaultAgent, DefaultTarget: settings.DefaultTarget,
		DefaultCwd: settings.DefaultCwd, DefaultModel: settings.DefaultModelRef, DefaultArgs: settings.DefaultArgs,
		ThreadContextLimit: settings.ThreadContextLimit, MaxSessionsTotal: settings.MaxSessionsTotal,
		MaxSessionsPerUser: settings.MaxSessionsPerUser, IdleTimeoutSeconds: settings.IdleTimeoutSeconds,
	}
	if !reflect.DeepEqual(ws, want) {
		t.Fatalf("workspace projection = %+v; want %+v", ws, want)
	}
}

func TestBridgeDBGroupSettingsReadOnlyAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db := openGroupSettingsTestDB(t, path)
	settings := testGroupSettings()
	if got, err := db.ReadGroupSettings(settings.BridgeID, settings.ChatID); err != nil || got != nil {
		t.Fatalf("unset settings = %+v, %v", got, err)
	}
	if got, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || got != "" {
		t.Fatalf("unset root = %q, %v", got, err)
	}
	if got, err := db.FindGroupSessionOrigin(settings.BridgeID, settings.ChatID, "om-root"); err != nil || got != "" {
		t.Fatalf("unset origin = %q, %v", got, err)
	}
	for _, table := range []string{"feishu_group_settings", "feishu_workspaces", "feishu_bot_profiles", "feishu_group_session_origins"} {
		var count int
		if err := db.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("read created rows in %s: count=%d err=%v", table, count, err)
		}
	}
	before := time.Now().UTC().Truncate(time.Second)
	saved, err := db.SaveGroupSettings(settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	settings.Version = 1
	settings.UpdatedAt = saved.UpdatedAt
	if !reflect.DeepEqual(saved, &settings) || saved.UpdatedAt.Before(before) {
		t.Fatalf("saved settings = %+v; want %+v", saved, settings)
	}
	requireGroupWorkspaceProjection(t, db, saved)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openGroupSettingsTestDB(t, path)
	got, err := reopened.ReadGroupSettings(settings.BridgeID, settings.ChatID)
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("reopened settings = %+v, %v; want %+v", got, err, saved)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	keys := []string{"bridge_id", "chat_id", "interaction_mode", "isolation_required", "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "thinking_process", "mode", "default_target", "default_cwd", "default_args", "idle_timeout_seconds", "thread_context_limit", "max_sessions_total", "max_sessions_per_user", "version", "updated_by", "updated_at"}
	if len(fields) != len(keys) {
		t.Fatalf("JSON field count = %d; want %d: %s", len(fields), len(keys), data)
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing JSON field %q", key)
		}
	}
}

func TestBridgeDBGroupSettingsCASAndScope(t *testing.T) {
	db := openGroupSettingsTestDB(t, filepath.Join(t.TempDir(), "bridge.db"))
	settings := testGroupSettings()
	first, err := db.SaveGroupSettings(settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{0, 2} {
		if _, err := db.SaveGroupSettings(settings, version); !errors.Is(err, errGroupSettingsChanged) {
			t.Fatalf("save version %d = %v; want conflict", version, err)
		}
	}
	for _, scope := range [][2]string{{"feishu:other", settings.ChatID}, {settings.BridgeID, "oc-other"}} {
		other := settings
		other.BridgeID, other.ChatID, other.DefaultAgent = scope[0], scope[1], "other/profile"
		if _, err := db.SaveGroupSettings(other, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.EnsureWorkspaceKind(settings.BridgeID, settings.ChatID, WorkspaceKindTopicGroup); err != nil {
		t.Fatal(err)
	}
	// Empty execution selections and zero quotas are complete replacement values.
	settings.DefaultAgent, settings.DefaultRuntimeRef, settings.DefaultModelRef = "", "", ""
	settings.DefaultReasoningEffort, settings.DefaultTarget, settings.DefaultCwd = "", "", ""
	settings.DefaultArgs = nil
	settings.MaxSessionsTotal, settings.MaxSessionsPerUser, settings.IdleTimeoutSeconds = 0, 0, 0
	second, err := db.SaveGroupSettings(settings, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || second.DefaultAgent != "" || second.DefaultRuntimeRef != "" || len(second.DefaultArgs) != 0 {
		t.Fatalf("replacement settings = %+v", second)
	}
	requireGroupWorkspaceProjection(t, db, second)
	ws, _ := db.ReadWorkspace(settings.BridgeID, settings.ChatID)
	if ws.Kind != WorkspaceKindTopicGroup {
		t.Fatalf("existing workspace kind = %q", ws.Kind)
	}
	for _, scope := range [][2]string{{"feishu:other", settings.ChatID}, {settings.BridgeID, "oc-other"}} {
		got, err := db.ReadGroupSettings(scope[0], scope[1])
		if err != nil || got == nil || got.DefaultAgent != "other/profile" || got.Version != 1 {
			t.Fatalf("other scope changed: %+v, %v", got, err)
		}
	}
}

func TestBridgeDBGroupSettingsConcurrentCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	dbs := []*bridgeDB{openGroupSettingsTestDB(t, path), openGroupSettingsTestDB(t, path)}
	for expectedVersion := int64(0); expectedVersion < 2; expectedVersion++ {
		start := make(chan struct{})
		type outcome struct {
			settings *GroupSettings
			err      error
		}
		results := make(chan outcome, len(dbs))
		for index, db := range dbs {
			go func(index int, db *bridgeDB) {
				settings := testGroupSettings()
				settings.MaxSessionsTotal = 80 + index
				<-start
				saved, err := db.SaveGroupSettings(settings, expectedVersion)
				results <- outcome{settings: saved, err: err}
			}(index, db)
		}
		close(start)
		var winner *GroupSettings
		conflicts := 0
		for range dbs {
			result := <-results
			if result.err == nil {
				if winner != nil {
					t.Fatal("two saves won one settings version")
				}
				winner = result.settings
			} else if errors.Is(result.err, errGroupSettingsChanged) {
				conflicts++
			} else {
				t.Fatal(result.err)
			}
		}
		if winner == nil || conflicts != 1 || winner.Version != expectedVersion+1 {
			t.Fatalf("winner=%+v conflicts=%d", winner, conflicts)
		}
		got, err := dbs[0].ReadGroupSettings(winner.BridgeID, winner.ChatID)
		if err != nil || !reflect.DeepEqual(got, winner) {
			t.Fatalf("persisted winner = %+v, %v; want %+v", got, err, winner)
		}
		requireGroupWorkspaceProjection(t, dbs[0], winner)
	}
}

func TestBridgeDBGroupSettingsValidationAndAtomicRollback(t *testing.T) {
	db := openGroupSettingsTestDB(t, filepath.Join(t.TempDir(), "bridge.db"))
	invalid := map[string]func(*GroupSettings){
		"bridge":      func(s *GroupSettings) { s.BridgeID = " " },
		"chat":        func(s *GroupSettings) { s.ChatID = "" },
		"interaction": func(s *GroupSettings) { s.InteractionMode = "thread_isolated" },
		"isolation":   func(s *GroupSettings) { s.InteractionMode = config.FeishuInteractionSingleton },
		"workspace":   func(s *GroupSettings) { s.Mode = "public" },
		"thinking":    func(s *GroupSettings) { s.ThinkingProcess = "invalid" },
		"idle":        func(s *GroupSettings) { s.IdleTimeoutSeconds = 1 },
		"context":     func(s *GroupSettings) { s.ThreadContextLimit = -1 },
		"total quota": func(s *GroupSettings) { s.MaxSessionsTotal = -1 },
		"user quota":  func(s *GroupSettings) { s.MaxSessionsPerUser = -1 },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			settings := testGroupSettings()
			mutate(&settings)
			if _, err := db.SaveGroupSettings(settings, 0); err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
	settings := testGroupSettings()
	if err := db.EnsureWorkspaceKind(settings.BridgeID, settings.ChatID, WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveGroupSettings(settings, 0); err == nil {
		t.Fatal("private workspace accepted group settings")
	}
	if got, err := db.ReadGroupSettings(settings.BridgeID, settings.ChatID); err != nil || got != nil {
		t.Fatalf("failed save created settings: %+v, %v", got, err)
	}
	settings.ChatID = "oc-transaction"
	settings.InteractionMode, settings.IsolationRequired = config.FeishuInteractionSingleton, false
	saved, err := db.SaveGroupSettings(settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	createGroupSessionTopic(t, db, settings, "om-transaction")
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-transaction"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_group_workspace_update BEFORE UPDATE ON feishu_workspaces
		BEGIN SELECT RAISE(ABORT, 'injected workspace write failure'); END`); err != nil {
		t.Fatal(err)
	}
	settings.Mode = WorkspaceModeOwnerOnly
	settings.DefaultAgent = "new/profile"
	settings.InteractionMode = config.FeishuInteractionThread
	if _, err := db.SaveGroupSettings(settings, saved.Version); err == nil {
		t.Fatal("injected projection failure did not fail save")
	}
	got, err := db.ReadGroupSettings(settings.BridgeID, settings.ChatID)
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("partial settings persisted: %+v, %v; want %+v", got, err, saved)
	}
	requireGroupWorkspaceProjection(t, db, saved)
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "om-transaction" {
		t.Fatalf("failed save retired the active pointer: %q, %v", root, err)
	}
}

func createGroupSessionTopic(t *testing.T, db *bridgeDB, settings GroupSettings, root string) {
	t.Helper()
	if err := db.UpsertChat(LarkChat{
		BridgeId: settings.BridgeID, ChatID: settings.ChatID, RootMsgID: root,
		OpenId: "ou-owner", TargetID: "target", TargetName: "worker", SessionName: "session-" + root,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeDBGroupSessionRootLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db := openGroupSettingsTestDB(t, path)
	settings := testGroupSettings()
	settings.InteractionMode, settings.IsolationRequired = config.FeishuInteractionSingleton, false
	saved, err := db.SaveGroupSettings(settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "synthetic-root"); err == nil {
		t.Fatal("missing real topic binding accepted")
	}
	for _, root := range []string{"om-first", "om-second"} {
		createGroupSessionTopic(t, db, settings, root)
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version+1, "om-first"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("stale launch = %v", err)
	}
	for range 2 {
		if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-first"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-second"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("replace active root = %v", err)
	}
	for range 2 {
		if err := db.RecordGroupSessionOrigin(settings.BridgeID, settings.ChatID, "om-alias", "om-first"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordGroupSessionOrigin(settings.BridgeID, settings.ChatID, "om-alias", "om-second"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("replace origin receipt = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openGroupSettingsTestDB(t, path)
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "om-first" {
		t.Fatalf("active pointer after reopen = %q, %v", root, err)
	}
	settings.DefaultAgent = "new/profile"
	saved, err = db.SaveGroupSettings(settings, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "om-first" {
		t.Fatalf("default edit changed root: %q, %v", root, err)
	}
	if err := db.ClearGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version-1); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("stale reset = %v", err)
	}
	if err := db.ClearGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openGroupSettingsTestDB(t, path)
	saved, err = db.ReadGroupSettings(settings.BridgeID, settings.ChatID)
	if err != nil || saved.Version != 3 {
		t.Fatalf("reset settings = %+v, %v", saved, err)
	}
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "" {
		t.Fatalf("retired pointer after reopen = %q, %v", root, err)
	}
	for _, root := range []string{"om-first", "om-alias"} {
		if got, err := db.FindGroupSessionOrigin(settings.BridgeID, settings.ChatID, root); err != nil || got != "om-first" {
			t.Fatalf("retired origin %q = %q, %v", root, got, err)
		}
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-first"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("reactivate retired root = %v", err)
	}
	if err := db.RecordGroupSessionOrigin(settings.BridgeID, settings.ChatID, "om-late", "om-first"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("late alias to retired root = %v", err)
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-second"); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version-1); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("old callback reset successor = %v", err)
	}
	settings.InteractionMode = config.FeishuInteractionThread
	saved, err = db.SaveGroupSettings(settings, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "" {
		t.Fatalf("thread mode kept singleton pointer = %q, %v", root, err)
	}
	if err := db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-second"); !errors.Is(err, errGroupSettingsChanged) {
		t.Fatalf("thread mode activated singleton = %v", err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM lark_chats WHERE status='active'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("retirement changed live topic bindings: %d, %v", count, err)
	}
	if got, err := db.FindGroupSessionOrigin("feishu:other", settings.ChatID, "om-first"); err != nil || got != "" {
		t.Fatalf("cross-Bot origin = %q, %v", got, err)
	}
	if got, err := db.FindGroupSessionOrigin(settings.BridgeID, "oc-other", "om-first"); err != nil || got != "" {
		t.Fatalf("cross-group origin = %q, %v", got, err)
	}
	settings.InteractionMode = config.FeishuInteractionSingleton
	if _, err := db.SaveGroupSettings(settings, saved.Version); err != nil {
		t.Fatal(err)
	}
	if root, err := db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || root != "" {
		t.Fatalf("return to singleton resurrected a root: %q, %v", root, err)
	}
}

func TestBridgeDBGroupSessionConcurrentActivationAndScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	dbs := []*bridgeDB{openGroupSettingsTestDB(t, path), openGroupSettingsTestDB(t, path)}
	settings := testGroupSettings()
	settings.InteractionMode, settings.IsolationRequired = config.FeishuInteractionSingleton, false
	saved, err := dbs[0].SaveGroupSettings(settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	other := settings
	other.ChatID = "oc-other"
	if _, err := dbs[0].SaveGroupSettings(other, 0); err != nil {
		t.Fatal(err)
	}
	createGroupSessionTopic(t, dbs[0], other, "om-other-group")
	if err := dbs[0].SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, "om-other-group"); err == nil {
		t.Fatal("cross-group topic binding accepted")
	}
	roots := []string{"om-contender-a", "om-contender-b"}
	for _, root := range roots {
		createGroupSessionTopic(t, dbs[0], settings, root)
	}
	type outcome struct {
		root string
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, len(dbs))
	for index, db := range dbs {
		go func(index int, db *bridgeDB) {
			<-start
			root := roots[index]
			results <- outcome{root, db.SetGroupSessionRoot(settings.BridgeID, settings.ChatID, saved.Version, root)}
		}(index, db)
	}
	close(start)
	var winner, loser string
	for range dbs {
		result := <-results
		if result.err == nil {
			if winner != "" {
				t.Fatal("two singleton activations won")
			}
			winner = result.root
		} else if errors.Is(result.err, errGroupSettingsChanged) {
			loser = result.root
		} else {
			t.Fatal(result.err)
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("activation winner=%q loser=%q", winner, loser)
	}
	if got, err := dbs[0].GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || got != winner {
		t.Fatalf("active root = %q, %v; want %q", got, err, winner)
	}
	for origin, want := range map[string]string{winner: winner, loser: ""} {
		if got, err := dbs[0].FindGroupSessionOrigin(settings.BridgeID, settings.ChatID, origin); err != nil || got != want {
			t.Fatalf("activation origin %q = %q, %v; want %q", origin, got, err, want)
		}
	}
	if err := dbs[0].SetGroupSessionRoot(other.BridgeID, other.ChatID, 1, "om-other-group"); err != nil {
		t.Fatal(err)
	}
	if got, err := dbs[0].GetGroupSessionRoot(settings.BridgeID, settings.ChatID); err != nil || got != winner {
		t.Fatalf("other group's activation changed root = %q, %v", got, err)
	}
}

func TestBridgeDBGroupDefaultAgentReferences(t *testing.T) {
	db := openGroupSettingsTestDB(t, filepath.Join(t.TempDir(), "bridge.db"))
	if _, err := db.ConfigureThreadBotDefaultAgent("feishu:bot-default", "review/bot"); err != nil {
		t.Fatal(err)
	}
	settings := testGroupSettings()
	if _, err := db.SaveGroupSettings(settings, 0); err != nil {
		t.Fatal(err)
	}
	settings.ChatID, settings.DefaultAgent = "oc-inherited", ""
	if _, err := db.SaveGroupSettings(settings, 0); err != nil {
		t.Fatal(err)
	}
	settings.ChatID, settings.DefaultAgent = "oc-unrelated", "review-other/profile"
	if _, err := db.SaveGroupSettings(settings, 0); err != nil {
		t.Fatal(err)
	}
	got, err := db.BotDefaultAgentReferences("review")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"feishu:bot-default", "feishu:group-test/oc-group"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pack references = %v; want %v", got, want)
	}
}
