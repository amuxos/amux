package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge/command"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSyncNativeSlashCommandsCreatesMissingCommands(t *testing.T) {
	t.Parallel()
	var created []nativeSlashCommandPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/application/v7/app_slash_commands":
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/application/v7/app_slash_commands":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create payload: %v", err)
			}
			assertNativeSlashIconJSONShape(t, raw)
			var payload nativeSlashCommandPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			created = append(created, payload)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]string{"command_id": "command-" + payload.Command},
			})
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient(
		"app-id",
		"app-secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
	)
	if err := SyncNativeSlashCommands(context.Background(), client, command.ManagedNativeSlashCommands()); err != nil {
		t.Fatalf("SyncNativeSlashCommands: %v", err)
	}

	desired := command.ManagedNativeSlashCommands()
	if len(created) != len(desired) {
		t.Fatalf("created %d commands, want %d: %#v", len(created), len(desired), created)
	}
	assertNativeSlashPayload(t, created[0], nativeSlashCommandPayload{
		Command: "amux",
		Description: nativeSlashCommandDescription{
			DefaultValue: "查看当前对话的 amux 状态与入口",
			I18n: map[string]string{
				"zh_cn": "查看当前对话的 amux 状态与入口",
				"en_us": "Open amux status and actions for this conversation",
			},
		},
		Icon: nativeSlashCommandIcon{IconKey: "ai-agent_outlined"},
	})
	assertNativeSlashPayload(t, created[1], nativeSlashCommandPayload{
		Command: "help",
		Description: nativeSlashCommandDescription{
			DefaultValue: "查看当前对话的 amux 使用帮助",
			I18n: map[string]string{
				"zh_cn": "查看当前对话的 amux 使用帮助",
				"en_us": "Show amux help for this conversation",
			},
		},
		Icon: nativeSlashCommandIcon{IconKey: "explanation-ai_outlined"},
	})
}

func TestSyncNativeSlashCommandsUpdatesDriftAndPreservesOtherNames(t *testing.T) {
	t.Parallel()
	var patchedPath string
	var patched nativeSlashCommandPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/application/v7/app_slash_commands":
			_, _ = w.Write([]byte(`{
				"code": 0,
				"msg": "success",
				"data": {
					"items": [
						{
							"command_id": "cmd-amux",
							"command": "amux",
							"description": {
								"default_value": "old description",
								"i18n": {"zh_cn": "旧描述", "en_us": "old description"}
							},
							"icon": {"icon_key": "skill_outlined"}
						},
						{
							"command_id": "cmd-help",
							"command": "help",
							"description": {
								"default_value": "查看当前对话的 amux 使用帮助",
								"i18n": {
									"zh_cn": "查看当前对话的 amux 使用帮助",
									"en_us": "Show amux help for this conversation"
								}
							},
							"icon": {"icon_key": "explanation-ai_outlined"}
						},
						{
							"command_id": "cmd-custom",
							"command": "custom",
							"description": {"default_value": "operator command"},
							"icon": {"icon_key": "skill_outlined"}
						}
					]
				}
			}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/open-apis/application/v7/app_slash_commands/cmd-amux":
			patchedPath = r.URL.Path
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read patch payload: %v", err)
			}
			assertNativeSlashIconJSONShape(t, raw)
			if err := json.Unmarshal(raw, &patched); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient(
		"app-id",
		"app-secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
	)
	if err := SyncNativeSlashCommands(context.Background(), client, command.ManagedNativeSlashCommands()[:2]); err != nil {
		t.Fatalf("SyncNativeSlashCommands: %v", err)
	}
	if patchedPath == "" {
		t.Fatal("SyncNativeSlashCommands did not patch the drifted amux command")
	}
	assertNativeSlashPayload(t, patched, nativeSlashCommandPayload{
		Description: nativeSlashCommandDescription{
			DefaultValue: "查看当前对话的 amux 状态与入口",
			I18n: map[string]string{
				"zh_cn": "查看当前对话的 amux 状态与入口",
				"en_us": "Open amux status and actions for this conversation",
			},
		},
		Icon: nativeSlashCommandIcon{IconKey: "ai-agent_outlined"},
	})
}

func TestSyncNativeSlashCommandsRemovesOnlyLegacyPerSkillCommands(t *testing.T) {
	t.Parallel()
	desired := command.NativeSlashCommand{
		Command: "skill", Description: "选择 Skill",
		I18n:    map[string]string{"zh_cn": "选择 Skill", "en_us": "Choose a skill"},
		IconKey: "ai-agent_outlined",
	}
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == nativeSlashCommandsPath:
			_, _ = w.Write([]byte(`{
				"code":0,"msg":"success","data":{"items":[
					{"command_id":"cmd-skill","command":"skill","description":{"default_value":"选择 Skill","i18n":{"zh_cn":"选择 Skill","en_us":"Choose a skill"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-old-skill","command":"agent_code_review","description":{"default_value":"$code-review — Review changes"},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-localized-skill","command":"agent_other","description":{"default_value":"$other — Owned elsewhere","i18n":{"zh_cn":"$other — Owned elsewhere","en_us":"$other — Owned elsewhere"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-custom","command":"custom","description":{"default_value":"$operator-owned"},"icon":{"icon_key":"skill_outlined"}}
				]}}
			`))
		case r.Method == http.MethodDelete && r.URL.Path == nativeSlashCommandsPath+"/cmd-old-skill":
			deleted = append(deleted, r.URL.Path)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	if err := SyncNativeSlashCommands(context.Background(), client, []command.NativeSlashCommand{desired}); err != nil {
		t.Fatalf("SyncNativeSlashCommands: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != nativeSlashCommandsPath+"/cmd-old-skill" {
		t.Fatalf("deleted = %+v, want only legacy skill command", deleted)
	}
}

func TestSyncNativeSlashCommandsRemovesRetiredAmuxCommandsOnly(t *testing.T) {
	t.Parallel()
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == nativeSlashCommandsPath:
			_, _ = w.Write([]byte(`{
				"code":0,"msg":"success","data":{"items":[
					{"command_id":"cmd-queue","command":"queue","description":{"default_value":"查看当前 Session 的待处理消息"},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-localized-queue","command":"queue","description":{"default_value":"查看当前 Session 的待处理消息","i18n":{"zh_cn":"查看当前 Session 的待处理消息","en_us":"Operator queue command"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-unbind","command":"unbind","description":{"default_value":"解除当前话题的 Session 绑定"},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-custom","command":"custom","description":{"default_value":"查看当前 Session 的待处理消息"},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-operator-unbind","command":"operator_unbind","description":{"default_value":"解除当前话题的 Session 绑定"},"icon":{"icon_key":"skill_outlined"}}
				]}}
			`))
		case r.Method == http.MethodDelete && (r.URL.Path == nativeSlashCommandsPath+"/cmd-queue" || r.URL.Path == nativeSlashCommandsPath+"/cmd-unbind"):
			deleted = append(deleted, r.URL.Path)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	if err := SyncNativeSlashCommands(context.Background(), client, nil); err != nil {
		t.Fatalf("SyncNativeSlashCommands: %v", err)
	}
	slices.Sort(deleted)
	want := []string{nativeSlashCommandsPath + "/cmd-queue", nativeSlashCommandsPath + "/cmd-unbind"}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %+v, want only retired amux commands %+v", deleted, want)
	}
}

func TestSyncNativeSlashCommandsRemovesExactManagedCommandsOutsideCurrentSurface(t *testing.T) {
	t.Parallel()
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == nativeSlashCommandsPath:
			_, _ = w.Write([]byte(`{
				"code":0,"msg":"success","data":{"items":[
					{"command_id":"cmd-amux","command":"amux","description":{"default_value":"查看当前对话的 amux 状态与入口","i18n":{"zh_cn":"查看当前对话的 amux 状态与入口","en_us":"Open amux status and actions for this conversation"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-help","command":"help","description":{"default_value":"查看当前对话的 amux 使用帮助","i18n":{"zh_cn":"查看当前对话的 amux 使用帮助","en_us":"Show amux help for this conversation"}},"icon":{"icon_key":"explanation-ai_outlined"}},
					{"command_id":"cmd-interrupt","command":"interrupt","description":{"default_value":"停止当前回答，保留任务","i18n":{"zh_cn":"停止当前回答，保留任务","en_us":"Interrupt the current response and keep the session"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-review","command":"review","description":{"default_value":"/review — Review changes","i18n":{"zh_cn":"/review — Review changes","en_us":"/review — Review changes"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-grill","command":"agent_grill_me","description":{"default_value":"/grill-me — Interview the user","i18n":{"zh_cn":"/grill-me — Interview the user","en_us":"/grill-me — Interview the user"}},"icon":{"icon_key":"ai-agent_outlined"}},
					{"command_id":"cmd-operator","command":"operator","description":{"default_value":"/operator — Owned elsewhere","i18n":{"zh_cn":"/operator — Owned elsewhere","en_us":"different"}},"icon":{"icon_key":"ai-agent_outlined"}}
				]}}
			`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	if err := SyncNativeSlashCommands(context.Background(), client, command.GlobalNativeSlashCommands()); err != nil {
		t.Fatalf("SyncNativeSlashCommands: %v", err)
	}
	slices.Sort(deleted)
	want := []string{
		nativeSlashCommandsPath + "/cmd-grill",
		nativeSlashCommandsPath + "/cmd-interrupt",
		nativeSlashCommandsPath + "/cmd-review",
	}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %+v, want exact stale managed commands %+v", deleted, want)
	}
}

func TestSyncNativeSlashCommandsReportsRequiredScopes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/application/v7/app_slash_commands":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":99991640,"msg":"lacks permission","data":{}}`))
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient(
		"app-id",
		"app-secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
	)
	err := SyncNativeSlashCommands(context.Background(), client, command.ManagedNativeSlashCommands())
	if err == nil {
		t.Fatal("SyncNativeSlashCommands succeeded without required scopes")
	}
	for _, want := range []string{
		"application:app_slash_command:read",
		"application:app_slash_command:write",
		"publish",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("SyncNativeSlashCommands error = %q, missing %q", err, want)
		}
	}
}

func TestObserveAvailableCommandsBuildsSessionScopedAliases(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "singleton"
`)
	b, _ := newBridgeForInputTest(t)
	b.nativeSlashSyncing = true
	if err := b.db.SetBotSession(BotSession{
		BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "target-1", TargetName: "local",
		SessionName: "session-1", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}
	raw := json.RawMessage(`[
		{"name":"amux-skill","description":"Run the skill","input_hint":"<task>"},
		{"name":"code-review","description":"Review changes","kind":"skill"},
		{"name":"review","description":"Review changes"},
		{"name":"queue","description":"Must not restore a retired amux command"},
		{"name":"status","description":"Must not shadow amux"},
		{"name":"unsafe/name","description":"Unsafe"}
	]`)
	if !b.observeAvailableCommands("target-1", "session-1", raw) {
		t.Fatal("observeAvailableCommands() = false")
	}
	if got := b.expandAdvertisedAgentCommand("target-1", "session-1", "/agent_amux_skill fix login"); got != "/amux-skill fix login" {
		t.Fatalf("expanded command = %q", got)
	}
	if got := b.expandAdvertisedAgentCommand("target-1", "session-1", "/review"); got != "/review" {
		t.Fatalf("legal command expansion = %q", got)
	}
	if _, ok := b.advertisedAgentCommandName("target-1", "session-1", "/agent_code_review"); ok {
		t.Fatal("Codex skill was exposed as a first-level native command")
	}
	skills := b.advertisedSkills("target-1", "session-1")
	if len(skills) != 1 || skills[0].Name != "code-review" {
		t.Fatalf("advertised skills = %+v, want code-review", skills)
	}
	if _, ok := b.advertisedAgentCommandName("target-1", "session-1", "/status"); ok {
		t.Fatal("static command collision was advertised")
	}
	if _, ok := b.advertisedAgentCommandName("target-1", "session-1", "/queue"); ok {
		t.Fatal("retired amux command was advertised by an agent")
	}
	if _, ok := b.advertisedAgentCommandName("target-2", "session-1", "/agent_amux_skill"); ok {
		t.Fatal("agent alias leaked into another target session")
	}
	desired := b.desiredNativeSlashCommands()
	found := false
	for _, command := range desired {
		if command.Command == "agent_amux_skill" {
			found = strings.Contains(command.Description, "/amux-skill") && strings.Contains(command.Description, "<task>")
		}
		if command.Command == "agent_code_review" {
			t.Fatalf("Codex skill leaked into application-level commands: %+v", command)
		}
	}
	if !found {
		t.Fatalf("desired commands missing skill alias: %+v", desired)
	}

	// Each notification is a full session snapshot. The current singleton menu
	// and invocation cache both drop commands that are no longer advertised.
	b.observeAvailableCommands("target-1", "session-1", json.RawMessage(`[]`))
	if _, ok := b.advertisedAgentCommandName("target-1", "session-1", "/agent_amux_skill"); ok {
		t.Fatal("removed command remained invokable in the session")
	}
	if skills := b.advertisedSkills("target-1", "session-1"); len(skills) != 0 {
		t.Fatalf("removed skills remained selectable: %+v", skills)
	}
	for _, command := range b.desiredNativeSlashCommands() {
		if command.Command == "agent_amux_skill" {
			t.Fatalf("removed singleton command remained in native menu: %+v", command)
		}
	}
}

func TestClearAdvertisedCommandsRemovesCurrentSingletonProjection(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "singleton"
`)
	b, _ := newBridgeForInputTest(t)
	b.nativeSlashSyncing = true
	if err := b.db.SetBotSession(BotSession{
		BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "target-1", TargetName: "local",
		SessionName: "session-1", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}
	b.observeAvailableCommands("target-1", "session-1", json.RawMessage(`[{"name":"review","description":"Review changes"}]`))
	if got := nativeSlashCommandNames(b.desiredNativeSlashCommands()); !slices.Contains(got, "review") {
		t.Fatalf("desired commands = %#v, want review before clear", got)
	}

	b.clearAdvertisedAgentCommands("target-1", "session-1")
	if got := nativeSlashCommandNames(b.desiredNativeSlashCommands()); slices.Contains(got, "review") {
		t.Fatalf("desired commands = %#v, stale review remained after clear", got)
	}
}

func TestDesiredNativeSlashCommandsFollowInteractionModeAndSingletonBinding(t *testing.T) {
	t.Run("thread only exposes global commands", func(t *testing.T) {
		b, _ := newBridgeForInputTest(t)
		b.nativeSlashSyncing = true
		b.observeAvailableCommands("target-1", "thread-session", json.RawMessage(`[{"name":"review","description":"Review changes"}]`))
		if got, want := nativeSlashCommandNames(b.desiredNativeSlashCommands()), []string{"amux", "help"}; !slices.Equal(got, want) {
			t.Fatalf("thread native slash commands = %#v, want %#v", got, want)
		}
		if got := b.expandAdvertisedAgentCommand("target-1", "thread-session", "/review"); got != "/review" {
			t.Fatalf("thread session command cache expansion = %q, want /review", got)
		}
	})

	t.Run("singleton exposes current bound session only", func(t *testing.T) {
		loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "singleton"
`)
		b, _ := newBridgeForInputTest(t)
		b.nativeSlashSyncing = true
		if err := b.db.SetBotSession(BotSession{
			BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "target-1", TargetName: "local",
			SessionName: "singleton-session", AgentType: "codex",
		}); err != nil {
			t.Fatalf("SetBotSession: %v", err)
		}
		b.observeAvailableCommands("target-2", "other-session", json.RawMessage(`[{"name":"other","description":"Other"}]`))
		b.observeAvailableCommands("target-1", "singleton-session", json.RawMessage(`[{"name":"review","description":"Review changes"}]`))
		want := []string{"amux", "help", "status", "restart", "interrupt", "skill", "compact", "clear", "review"}
		if got := nativeSlashCommandNames(b.desiredNativeSlashCommands()); !slices.Equal(got, want) {
			t.Fatalf("singleton native slash commands = %#v, want %#v", got, want)
		}
	})
}

func TestBotSessionModeTransitionsScheduleNativeSlashCommandReconciliation(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "thread"
`)
	var mu sync.Mutex
	listCalls := 0
	createdByPass := map[int][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == nativeSlashCommandsPath:
			mu.Lock()
			listCalls++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == nativeSlashCommandsPath:
			var payload nativeSlashCommandPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			mu.Lock()
			createdByPass[listCalls] = append(createdByPass[listCalls], payload.Command)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"command_id":"created"}}`))
		default:
			t.Errorf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	b, _ := newBridgeForInputTest(t)
	b.client = lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	if err := b.BindBotSession(BotSession{
		Enabled: true, TargetID: "runtime-1", TargetName: "local", SessionName: "bot-a-main",
		AgentProfile: "builtin/codex", AgentType: "codex",
	}, "owner-open"); err != nil {
		t.Fatalf("BindBotSession: %v", err)
	}
	waitForNativeSlashSync(t, b, 1)

	mu.Lock()
	firstPass := append([]string(nil), createdByPass[1]...)
	mu.Unlock()
	if got, want := firstPass, []string{"amux", "help", "status", "restart", "interrupt", "skill", "compact", "clear"}; !slices.Equal(got, want) {
		t.Fatalf("singleton reconciliation created %#v, want %#v", got, want)
	}

	if err := b.DisableBotSession(); err != nil {
		t.Fatalf("DisableBotSession: %v", err)
	}
	waitForNativeSlashSync(t, b, 2)
	mu.Lock()
	secondPass := append([]string(nil), createdByPass[2]...)
	mu.Unlock()
	if got, want := secondPass, []string{"amux", "help"}; !slices.Equal(got, want) {
		t.Fatalf("thread reconciliation created %#v, want %#v", got, want)
	}
}

func TestSingletonSessionIdentityChangeSchedulesNativeSlashCommandReconciliation(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "singleton"
`)
	var listCalls atomic.Int32
	var mu sync.Mutex
	var created []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == nativeSlashCommandsPath:
			listCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == nativeSlashCommandsPath:
			var payload nativeSlashCommandPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			mu.Lock()
			created = append(created, payload.Command)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"command_id":"created"}}`))
		default:
			t.Errorf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	b, hub := newBridgeForInputTest(t)
	b.client = lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	if err := b.db.SetBotSession(BotSession{
		BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "runtime-1", TargetName: "local",
		SessionName: "bot-a-main", AgentProfile: "builtin/codex", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}
	hub.targets = []*pb.Target{{Id: "runtime-1", Name: "local", Sessions: []*pb.Session{{
		Name: "bot-a-main", State: "running", AgentProfile: "builtin/codex", AgentType: "codex",
	}}}}
	b.OnSessionAdded("runtime-1", "local", hub.targets[0].Sessions[0])

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && listCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if listCalls.Load() == 0 {
		t.Fatal("session identity change did not schedule native slash reconciliation")
	}
	waitForNativeSlashSync(t, b, 1)
	mu.Lock()
	got := append([]string(nil), created...)
	mu.Unlock()
	want := []string{"amux", "help", "status", "restart", "interrupt", "skill", "compact", "clear"}
	if !slices.Equal(got, want) {
		t.Fatalf("identity reconciliation created %#v, want %#v", got, want)
	}
}

func waitForNativeSlashSync(t *testing.T, b *Bridge, wantListCalls int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b.nativeSlashMu.Lock()
		syncing := b.nativeSlashSyncing
		b.nativeSlashMu.Unlock()
		if !syncing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native slash sync %d did not complete", wantListCalls)
}

func nativeSlashCommandNames(commands []command.NativeSlashCommand) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Command)
	}
	return names
}

func TestNativeAgentCommandAliasIsInjectiveAndRejectsUnsafeNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{name: "review", want: "review", ok: true},
		{name: "amux-skill", want: "agent_amux_skill", ok: true},
		{name: "a-_b", want: "agent_a___b", ok: true},
		{name: "skill/name", ok: false},
		{name: "技能", ok: false},
	}
	for _, tt := range tests {
		got, ok := nativeAgentCommandAlias(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("nativeAgentCommandAlias(%q) = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestStartNativeSlashCommandSyncRunsOnceAndFailsOpen(t *testing.T) {
	t.Parallel()
	var listCalls atomic.Int32
	listStarted := make(chan struct{}, 2)
	listCompleted := make(chan struct{}, 2)
	releaseList := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/application/v7/app_slash_commands":
			listCalls.Add(1)
			listStarted <- struct{}{}
			<-releaseList
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":99991640,"msg":"lacks permission","data":{}}`))
			listCompleted <- struct{}{}
		default:
			t.Fatalf("unexpected Lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	b := &Bridge{
		client: lark.NewClient(
			"app-id",
			"app-secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
		profile: "default",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.startNativeSlashCommandSync(ctx)
	b.startNativeSlashCommandSync(ctx)

	select {
	case <-listStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("native slash command sync did not start")
	}
	close(releaseList)
	select {
	case <-listCompleted:
	case <-time.After(2 * time.Second):
		t.Fatal("native slash command sync did not complete after permission error")
	}
	select {
	case <-listStarted:
		t.Fatal("native slash command sync ran more than once")
	case <-time.After(50 * time.Millisecond):
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("native slash command list calls = %d, want 1", got)
	}
}

func assertNativeSlashPayload(t *testing.T, got, want nativeSlashCommandPayload) {
	t.Helper()
	if got.Command != want.Command ||
		got.Description.DefaultValue != want.Description.DefaultValue ||
		got.Description.I18n["zh_cn"] != want.Description.I18n["zh_cn"] ||
		got.Description.I18n["en_us"] != want.Description.I18n["en_us"] ||
		len(got.Description.I18n) != len(want.Description.I18n) ||
		got.Icon.IconKey != want.Icon.IconKey {
		t.Fatalf("native slash payload = %#v, want %#v", got, want)
	}
}

func assertNativeSlashIconJSONShape(t *testing.T, raw []byte) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode native slash payload shape: %v", err)
	}
	if _, ok := body["icon"]; !ok {
		t.Fatalf("native slash payload has no top-level icon: %s", raw)
	}
	var description map[string]json.RawMessage
	if err := json.Unmarshal(body["description"], &description); err != nil {
		t.Fatalf("decode native slash description shape: %v", err)
	}
	if _, ok := description["icon"]; ok {
		t.Fatalf("native slash payload incorrectly nests icon under description: %s", raw)
	}
}
